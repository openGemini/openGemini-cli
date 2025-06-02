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

package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openGemini/opengemini-client-go/opengemini"
	"github.com/openGemini/opengemini-client-go/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/openGemini/openGemini-cli/core"
)

const (
	ImportFormatLineProtocol = "line_protocol"
	ImportFormatCSV          = "csv"

	ImportTokenDDL             = "# DDL"
	ImportTokenDML             = "# DML"
	ImportTokenDatabase        = "# CONTEXT-DATABASE:"
	ImportTokenRetentionPolicy = "# CONTEXT-RETENTION-POLICY:"
	ImportTokenMeasurement     = "# CONTEXT-MEASUREMENT:"
	ImportTokenTags            = "# CONTEXT-TAGS:"
	ImportTokenFields          = "# CONTEXT-FIELDS:"
	ImportTokenTimeField       = "# CONTEXT-TIME:"
)

var (
	builderEntities = make(map[string]opengemini.WriteRequestBuilder)
)

func NewColumnWriterClient(cfg *ImportConfig) (proto.WriteServiceClient, error) {
	var dialOptions = []grpc.DialOption{
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             3 * time.Second,
			PermitWithoutStream: true,
		}),
		// https://github.com/grpc/grpc/blob/master/doc/connection-backoff.md
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  time.Second,
				Multiplier: 1.6,
				Jitter:     0.2,
				MaxDelay:   time.Second * 30,
			},
			MinConnectTimeout: time.Second * 20,
		}),
		grpc.WithInitialWindowSize(1 << 24),                                    // 16MB
		grpc.WithInitialConnWindowSize(1 << 24),                                // 16MB
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64 * 1024 * 1024)), // 64MB
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(64 * 1024 * 1024)), // 64MB
	}
	if cfg.EnableTls {
		var tlsManager, err = core.NewCertificateManager(cfg.CACert, cfg.Cert, cfg.CertKey)
		if err != nil {
			return nil, err
		}
		cred := credentials.NewTLS(tlsManager.CreateTls(cfg.InsecureTls, cfg.InsecureHostname))
		dialOptions = append(dialOptions, grpc.WithTransportCredentials(cred))
	} else {
		dialOptions = append(dialOptions, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	conn, err := grpc.NewClient(cfg.Host+":"+strconv.Itoa(cfg.ColumnWritePort), dialOptions...)
	if err != nil {
		return nil, err
	}
	return proto.NewWriteServiceClient(conn), nil
}

type ImportConfig struct {
	*core.CommandLineConfig
	Path            string
	Format          string
	ColumnWrite     bool
	ColumnWritePort int
	BatchSize       int
	Tags            []string
	Fields          []string
	TimeField       string
}

type ImportCommand struct {
	cfg         *ImportConfig
	httpClient  core.HttpClient
	writeClient proto.WriteServiceClient
	fsm         *ImportFileFSM
}

func (c *ImportCommand) Run(config *ImportConfig) error {
	if config.Format == "" {
		config.Format = ImportFormatLineProtocol
	}
	httpClient, err := core.NewHttpClient(config.CommandLineConfig)
	if err != nil {
		slog.Error("create http client failed", "reason", err)
		return err
	}
	c.httpClient = httpClient
	if config.ColumnWritePort == 0 {
		config.ColumnWritePort = 8035
	}
	c.writeClient, err = NewColumnWriterClient(config)
	if err != nil {
		slog.Error("create column writer client failed", "reason", err)
		return err
	}
	c.cfg = config
	c.fsm = new(ImportFileFSM)
	return c.process()
}

func (c *ImportCommand) process() error {
	file, err := os.Open(c.cfg.Path)
	if err != nil {
		slog.Error("open file failed", "file", c.cfg.Path, "reason", err)
		return err
	}
	defer file.Close()
	var ctx = context.Background()
	switch c.cfg.Format {
	case ImportFormatLineProtocol:
		scanner := bufio.NewReader(file)
		for {
			line, err := scanner.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					break
				}
				slog.Error("read line failed", "reason", err)
				continue
			}
			fsmCall, err := c.fsm.processLineProtocol(ctx, string(line))
			if err != nil {
				slog.Error("process line protocol failed", "reason", err)
				continue
			}
			err = fsmCall(ctx, c)
			if err != nil {
				slog.Error("call line protocol fsm function failed", "reason", err)
				continue
			}
		}
		if err := c.fsm.clearBuffer()(ctx, c); err != nil {
			slog.Error("clear buffer failed", "reason", err)
		}
		slog.Info("process finished", "path", c.cfg.Path)
		return nil
	case ImportFormatCSV:
		slog.Info("tips: csv file import only support by column write protocol")
		csvReader := csv.NewReader(file)
		csvReader.Comment = '#'
		for {
			row, err := csvReader.Read()
			if err != nil {
				if err == io.EOF {
					break
				}
				slog.Error("read csv line failed", "reason", err)
				continue
			}
			fsmCall, err := c.fsm.processCSV(ctx, row)
			if err != nil {
				slog.Error("process csv line failed", "reason", err)
				continue
			}
			err = fsmCall(ctx, c)
			if err != nil {
				slog.Error("call csv line fsm function failed", "reason", err)
				continue
			}
		}
		if err := c.fsm.clearBuffer()(ctx, c); err != nil {
			slog.Error("clear buffer failed", "reason", err)
		}
		slog.Info("process finished", "path", c.cfg.Path)
		return nil
	default:
		return fmt.Errorf("unknown --format %s, only support line_protocol,csv", c.cfg.Format)
	}
}

