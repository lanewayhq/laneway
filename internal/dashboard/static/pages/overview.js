// The Overview, at /. For now it lists the Linked Projects, each with its
// run state, Worktrees and how many failed preparing, linking to its page.
// It re-renders on every `projects` event.
import { h, pageHeader } from "../dom.js";
import { failedCount, projectHref, runState } from "../sidebar.js";

export default {
  id: "overview",
  path: "/",
  title: "Overview",
  show(el, dash) {
    dash.onProjects((list) => {
      const projects = list.projects;
      const worktrees = projects.reduce((n, p) => n + p.worktrees.length, 0);
      const header = pageHeader("Overview", [
        h("span", { class: "mono" }, location.hostname),
        plural(projects.length, "Project"),
        plural(worktrees, "Worktree"),
      ]);
      if (projects.length === 0) {
        el.replaceChildren(header, h("div", { class: "body" }, h("p", { class: "empty" },
          "No Projects are Linked. Run ", h("code", {}, "lane link"), " in a git repository.")));
        return;
      }
      el.replaceChildren(header, h("table", { class: "tbl" },
        h("thead", {}, h("tr", {}, ...["Project", "Domain", "Path", "Worktrees", "State"].map((c) => h("th", {}, c)))),
        h("tbody", {}, ...projects.map(row))));
    });
  },
};

function row(p) {
  const main = p.worktrees.find((w) => w.main);
  const failed = failedCount(p);
  const state = runState(p);
  return h("tr", { "data-project": p.name },
    h("td", {}, h("a", { class: "strong", href: projectHref(p.name) }, p.name)),
    h("td", {}, main?.primaryDomain
      ? h("a", { class: "domain", href: `https://${main.primaryDomain}/`, target: "_blank", rel: "noopener" }, main.primaryDomain)
      : "-"),
    h("td", { class: "mono muted" }, p.path),
    h("td", {}, String(p.worktrees.length), failed ? h("span", { class: "pill bad" }, `${failed} failed`) : ""),
    h("td", {}, h("span", { class: `st ${{ run: "ok", idle: "warn", stopped: "off" }[state]}` }, h("i"),
      { run: "Running", idle: "Idle", stopped: "Stopped" }[state])));
}

function plural(n, noun) {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}
