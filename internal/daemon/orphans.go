package daemon

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/database"
	"github.com/mrsafalpiya/portby.dev/internal/naming"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
)

// OrphanGrace is how long an Orphan is kept before it is dropped, so that a
// Worktree removed (or a Project unlinked) by mistake doesn't lose its data
// at once.
const OrphanGrace = 24 * time.Hour

// OrphanRetry is how long Portby waits to try again after Orphans that are
// due could not be dropped (no container engine, say).
const OrphanRetry = time.Hour

// Orphan kinds, as the API reports them.
const (
	orphanDatabase     = "database"
	orphanTestDatabase = "test database"
	orphanParallelCopy = "parallel test copy"
)

// worktreeOrphans are the databases of the Worktree w that become Orphans
// when it goes, as Orphans since since: its own database (unless it shares
// the main Worktree's) and its Test Database. cfg is the config they come
// from. None unless the database is in a Service whose Engine can drop
// databases (database.Dropper): a SQLite file goes with its Worktree.
func worktreeOrphans(p registry.Project, w worktreeRef, cfg recipe.Resolved, since time.Time) []registry.Orphan {
	svc, ok := cfg.DatabaseService()
	if !ok {
		return nil
	}
	e, ok := databaseEngine(cfg.Database, database.Server{}).(database.Dropper)
	if !ok {
		return nil
	}
	o := registry.Orphan{Ref: svc, ProjectName: p.Name, Worktree: w.name, Since: since}
	var out []registry.Orphan
	main, own := e.Database(p.Name, "", p.Path), e.Database(p.Name, w.label, w.dir)
	if database.Mode(cfg.Database.Mode).Use(main, own) == own {
		o.Database, o.Kind = own, orphanDatabase
		out = append(out, o)
	}
	if test, ok := worktreeTestDatabase(p, w, cfg); ok {
		o.Database, o.Kind = test, orphanTestDatabase
		out = append(out, o)
	}
	return out
}

// orphanWorktree turns the databases of the Worktree w, which was removed,
// into Orphans. Its checkout is gone, so they are worked out from the main
// Worktree's config.
func (d *Daemon) orphanWorktree(p registry.Project, w worktreeRef) {
	cfg, err := recipe.ResolveDir(p.Path)
	if err != nil {
		d.log.Error("finding the databases of a removed Worktree", "project", p.Name, "worktree", w.name, "err", err)
		return
	}
	d.addOrphans(worktreeOrphans(p, w, cfg, d.cfg.Clock.Now()))
}

// reclaimOrphans takes back the databases of the Worktree w that are
// Orphans (of an earlier Link of the Project, or of a removed Worktree of
// the same name), so they aren't dropped while it uses them: its setup then
// finds them and leaves them as they are. It runs whenever a Worktree is
// served, before its setup begins.
func (d *Daemon) reclaimOrphans(p registry.Project, w worktreeRef) {
	cfg, err := recipe.ResolveDir(w.dir)
	if err != nil {
		return
	}
	mine := worktreeOrphans(p, w, cfg, time.Time{})
	if len(mine) == 0 {
		return
	}
	d.orphanMu.Lock()
	defer d.orphanMu.Unlock()
	removed, err := d.registry.RemoveOrphans(mine)
	if err != nil {
		d.log.Error("taking back Orphans", "project", p.Name, "dir", w.dir, "err", err)
	}
	for _, o := range removed {
		d.log.Info("Orphan taken back", "project", p.Name, "dir", w.dir, "database", o.Database)
	}
	d.scheduleOrphanDropLocked()
}

// addOrphans records orphans, then checks them against their Services in
// the background (checkOrphans).
func (d *Daemon) addOrphans(orphans []registry.Orphan) {
	if len(orphans) == 0 {
		return
	}
	d.orphanMu.Lock()
	err := d.registry.AddOrphans(orphans)
	d.scheduleOrphanDropLocked()
	d.orphanMu.Unlock()
	if err != nil {
		d.log.Error("recording Orphans", "err", err)
		return
	}
	d.background(func() { d.checkOrphans(d.stopping, orphans) })
}

