package daemon

import (
	"context"

	"github.com/mrsafalpiya/portby.dev/internal/gitrepo"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
)

// worktreeDomain is a linked Worktree's Domain: <worktree>.<project>.localhost.
func worktreeDomain(p registry.Project, w registry.Worktree) string {
	return w.Label + "." + domainOf(p)
}

// worktreesChanged is the Worktree Watcher's callback.
func (d *Daemon) worktreesChanged(main string) {
	if p, ok := d.registry.ProjectAt(main); ok {
		d.syncWorktrees(context.Background(), p)
	}
}

// syncWorktrees reconciles the Project's Worktrees with git's worktree
// registry: Worktrees that appeared are recorded and their Processes started,
// and Worktrees that are gone are forgotten and their Processes stopped. It
// runs at daemon start, on Link and whenever the Worktree Watcher reports a
// possible change.
func (d *Daemon) syncWorktrees(ctx context.Context, p registry.Project) {
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	select {
	case <-d.closing:
		return
	default:
	}
	list, err := gitrepo.Worktrees(ctx, p.Path)
	if err != nil {
		d.log.Error("listing Worktrees", "project", p.Name, "err", err)
		return
	}
	knownAt := map[string]registry.Worktree{}
	for _, w := range d.registry.Worktrees(p.Path) {
		knownAt[w.Path] = w
	}
	var found []registry.Found
	pending := false
	for _, w := range list {
		switch {
		case w.Main || w.Gone:
		case w.Initializing:
			// Not ready to be read yet: keep what we know and look again.
			pending = true
			if k, ok := knownAt[w.Dir]; ok {
				found = append(found, registry.Found{Name: k.Name, Path: k.Path})
			}
		default:
			found = append(found, registry.Found{Name: w.Name, Path: w.Dir})
		}
	}
	// Removed Worktrees stop first, so a moved one can start again at the
	// same Domain. Added ones are served before the Registry (and so the
	// API) reports them.
	changes := d.registry.PlanWorktrees(p.Path, found, reservedLabels(p))
	for _, w := range changes.Removed {
		d.log.Info("Worktree removed", "project", p.Name, "worktree", w.Name, "path", w.Path)
		d.stopWorktree(ctx, worktreeDomain(p, w), w.Path)
	}
	for _, w := range changes.Added {
		d.log.Info("Worktree discovered", "project", p.Name, "worktree", w.Name, "path", w.Path)
		d.startWorktree(ctx, p, worktreeDomain(p, w), w.Path)
	}
	if err := d.registry.ApplyWorktrees(changes); err != nil {
		d.log.Error("recording Worktrees", "project", p.Name, "err", err)
		for _, w := range changes.Added {
			d.stopWorktree(ctx, worktreeDomain(p, w), w.Path)
		}
		return
	}
	var dirs []string
	for _, w := range d.registry.Worktrees(p.Path) {
		dirs = append(dirs, w.Path)
	}
	d.watcher.Watch(p.Path, dirs)
	if pending {
		d.watcher.Trigger(p.Path)
	}
}

// startKnownWorktrees starts the Processes of the Project's linked Worktrees
// that the Registry already knew about, at daemon start. (syncWorktrees only
// starts the ones it discovers.)
func (d *Daemon) startKnownWorktrees(ctx context.Context, p registry.Project) {
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	for _, w := range d.registry.Worktrees(p.Path) {
		domain := worktreeDomain(p, w)
		d.servedMu.Lock()
		_, ok := d.served[domain]
		d.servedMu.Unlock()
		if !ok {
			d.startWorktree(ctx, p, domain, w.Path)
		}
	}
}
