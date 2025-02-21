package common

import (
	"fmt"
	"runtime"
)

// Version information, the value is set by the build script
var (
	Version   string
	GitCommit string
	GitBranch string
	BuildTime string
)

// FullVersion returns the full version string.
func FullVersion(app string) string {
	const format = `openGemini version info:
%s: %s
git: %s %s
os: %s
arch: %s`

	return fmt.Sprintf(format, app, Version, GitBranch, GitCommit, runtime.GOOS, runtime.GOARCH)
}
