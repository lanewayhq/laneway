// The Overview, at /, with no Project selected: totals of Projects and
// Worktrees, Processes running, Services and Orphans; what needs attention
// across every Project (failures first); the linked Worktrees that are ready
// to Remove; a card per Project; how other devices reach Laneway (the Root
// Domain, the addresses it listens on, and a QR code for the CA page); the
// Services with their state and port; and links: each Service's own links
// (from the Plugin that adds its Engine), the guides and how to Link a
// Project. It re-renders on every `projects`, `services`, `orphans` and
// `daemon` event, and each minute for the relative times.
//
// Which Worktrees need attention or are ready to Remove is attention.js's
// call. A Worktree ready to Remove gets a Remove button
// (dash.removeWorktree). With two or more, its header has "Remove all N"
// (remove.js's removeAll), which asks once and removes exactly the
// Worktrees it listed; with one, that row's own Remove is the way, with the
// full Remove dialog.
import { h, pageHeader } from "../dom.js";
import { icon } from "../icons.js";
import { href as pluginHref } from "../pluginitems.js";
import { projectHref, runningProcesses, runs, runState, runStateLabels, worktreeName } from "../projects.js";
import { relative, absolute } from "../time.js";
import { failedCount, needsAttention, readyToRemove } from "../attention.js";
import { removeAll } from "../remove.js";

// guidesURL is where the guides are: the Dashboard serves none itself.
const guidesURL = "https://github.com/lanewayhq/laneway/tree/main/docs/guides";

