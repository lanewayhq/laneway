package supervisor

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// listening reports whether a process in process group pgid (the run's
// command and whatever it started) has a TCP socket listening on port that
// connections to 127.0.0.1 reach.
//
// WaitUp asks this as well as connecting, because a connection succeeding
// doesn't prove the Process is up: another program (such as another
// Portby's Process) may hold the port, and on WSL2 in mirrored networking
// mode a connection to a loopback port nothing in Linux listens on is
// sometimes accepted from outside Linux and then reset.
func listening(port, pgid int) (bool, error) {
	inodes := map[string]bool{}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(table)
		if errors.Is(err, fs.ErrNotExist) { // no IPv6
			continue
		}
		if err != nil {
			return false, err
		}
		listenersIn(string(data), port, inodes)
	}
	if len(inodes) == 0 {
		return false, nil
	}
	// The run's command is the group leader, and usually the listener.
	if holdsSocket(pgid, inodes) {
		return true, nil
	}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, e := range procs {
		pid, err := strconv.Atoi(e.Name())
		if err == nil && pid != pgid && processGroup(pid) == pgid && holdsSocket(pid, inodes) {
			return true, nil
		}
	}
	return false, nil
}

// loopbackReachable are the local addresses, as /proc/net/tcp{,6} print them
// (each 32-bit word in host byte order, little-endian here), that a
// listener reached through 127.0.0.1 is bound to.
var loopbackReachable = map[string]bool{
	"0100007F":                         true, // 127.0.0.1
	"00000000":                         true, // 0.0.0.0
	"00000000000000000000000000000000": true, // ::
	"0000000000000000FFFF00000100007F": true, // ::ffff:127.0.0.1
}

// tcpListen is the LISTEN state in /proc/net/tcp{,6}.
const tcpListen = "0A"

// listenersIn adds to inodes the socket inode of every listener in a
// /proc/net/tcp{,6} table on port at an address loopbackReachable.
func listenersIn(table string, port int, inodes map[string]bool) {
	wantPort := fmt.Sprintf("%04X", port)
	for line := range strings.Lines(table) {
		// sl local_address rem_address st tx_queue:rx_queue tr:tm->when
		// retrnsmt uid timeout inode ...
		f := strings.Fields(line)
		if len(f) < 10 || f[3] != tcpListen {
			continue
		}
		addr, p, ok := strings.Cut(f[1], ":")
		if ok && p == wantPort && loopbackReachable[addr] {
			inodes[f[9]] = true
		}
	}
}

// processGroup is pid's process group, or 0 if it can't be read (it may
// have exited).
func processGroup(pid int) int {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	// pid (comm) state ppid pgrp ...; comm may contain spaces and parens.
	f := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
	if len(f) < 3 {
		return 0
	}
	pgrp, _ := strconv.Atoi(f[2])
	return pgrp
}

// holdsSocket reports whether pid has one of the socket inodes open.
func holdsSocket(pid int, inodes map[string]bool) bool {
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	fds, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		link, err := os.Readlink(dir + "/" + fd.Name())
		if err != nil {
			continue
		}
		if inode, ok := strings.CutPrefix(link, "socket:["); ok && inodes[strings.TrimSuffix(inode, "]")] {
			return true
		}
	}
	return false
}
