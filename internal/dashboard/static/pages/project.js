// A Project's page, at /projects/<name>.
//
// A header names the Project with its path, Recipe (GET /v1/info, read
// once), Default Branch and how many Processes run, with its Plugins'
// badges, links and actions, and Logs and Unlink buttons. Tabs narrow the
// Worktrees to All, Running, Merged or Needs attention (as attention.js
// says: Preparing failed, changed files or unmerged commits, failures
// listed first), each with a live count, and "Filter Worktrees…" narrows
// them by name, branch or Domain.
//
// One quiet row per Worktree shows its name and branch, Primary Domain
// (and how many other Domains it has), Readiness, merge status against the
// Default Branch, its Processes as status dots, and a few icon buttons:
// start or stop and restart its Processes, a "⋯" menu with the less common
// actions (its Plugins' actions and the ones other pages add through
// dash.worktreeActions, such as Logs), and, on every linked Worktree, Remove
// (dash.removeWorktree). A row expands into its Processes with their own
// controls (and dash.processActions) and the last lines of their output
// (GET /v1/logs without follow), linking to the Logs page for that
// Worktree. A linked Worktree with changed files or commits not on the
// Default Branch lists them there too, as the merge status in the
// `projects` event has them (at most 50 of each, with the full counts);
// clicking one shows its diff (diff.js).
// /projects/<name>?worktree=<git name> opens with that Worktree's row
// expanded and scrolled to.
//
// It re-renders on every `projects` event, keeping which rows are expanded,
// which menu is open, which requests are in flight and where the focus is.
import { attentionNeeded, byAttention, failed } from "../attention.js";
import { commitRow, fileRow } from "../changes.js";
import { list as rows } from "../confirm.js";
import { showDiff } from "../diff.js";
import { h, pageHeader } from "../dom.js";
import { icon } from "../icons.js";
import { pluginActions, pluginBadges, pluginLinks } from "../pluginitems.js";
import { runningProcesses, runs, worktreeName } from "../projects.js";
import { colors, logsHref, plain } from "./logs.js";

// recentLines is how many lines of a Worktree's output its row shows.
const recentLines = 6;

// recipes caches each Project's Recipe by its path: it only changes when
// its config does, so it is read once.
const recipes = new Map();

// tabs narrow the Worktrees to those it has, in its order if it has one.
const tabs = [
  { id: "all", label: "All", has: () => true },
  { id: "running", label: "Running", has: runs },
  { id: "merged", label: "Merged", has: merged },
  { id: "attention", label: "Needs attention", has: attentionNeeded, order: byAttention },
];

