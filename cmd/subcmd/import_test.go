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
	"testing"

	"github.com/openGemini/openGemini-cli/core"
	"github.com/stretchr/testify/require"
)

func TestParseTimestamp(t *testing.T) {
	type testCase struct {
		timestamp string
		precision string
		expect    int64
	}

	testCases := []testCase{
		{"1234567890", "s", 1234567890000000000},
		{"1234567890", "ms", 1234567890000000},
		{"1234567890", "us", 1234567890000},
		{"1234567890", "ns", 1234567890},
		{"1234567890000000000", "", 1234567890000000000},
	}

	for _, tcase := range testCases {
		t.Run(tcase.precision, func(t *testing.T) {
			c := new(ImportCommand)
			cfg := &ImportConfig{CommandLineConfig: new(core.CommandLineConfig)}
			cfg.Precision = tcase.precision
			c.cfg = cfg
			c.cfg.configTimeMultiplier()
			act := c.parseTimestamp2Int64(tcase.timestamp)
			require.Equal(t, tcase.expect, act)
		})
	}
}
