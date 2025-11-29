// Copyright 2025 openGemini Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package subcmd

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openGemini/openGemini-cli/core"
	"github.com/openGemini/opengemini-client-go/opengemini"
	"github.com/vbauerster/mpb/v7"
)

type ExportConfig struct {
	*core.CommandLineConfig
	Export            bool
	Format            string `json:"format"`
	Out               string `json:"out"`
	DataDir           string `json:"data"`
	WalDir            string `json:"wal"`
	Remote            string `json:"remote"`
	RemoteUsername    string `json:"-"`
	RemotePassword    string `json:"-"`
	RemoteSsl         bool   `json:"remotessl"`
	DBFilter          string `json:"dbfilter"`
	RetentionFilter   string `json:"retentionfilter"`
	MeasurementFilter string `json:"mstfilter"`
	TimeFilter        string `json:"timefilter"`
	Compress          bool   `json:"compress"`
	Resume            bool
}

type ExportCommand struct {
	cfg       *ExportConfig
	exportCmd *Exporter
}

type Exporter struct {
	exportFormat      string              //nolint:unused,structcheck // used in offline mode
	databaseDiskInfos []*DatabaseDiskInfo //nolint:unused,structcheck // used in offline mode
	filesTotalCount   int
	actualDataPath    string      //nolint:unused,structcheck // used in offline mode
	actualWalPath     string      //nolint:unused,structcheck // used in offline mode
	outPutPath        string      //nolint:unused,structcheck // used in offline mode
	filter            *dataFilter //nolint:unused,structcheck // used in offline mode
	compress          bool        //nolint:unused,structcheck // used in offline mode
	lineCount         uint64
	resume            bool
	progress          map[string]struct{}
	remote            string          //nolint:unused,structcheck // used in offline mode
	remoteExporter    *remoteExporter //nolint:unused,structcheck // used in offline mode
	parser

	stderrLogger  *log.Logger
	stdoutLogger  *log.Logger
	defaultLogger *log.Logger

	manifest                        map[string]struct{}
	rpNameToMeasurementTsspFilesMap map[string]map[string][]string
	rpNameToIdToIndexMap            map[string]map[uint64]interface{} // 在offline模式下为*tsi.MergeSetIndex，在online模式下为nil
	rpNameToWalFilesMap             map[string][]string

	Stderr io.Writer
	Stdout io.Writer
	bar    *mpb.Bar
}

type DatabaseDiskInfo struct { //nolint:unused // used in offline mode
	dbName          string
	rps             map[string]struct{}
	dataDir         string
	walDir          string
	rpToTsspDirMap  map[string]string
	rpToWalDirMap   map[string]string
	rpToIndexDirMap map[string]string
}

func (d *DatabaseDiskInfo) init(actualDataDir string, actualWalDir string, databaseName string, retentionPolicy string) error {
	d.dbName = databaseName

	// check whether the database is in actualDataPath
	dataDir := filepath.Join(actualDataDir, databaseName)
	if _, err := os.Stat(dataDir); err != nil {
		return err
	}
	// check whether the database is in actualWalPath
	walDir := filepath.Join(actualWalDir, databaseName)
	if _, err := os.Stat(walDir); err != nil {
		return err
	}

	// ie. /tmp/openGemini/data/data/my_db  /tmp/openGemini/data/wal/my_db
	d.dataDir, d.walDir = dataDir, walDir

	ptDirs, err := os.ReadDir(d.dataDir)
	if err != nil {
		return err
	}
	for _, ptDir := range ptDirs {
		// ie. /tmp/openGemini/data/data/my_db/0
		ptTsspPath := filepath.Join(d.dataDir, ptDir.Name())
		// ie. /tmp/openGemini/data/wal/my_db/0
		ptWalPath := filepath.Join(d.walDir, ptDir.Name())

		if retentionPolicy != "" {
			ptWithRp := ptDir.Name() + ":" + retentionPolicy
			// ie. /tmp/openGemini/data/data/my_db/0/autogen
			rpTsspPath := filepath.Join(ptTsspPath, retentionPolicy)
			if _, err := os.Stat(rpTsspPath); err != nil {
				return fmt.Errorf("retention policy %q invalid : %s", retentionPolicy, err)
			} else {
				d.rps[ptWithRp] = struct{}{}
				d.rpToTsspDirMap[ptWithRp] = rpTsspPath
				d.rpToIndexDirMap[ptWithRp] = filepath.Join(rpTsspPath, "index")
			}
			// ie. /tmp/openGemini/data/wal/my_db/0/autogen
			rpWalPath := filepath.Join(ptWalPath, retentionPolicy)
			if _, err := os.Stat(rpWalPath); err != nil {
				return fmt.Errorf("retention policy %q invalid : %s", retentionPolicy, err)
			} else {
				d.rpToWalDirMap[ptWithRp] = rpWalPath
			}
			continue
		}

		rpTsspDirs, err1 := os.ReadDir(ptTsspPath)
		if err1 != nil {
			return err1
		}
		for _, rpDir := range rpTsspDirs {
			if !rpDir.IsDir() {
				continue
			}
			ptWithRp := ptDir.Name() + ":" + rpDir.Name()
			rpPath := filepath.Join(ptTsspPath, rpDir.Name())
			d.rps[ptWithRp] = struct{}{}
			d.rpToTsspDirMap[ptWithRp] = rpPath
			d.rpToIndexDirMap[ptWithRp] = filepath.Join(rpPath, "index")
		}

		rpWalDirs, err2 := os.ReadDir(ptWalPath)
		if err2 != nil {
			return err2
		}
		for _, rpDir := range rpWalDirs {
			ptWithRp := ptDir.Name() + ":" + rpDir.Name()
			if !rpDir.IsDir() {
				continue
			}
			rpPath := filepath.Join(ptWalPath, rpDir.Name())
			d.rpToWalDirMap[ptWithRp] = rpPath
		}
	}
	return nil
}