export default {
  id: "project",
  path: "/projects/:name",
  title: (route) => route.params.name,
  show(el, dash, route) {
    const name = route.params.name;
    const open = new Set(); // paths of the expanded Worktree rows
    const pending = new Set(); // keys of the buttons whose request is in flight
    const recent = new Map(); // Worktree path -> { lines, error, loading, states }
    let menu = null; // key of the open "⋯" menu
    let menuJustOpened = false;
    let tab = "all";
    let last = null;
    let project = null;
    let wanted = route.query.get("worktree"); // a Worktree to open, from the URL
    let scrollTo = null; // path of a row to scroll to once rendered

    const header = h("div");
    const warnings = h("div");
    const counts = h("div", { class: "tab-list", role: "group", "aria-label": "Show Worktrees" });
    const query = h("input", { type: "search", placeholder: "Filter Worktrees…", "aria-label": "Filter Worktrees", autocomplete: "off" });
    const bar = h("div", { class: "tabs" }, counts, h("label", { class: "filter" }, icon("search"), query));
    const list = h("div", { class: "wts", role: "list" });

    const render = () => {
      if (!last) {
        return;
      }
      const p = last.projects.find((p) => p.name === name);
      project = p;
      if (!p) {
        el.replaceChildren(pageHeader(name), h("div", { class: "body" }, h("p", { class: "empty" },
          "No Project named ", h("strong", {}, name), " is Linked. Run ", h("code", {}, "lane link"),
          " in its git repository, or go back to the ", h("a", { href: "/" }, "Overview"), ".")));
        return;
      }
      if (!el.contains(list)) {
        el.replaceChildren(header, warnings, bar, list);
      }
      if (wanted) {
        const w = p.worktrees.find((w) => !w.main && w.name === wanted);
        if (w) {
          open.add(w.path);
          scrollTo = w.path;
        }
        wanted = null;
      }
      const focused = document.activeElement?.closest?.("[data-focus]")?.dataset.focus;
      loadRecipe(p);
      header.replaceChildren(projectHeader(p));
      // Problems with the whole Project, such as Catalog Plugins it needs
      // that Laneway couldn't fetch when it started.
      warnings.replaceChildren(...(p.warnings ?? []).map((w) => h("p", { class: "warning" }, "warning: ", w)));
      counts.replaceChildren(...tabs.map((t) => tabButton(t, p.worktrees.filter(t.has).length)));
      const current = tabs.find((t) => t.id === tab);
      const shown = p.worktrees.filter(current.has).filter(matches(query.value));
      if (current.order) {
        shown.sort(current.order);
      }
      list.replaceChildren(
        h("div", { class: "grid head", role: "presentation" },
          h("span"), h("span", {}, "Worktree"), h("span", {}, "Domain"), h("span", {}, "Readiness"),
          h("span", {}, "Branch"), h("span", {}, "Processes"), h("span")),
        ...shown.flatMap((w) => row(p, w)),
        shown.length ? "" : h("p", { class: "empty none" }, p.worktrees.length ? "No Worktrees match." : "No Worktrees."));
      for (const w of p.worktrees) {
        if (open.has(w.path)) {
          loadRecent(w);
        }
      }
      if (scrollTo) {
        list.querySelector(`[data-path="${CSS.escape(scrollTo)}"]`)?.scrollIntoView({ block: "start" });
        scrollTo = null;
      }
      // Re-rendering replaced the element that had the focus; give it to
      // its replacement, or to the first item of a menu just opened.
      const target = menuJustOpened ? list.querySelector(".menu [role=menuitem]")
        : focused ? el.querySelector(`[data-focus="${CSS.escape(focused)}"]`) : null;
      menuJustOpened = false;
      if (target && document.activeElement !== target) {
        target.focus();
      }
    };

    const projectHeader = (p) => {
      const recipe = recipes.get(p.path);
      const unlinkKey = `${p.path}:unlink`;
      const unlinkButton = h("button", { type: "button", class: "btn danger", "data-focus": unlinkKey, disabled: pending.has(unlinkKey) },
        pending.has(unlinkKey) ? "Unlink…" : "Unlink");
      unlinkButton.addEventListener("click", () => run(unlinkKey, () => unlink(dash, p)));
      return pageHeader(p.name,
        [h("span", { class: "mono", title: "The main Worktree's directory" }, p.path),
          recipe ? h("span", { title: `Recipe ${recipe}` }, recipe[0].toUpperCase() + recipe.slice(1)) : "",
          p.defaultBranch ? h("span", {}, "Default Branch ", h("b", { class: "mono" }, p.defaultBranch)) : "",
          plural(runningProcesses(p), "Process", "Processes") + " running"],
        [...pluginBadges(p), ...pluginLinks(p),
          ...pluginActions(dash, p, p.path).map((a) => textButton(`${p.path}:project:${a.label}`, a.label, a.run)),
          h("a", { class: "btn", href: logsHref(p.path), title: "The main Worktree's Logs" }, icon("logs"), "Logs"),
          unlinkButton]);
    };

    const tabButton = (t, n) => {
      const b = h("button", { type: "button", class: t.id === tab ? "tab on" : "tab", "aria-pressed": String(t.id === tab),
        "data-focus": `tab:${t.id}` }, t.label, h("span", { class: "c" }, String(n)));
      b.addEventListener("click", () => {
        tab = t.id;
        render();
      });
      return b;
    };

    // run runs fn (a request) once for the button key, which shows it is in
    // flight until fn finishes, and reports fn's error.
    const run = async (key, fn) => {
      if (pending.has(key)) {
        return;
      }
      pending.add(key);
      render();
      try {
        await fn();
        dash.report("");
      } catch (err) {
        dash.report(err.message);
      } finally {
        pending.delete(key);
        render();
      }
    };

    // iconButton is a small button showing only an icon, labelled for
    // screen readers and its tooltip.
    const iconButton = (key, name, label, fn, cls = "ib") => {
      const busy = pending.has(key);
      const b = h("button", { type: "button", class: busy ? `${cls} busy` : cls, title: label, "aria-label": label,
        "data-focus": key, disabled: busy, "aria-busy": busy ? "true" : false }, icon(name));
      b.addEventListener("click", () => run(key, fn));
      return b;
    };

    const textButton = (key, label, fn) => {
      const b = h("button", { type: "button", class: "btn", "data-focus": key, disabled: pending.has(key) },
        pending.has(key) ? label + "…" : label);
      b.addEventListener("click", () => run(key, fn));
      return b;
    };

    // startStop is Stop while the Worktree (or Process) runs, else Start.
    const startStop = (key, running, what, worktree, process) => running
      ? iconButton(`${key}:down`, "stop", `Stop ${what}`, () => dash.control("down", worktree, process))
      : iconButton(`${key}:up`, "start", `Start ${what}`, () => dash.control("up", worktree, process));

    // more is the "⋯" button opening a menu of actions ({ label, run }).
    const more = (key, label, actions) => {
      if (actions.length === 0) {
        return h("span", { class: "ib", "aria-hidden": "true" });
      }
      const opened = menu === key;
      const busy = actions.some((a) => pending.has(`${key}:${a.label}`));
      const b = h("button", { type: "button", class: busy ? "ib busy" : "ib", title: label, "aria-label": label,
        "aria-haspopup": "menu", "aria-expanded": String(opened), "data-focus": key }, icon("more"));
      b.addEventListener("click", () => {
        menu = opened ? null : key;
        menuJustOpened = !opened;
        render();
      });
      const items = opened ? h("div", { class: "menu", role: "menu", "aria-label": label }, ...actions.map((a) => {
        const itemKey = `${key}:${a.label}`;
        const item = h("button", { type: "button", role: "menuitem", "data-focus": itemKey, disabled: pending.has(itemKey) },
          pending.has(itemKey) ? a.label + "…" : a.label);
        item.addEventListener("click", () => {
          menu = null;
          run(itemKey, a.run);
        });
        return item;
      })) : "";
      return h("span", { class: "menu-wrap" }, b, items);
    };

    // Clicking elsewhere or pressing Escape closes the open menu.
    document.addEventListener("click", (e) => {
      if (menu && !e.target.closest?.(".menu-wrap")) {
        menu = null;
        render();
      }
    }, { signal: dash.signal });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && menu) {
        const key = menu;
        menu = null;
        render();
        el.querySelector(`[data-focus="${CSS.escape(key)}"]`)?.focus();
      }
    }, { signal: dash.signal });

    const toggle = (w) => {
      if (open.has(w.path)) {
        open.delete(w.path);
      } else {
        open.add(w.path);
        recent.delete(w.path); // read it afresh
      }
      render();
    };

    const row = (p, w) => {
      const key = w.path;
      const expanded = open.has(key);
      const label = worktreeName(w);
      const chev = h("button", { type: "button", class: "ib chev", "aria-expanded": String(expanded), "data-focus": `${key}:expand`,
        "aria-label": `${expanded ? "Hide" : "Show"} ${label}'s details` }, icon(expanded ? "collapse" : "expand"));
      chev.addEventListener("click", () => toggle(w));
      const r = h("div", { class: expanded ? "grid r open" : "grid r", role: "listitem", "data-domain": w.domain ?? "", "data-path": w.path },
        chev,
        h("div", { class: "wt" }, h("b", { title: w.path }, label), branchLine(p, w)),
        h("div", { class: "dom" }, domains(w), plugins(w)),
        readiness(w.setup),
        h("div", { class: "merge" }, mergeStatus(w)),
        processDots(w),
        h("div", { class: "acts" },
          startStop(key, runs(w), `${label}'s Processes`, w),
          iconButton(`${key}:restart`, "restart", `Restart ${label}'s Processes`, () => dash.control("restart", w)),
          more(`${key}:more`, `More actions for ${label}`,
            [...pluginActions(dash, w, w.path), ...dash.worktreeActions.flatMap((fn) => fn(w, p))]),
          !w.main
            ? iconButton(`${key}:remove`, "remove", `Remove Worktree ${label}`, () => dash.removeWorktree(w, p), "ib del")
            : h("span", { class: "ib", "aria-hidden": "true" })));
      // A click anywhere else on the row expands it too.
      r.addEventListener("click", (e) => {
        if (!e.target.closest("a, button, input, .menu")) {
          toggle(w);
        }
      });
      return expanded ? [r, expansion(p, w)] : [r];
    };

    const expansion = (p, w) => h("div", { class: "x" },
      failed(w) ? failure(w.setup) : "",
      changes(dash, p, w),
      h("div", { class: "xin" },
        h("section", { class: "box" }, h("h4", {}, "Processes"), ...processRows(p, w)),
        h("section", { class: "box" },
          h("h4", {}, "Recent output", h("a", { href: logsHref(w.path) }, "Open in Logs")),
          recentOutput(w))));

    // processRows lists the declared Processes plus any running one the
    // config no longer declares, each with its controls.
    const processRows = (p, w) => {
      const all = processesOf(w);
      if (all.length === 0) {
        return [h("p", { class: "empty pad" }, "The Worktree's config declares no Processes.")];
      }
      return all.map((d) => {
        const key = `${w.path}:${d.name}`;
        const state = d.state;
        const running = state === "running" || state === "restarting";
        return h("div", { class: "pr" },
          h("span", { class: "n" }, d.name, d.primary ? h("span", { class: "tag" }, "primary") : ""),
          h("span", { class: `st ${stateTone(state)}` }, h("i"), state),
          d.http && !d.primary && w.primaryDomain
            ? link(`${d.name}.${w.primaryDomain}`, "mono")
            : h("span", { class: "tag" }, d.startMode ?? ""),
          h("div", { class: "acts" },
            startStop(key, running, d.name, w, d.name),
            iconButton(`${key}:restart`, "restart", `Restart ${d.name}`, () => dash.control("restart", w, d.name)),
            more(`${key}:more`, `More actions for ${d.name}`, dash.processActions.flatMap((fn) => fn(w, p, d)))));
      });
    };

    const recentOutput = (w) => {
      const got = recent.get(w.path);
      if (!got || (got.loading && !got.lines)) {
        return h("div", { class: "log" }, h("p", { class: "empty" }, "Reading…"));
      }
      if (got.error) {
        return h("div", { class: "log" }, h("p", { class: "error" }, got.error));
      }
      if (got.lines.length === 0) {
        return h("div", { class: "log" }, h("p", { class: "empty" }, "No output yet."));
      }
      const names = processesOf(w).map((d) => d.name);
      return h("div", { class: "log", role: "log" }, ...got.lines.map((l) => {
        const i = names.indexOf(l.process);
        return h("div", { title: new Date(l.time).toLocaleString() },
          h("span", { class: "p process", "data-color": (i < 0 ? names.length : i) % colors }, l.process),
          h("span", { class: "t" }, plain(l.text)));
      }));
    };

    // loadRecent reads the Worktree's recent output when its row is
    // expanded, and again when its Processes' states change.
    const loadRecent = (w) => {
      const states = processesOf(w).map((d) => `${d.name}=${d.state}`).join(",");
      const got = recent.get(w.path);
      if (got && (got.loading || got.states === states)) {
        return;
      }
      const entry = { ...got, loading: true, states };
      recent.set(w.path, entry);
      const lines = [];
      dash.api.stream("/v1/logs?" + new URLSearchParams({ dir: w.path }), (l) => {
        lines.push(l);
        if (lines.length > recentLines) {
          lines.shift();
        }
      }, dash.signal).then(() => {
        Object.assign(entry, { lines, error: "", loading: false });
      }, (err) => {
        Object.assign(entry, { error: dash.signal.aborted ? "" : err.message, loading: false });
      }).finally(() => {
        if (!dash.signal.aborted && recent.get(w.path) === entry) {
          render();
        }
      });
    };

    const loadRecipe = (p) => {
      if (recipes.has(p.path)) {
        return;
      }
      recipes.set(p.path, ""); // asked once, even if it fails
      dash.api.get("/v1/info?" + new URLSearchParams({ dir: p.path })).then((info) => {
        recipes.set(p.path, info.recipe ?? "");
        if (!dash.signal.aborted && info.recipe && project?.path === p.path) {
          render();
        }
      }, () => {});
    };

    query.addEventListener("input", render);
    query.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && query.value) {
        query.value = "";
        render();
      }
    });

    dash.onProjects((list) => {
      last = list;
      render();
    });
  },
};

