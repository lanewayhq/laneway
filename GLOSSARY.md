# Portby

Portby is a local web development environment manager. It serves every registered project at its own local domain, starts a project's processes only when they're needed, and gives each git worktree its own isolated environment.

## Projects and worktrees

**Project**:
A git repository registered with Portby by Linking it.
_Avoid_: Site, app, stack

**Link**:
Registering a repository as a Project. Linking always registers the whole repository; its Worktrees are found automatically.
_Avoid_: Add, register, park

**Worktree**:
One checkout of a Project. The main checkout is a Worktree like any other, with no special status.
_Avoid_: Instance, environment, branch

**Env Patch**:
The set of env file keys Portby owns and writes for each Worktree, such as its URL, database name and Service ports. Every other key in the file belongs to the developer and is never touched. Portby re-applies it whenever its inputs change, such as when the Worktree's URL changes.
_Avoid_: Env override, env template, env sync

**Ready**:
A Worktree is Ready once its own database exists and its env file has been patched, so an agent or developer can start work in it.
_Avoid_: Provisioned, set up, initialised

**Preparing**:
A Worktree is Preparing from when Portby finds it until it is Ready: its database is being made, its env file patched and its Project's steps run.
_Avoid_: Provisioning, setting up, initialising

**Test Database**:
A second database belonging to a Worktree, used only by its test suite, so test runs in different Worktrees never touch each other's data.
_Avoid_: Testing DB, CI database

**Orphan**:
A database (including a Test Database) left behind by a removed Worktree, kept for a grace period before it is dropped.
_Avoid_: Stale database, leftover

**Domain**:
The local hostname a Worktree is served at. The main Worktree uses `<project>.localhost`. Every other Worktree uses `<worktree>.<project>.localhost`, named after git's name for the Worktree, not its branch.
_Avoid_: URL, host, site

**Alias**:
An extra, human-chosen Domain for a Worktree, alongside its own Domain.
_Avoid_: Nickname, vanity domain

## Running things

**Process**:
A long-running command Portby supervises on behalf of one Worktree, such as a web server, queue worker or asset bundler.
_Avoid_: App, server, daemon, job

**Primary Process**:
The one Process in a Worktree that serves its Domain. Any other HTTP-serving Process is reached at `<process>.<domain>`.
_Avoid_: Main process, web process

**Start Mode**:
Whether a Process is started on the first request to its Worktree (`lazy`), always kept running (`always`), or only started by hand (`manual`).
_Avoid_: Autostart

**Idle**:
A Worktree is Idle once it has had no requests and no open connections for the configured timeout. An Idle Worktree has its lazy Processes stopped.
_Avoid_: Inactive, sleeping

**Service**:
Shared backing infrastructure, such as a database or cache, that many Projects use at the same time. Like lazy Processes, a Service starts on first use and stops when unused.
_Avoid_: Container, dependency, add-on

**Recipe**:
A stack definition: how to recognise a kind of Project and which Processes it runs by default.
_Avoid_: Plugin, framework support, preset, driver

**Runtime**:
A language toolchain at a specific version, such as Node 22 or PHP 8.4, that a Project's Processes need.
_Avoid_: SDK, interpreter

## Agents

**Harness**:
An AI coding tool that works inside a Project's Worktrees, such as Claude Code or T3 Code.
_Avoid_: Agent tool, IDE
