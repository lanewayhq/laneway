// Package watcher is the Worktree Watcher (ADR-0004). It tells the daemon when
// a Project's set of Worktrees may have changed, so the daemon can rescan
// git's worktree registry and reconcile.
//
// File system events are only a trigger, never the source of truth: git's
// registry is read again after every one. For each Project it watches
//
//   - <main>/.git, to see .git/worktrees appear (it doesn't exist until the
//     first `git worktree add`, and disappears after the last is removed);
//   - <main>/.git/worktrees, to see `git worktree add`, `remove` and `prune`;
//   - each linked Worktree's directory, to see it deleted or moved by hand,
//     which git's registry doesn't record.
//
// Events are debounced per Project. Every Project is also rescanned on a
// timer, which covers missed events and `git worktree move`. Projects on
// file systems where inotify doesn't work (WSL's /mnt/c, NFS, SMB, FUSE) get
// no watches and are polled more often instead.
package watcher

import (
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Debounce is how long the Watcher waits after the last event for a Project
// before reporting it, so one git command gives one rescan.
const Debounce = 200 * time.Millisecond

// Defaults for Config.
const (
	DefaultRescanInterval = 30 * time.Second
	DefaultPollInterval   = 3 * time.Second
)

// Config configures a Watcher.
type Config struct {
	Log *slog.Logger
	// Changed is called, never concurrently for the same Project, when the
	// Worktrees of the Project whose main Worktree is at project may have
	// changed.
	Changed func(project string)
	// RescanInterval is how often every Project is rescanned anyway.
	RescanInterval time.Duration
	// PollInterval is how often Projects that can't be watched are rescanned.
	PollInterval time.Duration
}

// Watcher is safe for concurrent use.
type Watcher struct {
	cfg     Config
	fs      *fsnotify.Watcher
	stop    chan struct{}
	wg      sync.WaitGroup // the event and timer goroutines
	reports sync.WaitGroup // calls to Changed in progress

	mu       sync.Mutex
	closed   bool
	projects map[string]*project // main Worktree dir → state
	paths    map[string]string   // watched path → main Worktree dir
}

type project struct {
	poll      bool        // inotify can't be trusted here
	worktrees []string    // linked Worktree dirs being watched
	timer     *time.Timer // pending debounced report
	reporting sync.Mutex  // held while Changed runs, so reports never overlap
}

// New starts a Watcher with no Projects.
func New(cfg Config) (*Watcher, error) {
	if cfg.RescanInterval == 0 {
		cfg.RescanInterval = DefaultRescanInterval
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	fs, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		cfg:      cfg,
		fs:       fs,
		stop:     make(chan struct{}),
		projects: map[string]*project{},
		paths:    map[string]string{},
	}
	w.wg.Add(2)
	go w.events()
	go w.timers()
	return w, nil
}

// Watch starts watching the Project whose main Worktree is at main, or
// updates the set of linked Worktree dirs watched for it.
func (w *Watcher) Watch(main string, worktrees []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	p, ok := w.projects[main]
	if !ok {
		p = &project{poll: !inotifyWorks(main)}
		w.projects[main] = p
		if p.poll {
			w.cfg.Log.Info("polling Worktrees: file system events don't work here", "project", main)
		}
	}
	if p.poll {
		return
	}
	common := filepath.Join(main, ".git")
	w.add(main, common)
	w.add(main, filepath.Join(common, "worktrees"))
	for _, dir := range p.worktrees {
		if !slices.Contains(worktrees, dir) {
			w.remove(dir)
		}
	}
	for _, dir := range worktrees {
		if _, ok := w.paths[dir]; !ok {
			w.add(main, dir)
		}
	}
	p.worktrees = slices.Clone(worktrees)
}

// add watches path for the Project at main. Missing paths are fine: the
// event that creates them, or the next rescan, adds them later.
func (w *Watcher) add(main, path string) {
	if err := w.fs.Add(path); err != nil {
		return
	}
	w.paths[path] = main
}

func (w *Watcher) remove(path string) {
	w.fs.Remove(path)
	delete(w.paths, path)
}

// Close stops the Watcher and waits for any report in progress to finish.
// Changed isn't called after Close returns.
func (w *Watcher) Close() error {
	w.mu.Lock()
	w.closed = true
	for _, p := range w.projects {
		if p.timer != nil {
			p.timer.Stop()
		}
	}
	w.mu.Unlock()
	close(w.stop)
	err := w.fs.Close()
	w.wg.Wait()
	w.reports.Wait()
	return err
}

// Trigger reports the Project at main after the debounce delay, restarting
// the delay if a report is already pending.
func (w *Watcher) Trigger(main string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.projects[main]
	if !ok || w.closed {
		return
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(Debounce, func() { w.report(main, p) })
}

func (w *Watcher) report(main string, p *project) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.reports.Add(1)
	w.mu.Unlock()
	defer w.reports.Done()
	p.reporting.Lock()
	defer p.reporting.Unlock()
	w.cfg.Changed(main)
}

func (w *Watcher) events() {
	defer w.wg.Done()
	for {
		select {
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			// Usually an overflowed event queue: rescan everything.
			w.cfg.Log.Warn("watching Worktrees", "err", err)
			w.triggerAll(func(*project) bool { return true })
		}
	}
}

// handle triggers a rescan for events that can mean a Worktree came or went,
// and ignores the rest (such as files changing at the top of a Worktree).
func (w *Watcher) handle(ev fsnotify.Event) {
	w.mu.Lock()
	var main string
	if m, ok := w.paths[ev.Name]; ok {
		// Something happened to a watched path itself: a Worktree's
		// directory, or .git/worktrees, was deleted or moved.
		main = m
		if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
			delete(w.paths, ev.Name)
		}
	} else if m, ok := w.paths[filepath.Dir(ev.Name)]; ok {
		dir := filepath.Dir(ev.Name)
		switch {
		case filepath.Base(dir) == "worktrees" && dir == filepath.Join(m, ".git", "worktrees"):
			main = m // an entry in git's registry came or went
		case ev.Name == filepath.Join(m, ".git", "worktrees"):
			main = m
			if ev.Has(fsnotify.Create) {
				// Its first entry may have been created before this watch
				// existed; the rescan this triggers finds it.
				w.add(m, ev.Name)
			}
		case filepath.Base(ev.Name) == ".git" && dir != m:
			main = m // a linked Worktree's .git file went away
		}
	}
	w.mu.Unlock()
	if main != "" {
		w.Trigger(main)
	}
}

