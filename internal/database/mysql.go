package database

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lanewayhq/laneway/internal/naming"
)

// MySQLMaxName is the longest a MySQL database name may be.
const MySQLMaxName = 64

// copyMarker is a table Clone puts in the database it is building and drops
// last. MySQL can't create a database and its contents all at once, so a
// database that still has it is a copy cut short (Laneway was killed half
// way): Exists says it doesn't exist, and Create and Clone drop it first.
const copyMarker = "laneway_copy_in_progress"

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
	exists, err := m.schemaExists(ctx, db)
	if err != nil || !exists {
		return false, err
	}
	unfinished, err := m.isUnfinishedCopy(ctx, db)
	return !unfinished, err
}

// Create creates db, empty.
func (m MySQL) Create(ctx context.Context, db string) error {
	if err := m.dropUnfinishedCopy(ctx, db); err != nil {
		return err
	}
	_, err := m.mysql(ctx, "CREATE DATABASE "+myIdent(db))
	return err
}

// dumpAndRestore is the shell script Clone runs in the Service's container
// with the user, from and to as $1, $2 and $3: mysqldump's dump of from,
// read as one consistent snapshot without locking its tables (so the main
// Worktree's app, queue worker or GUI client carry on), piped straight into
// mysql restoring it into to, so the dump never leaves the container. It
// carries what mysqldump --routines does: tables with their rows, routines,
// views and triggers. Events aren't copied: they would run on the copy too.
// pipefail makes a failed dump fail the script, not only a failed restore.
// The password is in MYSQL_PWD.
const dumpAndRestore = `set -o pipefail; ` +
	`mysqldump --user="$1" --single-transaction --routines --triggers --skip-events --no-tablespaces --set-gtid-purged=OFF "$2" | ` +
	`mysql --user="$1" --database="$3"`

// Clone creates to as a copy of from with mysqldump and mysql, run inside
// the Service's container (see dumpAndRestore). A write landing during the
// copy may be in some tables and not others. If the copy fails or ctx ends,
// to is dropped.
func (m MySQL) Clone(ctx context.Context, from, to string) error {
	if err := m.dropUnfinishedCopy(ctx, to); err != nil {
		return err
	}
	rows, err := m.mysql(ctx, schemaQuery(from))
	if err != nil {
		return err
	}
	if len(rows) == 0 || len(rows[0]) < 2 {
		return fmt.Errorf("the database %s doesn't exist", from)
	}
	if _, err := m.mysql(ctx, "CREATE DATABASE "+myIdent(to)+
		" CHARACTER SET "+rows[0][0]+" COLLATE "+rows[0][1]); err != nil {
		return err
	}
	err = m.copyInto(ctx, from, to)
	if err != nil {
		// ctx may be over: drop it all the same. A restore still
		// running in the container then fails on the missing database.
		dropCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, dropErr := m.mysql(dropCtx, "DROP DATABASE IF EXISTS "+myIdent(to)); dropErr != nil {
			return fmt.Errorf("%w (and dropping the unfinished copy failed: %v)", err, dropErr)
		}
	}
	return err
}

// copyInto copies from into to, which was just created, marking it
// unfinished (copyMarker) until it is done.
func (m MySQL) copyInto(ctx context.Context, from, to string) error {
	if _, err := m.mysql(ctx, "CREATE TABLE "+myIdent(to)+"."+copyMarker+" (id int)"); err != nil {
		return err
	}
	if _, err := m.run(ctx, "mysqldump", "sh", "-c", dumpAndRestore, "sh", m.Server.User, from, to); err != nil {
		return err
	}
	_, err := m.mysql(ctx, "DROP TABLE "+myIdent(to)+"."+copyMarker)
	return err
}

func schemaQuery(db string) string {
	return "SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = " +
		myLiteral(db)
}

func (m MySQL) schemaExists(ctx context.Context, db string) (bool, error) {
	rows, err := m.mysql(ctx, schemaQuery(db))
	return len(rows) > 0, err
}

// isUnfinishedCopy reports whether db, which exists, is a copy cut short.
func (m MySQL) isUnfinishedCopy(ctx context.Context, db string) (bool, error) {
	rows, err := m.mysql(ctx, "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = "+
		myLiteral(db)+" AND TABLE_NAME = "+myLiteral(copyMarker))
	return len(rows) > 0, err
}

// dropUnfinishedCopy drops db if it is a copy cut short.
func (m MySQL) dropUnfinishedCopy(ctx context.Context, db string) error {
	exists, err := m.schemaExists(ctx, db)
	if err != nil || !exists {
		return err
	}
	unfinished, err := m.isUnfinishedCopy(ctx, db)
	if err != nil || !unfinished {
		return err
	}
	_, err = m.mysql(ctx, "DROP DATABASE "+myIdent(db))
	return err
}

// mysqlError is a statement the mysql client ran and MySQL refused, or a
// client that couldn't log in or run.
type mysqlError struct {
	// Client is the program that failed: mysql or mysqldump.
	Client string
	// Code is MySQL's error number, such as 1094; 0 if the client didn't
	// print one.
	Code     int
	ExitCode int
	// Message is what the client printed on stderr.
	Message string
}

func (e *mysqlError) Error() string {
	return fmt.Sprintf("%s exited with status %d: %s", e.Client, e.ExitCode, e.Message)
}

// mysqlCode finds MySQL's error number in what mysql printed
// ("ERROR 1094 (HY000) at line 1: …") or mysqldump did ("mysqldump: Got
// error: 1049: …").
var mysqlCode = regexp.MustCompile(`(?m)(?:^ERROR |Got error: )(\d+)`)

// mysql runs statements (separated by ";") in one session of the mysql
// client inside the Service's container, with no default database, and
// returns the rows they gave: their values, as --batch prints them. The
// statements are an argument, not shell input, so they need no quoting
// beyond SQL's.
func (m MySQL) mysql(ctx context.Context, sql string) ([][]string, error) {
	out, err := m.run(ctx, "mysql", "mysql", "--user="+m.Server.User, "--batch", "--skip-column-names", "--execute="+sql)
	if err != nil {
		return nil, err
	}
	var rows [][]string
	for line := range strings.Lines(out) {
		rows = append(rows, strings.Split(strings.TrimSuffix(line, "\n"), "\t"))
	}
	return rows, nil
}

// run runs cmd inside the Service's container, logged in as the Server's
// User with its Password (in MYSQL_PWD, so the clients don't warn about a
// password on their command line), and returns its stdout. client names it
// in errors.
func (m MySQL) run(ctx context.Context, client string, cmd ...string) (string, error) {
	if m.Server.Exec == nil {
		return "", errors.New("the MySQL Service isn't running")
	}
	out, err := m.Server.Exec(ctx, append([]string{"env", "MYSQL_PWD=" + m.Server.Password}, cmd...))
	if err != nil {
		return "", fmt.Errorf("running %s in the MySQL Service: %w", client, err)
	}
	if out.ExitCode != 0 {
		merr := &mysqlError{Client: client, ExitCode: out.ExitCode, Message: strings.TrimSpace(string(out.Stderr))}
		if c := mysqlCode.FindStringSubmatch(merr.Message); c != nil {
			merr.Code, _ = strconv.Atoi(c[1])
		}
		return "", merr
	}
	return string(out.Stdout), nil
}

// myIdent quotes name as an identifier: `name`, with ` doubled.
func myIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// myLiteral quotes s as a string literal: 's', with ' doubled and \
// escaped (MySQL treats \ as an escape in literals by default).
func myLiteral(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", "''").Replace(s) + "'"
}
