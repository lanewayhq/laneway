package database

import (
	"context"
	"slices"

	"github.com/lanewayhq/laneway/internal/mysqlwire"
)

// mySystemSchemas are the MySQL server's own databases, never Laneway's.
var mySystemSchemas = []string{"information_schema", "mysql", "performance_schema", "sys"}

// dropLockWait is how long, in seconds, a DROP DATABASE waits for a
// metadata lock before giving up, rather than MySQL's default of a year.
const dropLockWait = "30"

// Drop drops db. Like Postgres's DROP DATABASE … WITH (FORCE), it first
// closes the other connections using db: one in the middle of a transaction
// on its tables (a GUI client, say) would hold a metadata lock that makes
// DROP DATABASE wait. One that doesn't exist is fine.
func (m MySQL) Drop(ctx context.Context, db string) error {
	return m.admin(ctx, func(c *mysqlwire.Conn) error {
		if err := c.Exec(ctx, "SET SESSION lock_wait_timeout = "+dropLockWait); err != nil {
			return err
		}
		rows, err := c.Query(ctx, "SELECT ID FROM information_schema.PROCESSLIST WHERE DB = "+
			mysqlwire.Literal(db)+" AND ID <> CONNECTION_ID()")
		if err != nil {
			return err
		}
		for _, row := range rows {
			// It may have gone meanwhile (1094, unknown thread).
			if err := c.Exec(ctx, "KILL CONNECTION "+row[0]); err != nil && !mysqlwire.HasCode(err, 1094) {
				return err
			}
		}
		return c.Exec(ctx, "DROP DATABASE IF EXISTS "+mysqlwire.Ident(db))
	})
}

// Databases lists the server's databases, except its own system schemas.
func (m MySQL) Databases(ctx context.Context) ([]string, error) {
	var names []string
	err := m.admin(ctx, func(c *mysqlwire.Conn) error {
		rows, err := c.Query(ctx, "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA ORDER BY SCHEMA_NAME")
		for _, row := range rows {
			if !slices.Contains(mySystemSchemas, row[0]) {
				names = append(names, row[0])
			}
		}
		return err
	})
	return names, err
}
