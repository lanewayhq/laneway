package daemon

import (
	"context"
	"io"

	"github.com/mrsafalpiya/portby.dev/internal/database"
	"github.com/mrsafalpiya/portby.dev/internal/provision"
	"github.com/mrsafalpiya/portby.dev/internal/recipe"
	"github.com/mrsafalpiya/portby.dev/internal/registry"
)

// databaseEngine is the Engine of the database config describes, or nil if
// the Project has no database. Each engine (#20 Postgres, #21 MySQL) adds a
// case here.
func databaseEngine(db recipe.Database) database.Engine {
	switch db.Engine {
	case "sqlite":
		return database.SQLite{Path: db.Path}
	default:
		return nil
	}
}

// worktreeDatabase is the database the Worktree w uses, which the Env Patch's
// {db} holds, and whether the Project has one.
func worktreeDatabase(p registry.Project, w envWorktree, db recipe.Database) (string, bool) {
	e := databaseEngine(db)
	if e == nil {
		return "", false
	}
	return database.Mode(db.Mode).Use(e.Database(p.Name, "", p.Path), e.Database(p.Name, w.name, w.dir)), true
}

// databaseStep is the setup step that gives the Worktree w its database, as
// config's mode says; false if the Project has none.
func databaseStep(p registry.Project, w envWorktree, db recipe.Database) (provision.Step, bool) {
	e := databaseEngine(db)
	if e == nil {
		return provision.Step{}, false
	}
	main, own := e.Database(p.Name, "", p.Path), e.Database(p.Name, w.name, w.dir)
	return provision.Step{Name: "database", Run: func(ctx context.Context, out io.Writer) error {
		warnings, err := database.Ensure(ctx, e, database.Mode(db.Mode), main, own, out)
		for _, warning := range warnings {
			provision.Warn(ctx, warning)
		}
		return err
	}}, true
}