type ImportState int

const (
	ImportStateDDL = iota
	ImportStateDML
)

type ImportFileFSM struct {
	state            ImportState
	database         string
	retentionPolicy  string
	measurement      string
	tagMap           map[string]FieldPos
	fieldMap         map[string]FieldPos
	timeField        FieldPos
	batchLPBuffer    []string
	batchPointBuffer []*opengemini.Point
}

type FieldPos struct {
	Name string
	Pos  int
}

type FSMCall func(ctx context.Context, command *ImportCommand) error

var FSMCallEmpty = func(ctx context.Context, command *ImportCommand) error { return nil }

func (fsm *ImportFileFSM) clearBuffer() FSMCall {
	var err error
	return func(ctx context.Context, command *ImportCommand) error {
		if len(fsm.batchLPBuffer) != 0 {
			defer func() {
				command.fsm.batchLPBuffer = command.fsm.batchLPBuffer[:0]
			}()
			var lines = strings.Join(command.fsm.batchLPBuffer, "\n")
			writeErr := command.httpClient.Write(ctx, fsm.database, fsm.retentionPolicy, lines, command.cfg.Precision)
			if err != nil {
				err = errors.Join(err, writeErr)
			}
		}
		if len(fsm.batchPointBuffer) != 0 {
			defer func() {
				command.fsm.batchPointBuffer = command.fsm.batchPointBuffer[:0]
			}()
			var builderName = command.fsm.database + "." + command.fsm.retentionPolicy
			builder, ok := builderEntities[builderName]
			if !ok {
				var buildRequestErr error
				builder, buildRequestErr = opengemini.NewWriteRequestBuilder(command.fsm.database, command.fsm.retentionPolicy)
				if err != nil {
					err = errors.Join(err, buildRequestErr)
					return err
				}
				builderEntities[builderName] = builder
			}
			var recordBuilder = make(map[string]opengemini.RecordBuilder)
			var recordLines []opengemini.RecordLine
			for _, point := range fsm.batchPointBuffer {
				rb, ok := recordBuilder[point.Measurement]
				if !ok {
					var recordBuilderErr error
					rb, recordBuilderErr = opengemini.NewRecordBuilder(point.Measurement)
					if err != nil {
						err = errors.Join(err, recordBuilderErr)
						return err
					}
					recordBuilder[point.Measurement] = rb
				}
				newLine := rb.NewLine()
				for key, value := range point.Tags {
					newLine.AddTag(key, value)
				}
				for key, value := range point.Fields {
					newLine.AddField(key, value)
				}
				recordLines = append(recordLines, newLine.Build(point.Timestamp))
			}
			var buildErr error
			request, buildErr := builder.Authenticate(command.cfg.Username, command.cfg.Password).AddRecord(recordLines...).Build()
			if err != nil {
				err = errors.Join(err, buildErr)
				return err
			}
			response, writeErr := command.writeClient.Write(ctx, request)
			if err != nil {
				err = errors.Join(err, writeErr)
				return err
			}
			switch response.Code {
			case 0:
				return err
			case 1:
				return fmt.Errorf("%w\nwrite failed, code: %d, partial write failure", err, response.GetCode())
			case 2:
				return fmt.Errorf("%w\nwrite failed, code: %d, write failure", err, response.GetCode())
			default:
				return fmt.Errorf("%w\nunexpected response code: %d", err, response.Code)
			}
		}
		return err
	}
}

