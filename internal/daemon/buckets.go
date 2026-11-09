package daemon

import (
	"context"
	"fmt"
	"io"

	"github.com/lanewayhq/laneway/internal/api"
	"github.com/lanewayhq/laneway/internal/bucket"
	"github.com/lanewayhq/laneway/internal/config"
	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/naming"
	"github.com/lanewayhq/laneway/internal/prepare"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/registry"
)

// worktreeBucket is one of a Worktree's Buckets (ADR-0008).
type worktreeBucket struct {
	// Service is the object storage Service the Bucket is in, and Engine
	// its Engine, which has Bucket commands.
	Service recipe.Service
	Engine  *engine.Manifest
	// Name is the Bucket's name in the Service (naming.BucketName).
	Name string
	// Main is the main Worktree's Bucket of the same name, which a new
	// Worktree's Bucket starts as a copy of; Name itself in the main
	// Worktree.
	Main string
	// Variable is the Env Patch variable holding Name, such as
	// rustfs_bucket.
	Variable string
	// Public is whether anyone may read its objects without signing.
	Public bool
}

// worktreeBuckets are the Buckets of the Worktree w whose config is cfg, in
// cfg.Services' order: a private one in each Service whose Engine has Bucket
// commands. A Bucket config declares adds its name to the Bucket's (the
// suffix of naming.BucketName).
func (d *Daemon) worktreeBuckets(p registry.Project, w worktreeRef, cfg recipe.Resolved) []worktreeBucket {
	var buckets []worktreeBucket
	for _, svc := range cfg.Services {
		e, ok := d.plugins.Engines().Lookup(svc.Engine)
		if !ok || e.Bucket == nil {
			continue
		}
		buckets = append(buckets, worktreeBucket{Service: svc, Engine: e,
			Name: naming.BucketName(p.Name, w.label, ""), Main: naming.BucketName(p.Name, "", ""),
			Variable: e.BucketVariable()})
	}
	return buckets
}

// bucketVars are the Env Patch variables naming the Worktree w's Buckets,
// such as {rustfs_bucket}.
func (d *Daemon) bucketVars(p registry.Project, w worktreeRef, cfg recipe.Resolved) map[string]string {
	vars := map[string]string{}
	for _, b := range d.worktreeBuckets(p, w, cfg) {
		vars[b.Variable] = b.Name
	}
	return vars
}

// DefaultBucketCopyLimit is the biggest the main Worktree's Bucket may be
// for a new Worktree's Bucket to start as a copy of it, unless
// bucket_copy_limit is configured.
const DefaultBucketCopyLimit config.Size = 1e9

// bucketSteps are the setup steps that give the Worktree w its Buckets,
// one per Bucket, named after it. Each starts the Bucket's Service like the
// database step and keeps it from going Idle until the step ends. A Bucket
// that exists is left as it is; the main Worktree's starts empty, and any
// other Worktree's as a copy of the main Worktree's, unless that is over
// the limit (bucket_copy_limit).
func (d *Daemon) bucketSteps(p registry.Project, w worktreeRef, cfg recipe.Resolved) []prepare.Step {
	var steps []prepare.Step
	for _, b := range d.worktreeBuckets(p, w, cfg) {
		steps = append(steps, prepare.Step{Name: "bucket " + b.Name, Run: func(ctx context.Context, out io.Writer) error {
			var from bucket.Source
			if !w.main {
				var err error
				if from, err = d.bucketSource(b, cfg); err != nil {
					return err
				}
			}
			server, release, err := d.databaseServer(ctx, b.Service, out)
			if err != nil {
				return err
			}
			defer release()
			warnings, err := bucket.Ensure(ctx, bucket.Commands{Engine: b.Engine, Exec: server.Exec}, b.Name, from, out)
			for _, warning := range warnings {
				prepare.Warn(ctx, warning)
			}
			return err
		}})
	}
	return steps
}

// bucketSource is what the Bucket b of a Worktree other than the main one
// starts as: a copy of the main Worktree's, up to bucket_copy_limit from
// the Project's config cfg, else the global config's, else
// DefaultBucketCopyLimit.
func (d *Daemon) bucketSource(b worktreeBucket, cfg recipe.Resolved) (bucket.Source, error) {
	path := d.cfg.Paths.GlobalConfig()
	global, err := config.LoadGlobal(path)
	if err != nil {
		return bucket.Source{}, err
	}
	limit := DefaultBucketCopyLimit
	for _, l := range []*config.Size{cfg.BucketCopyLimit, global.BucketCopyLimit} {
		if l != nil {
			limit = *l
			break
		}
	}
	where := config.FileName
	if path != "" {
		where += ", or for every Project in " + path
	}
	return bucket.Source{Main: b.Main, Limit: limit,
		Setting: fmt.Sprintf("raise bucket_copy_limit (now %s) in %s", limit, where)}, nil
}

// bucketInfo is the API view of the Worktree w's Buckets.
func (d *Daemon) bucketInfo(p registry.Project, w worktreeRef, cfg recipe.Resolved) []api.InfoBucket {
	out := []api.InfoBucket{}
	for _, b := range d.worktreeBuckets(p, w, cfg) {
		out = append(out, api.InfoBucket{Name: b.Name, Service: b.Service.Name(), Public: b.Public})
	}
	return out
}
