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

	subcmd "github.com/openGemini/openGemini-cli/cmd/subcmd"
	"github.com/openGemini/openGemini-cli/common"
	"github.com/openGemini/openGemini-cli/core"
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
			DisableNoDescFlag:   true,
			DisableDescriptions: true,
			HiddenDefaultCmd:    true,
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

	m.cmd.MarkFlagsRequiredTogether("username", "password")
	m.cmd.MarkFlagsRequiredTogether("cert", "cert-key")
}

func (m *Command) versionCommand() {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "version for openGemini CLI",
		CompletionOptions: cobra.CompletionOptions{
			DisableNoDescFlag:   true,
			DisableDescriptions: true,
			HiddenDefaultCmd:    true,
		},
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(common.FullVersion())
		},
	}
	m.cmd.AddCommand(cmd)
}

func (m *Command) importCommand() {
	var config = subcmd.ImportConfig{CommandLineConfig: new(core.CommandLineConfig)}
	cmd := &cobra.Command{
		Use:     "import",
		Short:   "import data to openGemini",
		Long:    "import line protocol text file to openGemini",
		Example: "ts-cli import --format csv --host localhost --port 8086 --path file.csv --precision=s --database db0 -m m0 -r autogen",
		CompletionOptions: cobra.CompletionOptions{
			DisableNoDescFlag:   true,
			DisableDescriptions: true,
			HiddenDefaultCmd:    true,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			importCmd := new(subcmd.ImportCommand)
			return importCmd.Run(&config)
		},
	}
	cmd.Flags().StringVarP(&config.Host, "host", "H", common.DefaultHost, "ts-sql host to connect to.")
	cmd.Flags().IntVarP(&config.Port, "port", "p", common.DefaultHttpPort, "ts-sql tcp port to connect to.")
	cmd.Flags().IntVarP(&config.Timeout, "timeout", "", common.DefaultRequestTimeout, "request-timeout in mill-seconds.")
	cmd.Flags().StringVarP(&config.Username, "username", "u", "", "username to connect to openGemini.")
	cmd.Flags().StringVarP(&config.Password, "password", "P", "", "password to connect to openGemini.")
	cmd.Flags().BoolVarP(&config.EnableTls, "ssl", "s", false, "use https for connecting to openGemini.")
	cmd.Flags().BoolVarP(&config.InsecureTls, "insecure-tls", "i", false, "ignore ssl verification when connecting openGemini by https.")
	cmd.Flags().StringVarP(&config.CACert, "cacert", "c", "", "CA certificate to verify peer against when connecting openGemini by https.")
	cmd.Flags().StringVarP(&config.Cert, "cert", "C", "", "client certificate file when connecting openGemini by https.")
	cmd.Flags().StringVarP(&config.CertKey, "cert-key", "k", "", "client certificate password.")
	cmd.Flags().BoolVarP(&config.InsecureHostname, "insecure-hostname", "I", false, "ignore server certificate hostname verification when connecting openGemini by https.")
	cmd.Flags().BoolVarP(&config.ColumnWrite, "column-write", "w", false, "use high performance column writing protocol, default use line protocol.")
	cmd.Flags().IntVarP(&config.ColumnWritePort, "column-write-port", "W", common.DefaultColumnWritePort, "high performance column writing protocol service port.")
	cmd.Flags().IntVarP(&config.BatchSize, "batch-size", "b", common.DefaultBatchSize, "enable batch submission to improve write performance.")
	cmd.Flags().StringVarP(&config.Path, "path", "T", "", "import file path to store openGemini.")
	cmd.Flags().StringVarP(&config.Format, "format", "f", common.DefaultFormat, "import file format, support 'line_protocol', 'csv'.")
	cmd.Flags().StringSliceVarP(&config.Tags, "tags", "", nil, "measurement tags name.")
	cmd.Flags().StringSliceVarP(&config.Fields, "fields", "", nil, "measurement fields name, if not specified, the remaining columns will act as fields.")
	cmd.Flags().StringVarP(&config.Measurement, "measurement", "m", "", "measurement name.")
	cmd.Flags().StringVarP(&config.Database, "database", "d", "", "database name.")
	cmd.Flags().StringVarP(&config.TimeField, "time", "t", "time", "measurement timestamp name.")
	cmd.Flags().StringVarP(&config.RetentionPolicy, "retention-policy", "r", common.DefaultRetentionPolicy, "measurement retention policy.")
	cmd.Flags().StringVarP(&config.Precision, "precision", "U", "ns", "precision for time unit conversion, support 's', 'ms', 'us', 'ns'.")

	cmd.MarkFlagsRequiredTogether("username", "password")
	cmd.MarkFlagsRequiredTogether("cert", "cert-key")
	m.cmd.AddCommand(cmd)
}

func (m *Command) load() {
	m.rootCommand()
	m.versionCommand()
	m.importCommand()
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
