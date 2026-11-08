package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// instructionEdits add the agent instructions where the Project's agents
// read them: Laravel Boost's guidelines if the Project uses Boost, else
// AGENTS.md, imported into CLAUDE.md if importIntoClaude.
func instructionEdits(root string, importIntoClaude bool) ([]edit, error) {
	boost, err := usesBoost(root)
	if err != nil {
		return nil, err
	}
	if boost {
		return boostEdits(root)
	}
	agents, err := agentsEdit(root)
	if err != nil {
		return nil, err
	}
	edits := []edit{agents}
	if !importIntoClaude {
		return edits, nil
	}
	imp, ok, err := claudeImportEdit(root)
	if err != nil {
		return nil, err
	}
	if ok {
		edits = append(edits, imp)
	}
	return edits, nil
}

// agentsEdit adds Laneway's block to AGENTS.md, creating the file if needed.
func agentsEdit(root string) (edit, error) {
	return markdownEdit(root, agentsFile, agentsBlock)
}

// claudeImportEdit adds a marked import of AGENTS.md to CLAUDE.md. It returns
// false when there is nothing to do: no CLAUDE.md (Claude Code then reads
// AGENTS.md itself), CLAUDE.md is AGENTS.md (a link), or CLAUDE.md already
// imports it.
func claudeImportEdit(root string) (edit, bool, error) {
	old, err := readFile(root, claudeFile)
	if err != nil || old == nil {
		return edit{}, false, err
	}
	if sameFile(filepath.Join(root, claudeFile), filepath.Join(root, agentsFile)) {
		return edit{}, false, nil
	}
	if begin, _, err := findBlock(string(old)); err == nil && begin < 0 && importsAgents.Match(old) {
		return edit{}, false, nil
	}
	e, err := markdownEdit(root, claudeFile, claudeImport)
	return e, true, err
}

// markdownEdit merges body into the marked block of the file rel.
func markdownEdit(root, rel, body string) (edit, error) {
	old, err := readFile(root, rel)
	if err != nil {
		return edit{}, err
	}
	doc, err := mergeBlock(string(old), body)
	if err != nil {
		return edit{}, fmt.Errorf("%s: %w", rel, err)
	}
	return edit{path: rel, old: old, new: []byte(doc)}, nil
}

// removeBlockEdit takes Laneway's block out of the file rel. It returns false
// when there is nothing to do: no file, or no block in it.
func removeBlockEdit(root, rel string) (edit, bool, error) {
	old, err := readFile(root, rel)
	if err != nil || old == nil {
		return edit{}, false, err
	}
	doc, ok, err := removeBlock(string(old))
	if err != nil {
		return edit{}, false, fmt.Errorf("%s: %w", rel, err)
	}
	return edit{path: rel, old: old, new: []byte(doc)}, ok, nil
}

func sameFile(a, b string) bool {
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	return err == nil && os.SameFile(ia, ib)
}
