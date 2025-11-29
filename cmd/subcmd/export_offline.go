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

//go:build full
// +build full

package subcmd

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/encoding"
	"github.com/klauspost/compress/snappy"
	influx "github.com/openGemini/openGemini-cli/lib/influxparser"
	"github.com/openGemini/openGemini/engine"
	"github.com/openGemini/openGemini/engine/immutable"
	"github.com/openGemini/openGemini/engine/index/tsi"
	"github.com/openGemini/openGemini/lib/bufferpool"
	"github.com/openGemini/openGemini/lib/config"
	"github.com/openGemini/openGemini/lib/errno"
	"github.com/openGemini/openGemini/lib/fileops"
	"github.com/openGemini/openGemini/lib/index"
	"github.com/openGemini/openGemini/lib/record"
	"github.com/openGemini/openGemini/lib/util"
	"github.com/openGemini/opengemini-client-go/opengemini"
	"github.com/vbauerster/mpb/v7"
	"github.com/vbauerster/mpb/v7/decor"
)

// Helper functions for offline mode

const (
	dirNameSeparator = "_"
)

func parseShardDir(shardDirName string) (uint64, int64, int64, uint64, error) {
	shardDir := strings.Split(shardDirName, dirNameSeparator)
	if len(shardDir) != 4 {
		return 0, 0, 0, 0, errno.NewError(errno.InvalidDataDir)
	}
	shardID, err := strconv.ParseUint(shardDir[0], 10, 64)
	if err != nil {
		return 0, 0, 0, 0, errno.NewError(errno.InvalidDataDir)
	}
	dirStartTime, err := strconv.ParseInt(shardDir[1], 10, 64)
	if err != nil {
		return 0, 0, 0, 0, errno.NewError(errno.InvalidDataDir)
	}
	dirEndTime, err := strconv.ParseInt(shardDir[2], 10, 64)
	if err != nil {
		return 0, 0, 0, 0, errno.NewError(errno.InvalidDataDir)
	}
	indexID, err := strconv.ParseUint(shardDir[3], 10, 64)
	if err != nil {
		return 0, 0, 0, 0, errno.NewError(errno.InvalidDataDir)
	}
	return shardID, dirStartTime, dirEndTime, indexID, nil
}

func parseIndexDir(indexDirName string) (uint64, error) {
	indexDir := strings.Split(indexDirName, dirNameSeparator)
	if len(indexDir) != 3 {
		return 0, errno.NewError(errno.InvalidDataDir)
	}

	indexID, err := strconv.ParseUint(indexDir[0], 10, 64)
	if err != nil {
		return 0, errno.NewError(errno.InvalidDataDir)
	}
	return indexID, nil
}

func getFieldNameIndexFromRecord(slice []record.Field, str string) (int, bool) {
	for i, v := range slice {
		if v.Name == str {
			return i, true
		}
	}
	return 0, false
}

func getFieldNameIndexFromRowOffline(slice []influx.Field, str string) (int, bool) {
	for i, v := range slice {
		if v.Key == str {
			return i, true
		}
	}
	return 0, false
}

// Init inits the Exporter instance for offline mode
func (e *Exporter) Init(clc *ExportConfig, progressedFiles map[string]struct{}) error {
	if clc.Format == "" {
		return fmt.Errorf("export flag format is required")
	}
	if clc.DataDir == "" {
		return fmt.Errorf("export flag data is required")
	}
	if clc.WalDir == "" {
		return fmt.Errorf("export flag wal is required")
	}
	if clc.DBFilter == "" {
		return fmt.Errorf("export flag dbfilter is required")
	}
	if clc.Format != csvFormatExporter && clc.Format != txtFormatExporter && clc.Format != remoteFormatExporter {
		return fmt.Errorf("unsupported export format %q", clc.Format)
	}
	if clc.Format != remoteFormatExporter && clc.Out == "" {
		return fmt.Errorf("execute -export cmd, not using remote format, --out is required")
	}
	if clc.Format == remoteFormatExporter {
		if err := e.remoteExporter.Init(clc); err != nil {
			return err
		}
	}
	e.exportFormat = clc.Format
	if e.exportFormat == txtFormatExporter || e.exportFormat == remoteFormatExporter {
		e.parser = newOfflineTxtParser()
	} else if e.exportFormat == csvFormatExporter {
		e.parser = newOfflineCsvParser()
	}
	e.outPutPath = clc.Out
	e.compress = clc.Compress
	e.remote = clc.Remote
	e.defaultLogger = e.stdoutLogger
	if clc.Resume {
		e.resume = true
		e.progress = progressedFiles
		e.defaultLogger.Printf("starting resume export file, you have exported %d files \n", len(e.progress))
	}
	if err := e.writeProgressJson(clc); err != nil {
		return err
	}
	// filter db, mst, time
	e.filter = newDataFilter()
	e.filter.parseDatabase(clc.DBFilter)
	e.filter.parseRetention(clc.RetentionFilter)
	if err := e.filter.parseTime(clc); err != nil {
		return err
	}
	if err := e.filter.parseMeasurement(clc.MeasurementFilter); err != nil {
		return err
	}
	// ie. dataDir=/tmp/openGemini/data               walDir=/tmp/openGemini/data
	//     actualDataPath=/tmp/openGemini/data/data    actualWalPath=/tmp/openGemini/data/wal
	if err := e.parseActualDir(clc); err != nil {
		return err
	}

	// Get all dir infos that we need,like all database/rp/tsspDirs and database/rp/walDirs
	if err := e.parseDatabaseInfos(); err != nil {
		return err
	}

	return nil
}

// Export exports all data user want in offline mode.
func (e *Exporter) Export(clc *ExportConfig, progressedFiles map[string]struct{}) error {
	err := e.Init(clc, progressedFiles)
	if err != nil {
		return err
	}
	for _, dbDiskInfo := range e.databaseDiskInfos {
		err = e.walkDatabase(dbDiskInfo)
		if err != nil {
			return err
		}
	}
	e.bar, err = e.newBar()
	if err != nil {
		return err
	}
	return e.write()
}

// Run executes the export command in offline mode
func (c *ExportCommand) runOfflineExport(config *ExportConfig) error {
	if err := flag.CommandLine.Parse([]string{"-loggerLevel=ERROR"}); err != nil {
		return err
	}
	c.cfg = config
	c.exportCmd = NewExporter()

	return c.processOffline()
}

