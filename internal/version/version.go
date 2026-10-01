// Package version carries build identity for the binary and its HTTP responses.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"time"
)

// Build-time variables, set with -ldflags, for example:
//
//	go build -ldflags "-X github.com/corerouter/corerouter/internal/version.Version=1.2.3"
//
// The defaults deliberately describe an unstamped local build rather than
// pretending to be a release, so an operator can tell at a glance that a binary
// did not come from the release pipeline.
var (
	// Version is the semantic version, or "dev" for an unstamped build.
	Version = "dev"
	// Commit is the git revision the binary was built from.
	Commit = "unknown"
	// BuildDate is the RFC 3339 build timestamp.
	BuildDate = "unknown"
	// Dirty marks a build from a modified working tree.
	Dirty = ""
)

// Info describes a build in a form that is safe to expose publicly: it contains
// no absolute paths and no environment details.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
	Dirty     bool   `json:"dirty"`
}

// Current returns the build information, filling in the Go toolchain details
// from the runtime so an operator can correlate a crash with a specific
// toolchain.
func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		Dirty:     Dirty != "",
	}
}

// Short returns a compact one-line version string for logs and banners.
func Short() string {
	s := Version
	if Commit != "unknown" && Commit != "" {
		s += "+" + shortCommit()
	}
	if Dirty != "" {
		s += ".dirty"
	}
	return s
}

// String renders the full version line used by the --version flag.
func String() string {
	info := Current()
	return fmt.Sprintf("corerouter %s (commit %s, built %s, %s %s)",
		info.Version, info.Commit, info.BuildDate, info.GoVersion, info.Platform)
}

func shortCommit() string {
	if len(Commit) > 12 {
		return Commit[:12]
	}
	return Commit
}

// RevisionFromBuildInfo recovers the VCS revision when ldflags were not used,
// which happens with plain `go build` inside a git checkout. It lets a
// developer build behave like a real one without extra flags.
func RevisionFromBuildInfo() (revision string, modified bool, when time.Time) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false, time.Time{}
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		case "vcs.time":
			if t, err := time.Parse(time.RFC3339, setting.Value); err == nil {
				when = t
			}
		}
	}
	return revision, modified, when
}

// init backfills the build stamp from Go's embedded VCS metadata when the
// linker did not supply one, so `go build` and `go run` still report a revision.
func init() {
	if Commit != "unknown" && Commit != "" {
		return
	}
	rev, modified, when := RevisionFromBuildInfo()
	if rev == "" {
		return
	}
	Commit = rev
	if modified {
		Dirty = "true"
	}
	if BuildDate == "unknown" && !when.IsZero() {
		BuildDate = when.UTC().Format(time.RFC3339)
	}
}
