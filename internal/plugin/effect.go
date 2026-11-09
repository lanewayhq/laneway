package plugin

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Effect is something a Plugin's hook or command asks Laneway to do (see
// sdk.Effect). Kind says which fields apply. The daemon applies them; to
// add a kind, add it to EffectKinds, its fields here, its check to check,
// and its applier to the daemon's (internal/daemon/effects.go).
type Effect struct {
	Kind string `json:"kind"`
	// Env are the keys an env effect sets in the Worktree's .env.
	Env map[string]string `json:"env,omitempty"`
	// Command is what an exec effect runs with /bin/sh -c in the
	// Worktree.
	Command string `json:"command,omitempty"`
	// Message is a warning effect's warning.
	Message string `json:"message,omitempty"`
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
		if strings.TrimSpace(e.Command) == "" {
			return errors.New("it has no command")
		}
	case EffectWarning:
		if strings.TrimSpace(e.Message) == "" {
			return errors.New("it has no message")
		}
	}
	return nil
}

// checkEffects keeps the effects p may return: of a kind its manifest
// declares, and well formed. Each one refused is a warning, naming what
// returned it (what) and what to do.
func (p Plugin) checkEffects(what string, effects []Effect) (allowed []Effect, warnings []string) {
	for _, e := range effects {
		switch _, known := EffectKinds[e.Kind]; {
		case !known:
			warnings = append(warnings, fmt.Sprintf("%s returned an effect of kind %q, which isn't an effect kind, so Laneway refused it", what, e.Kind))
		case !slices.Contains(p.Capabilities.Effects, e.Kind):
			warnings = append(warnings, fmt.Sprintf("%s returned an effect of kind %s, but the Plugin's manifest doesn't declare %q under "+
				"capabilities.effects, so Laneway refused it; the Plugin's author can declare it, and you'd be asked to confirm it when adding the Plugin again",
				what, e.Kind, e.Kind))
		default:
			if err := e.check(); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s returned an effect of kind %s that Laneway refused: %v", what, e.Kind, err))
				continue
			}
			allowed = append(allowed, e)
		}
	}
	return allowed, warnings
}