// unlink Unlinks Project p after asking, then shows the Overview.
async function unlink(dash, p) {
  const ok = await dash.confirm({
    title: `Unlink ${p.name}?`,
    body: [
      `Laneway stops its Worktrees and forgets its Domains (${p.worktrees.map((w) => w.domain).filter(Boolean).join(", ")}).`,
      "Its databases and Buckets become Orphans and are dropped in 24 hours unless you link it again. Files, including .env, are left alone.",
    ],
    action: "Unlink",
  });
  if (ok) {
    await dash.api.del("/v1/projects?" + new URLSearchParams({ dir: p.path }));
    dash.navigate("/");
  }
}

// merged is whether linked Worktree w's commits are all on the Default
// Branch.
function merged(w) {
  return !w.main && !!w.merge?.merged && !w.merge.error;
}

// matches is whether a Worktree's name, branch or Domains contain text.
function matches(text) {
  const q = text.trim().toLowerCase();
  return (w) => !q || [worktreeName(w), w.merge?.branch, w.domain, w.primaryDomain, ...(w.aliases ?? [])]
    .some((s) => s && s.toLowerCase().includes(q));
}

// branchLine is the Worktree's branch: the Default Branch for the main
// Worktree, which is on it.
function branchLine(p, w) {
  const branch = w.main ? p.defaultBranch : w.merge?.branch;
  if (!branch && !w.merge) {
    return "";
  }
  return h("span", { class: "br mono", title: branch ? `branch ${branch}` : "not on a branch" },
    icon("branch"), h("span", {}, branch || "not on a branch"));
}

