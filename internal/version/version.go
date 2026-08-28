package version

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// release is set for release builds with:
//
//	-ldflags "-X github.com/regb/workitem/internal/version.release=v0.1.0"
//
// Development builds need no linker flags; Go embeds the VCS revision, time,
// and modified state automatically when built from a Git checkout.
var release string

var buildIdentityOnce = sync.OnceValue(func() string {
	info := Current()
	executable, err := os.Executable()
	if err != nil {
		return info.Version
	}
	stat, err := os.Stat(executable)
	if err != nil {
		return info.Version
	}
	// The daemon captures this at startup. Replacing an installed dirty build
	// changes the file metadata observed by later CLI processes even when the
	// embedded VCS revision remains unchanged.
	return fmt.Sprintf("%s:%d:%d", info.Version, stat.Size(), stat.ModTime().UnixNano())
})

func BuildIdentity() string { return buildIdentityOnce() }

type Info struct {
	Version       string     `json:"version"`
	Release       string     `json:"release,omitempty"`
	Revision      string     `json:"revision,omitempty"`
	ShortRevision string     `json:"short_revision,omitempty"`
	CommitTime    *time.Time `json:"commit_time,omitempty"`
	Modified      bool       `json:"modified"`
	GoVersion     string     `json:"go_version"`
}

func Current() Info {
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return fromBuildInfo(strings.TrimSpace(release), nil)
	}
	return fromBuildInfo(strings.TrimSpace(release), build)
}

func fromBuildInfo(linkedRelease string, build *debug.BuildInfo) Info {
	info := Info{Release: linkedRelease, GoVersion: runtime.Version()}
	moduleVersion := ""
	if build != nil {
		if build.GoVersion != "" {
			info.GoVersion = build.GoVersion
		}
		moduleVersion = strings.TrimSpace(build.Main.Version)
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				info.Revision = strings.TrimSpace(setting.Value)
			case "vcs.time":
				if parsed, err := time.Parse(time.RFC3339, setting.Value); err == nil {
					info.CommitTime = &parsed
				}
			case "vcs.modified":
				info.Modified = setting.Value == "true"
			}
		}
	}
	info.ShortRevision = shortRevision(info.Revision)
	// A local Go build may synthesize a pseudo module version from the current
	// commit. Prefer the explicit VCS revision in that case. A module version is
	// only a release fallback for builds that have no checkout VCS settings,
	// such as `go install module/cmd@vX.Y.Z`.
	if info.Release == "" && info.Revision == "" && moduleVersion != "" && moduleVersion != "(devel)" {
		info.Release = moduleVersion
	}
	if info.Release != "" {
		info.Version = info.Release
	} else if info.ShortRevision != "" {
		info.Version = "devel+" + info.ShortRevision
	} else {
		info.Version = "devel"
	}
	return info
}

func shortRevision(revision string) string {
	revision = strings.TrimSpace(revision)
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}
