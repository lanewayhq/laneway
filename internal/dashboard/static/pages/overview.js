// The Overview, at /, with no Project selected: totals of Projects and
// Worktrees, Processes running, Services and Orphans; what needs attention
// across every Project (failures first); the linked Worktrees that are ready
// to Remove; a card per Project; the Services with their state and port; and
// links: each Service's own links (from the Plugin that adds its Engine), the
// guides and how to Link a Project. It re-renders on every `projects`,
// `services` and `orphans` event, and each minute for the relative times.
//
// Which Worktrees need attention or are ready to Remove is attention.js's
// call. A Worktree ready to Remove gets a Remove button when another page
// provides dash.removeWorktree(worktree, project) (a Promise); the Ready to
// remove card's header has room for actions on the whole list.
import { h, pageHeader } from "../dom.js";
import { icon } from "../icons.js";
import { href as pluginHref } from "../pluginitems.js";
import { failedCount, projectHref, runState } from "../sidebar.js";
import { relative, absolute } from "../time.js";
import { needsAttention, readyToRemove } from "../attention.js";

// guidesURL is where the guides are: the Dashboard serves none itself.
const guidesURL = "https://github.com/lanewayhq/laneway/tree/main/docs/guides";

export default {
  id: "overview",
  path: "/",
  title: "Overview",
  show(el, dash) {
    const data = { projects: null, services: null, orphans: null };
    const shown = new Set(); // Needs attention rows whose full list is shown
    const removing = new Set(); // paths of Worktrees whose Remove is in progress

    const render = () => {
      if (!data.projects) {
        return;
      }
      const projects = data.projects;
      const worktrees = projects.reduce((n, p) => n + p.worktrees.length, 0);
      el.replaceChildren(
        pageHeader("Overview", [
          h("span", { class: "mono" }, location.hostname),
          plural(projects.length, "Project"),
          plural(worktrees, "Worktree"),
        ], [h("a", { class: "btn", href: "/logs" }, icon("logs"), "All logs")]),
        h("div", { class: "ov" },
          totals(data),
          h("div", { class: "ov-cols" },
            h("div", { class: "ov-stack" }, attentionCard(projects), projectsCard(projects)),
            h("div", { class: "ov-stack" }, readyCard(projects), servicesCard(data.services), linksCard(data.services)))));
    };

    // attentionCard lists failures grouped by Project, then Worktrees with
    // uncommitted changes, then those with unmerged commits.
    const attentionCard = (projects) => {
      const a = needsAttention(projects);
      const rows = [
        ...a.failed.map(failedRow),
        a.changed.length ? listRow("changed", "changed", a.changed, "uncommitted changes", changedDetail) : "",
        a.unmerged.length ? listRow("unmerged", "commit", a.unmerged, "commits not on their Default Branch", unmergedDetail) : "",
      ];
      return card("Needs attention", a.count ? h("span", { class: "c bad" }, plural(a.count, "Worktree")) : "", [],
        a.count ? rows : [h("div", { class: "ov-row" }, h("span", { class: "ov-ico ok" }, icon("check")),
          h("div", { class: "t" }, "Nothing needs attention: no Worktree failed preparing or has work that isn't on its Default Branch."))]);
    };

    const failedRow = ({ project, worktrees, steps }) => {
      const n = worktrees.length;
      return h("div", { class: "ov-row" },
        h("span", { class: "ov-ico bad" }, icon("failed")),
        h("div", { class: "t" },
          h("b", {}, project.name), ": ",
          n === 1 ? `Worktree ${worktreeName(project, worktrees[0])} failed preparing` : `${n} Worktrees failed preparing`,
          h("div", { class: "sub wrap" },
            steps.length ? ["at ", ...steps.flatMap((s, i) => (i ? [", ", h("code", {}, s)] : [h("code", {}, s)])), ". "] : "",
            "Fix it, then run ", h("code", {}, "lane worktree ready --retry"), n === 1 ? " in it." : " in each.")),
        h("a", { class: "go", href: projectHref(project.name) }, "Open"));
    };

    // listRow is one row for every Worktree in items ({ project, worktree }),
    // summed up by Project, with Show for the whole list.
    const listRow = (key, iconName, items, what, detail) => {
      const open = shown.has(key);
      const toggle = h("button", { type: "button", class: "go", "aria-expanded": String(open) }, open ? "Hide" : "Show");
      toggle.addEventListener("click", () => {
        open ? shown.delete(key) : shown.add(key);
        render();
      });
      const n = items.length;
      return h("div", { class: "ov-row top" },
        h("span", { class: "ov-ico warn" }, icon(iconName)),
        h("div", { class: "t" },
          h("b", {}, plural(n, "Worktree")), n === 1 ? ` has ${what}` : ` have ${what}`,
          h("div", { class: "sub" }, ...summary(items, detail)),
          open ? h("ul", { class: "ov-list" }, ...items.map(({ project, worktree }) => h("li", {},
            h("a", { href: projectHref(project.name) }, worktree.name),
            h("span", { class: "muted" }, ` ${project.name} · ${detail(worktree)}`)))) : ""),
        toggle);
    };

    const readyCard = (projects) => {
      const items = readyToRemove(projects);
      const remove = typeof dash.removeWorktree === "function" ? dash.removeWorktree : null;
      const actions = []; // actions on the whole list go here
      return card("Ready to remove", h("span", { class: "c" }, String(items.length)), actions, items.length
        ? [h("p", { class: "note" }, "Merged into their Default Branch, with no uncommitted changes."),
          ...items.map(({ project, worktree }) => h("div", { class: "ov-row", "data-worktree": worktree.path },
            h("div", { class: "t" }, h("b", {}, worktree.name),
              h("div", { class: "sub" }, project.name, " · ", h("span", { class: "mono" }, worktree.merge.branch ?? "not on a branch"))),
            remove ? removeButton(remove, project, worktree) : ""))]
        : [h("p", { class: "note empty" }, "No linked Worktree is merged into its Default Branch with no uncommitted changes.")]);
    };

    const removeButton = (remove, project, worktree) => {
      const busy = removing.has(worktree.path);
      const b = h("button", { type: "button", class: "ov-ib del", title: "Remove Worktree", "aria-label": `Remove ${worktree.name}`, disabled: busy },
        icon("remove"));
      b.addEventListener("click", async () => {
        removing.add(worktree.path);
        render();
        try {
          await remove(worktree, project);
        } catch (err) {
          dash.report(err.message);
        } finally {
          removing.delete(worktree.path);
          render();
        }
      });
      return b;
    };

    dash.onProjects((list) => {
      data.projects = list.projects;
      render();
    });
    dash.onServices((list) => {
      data.services = list.services;
      render();
    });
    dash.onOrphans((list) => {
      data.orphans = list.orphans;
      render();
    });
    const timer = setInterval(render, 60_000);
    dash.signal.addEventListener("abort", () => clearInterval(timer));
  },
};

