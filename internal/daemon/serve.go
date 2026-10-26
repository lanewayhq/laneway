package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/config"
	"github.com/mrsafalpiya/portby.dev/internal/naming"
	"github.com/mrsafalpiya/portby.dev/internal/proxy"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
	"github.com/mrsafalpiya/portby.dev/internal/supervisor"
)

// StartTimeout is how long a request waits for the Primary Process to accept
// connections.
const StartTimeout = 60 * time.Second

// served is what the proxy needs to know about one Domain.
type served struct {
	project string
	group   string // the Supervisor group running the Worktree's Processes
	primary string // the Primary Process's name; "" if there is none
	// routed are the other HTTP Processes, served at <process>.<domain>.
	routed map[string]bool
	err    error // why the Processes couldn't be started, if they couldn't
}

// domainOf is the main Worktree's Domain.
func domainOf(p registry.Project) string {
	return naming.DomainLabel(p.Name) + ".localhost"
}

// startProject starts the main Worktree's Processes.
func (d *Daemon) startProject(ctx context.Context, p registry.Project) {
	d.startWorktree(ctx, p, domainOf(p), p.Path)
}

// startWorktree resolves the config in the Worktree at dir and starts its
// Processes, serving them at domain. Config is resolved on every start, so
// changes take effect the next time the Processes start. Failures are kept so
// requests to the Domain can report them, rather than failing the caller.
func (d *Daemon) startWorktree(ctx context.Context, p registry.Project, domain, dir string) {
	s := served{project: p.Name, group: dir, routed: map[string]bool{}}
	cfg, err := recipe.ResolveDir(dir)
	if err == nil {
		for _, w := range cfg.Warnings {
			d.log.Warn("config", "project", p.Name, "warning", w)
		}
		var specs []supervisor.Spec
		for _, proc := range cfg.Processes {
			if proc.Primary {
				s.primary = proc.Name
			} else if proc.HTTP {
				s.routed[proc.Name] = true
			}
			specs = append(specs, supervisor.Spec{
				Name:    proc.Name,
				Command: proc.Command,
				HTTP:    proc.HTTP,
				Dir:     dir,
				LogFile: filepath.Join(d.cfg.Paths.Logs(), domain, proc.Name+".log"),
			})
		}
		err = d.supervisor.StartGroup(ctx, s.group, specs)
	}
	if err != nil {
		d.log.Error("starting Worktree", "project", p.Name, "worktree", dir, "err", err)
		s.err = err
	}
	d.servedMu.Lock()
	d.served[domain] = s
	d.servedMu.Unlock()
}

// stopWorktree stops serving domain and stops the Processes of the Worktree
// at dir.
func (d *Daemon) stopWorktree(ctx context.Context, domain, dir string) {
	d.servedMu.Lock()
	delete(d.served, domain)
	d.servedMu.Unlock()
	d.supervisor.StopGroup(ctx, dir)
}

// router is the daemon's proxy.Router.
type router struct{ d *Daemon }

func (r router) Upstream(ctx context.Context, domain string) (string, error) {
	d := r.d
	s, process, ok := d.route(domain)
	switch {
	case !ok:
		return "", fmt.Errorf("%s: %w", domain, proxy.ErrUnknownDomain)
	case process != "":
		return d.upstream(ctx, s, process)
	case s.err != nil:
		return "", fmt.Errorf("Project %q could not be started: %w", s.project, s.err)
	case s.primary == "":
		return "", fmt.Errorf("Project %q has no Primary Process: declare one in %s, "+
			"for example [processes.web] with command = \"…\" and primary = true", s.project, config.FileName)
	}
	return d.upstream(ctx, s, s.primary)
}

// route finds what serves host: a Worktree's Domain is its Primary Process
// (process is ""), and <process>.<domain> is one of the Worktree's other
// HTTP Processes.
//
// A Domain or Alias wins over <process>.<domain>: Worktree labels and
// Aliases are kept clear of the main Worktree's HTTP Process names when they
// are given out, but config can add a Process later.
func (d *Daemon) route(host string) (s served, process string, ok bool) {
	if s, ok := d.servedAt(host); ok {
		return s, "", true
	}
	process, domain, found := strings.Cut(host, ".")
	if s, ok := d.servedAt(domain); found && ok && s.routed[process] {
		return s, process, true
	}
	return served{}, "", false
}

// servedAt finds the Worktree whose Domain, or one of whose Aliases, is
// domain.
func (d *Daemon) servedAt(domain string) (served, bool) {
	if target, ok := d.aliasTarget(domain); ok {
		domain = target
	}
	d.servedMu.Lock()
	defer d.servedMu.Unlock()
	s, ok := d.served[domain]
	return s, ok
}

// upstream waits for the Worktree's Process to accept connections and
// returns its address.
func (d *Daemon) upstream(ctx context.Context, s served, process string) (string, error) {
	proc, ok := d.supervisor.Process(s.group, process)
	if !ok {
		return "", fmt.Errorf("Process %q of Project %q is not running", process, s.project)
	}
	ctx, cancel := context.WithTimeout(ctx, StartTimeout)
	defer cancel()
	if err := proc.WaitUp(ctx); err != nil {
		return "", err
	}
	return proc.Addr(), nil
}
