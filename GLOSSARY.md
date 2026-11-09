# Laneway

Laneway is a local web development environment manager. It serves every registered project at its own local domain, starts a project's processes only when they're needed, and gives each git worktree its own isolated environment.

## Projects and worktrees

**Project**:
A git repository registered with Laneway by Linking it.
_Avoid_: Site, app, stack

**Link**:
Registering a repository as a Project. Linking always registers the whole repository; its Worktrees are found automatically.
_Avoid_: Add, register, park

**Worktree**:
One checkout of a Project. The main checkout is a Worktree like any other, with no special status.
_Avoid_: Instance, environment, branch, lane

**Env Patch**:
The set of env file keys Laneway owns and writes for each Worktree, such as its URL, database name and Service ports. Every other key in the file belongs to the developer and is never touched. Laneway re-applies it whenever its inputs change, such as when the Worktree's URL changes.
_Avoid_: Env override, env template, env sync

**Ready**:
A Worktree is Ready once its own database exists and its env file has been patched, so an agent or developer can start work in it.
_Avoid_: Provisioned, set up, initialised

**Preparing**:
A Worktree is Preparing from when Laneway finds it until it is Ready: the files config says to copy are copied from the main Worktree, its database is being made, its env file patched and its Project's steps run.
_Avoid_: Provisioning, setting up, initialising

**Test Database**:
A second database belonging to a Worktree, used only by its test suite, so test runs in different Worktrees never touch each other's data.
_Avoid_: Testing DB, CI database

**Orphan**:
A database (including a Test Database) left behind by a removed Worktree, kept for a grace period before it is dropped.
_Avoid_: Stale database, leftover

**Domain**:
The local hostname a Worktree is served at, or, once it has a different Primary Domain, redirects browsers from. The main Worktree uses `<project>.localhost`. Every other Worktree uses `<worktree>.<project>.localhost`, named after git's name for the Worktree, not its branch.
_Avoid_: URL, host, site

**Alias**:
An extra, human-chosen Domain for a Worktree, alongside its own Domain.
_Avoid_: Nickname, vanity domain

**Primary Domain**:
The Domain Laneway gives out for a Worktree: its newest Alias if it has one, otherwise its own Domain.
_Avoid_: Preferred alias, main URL, canonical domain

## Running things

**Process**:
A long-running command Laneway supervises on behalf of one Worktree, such as a web server, queue worker or asset bundler.
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

**Engine**:
A kind of Service, such as Postgres or Redis. A Service is an Engine at a version.
_Avoid_: Driver, backend, image

**Recipe**:
A stack definition: how to recognise a kind of Project and which Processes it runs by default. A Project has at most one Recipe.
_Avoid_: Framework support, preset, driver

**Integration**:
A tool a Project uses alongside its stack, such as Vite or Horizon, that Laneway recognises on its own and adds Processes, env or Services for. A Project can have any number of Integrations on top of its Recipe.
_Avoid_: Add-on, companion, extra, mixin

**Runtime**:
A language toolchain at a specific version, such as Node 22 or PHP 8.4, that a Project's Processes need.
_Avoid_: SDK, interpreter

## Extending Laneway

**Plugin**:
A package, installed separately from Laneway, that adds one or more Recipes, Integrations, Engines or Generators to it.
_Avoid_: Extension, add-on, package, pack

**Catalog**:
The list of first-party Plugins that Laneway may fetch on its own when a Project needs one. A Plugin outside the Catalog is only installed when a developer adds it by hand.
_Avoid_: Registry, marketplace, store, index

**Generator**:
Config a Plugin keeps merged into a Project's committed files, so that a tool such as a Harness works with Laneway. It is only written when a developer asks for it.
_Avoid_: Scaffold, template, codegen

## Agents

**Harness**:
An AI coding tool that works inside a Project's Worktrees, such as Claude Code or T3 Code.
_Avoid_: Agent tool, IDE
