package database

import (
	"context"
	"fmt"

	"github.com/mrsafalpiya/portby.dev/internal/naming"
	"github.com/mrsafalpiya/portby.dev/internal/pgwire"
)

// Server is how to reach and log in to the database Service an Engine's
// databases live in.
type Server struct {
	// Addr is the host:port the Service listens on.
	Addr     string
	User     string
	Password string
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
	var exists bool
	err := p.admin(ctx, func(c *pgwire.Conn) error {
		rows, err := c.Query(ctx, "SELECT 1 FROM pg_database WHERE datname = "+pgwire.Literal(db))
		exists = len(rows) > 0
		return err
	})
	return exists, err
}

// Create creates db, empty. CREATE DATABASE is all or nothing, and if ctx
// ends while it runs the server is told to cancel it.
func (p Postgres) Create(ctx context.Context, db string) error {
	return p.admin(ctx, func(c *pgwire.Conn) error {
		return c.Exec(ctx, "CREATE DATABASE "+pgwire.Ident(db))
	})
}

// Clone creates to with CREATE DATABASE … TEMPLATE from, which copies the
// database's files. Postgres refuses while anyone else is connected to
// from (SQLSTATE 55006), and the main Worktree's app, its queue worker or
// a GUI client often is. So when that happens Clone closes those
// connections (pg_terminate_backend) and tries again, up to cloneAttempts
// times. Laravel reconnects on its next query; a queue worker exits on the
// lost connection and its Process is restarted like any crash.
func (p Postgres) Clone(ctx context.Context, from, to string) error {
	return p.admin(ctx, func(c *pgwire.Conn) error {
		create := "CREATE DATABASE " + pgwire.Ident(to) + " TEMPLATE " + pgwire.Ident(from)
		for attempt := 1; ; attempt++ {
			err := c.Exec(ctx, create)
			if !pgwire.HasCode(err, pgwire.ObjectInUse) {
				return err
			}
			if attempt == cloneAttempts {
				return fmt.Errorf("%s is still in use after closing its connections %d times: %w", from, attempt-1, err)
			}
			// CREATE DATABASE waits a few seconds for the closed
			// connections' backends to exit.
			if err := c.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = "+
				pgwire.Literal(from)+" AND pid <> pg_backend_pid()"); err != nil {
				return fmt.Errorf("closing the connections to %s: %w", from, err)
			}
		}
	})
}

// admin runs f on a connection to the server's maintenance database.
func (p Postgres) admin(ctx context.Context, f func(*pgwire.Conn) error) error {
	c, err := pgwire.Connect(ctx, pgwire.Config{
		Addr: p.Server.Addr, User: p.Server.User, Password: p.Server.Password, Database: "postgres"})
	if err != nil {
		return fmt.Errorf("connecting to Postgres at %s: %w", p.Server.Addr, err)
	}
	defer c.Close()
	return f(c)
}
