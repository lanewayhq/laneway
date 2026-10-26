package testkit

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// killLeftovers kills, and fails the test for, every process still running
// in a directory under root (the test's temp dirs, which hold its Worktrees)
// once the daemon has stopped. Stopping the daemon must stop every Process
// it started; one left behind would outlive the test run and hold its port.
func killLeftovers(t testing.TB, root string) {
	procs, err := os.ReadDir("/proc")
	if err != nil {
		t.Errorf("testkit: looking for leftover processes: %v", err)
		return
	}
	for _, e := range procs {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd"))
		if err != nil || !strings.HasPrefix(cwd, root+string(filepath.Separator)) {
			continue
		}
		cmdline, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		// A Process leads its own process group; kill all of it. Anything
		// in the test binary's group is killed alone.
		target := pid
		if pgid, err := syscall.Getpgid(pid); err == nil && pgid != syscall.Getpgrp() {
			target = -pgid
		}
		syscall.Kill(target, syscall.SIGKILL)
		t.Errorf("testkit: process %d (%s) was still running in %s after the daemon stopped; killed it",
			pid, strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " ")), cwd)
	}
}