// domains links the Worktree at its Primary Domain, then counts its other
// Domains (its own Domain and older Aliases), named in the title.
function domains(w) {
  const others = [w.domain, ...(w.aliases ?? [])].filter((d) => d && d !== w.primaryDomain);
  return [link(w.primaryDomain),
    others.length ? h("span", { class: "al", title: others.join("\n") }, `+${others.length} Domain${others.length === 1 ? "" : "s"}`) : ""];
}

// plugins are the badges and links of the Plugins the Worktree uses.
function plugins(w) {
  const items = [...pluginBadges(w), ...pluginLinks(w)];
  return items.length ? h("div", { class: "plugin-items" }, ...items) : "";
}

// readiness is where the Worktree is in Preparing, with the failed step.
function readiness(s) {
  if (!s) {
    return h("span", { class: "st off" }, h("i"), "-");
  }
  switch (s.state) {
    case "ready":
      return h("span", { class: "st ok" }, h("i"), "Ready");
    case "failed":
      return h("div", { class: "ready" }, h("span", { class: "st bad" }, h("i"), "Failed"),
        s.step ? h("span", { class: "step", title: `Preparing failed at ${s.step}` }, s.step) : "");
    case "preparing":
      return h("div", { class: "ready" }, h("span", { class: "st warn" }, h("i"), "Preparing"),
        s.step ? h("span", { class: "step", title: s.step }, s.step) : "");
    default:
      return h("span", { class: "st off" }, h("i"), s.state === "discovered" ? "Found" : s.state);
  }
}

