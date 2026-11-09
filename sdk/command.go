package sdk

// CallCommand runs one of a Plugin's commands, `lane <plugin> <command>
// [args...]`, in the current Worktree.
const CallCommand = "command"

// Effect kinds, the "kind" of an Effect. A Plugin may only return the kinds
// its manifest declares under capabilities.effects.
const (
	EffectEnv     = "env"
	EffectExec    = "exec"
	EffectFile    = "file"
	EffectWarning = "warning"
)

// Effect is something a Plugin's command, Generator (or hook) asks Laneway
// to do, which Laneway does under its usual rules. Build one with Exec,
// Warning, Env, File or HTTP.
type Effect struct {
	// Kind is what the effect does, such as EffectExec.
	Kind string `json:"kind"`
	// Command is an exec effect's command and its arguments, run in the
	// Worktree with its Runtimes, without a shell.
	Command []string `json:"command,omitempty"`
	// Message is a warning effect's warning, said for the developer.
	Message string `json:"message,omitempty"`
	// Env is an env effect's keys and values for the Worktree's .env. Keys
	// the Env Patch sets are left to it, with a warning.
	Env map[string]string `json:"env,omitempty"`
	// File is a file effect's block, merged into the Worktree's files.
	File *FileBlock `json:"file,omitempty"`
	// HTTP is an http effect's request, which Laneway sends.
	HTTP *HTTPRequest `json:"http,omitempty"`
}

// Exec is an effect that runs command with its arguments in the Worktree,
// such as Exec("php", "artisan", "migrate"). It needs the "exec" effect.
func Exec(command ...string) Effect {
	return Effect{Kind: EffectExec, Command: command}
}

// Warning is an effect that shows message to the developer. It needs the
// "warning" effect.
func Warning(message string) Effect {
	return Effect{Kind: EffectWarning, Message: message}
}

// Env is an effect that sets keys in the Worktree's env file. It needs the
// "env" effect.
func Env(keys map[string]string) Effect {
	return Effect{Kind: EffectEnv, Env: keys}
}

// CommandResult is what a command returns: the effects Laneway applies, in
// order.
type CommandResult struct {
	Effects []Effect `json:"effects"`
}

// Command answers a CallCommand: it gets the Worktree the developer ran it
// in, mounted read-only, and the arguments after the command's name, and
// returns the effects Laneway applies, in order.
type Command func(p Project, args []string) ([]Effect, error)
