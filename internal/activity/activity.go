// Package activity is the Activity Tracker: it decides when something that
// serves traffic, a Worktree or (later) a Service, is Idle, meaning it has had
// no requests and no open connections for its idle timeout.
//
// One Tracker watches one subject. Whatever sees the traffic reports it:
// Begin when a request or connection starts and the returned end func when it
// finishes, or Touch for a moment of activity with no duration. An open
// connection (such as a WebSocket held by a live-reload client) keeps the
// subject active for as long as it is open.
//
// Time is read and scheduled through the injected clock, so tests drive idle
// timeouts with a fake clock. Only one timer is pending at a time: activity
// while it is pending just records the time, and when the timer fires it
// checks for later activity and re-arms itself for the rest of the timeout.
package activity

import (
	"sync"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/clock"
)

// Tracker decides when one subject is Idle. It is safe for concurrent use.
type Tracker struct {
	clock clock.Clock
	idle  func()

	mu      sync.Mutex
	timeout time.Duration
	open    int         // requests and connections that haven't ended
	last    time.Time   // when activity last ended
	timer   clock.Timer // the pending idle check, if any
	gen     uint64      // which timer is current; older ones do nothing
}

// New returns a Tracker that calls idle, in its own goroutine, once the
// subject has had no activity for timeout. Nothing is scheduled until the
// first activity, and after idle is called nothing is scheduled until the
// next.
func New(clk clock.Clock, timeout time.Duration, idle func()) *Tracker {
	return &Tracker{clock: clk, idle: idle, timeout: timeout}
}

// Begin records a request or connection starting. The subject can't become
// Idle until the returned func is called, when it finishes. Calling it more
// than once has no further effect.
func (t *Tracker) Begin() (end func()) {
	t.mu.Lock()
	t.open++
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.open--
			t.activeLocked()
		})
	}
}

// Touch records a moment of activity, such as the subject being started.
func (t *Tracker) Touch() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.activeLocked()
}

// SetTimeout changes the idle timeout, counted from the last activity. It
// applies to an idle check that is already pending, too.
func (t *Tracker) SetTimeout(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if d == t.timeout {
		return
	}
	t.timeout = d
	if t.timer != nil {
		t.cancelLocked()
		t.armLocked(t.last.Add(d).Sub(t.clock.Now()))
	}
}

// Reset forgets past activity, such as when the subject is stopped by hand:
// no idle call is pending until the next activity.
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancelLocked()
}

// activeLocked records activity now and makes sure an idle check is pending
// once nothing is open. The caller holds mu.
func (t *Tracker) activeLocked() {
	t.last = t.clock.Now()
	if t.open == 0 && t.timer == nil {
		t.armLocked(t.timeout)
	}
}

func (t *Tracker) armLocked(d time.Duration) {
	t.gen++
	gen := t.gen
	t.timer = t.clock.AfterFunc(max(d, 0), func() { t.check(gen) })
}

func (t *Tracker) cancelLocked() {
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	t.gen++
}

// check runs when an idle check is due.
func (t *Tracker) check(gen uint64) {
	t.mu.Lock()
	if gen != t.gen {
		t.mu.Unlock()
		return
	}
	t.timer = nil
	if t.open > 0 {
		// The last one to end schedules the next check.
		t.mu.Unlock()
		return
	}
	if rest := t.last.Add(t.timeout).Sub(t.clock.Now()); rest > 0 {
		t.armLocked(rest)
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	t.idle()
}
