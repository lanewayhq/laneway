# Worktrees are discovered from git's own registry, not from Harness hooks

Laneway finds Worktrees by watching each Project's `.git/worktrees/` directory. Git records every linked worktree there, whoever created it and wherever it lives on disk. That directory is the only source of truth for when a Worktree is created and removed.

Harness hooks were rejected as the source of truth because coverage is patchy. T3 Code, Cursor and Codex have setup hooks but no teardown hook. Claude Code's `WorktreeRemove` only fires for worktrees its own `WorktreeCreate` hook made. No Harness hook fires when an agent runs `git worktree add` from its shell. Generated Harness config may still call Laneway to *wait for* a Worktree to be ready, but it never registers or removes one.
