// Removing a linked Worktree from the Dashboard: dash.removeWorktree.
//
// It asks the daemon what removing the Worktree would lose (POST
// /v1/worktree/remove with preview), shows that in the Remove dialog, and,
// once confirmed, sends back exactly what the dialog showed: its branch,
// every unmerged commit and every changed file, and whether "Also delete
// branch" was ticked. The daemon refuses the removal if the Worktree has
// changed since (worktree_changed) or is locked (worktree_locked); the
// dialog then shows why, and nothing is removed.
//
// The dialog, from the preview:
//   - merged and clean: a green note;
//   - changed files: a red note listing them, and the button becomes
//     "Remove and discard changes";
//   - unmerged commits on a branch: an amber note listing them, with "Also
//     delete branch x", unchecked;
//   - unmerged commits on no branch: a red note listing them (nothing keeps
//     them once the Worktree goes);
//   - locked: why, and only a Close button;
// then what removing it does: its checkout (and a merged branch) deleted,
// its Processes stopped and Domains freed, its databases and Buckets
// turned into Orphans.
import { confirm, list, notice } from "./confirm.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";
import { absolute, relative } from "./time.js";

// removeWorktree previews, asks and removes worktree (a Worktree of
// project, as the `projects` event has them). It resolves true only if the
// Worktree was removed; it never rejects. Its row then goes by itself with
// the next `projects` event.
export async function removeWorktree(dash, worktree, project) {
  const target = { dir: project.path, worktree: worktree.path };
  let preview;
  try {
    preview = await dash.api.post("/v1/worktree/remove", { ...target, preview: true });
  } catch (err) {
    dash.report(err.message);
    return false;
  }
  const name = worktree.name || preview.worktree?.name || worktree.path;
  const subtitle = [preview.project || project.name, " · ", h("span", { class: "mono" }, preview.worktree?.path || worktree.path)];

  if (preview.locked) {
    await notice({
      title: `Worktree ${name} is locked`,
      subtitle,
      className: "remove",
      body: [note("warn", "lock", [h("b", {}, "git won't remove a locked Worktree")],
        [preview.lockReason ? ["It was locked: ", h("q", {}, preview.lockReason), ". "] : "",
          "Unlock it with ", h("code", {}, "git worktree unlock"), " when whatever locked it is done, then Remove it."])],
    });
    return false;
  }

  const deleteBranch = h("input", { type: "checkbox" });
  return confirm({
    title: `Remove Worktree ${name}?`,
    subtitle,
    className: "remove",
    body: dialogBody(preview, deleteBranch),
    action: preview.clean ? "Remove Worktree" : "Remove and discard changes",
    busy: "Removing…",
    async run() {
      const res = await dash.api.post("/v1/worktree/remove", {
        ...target,
        branch: preview.branch ?? "",
        unmergedCommits: preview.unmergedCommits.map((c) => c.hash),
        changedFiles: preview.changedFiles.map((f) => f.path),
        deleteBranch: deleteBranch.checked,
      }).catch((err) => {
        if (err.code === "worktree_changed") {
          err.message += " Close this and choose Remove again to see what it has now.";
        }
        throw err;
      });
      dash.report(res.warnings?.length ? res.warnings.join("\n") : "");
    },
  });
}

