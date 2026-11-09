package database

import (
	"context"
	"errors"
	"slices"
)

// mySystemSchemas are the MySQL server's own databases, never Laneway's.
var mySystemSchemas = []string{"information_schema", "mysql", "performance_schema", "sys"}

// dropLockWait is how long, in seconds, a DROP DATABASE waits for a
// metadata lock before giving up, rather than MySQL's default of a year.
const dropLockWait = "30"

// unknownThread is MySQL's error for KILL of a connection that is gone.
const unknownThread = 1094

// Drop drops db. Like Postgres's DROP DATABASE … WITH (FORCE), it first
// closes the other connections using db: one in the middle of a transaction
// on its tables (a GUI client, say) would hold a metadata lock that makes
// DROP DATABASE wait. One that doesn't exist is fine.
func (m MySQL) Drop(ctx context.Context, db string) error {
	rows, err := m.mysql(ctx, "SELECT ID FROM information_schema.PROCESSLIST WHERE DB = "+
		myLiteral(db)+" AND ID <> CONNECTION_ID()")
	if err != nil {
		return err
	}
	for _, row := range rows {
		// It may have gone meanwhile.
		_, err := m.mysql(ctx, "KILL CONNECTION "+row[0])
		var merr *mysqlError
		if errors.As(err, &merr) && merr.Code == unknownThread {
			continue
		}
		if err != nil {
			return err
		}
	}
	_, err = m.mysql(ctx, "SET SESSION lock_wait_timeout = "+dropLockWait+"; DROP DATABASE IF EXISTS "+myIdent(db))
	return err
}

// Databases lists the server's databases, except its own system schemas.
func (m MySQL) Databases(ctx context.Context) ([]string, error) {
	rows, err := m.mysql(ctx, "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA ORDER BY SCHEMA_NAME")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, row := range rows {
		if !slices.Contains(mySystemSchemas, row[0]) {
			names = append(names, row[0])
		}
	}
	return names, nil
}
