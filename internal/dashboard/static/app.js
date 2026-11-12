// The Dashboard's core. It owns the connection to the daemon, draws the
// sidebar (sidebar.js) and shows the page the URL names (router.js); pages
// do the rendering.
//
// Adding a page (pages/services.js is a small example):
//   1. Write pages/<name>.js whose default export is
//        { id: "<name>", path: "/<name>", title: "<Heading>",
//          register(dash) { … },            // optional
//          show(el, dash, route) { … } }
//      path may have parameters, as in "/projects/:name". register runs
//      once when the Dashboard opens, whichever page is shown: it is for
//      what other pages use, such as dash.worktreeActions. show runs each
//      time the page is shown, with its (empty) element and the route
//      ({ path, params, query }). title may be a function of the route.
//   2. Add it to the list in pages/index.js, and to the sidebar
//      (sidebar.js) if it has an entry there.
//
// What a page gets in `dash`:
//   dash.api.get(path) / dash.api.post(path, body) / dash.api.del(path)  JSON
//       API under /api (same routes as the CLI, see package api). They throw
//       an Error carrying the API's message, and its code as err.code.
//   dash.api.stream(path, onLine, signal)  NDJSON streaming routes such as
//       /v1/logs?follow=true, one parsed object per call.
//   dash.onProjects(fn)  fn(list) with every ProjectList from GET /v1/events:
//       the latest one at once if there is one, then each change.
//   dash.onServices(fn) / dash.onOrphans(fn)  the same for the ServiceList and
//       OrphanList (the `services` and `orphans` events).
//   dash.onPlugins(fn)  the same for GET /v1/plugins, read when the
//       Dashboard opens and again, at most once a minute, on a `projects`
//       event (Linking can fetch Plugins).
//   dash.control(action, worktree, process)  POST /v1/worktree/{up|down|restart}.
//   dash.worktreeActions / dash.processActions  extra buttons the Project
//       page shows: push fn(worktree, project[, process]) returning
//       [{ label, run() }] (an empty array for none), from register.
//   dash.on(name, fn) / dash.emit(name, detail)  events between pages, e.g. a
//       Process action asking for that Process's logs (`logs`), and `route`
//       when the page shown changes.
//   dash.navigate(href)  shows the page at href (a path such as
//       "/logs?dir=…"), as following a link to it does.
//   dash.route  the route shown: { page, path, params, query }.
//   dash.signal  in show, an AbortSignal that aborts when the page is left.
//       Everything subscribed through that page's dash (dash.on and the
//       dash.on* above) stops then by itself; streams and timers should
//       use the signal.
//   dash.report(message)  shows a message (an error) at the top of the page.
//   dash.confirm({ title, body, action })  asks in a modal dialog before a
//       destructive action; resolves true only if `action` was chosen. With
//       run, it also does the action in the dialog (see confirm.js).
//   dash.removeWorktree(worktree, project)  Removes a linked Worktree after
//       the Remove dialog (remove.js) shows what it would lose and the
//       developer confirms. Resolves true only if it was removed; errors are
//       shown, never thrown. The row then goes with the `projects` event.
import pages from "./pages/index.js";
import { confirm } from "./confirm.js";
import { removeWorktree } from "./remove.js";
import { createRouter } from "./router.js";
import { mountSidebar } from "./sidebar.js";

const api = {
  async get(path) {
    return parse(await fetch("/api" + path, { headers: { Accept: "application/json" } }));
  },
  async del(path) {
    return parse(await fetch("/api" + path, { method: "DELETE", headers: { Accept: "application/json" } }));
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

// parse returns a response's JSON body, or throws an Error with the API's
// message and its code (such as "worktree_changed") as err.code.
async function parse(res) {
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    throw Object.assign(new Error(body?.error?.message ?? `${res.status} ${res.statusText}`), { code: body?.error?.code ?? "" });
  }
  return body;
}

const listeners = new Map(); // event name -> Set of fns
const latest = new Map(); // live event name -> its latest data

// listen calls fn on each `name` event until signal (if any) aborts.
function listen(name, fn, signal) {
  if (signal?.aborted) {
    return () => {};
  }
  if (!listeners.has(name)) {
    listeners.set(name, new Set());
  }
  const set = listeners.get(name);
  const entry = (detail) => fn(detail); // its own identity, so fn can be added twice
  set.add(entry);
  const off = () => set.delete(entry);
  signal?.addEventListener("abort", off, { once: true });
  return off;
}

// live calls fn with the latest data of the live event name, if any, then
// with each change, until signal aborts.
function live(name, fn, signal) {
  const off = listen(name, fn, signal);
  if (latest.has(name) && !signal?.aborted) {
    fn(latest.get(name));
  }
  return off;
}

function emit(name, detail) {
  for (const fn of [...(listeners.get(name) ?? [])]) {
    try {
      fn(detail);
    } catch (err) {
      console.error(err);
    }
  }
}

function publish(name, data) {
  latest.set(name, data);
  emit(name, data);
}

let router = null; // created once every page has registered

// shared is what every page's dash has in common.
const shared = {
  api,
  confirm,
  emit,
  control(action, worktree, process) {
    return api.post(`/v1/worktree/${action}`, { dir: worktree.path, process: process ?? "" });
  },
  removeWorktree(worktree, project) {
    return removeWorktree(shared, worktree, project);
  },
  worktreeActions: [],
  processActions: [],
  navigate(href) {
    router.navigate(href);
  },
  get route() {
    return router?.route ?? null;
  },
  report(message) {
    const el = document.getElementById("report");
    el.textContent = message;
    el.hidden = !message;
  },
};

// scoped is a dash whose subscriptions end when signal aborts (never, for
// none). It has what pages set on the Dashboard's own dash (parent) in
// register.
function scoped(signal, parent = dash) {
  return Object.assign(Object.create(parent), {
    signal,
    on: (name, fn) => listen(name, fn, signal),
    onProjects: (fn) => live("projects", fn, signal),
    onServices: (fn) => live("services", fn, signal),
    onOrphans: (fn) => live("orphans", fn, signal),
    onPlugins: (fn) => live("plugins", fn, signal),
  });
}

const dash = scoped(undefined, shared);

const connection = document.getElementById("connection");
const setConnection = (state, label) => {
  connection.dataset.state = state;
  connection.querySelector(".label").textContent = label;
};
const events = new EventSource("/api/v1/events");
events.addEventListener("open", () => setConnection("live", "live"));
events.addEventListener("error", () => setConnection("down", "reconnecting…"));
for (const name of ["projects", "services", "orphans"]) {
  events.addEventListener(name, (e) => publish(name, JSON.parse(e.data)));
}

// Plugins aren't a live event: read them now, and again at most once a
// minute when Projects change.
let pluginsLoading = false;
let pluginsLoadedAt = 0;
async function loadPlugins() {
  if (pluginsLoading || Date.now() - pluginsLoadedAt < 60_000) {
    return;
  }
  pluginsLoading = true;
  pluginsLoadedAt = Date.now();
  try {
    publish("plugins", await api.get("/v1/plugins"));
  } catch (err) {
    dash.report(err.message);
  } finally {
    pluginsLoading = false;
  }
}
dash.on("projects", loadPlugins);
loadPlugins();

for (const page of pages) {
  page.register?.(dash);
}
mountSidebar(document.getElementById("sidebar"), dash);
router = createRouter(pages, document.getElementById("page"), scoped, dash);
router.start();
