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
	"strings"
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
	// dir, for the Project called project; worktree is the Worktree's label
	// (its part of its Domain, unique within the Project), "" for the main
	// Worktree.
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

// TestEngine is an Engine whose Worktrees can also have a Test Database: an
// empty database of their own for their test suite. Engines of databases in
// a Service (Postgres, MySQL) are TestEngines; SQLite isn't.
type TestEngine interface {
	Engine
	// TestDatabase is the name of the Test Database of the Worktree
	// checked out at dir, for the Project called project; worktree is
	// its label, as for Database, "" for the main Worktree. It is the Worktree's
	// own database name (Database) with "_test", cut short to fit the
	// engine's limit.
	TestDatabase(project, worktree, dir string) string
}

// EnsureTest makes sure the Test Database db exists. It is always created
// empty, never copied from another database, and one that exists is never
// touched, so running EnsureTest again is safe. It says what it did on out.
func EnsureTest(ctx context.Context, e Engine, db string, out io.Writer) error {
	exists, err := e.Exists(ctx, db)
	if err != nil {
		return err
	}
	if exists {
		fmt.Fprintf(out, "%s exists; leaving it as it is\n", db)
		return nil
	}
	if err := e.Create(ctx, db); err != nil {
		return fmt.Errorf("creating the Test Database %s: %w", db, err)
	}
	fmt.Fprintf(out, "created the empty Test Database %s\n", db)
	return nil
}

// Dropper is an Engine whose databases Portby can drop once they are
// Orphans: the Engines of databases in a Service (Postgres, MySQL). SQLite
// isn't one: its database is a file in the Worktree's directory, which goes
// with the Worktree, and the main Worktree's file is the developer's.
type Dropper interface {
	Engine
	// Drop drops db, closing any connections to it first. Dropping a
	// database that doesn't exist is not an error.
	Drop(ctx context.Context, db string) error
	// Databases lists every database in the Service, so Portby can find
	// the parallel test copies of a Test Database (ParallelCopy).
	Databases(ctx context.Context) ([]string, error)
}

// ParallelCopy reports whether db is a copy of the Test Database test made
// by a parallel test run: <test>_test_<n>, as Laravel's parallel testing
// names them (one per test process, numbered from 1).
func ParallelCopy(test, db string) bool {
	n, ok := strings.CutPrefix(db, test+"_test_")
	if !ok || n == "" {
		return false
	}
	for _, r := range n {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ParallelCopyOf is the Test Database db is a parallel test copy of
// (ParallelCopy), if it looks like one.
func ParallelCopyOf(db string) (test string, ok bool) {
	i := strings.LastIndex(db, "_test_")
	if i <= 0 || !ParallelCopy(db[:i], db) {
		return "", false
	}
	return db[:i], true
}