type dataFilter struct { //nolint:unused // used in offline mode
	database    string //nolint:unused // used in offline mode
	retention   string //nolint:unused // used in offline mode
	measurement string
	startTime   int64
	endTime     int64
}

func (d *dataFilter) isBelowMinTimeFilter(t int64) bool { //nolint:unused // used in offline mode
	return t < d.startTime
}

func (d *dataFilter) isAboveMaxTimeFilter(t int64) bool { //nolint:unused // used in offline mode
	return t > d.endTime
}

func newDataFilter() *dataFilter { //nolint:unused // used in offline mode
	return &dataFilter{
		database:    "",
		measurement: "",
		startTime:   math.MinInt64,
		endTime:     math.MaxInt64,
	}
}

func newDatabaseDiskInfo() *DatabaseDiskInfo { //nolint:unused // used in offline mode
	return &DatabaseDiskInfo{
		rps:             make(map[string]struct{}),
		rpToTsspDirMap:  make(map[string]string),
		rpToWalDirMap:   make(map[string]string),
		rpToIndexDirMap: make(map[string]string),
	}
}

func newRemoteExporter() *remoteExporter {
	return &remoteExporter{
		isExist: false,
	}
}

func NewExporter() *Exporter {
	return &Exporter{
		resume:   false,
		progress: make(map[string]struct{}),

		stderrLogger: log.New(os.Stderr, "export: ", log.LstdFlags),
		stdoutLogger: log.New(os.Stdout, "export: ", log.LstdFlags),

		manifest:                        make(map[string]struct{}),
		rpNameToMeasurementTsspFilesMap: make(map[string]map[string][]string),
		rpNameToIdToIndexMap:            make(map[string]map[uint64]interface{}),
		rpNameToWalFilesMap:             make(map[string][]string),
		remoteExporter:                  newRemoteExporter(),

		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
}

type remoteExporter struct {
	isExist         bool
	client          opengemini.Client
	database        string              //nolint:unused // used in offline mode
	retentionPolicy string              //nolint:unused // used in offline mode
	points          []*opengemini.Point //nolint:unused // used in offline mode
}

type parser interface {
	parse2SeriesKeyWithoutVersion(key []byte, dst []byte, splitWithNull bool, point *opengemini.Point) ([]byte, error)
	appendFields(rec interface{}, buf []byte, point *opengemini.Point) ([]byte, error)
	writeMstInfoFromTssp(metaWriter io.Writer, outputWriter io.Writer, filePath string, isOrder bool, index interface{}) error
	writeMstInfoFromWal(metaWriter io.Writer, outputWriter io.Writer, row interface{}, curDatabase string) error
	writeMetaInfo(metaWriter io.Writer, infoType InfoType, info string)
	writeOutputInfo(outputWriter io.Writer, info string)
	getRowBuf(buf []byte, measurementName string, row interface{}, point *opengemini.Point) ([]byte, error)
}

type InfoType int

const (
	InfoTypeDatabase InfoType = 1 + iota
	InfoTypeRetentionPolicy
	InfoTypeMeasurement
)

const (
	tsspFileExtension    = "tssp"
	walFileExtension     = "wal"
	csvFormatExporter    = "csv"
	txtFormatExporter    = "txt"
	remoteFormatExporter = "remote"
	resumeFilePrefix     = "resume_"
)

// Global variables for progress tracking
var (
	MpbProgress         = mpb.New(mpb.WithWidth(100))
	ResumeJsonPath      string
	ProgressedFilesPath string
)

// Progress management functions

func getResumeConfig(options *ExportConfig) (*ExportConfig, error) {
	jsonData, err := os.ReadFile(ResumeJsonPath)
	if err != nil {
		return nil, err
	}
	var config ExportConfig
	err = json.Unmarshal(jsonData, &config)
	if err != nil {
		return nil, err
	}
	config.Resume = true
	config.RemoteUsername = options.RemoteUsername
	config.RemotePassword = options.RemotePassword
	return &config, nil
}

func getProgressedFiles() (map[string]struct{}, error) {
	file, err := os.Open(ProgressedFilesPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineSet := make(map[string]struct{})

	for scanner.Scan() {
		line := scanner.Text()
		lineSet[line] = struct{}{}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lineSet, nil
}

// CreateNewProgressFolder init ResumeJsonPath and ProgressedFilesPath
func CreateNewProgressFolder() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	targetPath := filepath.Join(home, ".ts-cli", time.Now().Format("2006-01-02_15-04-05.000000000"))
	err = os.MkdirAll(targetPath, os.ModePerm)
	if err != nil {
		return err
	}
	// create progress.json
	progressJson := filepath.Join(targetPath, "progress.json")
	ResumeJsonPath = progressJson
	// create progressedFiles
	progressedFiles := filepath.Join(targetPath, "progressedFiles")
	ProgressedFilesPath = progressedFiles
	return nil
}

// ReadLatestProgressFile reads and processes the latest folder
func ReadLatestProgressFile() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	baseDir := filepath.Join(home, ".ts-cli")
	var dirs []string
	err = filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() || path == baseDir {
			return nil
		}
		dirs = append(dirs, path)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(dirs)
	latestDir := dirs[len(dirs)-1]
	// read progress.json
	ResumeJsonPath = filepath.Join(latestDir, "progress.json")
	// read progressedFiles
	ProgressedFilesPath = filepath.Join(latestDir, "progressedFiles")
	return nil
}

// writeProgressJson writes progress to json file
func (e *Exporter) writeProgressJson(clc *ExportConfig) error {
	output, err := json.MarshalIndent(clc, "", "\t")
	if err != nil {
		return err
	}
	err = os.WriteFile(ResumeJsonPath, output, 0644)
	if err != nil {
		return err
	}
	return nil
}

// writeProgressedFiles writes progressed file name
func (e *Exporter) writeProgressedFiles(filename string) error {
	file, err := os.OpenFile(ProgressedFilesPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(filename + "\n")
	if err != nil {
		return err
	}
	return nil
}

// Escape utility functions and variables
var escapeFieldKeyReplacer = strings.NewReplacer(`,`, `\,`, `=`, `\=`, ` `, `\ `)
var escapeTagKeyReplacer = strings.NewReplacer(`,`, `\,`, `=`, `\=`, ` `, `\ `)
var escapeTagValueReplacer = strings.NewReplacer(`,`, `\,`, `=`, `\=`, ` `, `\ `)
var escapeMstNameReplacer = strings.NewReplacer(`=`, `\=`, ` `, `\ `)
var escapeStringFieldReplacer = strings.NewReplacer(`"`, `\"`, `\`, `\\`)

// EscapeFieldKey returns a copy of in with any comma or equal sign or space
// with escaped values.
func EscapeFieldKey(in string) string {
	return escapeFieldKeyReplacer.Replace(in)
}

// EscapeStringFieldValue returns a copy of in with any double quotes or
// backslashes with escaped values.
func EscapeStringFieldValue(in string) string {
	return escapeStringFieldReplacer.Replace(in)
}

// EscapeTagKey returns a copy of in with any "comma" or "equal sign" or "space"
// with escaped values.
func EscapeTagKey(in string) string {
	return escapeTagKeyReplacer.Replace(in)
}

// EscapeTagValue returns a copy of in with any "comma" or "equal sign" or "space"
// with escaped values
func EscapeTagValue(in string) string {
	return escapeTagValueReplacer.Replace(in)
}

// EscapeMstName returns a copy of in with any "equal sign" or "space"
// with escaped values.
func EscapeMstName(in string) string {
	return escapeMstNameReplacer.Replace(in)
}

// Helper utility functions

func convertTime(input string) (int64, error) { //nolint:unused // used in offline mode
	t, err := time.Parse(time.RFC3339, input)
	if err == nil {
		return t.UnixNano(), nil
	}

	timestamp, err := strconv.ParseInt(input, 10, 64)
	if err == nil {
		return timestamp, nil
	}

	return 0, err
}

// dataFilter methods

func (d *dataFilter) parseTime(clc *ExportConfig) error { //nolint:unused // used in offline mode
	var start, end string
	timeSlot := strings.Split(clc.TimeFilter, "~")
	if len(timeSlot) == 2 {
		start = timeSlot[0]
		end = timeSlot[1]
	} else if clc.TimeFilter != "" {
		return fmt.Errorf("invalid time filter %q", clc.TimeFilter)
	}

	if start != "" {
		st, err := convertTime(start)
		if err != nil {
			return err
		}
		d.startTime = st
	}

	if end != "" {
		ed, err := convertTime(end)
		if err != nil {
			return err
		}
		d.endTime = ed
	}

	if d.startTime > d.endTime {
		return fmt.Errorf("start time `%q` > end time `%q`", start, end)
	}

	return nil
}

func (d *dataFilter) parseDatabase(dbFilter string) { //nolint:unused // used in offline mode
	if dbFilter == "" {
		return
	}
	d.database = dbFilter
}

func (d *dataFilter) parseRetention(retentionFilter string) { //nolint:unused // used in offline mode
	if retentionFilter == "" {
		return
	}
	d.retention = retentionFilter
}

func (d *dataFilter) parseMeasurement(mstFilter string) error { //nolint:unused // used in offline mode
	if mstFilter == "" {
		return nil
	}
	if mstFilter != "" && d.database == "" {
		return fmt.Errorf("measurement filter %q requires database filter", mstFilter)
	}
	d.measurement = mstFilter
	return nil
}

// timeFilter [startTime, endTime]
func (d *dataFilter) timeFilter(t int64) bool { //nolint:unused // used in offline mode
	return t >= d.startTime && t <= d.endTime
}

// remoteExporter methods

func (re *remoteExporter) Init(clc *ExportConfig) error {
	if len(clc.Remote) == 0 {
		return fmt.Errorf("execute -export cmd, using remote format, --remote is required")
	}
	h, p, err := net.SplitHostPort(clc.Remote)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return fmt.Errorf("invalid port number :%s", err)
	}
	var authConfig *opengemini.AuthConfig
	if clc.RemoteUsername != "" {
		authConfig = &opengemini.AuthConfig{
			AuthType: 0,
			Username: clc.RemoteUsername,
			Password: clc.RemotePassword,
		}
	} else {
		authConfig = nil
	}
	var remoteConfig *opengemini.Config
	if clc.RemoteSsl {
		remoteConfig = &opengemini.Config{
			Addresses: []opengemini.Address{
				{
					Host: h,
					Port: port,
				},
			},
			AuthConfig: authConfig,
			TlsConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		}
	} else {
		remoteConfig = &opengemini.Config{
			Addresses: []opengemini.Address{
				{
					Host: h,
					Port: port,
				},
			},
			AuthConfig: authConfig,
		}
	}

	cli, err := opengemini.NewClient(remoteConfig)
	if err != nil {
		return err
	}
	re.isExist = true
	re.client = cli
	if err = re.client.Ping(0); err != nil {
		return err
	}
	return nil
}

func (re *remoteExporter) createDatabase(dbName string) error { //nolint:unused // used in offline mode
	err := re.client.CreateDatabase(dbName)
	if err != nil {
		return fmt.Errorf("error writing command: %s", err)
	}
	return nil
}

func (re *remoteExporter) createRetentionPolicy(dbName string, rpName string) error { //nolint:unused // used in offline mode
	err := re.client.CreateRetentionPolicy(dbName, opengemini.RpConfig{
		Name:     rpName,
		Duration: "0s",
	}, false)
	if err != nil {
		return fmt.Errorf("error writing command: %s", err)
	}
	return nil
}

func (re *remoteExporter) writeAllPoints() error { //nolint:unused // used in offline mode
	err := re.client.WriteBatchPointsWithRp(context.Background(), re.database, re.retentionPolicy, re.points)
	if err != nil {
		return err
	}
	re.points = re.points[:0]
	return nil
}
