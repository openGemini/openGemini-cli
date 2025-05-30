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
	ColumnWrite     bool
	ColumnWritePort int
	BatchSize       int
}

type ImportCommand struct {
	cfg         *ImportConfig
	httpClient  core.HttpClient
	writeClient proto.WriteServiceClient
	fsm         *ImportFileFSM
}

func (c *ImportCommand) Run(config *ImportConfig) error {
	httpClient, err := core.NewHttpClient(config.CommandLineConfig)
	if err != nil {
		slog.Error("create http client failed", "reason", err)
		return err
	}
	c.httpClient = httpClient
	if config.ColumnWritePort == 0 {
		config.ColumnWritePort = 8035
	}
	if config.ColumnWrite {
		c.writeClient, err = NewColumnWriterClient(config)
		if err != nil {
			slog.Error("create column writer client failed", "reason", err)
			return err
		}
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
	scanner := bufio.NewReader(file)
	var ctx = context.Background()
	for {
		line, err := scanner.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			slog.Error("read line failed", "reason", err)
			continue
		}
		fsmCall, err := c.fsm.process(ctx, string(line))
		if err != nil {
			slog.Error("process line failed", "reason", err)
			continue
		}
		err = fsmCall(ctx, c)
		if err != nil {
			slog.Error("call fsm function failed", "reason", err)
			continue
		}
	}
	slog.Info("process finished", "path", c.cfg.Path)
	return nil
}

type ImportState int

const (
	ImportStateDDL = iota
	ImportStateDML
)

type ImportFileFSM struct {
	state           ImportState
	database        string
	retentionPolicy string
	batchBuffer     []string
}

type FSMCall func(ctx context.Context, command *ImportCommand) error

var FSMCallEmpty = func(ctx context.Context, command *ImportCommand) error { return nil }

func (fsm *ImportFileFSM) process(ctx context.Context, data string) (FSMCall, error) {
	if strings.HasPrefix(data, "# DDL") {
		fsm.state = ImportStateDDL
		return FSMCallEmpty, nil
	}
	if strings.HasPrefix(data, "# DML") {
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
		if strings.HasPrefix(data, "# CONTEXT-DATABASE:") {
			fsm.database = strings.TrimSpace(strings.Split(data, ":")[1])
			return FSMCallEmpty, nil
		}
		if strings.HasPrefix(data, "# CONTEXT-RETENTION-POLICY:") {
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
			if len(command.fsm.batchBuffer) < command.cfg.BatchSize {
				command.fsm.batchBuffer = append(command.fsm.batchBuffer, data)
				return nil
			}
			defer func() {
				// clear batch buffer
				command.fsm.batchBuffer = command.fsm.batchBuffer[:0]
			}()
			var err error
			var lines = strings.Join(command.fsm.batchBuffer, "\n")
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
