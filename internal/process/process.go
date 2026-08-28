package process

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/regb/workitem/internal/model"
)

// Info is a small Linux /proc process snapshot.
type Info = model.ProcessInfo

type Inspector struct {
	ProcRoot string
}

func New() Inspector { return Inspector{ProcRoot: "/proc"} }

func (i Inspector) Alive(pid int) bool {
	info, err := i.Info(pid)
	return err == nil && info.State != "Z"
}

func (i Inspector) Info(pid int) (Info, error) {
	if pid <= 0 {
		return Info{}, fmt.Errorf("invalid process id %d", pid)
	}
	return i.readInfo(pid)
}

// TerminateGroup sends SIGTERM to a runtime process group. Agent runtimes are
// launched as session leaders, so the group includes the wi owner and its Pi
// child while excluding the calling CLI.
func (i Inspector) TerminateGroup(rootPID int) error {
	if rootPID <= 0 {
		return fmt.Errorf("invalid process group leader %d", rootPID)
	}
	if err := syscall.Kill(-rootPID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("terminate process group %d: %w", rootPID, err)
	}
	return nil
}

func (i Inspector) FindDescendant(rootPID int, needles []string) (Info, bool, error) {
	if rootPID <= 0 {
		return Info{}, false, nil
	}
	root, err := i.readInfo(rootPID)
	if err != nil {
		if os.IsNotExist(err) {
			return Info{}, false, nil
		}
		return Info{}, false, err
	}
	if root.State != "Z" && matches(root, needles) {
		return root, true, nil
	}
	queue, err := i.directChildren(rootPID)
	if err != nil {
		return i.findDescendantSnapshot(rootPID, needles)
	}
	seen := map[int]bool{rootPID: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		info, err := i.readInfo(pid)
		if err != nil || info.State == "Z" {
			continue
		}
		if matches(info, needles) {
			return info, true, nil
		}
		children, err := i.directChildren(pid)
		if err == nil {
			queue = append(queue, children...)
		}
	}
	return Info{}, false, nil
}

func (i Inspector) directChildren(pid int) ([]int, error) {
	root := i.ProcRoot
	if root == "" {
		root = "/proc"
	}
	contents, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "task", strconv.Itoa(pid), "children"))
	if err != nil {
		return nil, err
	}
	children := []int{}
	for _, field := range strings.Fields(string(contents)) {
		child, err := strconv.Atoi(field)
		if err == nil && child > 0 {
			children = append(children, child)
		}
	}
	sort.Ints(children)
	return children, nil
}

func (i Inspector) findDescendantSnapshot(rootPID int, needles []string) (Info, bool, error) {
	infos, err := i.snapshot()
	if err != nil {
		return Info{}, false, err
	}
	children := map[int][]int{}
	for pid, info := range infos {
		children[info.PPID] = append(children[info.PPID], pid)
	}
	for _, pids := range children {
		sort.Ints(pids)
	}
	queue := append([]int{}, children[rootPID]...)
	seen := map[int]bool{rootPID: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		info, ok := infos[pid]
		if !ok || info.State == "Z" {
			continue
		}
		if matches(info, needles) {
			return info, true, nil
		}
		queue = append(queue, children[pid]...)
	}
	return Info{}, false, nil
}

func (i Inspector) snapshot() (map[int]Info, error) {
	if runtime.GOOS == "darwin" && (i.ProcRoot == "" || i.ProcRoot == "/proc") {
		return darwinSnapshot()
	}
	root := i.ProcRoot
	if root == "" {
		root = "/proc"
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	infos := map[int]Info{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		info, err := i.readInfo(pid)
		if err != nil {
			continue
		}
		infos[pid] = info
	}
	return infos, nil
}

func (i Inspector) readInfo(pid int) (Info, error) {
	if runtime.GOOS == "darwin" && (i.ProcRoot == "" || i.ProcRoot == "/proc") {
		infos, err := darwinSnapshot()
		if err != nil {
			return Info{}, err
		}
		info, ok := infos[pid]
		if !ok {
			return Info{}, os.ErrNotExist
		}
		return info, nil
	}
	root := i.ProcRoot
	if root == "" {
		root = "/proc"
	}
	statPath := filepath.Join(root, strconv.Itoa(pid), "stat")
	b, err := os.ReadFile(statPath)
	if err != nil {
		return Info{}, err
	}
	info, err := parseStat(pid, string(b))
	if err != nil {
		return Info{}, err
	}
	if cmdline, err := readCmdline(filepath.Join(root, strconv.Itoa(pid), "cmdline")); err == nil {
		info.Cmdline = cmdline
	}
	return info, nil
}

func darwinSnapshot() (map[int]Info, error) {
	// lstart is stable for the life of a PID and avoids relying on Linux /proc.
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,pgid=,state=,lstart=,comm=,args=").Output()
	if err != nil {
		return nil, fmt.Errorf("inspect processes with ps: %w", err)
	}
	infos := map[int]Info{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		pid, e1 := strconv.Atoi(fields[0])
		ppid, e2 := strconv.Atoi(fields[1])
		pgrp, e3 := strconv.Atoi(fields[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		// ps lstart occupies fields 4 through 8. Hash its text into the durable
		// numeric identity used by the existing process contract.
		startText := strings.Join(fields[4:9], " ")
		var start uint64 = 1469598103934665603
		for j := 0; j < len(startText); j++ {
			start ^= uint64(startText[j])
			start *= 1099511628211
		}
		command := filepath.Base(fields[9])
		args := append([]string(nil), fields[10:]...)
		if len(args) == 0 {
			args = []string{fields[9]}
		}
		infos[pid] = Info{PID: pid, PPID: ppid, PGRP: pgrp, State: fields[3], StartTime: start, Command: command, Cmdline: args}
	}
	return infos, nil
}

func parseStat(pid int, stat string) (Info, error) {
	open := strings.IndexByte(stat, '(')
	close := strings.LastIndexByte(stat, ')')
	if open < 0 || close <= open {
		return Info{}, fmt.Errorf("invalid proc stat for pid %d", pid)
	}
	command := stat[open+1 : close]
	rest := strings.Fields(stat[close+1:])
	if len(rest) < 20 {
		return Info{}, fmt.Errorf("invalid proc stat fields for pid %d", pid)
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return Info{}, err
	}
	pgrp, err := strconv.Atoi(rest[2])
	if err != nil {
		return Info{}, err
	}
	startTime, err := strconv.ParseUint(rest[19], 10, 64)
	if err != nil {
		return Info{}, err
	}
	return Info{PID: pid, PPID: ppid, PGRP: pgrp, StartTime: startTime, State: rest[0], Command: command}, nil
}

func readCmdline(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		return nil, nil
	}
	parts := bytes.Split(b, []byte{0})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			out = append(out, string(part))
		}
	}
	return out, nil
}

func matches(info Info, needles []string) bool {
	if len(needles) == 0 {
		return false
	}
	command := strings.ToLower(info.Command)
	line := strings.ToLower(info.CommandLine())
	for _, needle := range needles {
		needle = strings.ToLower(strings.TrimSpace(needle))
		if needle == "" {
			continue
		}
		if command == needle {
			return true
		}
		for _, arg := range info.Cmdline {
			base := strings.ToLower(filepath.Base(arg))
			if base == needle {
				return true
			}
		}
		// Short needles such as "pi" are only matched exactly above; substring
		// matching them would confuse unrelated commands like python/pip.
		if len(needle) > 2 && strings.Contains(line, needle) {
			return true
		}
	}
	return false
}