// process handles the export process in offline mode
func (c *ExportCommand) processOffline() error {
	useOffline := (c.cfg.DataDir != "" || c.cfg.WalDir != "")

	if c.cfg.Resume {
		if err := ReadLatestProgressFile(); err != nil {
			return err
		}
		oldConfig, err := getResumeConfig(c.cfg)
		if err != nil {
			return err
		}
		progressedFiles, err := getProgressedFiles()
		if err != nil {
			return err
		}

		isOnlineResume := false
		for path := range progressedFiles {
			if strings.HasPrefix(path, "online://") {
				isOnlineResume = true
				break
			}
		}

		if isOnlineResume {
			return fmt.Errorf("cannot resume online export in offline mode. Please use online mode to resume")
		} else {
			return c.exportCmd.Export(oldConfig, progressedFiles)
		}
	} else {
		if err := CreateNewProgressFolder(); err != nil {
			return err
		}

		if useOffline {
			return c.exportCmd.Export(c.cfg, nil)
		} else {
			return fmt.Errorf("offline mode requires --data and --wal flags")
		}
	}
}

// parseActualDir transforms user puts in datadir and waldir to actual dirs
func (e *Exporter) parseActualDir(clc *ExportConfig) error {
	actualDataDir := filepath.Join(clc.DataDir, config.DataDirectory)
	if _, err := os.Stat(actualDataDir); err != nil {
		return err
	} else {
		e.actualDataPath = actualDataDir
	}

	actualWalDir := filepath.Join(clc.WalDir, config.WalDirectory)
	if _, err := os.Stat(actualWalDir); err != nil {
		return err
	} else {
		e.actualWalPath = actualWalDir
	}

	return nil
}

// parseDatabaseInfos get all path infos for export.
func (e *Exporter) parseDatabaseInfos() error {
	dbName := e.filter.database

	dbDiskInfo := newDatabaseDiskInfo()
	err := dbDiskInfo.init(e.actualDataPath, e.actualWalPath, dbName, e.filter.retention)
	if err != nil {
		return fmt.Errorf("can't find database files for %s : %s", dbName, err)
	}
	e.databaseDiskInfos = append(e.databaseDiskInfos, dbDiskInfo)
	return nil
}

// walkDatabase gets all db's tssp filepath, wal filepath, and index filepath.
func (e *Exporter) walkDatabase(dbDiskInfo *DatabaseDiskInfo) error {
	if err := e.walkTsspFile(dbDiskInfo); err != nil {
		return err
	}
	if err := e.walkIndexFiles(dbDiskInfo); err != nil {
		return err
	}
	if err := e.walkWalFile(dbDiskInfo); err != nil {
		return err
	}
	return nil
}

