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
	"fmt"

	"github.com/spf13/cobra"

	"github.com/openGemini/openGemini-cli/internal/common"
	"github.com/openGemini/openGemini-cli/internal/core"
)

type Command struct {
	cmd     *cobra.Command
	options *core.CommandLineConfig
}

func (m *Command) rootCommand() {
	m.cmd = &cobra.Command{
		Use:   "ts-cli",
		Short: "openGemini client interactive CLI.",
		Long:  `CNCF openGemini client interactive command-line interface.`,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd:   true,
			DisableNoDescFlag:   true,
			DisableDescriptions: true,
		},
		Run: func(cmd *cobra.Command, args []string) {
			core.NewCommandLine(m.options).Run()
		},
	}
	m.cmd.Flags().StringVarP(&m.options.Host, "host", "H", common.DefaultHost, "ts-sql host to connect to.")
	m.cmd.Flags().IntVarP(&m.options.Port, "port", "p", common.DefaultHttpPort, "ts-sql tcp port to connect to.")
	m.cmd.Flags().StringVarP(&m.options.UnixSocket, "socket", "S", "", "openGemini unix domain socket to connect to. ")
	m.cmd.Flags().IntVarP(&m.options.Timeout, "timeout", "t", common.DefaultRequestTimeout, "request-timeout in mill-seconds.")
	m.cmd.Flags().StringVarP(&m.options.Username, "username", "u", "", "username to connect to openGemini.")
	m.cmd.Flags().StringVarP(&m.options.Password, "password", "P", "", "password to connect to openGemini.")
	m.cmd.Flags().BoolVarP(&m.options.EnableTls, "ssl", "s", false, "use https for connecting to openGemini.")
	m.cmd.Flags().BoolVarP(&m.options.InsecureTls, "insecure-tls", "i", false, "ignore ssl verification when connecting openGemini by https.")
	m.cmd.Flags().StringVarP(&m.options.CACert, "cacert", "c", "", "CA certificate to verify peer against when connecting openGemini by https.")
	m.cmd.Flags().StringVarP(&m.options.Cert, "cert", "C", "", "client certificate file when connecting openGemini by https.")
	m.cmd.Flags().StringVarP(&m.options.CertKey, "cert-key", "k", "", "client certificate password.")
	m.cmd.Flags().BoolVarP(&m.options.InsecureHostname, "insecure-hostname", "I", false, "ignore server certificate hostname verification when connecting openGemini by https.")
	m.cmd.Flags().StringVarP(&m.options.Database, "database", "d", "", "database to connect to openGemini.")
}

func (m *Command) versionCommand() {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "version for openGemini CLI",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(common.FullVersion())
		},
	}
	m.cmd.AddCommand(cmd)
}

func (m *Command) load() {
	m.rootCommand()
	m.versionCommand()
}

func (m *Command) Execute() error {
	return m.cmd.Execute()
}

func main() {
	var command = &Command{options: new(core.CommandLineConfig)}
	command.load()
	if err := command.Execute(); err != nil {
		fmt.Printf("execute command failed: %s\n", err)
	}
}
