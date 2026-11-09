// Package agents is what the built-in Harness Generators (claude, t3) and
// the boost Generator share: the agent instructions, where a Project's
// agents read them, and the command Harness hooks run. Each Generator is a
// WASM command of its own that imports it.
package agents

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lanewayhq/laneway/sdk"
)

const (
	agentsFile = "AGENTS.md"
	claudeFile = "CLAUDE.md"
)

// ReadyCommand is the shell command Harness hooks run: `lane worktree
// ready`, guarded so that it succeeds quietly when `lane` isn't installed
// or there is nothing to wait for (exit 3: not a Linked repository, or no
// daemon). It still fails when setup Failed (1) or waiting gave up (4).
// It runs under `sh` because T3 Code types its scripts into the developer's
// interactive shell, which may not be POSIX and where `exit` would close the
// terminal.
const ReadyCommand = `sh -c 'command -v lane >/dev/null 2>&1 || exit 0; lane worktree ready || [ $? -eq 3 ]'`

// ReadyTimeout is how long a Harness may let ReadyCommand run, in seconds.
// It is longer than `lane worktree ready`'s own 10 minute timeout, so that
// Laneway gives up first and says why.
const ReadyTimeout = 660

// agentsBlock is what every generator puts between the markers in AGENTS.md.
// It is static: the Worktree's own details come from `lane info --json`.
const agentsBlock = `## Local environment (Laneway)

This repository is served by Laneway when it's installed. Every git worktree gets its own HTTPS URL, database and ` + "`.env`" + `, so work only with this worktree's.

- Find this worktree's URL, its Processes and their states, its databases and its Service connection details with ` + "`lane info --json`" + `. Use that URL rather than starting your own server or guessing a port.
- Run the project's commands (tests, artisan, composer, npm…) with ` + "`lane exec <command>`" + `, such as ` + "`lane exec php artisan test`" + `, so they use this worktree's PHP and Node versions.
- ` + "`lane worktree ready`" + ` waits until this worktree is Ready (Laneway prepares each new worktree: its database, ` + "`.env`" + ` and setup commands). If it reports that preparing it failed, show the user its output; ` + "`lane worktree ready --retry`" + ` prepares it again once the problem is fixed.
- Name a linked worktree after your task: if ` + "`worktree.main`" + ` in ` + "`lane info --json`" + ` is false and its ` + "`aliases`" + ` is empty, then once you know your task and ` + "`lane worktree ready`" + ` has finished, run ` + "`lane worktree alias <name>`" + ` with a short kebab-case name for the task (lowercase letters, digits and ` + "`-`" + `, such as ` + "`checkout-redesign`" + `). If the name is taken, pick another. The Alias becomes this worktree's URL, so from then on use the ` + "`url`" + ` that ` + "`lane info --json`" + ` reports. Never add an Alias in the main worktree.
- If there is no ` + "`lane`" + ` command, Laneway isn't installed: ignore this section.
`

// claudeImport is the marked block added to CLAUDE.md: Claude Code reads
// CLAUDE.md, not AGENTS.md, and `@path` on its own line imports a file.
const claudeImport = "@" + agentsFile + "\n"

// importsAgents matches an existing import of AGENTS.md in CLAUDE.md.
var importsAgents = regexp.MustCompile(`(^|\s)@(\./)?AGENTS\.md(\s|$)`)

// InstructionEffects add the agent instructions where the Project's agents
// read them: Laravel Boost's guidelines if the Project uses Boost, else
// AGENTS.md, imported into CLAUDE.md if importIntoClaude.
func InstructionEffects(p sdk.Project, importIntoClaude bool) ([]sdk.Effect, error) {
	if usesBoost(p) {
		return BoostEffects(p), nil
	}
	effects := []sdk.Effect{sdk.File(sdk.Markdown(agentsFile, agentsBlock))}
	if importIntoClaude && needsClaudeImport(p) {
		effects = append(effects, sdk.File(sdk.Markdown(claudeFile, claudeImport)))
	}
	return effects, nil
}

// needsClaudeImport reports whether CLAUDE.md needs a marked import of
// AGENTS.md. It doesn't when there is no CLAUDE.md (Claude Code then reads
// AGENTS.md itself), when CLAUDE.md is AGENTS.md (a link), or when CLAUDE.md
// imports it outside a Laneway block. A CLAUDE.md with a Laneway block keeps
// it up to date.
func needsClaudeImport(p sdk.Project) bool {
	data, err := p.ReadFile(claudeFile)
	if err != nil {
		return false
	}
	if sameFile(p, claudeFile, agentsFile) {
		return false
	}
	return strings.Contains(string(data), "<!-- laneway:begin") || !importsAgents.Match(data)
}

// sameFile reports whether a and b in the Project are one file.
func sameFile(p sdk.Project, a, b string) bool {
	ia, err := os.Stat(filepath.Join(p.Dir, a))
	if err != nil {
		return false
	}
	ib, err := os.Stat(filepath.Join(p.Dir, b))
	return err == nil && os.SameFile(ia, ib)
}