func (w *Watcher) timers() {
	defer w.wg.Done()
	rescan := time.NewTicker(w.cfg.RescanInterval)
	defer rescan.Stop()
	poll := time.NewTicker(w.cfg.PollInterval)
	defer poll.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-rescan.C:
			w.triggerAll(func(*project) bool { return true })
		case <-poll.C:
			w.triggerAll(func(p *project) bool { return p.poll })
		}
	}
}

func (w *Watcher) triggerAll(match func(*project) bool) {
	w.mu.Lock()
	var mains []string
	for main, p := range w.projects {
		if match(p) {
			mains = append(mains, main)
		}
	}
	w.mu.Unlock()
	for _, main := range mains {
		w.Trigger(main)
	}
}

// File system magic numbers (statfs f_type) where inotify misses changes.
const (
	v9fsMagic = 0x01021997 // WSL2 drvfs (/mnt/c)
	nfsMagic  = 0x6969
	fuseMagic = 0x65735546
	smb2Magic = 0xFE534D42
	cifsMagic = 0xFF534D42
)

// inotifyWorks reports whether file system events can be trusted for dir.
func inotifyWorks(dir string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return true
	}
	switch uint32(st.Type) {
	case v9fsMagic, nfsMagic, fuseMagic, smb2Magic, cifsMagic:
		return false
	}
	return true
}
