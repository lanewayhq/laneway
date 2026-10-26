package database

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mrsafalpiya/portby.dev/internal/mysqlwire"
	"github.com/mrsafalpiya/portby.dev/internal/naming"
)

// MySQLMaxName is the longest a MySQL database name may be.
const MySQLMaxName = 64

// copyMarker is a table Clone puts in the database it is building and drops
// last. MySQL can't create a database and its contents all at once, so a
// database that still has it is a copy cut short (Portby was killed half
// way): Exists says it doesn't exist, and Create and Clone drop it first.
const copyMarker = "portby_copy_in_progress"

// MySQL is the Engine of databases in the MySQL Service at Server. Database
// names don't need the Server, so the zero MySQL names them.
type MySQL struct {
	Server Server
}

// Database is <project> for the main Worktree and <project>__<worktree> for
// others, made safe for MySQL (see naming.DatabaseName).
func (MySQL) Database(project, worktree, _ string) string {
	return naming.DatabaseName(project, worktree, MySQLMaxName)
}

// TestDatabase is the Worktree's database name with "_test", the database
// name cut short first so that the whole fits in MySQLMaxName (see
// naming.TestDatabaseName).
func (MySQL) TestDatabase(project, worktree, _ string) string {
	return naming.TestDatabaseName(project, worktree, MySQLMaxName)
}

func (m MySQL) Exists(ctx context.Context, db string) (bool, error) {
	var exists bool
	err := m.admin(ctx, func(c *mysqlwire.Conn) error {
		var err error
		exists, err = schemaExists(ctx, c, db)
		if err != nil || !exists {
			return err
		}
		unfinished, err := isUnfinishedCopy(ctx, c, db)
		exists = !unfinished
		return err
	})
	return exists, err
}

// Create creates db, empty.
func (m MySQL) Create(ctx context.Context, db string) error {
	return m.admin(ctx, func(c *mysqlwire.Conn) error {
		if err := dropUnfinishedCopy(ctx, c, db); err != nil {
			return err
		}
		return c.Exec(ctx, "CREATE DATABASE "+mysqlwire.Ident(db))
	})
}

// Clone creates to as a copy of from by dump and restore, done by the server
// itself rather than through Portby: every table's definition (SHOW CREATE
// TABLE) and rows (INSERT … SELECT), then its routines, views and triggers,
// like mysqldump --routines (events aren't copied: they would run on the
// copy too). Each table is read as one consistent snapshot without locking
// it, so the main Worktree's app, queue worker or GUI client carry on; a
// write landing during the copy may be in some tables and not others. If
// the copy fails or ctx ends, to is dropped.
func (m MySQL) Clone(ctx context.Context, from, to string) error {
	created := false
	err := m.admin(ctx, func(c *mysqlwire.Conn) error {
		if err := dropUnfinishedCopy(ctx, c, to); err != nil {
			return err
		}
		rows, err := c.Query(ctx, schemaQuery(from))
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("the database %s doesn't exist", from)
		}
		if err := c.Exec(ctx, "CREATE DATABASE "+mysqlwire.Ident(to)+
			" CHARACTER SET "+rows[0][0]+" COLLATE "+rows[0][1]); err != nil {
			return err
		}
		created = true
		if err := c.Exec(ctx, "CREATE TABLE "+mysqlwire.Ident(to)+"."+copyMarker+" (id int)"); err != nil {
			return err
		}
		if err := copyDatabase(ctx, c, from, to); err != nil {
			return err
		}
		return c.Exec(ctx, "DROP TABLE "+mysqlwire.Ident(to)+"."+copyMarker)
	})
	if err != nil && created {
		// The connection may be gone with ctx: drop it on a new one.
		dropCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if dropErr := m.admin(dropCtx, func(c *mysqlwire.Conn) error {
			return c.Exec(dropCtx, "DROP DATABASE IF EXISTS "+mysqlwire.Ident(to))
		}); dropErr != nil {
			return fmt.Errorf("%w (and dropping the unfinished copy failed: %v)", err, dropErr)
		}
	}
	return err
}