// dialogBody is the Remove dialog's notes and its "Removing it" list.
function dialogBody(p, deleteBranch) {
  const def = p.defaultBranch || "the Default Branch";
  const defCode = p.defaultBranch ? code(p.defaultBranch) : def;
  const checked = p.defaultBranch ? ["Checked against ", code(p.defaultBranch), " and ", code("origin/" + p.defaultBranch), " as of the last fetch."] : "";
  const commits = p.unmergedCommits;
  const n = commits.length;
  const body = [];

  if (p.merged && p.clean) {
    body.push(note("ok", "merged",
      p.branch ? [h("b", {}, "Branch ", code(p.branch), " is merged into ", defCode, ".")] : [h("b", {}, "Its commits are all on ", defCode, ".")],
      ["No uncommitted changes. ", checked]));
  }
  if (!p.clean) {
    const f = p.changedFiles.length;
    body.push(note("bad", "warning", [h("b", {}, f === 1 ? "1 uncommitted change will be lost" : `${f} uncommitted changes will be lost`)],
      [f === 1 ? "It isn't" : "They aren't", " in any commit, so nothing can bring ", f === 1 ? "it" : "them",
        " back. Ignored files such as ", code(".env"), " are deleted too."],
      list(p.changedFiles, fileRow, { className: "files" })));
  }
  if (n > 0 && !p.branch) {
    body.push(note("bad", "warning",
      [h("b", {}, n === 1 ? "1 commit isn't on any branch or on " : `${n} commits aren't on any branch or on `, defCode)],
      ["This Worktree isn't on a branch, so once it's removed nothing keeps ", n === 1 ? "it" : "them",
        " and ", n === 1 ? "it becomes" : "they become", " unreachable. ", checked],
      list(commits, commitRow, { className: "commits" })));
  } else if (n > 0) {
    const sub = h("div", { class: "sub" });
    const explain = () => sub.replaceChildren(deleteBranch.checked
      ? `On: the branch is deleted too, so ${n === 1 ? "its commit becomes" : "its commits become"} unreachable.`
      : `Off: the branch is kept, so ${n === 1 ? "its commit stays" : "its commits stay"} reachable after the Worktree is gone.`);
    explain();
    body.push(note("warn", "commit",
      [h("b", {}, n === 1 ? "1 commit on " : `${n} commits on `, code(p.branch), n === 1 ? " isn't merged into " : " aren't merged into ", defCode)],
      [checked, checked ? " " : "", "A squash-merged branch also shows up here."],
      list(commits, commitRow, { className: "commits" }),
      h("label", { class: "chk" }, deleteBranch, h("span", {}, "Also delete branch ", code(p.branch), sub))));
    deleteBranch.addEventListener("change", () => {
      explain();
      checkout.replaceChildren(...checkoutText());
    });
  }

  // What removing it does.
  const checkoutText = () => {
    if (p.branch && p.merged && p.branch !== p.defaultBranch) {
      return ["deletes its checkout and the merged branch ", code(p.branch)];
    }
    if (p.branch && deleteBranch.checked) {
      return ["deletes its checkout and the branch ", code(p.branch)];
    }
    if (p.branch) {
      return ["deletes its checkout and keeps the branch ", code(p.branch)];
    }
    return ["deletes its checkout"];
  };
  const checkout = h("span", {}, ...checkoutText());
  const [primary, ...others] = p.domains ?? [];
  const items = [
    [icon("orphans"), checkout],
    [icon("stop"), h("span", {}, "stops its Processes",
      primary ? [" and frees ", code(primary), others.length ? ` and ${others.length} other Domain${others.length === 1 ? "" : "s"}` : ""] : "")],
  ];
  if (p.orphans?.length) {
    items.push([icon("database"), h("span", {}, ...orphansText(p.orphans))]);
  }
  body.push(h("div", { class: "removing" }, h("div", { class: "lbl" }, "Removing it"),
    h("ul", { class: "what" }, ...items.map((children) => h("li", {}, ...children)))));
  for (const w of p.warnings ?? []) {
    body.push(h("p", { class: "dlg-warning" }, w));
  }
  return body;
}

// orphansText says which databases and Buckets become Orphans, and when
// they are dropped.
function orphansText(orphans) {
  const names = (kind) => orphans.filter((o) => o.kind === kind).map((o) => code(o.database));
  const dbs = names("database");
  const buckets = names("bucket");
  const parts = [];
  if (dbs.length) {
    parts.push([dbs.length === 1 ? "its database " : "its databases ", ...and(dbs)]);
  }
  if (buckets.length) {
    parts.push([buckets.length === 1 ? "its Bucket " : "its Buckets ", ...and(buckets)]);
  }
  const grace = Math.round((new Date(orphans[0].expires) - new Date(orphans[0].since)) / 3_600_000);
  const one = orphans.length === 1;
  return ["makes ", ...and(parts), one ? " an Orphan, which is" : " Orphans, which are", ` dropped in ${grace} hours`];
}

// and joins items as "a", "a and b", "a, b and c".
function and(items) {
  return items.flatMap((item, i) => [i === 0 ? "" : i === items.length - 1 ? " and " : ", ", item].flat());
}

// note is a coloured block: a heading, a muted line under it, and more
// (lists, a checkbox).
function note(tone, iconName, title, sub, ...more) {
  return h("div", { class: `note ${tone}` }, icon(iconName),
    h("div", { class: "t" }, ...title, h("div", { class: "sub" }, ...[sub].flat(2)), ...more));
}

function fileRow(f) {
  const status = f.status === "??" ? "?" : f.status.trim().charAt(0) || "M";
  const tone = status === "?" ? "new" : status === "D" ? "gone" : "mod";
  return [h("span", { class: `flag ${tone}`, title: f.status }, status), h("span", { class: "file" }, f.path)];
}

function commitRow(c) {
  const t = new Date(c.time);
  return [h("span", { class: "hash" }, c.hash.slice(0, 7)), h("span", { class: "subject", title: c.subject }, c.subject),
    h("span", { class: "age", title: absolute(t) }, relative(t))];
}

function code(text) {
  return h("code", {}, text);
}
