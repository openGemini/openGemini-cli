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

package core

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openGemini/opengemini-client-go/opengemini"
	"golang.org/x/term"

	"github.com/openGemini/openGemini-cli/internal/geminiql"
	"github.com/openGemini/openGemini-cli/internal/prompt"
)

type CommandLine struct {
	config     *CommandLineConfig
	httpClient HttpClient
	parser     geminiql.QLParser
	prompt     *prompt.Prompt

	executeAt time.Time
	timer     bool
	debug     bool
	suggest   bool
}

func NewCommandLine(cfg *CommandLineConfig) *CommandLine {
	var cl = &CommandLine{
		config: cfg,
		parser: geminiql.QLNewParser(),
	}
	cl.prompt = prompt.NewPrompt(cl.executor)
	return cl
}

func (cl *CommandLine) executor(input string) {
	// no input nothing to do
	if input == "" {
		return
	}

	// input token to exit program
	if input == "quit" || input == "exit" || input == "\\q" {
		cl.prompt.Destruction(nil)
	}

	ast := &geminiql.QLAst{}
	lexer := geminiql.QLNewLexer(geminiql.NewTokenizer(strings.NewReader(input)), ast)
	cl.parser.Parse(lexer)

	cl.executeAt = time.Now()
	defer cl.elapse()

	var err error
	// parse token success
	if ast.Error == nil {
		err = cl.executeOnLocal(ast.Stmt)
	} else {
		err = cl.executeOnRemote(input)
	}
	if err != nil {
		fmt.Printf("error: %s\n", err)
	}
}

func (cl *CommandLine) elapse() {
	d := time.Since(cl.executeAt)
	fmt.Printf("Elapsed: %v\n", d)
}

func (cl *CommandLine) executeOnLocal(stmt geminiql.Statement) error {
	switch stmt := stmt.(type) {
	case *geminiql.UseStatement:
		return cl.executeUse(stmt)
	case *geminiql.PrecisionStatement:
		return cl.executePrecision(stmt)
	case *geminiql.HelpStatement:
		return cl.executeHelp(stmt)
	case *geminiql.AuthStatement:
		return cl.executeAuth(stmt)
	case *geminiql.TimerStatement:
		return cl.executeTimer(stmt)
	case *geminiql.DebugStatement:
		return cl.executeDebug(stmt)
	case *geminiql.PromptStatement:
		return cl.executePrompt(stmt)
	case *geminiql.InsertStatement:
		return nil
	case *geminiql.ChunkedStatement:
		return nil
	case *geminiql.ChunkSizeStatement:
		return nil

	default:
		return fmt.Errorf("unsupport stmt %s", stmt)
	}
}

func (cl *CommandLine) executeOnRemote(s string) error {
	return nil
}

func (cl *CommandLine) Run() {
	cl.prompt.Run()
}

func (cl *CommandLine) executeUse(stmt *geminiql.UseStatement) error {
	cl.config.Database = stmt.DB
	if stmt.RP == "" {
		cl.config.RetentionPolicy = ""
	} else {
		cl.config.RetentionPolicy = stmt.RP
	}
	fmt.Println(cl.config.RetentionPolicy, cl.config.Database)
	return nil
}

func (cl *CommandLine) executePrecision(stmt *geminiql.PrecisionStatement) error {
	precision := strings.ToLower(stmt.Precision)
	switch precision {
	case "":
		cl.config.precision = "ns"
	case "h", "m", "s", "ms", "u", "ns", "rfc3339":
		cl.config.precision = precision
	default:
		return fmt.Errorf("unknown precision %q. precision must be rfc3339, h, m, s, ms, u or ns", precision)
	}
	return nil
}

func (cl *CommandLine) executeHelp(stmt *geminiql.HelpStatement) error {
	fmt.Println(
		`Usage:
  exit/quit/ctrl+d        quit the openGemini shell
  auth                    prompt for username and password
  use <db>[.rp]           set current database and optional retention policy
  precision <format>      specifies the format of the timestamp: rfc3339, h, m, s, ms, u or ns
  show cluster            show cluster node status information
  show users              show all existing users and their permission status
  show databases          show a list of all databases on the cluster
  show measurements       show measurement information on the database.retention_policy
  show series             show series information
  show tag keys           show tag key information
  show field keys         show field key information
  timer                   display execution time
  debug                   display http request interaction content
  prompt                  enable command line reminder and suggestion
  
  A full list of openGemini commands can be found at:
  https://docs.opengemini.org
	`)
	return nil
}

func (cl *CommandLine) executeAuth(stmt *geminiql.AuthStatement) error {
	fmt.Printf("username: ")
	_, _ = fmt.Scanf("%s\n", &cl.config.Username)
	fmt.Printf("password: ")
	password, _ := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Printf("\n")
	cl.config.Password = string(password)
	// TODO refresh httpClient auth information
	return nil
}

func (cl *CommandLine) executeTimer(stmt *geminiql.TimerStatement) error {
	// switch timer model enable or disable
	cl.timer = !cl.timer
	displayFlag := "disabled"
	if cl.timer {
		displayFlag = "enabled"
	}
	fmt.Printf("Timer is %s\n", displayFlag)
	return nil
}

func (cl *CommandLine) executeDebug(stmt *geminiql.DebugStatement) error {
	// switch debug model enable or disable
	cl.debug = !cl.debug
	displayFlag := "disabled"
	if cl.debug {
		displayFlag = "enabled"
	}
	fmt.Printf("Debug is %s\n", displayFlag)
	return nil
}

func (cl *CommandLine) executePrompt(stmt *geminiql.PromptStatement) error {
	// switch suggest model enable or disable
	cl.suggest = !cl.suggest
	displayFlag := "disabled"
	if cl.suggest {
		displayFlag = "enabled"
	}
	fmt.Printf("Prompt is %s\n", displayFlag)
	return nil
}

func (cl *CommandLine) executeInsert(stmt *geminiql.InsertStatement) error {
	var point = &opengemini.Point{}

	if err := cl.httpClient.Write(cl.config.Database, cl.config.RetentionPolicy, point); err != nil {
		return err
	}
	return nil
}
