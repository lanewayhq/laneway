package builtin

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

// instructionEffects add the agent instructions where the Project's agents
// read them: Laravel Boost's guidelines if the Project uses Boost, else
// AGENTS.md, imported into CLAUDE.md if importIntoClaude.
func instructionEffects(p sdk.Project, importIntoClaude bool) ([]sdk.Effect, error) {
	if usesBoost(p) {
		return boostEffects(p), nil
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