// failure says which step of Preparing failed, why, and how to retry.
function failure(s) {
  return h("section", { class: "box failure" },
    h("h4", {}, "Preparing failed at ", h("code", {}, s.step || "an unknown step")),
    h("div", { class: "pad" },
      s.error ? h("p", {}, s.error) : "",
      s.output?.length ? h("pre", {}, s.output.join("\n")) : "",
      h("p", {}, "Fix it, then run ", h("code", {}, "lane worktree ready --retry"), " in the Worktree.")));
}

// mergeStatus is how the Worktree's work stands against the Default
// Branch.
function mergeStatus(w) {
  if (w.main) {
    return h("span", { class: "pill off" }, "Default Branch");
  }
  const m = w.merge;
  if (!m) {
    return "";
  }
  if (m.error) {
    return h("span", { class: "pill off", title: m.error }, "Unknown");
  }
  const pills = [];
  if (m.unmergedCount > 0) {
    pills.push(h("span", { class: "pill warn", title: "Commits not on the Default Branch" },
      icon("branch"), `${m.unmergedCount} unmerged commit${m.unmergedCount === 1 ? "" : "s"}`));
  }
  if (m.changedCount > 0) {
    pills.push(h("span", { class: "pill warn", title: "Uncommitted changes and untracked files" },
      icon("changed"), `${m.changedCount} changed file${m.changedCount === 1 ? "" : "s"}`));
  }
  if (pills.length === 0) {
    pills.push(h("span", { class: "pill ok" }, icon("check"), "Merged"));
  }
  return pills;
}

