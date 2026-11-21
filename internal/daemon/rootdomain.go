package daemon

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/prepare"
	"github.com/lanewayhq/laneway/internal/rootdomain"
)

// The Root Domain (ADR-0009) is the domain every Laneway name is under. It
// comes from the personal config's root_domain, read when the daemon
// starts, and SetRootDomain changes it while the daemon runs. Every name is
// derived from it when it's needed, never stored, so swapping it moves
// every Domain, Alias, Process host, the Dashboard and the Service Domains
// at once. What Laneway gave out under the old one (Env Patches, Bucket
// origins) is brought up to date by applyRootDomain.

// rootDomain is the current Root Domain.
func (d *Daemon) rootDomain() string {
	return *d.root.Load()
}

// dashboardDomain is where the Dashboard is served: laneway.<Root Domain>,
// unless Config.DashboardDomain says otherwise.
func (d *Daemon) dashboardDomain() string {
	if d.cfg.DashboardDomain != "" {
		return d.cfg.DashboardDomain
	}
	return rootdomain.Dashboard(d.rootDomain())
}

// serves reports whether Laneway serves anything at name: a Worktree's
// Domain or Alias, one of its HTTP Processes' hosts, the Dashboard, or a
// Service Domain an installed Engine declares.
func (d *Daemon) serves(name string) bool {
	if name == d.dashboardDomain() {
		return true
	}
	if label, ok := d.serviceDomainLabel(name); ok {
		_, _, declared := d.plugins.ServiceDomain(label)
		return declared
	}
	_, _, ok := d.route(name)
	return ok
}

// startRootDomain is the Root Domain the daemon starts with:
// Config.RootDomain, else the personal config's root_domain, else
// rootdomain.Default. If the personal config can't be read, it is the one
// Env Patches were last applied with, so a typo doesn't move every
// Worktree; the error is logged, and starting a Worktree reports it.
func (d *Daemon) startRootDomain() string {
	if d.cfg.RootDomain != "" {
		return d.cfg.RootDomain
	}
	g, err := config.LoadGlobal(d.cfg.Paths.GlobalConfig())
	if err != nil {
		applied, _ := d.registry.RootDomain()
		d.log.Error("reading the Root Domain from the global config; keeping the last one", "root_domain", applied, "err", err)
		return applied
	}
	return cmp.Or(ptrValue(g.RootDomain), rootdomain.Default)
}

// RootDomainChange is what changing the Root Domain did.
type RootDomainChange struct {
	// Previous and Current are the Root Domains before and after.
	Previous, Current string
	// Patched are the directories of the Ready Worktrees whose Env Patch
	// was applied again. Worktrees that aren't Ready get it when they are.
	Patched []string
	// Restarted are the Processes restarted, because they were running,
	// by Worktree directory.
	Restarted map[string][]string
}

// SetRootDomain makes root the Root Domain while the daemon runs, in the
// order the spec gives: the names served change at once, then every Ready
// Worktree's Env Patch is applied again and its running Processes restart
// (which rewrites the per-Process files that embed a host, such as the Vite
// config), and its Buckets' allowed origins follow (in the background). The
// Root Domain is recorded as the one Env Patches were applied with, and the
// one before it as the previous one. It doesn't write the personal config:
// the caller does, so the next start keeps root.
func (d *Daemon) SetRootDomain(ctx context.Context, root string) (RootDomainChange, error) {
	if err := rootdomain.Check(root); err != nil {
		return RootDomainChange{}, fmt.Errorf("root_domain %w", err)
	}
	d.rootChange.Lock()
	defer d.rootChange.Unlock()
	previous := d.rootDomain()
	d.root.Store(&root)
	d.log.Info("Root Domain changed", "from", previous, "to", root)
	return d.applyRootDomain(ctx, previous), nil
}

// applyRootDomain brings what Laneway gave out under the Root Domain from
// (Env Patches, running Processes, Bucket origins, the Primary Domains
// Plugins were told about) up to date with the current one, and records it,
// unless the Env Patches were already applied with it.
func (d *Daemon) applyRootDomain(ctx context.Context, from string) RootDomainChange {
	root := d.rootDomain()
	change := RootDomainChange{Previous: from, Current: root, Patched: []string{}, Restarted: map[string][]string{}}
	if applied, _ := d.registry.RootDomain(); applied == root {
		return change
	}
	d.servedMu.Lock()
	worktrees := make([]*served, 0, len(d.served))
	for _, s := range d.served {
		worktrees = append(worktrees, s)
	}
	d.servedMu.Unlock()
	slices.SortFunc(worktrees, func(a, b *served) int { return strings.Compare(a.dir, b.dir) })
	for _, s := range worktrees {
		p, ok := d.servedProject(s)
		if !ok || ctx.Err() != nil {
			continue
		}
		if d.preparer.Status(s.dir).State == prepare.Ready {
			restarted := d.reapplyEnvPatch(s)
			change.Patched = append(change.Patched, s.dir)
			if len(restarted) > 0 {
				change.Restarted[s.dir] = restarted
			}
		}
		d.worktreeOriginsChanged(p, s.worktreeRef)
		if primary := d.primaryDomain(p, s.worktreeRef); primary != "" {
			d.primaryDomainChanged(p, s.worktreeRef, strings.TrimSuffix(primary, root)+from)
		}
	}
	if err := d.registry.RecordRootDomain(root); err != nil {
		d.log.Error("recording the Root Domain", "root_domain", root, "err", err)
	}
	return change
}

// reapplyEnvPatch applies the served Worktree s's Env Patch again and, if
// it changed .env, restarts its running Processes, which it returns.
func (d *Daemon) reapplyEnvPatch(s *served) []string {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.ctx.Err() != nil || !d.applyServedEnvPatch(s) || len(d.supervisor.Processes(s.group)) == 0 {
		return nil
	}
	cfg, err := d.resolveWorktree(s.ctx, s.dir)
	if err != nil {
		d.log.Error("restarting Processes for the new Root Domain", "project", s.project, "dir", s.dir, "err", err)
		return nil
	}
	return d.restartForEnv(s, cfg)
}