func (fsm *ImportFileFSM) processLineProtocol(ctx context.Context, data string) (FSMCall, error) {
	if strings.HasPrefix(data, ImportTokenDDL) {
		fsm.state = ImportStateDDL
		return FSMCallEmpty, nil
	}
	if strings.HasPrefix(data, ImportTokenDML) {
		fsm.state = ImportStateDML
		fsm.retentionPolicy = "autogen"
		return FSMCallEmpty, nil
	}
	switch fsm.state {
	case ImportStateDDL:
		if strings.TrimSpace(data) == "" {
			return FSMCallEmpty, nil
		}
		data = strings.TrimSpace(data)
		return func(ctx context.Context, command *ImportCommand) error {
			_, err := command.httpClient.Query(ctx, &opengemini.Query{
				Command: data,
			})
			if err != nil {
				slog.Error("execute ddl failed", "reason", err, "command", data)
				return err
			}
			slog.Info("execute ddl success", "command", data)
			return nil
		}, nil
	case ImportStateDML:
		if strings.HasPrefix(data, ImportTokenDatabase) {
			fsm.database = strings.TrimSpace(strings.Split(data, ":")[1])
			return FSMCallEmpty, nil
		}
		if strings.HasPrefix(data, ImportTokenRetentionPolicy) {
			fsm.retentionPolicy = strings.TrimSpace(strings.Split(data, ":")[1])
			return FSMCallEmpty, nil
		}
		if strings.HasPrefix(data, "#") {
			return FSMCallEmpty, nil
		}
		// skip blank lines
		if strings.TrimSpace(data) == "" {
			return FSMCallEmpty, nil
		}
		data = strings.TrimSpace(data)
		return func(ctx context.Context, command *ImportCommand) error {
			if command.fsm.database == "" {
				return errors.New("database is required, make sure `# CONTEXT-DATABASE:` token is exist")
			}
			if len(command.fsm.batchLPBuffer) < command.cfg.BatchSize {
				command.fsm.batchLPBuffer = append(command.fsm.batchLPBuffer, data)
				return nil
			}
			defer func() {
				// clear batch buffer
				command.fsm.batchLPBuffer = command.fsm.batchLPBuffer[:0]
			}()
			var err error
			var lines = strings.Join(command.fsm.batchLPBuffer, "\n")
			if command.cfg.ColumnWrite {
				var builderName = command.fsm.database + "." + command.fsm.retentionPolicy
				builder, ok := builderEntities[builderName]
				if !ok {
					builder, err = opengemini.NewWriteRequestBuilder(command.fsm.database, command.fsm.retentionPolicy)
					if err != nil {
						return err
					}
					builderEntities[builderName] = builder
				}
				parser := core.NewLineProtocolParser(lines)
				points, err := parser.Parse()
				if err != nil {
					return err
				}
				var recordBuilder = make(map[string]opengemini.RecordBuilder)
				var recordLines []opengemini.RecordLine
				for _, point := range points {
					rb, ok := recordBuilder[point.Measurement]
					if !ok {
						rb, err = opengemini.NewRecordBuilder(point.Measurement)
						if err != nil {
							return err
						}
						recordBuilder[point.Measurement] = rb
					}
					newLine := rb.NewLine()
					for key, value := range point.Tags {
						newLine.AddTag(key, value)
					}
					for key, value := range point.Fields {
						newLine.AddField(key, value)
					}
					recordLines = append(recordLines, newLine.Build(point.Timestamp))
				}
				request, err := builder.Authenticate(command.cfg.Username, command.cfg.Password).AddRecord(recordLines...).Build()
				if err != nil {
					return err
				}
				response, err := command.writeClient.Write(ctx, request)
				if err != nil {
					return err
				}
				switch response.Code {
				case 0:
					return nil
				case 1:
					return fmt.Errorf("write failed, code: %d, partial write failure", response.GetCode())
				case 2:
					return fmt.Errorf("write failed, code: %d, write failure", response.GetCode())
				default:
					return fmt.Errorf("unexpected response code: %d", response.Code)
				}
			} else {
				err = command.httpClient.Write(ctx, fsm.database, fsm.retentionPolicy, lines, command.cfg.Precision)
			}
			return err
		}, nil
	}
	return FSMCallEmpty, nil
}

