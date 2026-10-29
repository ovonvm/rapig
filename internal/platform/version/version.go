// Package version exposes build metadata injected at link time.
//
// Build scripts inject real values:
//
//	go build -ldflags "-X rapig/internal/platform/version.Version=0.1.0 ..."
package version

import (
	"fmt"
	"runtime"
)

var (
	// Version is the human readable release version, e.g. "0.1.0".
	Version = "dev"
	// Commit is the git commit the binary was built from.
	Commit = "none"
	// BuildTime is the UTC build timestamp, RFC3339.
	BuildTime = "unknown"
)

// Info is the JSON friendly snapshot of build metadata.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Current returns the build metadata of the running binary.
func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		GoVersion: runtime.Version(),
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}
}

// String renders a single human readable line, used by `rapig version`.
func String() string {
	i := Current()
	return fmt.Sprintf("rapig %s commit=%s built=%s go=%s platform=%s",
		i.Version, i.Commit, i.BuildTime, i.GoVersion, i.Platform)
}
