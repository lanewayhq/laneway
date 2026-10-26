// The Orphans panel: databases of removed Worktrees and Unlinked Projects,
// with the Worktree they belonged to, when they were orphaned and when the
// daemon drops them (24 hours later). It re-renders on every `orphans`
// event (and each minute, for the relative times).
//
// Dropping now matches `portby gc`: "Drop all now" previews (POST /v1/gc
// with preview, which also finds leftover parallel test copies), asks, then
// sends exactly the previewed list; "Drop" on a row sends just that one.
// It also adds "Unlink" to each Project's main Worktree in the Projects
// panel (DELETE /v1/projects), since Unlinking is what makes a Project's
// databases Orphans. Every one of these asks first.
import { h } from "../dom.js";
import { absolute, relative } from "../time.js";

export default {
  id: "orphans",
  title: "Orphans",
  mount(el, dash) {
    const pending = new Set(); // keys of buttons whose request is in flight
    let last = null;
    let message = ""; // what the last "Drop all now" did

    dash.worktreeActions.push((w, p) => (w.main ? [{ label: "Unlink", run: () => unlink(dash, p) }] : []));

    const render = (list) => {
      last = list;
      const note = h("p", { class: "note" },
        "Databases of removed Worktrees and Unlinked Projects. Portby drops each 24 hours after it was orphaned; ",
        "linking the Worktree again before then keeps it.");
      const header = h("div", { class: "panel-header" }, note,
        h("span", { class: "status" }, message),
        button("gc", "Drop all now", async () => (message = await dropAll(dash))));
      if (list.orphans.length === 0) {
        el.replaceChildren(header, h("p", { class: "empty" }, "No Orphans."));
        return;
      }
      el.replaceChildren(header, h("table", {},
        h("thead", {}, h("tr", {}, ...["Database", "Service", "Kind", "Worktree", "Orphaned", "Dropped", ""].map((c) => h("th", {}, c)))),
        h("tbody", {}, ...list.orphans.map(row))));
    };

    const row = (o) => h("tr", { "data-database": o.database },
      h("td", {}, h("code", {}, o.database)),
      h("td", {}, o.service),
      h("td", {}, o.kind),
      h("td", {}, worktreeOf(o)),
      h("td", {}, when(o.since)),
      h("td", {}, when(o.expires)),
      h("td", { class: "actions" }, button(`${o.service}/${o.database}`, "Drop", () => dropOne(dash, o))));

    // button runs fn once, disabled until it finishes.
    const button = (key, label, fn) => {
      const b = h("button", { type: "button", class: "danger", disabled: pending.has(key) }, pending.has(key) ? label + "…" : label);
      b.addEventListener("click", async () => {
        pending.add(key);
        message = "";
        render(last);
        try {
          await fn();
        } catch (err) {
          dash.report(err.message);
        } finally {
          pending.delete(key);
          render(last);
        }
      });
      return b;
    };

    dash.onOrphans(render);
    setInterval(() => last && render(last), 60_000);
  },
};

// dropOne drops one Orphan after asking.
async function dropOne(dash, o) {
  const ok = await dash.confirm({
    title: `Drop ${o.database}?`,
    body: [
      `The ${o.kind} of ${describe(o)} in ${o.service} is dropped now instead of ${relative(new Date(o.expires))}. Its data can't be brought back.`,
    ],
    action: "Drop it",
  });
  if (ok) {
    await gc(dash, [o]);
  }
}

// dropAll does what `portby gc` does: preview, ask, drop what was listed.
// It returns what happened, for the panel to show.
async function dropAll(dash) {
  const preview = await dash.api.post("/v1/gc", { preview: true });
  dash.report(preview.errors?.length ? preview.errors.join("\n") : "");
  if (preview.orphans.length === 0) {
    return "Nothing to drop.";
  }
  const n = preview.orphans.length;
  const ok = await dash.confirm({
    title: `Drop ${n === 1 ? "this database" : `these ${n} databases`} now?`,
    body: [
      h("ul", { class: "drops" }, ...preview.orphans.map((o) => h("li", {},
        h("code", {}, o.database), ` in ${o.service}: ${o.since ? `${o.kind} of ${describe(o)}` : "leftover parallel test copy"}`))),
      n === 1 ? "Its data can't be brought back." : "Their data can't be brought back.",
    ],
    action: n === 1 ? "Drop it" : `Drop ${n} databases`,
  });
  if (!ok) {
    return "";
  }
  const dropped = await gc(dash, preview.orphans);
  return `Dropped ${dropped} database${dropped === 1 ? "" : "s"}.`;
}

// gc drops exactly orphans, reports any that couldn't be and returns how
// many were.
async function gc(dash, orphans) {
  const res = await dash.api.post("/v1/gc", {
    databases: orphans.map((o) => ({ service: o.service, database: o.database })),
  });
  dash.report(res.errors?.length ? res.errors.join("\n") : "");
  return res.orphans.length;
}

// unlink Unlinks Project p after asking.
async function unlink(dash, p) {
  const ok = await dash.confirm({
    title: `Unlink ${p.name}?`,
    body: [
      `Portby stops its Worktrees and forgets its Domains (${p.worktrees.map((w) => w.domain).filter(Boolean).join(", ")}).`,
      "Its databases become Orphans and are dropped in 24 hours unless you link it again. Files, including .env, are left alone.",
    ],
    action: "Unlink",
  });
  if (ok) {
    await dash.api.del("/v1/projects?" + new URLSearchParams({ dir: p.path }));
  }
}

function describe(o) {
  return o.worktree ? `${o.project}'s Worktree ${o.worktree}` : `${o.project}'s main Worktree`;
}

function worktreeOf(o) {
  return h("span", {}, o.project, " ", h("span", { class: "tag" }, o.worktree || "main"));
}

function when(iso) {
  if (!iso) {
    return "-";
  }
  const t = new Date(iso);
  return h("time", { datetime: iso, title: absolute(t) }, relative(t));
}
