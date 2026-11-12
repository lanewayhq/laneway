// The Orphans page, at /orphans: databases and Buckets of removed
// Worktrees and Unlinked Projects, with the Worktree they belonged to, when
// they were orphaned and when the daemon drops them (24 hours later). It
// re-renders on every `orphans` event (and each minute, for the relative
// times).
//
// Dropping now matches `lane gc`: "Drop all now" previews (POST /v1/gc
// with preview, which also finds leftover parallel test copies), asks, then
// sends exactly the previewed list; "Drop" on a row sends just that one.
// Both ask first.
import { h, pageHeader } from "../dom.js";
import { absolute, relative } from "../time.js";

export default {
  id: "orphans",
  path: "/orphans",
  title: "Orphans",
  show(el, dash) {
    const pending = new Set(); // keys of buttons whose request is in flight
    let last = null;
    let message = ""; // what the last "Drop all now" did

    const render = (list) => {
      last = list;
      const header = pageHeader("Orphans",
        ["Databases and Buckets of removed Worktrees and Unlinked Projects. Laneway drops each 24 hours after it was orphaned; " +
          "linking the Worktree again before then keeps it.",
        message ? h("span", { class: "status" }, message) : ""],
        [button("gc", "Drop all now", async () => (message = await dropAll(dash)))]);
      if (list.orphans.length === 0) {
        el.replaceChildren(header, h("div", { class: "body" }, h("p", { class: "empty" }, "No Orphans.")));
        return;
      }
      el.replaceChildren(header, h("table", { class: "tbl" },
        h("thead", {}, h("tr", {}, ...["Name", "Service", "Kind", "Worktree", "Orphaned", "Dropped", ""].map((c) => h("th", {}, c)))),
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
      const b = h("button", { type: "button", class: "btn danger", disabled: pending.has(key) }, pending.has(key) ? label + "…" : label);
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
    const timer = setInterval(() => last && render(last), 60_000);
    dash.signal.addEventListener("abort", () => clearInterval(timer));
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

// dropAll does what `lane gc` does: preview, ask, drop what was listed.
// It returns what happened, for the page to show.
async function dropAll(dash) {
  const preview = await dash.api.post("/v1/gc", { preview: true });
  dash.report(preview.errors?.length ? preview.errors.join("\n") : "");
  if (preview.orphans.length === 0) {
    return "Nothing to drop.";
  }
  const n = preview.orphans.length;
  const ok = await dash.confirm({
    title: `Drop ${n === 1 ? "this Orphan" : `these ${n} Orphans`} now?`,
    body: [
      h("ul", { class: "drops" }, ...preview.orphans.map((o) => h("li", {},
        h("code", {}, o.database), ` in ${o.service}: ${o.since ? `${o.kind} of ${describe(o)}` : "leftover parallel test copy"}`))),
      n === 1 ? "Its data can't be brought back." : "Their data can't be brought back.",
    ],
    action: n === 1 ? "Drop it" : `Drop ${n} Orphans`,
  });
  if (!ok) {
    return "";
  }
  const dropped = await gc(dash, preview.orphans);
  return `Dropped ${dropped} Orphan${dropped === 1 ? "" : "s"}.`;
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
