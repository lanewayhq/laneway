package daemon

import (
	"context"
	"fmt"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/registry"
)

// unlink Unlinks the Project containing dir: its Worktrees' Processes stop,
// its Domains and Aliases are forgotten, and the databases of every
// Worktree become Orphans, kept for OrphanGrace so linking it again takes
// them back. Env files are left as they are, and on_remove doesn't run (no
// Worktree was removed).
func (d *Daemon) unlink(ctx context.Context, dir string) (api.UnlinkResponse, error) {
	p, _, err := d.worktreeAt(ctx, dir)
	if err != nil {
		return api.UnlinkResponse{}, err
	}
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	res := api.UnlinkResponse{Project: d.apiProject(p), Orphans: []api.Orphan{}}
	worktrees := d.worktreesOf(p)
	// The databases are worked out from each Worktree's own config, or
	// the main Worktree's if it can't be read.
	now := d.cfg.Clock.Now()
	var orphans []registry.Orphan
	for _, w := range worktrees {
		cfg, err := d.plugins.ResolveDir(w.dir)
		if err != nil {
			if cfg, err = d.plugins.ResolveDir(p.Path); err != nil {
				d.log.Error("finding the databases of an unlinked Worktree", "project", p.Name, "dir", w.dir, "err", err)
				continue
			}
		}
		orphans = append(orphans, d.worktreeOrphans(p, w, cfg, now)...)
	}
	// Forgotten first, so a Worktree Watcher report meanwhile finds no
	// Project to sync.
	if _, err := d.registry.RemoveProject(p.Path); err != nil {
		return api.UnlinkResponse{}, fmt.Errorf("unlinking %q: %w", p.Name, err)
	}
	d.watcher.Unwatch(p.Path)
	for _, w := range worktrees {
		d.stopWorktree(ctx, w.domain, w.dir)
		d.preparer.Forget(w.dir)
	}
	d.addOrphans(orphans)
	d.projectUnlinked(p)
	for _, o := range orphans {
		res.Orphans = append(res.Orphans, apiOrphan(o))
	}
	d.log.Info("Project unlinked", "project", p.Name, "path", p.Path)
	return res, nil
}
