// Package buildinfo holds the version, commit and build date of the binary.
//
// The release build sets the three values with -ldflags -X (see the Makefile).
// Without them (go run, go test, go build without flags) the version reads
// "dev", and the commit and date fall back to the VCS data the Go toolchain
// embeds in the binary.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Set by -ldflags "-X github.com/guided-traffic/trove/internal/buildinfo.version=…".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// Info describes one build.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"goVersion"`
}

// Get returns the build information of the running binary.
func Get() Info {
	info := Info{Version: version, Commit: commit, Date: date, GoVersion: runtime.Version()}
	if info.Commit != "" && info.Date != "" {
		return info
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch {
			case s.Key == "vcs.revision" && info.Commit == "":
				info.Commit = s.Value
			case s.Key == "vcs.time" && info.Date == "":
				info.Date = s.Value
			}
		}
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	if info.Date == "" {
		info.Date = "unknown"
	}
	return info
}

// String renders the one-line form printed by `trove --version`.
func (i Info) String() string {
	return fmt.Sprintf("trove %s (commit %s, built %s, %s)", i.Version, i.Commit, i.Date, i.GoVersion)
}