export default {
  id: "overview",
  path: "/",
  title: "Overview",
  show(el, dash) {
    const data = { projects: null, services: null, orphans: null, daemon: null };
    const shown = new Set(); // Needs attention rows whose full list is shown
    const removing = new Set(); // paths of Worktrees whose Remove is in progress
    let removedAll = null; // what the last "Remove all N" did: { removed, total, orphans }

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
            h("div", { class: "ov-stack" }, readyCard(projects), remoteCard(data.daemon), servicesCard(data.services), linksCard(data.services)))));
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
          n === 1 ? `Worktree ${worktreeName(worktrees[0])} failed preparing` : `${n} Worktrees failed preparing`,
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
            h("a", { href: projectHref(project.name, worktree) }, worktreeName(worktree)),
            h("span", { class: "muted" }, ` ${project.name} · ${detail(worktree)}`)))) : ""),
        toggle);
    };

    const readyCard = (projects) => {
      const items = readyToRemove(projects);
      const actions = items.length >= 2 ? [removeAllButton(items)] : [];
      return card("Ready to remove", h("span", { class: "c" }, String(items.length)), actions, items.length
        ? [removedAllNote(),
          h("p", { class: "note" }, "Merged into their Default Branch, with no uncommitted changes."),
          ...items.map(({ project, worktree }) => h("div", { class: "ov-row", "data-worktree": worktree.path },
            h("div", { class: "t" }, h("b", {}, worktree.name),
              h("div", { class: "sub" }, project.name, " · ", h("span", { class: "mono" }, worktree.merge.branch ?? "not on a branch"))),
            removeButton(project, worktree)))]
        : [removedAllNote(),
          h("p", { class: "note empty" }, "No linked Worktree is merged into its Default Branch with no uncommitted changes.")]);
    };

    // removeAllButton asks once about exactly the Worktrees items lists now,
    // and removes them.
    const removeAllButton = (items) => {
      const b = h("button", { type: "button", class: "danger", "data-action": "remove-all" }, `Remove all ${items.length}`);
      b.addEventListener("click", async () => {
        removedAll = null;
        const outcome = await removeAll(dash, items);
        if (outcome) {
          removedAll = outcome;
          render();
        }
      });
      return b;
    };

    // removedAllNote says what the last "Remove all N" did.
    const removedAllNote = () => {
      if (!removedAll) {
        return "";
      }
      const { removed, total, orphans } = removedAll;
      return h("p", { class: "note done", role: "status" },
        removed === total ? `Removed ${plural(removed, "Worktree")}.` : `Removed ${removed} of ${plural(total, "Worktree")}; the rest were skipped.`,
        orphans ? [" Their databases and Buckets are ", h("a", { href: "/orphans" }, "Orphans"), " now, dropped in 24 hours."] : "");
    };

    const removeButton = (project, worktree) => {
      const busy = removing.has(worktree.path);
      const b = h("button", { type: "button", class: "ov-ib del", title: "Remove Worktree", "aria-label": `Remove ${worktree.name}`, disabled: busy },
        icon("remove"));
      b.addEventListener("click", async () => {
        removing.add(worktree.path);
        render();
        try {
          await dash.removeWorktree(worktree, project);
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
    dash.onDaemon((info) => {
      data.daemon = info;
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
  const running = p.worktrees.filter(runs).length;
  return h("a", { class: "ov-pc", href: projectHref(p.name), "data-project": p.name },
    h("div", { class: "top" },
      h("span", { class: `dot ${state}`, title: runStateLabels[state] }),
      h("b", {}, p.name),
      failed ? h("span", { class: "bad" }, `${failed} failed`) : ""),
    h("span", { class: "domain mono" }, main?.primaryDomain || "no Domain"),
    h("span", { class: "path mono", title: p.path }, p.path),
    h("div", { class: "foot" },
      h("span", {}, plural(p.worktrees.length, "Worktree")),
      h("span", {}, running ? `${running} running` : { idle: "Idle", stopped: "Stopped" }[state]),
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

// remoteCard says how other devices reach Laneway, from the daemon's status
// (info): the Root Domain, loopback and each address listen names with its
// state, and, unless the Root Domain is localhost, which no other device
// reaches, a QR code that opens the CA page on a phone.
function remoteCard(info) {
  if (!info) {
    return "";
  }
  const local = info.rootDomain === "localhost";
  return card("Remote access", "", [], [
    h("div", { class: "ov-addr" },
      h("div", { class: "t" }, "Root Domain", h("div", { class: "sub mono" }, info.rootDomain))),
    local
      ? h("p", { class: "note" }, "Only this PC reaches ", h("code", {}, "localhost"), ". For your other devices, set a Root Domain you own with ",
        h("code", {}, "lane root-domain set"), ".")
      : "",
    addressRow(info.proxyAddr.replace(/:\d+$/, ""), "this PC only", "ok", "listening", ""),
    ...(info.listen ?? []).map((a) => addressRow(a.name, a.ip && a.ip !== a.name ? a.ip : "",
      a.listening ? (a.error ? "warn" : "ok") : "bad", a.listening ? "listening" : "not listening", a.error ?? "")),
    info.listenError
      ? h("div", { class: "ov-addr" }, h("div", { class: "t" }, h("span", { class: "bad" }, "Can't read listen"), h("div", { class: "sub wrap" }, info.listenError)))
      : "",
    local ? "" : h("div", { class: "ov-qr" },
      qrImage(info.caPage),
      h("div", { class: "t" },
        h("b", {}, "Trust Laneway on your phone"),
        h("div", { class: "sub wrap" }, "Scan to open the page that installs Laneway's root certificate, or open ",
          h("a", { class: "mono", href: info.caPage, target: "_blank", rel: "noopener" }, info.caPage), " on the device."))),
  ]);
}

// addressRow is one address Laneway listens on (or should): its name, what
// it resolved to, its state (tone: ok, warn or bad) and why it isn't
// fully listening.
function addressRow(name, detail, tone, state, error) {
  return h("div", { class: "ov-addr", "data-address": name },
    h("div", { class: "t" },
      h("span", { class: "mono" }, name), detail ? h("span", { class: "muted" }, ` · ${detail}`) : "",
      error ? h("div", { class: "sub wrap" }, error) : ""),
    h("span", { class: `st ${tone}` }, h("i"), state));
}

// qrImage is the QR code for the CA page at url, which the daemon draws. The
// same element is kept while url stays, so re-rendering doesn't fetch it
// again.
let qr = { url: "", img: null };
function qrImage(url) {
  if (qr.url !== url) {
    qr = { url, img: h("img", { class: "code", src: "/ca/qr.svg", alt: `QR code for ${url}`, width: 132, height: 132 }) };
  }
  return qr.img;
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
      parts.push(...list.map(({ worktree }) => h("a", { href: projectHref(name, worktree), title: `${worktreeName(worktree)} in ${name}` },
        `${worktreeName(worktree)} (${detail(worktree)})`)));
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
