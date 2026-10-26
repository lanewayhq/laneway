package runtimes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Environment is what commands in one directory run with: the base
// environment with mise's environment for that directory applied, or the
// base environment alone when mise isn't on its PATH.
type Environment struct {
	// Dir is the directory it was resolved for.
	Dir string
	// Vars are the environment variables, "KEY=value".
	Vars []string
	// Mise is the mise executable that resolved it; "" if mise wasn't
	// found, or failed (see Warnings), so it is the base PATH's Runtimes.
	Mise string
	// Warnings are problems mise reported, such as Runtimes its config
	// declares that aren't installed.
	Warnings []Warning

	basePath string // PATH before mise, to tell what mise supplied
}

// miseTimeout bounds `mise env`, which may have to read many config files.
const miseTimeout = 30 * time.Second

// versionTimeout bounds asking a Runtime for its version.
const versionTimeout = 10 * time.Second

// Environ resolves the environment for commands run in dir, starting from
// base (normally the daemon's or the CLI's own environment). mise is looked
// up on base's PATH and asked for dir's environment with `mise env --json`.
// mise is told not to install anything: Portby never installs Runtimes.
func Environ(ctx context.Context, dir string, base []string) Environment {
	e := Environment{Dir: dir, Vars: slices.Clone(base), basePath: getenv(base, "PATH")}
	mise, err := lookPath("mise", e.basePath)
	if err != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, miseTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, mise, "env", "--json")
	cmd.Dir = dir
	cmd.Env = append(slices.Clone(base), "MISE_AUTO_INSTALL=false")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	vars := map[string]string{}
	if err == nil {
		err = json.Unmarshal(stdout.Bytes(), &vars)
	}
	if err != nil {
		e.Warnings = append(e.Warnings, Warning{Runtime: "mise",
			Message: fmt.Sprintf("`mise env` failed in %s, so Runtimes come from PATH: %s", dir, firstLine(stderr.String(), err)),
		})
		return e
	}
	e.Mise = mise
	e.Vars = setenv(e.Vars, vars)
	e.Warnings = append(e.Warnings, missingTools(dir, stderr.String())...)
	return e
}

// missingTools turns mise's "missing: php@8.4.1" warnings into Warnings.
func missingTools(dir, stderr string) []Warning {
	var warnings []Warning
	sc := bufio.NewScanner(strings.NewReader(stderr))
	for sc.Scan() {
		_, list, ok := strings.Cut(sc.Text(), "missing: ")
		if !ok {
			continue
		}
		for tool := range strings.FieldsSeq(list) {
			name, _, _ := strings.Cut(tool, "@")
			warnings = append(warnings, Warning{Runtime: name,
				Message: fmt.Sprintf("mise config for %s asks for %s, but it isn't installed", dir, tool),
				Fix:     "run `mise install` in " + dir + " (Portby never installs Runtimes)",
			})
		}
	}
	return warnings
}

func firstLine(stderr string, err error) string {
	for line := range strings.SplitSeq(stderr, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return err.Error()
}

// Getenv returns the value of key in e.Vars.
func (e Environment) Getenv(key string) string { return getenv(e.Vars, key) }

// LookPath finds the executable name on e's PATH.
func (e Environment) LookPath(name string) (string, error) {
	return lookPath(name, e.Getenv("PATH"))
}

// Find finds PHP and Node in e and asks each for its version (and PHP for
// its extensions). It always returns both, in that order; one that wasn't
// found has no Version.
func (e Environment) Find(ctx context.Context) []Runtime {
	return []Runtime{e.findPHP(ctx), e.findNode(ctx)}
}

// phpInfo prints PHP_VERSION, then one loaded extension per line.
const phpInfo = `echo PHP_VERSION, PHP_EOL, implode(PHP_EOL, get_loaded_extensions()), PHP_EOL;`

func (e Environment) findPHP(ctx context.Context) Runtime {
	r := Runtime{Name: PHP}
	out, ok := e.ask(ctx, &r, "php", "-r", phpInfo)
	if !ok {
		return r
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	r.Version = strings.TrimSpace(lines[0])
	for _, ext := range lines[1:] {
		if ext = strings.TrimSpace(ext); ext != "" {
			// composer.json's names: "Zend OPcache" is ext-zend-opcache.
			r.Extensions = append(r.Extensions, strings.ReplaceAll(strings.ToLower(ext), " ", "-"))
		}
	}
	return r
}

func (e Environment) findNode(ctx context.Context) Runtime {
	r := Runtime{Name: Node}
	if out, ok := e.ask(ctx, &r, "node", "--version"); ok {
		r.Version = strings.TrimPrefix(strings.TrimSpace(out), "v")
	}
	return r
}

// ask runs executable name with args in e and returns its output. It sets
// r.Path if the executable is found, and r.Error if running it fails.
func (e Environment) ask(ctx context.Context, r *Runtime, name string, args ...string) (string, bool) {
	path, err := e.LookPath(name)
	if err != nil {
		return "", false
	}
	r.Path = path
	r.Source = "PATH"
	if e.Mise != "" {
		if onBase, err := lookPath(name, e.basePath); err != nil || onBase != path {
			r.Source = "mise"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = e.Dir
	cmd.Env = e.Vars
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil && strings.TrimSpace(string(out)) == "" {
		err = errors.New("it printed no version")
	}
	if err != nil {
		r.Error = firstLine(stderr.String(), err)
		return "", false
	}
	return string(out), true
}

// lookPath finds an executable on path (a PATH value), like exec.LookPath
// but without reading this process's PATH. Names with a slash are used as
// they are.
func lookPath(name, path string) (string, error) {
	if strings.Contains(name, "/") {
		if isExecutable(name) {
			return name, nil
		}
		return "", fmt.Errorf("%s: %w", name, exec.ErrNotFound)
	}
	for dir := range strings.SplitSeq(path, string(os.PathListSeparator)) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if p := filepath.Join(dir, name); isExecutable(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: %w", name, exec.ErrNotFound)
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func getenv(vars []string, key string) string {
	for i := len(vars) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(vars[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// setenv returns vars with every key in set replaced or, if new, appended in
// key order.
func setenv(vars []string, set map[string]string) []string {
	out := make([]string, 0, len(vars)+len(set))
	for _, kv := range vars {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := set[k]; !ok {
			out = append(out, kv)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		out = append(out, k+"="+set[k])
	}
	return out
}