// totals are the four numbers across the top.
function totals({ projects, services, orphans }) {
  const all = projects.flatMap((p) => p.worktrees);
  const failed = projects.reduce((n, p) => n + failedCount(p), 0);
  const running = projects.map((p) => ({ p, n: runningProcesses(p) })).filter((x) => x.n > 0);
  const processes = running.reduce((n, x) => n + x.n, 0);
  const idle = all.filter((w) => w.state === "idle").length;

  return h("div", { class: "ov-stats" },
    stat("Projects", String(projects.length), "",
      [h("b", {}, String(all.length)), all.length === 1 ? " Worktree" : " Worktrees",
        failed ? [" · ", h("span", { class: "bad" }, `${failed} failed`)] : ""]),
    stat("Processes running", String(processes), "",
      [running.length ? `in ${running.map((x) => x.p.name).join(", ")}` : "none running",
        idle ? ` · ${plural(idle, "Worktree")} Idle` : ""]),
    services
      ? stat("Services", String(services.filter((s) => s.state === "running").length),
        services.length ? ` / ${services.length} running` : "", servicesSummary(services))
      : stat("Services", "–", "", ""),
    orphans
      ? stat("Orphans", String(orphans.length), "", nextDrop(orphans))
      : stat("Orphans", "–", "", ""));
}

function stat(label, value, of, sub) {
  return h("div", { class: "ov-stat" },
    h("div", { class: "k" }, label),
    h("div", { class: "v" }, value, of ? h("span", { class: "of" }, of) : ""),
    h("div", { class: "s" }, ...[sub].flat(Infinity)));
}

// runningProcesses is how many of Project p's Processes are running.
function runningProcesses(p) {
  return p.worktrees.reduce((n, w) => n + (w.processes ?? []).filter((pr) => pr.state === "running").length, 0);
}

// servicesSummary counts the Services that aren't running by state.
function servicesSummary(services) {
  if (!services.length) {
    return "none declared";
  }
  const counts = new Map();
  for (const s of services) {
    if (s.state !== "running") {
      counts.set(s.state, (counts.get(s.state) ?? 0) + 1);
    }
  }
  const order = ["starting", "idle", "stopped"];
  const states = [...counts.keys()].sort((a, b) => order.indexOf(a) - order.indexOf(b));
  return states.length ? states.map((s) => `${counts.get(s)} ${s === "idle" ? "Idle" : s}`).join(" · ") : "all running";
}

// nextDrop says when the daemon drops the next Orphan.
function nextDrop(orphans) {
  const times = orphans.filter((o) => o.expires).map((o) => new Date(o.expires));
  if (!times.length) {
    return orphans.length ? "dropped by lane gc" : "none waiting to be dropped";
  }
  const next = new Date(Math.min(...times));
  return h("span", { title: absolute(next) }, `next one dropped ${relative(next)}`);
}

