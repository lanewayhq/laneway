package daemon

import (
	"context"
	"fmt"
	"path/filepath"
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
	err     error  // why the Processes couldn't be started, if they couldn't
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
	s := served{project: p.Name, group: dir}
	cfg, err := recipe.ResolveDir(dir)
	if err == nil {
		for _, w := range cfg.Warnings {
			d.log.Warn("config", "project", p.Name, "warning", w)
		}
		var specs []supervisor.Spec
		for _, proc := range cfg.Processes {
			if proc.Primary {
				s.primary = proc.Name
			}
			specs = append(specs, supervisor.Spec{
				Name:    proc.Name,
				Command: proc.Command,
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
	d.servedMu.Lock()
	s, ok := d.served[domain]
	d.servedMu.Unlock()
	switch {
	case !ok:
		return "", fmt.Errorf("%s: %w", domain, proxy.ErrUnknownDomain)
	case s.err != nil:
		return "", fmt.Errorf("Project %q could not be started: %w", s.project, s.err)
	case s.primary == "":
		return "", fmt.Errorf("Project %q has no Primary Process: declare one in %s, "+
			"for example [processes.web] with command = \"…\" and primary = true", s.project, config.FileName)
	}
	proc, ok := d.supervisor.Process(s.group, s.primary)
	if !ok {
		return "", fmt.Errorf("Process %q of Project %q is not running", s.primary, s.project)
	}
	ctx, cancel := context.WithTimeout(ctx, StartTimeout)
	defer cancel()
	if err := proc.WaitUp(ctx); err != nil {
		return "", err
	}
	return proc.Addr(), nil
}
