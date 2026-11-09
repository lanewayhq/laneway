package database

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/lanewayhq/laneway/internal/container"
	"github.com/lanewayhq/laneway/internal/engine"
	"github.com/lanewayhq/laneway/internal/naming"
)

// Server is how to reach the Service an Engine's databases live in.
type Server struct {
	// Exec runs a command inside the Service's container and returns what
	// it printed and how it exited.
	Exec func(ctx context.Context, cmd []string) (container.Output, error)
}

// Commands is the database Engine of a Service's Engine (such as Postgres or
// MySQL): its databases are names in the Service, made, copied, dropped and
// listed by the commands in the Engine's manifest (engine.Databases), run in
// the Service's container at Server. Database names don't need the Server,
// so a Commands with the zero Server names them.
type Commands struct {
	Engine *engine.Manifest
	Server Server
}

// Database is <project> for the main Worktree and <project>__<worktree> for
// others, made safe for the Engine (see naming.DatabaseName).
func (c Commands) Database(project, worktree, _ string) string {
	return naming.DatabaseName(project, worktree, c.Engine.Database.MaxName)
}

// TestDatabase is the Worktree's database name with "_test", the database
// name cut short first so that the whole fits in the Engine's limit (see
// naming.TestDatabaseName).
func (c Commands) TestDatabase(project, worktree, _ string) string {
	return naming.TestDatabaseName(project, worktree, c.Engine.Database.MaxName)
}

func (c Commands) Exists(ctx context.Context, db string) (bool, error) {
	out, err := c.run(ctx, "exists", c.Engine.Database.Exists, "db", db)
	return strings.TrimSpace(out) != "", err
}

// Create creates db, empty. If ctx ends while the command runs, Laneway
// stops waiting but the command runs on in the container.
func (c Commands) Create(ctx context.Context, db string) error {
	_, err := c.run(ctx, "create", c.Engine.Database.Create, "db", db)
	return err
}

// Clone creates to as a copy of from. If ctx ends while the command runs,
// Laneway stops waiting but the command runs on in the container, which
// leaves to complete or not there (engine.Databases).
func (c Commands) Clone(ctx context.Context, from, to string) error {
	_, err := c.run(ctx, "clone", c.Engine.Database.Clone, "from", from, "to", to)
	return err
}

// Drop drops db, closing any connections to it first. One that doesn't exist
// is fine.
func (c Commands) Drop(ctx context.Context, db string) error {
	_, err := c.run(ctx, "drop", c.Engine.Database.Drop, "db", db)
	return err
}

// Databases lists every database in the Service.
func (c Commands) Databases(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, "list", c.Engine.Database.List)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// safeName is what a database name Laneway made looks like (package naming),
// which the manifest's commands put into SQL and shell scripts as it is.
var safeName = regexp.MustCompile(`^[a-z0-9_]+$`)

// run runs the database command called op with the placeholders in names
// (pairs of placeholder and database name) filled in, and returns its
// stdout.
func (c Commands) run(ctx context.Context, op string, cmd []string, names ...string) (string, error) {
	if c.Server.Exec == nil {
		return "", fmt.Errorf("the %s Service isn't running", c.Engine.Name)
	}
	values := map[string]string{}
	for i := 0; i+1 < len(names); i += 2 {
		if !safeName.MatchString(names[i+1]) {
			return "", fmt.Errorf("%q isn't a database name Laneway makes: only lowercase letters, digits and _", names[i+1])
		}
		values[names[i]] = names[i+1]
	}
	out, err := c.Server.Exec(ctx, c.Engine.Expand(cmd, values))
	if err != nil {
		return "", fmt.Errorf("running %s's %s command in its Service: %w", c.Engine.Name, op, err)
	}
	if out.ExitCode != 0 {
		return "", &CommandError{Engine: c.Engine.Name, Op: op, ExitCode: out.ExitCode,
			Message: strings.TrimSpace(string(out.Stderr))}
	}
	return string(out.Stdout), nil
}

// CommandError is a database command that ran and failed.
type CommandError struct {
	// Engine and Op name the command, such as postgres's clone.
	Engine, Op string
	ExitCode   int
	// Message is what it printed on stderr.
	Message string
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%s's %s command exited with status %d: %s", e.Engine, e.Op, e.ExitCode, e.Message)
}

var _ interface {
	TestEngine
	Dropper
} = Commands{}
