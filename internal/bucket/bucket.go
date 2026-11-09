// Package bucket gives each Worktree its own Buckets in the object storage
// Services its Project uses (ADR-0008). A Bucket is a name in a Service,
// made, dropped and listed by the commands in the Service's Engine manifest
// (engine.Buckets), run in the Service's container. Bucket names come from
// package naming (naming.BucketName).
package bucket

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/lanewayhq/laneway/internal/container"
	"github.com/lanewayhq/laneway/internal/engine"
)

// Commands are the Bucket commands of a Service's Engine, run in the
// Service's container by Exec.
type Commands struct {
	Engine *engine.Manifest
	// Exec runs a command inside the Service's container and returns what
	// it printed and how it exited.
	Exec func(ctx context.Context, cmd []string) (container.Output, error)
}

// Exists reports whether the Bucket name exists.
func (c Commands) Exists(ctx context.Context, name string) (bool, error) {
	out, err := c.run(ctx, "exists", c.Engine.Bucket.Exists, "bucket", name)
	return strings.TrimSpace(out) != "", err
}

// Create creates the Bucket name, empty and private. If ctx ends while the
// command runs, Laneway stops waiting but the command runs on in the
// container.
func (c Commands) Create(ctx context.Context, name string) error {
	_, err := c.run(ctx, "create", c.Engine.Bucket.Create, "bucket", name)
	return err
}

// Drop drops the Bucket name with everything in it. One that doesn't exist
// is fine.
func (c Commands) Drop(ctx context.Context, name string) error {
	_, err := c.run(ctx, "drop", c.Engine.Bucket.Drop, "bucket", name)
	return err
}

// List lists every Bucket in the Service.
func (c Commands) List(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, "list", c.Engine.Bucket.List)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// safeName is what a Bucket name Laneway made looks like (see
// naming.BucketName), which the manifest's commands put into URLs and shell
// scripts as it is.
var safeName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// run runs the Bucket command called op with the placeholders in names
// (pairs of placeholder and Bucket name) filled in, and returns its stdout.
func (c Commands) run(ctx context.Context, op string, cmd []string, names ...string) (string, error) {
	if c.Exec == nil {
		return "", fmt.Errorf("the %s Service isn't running", c.Engine.Name)
	}
	values := map[string]string{}
	for i := 0; i+1 < len(names); i += 2 {
		if !safeName.MatchString(names[i+1]) || strings.HasPrefix(names[i+1], "xn--") {
			return "", fmt.Errorf("%q isn't a Bucket name Laneway makes: only lowercase letters, digits and -, 3 to 63 of them", names[i+1])
		}
		values[names[i]] = names[i+1]
	}
	out, err := c.Exec(ctx, c.Engine.Expand(cmd, values))
	if err != nil {
		return "", fmt.Errorf("running %s's %s command in its Service: %w", c.Engine.Name, op, err)
	}
	if out.ExitCode != 0 {
		return "", &CommandError{Engine: c.Engine.Name, Op: op, ExitCode: out.ExitCode,
			Message: strings.TrimSpace(string(out.Stderr))}
	}
	return string(out.Stdout), nil
}

// CommandError is a Bucket command that ran and failed.
type CommandError struct {
	// Engine and Op name the command, such as rustfs's create.
	Engine, Op string
	ExitCode   int
	// Message is what it printed on stderr.
	Message string
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%s's %s command exited with status %d: %s", e.Engine, e.Op, e.ExitCode, e.Message)
}

// Ensure makes sure the Bucket name exists, creating it empty if it
// doesn't. A Bucket that exists is never touched, so running Ensure again
// is safe. It says what it did on out.
func Ensure(ctx context.Context, c Commands, name string, out io.Writer) error {
	exists, err := c.Exists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		fmt.Fprintf(out, "the Bucket %s exists; leaving it as it is\n", name)
		return nil
	}
	if err := c.Create(ctx, name); err != nil {
		return fmt.Errorf("creating the Bucket %s: %w", name, err)
	}
	fmt.Fprintf(out, "created the empty Bucket %s\n", name)
	return nil
}