// copySQLMode is the session's sql_mode while copying: none of the strict
// modes, so every row the main database holds goes in as it is, and
// NO_AUTO_VALUE_ON_ZERO, so a 0 in an AUTO_INCREMENT column stays 0 (as
// mysqldump does).
const copySQLMode = "NO_AUTO_VALUE_ON_ZERO"

// copyDatabase copies from's contents into to, which is empty.
func copyDatabase(ctx context.Context, c *mysqlwire.Conn, from, to string) error {
	for _, set := range []string{
		"SET SESSION foreign_key_checks = 0",
		"SET SESSION sql_mode = " + mysqlwire.Literal(copySQLMode),
		"SET SESSION time_zone = '+00:00'", // TIMESTAMPs go across unconverted
		"SET SESSION TRANSACTION ISOLATION LEVEL READ COMMITTED",
		"USE " + mysqlwire.Ident(to),
	} {
		if err := c.Exec(ctx, set); err != nil {
			return err
		}
	}
	objects, err := c.Query(ctx, "SELECT TABLE_NAME, TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA = "+
		mysqlwire.Literal(from)+" ORDER BY TABLE_NAME")
	if err != nil {
		return err
	}
	var tables, views []string
	for _, o := range objects {
		switch o[1] {
		case "BASE TABLE":
			tables = append(tables, o[0])
		case "VIEW":
			views = append(views, o[0])
		}
	}
	src := func(name string) string { return mysqlwire.Ident(from) + "." + mysqlwire.Ident(name) }
	for _, t := range tables {
		rows, err := c.Query(ctx, "SHOW CREATE TABLE "+src(t))
		if err != nil {
			return err
		}
		if err := c.Exec(ctx, rows[0][1]); err != nil {
			return fmt.Errorf("creating the table %s: %w", t, err)
		}
	}
	for _, t := range tables {
		// Generated columns can't be written; invisible ones (which
		// SELECT * leaves out) must be.
		rows, err := c.Query(ctx, "SELECT COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = "+
			mysqlwire.Literal(from)+" AND TABLE_NAME = "+mysqlwire.Literal(t)+
			" AND GENERATION_EXPRESSION = '' ORDER BY ORDINAL_POSITION")
		if err != nil {
			return err
		}
		var columns []string
		for _, r := range rows {
			columns = append(columns, mysqlwire.Ident(r[0]))
		}
		list := strings.Join(columns, ", ")
		if err := c.Exec(ctx, "INSERT INTO "+mysqlwire.Ident(t)+" ("+list+") SELECT "+list+" FROM "+src(t)); err != nil {
			return fmt.Errorf("copying the rows of %s: %w", t, err)
		}
	}
	// Routines first: a view may call a function, and a trigger a
	// procedure.
	routines, err := c.Query(ctx, "SELECT ROUTINE_NAME, ROUTINE_TYPE FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = "+
		mysqlwire.Literal(from)+" ORDER BY ROUTINE_NAME")
	if err != nil {
		return err
	}
	for _, r := range routines {
		if err := recreate(ctx, c, "SHOW CREATE "+r[1]+" "+src(r[0])); err != nil {
			return fmt.Errorf("creating the %s %s: %w", strings.ToLower(r[1]), r[0], err)
		}
	}
	if err := copyViews(ctx, c, from, to, views); err != nil {
		return err
	}
	triggers, err := c.Query(ctx, "SELECT TRIGGER_NAME FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = "+
		mysqlwire.Literal(from)+" ORDER BY EVENT_OBJECT_TABLE, ACTION_TIMING, EVENT_MANIPULATION, ACTION_ORDER")
	if err != nil {
		return err
	}
	for _, tr := range triggers {
		if err := recreate(ctx, c, "SHOW CREATE TRIGGER "+src(tr[0])); err != nil {
			return fmt.Errorf("creating the trigger %s: %w", tr[0], err)
		}
	}
	return nil
}