// checkOrphans looks in the Services of orphans, just recorded, for what
// is really there: an Orphan whose database doesn't exist (its Worktree
// went before setup made it) is forgotten, and the parallel test copies of
// an Orphan Test Database become Orphans too, since the same time. It
// starts the Services to look. If it can't, the records stay as they are:
// dropping a database that doesn't exist is fine, and `portby gc` finds
// leftover copies.
func (d *Daemon) checkOrphans(ctx context.Context, orphans []registry.Orphan) {
	for _, batch := range byService(orphans) {
		e, release, err := d.dropper(ctx, batch[0].Ref)
		if err != nil {
			d.log.Error("checking Orphans", "service", batch[0].Ref.Name(), "err", err)
			continue
		}
		names, err := e.Databases(ctx)
		release()
		if err != nil {
			d.log.Error("checking Orphans", "service", batch[0].Ref.Name(), "err", err)
			continue
		}
		d.orphanMu.Lock()
		// Only Orphans still recorded: one taken back meanwhile stays
		// taken back.
		current := d.registry.Orphans()
		var gone, copies []registry.Orphan
		for _, o := range batch {
			i := slices.IndexFunc(current, o.Same)
			if i < 0 {
				continue
			}
			if !slices.Contains(names, o.Database) {
				gone = append(gone, o)
			}
			if o.Kind != orphanTestDatabase {
				continue
			}
			for _, name := range names {
				if database.ParallelCopy(o.Database, name) {
					c := current[i]
					c.Database, c.Kind = name, orphanParallelCopy
					copies = append(copies, c)
				}
			}
		}
		if _, err := d.registry.RemoveOrphans(gone); err != nil {
			d.log.Error("forgetting Orphans that don't exist", "err", err)
		}
		if err := d.registry.AddOrphans(copies); err != nil {
			d.log.Error("recording parallel test copies as Orphans", "err", err)
		}
		d.scheduleOrphanDropLocked()
		d.orphanMu.Unlock()
	}
}

// scheduleOrphanDropLocked sets the clock's timer for the next Orphan
// whose grace period ends (or, after a failed drop, OrphanRetry later). The
// caller holds orphanMu and calls it whenever the Orphans change.
func (d *Daemon) scheduleOrphanDropLocked() {
	if d.orphanTimer != nil {
		d.orphanTimer.Stop()
		d.orphanTimer = nil
	}
	orphans := d.registry.Orphans()
	if len(orphans) == 0 || d.orphansClosed {
		return
	}
	// Orphans are oldest first.
	at := orphans[0].Since.Add(OrphanGrace)
	if d.orphanRetryAt.After(at) {
		at = d.orphanRetryAt
	}
	if delay := at.Sub(d.cfg.Clock.Now()); delay > 0 {
		d.orphanTimer = d.cfg.Clock.AfterFunc(delay, func() {
			d.orphanMu.Lock()
			defer d.orphanMu.Unlock()
			d.sweepOrphansLocked()
		})
	} else {
		d.sweepOrphansLocked()
	}
}

// sweepOrphansLocked drops the Orphans that are due in the background,
// unless a sweep is already running (it schedules the next one when it
// ends). The caller holds orphanMu.
func (d *Daemon) sweepOrphansLocked() {
	if d.orphanSweeping || d.orphansClosed {
		return
	}
	d.orphanSweeping = true
	d.background(func() { d.dropDueOrphans(d.stopping) })
}

// dropDueOrphans drops the Orphans whose grace period is over.
func (d *Daemon) dropDueOrphans(ctx context.Context) {
	d.orphanMu.Lock()
	now := d.cfg.Clock.Now()
	var due []registry.Orphan
	for _, o := range d.registry.Orphans() {
		if !o.Since.Add(OrphanGrace).After(now) {
			due = append(due, o)
		}
	}
	d.orphanMu.Unlock()
	_, errs := d.dropOrphans(ctx, due)
	for _, err := range errs {
		d.log.Error("dropping Orphans", "err", err)
	}
	d.orphanMu.Lock()
	defer d.orphanMu.Unlock()
	if len(errs) > 0 && ctx.Err() == nil {
		d.orphanRetryAt = d.cfg.Clock.Now().Add(OrphanRetry)
	}
	d.orphanSweeping = false
	d.scheduleOrphanDropLocked()
}

