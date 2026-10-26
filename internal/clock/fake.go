package clock

import (
	"context"
	"math"
	"slices"
	"sync"
	"time"
)

// Fake is a Clock for tests. Time stands still until Advance moves it, and
// AfterFunc calls run (each in its own goroutine, like the real clock's) once
// Advance passes their deadline.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeTimer
	changed chan struct{} // closed and replaced whenever timers changes
}

// NewFake returns a Fake whose time starts at now.
func NewFake(now time.Time) *Fake {
	return &Fake{now: now, changed: make(chan struct{})}
}

// Now returns the Fake's current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// AfterFunc schedules fn to run once time has been advanced by at least d.
func (f *Fake) AfterFunc(d time.Duration, fn func()) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{clock: f, at: f.now.Add(d), fn: fn}
	f.timers = append(f.timers, t)
	f.notify()
	return t
}

// Advance moves time forward by d and starts every call that has become due,
// earliest first.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	var due []*fakeTimer
	f.timers = slices.DeleteFunc(f.timers, func(t *fakeTimer) bool {
		if t.at.After(f.now) {
			return false
		}
		due = append(due, t)
		return true
	})
	if len(due) > 0 {
		f.notify()
	}
	f.mu.Unlock()
	slices.SortStableFunc(due, func(a, b *fakeTimer) int { return a.at.Compare(b.at) })
	for _, t := range due {
		go t.fn()
	}
}

// WaitForTimer blocks until at least one call is pending and returns how far
// time must be advanced for the earliest one to run. Tests use it to learn
// that the daemon has scheduled something, and when for.
func (f *Fake) WaitForTimer(ctx context.Context) (time.Duration, error) {
	return f.WaitForTimerWithin(ctx, math.MaxInt64)
}

// WaitForTimerWithin is WaitForTimer for calls due within limit of now:
// calls due later are ignored, so a test can wait for a crash restart while
// an idle timeout minutes away is pending too.
func (f *Fake) WaitForTimerWithin(ctx context.Context, limit time.Duration) (time.Duration, error) {
	for {
		f.mu.Lock()
		changed := f.changed
		if len(f.timers) > 0 {
			next := slices.MinFunc(f.timers, func(a, b *fakeTimer) int { return a.at.Compare(b.at) })
			if d := next.at.Sub(f.now); d <= limit {
				f.mu.Unlock()
				return d, nil
			}
		}
		f.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return 0, context.Cause(ctx)
		}
	}
}

// Pending returns how many calls are waiting to run.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

// notify wakes WaitForTimer. The caller holds mu.
func (f *Fake) notify() {
	close(f.changed)
	f.changed = make(chan struct{})
}

type fakeTimer struct {
	clock *Fake
	at    time.Time
	fn    func()
}

func (t *fakeTimer) Stop() bool {
	f := t.clock
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.Index(f.timers, t)
	if i < 0 {
		return false
	}
	f.timers = slices.Delete(f.timers, i, i+1)
	f.notify()
	return true
}