// walkTsspFile walk all tssp files for every database.
func (e *Exporter) walkTsspFile(dbDiskInfo *DatabaseDiskInfo) error {
	for ptWithRp := range dbDiskInfo.rps {
		rpDir := dbDiskInfo.rpToTsspDirMap[ptWithRp]
		if err := filepath.Walk(rpDir, func(path string, info fs.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if filepath.Ext(path) != "."+tsspFileExtension {
				return nil
			}
			// search .tssp file
			tsspPathSplits := strings.Split(path, string(byte(os.PathSeparator)))
			measurementDirWithVersion := tsspPathSplits[len(tsspPathSplits)-2] // measurement_version: m_0000
			measurementName := influx.GetOriginMstName(measurementDirWithVersion)
			// filter measurement
			if len(e.filter.measurement) != 0 && e.filter.measurement != measurementName {
				return nil
			}
			// eg. "0:autogen" to ["0","autogen"]
			splitPtWithRp := strings.Split(ptWithRp, ":")
			key := dbDiskInfo.dbName + ":" + splitPtWithRp[1]
			e.manifest[key] = struct{}{}
			if _, ok := e.rpNameToMeasurementTsspFilesMap[key]; !ok { // db:rp
				e.rpNameToMeasurementTsspFilesMap[key] = make(map[string][]string)
			}
			e.rpNameToMeasurementTsspFilesMap[key][measurementName] = append(e.rpNameToMeasurementTsspFilesMap[key][measurementName], path)
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (e *Exporter) walkWalFile(dbDiskInfo *DatabaseDiskInfo) error {
	for ptWithRp := range dbDiskInfo.rps {
		rpDir := dbDiskInfo.rpToWalDirMap[ptWithRp]
		if err := filepath.Walk(rpDir, func(path string, info fs.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if filepath.Ext(path) != "."+walFileExtension {
				return nil
			}
			// eg. "0:autogen" to ["0","autogen"]
			splitPtWithRp := strings.Split(ptWithRp, ":")
			key := dbDiskInfo.dbName + ":" + splitPtWithRp[1]
			e.manifest[key] = struct{}{}
			e.rpNameToWalFilesMap[key] = append(e.rpNameToWalFilesMap[key], path)
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (e *Exporter) walkIndexFiles(dbDiskInfo *DatabaseDiskInfo) error {
	for ptWithRp := range dbDiskInfo.rps {
		indexPath := dbDiskInfo.rpToIndexDirMap[ptWithRp]
		files, err := os.ReadDir(indexPath)
		if err != nil {
			return err
		}
		for _, file := range files {
			if !file.IsDir() {
				continue
			}
			indexId, err2 := parseIndexDir(file.Name())
			if err2 != nil {
				return err2
			}
			// eg. "0:autogen" to ["0","autogen"]
			splitPtWithRp := strings.Split(ptWithRp, ":")
			key := dbDiskInfo.dbName + ":" + splitPtWithRp[1]
			lockPath := ""
			opt := &tsi.Options{}
			opt.Path(filepath.Join(indexPath, file.Name())).IndexType(index.MergeSet).Lock(&lockPath)
			if _, ok := e.rpNameToIdToIndexMap[key]; !ok { // db:rp
				e.rpNameToIdToIndexMap[key] = make(map[uint64]interface{})
			}
			e.manifest[key] = struct{}{}
			index, err := tsi.NewMergeSetIndex(opt)
			if err != nil {
				return err
			}
			e.rpNameToIdToIndexMap[key][indexId] = index
		}
	}
	return nil
}

// writeSingleTsspFile writes a single tssp file's all records.
func (e *Exporter) writeSingleTsspFile(filePath string, outputWriter io.Writer, index *tsi.MergeSetIndex, isOrder bool) error {
	lockPath := ""
	tsspFile, err := immutable.OpenTSSPFile(filePath, &lockPath, isOrder)
	defer util.MustClose(tsspFile)

	if err != nil {
		return err
	}
	fi := immutable.NewFileIterator(tsspFile, immutable.CLog)
	itr := immutable.NewChunkIterator(fi)
	itrChunk := immutable.NewChunkIterator(fi)
	itrChunk.NextChunkMeta()
	var maxTime int64
	var minTime int64
	minTime, maxTime = fi.GetCurtChunkMeta().MinMaxTime()
	// Check if the maximum and minimum time of records that the SID points to are in the filter range of e.filter
	if e.filter.isBelowMinTimeFilter(maxTime) || e.filter.isAboveMaxTimeFilter(minTime) {
		return nil
	}
	for {
		if !itr.Next() {
			break
		}
		sid := itr.GetSeriesID()
		if sid == 0 {
			return fmt.Errorf("series ID is zero")
		}
		rec := itr.GetRecord()
		record.CheckRecord(rec)

		maxTime = rec.MaxTime(true)
		minTime = rec.MinTime(true)

		// Check if the maximum and minimum time of records that the SID points to are in the filter range of e.filter
		if e.filter.isBelowMinTimeFilter(maxTime) || e.filter.isAboveMaxTimeFilter(minTime) {
			continue
		}

		if err := e.writeSeriesRecords(outputWriter, sid, rec, index); err != nil {
			return err
		}
	}
	err = e.writeProgressedFiles(filePath)
	if err != nil {
		return err
	}
	return nil
}

// writeSeriesRecords writes all records pointed to by one sid.
func (e *Exporter) writeSeriesRecords(outputWriter io.Writer, sid uint64, rec *record.Record, index *tsi.MergeSetIndex) error {

	var combineKey []byte
	var seriesKeys [][]byte
	var isExpectSeries []bool
	var err error
	// Use sid get series key's []byte
	if seriesKeys, _, isExpectSeries, err = index.SearchSeriesWithTagArray(sid, seriesKeys, nil, combineKey, isExpectSeries, nil); err != nil {
		return err
	}
	series := make([][]byte, 1)
	point := &opengemini.Point{}
	sIndex := 0
	for i := range seriesKeys {
		if !isExpectSeries[i] {
			continue
		}
		if sIndex >= 1 {
			bufSeries := influx.GetBytesBuffer()
			bufSeries, err = e.parser.parse2SeriesKeyWithoutVersion(seriesKeys[i], bufSeries, false, point)
			if err != nil {
				return err
			}
			series = append(series, bufSeries)
		} else {
			if series[sIndex] == nil {
				series[sIndex] = influx.GetBytesBuffer()
			}
			series[sIndex], err = e.parser.parse2SeriesKeyWithoutVersion(seriesKeys[i], series[sIndex][:0], false, point)
			if err != nil {
				return err
			}
			sIndex++
		}
	}
	var recs []record.Record
	recs = rec.Split(recs, 1)
	buf := influx.GetBytesBuffer()
	defer influx.PutBytesBuffer(buf)
	for _, r := range recs {
		pointWithTag := &opengemini.Point{
			Measurement: point.Measurement,
			Tags:        point.Tags,
		}
		if buf, err = e.writeSingleRecord(outputWriter, series, r, buf, pointWithTag); err != nil {
			return err
		}
	}
	if e.remoteExporter.isExist {
		err := e.remoteExporter.writeAllPoints()
		if err != nil {
			return err
		}
	}
	for _, bufSeries := range series {
		influx.PutBytesBuffer(bufSeries)
	}
	return nil
}

// writeSingleRecord parses a record and a series key to line protocol, and writes it.
func (e *Exporter) writeSingleRecord(outputWriter io.Writer, seriesKey [][]byte, rec record.Record, buf []byte, point *opengemini.Point) ([]byte, error) {
	tm := rec.Times()[0]
	if !e.filter.timeFilter(tm) {
		return buf, nil
	}
	buf = bytes.Join(seriesKey, []byte(","))
	buf, err := e.parser.appendFields(rec, buf, point)
	if err != nil {
		return nil, err
	}
	if e.remoteExporter.isExist {
		e.remoteExporter.points = append(e.remoteExporter.points, point)
	} else {
		if _, err := outputWriter.Write(buf); err != nil {
			return buf, err
		}
	}
	e.lineCount++
	buf = buf[:0]
	return buf, nil
}

// writeSingleWalFile writes a single wal file's all rows.
func (e *Exporter) writeSingleWalFile(file string, metaWriter io.Writer, outputWriter io.Writer, currentDatabase string, currentMeasurement *string) error {
	lockPath := fileops.FileLockOption("")
	priority := fileops.FilePriorityOption(fileops.IO_PRIORITY_NORMAL)
	fd, err := fileops.OpenFile(file, os.O_RDONLY, 0640, lockPath, priority)
	defer util.MustClose(fd)
	if err != nil {
		return err
	}

	stat, err := fd.Stat()
	if err != nil {
		return err
	}
	fileSize := stat.Size()
	if fileSize == 0 {
		return nil
	}
	recordCompBuff := bufferpool.NewByteBufferPool(engine.WalCompBufSize, 0, bufferpool.MaxLocalCacheLen).Get()
	var offset int64 = 0
	var rows []influx.Row
	for {
		rows, offset, recordCompBuff, err = e.readWalRows(fd, offset, fileSize, recordCompBuff)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return nil
		}
		if e.lineCount == 0 {
			measurementWithVersion := rows[0].Name
			*currentMeasurement = influx.GetOriginMstName(measurementWithVersion)
			*currentMeasurement = EscapeMstName(*currentMeasurement)
			e.parser.writeMetaInfo(metaWriter, InfoTypeMeasurement, *currentMeasurement)
			if err := e.parser.writeMstInfoFromWal(metaWriter, outputWriter, rows[0], currentDatabase); err != nil {
				return err
			}
		}
		if err = e.writeRows(rows, metaWriter, outputWriter, currentDatabase, currentMeasurement); err != nil {
			return err
		}
	}
}

// readWalRows read some rows from the fd, and reuse recordCompBuff to save memory.
func (e *Exporter) readWalRows(fd fileops.File, offset, fileSize int64, recordCompBuff []byte) ([]influx.Row, int64, []byte, error) {
	if offset >= fileSize {
		return nil, offset, recordCompBuff, io.EOF
	}

	// read record header
	var recordHeader [engine.WalRecordHeadSize]byte
	n, err := fd.ReadAt(recordHeader[:], offset)
	if err != nil {
		e.stderrLogger.Println(errno.NewError(errno.ReadWalFileFailed, fd.Name(), offset, "record header").Error())
		return nil, offset, recordCompBuff, io.EOF
	}
	if n != engine.WalRecordHeadSize {
		e.stderrLogger.Println(errno.NewError(errno.WalRecordHeaderCorrupted, fd.Name(), offset).Error())
		return nil, offset, recordCompBuff, io.EOF
	}
	offset += int64(len(recordHeader))

	// prepare record memory
	compBinaryLen := binary.BigEndian.Uint32(recordHeader[1:engine.WalRecordHeadSize])
	recordCompBuff = bufferpool.Resize(recordCompBuff, int(compBinaryLen))

	// read record body
	var recordBuff []byte
	n, err = fd.ReadAt(recordCompBuff, offset)
	if err == nil || err == io.EOF {
		offset += int64(n)
		var innerErr error
		recordBuff, innerErr = snappy.Decode(recordBuff, recordCompBuff)
		if innerErr != nil {
			e.stderrLogger.Println(errno.NewError(errno.DecompressWalRecordFailed, fd.Name(), offset, innerErr.Error()).Error())
			return nil, offset, recordCompBuff, io.EOF
		}
		var rows []influx.Row
		var tagPools []influx.Tag
		var fieldPools []influx.Field
		var indexKeyPools []byte
		var indexOptionPools []influx.IndexOption
		var err error
		rows, _, _, _, _, innerErr = influx.FastUnmarshalMultiRows(recordBuff, rows, tagPools, fieldPools, indexOptionPools, indexKeyPools)

		if innerErr == nil {
			return rows, offset, recordCompBuff, err
		}
		return rows, offset, recordCompBuff, innerErr
	}
	e.stderrLogger.Println(errno.NewError(errno.ReadWalFileFailed, fd.Name(), offset, "record body").Error())
	return nil, offset, recordCompBuff, io.EOF
}

// writeRows process a cluster of rows
func (e *Exporter) writeRows(rows []influx.Row, metaWriter io.Writer, outputWriter io.Writer, currentDatabase string, currentMeasurement *string) error {
	buf := influx.GetBytesBuffer()
	defer influx.PutBytesBuffer(buf)
	var err error
	for _, r := range rows {
		point := &opengemini.Point{}
		if buf, err = e.writeSingleRow(r, metaWriter, outputWriter, buf, point, currentDatabase, currentMeasurement); err != nil {
			return err
		}
	}
	if e.remoteExporter.isExist {
		err := e.remoteExporter.writeAllPoints()
		if err != nil {
			return err
		}
	}
	return nil
}

// writeSingleRow parse a single row to lint protocol, and writes it.
func (e *Exporter) writeSingleRow(row influx.Row, metaWriter io.Writer, outputWriter io.Writer, buf []byte,
	point *opengemini.Point, currentDatabase string, mstName *string) ([]byte, error) {
	measurementWithVersion := row.Name
	measurementName := influx.GetOriginMstName(measurementWithVersion)
	measurementName = EscapeMstName(measurementName)
	tm := row.Timestamp
	// filter measurement
	if len(e.filter.measurement) != 0 && e.filter.measurement != measurementName {
		return buf, nil
	}
	if !e.filter.timeFilter(tm) {
		return buf, nil
	}

	if measurementName != *mstName {
		e.parser.writeMetaInfo(metaWriter, InfoTypeMeasurement, measurementName)
		if err := e.parser.writeMstInfoFromWal(metaWriter, outputWriter, row, currentDatabase); err != nil {
			return buf, err
		}
		*mstName = measurementName
	}
	buf, err := e.parser.getRowBuf(buf, measurementName, row, point)
	if err != nil {
		return nil, err
	}
	if e.remoteExporter.isExist {
		e.remoteExporter.points = append(e.remoteExporter.points, point)
	} else {
		if _, err := outputWriter.Write(buf); err != nil {
			return buf, err
		}
	}
	e.lineCount++
	buf = buf[:0]
	return buf, nil
}

func (e *Exporter) newBar() (*mpb.Bar, error) {
	for _, measurementToTsspFileMap := range e.rpNameToMeasurementTsspFilesMap {
		for _, tsspFiles := range measurementToTsspFileMap {
			e.filesTotalCount += len(tsspFiles)
		}
	}
	for _, walFiles := range e.rpNameToWalFilesMap {
		e.filesTotalCount += len(walFiles)
	}
	if e.filesTotalCount == 0 {
		return nil, fmt.Errorf("no files to export.check your filter or datapath")
	}
	bar := MpbProgress.New(int64(e.filesTotalCount),
		mpb.BarStyle().Lbound("[").Filler("=").Tip(">").Padding("-").Rbound("]"),
		mpb.PrependDecorators(
			decor.Name("Exporting Data:", decor.WC{W: 20, C: decor.DidentRight}),
			decor.CountersNoUnit("%d/%d", decor.WC{W: 15, C: decor.DidentRight}),
			decor.OnComplete(
				decor.AverageETA(decor.ET_STYLE_GO, decor.WC{W: 6}),
				"complete",
			),
		),
		mpb.AppendDecorators(
			decor.Percentage(),
		),
	)
	return bar, nil
}

// write writes data to output fd user specifics.
func (e *Exporter) write() error {
	var outputWriter, metaWriter io.Writer
	var err error
	if e.remoteExporter.isExist {
		outputWriter = io.Discard
	} else {
		err = os.MkdirAll(filepath.Dir(e.outPutPath), 0755)
		if err != nil {
			return err
		}
		var outputFile *os.File // open file descriptor.
		if e.resume {
			exportDir := filepath.Dir(e.outPutPath)
			exportFilePath := filepath.Join(exportDir, resumeFilePrefix+time.Now().Format("2006-01-02_15-04-05.000000000")+filepath.Ext(e.outPutPath))
			outputFile, err = os.OpenFile(exportFilePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
			if err != nil {
				return err
			}
		} else {
			outputFile, err = os.OpenFile(e.outPutPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
			if err != nil {
				return err
			}
		}
		defer outputFile.Close()

		outputWriter = outputFile
	}

	if e.compress {
		if e.remoteExporter.isExist {
			return fmt.Errorf("remote format can't compress")
		}
		gzipWriter := gzip.NewWriter(outputWriter)
		defer gzipWriter.Close()
		outputWriter = gzipWriter
	}

	// metaWriter to write information that are not line-protocols
	if e.remoteExporter.isExist {
		metaWriter = io.Discard
	} else {
		metaWriter = outputWriter
	}

	return e.writeFull(metaWriter, outputWriter)
}

// writeFull writes all DDL and DML
func (e *Exporter) writeFull(metaWriter io.Writer, outputWriter io.Writer) error {
	start, end := time.Unix(0, e.filter.startTime).UTC().Format(time.RFC3339), time.Unix(0, e.filter.endTime).UTC().Format(time.RFC3339)
	e.parser.writeMetaInfo(metaWriter, 0, fmt.Sprintf("# openGemini EXPORT: %s - %s", start, end))
	e.defaultLogger.Printf("Exporting data total %d files\n", e.filesTotalCount)
	if err := e.writeDDL(metaWriter, outputWriter); err != nil {
		return err
	}

	if err := e.writeDML(metaWriter, outputWriter); err != nil {
		return err
	}
	e.defaultLogger.Printf("Summarize %d line protocol\n", e.lineCount)
	return nil
}

// writeDDL write every "database:retention policy" DDL
func (e *Exporter) writeDDL(metaWriter io.Writer, outputWriter io.Writer) error {
	e.parser.writeMetaInfo(metaWriter, 0, "# DDL")
	for _, dbDiskInfo := range e.databaseDiskInfos {
		avoidRepetition := map[string]struct{}{}
		databaseName := dbDiskInfo.dbName
		e.parser.writeOutputInfo(outputWriter, fmt.Sprintf("CREATE DATABASE %s\n", databaseName))
		if e.remoteExporter.isExist {
			// write DDL to remote
			if err := e.remoteExporter.createDatabase(databaseName); err != nil {
				return err
			}
		}
		for ptWithRp := range dbDiskInfo.rps {
			rpName := strings.Split(ptWithRp, ":")[1]
			if _, ok := avoidRepetition[rpName]; !ok {
				if e.remoteExporter.isExist {
					// write DDL to remote
					if err := e.remoteExporter.createRetentionPolicy(databaseName, rpName); err != nil {
						return err
					}
				}
				e.parser.writeOutputInfo(outputWriter, fmt.Sprintf("CREATE RETENTION POLICY %s ON %s DURATION 0s REPLICATION 1\n", rpName, databaseName))
				avoidRepetition[rpName] = struct{}{}
			}
		}
		e.parser.writeMetaInfo(metaWriter, 0, "")
	}
	return nil
}

// writeDML write every "database:retention policy" DML
func (e *Exporter) writeDML(metaWriter io.Writer, outputWriter io.Writer) error {
	e.parser.writeMetaInfo(metaWriter, 0, "# DML")
	var curDatabaseName string
	// write DML for every item which key = "database:retention policy"
	for key := range e.manifest {
		keySplits := strings.Split(key, ":")

		if keySplits[0] != curDatabaseName {
			e.parser.writeMetaInfo(metaWriter, InfoTypeDatabase, keySplits[0])
			curDatabaseName = keySplits[0]
		}
		e.remoteExporter.database = curDatabaseName

		// shardKeyToIndexMap stores all indexes for this "database:retention policy"
		shardKeyToIndexMapInterface, ok := e.rpNameToIdToIndexMap[key]
		if !ok {
			return fmt.Errorf("cant find rpNameToIdToIndexMap for %q", key)
		}
		e.remoteExporter.retentionPolicy = keySplits[1]

		// Convert interface{} map to *tsi.MergeSetIndex map
		shardKeyToIndexMap := make(map[uint64]*tsi.MergeSetIndex)
		for k, v := range shardKeyToIndexMapInterface {
			if idx, ok := v.(*tsi.MergeSetIndex); ok {
				shardKeyToIndexMap[k] = idx
			}
		}

		e.parser.writeMetaInfo(metaWriter, InfoTypeRetentionPolicy, keySplits[1])
		// Write all tssp files from this "database:retention policy"
		if measurementToTsspFileMap, ok := e.rpNameToMeasurementTsspFilesMap[key]; ok {
			if err := e.writeAllTsspFilesInRp(metaWriter, outputWriter, measurementToTsspFileMap, shardKeyToIndexMap); err != nil {
				return err
			}
		}
		// Write all wal files from this "database:retention policy"
		if files, ok := e.rpNameToWalFilesMap[key]; ok {
			if err := e.writeAllWalFilesInRp(metaWriter, outputWriter, files, curDatabaseName); err != nil {
				return err
			}
		}
	}
	MpbProgress.Wait()
	return nil
}

// writeAllTsspFilesInRp writes all tssp files in a "database:retention policy"
func (e *Exporter) writeAllTsspFilesInRp(metaWriter io.Writer, outputWriter io.Writer, measurementFilesMap map[string][]string, indexesMap map[uint64]*tsi.MergeSetIndex) error {
	e.parser.writeMetaInfo(metaWriter, 0, "# FROM TSSP FILE")
	var isOrder bool
	hasWrittenMstInfo := make(map[string]bool)
	for measurementName, files := range measurementFilesMap {
		e.parser.writeMetaInfo(metaWriter, InfoTypeMeasurement, measurementName)
		hasWrittenMstInfo[measurementName] = false
		for _, file := range files {
			if _, ok := e.progress[file]; ok {
				e.bar.Increment()
				continue
			}
			splits := strings.Split(file, string(os.PathSeparator))
			var shardDir string
			if strings.Contains(file, "out-of-order") {
				isOrder = false
				// ie./tmp/openGemini/data/data/db1/0/autogen/1_1567382400000000000_1567987200000000000_1/tssp/average_temperature_0000/out-of-order/00000002-0000-00000000.tssp
				shardDir = splits[len(splits)-5]
			} else {
				isOrder = true
				// ie./tmp/openGemini/data/data/db1/0/autogen/1_1567382400000000000_1567987200000000000_1/tssp/average_temperature_0000/00000002-0000-00000000.tssp
				shardDir = splits[len(splits)-4]
			}
			_, dirStartTime, dirEndTime, indexId, err := parseShardDir(shardDir)
			if err != nil {
				return err
			}
			if err = indexesMap[indexId].Open(); err != nil {
				return err
			}
			if !hasWrittenMstInfo[measurementName] {
				if err := e.parser.writeMstInfoFromTssp(metaWriter, outputWriter, file, isOrder, indexesMap[indexId]); err != nil {
					return err
				}
				hasWrittenMstInfo[measurementName] = true
			}
			if e.filter.isBelowMinTimeFilter(dirEndTime) || e.filter.isAboveMaxTimeFilter(dirStartTime) {
				e.bar.Increment()
				continue
			}
			if err := e.writeSingleTsspFile(file, outputWriter, indexesMap[indexId], isOrder); err != nil {
				return err
			}
			if err = indexesMap[indexId].Close(); err != nil {
				return err
			}
			e.bar.Increment()
		}
		fmt.Fprintf(outputWriter, "\n")
	}
	return nil
}

// writeAllWalFilesInRp writes all wal files in a "database:retention policy"
func (e *Exporter) writeAllWalFilesInRp(metaWriter io.Writer, outputWriter io.Writer, files []string, currentDatabase string) error {
	e.parser.writeMetaInfo(metaWriter, 0, "# FROM WAL FILE")
	var currentMeasurement string
	for _, file := range files {
		if _, ok := e.progress[file]; ok {
			e.bar.Increment()
			continue
		}
		if err := e.writeSingleWalFile(file, metaWriter, outputWriter, currentDatabase, &currentMeasurement); err != nil {
			return err
		}
		e.bar.Increment()
		if err := e.writeProgressedFiles(file); err != nil {
			return err
		}
	}
	fmt.Fprintf(outputWriter, "\n")
	return nil
}

type offlineTxtParser struct{}

func newOfflineTxtParser() *offlineTxtParser {
	return &offlineTxtParser{}
}

// parse2SeriesKeyWithoutVersion parse encoded index key to line protocol series key,without version and escape special characters
// encoded index key format: [total len][ms len][ms][tagkey1 len][tagkey1 val]...]
// parse to line protocol format: mst,tagkey1=tagval1,tagkey2=tagval2...
func (t *offlineTxtParser) parse2SeriesKeyWithoutVersion(key []byte, dst []byte, splitWithNull bool, point *opengemini.Point) ([]byte, error) {
	msName, src, err := influx.MeasurementName(key)
	originMstName := influx.GetOriginMstName(string(msName))
	originMstName = EscapeMstName(originMstName)
	if err != nil {
		return []byte{}, err
	}
	var split [2]byte
	if splitWithNull {
		split[0], split[1] = influx.ByteSplit, influx.ByteSplit
	} else {
		split[0], split[1] = '=', ','
	}
	point.Measurement = originMstName
	dst = append(dst, originMstName...)
	dst = append(dst, ',')
	tagsN := encoding.UnmarshalUint16(src)
	src = src[2:]
	var i uint16
	for i = 0; i < tagsN; i++ {
		keyLen := encoding.UnmarshalUint16(src)
		src = src[2:]
		tagKey := EscapeTagKey(string(src[:keyLen]))
		dst = append(dst, tagKey...)
		dst = append(dst, split[0])
		src = src[keyLen:]

		valLen := encoding.UnmarshalUint16(src)
		src = src[2:]
		tagVal := EscapeTagValue(string(src[:valLen]))
		dst = append(dst, tagVal...)
		dst = append(dst, split[1])
		src = src[valLen:]

		point.AddTag(tagKey, tagVal)
	}
	return dst[:len(dst)-1], nil
}

func (t *offlineTxtParser) appendFields(recInterface interface{}, buf []byte, point *opengemini.Point) ([]byte, error) {
	rec, ok := recInterface.(record.Record)
	if !ok {
		return nil, fmt.Errorf("invalid record type for offline mode")
	}
	buf = append(buf, ' ')
	for i, field := range rec.Schema {
		if field.Name == "time" {
			continue
		}
		buf = append(buf, EscapeFieldKey(field.Name)+"="...)
		switch field.Type {
		case influx.Field_Type_Float:
			buf = strconv.AppendFloat(buf, rec.Column(i).FloatValues()[0], 'g', -1, 64)
			point.AddField(EscapeFieldKey(field.Name), strconv.FormatFloat(rec.Column(i).FloatValues()[0], 'g', -1, 64))
		case influx.Field_Type_Int:
			buf = strconv.AppendInt(buf, rec.Column(i).IntegerValues()[0], 10)
			point.AddField(EscapeFieldKey(field.Name), strconv.FormatInt(rec.Column(i).IntegerValues()[0], 10))
		case influx.Field_Type_Boolean:
			buf = strconv.AppendBool(buf, rec.Column(i).BooleanValues()[0])
			point.AddField(EscapeFieldKey(field.Name), strconv.FormatBool(rec.Column(i).BooleanValues()[0]))
		case influx.Field_Type_String:
			var str []string
			str = rec.Column(i).StringValues(str)
			buf = append(buf, '"')
			buf = append(buf, EscapeStringFieldValue(str[0])...)
			buf = append(buf, '"')
			point.AddField(EscapeFieldKey(field.Name), str[0])
		default:
			// This shouldn't be possible, but we'll format it anyway.
			buf = append(buf, fmt.Sprintf("%v", rec.Column(i))...)
			point.AddField(EscapeFieldKey(field.Name), fmt.Sprintf("%v", rec.Column(i)))
		}
		if i != rec.Len()-2 {
			buf = append(buf, ',')
		} else {
			buf = append(buf, ' ')
		}
	}
	buf = strconv.AppendInt(buf, rec.Times()[0], 10)
	buf = append(buf, '\n')
	point.Timestamp = rec.Times()[0]
	return buf, nil
}

func (t *offlineTxtParser) writeMstInfoFromTssp(metaWriter io.Writer, outputWriter io.Writer, filePath string, isOrder bool, index interface{}) error {
	idx, ok := index.(*tsi.MergeSetIndex)
	if !ok {
		return fmt.Errorf("invalid index type")
	}
	lockPath := ""
	tsspFile, err := immutable.OpenTSSPFile(filePath, &lockPath, isOrder)
	defer util.MustClose(tsspFile)
	if err != nil {
		return err
	}
	// search tags
	fiTag := immutable.NewFileIterator(tsspFile, immutable.CLog)
	itrTag := immutable.NewChunkIterator(fiTag)
	itrTag.Next()
	sid := itrTag.GetSeriesID()
	if sid == 0 {
		return fmt.Errorf("series ID is zero")
	}
	var combineKey []byte
	var seriesKeys [][]byte
	var isExpectSeries []bool
	// Use sid get series key's []byte
	if seriesKeys, _, _, err = idx.SearchSeriesWithTagArray(sid, seriesKeys, nil, combineKey, isExpectSeries, nil); err != nil {
		return err
	}
	_, src, err := influx.MeasurementName(seriesKeys[0])
	if err != nil {
		return err
	}
	tagsN := encoding.UnmarshalUint16(src)
	src = src[2:]
	var i uint16
	var tags, fields, tagsType, fieldsType []string
	for i = 0; i < tagsN; i++ {
		keyLen := encoding.UnmarshalUint16(src)
		src = src[2:]
		tagKey := EscapeTagKey(string(src[:keyLen]))
		tags = append(tags, tagKey)
		src = src[keyLen:]

		valLen := encoding.UnmarshalUint16(src)
		src = src[2:]
		src = src[valLen:]
	}
	for i := 0; i < len(tags); i++ {
		tagsType = append(tagsType, "tag")
	}
	// search fields
	fiField := immutable.NewFileIterator(tsspFile, immutable.CLog)
	itrField := immutable.NewChunkIterator(fiField)
	itrField.NextChunkMeta()
	for _, colMeta := range fiField.GetCurtChunkMeta().GetColMeta() {
		fields = append(fields, colMeta.Name())
		if colMeta.Name() == "time" {
			fieldsType = append(fieldsType, "dateTime:timeStamp")
		} else {
			fieldsType = append(fieldsType, influx.FieldTypeString(int32(colMeta.Type())))
		}
	}
	// write datatype
	fmt.Fprintf(metaWriter, "#datatype %s,%s\n", strings.Join(tagsType, ","), strings.Join(fieldsType, ","))
	// write tags and fields name
	buf := influx.GetBytesBuffer()
	defer influx.PutBytesBuffer(buf)
	buf = append(buf, strings.Join(tags, ",")...)
	buf = append(buf, ',')
	buf = append(buf, strings.Join(fields, ",")...)
	buf = append(buf, '\n')
	_, err = outputWriter.Write(buf)
	if err != nil {
		return err
	}
	return nil
}

func (t *offlineTxtParser) writeMstInfoFromWal(_ io.Writer, _ io.Writer, _ interface{}, _ string) error {
	return nil
}

func (t *offlineTxtParser) getRowBuf(buf []byte, measurementName string, row interface{}, point *opengemini.Point) ([]byte, error) {
	rowData, ok := row.(influx.Row)
	if !ok {
		return nil, fmt.Errorf("invalid row type")
	}
	point.Measurement = measurementName
	tags := rowData.Tags
	fields := rowData.Fields
	tm := rowData.Timestamp

	buf = append(buf, measurementName...)
	buf = append(buf, ',')
	for i, tag := range tags {
		buf = append(buf, EscapeTagKey(tag.Key)+"="...)
		buf = append(buf, EscapeTagValue(tag.Value)...)
		if i != len(tags)-1 {
			buf = append(buf, ',')
		} else {
			buf = append(buf, ' ')
		}
		point.AddTag(EscapeTagKey(tag.Key), EscapeTagValue(tag.Value))
	}
	for i, field := range fields {
		buf = append(buf, EscapeFieldKey(field.Key)+"="...)
		switch field.Type {
		case influx.Field_Type_Float:
			buf = strconv.AppendFloat(buf, field.NumValue, 'g', -1, 64)
			point.AddField(EscapeFieldKey(field.Key), strconv.FormatFloat(field.NumValue, 'g', -1, 64))
		case influx.Field_Type_Int:
			buf = strconv.AppendInt(buf, int64(field.NumValue), 10)
			point.AddField(EscapeFieldKey(field.Key), strconv.FormatInt(int64(field.NumValue), 10))
		case influx.Field_Type_Boolean:
			buf = strconv.AppendBool(buf, field.NumValue == 1)
			point.AddField(EscapeFieldKey(field.Key), strconv.FormatBool(field.NumValue == 1))
		case influx.Field_Type_String:
			buf = append(buf, '"')
			buf = append(buf, EscapeStringFieldValue(field.StrValue)...)
			buf = append(buf, '"')
			point.AddField(EscapeFieldKey(field.Key), field.StrValue)
		default:
			// This shouldn't be possible, but we'll format it anyway.
			buf = append(buf, fmt.Sprintf("%v", field)...)
			point.AddField(EscapeFieldKey(field.Key), fmt.Sprintf("%v", field))
		}
		if i != len(fields)-1 {
			buf = append(buf, ',')
		} else {
			buf = append(buf, ' ')
		}
	}
	buf = strconv.AppendInt(buf, tm, 10)
	buf = append(buf, '\n')
	point.Timestamp = tm
	return buf, nil
}

func (t *offlineTxtParser) writeMetaInfo(metaWriter io.Writer, infoType InfoType, info string) {
	switch infoType {
	case InfoTypeDatabase:
		fmt.Fprintf(metaWriter, "# CONTEXT-DATABASE: %s\n", info)
	case InfoTypeRetentionPolicy:
		fmt.Fprintf(metaWriter, "# CONTEXT-RETENTION-POLICY: %s\n", info)
	case InfoTypeMeasurement:
		fmt.Fprintf(metaWriter, "# CONTEXT-MEASUREMENT: %s\n", info)
	default:
		fmt.Fprintf(metaWriter, "%s\n", info)
	}
}

func (t *offlineTxtParser) writeOutputInfo(outputWriter io.Writer, info string) {
	fmt.Fprint(outputWriter, info)
}

type offlineCsvParser struct {
	fieldsName     map[string]map[string][]string // database -> measurement -> []field
	curDatabase    string
	curMeasurement string
}

func newOfflineCsvParser() *offlineCsvParser {
	return &offlineCsvParser{
		fieldsName: make(map[string]map[string][]string),
	}
}

// parse2SeriesKeyWithoutVersion parse encoded index key to csv series key,without version and escape special characters
// encoded index key format: [total len][ms len][ms][tagkey1 len][tagkey1 val]...]
// parse to csv format: mst,tagval1,tagval2...
func (c *offlineCsvParser) parse2SeriesKeyWithoutVersion(key []byte, dst []byte, splitWithNull bool, _ *opengemini.Point) ([]byte, error) {
	_, src, err := influx.MeasurementName(key)
	if err != nil {
		return []byte{}, err
	}
	var split [2]byte
	if splitWithNull {
		split[0], split[1] = influx.ByteSplit, influx.ByteSplit
	} else {
		split[0], split[1] = '=', ','
	}

	tagsN := encoding.UnmarshalUint16(src)
	src = src[2:]
	var i uint16
	for i = 0; i < tagsN; i++ {
		keyLen := encoding.UnmarshalUint16(src)
		src = src[2:]
		src = src[keyLen:]

		valLen := encoding.UnmarshalUint16(src)
		src = src[2:]
		tagVal := EscapeTagValue(string(src[:valLen]))
		dst = append(dst, tagVal...)
		dst = append(dst, split[1])
		src = src[valLen:]
	}
	return dst, nil
}

func (c *offlineCsvParser) appendFields(recInterface interface{}, buf []byte, _ *opengemini.Point) ([]byte, error) {
	rec, ok := recInterface.(record.Record)
	if !ok {
		return nil, fmt.Errorf("invalid record type for offline mode")
	}
	curFieldsName := c.fieldsName[c.curDatabase][c.curMeasurement]
	for _, fieldName := range curFieldsName {
		if fieldName == "time" {
			continue
		}
		k, ok := getFieldNameIndexFromRecord(rec.Schema, fieldName)
		if !ok {
			buf = append(buf, ',')
		} else {
			switch rec.Schema[k].Type {
			case influx.Field_Type_Float:
				buf = strconv.AppendFloat(buf, rec.Column(k).FloatValues()[0], 'g', -1, 64)
			case influx.Field_Type_Int:
				buf = strconv.AppendInt(buf, rec.Column(k).IntegerValues()[0], 10)
			case influx.Field_Type_Boolean:
				buf = strconv.AppendBool(buf, rec.Column(k).BooleanValues()[0])
			case influx.Field_Type_String:
				var str []string
				str = rec.Column(k).StringValues(str)
				buf = append(buf, '"')
				buf = append(buf, EscapeStringFieldValue(str[0])...)
				buf = append(buf, '"')
			default:
				// This shouldn't be possible, but we'll format it anyway.
				buf = append(buf, fmt.Sprintf("%v", rec.Column(k))...)
			}
			if k != rec.Len()-1 {
				buf = append(buf, ',')
			}
		}
	}
	buf = strconv.AppendInt(buf, rec.Times()[0], 10)
	buf = append(buf, '\n')
	return buf, nil
}

func (c *offlineCsvParser) writeMstInfoFromTssp(metaWriter io.Writer, outputWriter io.Writer, filePath string, isOrder bool, index interface{}) error {
	idx, ok := index.(*tsi.MergeSetIndex)
	if !ok {
		return fmt.Errorf("invalid index type")
	}
	tsspPathSplits := strings.Split(filePath, string(byte(os.PathSeparator)))
	measurementDirWithVersion := tsspPathSplits[len(tsspPathSplits)-2]
	measurementName := influx.GetOriginMstName(measurementDirWithVersion)
	dbName := tsspPathSplits[len(tsspPathSplits)-7]
	lockPath := ""
	tsspFile, err := immutable.OpenTSSPFile(filePath, &lockPath, isOrder)
	defer util.MustClose(tsspFile)
	if err != nil {
		return err
	}
	// search tags
	fiTag := immutable.NewFileIterator(tsspFile, immutable.CLog)
	itrTag := immutable.NewChunkIterator(fiTag)
	itrTag.Next()
	sid := itrTag.GetSeriesID()
	if sid == 0 {
		return fmt.Errorf("series ID is zero")
	}
	var combineKey []byte
	var seriesKeys [][]byte
	var isExpectSeries []bool
	// Use sid get series key's []byte
	if seriesKeys, _, _, err = idx.SearchSeriesWithTagArray(sid, seriesKeys, nil, combineKey, isExpectSeries, nil); err != nil {
		return err
	}
	_, src, err := influx.MeasurementName(seriesKeys[0])
	if err != nil {
		return err
	}
	tagsN := encoding.UnmarshalUint16(src)
	src = src[2:]
	var i uint16
	var tags, fields, tagsType, fieldsType []string
	for i = 0; i < tagsN; i++ {
		keyLen := encoding.UnmarshalUint16(src)
		src = src[2:]
		tagKey := EscapeTagKey(string(src[:keyLen]))
		tags = append(tags, tagKey)
		src = src[keyLen:]

		valLen := encoding.UnmarshalUint16(src)
		src = src[2:]
		src = src[valLen:]
	}
	for i := 0; i < len(tags); i++ {
		tagsType = append(tagsType, "tag")
	}
	// search fields
	fiField := immutable.NewFileIterator(tsspFile, immutable.CLog)
	itrField := immutable.NewChunkIterator(fiField)
	itrField.NextChunkMeta()
	for _, colMeta := range fiField.GetCurtChunkMeta().GetColMeta() {
		fields = append(fields, colMeta.Name())
		if colMeta.Name() == "time" {
			fieldsType = append(fieldsType, "dateTime:timeStamp")
		} else {
			fieldsType = append(fieldsType, influx.FieldTypeString(int32(colMeta.Type())))
		}
	}
	c.fieldsName[dbName] = make(map[string][]string)
	c.fieldsName[dbName][measurementName] = fields
	c.curDatabase = dbName
	c.curMeasurement = measurementName
	// write datatype
	fmt.Fprintf(metaWriter, "#datatype %s,%s\n", strings.Join(tagsType, ","), strings.Join(fieldsType, ","))
	// write tags and fields name
	buf := influx.GetBytesBuffer()
	defer influx.PutBytesBuffer(buf)
	buf = append(buf, strings.Join(tags, ",")...)
	buf = append(buf, ',')
	buf = append(buf, strings.Join(fields, ",")...)
	buf = append(buf, '\n')
	_, err = outputWriter.Write(buf)
	if err != nil {
		return err
	}
	return nil
}

func (c *offlineCsvParser) writeMstInfoFromWal(metaWriter io.Writer, outputWriter io.Writer, row interface{}, currentDatabase string) error {
	rowData, ok := row.(influx.Row)
	if !ok {
		return fmt.Errorf("invalid row type")
	}
	tagsN := rowData.Tags
	fieldsN := rowData.Fields
	var tags, fields, tagsType, fieldsType []string
	for _, tag := range tagsN {
		tags = append(tags, tag.Key)
		tagsType = append(tagsType, "tag")
	}
	for _, field := range fieldsN {
		fields = append(fields, field.Key)
		fieldsType = append(fieldsType, influx.FieldTypeString(field.Type))
	}
	fieldsType = append(fieldsType, "dateTime:timeStamp")
	measurementWithVersion := rowData.Name
	measurementName := influx.GetOriginMstName(measurementWithVersion)
	measurementName = EscapeMstName(measurementName)
	c.fieldsName[currentDatabase] = make(map[string][]string)
	c.fieldsName[currentDatabase][measurementName] = fields
	c.curDatabase = currentDatabase
	c.curMeasurement = measurementName
	// write datatype
	fmt.Fprintf(metaWriter, "#datatype %s,%s\n", strings.Join(tagsType, ","), strings.Join(fieldsType, ","))
	// write tags and fields name
	buf := influx.GetBytesBuffer()
	defer influx.PutBytesBuffer(buf)
	buf = append(buf, strings.Join(tags, ",")...)
	buf = append(buf, ',')
	buf = append(buf, strings.Join(fields, ",")...)
	buf = append(buf, ',')
	buf = append(buf, "time"...)
	buf = append(buf, '\n')
	_, err := outputWriter.Write(buf)
	if err != nil {
		return err
	}
	return nil
}

func (c *offlineCsvParser) getRowBuf(buf []byte, measurementName string, row interface{}, _ *opengemini.Point) ([]byte, error) {
	rowData, ok := row.(influx.Row)
	if !ok {
		return nil, fmt.Errorf("invalid row type")
	}
	tags := rowData.Tags
	fields := rowData.Fields
	tm := rowData.Timestamp

	for _, tag := range tags {
		buf = append(buf, EscapeTagValue(tag.Value)...)
		buf = append(buf, ',')
	}
	curFieldsName := c.fieldsName[c.curDatabase][c.curMeasurement]
	for _, fieldName := range curFieldsName {
		if fieldName == "time" {
			continue
		}
		k, ok := getFieldNameIndexFromRow(fields, fieldName)
		if !ok {
			buf = append(buf, ',')
		} else {
			switch fields[k].Type {
			case influx.Field_Type_Float:
				buf = strconv.AppendFloat(buf, fields[k].NumValue, 'g', -1, 64)
			case influx.Field_Type_Int:
				buf = strconv.AppendInt(buf, int64(fields[k].NumValue), 10)
			case influx.Field_Type_Boolean:
				buf = strconv.AppendBool(buf, fields[k].NumValue == 1)
			case influx.Field_Type_String:
				buf = append(buf, '"')
				buf = append(buf, EscapeStringFieldValue(fields[k].StrValue)...)
				buf = append(buf, '"')
			default:
				// This shouldn't be possible, but we'll format it anyway.
				buf = append(buf, fmt.Sprintf("%v", fields[k])...)
			}
			buf = append(buf, ',')
		}
	}
	buf = strconv.AppendInt(buf, tm, 10)
	buf = append(buf, '\n')
	return buf, nil
}

func (c *offlineCsvParser) writeMetaInfo(metaWriter io.Writer, infoType InfoType, info string) {
	switch infoType {
	case InfoTypeDatabase:
		fmt.Fprintf(metaWriter, "#constant database,%s\n", info)
	case InfoTypeRetentionPolicy:
		fmt.Fprintf(metaWriter, "#constant retention_policy,%s\n", info)
	case InfoTypeMeasurement:
		fmt.Fprintf(metaWriter, "#constant measurement,%s\n", info)
	default:
		return
	}
}

func (c *offlineCsvParser) writeOutputInfo(_ io.Writer, _ string) {
}
