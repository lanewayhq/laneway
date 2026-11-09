package database

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/lanewayhq/laneway/internal/container"
	"github.com/lanewayhq/laneway/internal/naming"
)

// Server is how to reach and log in to the database Service an Engine's
// databases live in.
type Server struct {
	// Addr is the host:port the Service listens on.
	Addr     string
	User     string
	Password string
	// Exec runs a command (such as psql) inside the Service's container
	// and returns what it printed and how it exited.
	Exec func(ctx context.Context, cmd []string) (container.Output, error)
}

// PostgresMaxName is the longest a Postgres identifier may be (NAMEDATALEN
// - 1 bytes).
const PostgresMaxName = 63

// cloneAttempts is how many times Clone tries CREATE DATABASE … TEMPLATE
// while the main Worktree's database has connections that come back.
const cloneAttempts = 5

// Postgres is the Engine of databases in the Postgres Service at Server.
// Database names don't need the Server, so the zero Postgres names them.
type Postgres struct {
	Server Server
}

// Database is <project> for the main Worktree and <project>__<worktree> for
// others, made safe for Postgres (see naming.DatabaseName).
func (Postgres) Database(project, worktree, _ string) string {
	return naming.DatabaseName(project, worktree, PostgresMaxName)
}

// TestDatabase is the Worktree's database name with "_test", the database
// name cut short first so that the whole fits in PostgresMaxName (see
// naming.TestDatabaseName).
func (Postgres) TestDatabase(project, worktree, _ string) string {
	return naming.TestDatabaseName(project, worktree, PostgresMaxName)
}

func (p Postgres) Exists(ctx context.Context, db string) (bool, error) {
	out, err := p.psql(ctx, "SELECT 1 FROM pg_database WHERE datname = "+literal(db))
	return strings.TrimSpace(out) == "1", err
}

// Create creates db, empty. CREATE DATABASE is all or nothing: if ctx ends
// while it runs, Laneway stops waiting but psql runs on in the container, so
// db is either complete or not there.
func (p Postgres) Create(ctx context.Context, db string) error {
	_, err := p.psql(ctx, "CREATE DATABASE "+ident(db))
	return err
}

// Clone creates to with CREATE DATABASE … TEMPLATE from, which copies the
// database's files. Postgres refuses while anyone else is connected to
// from (SQLSTATE 55006), and the main Worktree's app, its queue worker or
// a GUI client often is. So when that happens Clone closes those
// connections (pg_terminate_backend) and tries again, up to cloneAttempts
// times. Laravel reconnects on its next query; a queue worker exits on the
// lost connection and its Process is restarted like any crash.
func (p Postgres) Clone(ctx context.Context, from, to string) error {
	create := "CREATE DATABASE " + ident(to) + " TEMPLATE " + ident(from)
	for attempt := 1; ; attempt++ {
		_, err := p.psql(ctx, create)
		var perr *psqlError
		if !errors.As(err, &perr) || perr.Code != objectInUse {
			return err
		}
		if attempt == cloneAttempts {
			return fmt.Errorf("%s is still in use after closing its connections %d times: %w", from, attempt-1, err)
		}
		// CREATE DATABASE waits a few seconds for the closed
		// connections' backends to exit. psql's own session is in the
		// maintenance database, never from.
		if _, err := p.psql(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = "+
			literal(from)+" AND pid <> pg_backend_pid()"); err != nil {
			return fmt.Errorf("closing the connections to %s: %w", from, err)
		}
	}
}

// Drop drops db with DROP DATABASE … WITH (FORCE) (Postgres 13 and later),
// which closes every connection to it first. One that doesn't exist is
// fine.
func (p Postgres) Drop(ctx context.Context, db string) error {
	_, err := p.psql(ctx, "DROP DATABASE IF EXISTS "+ident(db)+" WITH (FORCE)")
	return err
}

// Databases lists the server's databases, except the templates.
func (p Postgres) Databases(ctx context.Context) ([]string, error) {
	out, err := p.psql(ctx, "SELECT datname FROM pg_database WHERE NOT datistemplate")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// objectInUse is the SQLSTATE of CREATE DATABASE … TEMPLATE refused because
// others are connected to the template.
const objectInUse = "55006"

// psqlError is a statement psql ran and Postgres refused, or a psql that
// couldn't log in.
type psqlError struct {
	// Code is the error's SQLSTATE, such as "55006"; "" if psql didn't
	// print one.
	Code     string
	ExitCode int
	// Message is what psql printed on stderr.
	Message string
}

func (e *psqlError) Error() string {
	return fmt.Sprintf("psql exited with status %d: %s", e.ExitCode, e.Message)
}

// sqlstate finds the SQLSTATE in an error psql printed with VERBOSITY
// verbose: "ERROR:  55006: source database …".
var sqlstate = regexp.MustCompile(`(?m)(?:ERROR|FATAL):\s+([0-9A-Z]{5}):`)

// psql runs one statement with psql inside the Service's container, in the
// maintenance database, and returns what it printed: one line per row, no
// headers. psql logs in over the container's Unix socket, which the image
// trusts, so no password is needed. The statement is an argument, not
// shell input, so it needs no quoting beyond SQL's.
func (p Postgres) psql(ctx context.Context, sql string) (string, error) {
	if p.Server.Exec == nil {
		return "", errors.New("the Postgres Service isn't running")
	}
	out, err := p.Server.Exec(ctx, []string{"psql", "-X", "-q", "-t", "-A",
		"-v", "ON_ERROR_STOP=1", "-v", "VERBOSITY=verbose",
		"-U", p.Server.User, "-d", "postgres", "-c", sql})
	if err != nil {
		return "", fmt.Errorf("running psql in the Postgres Service: %w", err)
	}
	if out.ExitCode != 0 {
		perr := &psqlError{ExitCode: out.ExitCode, Message: strings.TrimSpace(string(out.Stderr))}
		if m := sqlstate.FindStringSubmatch(perr.Message); m != nil {
			perr.Code = m[1]
		}
		return "", perr
	}
	return string(out.Stdout), nil
}

// ident quotes name as an identifier: "name", with " doubled.
func ident(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// literal quotes s as a string literal: 's', with ' doubled. The server
// has standard_conforming_strings on (the default since 9.1).
func literal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