// changes lists linked Worktree w's changed files and its commits that
// aren't on the Default Branch, when it has any, each opening its diff. The
// event carries at most 50 of each, so the rest are counted.
function changes(dash, p, w) {
  const m = w.merge;
  if (w.main || !m || m.error || (m.changedCount === 0 && m.unmergedCount === 0)) {
    return "";
  }
  const boxes = [];
  if (m.changedCount > 0) {
    boxes.push(h("section", { class: "box" },
      h("h4", {}, "Changed files", h("span", { class: "c" }, String(m.changedCount))),
      changeList(m.changedFiles, m.changedCount, opens(fileRow, (f) => showDiff(dash, p, w, { path: f.path }), (f) => `Show the changes to ${f.path}`), "git status")));
  }
  if (m.unmergedCount > 0) {
    boxes.push(h("section", { class: "box" },
      h("h4", {}, p.defaultBranch ? ["Commits not on ", h("code", {}, p.defaultBranch)] : "Unmerged commits",
        h("span", { class: "c" }, String(m.unmergedCount))),
      changeList(m.unmergedCommits, m.unmergedCount, opens(commitRow, (c) => showDiff(dash, p, w, { commit: c }), (c) => `Show commit ${c.hash.slice(0, 7)}`), "git log")));
  }
  return h("div", { class: boxes.length === 2 ? "xin changes" : "xin changes one" }, ...boxes);
}

// opens makes row a button that calls open with its item.
function opens(row, open, label) {
  return (item) => {
    const b = h("button", { type: "button", class: "row-open", title: label(item) }, ...row(item));
    b.addEventListener("click", () => open(item));
    return b;
  };
}

// changeList lists items with row, then says how many of total it left out
// and which git command shows them all.
function changeList(items, total, row, command) {
  const l = rows(items ?? [], row, { className: "flat" });
  if (total > (items?.length ?? 0)) {
    l.append(h("div", { class: "row more" }, `and ${total - (items?.length ?? 0)} more: run `, h("code", {}, command), " in the Worktree to see them all"));
  }
  return l;
}

// processesOf is the Worktree's declared Processes, the Primary Process
// first and the other HTTP ones next, then any running one the config no
// longer declares, each with its state.
function processesOf(w) {
  const states = new Map((w.processes ?? []).map((pr) => [pr.name, pr.state]));
  const rank = (d) => (d.primary ? 0 : d.http ? 1 : 2);
  const declared = [...(w.declared ?? [])].sort((a, b) => rank(a) - rank(b));
  const names = new Set(declared.map((d) => d.name));
  return [...declared, ...(w.processes ?? []).filter((pr) => !names.has(pr.name)).map((pr) => ({ name: pr.name }))]
    .map((d) => ({ ...d, state: states.get(d.name) ?? "stopped" }));
}

// processDots are the Worktree's Processes as status dots.
function processDots(w) {
  return h("div", { class: "procs" }, ...processesOf(w).map((d) =>
    h("span", { class: stateTone(d.state), title: `${d.name}: ${d.state}` }, h("i"), d.name)));
}

// stateTone is the colour of a Process state.
function stateTone(state) {
  return { running: "ok", restarting: "warn", failed: "bad" }[state] ?? "off";
}

// plural is "1 <one>" or "<n> <many>".
function plural(n, one, many) {
  return `${n} ${n === 1 ? one : many}`;
}

function link(domain, cls = "") {
  if (!domain) {
    return h("span", { class: "muted" }, "-");
  }
  return h("a", { class: cls || false, href: `https://${domain}/`, target: "_blank", rel: "noopener" }, domain);
}
