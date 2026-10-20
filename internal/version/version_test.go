package version

import (
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestFromBuildInfoDevelopmentRevision(t *testing.T) {
	build := &debug.BuildInfo{
		GoVersion: "go1.test",
		Main:      debug.Module{Version: "v0.0.0-20260809101112-aaaaaaaaaaaa+dirty"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: strings.Repeat("a", 40)},
			{Key: "vcs.time", Value: "2026-08-09T10:11:12Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	info := fromBuildInfo("", build)
	if info.Version != "devel+aaaaaaaaaaaa" || info.ShortRevision != "aaaaaaaaaaaa" || !info.Modified || info.GoVersion != "go1.test" {
		t.Fatalf("info = %+v", info)
	}
	wantTime := time.Date(2026, 8, 9, 10, 11, 12, 0, time.UTC)
	if info.CommitTime == nil || !info.CommitTime.Equal(wantTime) {
		t.Fatalf("commit time = %v", info.CommitTime)
	}
}

func TestFromBuildInfoReleasePrecedence(t *testing.T) {
	build := &debug.BuildInfo{Main: debug.Module{Version: "v0.2.0"}}
	if info := fromBuildInfo("", build); info.Version != "v0.2.0" || info.Release != "v0.2.0" {
		t.Fatalf("module release = %+v", info)
	}
	if info := fromBuildInfo("v0.3.0-local", build); info.Version != "v0.3.0-local" || info.Release != "v0.3.0-local" {
		t.Fatalf("linked release = %+v", info)
	}
}

func TestSourceSnapshotIdentity(t *testing.T) {
	build := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.2.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "stale"},
			{Key: "vcs.time", Value: "2026-08-09T10:11:12Z"},
		},
	}
	for _, tc := range []struct {
		name, revision, modified, wantVersion string
		wantModified                          bool
	}{
		{"clean", strings.Repeat("b", 40), "false", "devel+bbbbbbbbbbbb", false},
		{"dirty", strings.Repeat("b", 40), "true", "devel+bbbbbbbbbbbb", true},
		{"unknown", "", "true", "devel", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, metadata := range []*debug.BuildInfo{nil, build} {
				info := fromBuildInfoWithSource("", metadata, tc.revision, tc.modified)
				if info.Version != tc.wantVersion || info.Revision != tc.revision || info.Modified != tc.wantModified || info.Release != "" || info.CommitTime != nil {
					t.Fatalf("info = %+v", info)
				}
			}
		})
	}
	if info := fromBuildInfoWithSource("v0.3.0", build, strings.Repeat("b", 40), "false"); info.Release != "v0.3.0" || info.Version != "v0.3.0" {
		t.Fatalf("explicit release = %+v", info)
	}
}

func TestFromBuildInfoWithoutVCS(t *testing.T) {
	info := fromBuildInfo("", nil)
	if info.Version != "devel" || info.Revision != "" || info.ShortRevision != "" {
		t.Fatalf("info = %+v", info)
	}
}
