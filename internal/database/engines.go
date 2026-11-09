package database

import "github.com/lanewayhq/laneway/internal/engine"

// New is the database Engine called name, as config names it ([database]
// engine), or nil if Laneway has none by that name: SQLite, or the Engine in
// engines of a Service whose manifest has database commands (Commands). path
// is the database file relative to the Worktree (only SQLite uses it);
// server is how to reach the Service the databases live in (the zero Server
// will do for naming them).
func New(engines engine.Set, name, path string, server Server) Engine {
	if name == "sqlite" {
		return SQLite{Path: path}
	}
	if e, ok := engines.Lookup(name); ok && e.Database != nil {
		return Commands{Engine: e, Server: server}
	}
	return nil
}

// EngineNames lists the names of the database Engines among engines, and
// SQLite's, for config and messages.
func EngineNames(engines engine.Set) []string {
	names := []string{"sqlite"}
	for _, e := range engines {
		if e.Database != nil {
			names = append(names, e.Name)
		}
	}
	return names
}
