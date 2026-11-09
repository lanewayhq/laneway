package sdk

// Effect kinds, as a Plugin's manifest declares them under
// capabilities.effects. Laneway refuses, with a warning, an effect whose
// kind the manifest doesn't declare.
const (
	// EffectEnv sets keys in the Worktree's .env.
	EffectEnv = "env"
	// EffectExec runs a command in the Worktree.
	EffectExec = "exec"
	// EffectWarning shows the developer a warning.
	EffectWarning = "warning"
)

// Effect is something a Plugin asks Laneway to do, which Laneway does under
// its usual rules. Kind says which fields apply.
type Effect struct {
	// Kind is the effect's kind, such as EffectEnv.
	Kind string `json:"kind"`
	// Env are the keys an EffectEnv sets in the Worktree's .env. Keys the
	// Env Patch owns are refused: laneway.toml's env decides those.
	Env map[string]string `json:"env,omitempty"`
	// Command is what an EffectExec runs with /bin/sh -c in the Worktree,
	// with the environment its setup steps have (LANEWAY_DOMAIN, …).
	Command string `json:"command,omitempty"`
	// Message is an EffectWarning's warning, said for the developer.
	Message string `json:"message,omitempty"`
}

// SetEnv is an effect setting keys in the Worktree's .env.
func SetEnv(env map[string]string) Effect { return Effect{Kind: EffectEnv, Env: env} }

// Exec is an effect running command in the Worktree.
func Exec(command string) Effect { return Effect{Kind: EffectExec, Command: command} }

// Warn is an effect showing the developer a warning.
func Warn(message string) Effect { return Effect{Kind: EffectWarning, Message: message} }
