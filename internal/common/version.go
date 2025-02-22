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

package common

import (
	"fmt"
	"runtime"
)

// Version information, the value is set by the build script
var (
	Version   string
	GitCommit string
	BuildTime string
)

// FullVersion returns the full version string.
func FullVersion() string {
	const format = `ts-cli ver: %s
git commit: %s
build time: %s
os        : %s
arch      : %s`
	return fmt.Sprintf(format, Version, GitCommit, BuildTime, runtime.GOOS, runtime.GOARCH)
}