function projectsCard(projects) {
  return card("Projects", h("span", { class: "c" }, String(projects.length)),
    [h("span", { class: "hint" }, "Link another with ", h("code", {}, "lane link"))],
    projects.length
      ? [h("div", { class: "ov-grid" }, ...projects.map(projectCard))]
      : [h("p", { class: "note empty" }, "No Projects are Linked. Run ", h("code", {}, "lane link"), " in a git repository.")]);
}

function projectCard(p) {
  const main = p.worktrees.find((w) => w.main);
  const state = runState(p);
  const failed = failedCount(p);
  const running = p.worktrees.filter((w) => w.state === "running").length;
  return h("a", { class: "ov-pc", href: projectHref(p.name), "data-project": p.name },
    h("div", { class: "top" },
      h("span", { class: `dot ${state}`, title: { run: "running", idle: "Idle", stopped: "stopped" }[state] }),
      h("b", {}, p.name),
      failed ? h("span", { class: "bad" }, `${failed} failed`) : ""),
    h("span", { class: "domain mono" }, main?.primaryDomain || "no Domain"),
    h("span", { class: "path mono", title: p.path }, p.path),
    h("div", { class: "foot" },
      h("span", {}, plural(p.worktrees.length, "Worktree")),
      h("span", {}, running ? `${running} running` : { run: "Starting", idle: "Idle", stopped: "Stopped" }[state]),
      ...(p.badges ?? []).map((b) => h("span", { title: b.title || `from Plugin ${b.plugin}` }, b.text))));
}

function servicesCard(services) {
  const body = !services
    ? []
    : services.length
      ? services.map((s) => h("a", { class: "ov-svc", href: "/services", "data-service": s.name, title: s.error || false },
        h("span", { class: "mono" }, s.name),
        h("span", { class: `st ${s.error ? "bad" : serviceTone(s.state)}` }, h("i"), s.state === "idle" ? "Idle" : s.state),
        h("span", { class: "port" }, String(s.port))))
      : [h("p", { class: "note empty" }, "No Services yet. Declare one in ", h("code", {}, "laneway.toml"), ".")];
  return card("Services", services ? h("span", { class: "c" }, String(services.length)) : "",
    [h("a", { class: "more", href: "/services" }, "Details")], body);
}

function serviceTone(state) {
  return { running: "ok", starting: "warn", idle: "warn" }[state] ?? "off";
}

// linksCard has the links the Services' Plugins give them (such as an
// inbox or a console), then the guides and how to Link a Project.
function linksCard(services) {
  const seen = new Set();
  const links = [];
  for (const s of services ?? []) {
    for (const l of s.links ?? []) {
      const url = pluginHref(l.url);
      if (!seen.has(url)) {
        seen.add(url);
        links.push({ ...l, url, service: s.name });
      }
    }
  }
  return card("Links", "", [], [
    ...links.map((l) => h("a", { class: "ov-lnk", href: l.url, target: "_blank", rel: "noopener", title: `from Plugin ${l.plugin} for ${l.service}` },
      icon("services"), h("span", {}, l.label, h("span", { class: "from" }, l.plugin)), h("span", { class: "sub mono" }, host(l.url)))),
    h("a", { class: "ov-lnk", href: guidesURL, target: "_blank", rel: "noopener" },
      icon("guides"), "Guides", h("span", { class: "sub" }, "on GitHub")),
    h("div", { class: "ov-lnk" },
      icon("logs"), "Link a Project", h("span", { class: "sub" }, h("code", {}, "lane link"), " in a git repository")),
  ]);
}

// card is a titled card: title, a count after it, actions at the end of
// its header, then rows.
function card(title, count, actions, rows) {
  return h("section", { class: "ov-card" },
    h("h2", {}, h("span", {}, title), count, actions.length ? h("span", { class: "end" }, ...actions) : ""),
    ...rows);
}

// summary sums items ({ project, worktree }) up by Project: the Worktrees'
// names when a Project has one or two, else how many, each linking to the
// Project's page.
function summary(items, detail) {
  const byProject = new Map();
  for (const item of items) {
    const list = byProject.get(item.project.name) ?? [];
    list.push(item);
    byProject.set(item.project.name, list);
  }
  const parts = [];
  for (const [name, list] of byProject) {
    if (list.length <= 2) {
      parts.push(...list.map(({ worktree }) => h("a", { href: projectHref(name), title: `${worktree.name} in ${name}` },
        `${worktree.name} (${detail(worktree)})`)));
    } else {
      parts.push(h("a", { href: projectHref(name) }, `${list.length} in ${name}`));
    }
  }
  return parts.flatMap((p, i) => (i ? [" · ", p] : [p]));
}

function changedDetail(w) {
  return plural(w.merge.changedCount, "file");
}

function unmergedDetail(w) {
  const commits = plural(w.merge.unmergedCount, "commit");
  return w.merge.branch ? commits : `${commits}, not on a branch`;
}

function worktreeName(project, w) {
  return w.main ? `${project.name} (main)` : w.name;
}

function host(url) {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

function plural(n, noun) {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}
