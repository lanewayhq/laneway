package e2e

import (
	"os"
	"sync/atomic"
	"testing"
)

// builtBinDir is the temp dir portbyBin built portby into, if a test
// needed it.
var builtBinDir atomic.Value

// TestMain removes what tests shared across the whole run: the portby
// binary the Harness tests build (portbyBin), ~70 MB in the temp dir.
func TestMain(m *testing.M) {
	code := m.Run()
	if dir, ok := builtBinDir.Load().(string); ok {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}
