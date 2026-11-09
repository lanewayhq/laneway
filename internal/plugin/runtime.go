package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// projectMount is where a module sees the Project's directory: read-only, and
// its working directory.
const projectMount = "/project"

// maxStderr is how much of a module's stderr is kept for the logs and for
// the error of a call that failed.
const maxStderr = 8 << 10

// request is what Laneway sends a module (see sdk.Request).
type request struct {
	APIVersion int    `json:"api_version"`
	Call       string `json:"call"`
	Name       string `json:"name"`
	Dir        string `json:"dir"`
	// Event is what a hook call is about.
	Event *Event `json:"event,omitempty"`
}

// Calls a module answers.
const (
	callRecipe      = "recipe"
	callIntegration = "integration"
	callHook        = "hook"
)

// runtime runs Plugins' modules. Compiling a module takes about a second, and
// the Recipe Resolver asks Recipes for their layer every time the daemon
// reads a Project's config (several times a second while the Dashboard is
// open), so it keeps every module compiled, and reuses a call's result until
// a file the module looked at changes: a call is a function of the
// Project's files.
type runtime struct {
	opts Options
	wasm wazero.Runtime

	mu       sync.Mutex
	compiled map[string]wazero.CompiledModule // by module SHA-256
	results  map[string]map[string]result     // by Plugin name, then call key
}

// result is one call's outcome and the Project files it depended on.
type result struct {
	out   []byte
	err   error
	reads map[string]fileState
}

func newRuntime(opts Options) (*runtime, error) {
	ctx := context.Background()
	cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true)
	if opts.CacheDir != "" {
		cache, err := wazero.NewCompilationCacheWithDir(opts.CacheDir)
		if err != nil {
			return nil, err
		}
		cfg = cfg.WithCompilationCache(cache)
	}
	wasm := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, wasm); err != nil {
		wasm.Close(ctx)
		return nil, err
	}
	return &runtime{opts: opts, wasm: wasm, compiled: map[string]wazero.CompiledModule{}, results: map[string]map[string]result{}}, nil
}

func (rt *runtime) close() error {
	return rt.wasm.Close(context.Background())
}

// forget drops the results of the Plugin name's calls, when it is replaced.
func (rt *runtime) forget(name string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.results, name)
}

// call runs p's module for req, with the Project in dir mounted, and returns
// what it wrote to stdout. The result is reused until a file the module
// looked at changes.
func (rt *runtime) call(p Plugin, req request, dir string) ([]byte, error) {
	in, err := encodeRequest(req)
	if err != nil {
		return nil, err
	}
	key := p.SHA256 + "\x00" + dir + "\x00" + string(in)
	rt.mu.Lock()
	cached, ok := rt.results[p.Name][key]
	rt.mu.Unlock()
	if ok && unchanged(dir, cached.reads) {
		return cached.out, cached.err
	}

	project := &recordingFS{root: dir, fsys: os.DirFS(dir), reads: map[string]fileState{}}
	out, err := rt.run(p, in, project)
	rt.mu.Lock()
	if rt.results[p.Name] == nil {
		rt.results[p.Name] = map[string]result{}
	}
	rt.results[p.Name][key] = result{out: out, err: err, reads: project.recorded()}
	rt.mu.Unlock()
	return out, err
}

// callFresh is call without reusing a result, for calls that are about
// something other than the Project's files, such as an event.
func (rt *runtime) callFresh(p Plugin, req request, dir string) ([]byte, error) {
	in, err := encodeRequest(req)
	if err != nil {
		return nil, err
	}
	return rt.run(p, in, os.DirFS(dir))
}

func encodeRequest(req request) ([]byte, error) {
	req.APIVersion, req.Dir = APIVersion, projectMount
	return json.Marshal(req)
}

func (rt *runtime) run(p Plugin, in []byte, project fs.FS) ([]byte, error) {
	mod, err := rt.compile(p)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), rt.opts.Timeout)
	defer cancel()
	var stdout bytes.Buffer
	stderr := &limitedBuffer{max: maxStderr}
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithArgs(p.Name).
		WithEnv("PWD", projectMount).
		WithStdin(bytes.NewReader(in)).
		WithStdout(&stdout).
		WithStderr(stderr).
		WithFSConfig(wazero.NewFSConfig().WithFSMount(project, projectMount)).
		WithSysWalltime().
		WithSysNanotime()
	instance, err := rt.wasm.InstantiateModule(ctx, mod, cfg)
	if instance != nil {
		instance.Close(context.Background())
	}
	if s := strings.TrimSpace(stderr.String()); s != "" {
		rt.opts.Log.Info("Plugin stderr", "plugin", p.Name, "stderr", s)
	}
	var exit *sys.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("took longer than %s, so it was stopped", rt.opts.Timeout)
	case errors.As(err, &exit) && exit.ExitCode() == 0:
	case errors.As(err, &exit):
		return nil, fmt.Errorf("its module exited with status %d%s", exit.ExitCode(), stderrTail(stderr.String()))
	case err != nil:
		return nil, fmt.Errorf("its module failed: %w%s", err, stderrTail(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// compile returns p's compiled module, checking it is the module p was
// pinned to when added.
func (rt *runtime) compile(p Plugin) (wazero.CompiledModule, error) {
	rt.mu.Lock()
	mod, ok := rt.compiled[p.SHA256]
	rt.mu.Unlock()
	if ok {
		return mod, nil
	}
	data, err := os.ReadFile(filepath.Join(p.Dir, filepath.FromSlash(p.module())))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != p.SHA256 {
		return nil, fmt.Errorf("its module isn't the one it had when added (SHA-256 %s); add it again with `lane plugin add %s`", p.SHA256, p.Source)
	}
	mod, err = rt.wasm.CompileModule(context.Background(), data)
	if err != nil {
		return nil, fmt.Errorf("its module can't be compiled: %w", err)
	}
	rt.mu.Lock()
	rt.compiled[p.SHA256] = mod
	rt.mu.Unlock()
	return mod, nil
}

// stderrTail is the end of a failed module's stderr, for its error.
func stderrTail(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	lines := strings.Split(stderr, "\n")
	if len(lines) > 10 {
		lines = lines[len(lines)-10:]
	}
	return ":\n" + strings.Join(lines, "\n")
}

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// fileState is what a module saw of a path in the Project: enough to tell
// when it changes.
type fileState struct {
	exists bool
	dir    bool
	size   int64
	mod    time.Time
}

func stat(root, name string) fileState {
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		return fileState{}
	}
	return fileState{exists: true, dir: fi.IsDir(), size: fi.Size(), mod: fi.ModTime()}
}

// unchanged reports whether every path in reads is as it was.
func unchanged(root string, reads map[string]fileState) bool {
	for name, was := range reads {
		if stat(root, name) != was {
			return false
		}
	}
	return true
}

// recordingFS is the Project as a module sees it: read-only, recording every
// path the module opens or stats (a directory's state changes when its
// entries do), so the call's result can be reused until one changes.
type recordingFS struct {
	root string
	fsys fs.FS

	mu    sync.Mutex
	reads map[string]fileState
}

func (r *recordingFS) Open(name string) (fs.File, error) {
	r.mu.Lock()
	if _, ok := r.reads[name]; !ok {
		r.reads[name] = stat(r.root, name)
	}
	r.mu.Unlock()
	return r.fsys.Open(name)
}

func (r *recordingFS) recorded() map[string]fileState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}
