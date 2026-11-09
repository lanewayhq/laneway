package daemon

import (
	"context"
	"fmt"
	"io"

	"github.com/lanewayhq/laneway/internal/container"
	"github.com/lanewayhq/laneway/internal/database"
	"github.com/lanewayhq/laneway/internal/prepare"
	"github.com/lanewayhq/laneway/internal/recipe"
	"github.com/lanewayhq/laneway/internal/registry"
)

// databaseEngine is the Engine of the database config describes, or nil if
// the Project has no database (see database.New).
// server is how to reach the Service a Service engine's databases live in;
// naming databases doesn't need it, so the zero Server will do for that.
func (d *Daemon) databaseEngine(db recipe.Database, server database.Server) database.Engine {
	return database.New(d.plugins.Engines(), db.Engine, db.Path, server)
}

// worktreeDatabase is the database the Worktree w uses, which the Env Patch's
// {db} holds, and whether the Project has one.
func (d *Daemon) worktreeDatabase(p registry.Project, w worktreeRef, db recipe.Database) (string, bool) {
	e := d.databaseEngine(db, database.Server{})
	if e == nil {
		return "", false
	}
	return database.Mode(db.Mode).Use(e.Database(p.Name, "", p.Path), e.Database(p.Name, w.label, w.dir)), true
}

// databaseVars are the Env Patch variables saying where the database is,
// {db_host}, {db_port}, {db_username} and {db_password}, for a Project whose
// database lives in a Service: those of the Service's variables (vars, from
// serviceVars). A SQLite database has none.
func databaseVars(cfg recipe.Resolved, vars map[string]string) map[string]string {
	svc, ok := cfg.DatabaseService()
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, v := range []string{"host", "port", "username", "password"} {
		if value, ok := vars[svc.Engine+"_"+v]; ok {
			out["db_"+v] = value
		}
	}
	return out
}

// databaseStep is the setup step that gives the Worktree w its database, as
// config's mode says; false if the Project has none. A database in a Service
// starts the Service first (pulling its image if needed) and keeps it from
// going Idle until the step ends.
func (d *Daemon) databaseStep(p registry.Project, w worktreeRef, cfg recipe.Resolved) (prepare.Step, bool) {
	db := cfg.Database
	names := d.databaseEngine(db, database.Server{})
	if names == nil {
		return prepare.Step{}, false
	}
	main, own := names.Database(p.Name, "", p.Path), names.Database(p.Name, w.label, w.dir)
	return prepare.Step{Name: "database", Run: func(ctx context.Context, out io.Writer) error {
		var server database.Server
		if svc, ok := cfg.DatabaseService(); ok && database.Mode(db.Mode).Use(main, own) == own {
			var release func()
			var err error
			server, release, err = d.databaseServer(ctx, svc, out)
			if err != nil {
				return err
			}
			defer release()
		}
		warnings, err := database.Ensure(ctx, d.databaseEngine(db, server), database.Mode(db.Mode), main, own, out)
		for _, warning := range warnings {
			prepare.Warn(ctx, warning)
		}
		return err
	}}, true
}

// databaseServer starts the Service svc for a database or Bucket step and
// says how to reach it. Call release once the step is done with it.
func (d *Daemon) databaseServer(ctx context.Context, svc recipe.Service, out io.Writer) (database.Server, func(), error) {
	e, ok := d.plugins.Engines().Lookup(svc.Engine)
	if !ok {
		return database.Server{}, nil, fmt.Errorf("Laneway has no Service %q", svc.Engine)
	}
	d.serveServices([]recipe.Service{svc})
	if _, err := d.registry.PlanService(svc, e.Ports(svc.Version)); err != nil {
		return database.Server{}, nil, err
	}
	name := svc.Name()
	release := d.services.Use(name)
	fmt.Fprintf(out, "starting the Service %s\n", name)
	if err := d.services.Pull(ctx, name); err != nil {
		release()
		return database.Server{}, nil, fmt.Errorf("getting the image of the Service %s: %w", name, err)
	}
	if err := d.services.Up(ctx, name); err != nil {
		release()
		return database.Server{}, nil, err
	}
	return database.Server{
		Exec: func(ctx context.Context, cmd []string) (container.Output, error) {
			return d.services.Exec(ctx, name, cmd)
		},
	}, release, nil
}
