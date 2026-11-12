// The Logs page, at /logs?dir=<Worktree path>[&process=<name>]: one
// Process's output, or all of a Worktree's Processes together labelled by
// name, followed live. It shows the recent output the daemon kept, then
// streams new lines from GET /v1/logs?follow=true. Without dir it lists the
// Worktrees to choose from.
//
// It adds a "Logs" button to every Worktree and Process on Project pages,
// which emit a `logs` event ({ worktree, process? }); the Dashboard then
// shows this page for them. Link to it with logsHref.
import { h, pageHeader } from "../dom.js";
import { projectHref } from "../sidebar.js";

// maxLines matches the daemon's kept output per Process
// (supervisor.RecentLines); older lines are dropped from the page.
const maxLines = 1000;

// colors is how many distinct process label colours style.css defines.
const colors = 6;

// ansi matches terminal escape sequences (colours, cursor moves), which
// tools like Vite write even into a pipe.
const ansi = /\x1b\[[0-9;?]*[ -\/]*[@-~]/g;

// logsHref is the path of the Logs page following the Worktree at dir
// (all its Processes, or just process).
export function logsHref(dir, process) {
  const query = new URLSearchParams({ dir });
  if (process) {
    query.set("process", process);
  }
  return "/logs?" + query;
}

export default {
  id: "logs",
  path: "/logs",
  title: "Logs",
  register(dash) {
    dash.worktreeActions.push((w) => [{ label: "Logs", run: () => dash.emit("logs", { worktree: w }) }]);
    dash.processActions.push((w, p, proc) => [
      { label: "Logs", run: () => dash.emit("logs", { worktree: w, process: proc.name }) },
    ]);
    dash.on("logs", ({ worktree, process }) => dash.navigate(logsHref(worktree.path, process)));
  },
  show(el, dash, route) {
    const dir = route.query.get("dir");
    if (!dir) {
      choose(el, dash);
      return;
    }
    follow(el, dash, dir, route.query.get("process") ?? "");
  },
};

// choose lists every Worktree, each linking to its logs.
function choose(el, dash) {
  dash.onProjects((list) => {
    const header = pageHeader("Logs", ["Choose a Worktree to follow its Processes' output."]);
    if (list.projects.length === 0) {
      el.replaceChildren(header, h("div", { class: "body" }, h("p", { class: "empty" },
        "No Projects are Linked. Run ", h("code", {}, "lane link"), " in a git repository.")));
      return;
    }
    el.replaceChildren(header, h("table", { class: "tbl" },
      h("thead", {}, h("tr", {}, ...["Worktree", "Project", "Domain"].map((c) => h("th", {}, c)))),
      h("tbody", {}, ...list.projects.flatMap((p) => p.worktrees.map((w) => h("tr", {},
        h("td", {}, h("a", { class: "strong", href: logsHref(w.path) }, w.main ? "main" : w.name)),
        h("td", {}, p.name),
        h("td", { class: "muted" }, w.primaryDomain ?? w.path)))))));
  });
}

// follow shows the output of the Worktree at dir, all its Processes or
// just process, until the page is left.
function follow(el, dash, dir, process) {
  const all = !process;
  const head = h("div", { class: "logs-head" });
  const status = h("span", { class: "status" }, "connecting…");
  const lines = h("div", { class: "lines", role: "log", "aria-live": "off", tabindex: "0" });
  el.replaceChildren(head, h("div", { class: "body" }, h("div", { class: "logs-bar" }, status), lines));

  // The header names the Worktree and offers its Processes once the
  // projects event says what they are.
  dash.onProjects((list) => {
    let project = null;
    let worktree = null;
    for (const p of list.projects) {
      const w = p.worktrees.find((w) => w.path === dir);
      if (w) {
        [project, worktree] = [p, w];
      }
    }
    const names = [...new Set([...(worktree?.declared ?? []).map((d) => d.name),
      ...(worktree?.processes ?? []).map((pr) => pr.name), ...(process ? [process] : [])])].sort();
    const tab = (label, name) => h("a", { class: name === process ? "tab on" : "tab", href: logsHref(dir, name),
      "aria-current": name === process ? "page" : false }, label);
    head.replaceChildren(
      pageHeader("Logs",
        [project ? h("a", { href: projectHref(project.name) }, project.name) : "",
          worktree ? (worktree.main ? "main" : worktree.name) : "",
          h("span", { class: "mono" }, worktree?.primaryDomain ?? dir)]),
      h("nav", { class: "tabs", "aria-label": "Processes" }, tab("All Processes", ""), ...names.map((n) => tab(n, n))));
  });

  const labels = new Map(); // Process name -> colour index
  const append = (line) => {
    const atBottom = lines.scrollHeight - lines.scrollTop - lines.clientHeight < 4;
    if (!labels.has(line.process)) {
      labels.set(line.process, labels.size % colors);
    }
    lines.append(h("div", { class: "line", title: new Date(line.time).toLocaleString() },
      all ? h("span", { class: "process", "data-color": labels.get(line.process) }, line.process) : "",
      h("span", { class: "text" }, line.text.replace(ansi, ""))));
    while (lines.childElementCount > maxLines) {
      lines.firstElementChild.remove();
    }
    if (atBottom) {
      lines.scrollTop = lines.scrollHeight;
    }
  };

  const query = new URLSearchParams({ dir, follow: "true" });
  if (process) {
    query.set("process", process);
  }
  let retry = null; // the timer that reopens a stream that ended
  dash.signal.addEventListener("abort", () => clearTimeout(retry));
  const open = async () => {
    // A reopened stream starts again from the kept output.
    lines.replaceChildren();
    status.textContent = "live";
    status.dataset.state = "live";
    try {
      await dash.api.stream("/v1/logs?" + query, append, dash.signal);
    } catch (err) {
      if (dash.signal.aborted) {
        return;
      }
      if (!(err instanceof TypeError)) {
        // The daemon answered with an error (the Process is gone, the
        // Project was Unlinked): reopening wouldn't help.
        status.textContent = err.message;
        status.dataset.state = "down";
        return;
      }
    }
    if (dash.signal.aborted) {
      return;
    }
    // The stream ended (the daemon stopped or restarted): try again, as
    // the live events do.
    status.textContent = "reconnecting…";
    status.dataset.state = "down";
    retry = setTimeout(open, 2000);
  };
  open();
}
