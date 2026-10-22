package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestParseStatHandlesCommandWithSpaces(t *testing.T) {
	info, err := parseStat(123, "123 (pi worker) S 45 123 1 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 999")
	if err != nil {
		t.Fatal(err)
	}
	if info.PID != 123 || info.PPID != 45 || info.PGRP != 123 || info.StartTime != 999 || info.Command != "pi worker" || info.State != "S" {
		t.Fatalf("info = %+v", info)
	}
}

func TestParseDarwinSnapshotNormalizesProcessState(t *testing.T) {
	infos := parseDarwinSnapshot("42 1 42 Z+ Fri Aug 28 16:00:00 2026 /usr/bin/pi /usr/bin/pi --rpc\n43 1 43 S+ Fri Aug 28 16:00:01 2026 /bin/sh /bin/sh")
	zombie := infos[42]
	if zombie.State != "Z" || zombie.Command != "pi" || zombie.PID != 42 || zombie.PPID != 1 || zombie.PGRP != 42 || zombie.StartTime == 0 {
		t.Fatalf("zombie = %+v", zombie)
	}
	if len(zombie.Cmdline) != 2 || zombie.Cmdline[0] != "/usr/bin/pi" || zombie.Cmdline[1] != "--rpc" {
		t.Fatalf("zombie command line = %q", zombie.Cmdline)
	}
	if state := infos[43].State; state != "S" {
		t.Fatalf("running state = %q", state)
	}
}

func TestFindDescendantUsesTargetedChildrenTraversal(t *testing.T) {
	root := t.TempDir()
	writeProcess := func(pid, ppid int, command, children string) {
		dir := filepath.Join(root, fmt.Sprint(pid))
		if err := os.MkdirAll(filepath.Join(dir, "task", fmt.Sprint(pid)), 0o700); err != nil {
			t.Fatal(err)
		}
		stat := fmt.Sprintf("%d (%s) S %d %d 1 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 999", pid, command, ppid, pid)
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(command+"\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "task", fmt.Sprint(pid), "children"), []byte(children), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeProcess(100, 1, "shell", "101")
	writeProcess(101, 100, "node", "102")
	writeProcess(102, 101, "pi", "")
	writeProcess(999, 1, "pi", "") // unrelated and must not be selected
	found, ok, err := (Inspector{ProcRoot: root}).FindDescendant(100, []string{"pi"})
	if err != nil || !ok || found.PID != 102 {
		t.Fatalf("found=%+v ok=%v err=%v", found, ok, err)
	}
}

func TestMatchesCommandLine(t *testing.T) {
	info := Info{PID: 1, Command: "node", Cmdline: []string{"node", "/x/@earendil-works/pi-coding-agent/dist/index.js"}}
	if !matches(info, []string{"pi-coding-agent"}) {
		t.Fatal("expected command line match")
	}
	python := Info{PID: 2, Command: "python", Cmdline: []string{"python", "script.py"}}
	if matches(python, []string{"pi"}) {
		t.Fatal("did not expect short substring match")
	}
}

// A runtime's identity is its start time as ps reports it; reading one
// process and reading the whole table must agree on it.
func TestSingleProcessAndSnapshotAgreeOnIdentity(t *testing.T) {
	inspector := New()
	single, err := inspector.Info(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := inspector.Snapshot().Info(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if single.StartTime == 0 || single.StartTime != snapshot.StartTime || single.PGRP != snapshot.PGRP || single.PPID != snapshot.PPID {
		t.Fatalf("single=%+v snapshot=%+v", single, snapshot)
	}
	if _, err := inspector.Info(1 << 22); err == nil {
		t.Fatal("a missing process was found")
	}
}

// Identity must not depend on the locale of the process that reads it: a
// runtime host records its own start time and the daemon checks it later.
func TestDarwinIdentityIgnoresTheCallersLocale(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("ps start times are only read on macOS")
	}
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	got, err := New().Info(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/ps", "-o", "pid=,ppid=,pgid=,state=,lstart=,comm=,args=", "-p", strconv.Itoa(os.Getpid()))
	command.Env = append(os.Environ(), "LC_ALL=C")
	out, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := parseDarwinSnapshot(string(out))[os.Getpid()]
	if got.StartTime != want.StartTime {
		t.Fatalf("identity read in the caller's locale: got %d want %d", got.StartTime, want.StartTime)
	}
}

func TestStartedAsAcceptsTheLegacyIdentity(t *testing.T) {
	info := Info{StartTime: 7, LegacyStartTime: 9}
	if !info.StartedAs(7) || !info.StartedAs(9) || info.StartedAs(8) || (Info{}).StartedAs(0) {
		t.Fatal("StartedAs")
	}
}
