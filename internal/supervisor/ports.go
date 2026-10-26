package supervisor

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// Process ports come from this range rather than from the kernel (port 0):
// the kernel soon hands a freed ephemeral port to the next program asking
// for one, often before a slow-booting Process has bound it.
const minPort, maxPort = 20000, 29999

// ports are the ports given to runs that haven't exited yet, each with the
// lock file that reserves it.
var ports = struct {
	sync.Mutex
	held map[int]*os.File
}{held: map[int]*os.File{}}

// portLockDir is where every Portby on the machine (the daemon, test suites
// running alongside it) reserves the ports it gives to runs: it holds an
// flock on <portLockDir>/<port> until the run exits. Without it, two of them
// can give the same port to Processes that boot at the same time, and one
// sees the other's Process as its own. The kernel drops the lock when its
// holder dies, so a crashed daemon's ports become free again.
var portLockDir = filepath.Join(os.TempDir(), "portby-ports")

// freePort picks a random loopback port in the range that no Portby has
// reserved and that is free now, and reserves it. A program other than
// Portby could still take it before the Process binds it, but a fresh one
// is picked on every start.
//
// The check binds without SO_REUSEADDR (net.Listen sets it), as some
// programs do: a port a stopped Process leaves connections in TIME_WAIT on
// is free to net.Listen, but such a program gets "address already in use".
func freePort() (int, error) {
	ports.Lock()
	defer ports.Unlock()
	if err := os.MkdirAll(portLockDir, 0o777); err != nil {
		return 0, fmt.Errorf("reserving a port: %w", err)
	}
	// Shared with other users' daemons, like /tmp. Only its owner can chmod.
	os.Chmod(portLockDir, 0o777|os.ModeSticky)
	var lockErr error
	for range 100 {
		port := minPort + rand.IntN(maxPort-minPort+1)
		if ports.held[port] != nil {
			continue
		}
		lock, err := reservePort(port)
		if err != nil {
			lockErr = err
			continue
		}
		if lock == nil {
			continue
		}
		ln, err := strictListen.Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			lock.Close()
			continue
		}
		ln.Close()
		ports.held[port] = lock
		return port, nil
	}
	if lockErr != nil {
		return 0, fmt.Errorf("reserving a port: %w", lockErr)
	}
	return 0, fmt.Errorf("no free port between %d and %d", minPort, maxPort)
}

// reservePort takes the lock reserving port. It returns nil, nil if another
// Portby holds it.
func reservePort(port int) (*os.File, error) {
	// Read-only is enough for flock, and lets another user's daemon lock a
	// file this one created.
	f, err := os.OpenFile(filepath.Join(portLockDir, strconv.Itoa(port)), os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}

// strictListen listens without SO_REUSEADDR.
var strictListen = net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
	var err error
	if cerr := c.Control(func(fd uintptr) {
		err = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 0)
	}); cerr != nil {
		return cerr
	}
	return err
}}

// releasePort gives up the reservation of port.
func releasePort(port int) {
	ports.Lock()
	defer ports.Unlock()
	if lock := ports.held[port]; lock != nil {
		lock.Close()
		delete(ports.held, port)
	}
}
