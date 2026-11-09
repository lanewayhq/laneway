package envpatch

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Variables are the names a template can use as {name}, besides the Service
// variables of the Engines Laneway can run (engine.Set.Variables, such as
// {redis_port}), which only have values in Worktrees whose config uses the
// Service. Every Worktree has a value for each, except {db}, which only
// Worktrees of a Project with a database have, {db_host} and the other
// database connection variables, which only a database in a Service
// (Postgres) has, and {test_db}, which only Worktrees with a Test Database
// have.
var Variables = []string{
	"project",  // the Project name
	"worktree", // git's name for the Worktree; empty for the main Worktree
	"domain",   // the Worktree's Primary Domain, such as feat-x.shop.localhost or, with the Alias checkout, checkout.shop.localhost
	"url",      // the Worktree's URL (its Primary Domain's) without a trailing slash, such as https://feat-x.shop.localhost
	"db",       // the Worktree's database: for SQLite, the file's absolute path; else its name
	// Where the database is, for a database in a Service: its Service's
	// host, port and credentials.
	"db_host", "db_port", "db_username", "db_password",
	"test_db", // the Worktree's Test Database's name, when Test Databases are on
}

// placeholder matches what a template treats specially: an escaped brace,
// or {name}. Any other brace is literal.
var placeholder = regexp.MustCompile(`\{\{|\}\}|\{([a-z][a-z0-9_]*)\}`)

// CheckTemplate reports whether template only uses Variables and
// serviceVars, the Service variables of the Engines Laneway can run.
func CheckTemplate(template string, serviceVars []string) error {
	_, err := expand(template, func(name string) (string, error) {
		if !slices.Contains(Variables, name) && !slices.Contains(serviceVars, name) {
			return "", fmt.Errorf("unknown template variable {%s}; the variables are %s (write {{ for a literal {)",
				name, variableList(append(slices.Clone(Variables), serviceVars...)))
		}
		return "", nil
	})
	return err
}

// Uses reports whether template uses the variable name, as {name}.
func Uses(template, name string) bool {
	used := false
	expand(template, func(n string) (string, error) {
		used = used || n == name
		return "", nil
	})
	return used
}

// Render replaces each {name} in template with vars[name], {{ with { and
// }} with }. A variable with no value in vars is an error; CheckTemplate has
// already refused unknown ones.
func Render(template string, vars map[string]string) (string, error) {
	return expand(template, func(name string) (string, error) {
		v, ok := vars[name]
		if !ok {
			return "", fmt.Errorf("template variable {%s} has no value for this Worktree", name)
		}
		return v, nil
	})
}

// expand replaces each variable in template with what value says.
func expand(template string, value func(string) (string, error)) (string, error) {
	var b strings.Builder
	last := 0
	for _, m := range placeholder.FindAllStringSubmatchIndex(template, -1) {
		b.WriteString(template[last:m[0]])
		last = m[1]
		switch tok := template[m[0]:m[1]]; tok {
		case "{{":
			b.WriteByte('{')
		case "}}":
			b.WriteByte('}')
		default:
			name := template[m[2]:m[3]]
			v, err := value(name)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
		}
	}
	b.WriteString(template[last:])
	return b.String(), nil
}

func variableList(vars []string) string {
	names := make([]string, len(vars))
	for i, v := range vars {
		names[i] = "{" + v + "}"
	}
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
