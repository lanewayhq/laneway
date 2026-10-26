package envpatch

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mrsafalpiya/portby.dev/internal/service"
)

// Variables are the names a template can use as {name}. Every Worktree has a
// value for each. Later tickets add the database and Service variables
// (db, test_db, and each Service's host and port) here and in the daemon's
// envVars.
var Variables = append([]string{
	"project",  // the Project name
	"worktree", // git's name for the Worktree; empty for the main Worktree
	"domain",   // the Worktree's Domain, such as feat-x.shop.localhost
	"url",      // the Worktree's URL, such as https://feat-x.shop.localhost
},
	// Each Service's host, port and credentials, such as {redis_port},
	// which only have values in Worktrees whose config uses the Service.
	service.AllVariables()...)

// placeholder matches what a template treats specially: an escaped brace,
// or {name}. Any other brace is literal.
var placeholder = regexp.MustCompile(`\{\{|\}\}|\{([a-z][a-z0-9_]*)\}`)

// CheckTemplate reports whether template only uses known Variables.
func CheckTemplate(template string) error {
	_, err := expand(template, nil)
	return err
}

// Render replaces each {name} in template with vars[name], {{ with { and
// }} with }. A variable that isn't one of Variables, or has no value in
// vars, is an error.
func Render(template string, vars map[string]string) (string, error) {
	return expand(template, func(name string) (string, error) {
		v, ok := vars[name]
		if !ok {
			return "", fmt.Errorf("template variable {%s} has no value for this Worktree", name)
		}
		return v, nil
	})
}

// expand is Render with value looking up variables; with value nil it only
// checks the template.
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
			if !slices.Contains(Variables, name) {
				return "", fmt.Errorf("unknown template variable {%s}; the variables are %s (write {{ for a literal {)",
					name, variableList())
			}
			if value != nil {
				v, err := value(name)
				if err != nil {
					return "", err
				}
				b.WriteString(v)
			}
		}
	}
	b.WriteString(template[last:])
	return b.String(), nil
}

func variableList() string {
	names := make([]string, len(Variables))
	for i, v := range Variables {
		names[i] = "{" + v + "}"
	}
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
