package database

import "github.com/lanewayhq/laneway/internal/engine"

// New is the database Engine called name, as config names it ([database]
// engine), or nil if Laneway has none by that name: SQLite, or the Engine of
// a Service whose manifest has database commands (Commands). path is the
// database file relative to the Worktree (only SQLite uses it); server is
// how to reach the Service the databases live in (the zero Server will do
// for naming them).
func New(name, path string, server Server) Engine {
	if name == "sqlite" {
		return SQLite{Path: path}
	}
	if e, ok := engine.Lookup(name); ok && e.Database != nil {
		return Commands{Engine: e, Server: server}
	}
	return nil
}

// EngineNames lists the database Engines' names, for config and messages.
func EngineNames() []string {
	names := []string{"sqlite"}
	for _, e := range engine.All() {
		if e.Database != nil {
			names = append(names, e.Name)
		}
	}
	return names
}
