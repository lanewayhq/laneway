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
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"

	"github.com/lanewayhq/laneway/internal/gitrepo"
)

// projectMount is where a module sees the Project's directory: read-only, and
// its working directory.
const projectMount = "/project"

// mainMount is where a module sees the main Worktree's directory, read-only,
// when the Project's directory is another Worktree.
const mainMount = "/main"

// maxStderr is how much of a module's stderr is kept for the logs and for
// the error of a call that failed.
const maxStderr = 8 << 10

// request is what Laneway sends a module (see sdk.Request).
type request struct {
	APIVersion int    `json:"api_version"`
	Call       string `json:"call"`
	Name       string `json:"name"`
	Dir        string `json:"dir"`
	HostDir    string `json:"host_dir,omitempty"`
	MainDir    string `json:"main_dir,omitempty"`
	// Args are a command call's arguments.
	Args []string `json:"args,omitempty"`
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
	opts  Options
	wasm  wazero.Runtime
	cache *sharedCache

	mu       sync.Mutex
	compiled map[string]wazero.CompiledModule // by module SHA-256
	results  map[string]map[string]result     // by Plugin name, then call key
}

// result is one call's outcome and the files it depended on, by absolute
// path.
type result struct {
	out   []byte
	err   error
	reads map[string]fileState
}

func newRuntime(opts Options) (*runtime, error) {
	ctx := context.Background()
	cache, err := cacheFor(opts.CacheDir)
	if err != nil {
		return nil, err
	}
	cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true).WithCompilationCache(cache.cache)
	wasm := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, wasm); err != nil {
		wasm.Close(ctx)
		return nil, err
	}
	return &runtime{opts: opts, wasm: wasm, cache: cache, compiled: map[string]wazero.CompiledModule{}, results: map[string]map[string]result{}}, nil
}

// sharedCache is a compilation cache every runtime in the process with the
// same cache directory shares, and so their compiled code: a module is
// compiled once per process, not once per Host, however many Hosts (the
// daemon's, a CLI command's, test daemons') want it at once.
type sharedCache struct {
	cache wazero.CompilationCache

	mu        sync.Mutex
	compiling map[string]*sync.Mutex // by module SHA-256
}

// caches are the process's compilation caches, by directory ("" for one
// kept in memory only).
var caches = struct {
	sync.Mutex
	byDir map[string]*sharedCache
}{byDir: map[string]*sharedCache{}}

func cacheFor(dir string) (*sharedCache, error) {
	caches.Lock()
	defer caches.Unlock()
	if c, ok := caches.byDir[dir]; ok {
		return c, nil
	}
	cache := wazero.NewCompilationCache()
	if dir != "" {
		var err error
		if cache, err = wazero.NewCompilationCacheWithDir(dir); err != nil {
			return nil, err
		}
	}
	c := &sharedCache{cache: cache, compiling: map[string]*sync.Mutex{}}
	caches.byDir[dir] = c
	return c, nil
}