// recreate runs the CREATE statement a SHOW CREATE TRIGGER / PROCEDURE /
// FUNCTION gives (its third column) under the sql_mode it was created with
// (its second), which the object keeps.
func recreate(ctx context.Context, c *mysqlwire.Conn, show string) error {
	rows, err := c.Query(ctx, show)
	if err != nil {
		return err
	}
	if len(rows) == 0 || len(rows[0]) < 3 {
		return fmt.Errorf("%s gave nothing", show)
	}
	if err := c.Exec(ctx, "SET SESSION sql_mode = "+mysqlwire.Literal(rows[0][1])); err != nil {
		return err
	}
	if err := c.Exec(ctx, rows[0][2]); err != nil {
		return err
	}
	return c.Exec(ctx, "SET SESSION sql_mode = "+mysqlwire.Literal(copySQLMode))
}

// copyViews creates from's views in to. A view's definition names its
// tables with their database, so from is replaced with to. A view may use
// another view, so they are created in rounds until none is left or a round
// creates none (the error of a view that can't be created).
func copyViews(ctx context.Context, c *mysqlwire.Conn, from, to string, views []string) error {
	for len(views) > 0 {
		var left []string
		var lastErr error
		for _, v := range views {
			rows, err := c.Query(ctx, "SHOW CREATE VIEW "+mysqlwire.Ident(from)+"."+mysqlwire.Ident(v))
			if err != nil {
				return err
			}
			create := strings.ReplaceAll(rows[0][1], mysqlwire.Ident(from)+".", mysqlwire.Ident(to)+".")
			if err := c.Exec(ctx, create); err != nil {
				if !mysqlwire.HasCode(err, mysqlwire.NoSuchTable) {
					return fmt.Errorf("creating the view %s: %w", v, err)
				}
				left, lastErr = append(left, v), fmt.Errorf("creating the view %s: %w", v, err)
			}
		}
		if len(left) == len(views) {
			return lastErr
		}
		views = left
	}
	return nil
}

func schemaQuery(db string) string {
	return "SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = " +
		mysqlwire.Literal(db)
}

func schemaExists(ctx context.Context, c *mysqlwire.Conn, db string) (bool, error) {
	rows, err := c.Query(ctx, schemaQuery(db))
	return len(rows) > 0, err
}

// isUnfinishedCopy reports whether db, which exists, is a copy cut short.
func isUnfinishedCopy(ctx context.Context, c *mysqlwire.Conn, db string) (bool, error) {
	rows, err := c.Query(ctx, "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = "+
		mysqlwire.Literal(db)+" AND TABLE_NAME = "+mysqlwire.Literal(copyMarker))
	return len(rows) > 0, err
}

// dropUnfinishedCopy drops db if it is a copy cut short.
func dropUnfinishedCopy(ctx context.Context, c *mysqlwire.Conn, db string) error {
	exists, err := schemaExists(ctx, c, db)
	if err != nil || !exists {
		return err
	}
	unfinished, err := isUnfinishedCopy(ctx, c, db)
	if err != nil || !unfinished {
		return err
	}
	return c.Exec(ctx, "DROP DATABASE "+mysqlwire.Ident(db))
}

// admin runs f on a connection to the server, with no default database.
func (m MySQL) admin(ctx context.Context, f func(*mysqlwire.Conn) error) error {
	c, err := mysqlwire.Connect(ctx, mysqlwire.Config{
		Addr: m.Server.Addr, User: m.Server.User, Password: m.Server.Password})
	if err != nil {
		return fmt.Errorf("connecting to MySQL at %s: %w", m.Server.Addr, err)
	}
	defer c.Close()
	return f(c)
}
