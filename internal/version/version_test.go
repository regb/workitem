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

func TestFromBuildInfoWithoutVCS(t *testing.T) {
	info := fromBuildInfo("", nil)
	if info.Version != "devel" || info.Revision != "" || info.ShortRevision != "" {
		t.Fatalf("info = %+v", info)
	}
}
