//go:build ignore

// Generate builds the module of every built-in Plugin that runs as WASM (a
// directory with a main.go) from its source, for go:embed in package
// builtin: internal/builtin/<name>/plugin.wasm.gz, gzipped since a Go module
// is several megabytes, mostly runtime. The build is
// reproducible (no paths, no VCS stamp, no debug info), so a module only
// changes when its source or the Go toolchain does.
//
//	go generate ./internal/builtin   # rebuild the modules
//	go run generate.go -check        # fail if a module isn't its source's build (run in internal/builtin)
package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Module is the file each Plugin's module is written to, beside its
// plugin.json.
const Module = "plugin.wasm.gz"

func main() {
	check := flag.Bool("check", false, "compare each built module with the one in the repository instead of writing it")
	flag.Parse()
	mains, err := filepath.Glob("*/main.go")
	if err != nil {
		fail(err)
	}
	stale := false
	for _, main := range mains {
		dir := filepath.Dir(main)
		wasm, err := build(dir)
		if err != nil {
			fail(err)
		}
		gz, err := compress(wasm)
		if err != nil {
			fail(err)
		}
		file := filepath.Join(dir, Module)
		if *check {
			if old, err := os.ReadFile(file); err != nil || !bytes.Equal(old, gz) {
				fmt.Fprintf(os.Stderr, "%s isn't what %s builds to with %s; run `go generate ./internal/builtin`\n", file, dir, runtime.Version())
				stale = true
			}
			continue
		}
		if err := os.WriteFile(file, gz, 0o644); err != nil {
			fail(err)
		}
	}
	if stale {
		os.Exit(1)
	}
}

// build compiles the Plugin in dir as a WASI command.
func build(dir string) ([]byte, error) {
	tmp, err := os.MkdirTemp("", "laneway-plugin-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "plugin.wasm")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", out, "./"+dir)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("building %s: %v\n%s", dir, err, output)
	}
	return os.ReadFile(out)
}

// compress gzips data the same way every time: no name, no time.
func compress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
