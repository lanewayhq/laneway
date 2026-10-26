package database

// Engines are the database engines Portby knows, by the name config gives
// them ([database] engine). Adding an engine is a row here; one whose
// databases live in a Service also needs that Service's engine (package
// service) under the same name.
var Engines = []struct {
	Name string
	// New makes the Engine. path is the database file relative to the
	// Worktree (only SQLite uses it); server is how to reach the Service
	// the databases live in (the zero Server will do for naming them).
	New func(path string, server Server) Engine
}{
	{"sqlite", func(path string, _ Server) Engine { return SQLite{Path: path} }},
	{"postgres", func(_ string, server Server) Engine { return Postgres{Server: server} }},
	{"mysql", func(_ string, server Server) Engine { return MySQL{Server: server} }},
}

// New is the Engine called name, or nil if Portby has none by that name.
func New(name, path string, server Server) Engine {
	for _, e := range Engines {
		if e.Name == name {
			return e.New(path, server)
		}
	}
	return nil
}

// EngineNames lists the Engines' names, for config and messages.
func EngineNames() []string {
	var names []string
	for _, e := range Engines {
		names = append(names, e.Name)
	}
	return names
}
