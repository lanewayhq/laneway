// The sidebar: the Overview, every Project (its run state, how many
// Worktrees it has and how many failed preparing), then the Logs,
// Services, Orphans and Plugins pages with their counts. It marks the page
// shown, and "Find a Project…" narrows the list of Projects (Enter opens
// the first one left).
import { h } from "./dom.js";
import { icon } from "./icons.js";

export function mountSidebar(el, dash) {
  const find = h("input", { type: "search", placeholder: "Find a Project…", "aria-label": "Find a Project", autocomplete: "off" });
  const nav = h("nav", { "aria-label": "Dashboard" });
  el.append(h("label", { class: "filter find" }, icon("search"), find), nav);

  const counts = {}; // page id -> its count, once known
  let projects = null;

  const item = (href, current, ...children) =>
    h("a", { class: current ? "it on" : "it", href, "aria-current": current ? "page" : false }, ...children);
  const count = (n) => (n === undefined ? "" : h("span", { class: "c" }, String(n)));

  const render = () => {
    const route = dash.route;
    const on = (id) => route?.page === id;
    const query = find.value.trim().toLowerCase();
    const shown = (projects ?? []).filter((p) => p.name.toLowerCase().includes(query));
    nav.replaceChildren(
      item("/", on("overview"), icon("overview"), h("span", { class: "name" }, "Overview")),
      h("div", { class: "lbl" }, "Projects"),
      ...shown.map((p) => {
        const state = runState(p);
        const failed = failedCount(p);
        return item(projectHref(p.name), on("project") && route.params.name === p.name,
          h("span", { class: `dot ${state}`, title: { run: "running", idle: "idle", stopped: "stopped" }[state] }),
          h("span", { class: "name" }, p.name),
          failed ? h("span", { class: "bad" }, `${failed} failed`) : "",
          count(p.worktrees.length));
      }),
      projects && shown.length === 0
        ? h("p", { class: "none" }, projects.length === 0 ? "No Projects are Linked." : "No Project matches.")
        : "",
      h("div", { class: "sep" }),
      item("/logs", on("logs"), icon("logs"), h("span", { class: "name" }, "Logs")),
      item("/services", on("services"), icon("services"), h("span", { class: "name" }, "Services"), count(counts.services)),
      item("/orphans", on("orphans"), icon("orphans"), h("span", { class: "name" }, "Orphans"), count(counts.orphans)),
      item("/plugins", on("plugins"), icon("plugins"), h("span", { class: "name" }, "Plugins"), count(counts.plugins)));
  };

  find.addEventListener("input", render);
  find.addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      const first = nav.querySelector('a[href^="/projects/"]');
      if (first) {
        dash.navigate(first.getAttribute("href"));
      }
    } else if (e.key === "Escape") {
      find.value = "";
      render();
    }
  });

  dash.onProjects((list) => {
    projects = list.projects;
    render();
  });
  dash.onServices((list) => {
    counts.services = list.services.length;
    render();
  });
  dash.onOrphans((list) => {
    counts.orphans = list.orphans.length;
    render();
  });
  dash.onPlugins((list) => {
    counts.plugins = list.plugins.length;
    render();
  });
  dash.on("route", render);
  render();
}

// projectHref is the path of Project name's page.
export function projectHref(name) {
  return "/projects/" + encodeURIComponent(name);
}

// runState is "run" when any of Project p's Worktrees is running or
// starting, else "idle" when any is idle (stopped after its idle timeout,
// started again by the next request), else "stopped".
export function runState(p) {
  const states = new Set(p.worktrees.map((w) => w.state));
  if (states.has("running") || states.has("starting")) {
    return "run";
  }
  return states.has("idle") ? "idle" : "stopped";
}

// failedCount is how many of Project p's Worktrees failed preparing.
export function failedCount(p) {
  return p.worktrees.filter((w) => w.setup?.state === "failed").length;
}
