// Which Worktrees need attention and which are ready to Remove, from the
// Projects in a ProjectList (GET /v1/projects or the `projects` event).
// Pure functions of that data: no DOM, no requests.
//
// A Worktree needs attention when it failed preparing, has uncommitted
// changes, or has commits not on its Project's Default Branch. A linked
// Worktree is ready to Remove when it is merged and clean. A Worktree whose
// merge status git couldn't read (merge.error, such as a Project with no
// Default Branch) is in neither list: Laneway can't say.

// failed is whether Worktree w failed preparing.
export function failed(w) {
  return w.setup?.state === "failed";
}

// changed is whether Worktree w has uncommitted changes.
export function changed(w) {
  return Boolean(w.merge && !w.merge.error && !w.merge.clean);
}

// unmerged is whether Worktree w has commits not on the Default Branch.
export function unmerged(w) {
  return Boolean(w.merge && !w.merge.error && !w.merge.merged);
}

// removable is whether Worktree w is a linked Worktree, merged and clean.
export function removable(w) {
  return Boolean(!w.main && w.merge && !w.merge.error && w.merge.merged && w.merge.clean);
}

// needsAttention lists what needs attention across projects, failures
// first:
//   failed:   [{ project, worktrees, steps }] one per Project with failed
//             Worktrees, steps being the distinct steps that failed;
//   changed:  [{ project, worktree }] Worktrees with uncommitted changes;
//   unmerged: [{ project, worktree }] Worktrees with unmerged commits;
//   count:    how many Worktrees are in any of them (each counted once).
export function needsAttention(projects) {
  const result = { failed: [], changed: [], unmerged: [], count: 0 };
  for (const project of projects) {
    const failures = project.worktrees.filter(failed);
    if (failures.length) {
      const steps = [...new Set(failures.map((w) => w.setup.step).filter(Boolean))];
      result.failed.push({ project, worktrees: failures, steps });
    }
    for (const worktree of project.worktrees) {
      if (changed(worktree)) {
        result.changed.push({ project, worktree });
      }
      if (unmerged(worktree)) {
        result.unmerged.push({ project, worktree });
      }
      if (failed(worktree) || changed(worktree) || unmerged(worktree)) {
        result.count++;
      }
    }
  }
  return result;
}

// readyToRemove lists the linked Worktrees across projects that are merged
// and clean, as [{ project, worktree }].
export function readyToRemove(projects) {
  return projects.flatMap((project) =>
    project.worktrees.filter(removable).map((worktree) => ({ project, worktree })));
}
