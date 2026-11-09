package daemon

import (
	"context"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/bucket"
	"github.com/lanewayhq/laneway/internal/naming"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/registry"
)

// A removed Worktree's Buckets become Orphans like its databases
// (ADR-0008): recorded from config when it goes, then checked against the
// Bucket commands' list (checkOrphans), and dropped with drop.

// goneWorktree is a Worktree whose databases and Buckets were just made
// Orphans, and the name of its Project.
type goneWorktree struct {
	project string
	w       worktreeRef
}

// bucketOrphans are the Buckets of the Worktree w that become Orphans when
// it goes, as Orphans since since: every Bucket cfg gives it
// (worktreeBuckets).
func (d *Daemon) bucketOrphans(p registry.Project, w worktreeRef, cfg recipe.Resolved, since time.Time) []registry.Orphan {
	var out []registry.Orphan
	for _, b := range d.worktreeBuckets(p, w, cfg) {
		out = append(out, registry.Orphan{Ref: b.Service, Name: b.Name, ProjectName: p.Name,
			Worktree: w.name, Kind: api.OrphanBucket, Since: since})
	}
	return out
}

// orphanedBuckets are the Buckets, of those listed in names in the Service
// of batch (Bucket Orphans just recorded from config), that the naming rule
// gives a Worktree in gone (naming.BucketOf) and that aren't recorded
// already, as Orphans like that Worktree's in batch: Buckets its Project
// declares, or once declared, whatever its config says now. A Bucket the
// rule also gives an existing Worktree, of any Linked Project, is never
// one. The caller holds orphanMu.
func (d *Daemon) orphanedBuckets(batch []registry.Orphan, names []string, gone []goneWorktree, recorded []registry.Orphan) []registry.Orphan {
	var out []registry.Orphan
	for _, name := range names {
		for _, g := range gone {
			i := slices.IndexFunc(batch, func(o registry.Orphan) bool {
				return o.ProjectName == g.project && o.Worktree == g.w.name
			})
			if i < 0 || !naming.BucketOf(name, g.project, g.w.label) {
				continue
			}
			o := batch[i]
			o.Name = name
			if !slices.ContainsFunc(recorded, o.Same) && !slices.ContainsFunc(out, o.Same) && !d.existingWorktreeBucket(name, gone) {
				out = append(out, o)
			}
			break
		}
	}
	return out
}

// orphanBucketsOf are the recorded Orphans, of the Project p, that are
// Buckets the naming rule gives its Worktree w, whatever its config says
// now. The caller holds orphanMu.
func (d *Daemon) orphanBucketsOf(p registry.Project, w worktreeRef) []registry.Orphan {
	var out []registry.Orphan
	for _, o := range d.registry.Orphans() {
		if placeOf(o.Kind) == inBuckets && o.ProjectName == p.Name && naming.BucketOf(o.Name, p.Name, w.label) {
			out = append(out, o)
		}
	}
	return out
}

// existingWorktreeBucket reports whether the naming rule gives the Bucket
// name to a Worktree, of any Linked Project, that isn't one of those gone
// (which may still be recorded while they are made Orphans).
func (d *Daemon) existingWorktreeBucket(name string, gone []goneWorktree) bool {
	for _, p := range d.registry.Projects() {
		for _, w := range d.worktreesOf(p) {
			if slices.ContainsFunc(gone, func(g goneWorktree) bool { return g.project == p.Name && g.w.label == w.label }) {
				continue
			}
			if naming.BucketOf(name, p.Name, w.label) {
				return true
			}
		}
	}
	return false
}

// bucketStore is a Service's Buckets, as an orphanStore.
type bucketStore struct{ c bucket.Commands }

func (s bucketStore) names(ctx context.Context) ([]string, error) { return s.c.List(ctx) }
func (s bucketStore) drop(ctx context.Context, name string) error { return s.c.Drop(ctx, name) }

// bucketStore starts the object storage Service svc and returns its
// Buckets. Call release once done with it, so the Service can go Idle.
func (d *Daemon) bucketStore(ctx context.Context, svc recipe.Service) (orphanStore, func(), error) {
	e, ok := d.plugins.Engines().Lookup(svc.Engine)
	if !ok || e.Bucket == nil {
		return nil, nil, fmt.Errorf("Laneway can't drop %s Buckets", svc.Engine)
	}
	server, release, err := d.startService(ctx, svc, io.Discard)
	if err != nil {
		return nil, nil, err
	}
	return bucketStore{bucket.Commands{Engine: e, Exec: server.Exec}}, release, nil
}
