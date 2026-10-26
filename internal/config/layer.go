package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/mrsafalpiya/portby.dev/internal/envpatch"
)

// LocalFileName is the gitignored per-developer config file. It overrides
// portby.toml.
const LocalFileName = "portby.local.toml"

// Layer is one source of config: a Recipe's defaults, portby.toml or
// portby.local.toml. A nil pointer or a missing map entry means the layer
// doesn't set that key, so an earlier layer's value stands. Layers are merged
// by the Recipe Resolver, never by decoding several files into one value.
type Layer struct {
	// Source names where the layer came from, for error messages: the
	// file's path, or the Recipe.
	Source string `toml:"-"`
	// Warnings are problems that don't stop the layer loading, such as
	// unknown keys, each prefixed with Source.
	Warnings []string `toml:"-"`

	// Name is the Project name, overriding the directory name.
	Name *string `toml:"name"`
	// Processes are keyed by Process name ([processes.<name>] tables).
	Processes map[string]ProcessLayer `toml:"processes"`
	// StartTimeout is how long the Primary Process gets to accept
	// connections when its Worktree starts, such as "90s".
	StartTimeout *time.Duration `toml:"start_timeout"`
	// Env is the Env Patch: the keys Portby owns in each Worktree's .env,
	// keyed by env key ([env] table). Values are templates such as
	// "{url}"; see envpatch.Variables.
	Env map[string]string `toml:"env"`
}

// ProcessLayer is one [processes.<name>] table.
type ProcessLayer struct {
	// Command is run with `sh -c` in the Worktree. Every `{port}` in it is
	// replaced with the port Portby picked for the Process, which is also
	// in $PORT.
	Command *string `toml:"command"`
	// Primary marks the Process that serves the Worktree's Domain. A Project
	// with a single Process doesn't need to set it.
	Primary *bool `toml:"primary"`
	// Disabled turns off a Process an earlier layer declared, such as one
	// from the Recipe. A later layer can set it back to false.
	Disabled *bool `toml:"disabled"`
}

// LoadLayers reads dir's portby.toml and then portby.local.toml, in the
// order they apply. Missing files are skipped.
func LoadLayers(dir string) ([]Layer, error) {
	var layers []Layer
	for _, name := range []string{FileName, LocalFileName} {
		l, err := LoadLayer(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		layers = append(layers, l)
	}
	return layers, nil
}

// LoadLayer reads one config file. A missing file gives an error matching
// fs.ErrNotExist.
func LoadLayer(path string) (Layer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Layer{}, err
	}
	return ParseLayer(path, data)
}

// ParseLayer decodes and validates TOML config. source names it in errors:
// a file's path, or a Recipe for its defaults. Errors name the source and
// the key at fault.
func ParseLayer(source string, data []byte) (Layer, error) {
	l := Layer{Source: source}
	md, err := toml.Decode(string(data), &l)
	if err != nil {
		return Layer{}, decodeError(source, err)
	}
	// The decoder silently skips a non-table value for a map field.
	for _, key := range append([]toml.Key{{"processes"}, {"env"}}, processKeys(l)...) {
		if t := md.Type(key...); t != "" && t != "Hash" {
			return Layer{}, fmt.Errorf("%s: %s must be a table, not a %s", source, key, strings.ToLower(t))
		}
	}
	if err := l.validate(); err != nil {
		return Layer{}, fmt.Errorf("%s: %w", source, err)
	}
	// Unknown keys only warn, so a config written for a newer Portby, or
	// with a typo, still loads. Only the outermost unknown key is reported.
	unknown := map[string]bool{}
	for _, key := range md.Undecoded() {
		unknown[key.String()] = true
		if len(key) > 1 && unknown[key[:len(key)-1].String()] {
			continue
		}
		l.Warnings = append(l.Warnings, fmt.Sprintf("%s: unknown key %q", source, key.String()))
	}
	return l, nil
}

func processKeys(l Layer) []toml.Key {
	var keys []toml.Key
	for name := range l.Processes {
		keys = append(keys, toml.Key{"processes", name})
	}
	return keys
}

// validate checks what can be checked within one layer. Rules that need
// the merged config, such as every Process having a command, belong to the
// Recipe Resolver.
func (l Layer) validate() error {
	if l.Name != nil && strings.TrimSpace(*l.Name) == "" {
		return errors.New("name is empty")
	}
	if l.StartTimeout != nil && *l.StartTimeout <= 0 {
		return fmt.Errorf("start_timeout must be positive, such as \"90s\", not %s", *l.StartTimeout)
	}
	for _, k := range slices.Sorted(maps.Keys(l.Env)) {
		if !envKey.MatchString(k) {
			return fmt.Errorf("env.%s: an env key must be letters, digits and _ and not start with a digit", key(k))
		}
		if err := envpatch.CheckTemplate(l.Env[k]); err != nil {
			return fmt.Errorf("env.%s: %w", key(k), err)
		}
	}
	var primaries []string
	for _, name := range slices.Sorted(maps.Keys(l.Processes)) {
		p := l.Processes[name]
		if p.Command != nil && strings.TrimSpace(*p.Command) == "" {
			return fmt.Errorf("processes.%s.command is empty", name)
		}
		if p.Primary != nil && *p.Primary {
			primaries = append(primaries, "processes."+name+".primary")
		}
	}
	if len(primaries) > 1 {
		return fmt.Errorf("only one Process can be the Primary Process, but %s are all true",
			strings.Join(primaries, " and "))
	}
	return nil
}

// envKey is what phpdotenv accepts as a key.
var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// typeError matches the decoder's errors for a value of the wrong type.
var typeError = regexp.MustCompile(`^toml: line (\d+) \(last key "([^"]+)"\): (.*)$`)

// decodeError rewrites a TOML error as "<source>: line N: <key>: <problem>".
func decodeError(source string, err error) error {
	var pe toml.ParseError
	if errors.As(err, &pe) && pe.LastKey != "" {
		return fmt.Errorf("%s: line %d: %s: %s", source, pe.Position.Line, pe.LastKey, pe.Message)
	}
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: line %d: %s", source, pe.Position.Line, pe.Message)
	}
	if m := typeError.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Errorf("%s: line %s: %s: %s", source, m[1], m[2], m[3])
	}
	return fmt.Errorf("%s: %w", source, err)
}
