// Package runtimes is the Runtime Resolver (ADR-0002). It resolves the
// Runtimes a Worktree's commands run with through mise, falling back to PATH,
// and checks them against what the Project's own files declare. It never
// installs a Runtime.
package runtimes

// The Runtimes Portby knows about.
const (
	PHP  = "php"
	Node = "node"
)

// Runtime is one Runtime as resolved for a directory.
type Runtime struct {
	// Name is PHP or Node.
	Name string
	// Version is its version, such as "8.4.12"; "" if it wasn't found.
	Version string
	// Path is its executable; "" if it wasn't found.
	Path string
	// Source is "mise" if mise put it on PATH, else "PATH".
	Source string
	// Extensions are the loaded PHP extensions, named as composer.json
	// names them without "ext-" (lowercase, spaces as dashes).
	Extensions []string
	// Error says why its version couldn't be read, if Path is set but
	// Version isn't.
	Error string
}

// Warning is a Runtime problem: a mismatch with the Project's declared
// constraints, or a Runtime that is missing.
type Warning struct {
	// Runtime is the Runtime it is about, or "mise".
	Runtime string
	Message string
	// Fix says what to do about it; it may be empty.
	Fix string
}

func (w Warning) String() string {
	if w.Fix == "" {
		return w.Message
	}
	return w.Message + "; " + w.Fix
}
