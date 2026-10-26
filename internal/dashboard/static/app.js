// The Dashboard's core. It owns the connection to the daemon and gives each
// panel a `dash` object; panels do the rendering.
//
// Adding a panel (e.g. live logs, Services):
//   1. Write panels/<name>.js whose default export is
//        { id: "<name>", title: "<Heading>", mount(el, dash) { … } }
//      mount runs once, with the panel's (empty) body element.
//   2. Add it to the list in panels/index.js, in display order.
//
// What a panel gets in `dash`:
//   dash.api.get(path) / dash.api.post(path, body)  JSON API under /api (same
//       routes as the CLI, see package api). post throws an Error carrying the
//       API's message.
//   dash.api.stream(path, onLine, signal)  NDJSON streaming routes such as
//       /v1/logs?follow=true, one parsed object per call.
//   dash.onProjects(fn)  fn(list) with every ProjectList from GET /v1/events:
//       the latest one at once if there is one, then each change.
//   dash.control(action, worktree, process)  POST /v1/worktree/{up|down|restart}.
//   dash.worktreeActions / dash.processActions  extra buttons the Worktrees
//       panel shows: push fn(worktree, project[, process]) returning
//       [{ label, run() }] (an empty array for none).
//   dash.on(name, fn) / dash.emit(name, detail)  events between panels, e.g. a
//       Process action asking the logs panel to show that Process.
//   dash.report(message)  shows a message (an error) at the top of the page.
import panels from "./panels/index.js";

const api = {
  async get(path) {
    return parse(await fetch("/api" + path, { headers: { Accept: "application/json" } }));
  },
  async post(path, body) {
    return parse(await fetch("/api" + path, {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify(body ?? {}),
    }));
  },
  async stream(path, onLine, signal) {
    const res = await fetch("/api" + path, { signal });
    if (!res.ok) {
      await parse(res);
    }
    const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) {
        return;
      }
      buf += value;
      let nl;
      while ((nl = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, nl);
        buf = buf.slice(nl + 1);
        if (line.trim() !== "") {
          onLine(JSON.parse(line));
        }
      }
    }
  },
};

// parse returns a response's JSON body, or throws the API's error message.
async function parse(res) {
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    throw new Error(body?.error?.message ?? `${res.status} ${res.statusText}`);
  }
  return body;
}

const listeners = new Map();
let projects = null;

const dash = {
  api,
  onProjects(fn) {
    dash.on("projects", fn);
    if (projects) {
      fn(projects);
    }
  },
  control(action, worktree, process) {
    return api.post(`/v1/worktree/${action}`, { dir: worktree.path, process: process ?? "" });
  },
  worktreeActions: [],
  processActions: [],
  on(name, fn) {
    if (!listeners.has(name)) {
      listeners.set(name, []);
    }
    listeners.get(name).push(fn);
  },
  emit(name, detail) {
    for (const fn of listeners.get(name) ?? []) {
      try {
        fn(detail);
      } catch (err) {
        console.error(err);
      }
    }
  },
  report(message) {
    const el = document.getElementById("report");
    el.textContent = message;
    el.hidden = !message;
  },
};

const connection = document.getElementById("connection");
const events = new EventSource("/api/v1/events");
events.addEventListener("open", () => {
  connection.textContent = "live";
  connection.dataset.state = "live";
});
events.addEventListener("error", () => {
  connection.textContent = "reconnecting…";
  connection.dataset.state = "down";
});
events.addEventListener("projects", (e) => {
  projects = JSON.parse(e.data);
  dash.emit("projects", projects);
});

const main = document.getElementById("panels");
for (const panel of panels) {
  const section = document.createElement("section");
  section.className = "panel";
  section.id = panel.id;
  const heading = document.createElement("h2");
  heading.textContent = panel.title;
  const body = document.createElement("div");
  section.append(heading, body);
  main.append(section);
  panel.mount(body, dash);
}
