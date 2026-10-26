package daemon

import (
	"context"
	"fmt"
	"io"

	"github.com/mrsafalpiya/portby.dev/internal/api"
	"github.com/mrsafalpiya/portby.dev/internal/database"
	"github.com/mrsafalpiya/portby.dev/internal/provision"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
)

// worktreeTestDatabase is the Test Database of the Worktree w, which the Env
// Patch's {test_db} holds, and whether it has one: config's Test Database
// decision is on and the database's engine makes them (database.TestEngine).
// Every Worktree has its own, whatever the database mode: even Worktrees
// sharing the main Worktree's database never share a Test Database.
func worktreeTestDatabase(p registry.Project, w envWorktree, cfg recipe.Resolved) (string, bool) {
	if !cfg.TestDatabase {
		return "", false
	}
	e, ok := databaseEngine(cfg.Database, database.Server{}).(database.TestEngine)
	if !ok {
		return "", false
	}
	return e.TestDatabase(p.Name, w.name, w.dir), true
}

// testDatabaseStep is the setup step that gives the Worktree w its Test
// Database, always empty and never copied; false if config's Test Database
// decision is off. It starts the database's Service like the database step
// and keeps it from going Idle until the step ends.
func (d *Daemon) testDatabaseStep(p registry.Project, w envWorktree, cfg recipe.Resolved) (provision.Step, bool) {
	if !cfg.TestDatabase {
		return provision.Step{}, false
	}
	return provision.Step{Name: "test database", Run: func(ctx context.Context, out io.Writer) error {
		name, ok := worktreeTestDatabase(p, w, cfg)
		svc, inService := cfg.DatabaseService()
		if !ok || !inService {
			return fmt.Errorf("Portby can't make Test Databases of %s databases; set database.test to off", cfg.Database.Engine)
		}
		server, release, err := d.databaseServer(ctx, svc, out)
		if err != nil {
			return err
		}
		defer release()
		return database.EnsureTest(ctx, databaseEngine(cfg.Database, server), name, out)
	}}, true
}

// databaseInfo is the API view of the databases of the Project's Worktree at
// dir: the {db} and {test_db} it is given. nil if the Registry doesn't know
// the Worktree, its config can't be read or the Project has no database.
func (d *Daemon) databaseInfo(p registry.Project, dir string) *api.Database {
	w, ok := d.envWorktreeOf(p, dir)
	if !ok {
		return nil
	}
	cfg, err := recipe.ResolveDir(dir)
	if err != nil {
		return nil
	}
	name, ok := worktreeDatabase(p, w, cfg.Database)
	if !ok {
		return nil
	}
	test, _ := worktreeTestDatabase(p, w, cfg)
	return &api.Database{Engine: cfg.Database.Engine, Name: name, Test: test}
}
