package process

import (
	"fmt"
	"os"
	"path/filepath"
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