func (fsm *ImportFileFSM) processCSV(ctx context.Context, data []string) (FSMCall, error) {
	if len(data) == 0 {
		return FSMCallEmpty, nil
	}

	switch fsm.state {
	case ImportStateDDL:
		fsm.state = ImportStateDML
		return func(ctx context.Context, command *ImportCommand) error {
			fsm.database = command.cfg.Database
			fsm.retentionPolicy = command.cfg.RetentionPolicy
			fsm.measurement = command.cfg.Measurement
			fsm.tagMap = make(map[string]FieldPos)
			fsm.fieldMap = make(map[string]FieldPos)
			for _, tag := range command.cfg.Tags {
				fsm.tagMap[tag] = FieldPos{}
			}
			for _, field := range command.cfg.Fields {
				fsm.fieldMap[field] = FieldPos{}
			}
			for idx, datum := range data {
				_, ok := fsm.tagMap[datum]
				if ok {
					fsm.tagMap[datum] = FieldPos{datum, idx}
					continue
				}
				_, ok = fsm.fieldMap[datum]
				if ok {
					fsm.fieldMap[datum] = FieldPos{datum, idx}
					continue
				}
				if command.cfg.TimeField == datum {
					fsm.timeField = FieldPos{datum, idx}
					continue
				}
				slog.Info("ignore column name", "column", datum)
			}
			for _, field := range command.cfg.Fields {
				if fsm.fieldMap[field].Name == "" {
					return errors.New("field name not in csv header " + field)
				}
			}
			for _, tag := range command.cfg.Tags {
				if fsm.tagMap[tag].Name == "" {
					return errors.New("tag name not in csv header " + tag)
				}
			}
			if fsm.timeField.Name == "" {
				return errors.New("time field name not in csv header " + command.cfg.TimeField)
			}
			slog.Info("parse header success")
			return nil
		}, nil
	case ImportStateDML:
		return func(ctx context.Context, command *ImportCommand) error {
			if command.fsm.database == "" {
				return errors.New("database is required")
			}
			if command.fsm.retentionPolicy == "" {
				command.fsm.retentionPolicy = "autogen"
			}
			if command.cfg.Measurement == "" {
				return errors.New("measurement is required")
			}
			if len(command.cfg.Fields) == 0 || len(fsm.fieldMap) == 0 {
				return errors.New("--fields is required or field name not in csv header")
			}
			if len(command.fsm.batchLPBuffer) < command.cfg.BatchSize {
				var point = &opengemini.Point{
					Measurement: command.cfg.Measurement,
					Timestamp:   StringToInt64(data[fsm.timeField.Pos]),
					Tags:        make(map[string]string),
					Fields:      make(map[string]interface{}),
				}
				for _, tag := range fsm.tagMap {
					point.Tags[tag.Name] = data[tag.Pos]
				}
				for _, field := range fsm.fieldMap {
					point.Fields[field.Name] = data[field.Pos]
				}

				command.fsm.batchPointBuffer = append(command.fsm.batchPointBuffer, point)
				return nil
			}
			defer func() {
				command.fsm.batchPointBuffer = command.fsm.batchPointBuffer[:0]
			}()
			var err error
			var builderName = command.fsm.database + "." + command.fsm.retentionPolicy
			builder, ok := builderEntities[builderName]
			if !ok {
				builder, err = opengemini.NewWriteRequestBuilder(command.fsm.database, command.fsm.retentionPolicy)
				if err != nil {
					return err
				}
				builderEntities[builderName] = builder
			}
			var recordBuilder = make(map[string]opengemini.RecordBuilder)
			var recordLines []opengemini.RecordLine
			for _, point := range fsm.batchPointBuffer {
				rb, ok := recordBuilder[point.Measurement]
				if !ok {
					rb, err = opengemini.NewRecordBuilder(point.Measurement)
					if err != nil {
						return err
					}
					recordBuilder[point.Measurement] = rb
				}
				newLine := rb.NewLine()
				for key, value := range point.Tags {
					newLine.AddTag(key, value)
				}
				for key, value := range point.Fields {
					newLine.AddField(key, value)
				}
				recordLines = append(recordLines, newLine.Build(point.Timestamp))
			}
			request, err := builder.Authenticate(command.cfg.Username, command.cfg.Password).AddRecord(recordLines...).Build()
			if err != nil {
				return err
			}
			response, err := command.writeClient.Write(ctx, request)
			if err != nil {
				return err
			}
			switch response.Code {
			case 0:
				return nil
			case 1:
				return fmt.Errorf("write failed, code: %d, partial write failure", response.GetCode())
			case 2:
				return fmt.Errorf("write failed, code: %d, write failure", response.GetCode())
			default:
				return fmt.Errorf("unexpected response code: %d", response.Code)
			}
		}, nil
	}
	return FSMCallEmpty, nil
}

func StringToInt64(s string) int64 {
	i, _ := strconv.ParseInt(s, 10, 64)
	return i
}
