package testkit

import (
	"testing"
	"time"
)

// pollInterval is how often WaitUntil checks.
const pollInterval = 20 * time.Millisecond

// WaitUntil checks cond until it holds, failing the test after timeout with
// "timed out after <timeout> waiting for <what>". It is for state Portby
// reaches asynchronously (output read, a Worktree discovered, a Process up);
// never use it, or a sleep, to show that something did NOT happen: assert
// observable state instead ("Writing reliable e2e tests" in the notes).
func WaitUntil(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(pollInterval)
	}
}