// dropOrphans drops orphans, starting their Services to do so, and
// forgets their records. A recorded Orphan that was taken back meanwhile
// is left alone; one never recorded (a leftover parallel test copy `gc`
// found, with no Since) is dropped as it is. It returns the ones it
// dropped.
func (d *Daemon) dropOrphans(ctx context.Context, orphans []registry.Orphan) (dropped []registry.Orphan, errs []error) {
	// A sweep and `gc` never drop at the same time.
	d.orphanDropMu.Lock()
	defer d.orphanDropMu.Unlock()
	for _, batch := range byService(orphans) {
		service := batch[0].Ref.Name()
		e, release, err := d.dropper(ctx, batch[0].Ref)
		if err != nil {
			errs = append(errs, fmt.Errorf("dropping Orphans in %s: %w", service, err))
			continue
		}
		for _, o := range batch {
			if err := d.dropOrphan(ctx, e, o); err != nil {
				errs = append(errs, fmt.Errorf("dropping %s in %s: %w", o.Database, service, err))
				continue
			}
			dropped = append(dropped, o)
		}
		release()
	}
	return dropped, errs
}

func (d *Daemon) dropOrphan(ctx context.Context, e database.Dropper, o registry.Orphan) error {
	d.orphanMu.Lock()
	defer d.orphanMu.Unlock()
	if !o.Since.IsZero() && !slices.ContainsFunc(d.registry.Orphans(), o.Same) {
		return fmt.Errorf("%s is no longer an Orphan (it was taken back, or dropped already)", o.Database)
	}
	if err := e.Drop(ctx, o.Database); err != nil {
		return err
	}
	_, err := d.registry.RemoveOrphans([]registry.Orphan{o})
	d.scheduleOrphanDropLocked()
	return err
}

// serveOrphans schedules dropping the Orphans recorded before the daemon
// started (dropping those already due at once).
func (d *Daemon) serveOrphans() {
	d.orphanMu.Lock()
	defer d.orphanMu.Unlock()
	d.scheduleOrphanDropLocked()
}

// closeOrphans stops the Orphan timer for Shutdown.
func (d *Daemon) closeOrphans() {
	d.orphanMu.Lock()
	defer d.orphanMu.Unlock()
	d.orphansClosed = true
	d.scheduleOrphanDropLocked()
}

// byService groups orphans by the Service they live in.
func byService(orphans []registry.Orphan) [][]registry.Orphan {
	var groups [][]registry.Orphan
	for _, o := range orphans {
		i := slices.IndexFunc(groups, func(g []registry.Orphan) bool {
			return g[0].Engine == o.Engine && g[0].Version == o.Version
		})
		if i < 0 {
			groups = append(groups, nil)
			i = len(groups) - 1
		}
		groups[i] = append(groups[i], o)
	}
	return groups
}

// dropper starts the database Service svc and returns the Engine of its
// databases. Call release once done with it, so the Service can go Idle.
func (d *Daemon) dropper(ctx context.Context, svc recipe.Service) (e database.Dropper, release func(), err error) {
	server, release, err := d.databaseServer(ctx, svc, io.Discard)
	if err != nil {
		return nil, nil, err
	}
	e, ok := databaseEngine(recipe.Database{Engine: svc.Engine}, server).(database.Dropper)
	if !ok {
		release()
		return nil, nil, fmt.Errorf("Portby can't drop %s databases", svc.Engine)
	}
	return e, release, nil
}

// gc drops every Orphan now, along with the parallel test copies of Test
// Databases whose Worktree no longer exists (leftoverCopies); only those
// req.Databases names, if it names any. With req.Preview it only lists
// them.
func (d *Daemon) gc(ctx context.Context, req api.GCRequest) api.GCResponse {
	d.orphanMu.Lock()
	candidates := d.registry.Orphans()
	d.orphanMu.Unlock()
	leftovers, errs := d.leftoverCopies(ctx)
	candidates = append(candidates, leftovers...)
	if !req.Preview && len(req.Databases) > 0 {
		var chosen []registry.Orphan
		for _, ref := range req.Databases {
			i := slices.IndexFunc(candidates, func(o registry.Orphan) bool {
				return o.Ref.Name() == ref.Service && o.Database == ref.Database
			})
			if i < 0 {
				errs = append(errs, fmt.Errorf("%s in %s is not an Orphan (any more)", ref.Database, ref.Service))
				continue
			}
			chosen = append(chosen, candidates[i])
		}
		candidates = chosen
	}
	if !req.Preview {
		var dropErrs []error
		candidates, dropErrs = d.dropOrphans(ctx, candidates)
		errs = append(errs, dropErrs...)
	}
	res := api.GCResponse{Orphans: []api.Orphan{}}
	for _, o := range candidates {
		res.Orphans = append(res.Orphans, apiOrphan(o))
	}
	for _, err := range errs {
		res.Errors = append(res.Errors, err.Error())
	}
	return res
}