// compileOnce compiles the module whose hash is sha with compile, one
// compilation of it at a time in the process: those waiting find it in the
// shared cache.
func (c *sharedCache) compileOnce(sha string, compile func() (wazero.CompiledModule, error)) (wazero.CompiledModule, error) {
	c.mu.Lock()
	m, ok := c.compiling[sha]
	if !ok {
		m = &sync.Mutex{}
		c.compiling[sha] = m
	}
	c.mu.Unlock()
	m.Lock()
	defer m.Unlock()
	return compile()
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

// call runs p's module for req, with the Project in dir mounted (and, if dir
// is a linked Worktree, the main Worktree), and returns what it wrote to
// stdout. The result is reused until a file the module looked at changes.
func (rt *runtime) call(p Plugin, req request, dir string) ([]byte, error) {
	in, mounts, err := encodeRequest(req, dir)
	if err != nil {
		return nil, err
	}
	key := p.SHA256 + "\x00" + dir + "\x00" + string(in)
	rt.mu.Lock()
	cached, ok := rt.results[p.Name][key]
	rt.mu.Unlock()
	if ok && unchanged(cached.reads) {
		return cached.out, cached.err
	}

	out, err := rt.run(p, in, mounts)
	reads := map[string]fileState{}
	for _, m := range mounts {
		maps.Copy(reads, m.recorded())
	}
	rt.mu.Lock()
	if rt.results[p.Name] == nil {
		rt.results[p.Name] = map[string]result{}
	}
	rt.results[p.Name][key] = result{out: out, err: err, reads: reads}
	rt.mu.Unlock()
	return out, err
}

// callFresh is call without reusing a result, for calls that are about
// something other than the Project's files, such as an event.
func (rt *runtime) callFresh(p Plugin, req request, dir string) ([]byte, error) {
	in, mounts, err := encodeRequest(req, dir)
	if err != nil {
		return nil, err
	}
	return rt.run(p, in, mounts)
}

// encodeRequest is req as the module reads it, about the Project in dir, and
// the directories the module sees, by where it sees them: the Project and,
// if dir is a linked Worktree, the main Worktree.
func encodeRequest(req request, dir string) ([]byte, map[string]*recordingFS, error) {
	req.APIVersion, req.Dir, req.HostDir = APIVersion, projectMount, dir
	if abs, err := filepath.Abs(dir); err == nil {
		req.HostDir = abs
	}
	mounts := map[string]*recordingFS{projectMount: newRecordingFS(dir)}
	if main, ok := gitrepo.MainDir(dir); ok {
		req.MainDir = mainMount
		mounts[mainMount] = newRecordingFS(main)
	}
	in, err := json.Marshal(req)
	return in, mounts, err
}

func (rt *runtime) run(p Plugin, in []byte, mounts map[string]*recordingFS) ([]byte, error) {
	mod, err := rt.compile(p)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), rt.opts.Timeout)
	defer cancel()
	var stdout bytes.Buffer
	stderr := &limitedBuffer{max: maxStderr}
	fsConfig := wazero.NewFSConfig()
	for at, fsys := range mounts {
		fsConfig = fsConfig.WithFSMount(fsys, at)
	}
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithArgs(p.Name).
		WithEnv("PWD", projectMount).
		WithStdin(bytes.NewReader(in)).
		WithStdout(&stdout).
		WithStderr(stderr).
		WithFSConfig(fsConfig).
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
	return rt.cache.compileOnce(p.SHA256, func() (wazero.CompiledModule, error) { return rt.compileLocked(p) })
}

func (rt *runtime) compileLocked(p Plugin) (wazero.CompiledModule, error) {
	rt.mu.Lock()
	mod, ok := rt.compiled[p.SHA256]
	rt.mu.Unlock()
	if ok {
		return mod, nil
	}
	var data []byte
	var err error
	if p.readModule != nil {
		// Built into Laneway: nothing to check it against.
		data, err = p.readModule()
	} else {
		data, err = os.ReadFile(filepath.Join(p.Dir, filepath.FromSlash(p.module())))
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if p.readModule == nil && hex.EncodeToString(sum[:]) != p.SHA256 {
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

func stat(file string) fileState {
	fi, err := os.Stat(file)
	if err != nil {
		return fileState{}
	}
	return fileState{exists: true, dir: fi.IsDir(), size: fi.Size(), mod: fi.ModTime()}
}

// unchanged reports whether every file in reads is as it was.
func unchanged(reads map[string]fileState) bool {
	for file, was := range reads {
		if stat(file) != was {
			return false
		}
	}
	return true
}

// recordingFS is a directory as a module sees it: read-only, recording every
// path the module opens or stats (a directory's state changes when its
// entries do), so the call's result can be reused until one changes.
type recordingFS struct {
	root string
	fsys fs.FS

	mu    sync.Mutex
	reads map[string]fileState // by absolute path
}

func newRecordingFS(root string) *recordingFS {
	return &recordingFS{root: root, fsys: os.DirFS(root), reads: map[string]fileState{}}
}

func (r *recordingFS) Open(name string) (fs.File, error) {
	file := filepath.Join(r.root, filepath.FromSlash(name))
	r.mu.Lock()
	if _, ok := r.reads[file]; !ok {
		r.reads[file] = stat(file)
	}
	r.mu.Unlock()
	return r.fsys.Open(name)
}

func (r *recordingFS) recorded() map[string]fileState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.reads)
}
