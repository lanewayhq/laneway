// Package clock is the daemon's injectable clock: one of the two things the
// spec's Seam 1 lets tests swap (the other is the container engine).
//
// Everything that decides something based on how much time has passed reads
// time from a Clock: crash backoff and the crash window, and later Idle
// timeouts and the Orphan grace period. Tests pass a *Fake and move time
// forward with Advance instead of waiting.
//
// I/O deadlines and polling intervals (waiting for a port to accept
// connections, the SIGTERM → SIGKILL grace when stopping a Process) are not
// policy and use real time, so a frozen Fake can never hang them.
package clock

import "time"

// Clock tells the time and runs functions after a delay.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine once d has passed.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is a pending AfterFunc call.
type Timer interface {
	// Stop prevents the call if it hasn't happened yet, and reports whether
	// it did so.
	Stop() bool
}

// Real is the system clock.
func Real() Clock { return realClock{} }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
