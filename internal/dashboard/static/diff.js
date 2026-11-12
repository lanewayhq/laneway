// The diff dialog: one changed file's diff against HEAD, or one commit's
// patch, of a Worktree (GET /v1/worktree/diff), opened from the lists of
// changed files and commits in its row on the Project page.
import { notice } from "./confirm.js";
import { h } from "./dom.js";

// showDiff opens the dialog for changed file path, or for commit (a hash
// with its subject), of Worktree w of Project p, reading the diff as it
// opens. It resolves once the dialog is closed.
export function showDiff(dash, p, w, { path, commit }) {
  const name = w.name || "main";
  const body = h("div", { class: "diff-view" }, h("p", { class: "empty" }, "Reading…"));
  const q = { dir: w.path, ...(path ? { path } : { commit: commit.hash }) };
  dash.api.get("/v1/worktree/diff?" + new URLSearchParams(q)).then((res) => {
    body.replaceChildren(...diffLines(res.diff, path ? "file" : "commit"),
      res.truncated ? h("p", { class: "diff-cut" }, "The diff stops here: it's over 1 MiB. Run ",
        h("code", {}, path ? `git diff HEAD -- ${path}` : `git show ${commit.hash.slice(0, 12)}`), " in the Worktree for all of it.") : "");
  }, (err) => {
    body.replaceChildren(h("p", { class: "dlg-error" }, err.message));
  });
  return notice({
    title: path ?? `${commit.hash.slice(0, 7)} ${commit.subject}`,
    subtitle: [p.name, " · ", name, path ? " · uncommitted changes against HEAD" : ""],
    className: "diff",
    body: [body],
  });
}

// diffLines are the lines of a unified diff, each coloured by what it is:
// a file's header, a hunk's header, an added, removed or unchanged line.
// A commit's own header and message, before its first file, are muted.
function diffLines(text, kind) {
  if (!text) {
    return [h("p", { class: "empty" }, kind === "file" ? "No changes git can show as text." : "This commit changes no files.")];
  }
  let inFile = false;
  return text.replace(/\n$/, "").split("\n").map((line) => {
    if (line.startsWith("diff --git ") || line.startsWith("diff --cc ")) {
      inFile = true;
    }
    return h("div", { class: `dl dl-${lineKind(line, inFile)}` }, line || " ");
  });
}

function lineKind(line, inFile) {
  if (!inFile) {
    return /^(commit|Author|AuthorDate|Commit|CommitDate|Date|Merge):? /.test(line) ? "meta" : "msg";
  }
  if (/^(diff |index |--- |\+\+\+ |new file mode|deleted file mode|old mode|new mode|similarity index|rename |copy |Binary files )/.test(line)) {
    return "meta";
  }
  if (line.startsWith("@@")) {
    return "hunk";
  }
  if (line.startsWith("+")) {
    return "add";
  }
  if (line.startsWith("-")) {
    return "del";
  }
  return line.startsWith("\\") ? "meta" : "ctx";
}
