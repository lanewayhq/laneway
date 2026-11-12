// A Worktree's changed files and its commits that aren't on its Project's
// Default Branch, as rows of a list (confirm.js list): the Project page's
// expanded row and the Remove dialog both show them this way.
import { h } from "./dom.js";
import { absolute, relative } from "./time.js";

// fileRow is a changed file: a one-letter git status (M, A, D, R, or ? for
// untracked) and its path.
export function fileRow(f) {
  const status = f.status === "??" ? "?" : f.status.trim().charAt(0) || "M";
  const tone = status === "?" ? "new" : status === "D" ? "gone" : "mod";
  return [h("span", { class: `flag ${tone}`, title: f.status === "??" ? "untracked" : f.status }, status),
    h("span", { class: "file", title: f.path }, f.path)];
}

// commitRow is a commit: its short hash, subject and age.
export function commitRow(c) {
  const t = new Date(c.time);
  return [h("span", { class: "hash", title: c.hash }, c.hash.slice(0, 7)), h("span", { class: "subject", title: c.subject }, c.subject),
    h("span", { class: "age", title: absolute(t) }, relative(t))];
}
