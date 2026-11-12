// Small facts about the Projects in a ProjectList (GET /v1/projects or the
// `projects` event) that the sidebar and several pages show the same way.
// Pure functions of that data: no DOM, no requests.

// projectHref is the path of Project name's page, opening with linked
// Worktree worktree's row expanded when one is given (?worktree=<name>).
export function projectHref(name, worktree) {
  const href = "/projects/" + encodeURIComponent(name);
  return worktree?.name && !worktree.main ? href + "?" + new URLSearchParams({ worktree: worktree.name }) : href;
}

// worktreeName is how the Dashboard names Worktree w: git's name for a
// linked Worktree, "main" for the main Worktree (which has none).
export function worktreeName(w) {
  return w.main ? "main" : w.name;
}

// runs is whether Worktree w is running or starting.
export function runs(w) {
  return w.state === "running" || w.state === "starting";
}

// runState is "run" when any of Project p's Worktrees runs, else "idle"
// when any is Idle (stopped after its idle timeout, started again by the
// next request), else "stopped".
export function runState(p) {
  if (p.worktrees.some(runs)) {
    return "run";
  }
  return p.worktrees.some((w) => w.state === "idle") ? "idle" : "stopped";
}

// runStateLabels name the run states, as a status dot's tooltip.
export const runStateLabels = { run: "running", idle: "Idle", stopped: "stopped" };

// runningProcesses is how many of Project p's Processes are running.
export function runningProcesses(p) {
  return p.worktrees.reduce((n, w) => n + (w.processes ?? []).filter((pr) => pr.state === "running").length, 0);
}
