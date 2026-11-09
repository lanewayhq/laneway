package plugin

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/lanewayhq/laneway/internal/fileblock"
)

// Effect is something a Plugin's hook, command or Generator asks Laneway to
// do (see sdk.Effect); Kind says which fields apply. Laneway applies it, under its
// usual rules, only if the Plugin's manifest declares its kind. To add a
// kind: add it to EffectKinds, its fields here, its check to check, and its
// case to the daemon's applyEffect (internal/daemon/effects.go).
type Effect struct {
	Kind string `json:"kind"`
	// Command is an exec effect's command and arguments, run without a
	// shell.
	Command []string `json:"command,omitempty"`
	// Message is a warning effect's warning.
	Message string `json:"message,omitempty"`
	// Env is an env effect's keys for the Worktree's .env.
	Env map[string]string `json:"env,omitempty"`
	// File is a file effect's block, merged into the Worktree's files by
	// fileblock.Apply.
	File *fileblock.Block `json:"file,omitempty"`
	// HTTP is an http effect's request, which Laneway sends (see
	// HTTPRequest).
	HTTP *HTTPRequest `json:"http,omitempty"`
}

// envKey is what an env file key may be, as in laneway.toml's env.
var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// check says what's wrong with an effect of a kind Laneway knows.
func (e Effect) check() error {
	switch e.Kind {
	case EffectEnv:
		if len(e.Env) == 0 {
			return errors.New("it sets no keys")
		}
		for _, k := range slices.Sorted(maps.Keys(e.Env)) {
			if !envKey.MatchString(k) {
				return fmt.Errorf("it sets %q, but an env key must be letters, digits and _ and not start with a digit", k)
			}
		}
	case EffectExec:
		if len(e.Command) == 0 || strings.TrimSpace(e.Command[0]) == "" {
			return errors.New("it has no command")
		}
	case EffectWarning:
		if strings.TrimSpace(e.Message) == "" {
			return errors.New("it has no message")
		}
	case EffectFile:
		if e.File == nil {
			return errors.New("it has no file")
		}
		return e.File.Validate()
	case EffectHTTP:
		return e.HTTP.check()
	}
	return nil
}

// CheckEffects keeps the effects p may return: of a kind its manifest
// declares, and well formed. Each one refused is a warning naming what
// returned it (source, such as "Plugin tasks") and why.
func (p Plugin) CheckEffects(source string, effects []Effect) (allowed []Effect, warnings []string) {
	for _, e := range effects {
		if _, known := EffectKinds[e.Kind]; !known {
			warnings = append(warnings, fmt.Sprintf("%s returned an effect of kind %q, which isn't an effect kind; it was ignored", source, e.Kind))
			continue
		}
		if !p.Declares(e.Kind) {
			warnings = append(warnings, fmt.Sprintf("%s returned %s effect, but its manifest doesn't declare it may %s, so Laneway refused it",
				source, Article(e.Kind), EffectKinds[e.Kind]))
			continue
		}
		err := e.check()
		if err == nil && e.Kind == EffectHTTP {
			err = p.checkHost(e.HTTP)
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s returned %s effect that Laneway refused: %v", source, Article(e.Kind), err))
			continue
		}
		allowed = append(allowed, e)
	}
	return allowed, warnings
}

// Article is the effect kind with "a" or "an" before it, as it's read
// aloud: "an exec", "an http".
func Article(kind string) string {
	if strings.ContainsRune("aeiou", rune(kind[0])) || kind == EffectHTTP {
		return "an " + kind
	}
	return "a " + kind
}
