// Package database gives each Worktree its own database. What a Worktree gets
// depends on the Project's mode (Clone, Fresh or Shared) and is the same for
// every engine; how a database is created or copied is the Engine's.
//
// SQLite is the first Engine. Service-backed engines (Postgres, MySQL) add
// their own Engine whose databases are names in a Service rather than files.
package database

import (
	"context"
	"fmt"
	"io"
)

// Mode is how a Worktree other than the main one gets its database
// ([database] worktree in config).
type Mode string

const (
	// Clone copies the main Worktree's database, falling back to Fresh
	// when the main Worktree's database doesn't exist.
	Clone Mode = "clone"
	// Fresh creates an empty database.
	Fresh Mode = "fresh"
	// Shared uses the main Worktree's database: nothing is created.
	Shared Mode = "shared"
)

// Engine creates and copies databases of one engine. A database is named by
// a string whose meaning is the Engine's: a file's absolute path for SQLite,
// a database name in its Service for Postgres or MySQL. That name is what the
// Env Patch's {db} variable holds.
//
// Every method must stop when ctx is cancelled, and must leave either a
// complete database or none behind, so that a setup interrupted half way
// creates it again from the start.
type Engine interface {
	// Database is the name of the database of the Worktree checked out at
	// dir, for the Project called project; worktree is git's name for the
	// Worktree, "" for the main Worktree.
	Database(project, worktree, dir string) string
	// Exists reports whether db exists.
	Exists(ctx context.Context, db string) (bool, error)
	// Create creates db, empty. db doesn't exist.
	Create(ctx context.Context, db string) error
	// Clone creates to as a copy of from. from exists and to doesn't.
	Clone(ctx context.Context, from, to string) error
}

// Use is the database a Worktree whose own database is own uses, when the
// main Worktree's is main: main if the mode is Shared, else own.
func (m Mode) Use(main, own string) string {
	if m == Shared {
		return main
	}
	return own
}

// Ensure makes sure the Worktree whose own database is db has it, as mode
// says, when the main Worktree's is main (db == main for the main Worktree,
// whose database is created empty if missing). A database that exists is
// never touched, so running Ensure again is safe. It says what it did on out
// and returns warnings for the developer, such as a clone that fell back to
// an empty database.
func Ensure(ctx context.Context, e Engine, mode Mode, main, db string, out io.Writer) (warnings []string, err error) {
	if mode == Shared && db != main {
		fmt.Fprintf(out, "using the main Worktree's database %s\n", main)
		return nil, nil
	}
	exists, err := e.Exists(ctx, db)
	if err != nil {
		return nil, err
	}
	if exists {
		fmt.Fprintf(out, "%s exists; leaving it as it is\n", db)
		return nil, nil
	}
	if mode == Clone && db != main {
		mainExists, err := e.Exists(ctx, main)
		if err != nil {
			return nil, err
		}
		if mainExists {
			if err := e.Clone(ctx, main, db); err != nil {
				return nil, fmt.Errorf("copying the main Worktree's database %s to %s: %w", main, db, err)
			}
			fmt.Fprintf(out, "copied the main Worktree's database %s to %s\n", main, db)
			return nil, nil
		}
		warnings = append(warnings, fmt.Sprintf(
			"the main Worktree's database %s doesn't exist, so %s was created empty instead of copied from it", main, db))
	}
	if err := e.Create(ctx, db); err != nil {
		return nil, fmt.Errorf("creating the database %s: %w", db, err)
	}
	fmt.Fprintf(out, "created the empty database %s\n", db)
	return warnings, nil
}
