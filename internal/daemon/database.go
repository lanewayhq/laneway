package daemon

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/mrsafalpiya/portby.dev/internal/database"
	"github.com/mrsafalpiya/portby.dev/internal/prepare"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
	"github.com/mrsafalpiya/portby.dev/internal/service"
)

// databaseEngine is the Engine of the database config describes, or nil if
// the Project has no database. Each engine adds a case here.
// server is how to reach the Service a Service engine's databases live in;
// naming databases doesn't need it, so the zero Server will do for that.
func databaseEngine(db recipe.Database, server database.Server) database.Engine {
	switch db.Engine {
	case "sqlite":
		return database.SQLite{Path: db.Path}
	case "postgres":
		return database.Postgres{Server: server}
	case "mysql":
		return database.MySQL{Server: server}
	default:
		return nil
	}
}

// worktreeDatabase is the database the Worktree w uses, which the Env Patch's
// {db} holds, and whether the Project has one.
func worktreeDatabase(p registry.Project, w envWorktree, db recipe.Database) (string, bool) {
	e := databaseEngine(db, database.Server{})
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
func (d *Daemon) databaseStep(p registry.Project, w envWorktree, cfg recipe.Resolved) (prepare.Step, bool) {
	db := cfg.Database
	names := databaseEngine(db, database.Server{})
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
		warnings, err := database.Ensure(ctx, databaseEngine(db, server), database.Mode(db.Mode), main, own, out)
		for _, warning := range warnings {
			prepare.Warn(ctx, warning)
		}
		return err
	}}, true
}

// databaseServer starts the Service svc for a database step and says how to
// reach it. Call release once the step is done with it.
func (d *Daemon) databaseServer(ctx context.Context, svc recipe.Service, out io.Writer) (database.Server, func(), error) {
	e, ok := service.Lookup(svc.Engine)
	if !ok {
		return database.Server{}, nil, fmt.Errorf("Portby has no Service %q", svc.Engine)
	}
	d.serveServices([]recipe.Service{svc})
	rs, err := d.registry.PlanService(svc.Engine, svc.Version, e.Ports(svc.Version))
	if err != nil {
		return database.Server{}, nil, err
	}
	name := svc.Engine + ":" + svc.Version
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
		Addr:     net.JoinHostPort(d.serviceHost(), strconv.Itoa(rs.Port)),
		User:     e.User,
		Password: service.Password,
	}, release, nil
}