// leftoverCopies are the parallel test copies, not already Orphans, of
// Test Databases whose Worktree no longer exists, in the Services of the
// Linked Projects' databases. Only copies named after a Test Database one
// of those Projects could have (<project>_test, <project>__<worktree>_test)
// count, so other databases in the Service are never touched. It starts
// the Services to look.
func (d *Daemon) leftoverCopies(ctx context.Context) ([]registry.Orphan, []error) {
	type owner struct{ project, main string }
	type inService struct {
		svc    recipe.Service
		owners []owner
		tests  map[string]bool // the Test Databases of existing Worktrees
	}
	var services []*inService
	for _, p := range d.registry.Projects() {
		cfg, err := recipe.ResolveDir(p.Path)
		if err != nil {
			continue
		}
		svc, ok := cfg.DatabaseService()
		if !ok {
			continue
		}
		e, ok := databaseEngine(cfg.Database, database.Server{}).(database.TestEngine)
		if !ok {
			continue
		}
		i := slices.IndexFunc(services, func(s *inService) bool { return s.svc == svc })
		if i < 0 {
			services = append(services, &inService{svc: svc, tests: map[string]bool{}})
			i = len(services) - 1
		}
		s := services[i]
		s.owners = append(s.owners, owner{p.Name, e.Database(p.Name, "", p.Path)})
		s.tests[e.TestDatabase(p.Name, "", p.Path)] = true
		for _, w := range d.registry.Worktrees(p.Path) {
			s.tests[e.TestDatabase(p.Name, w.Label, w.Path)] = true
		}
	}
	d.orphanMu.Lock()
	recorded := d.registry.Orphans()
	d.orphanMu.Unlock()
	var leftovers []registry.Orphan
	var errs []error
	for _, s := range services {
		e, release, err := d.dropper(ctx, s.svc)
		if err != nil {
			errs = append(errs, fmt.Errorf("looking for parallel test copies in %s: %w", s.svc.Name(), err))
			continue
		}
		names, err := e.Databases(ctx)
		release()
		if err != nil {
			errs = append(errs, fmt.Errorf("looking for parallel test copies in %s: %w", s.svc.Name(), err))
			continue
		}
		for _, name := range names {
			test, ok := database.ParallelCopyOf(name)
			if !ok || s.tests[test] {
				continue
			}
			o := registry.Orphan{Ref: s.svc, Database: name, Kind: orphanParallelCopy}
			if slices.ContainsFunc(recorded, o.Same) {
				continue
			}
			for _, own := range s.owners {
				if test == own.main+naming.TestSuffix ||
					strings.HasPrefix(test, own.main+"__") && strings.HasSuffix(test, naming.TestSuffix) {
					o.ProjectName = own.project
					leftovers = append(leftovers, o)
					break
				}
			}
		}
	}
	return leftovers, errs
}

// listOrphans is every Orphan, oldest first.
func (d *Daemon) listOrphans() api.OrphanList {
	d.orphanMu.Lock()
	defer d.orphanMu.Unlock()
	list := api.OrphanList{Orphans: []api.Orphan{}}
	for _, o := range d.registry.Orphans() {
		list.Orphans = append(list.Orphans, apiOrphan(o))
	}
	return list
}

func apiOrphan(o registry.Orphan) api.Orphan {
	res := api.Orphan{
		Database: o.Database,
		Service:  o.Ref.Name(),
		Project:  o.ProjectName,
		Worktree: o.Worktree,
		Kind:     o.Kind,
		Since:    o.Since,
	}
	if !o.Since.IsZero() {
		res.Expires = o.Since.Add(OrphanGrace)
	}
	return res
}
