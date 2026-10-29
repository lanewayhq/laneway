// The Worktrees panel: every Project with its Worktrees, their readiness
// (Preparing, Ready, Failed) and run states, Domains, Aliases and Processes, with start/stop/restart buttons.
// It re-renders on every `projects` event.
import { h } from "../dom.js";

export default {
  id: "worktrees",
  title: "Projects",
  mount(el, dash) {
    const open = new Set(); // keys of the <details> the user has opened
    const pending = new Set(); // buttons whose request is in flight, by key
    let last = null;

    const render = (list) => {
      last = list;
      if (list.projects.length === 0) {
        el.replaceChildren(h("p", { class: "empty" },
          "No Projects are Linked. Run ", h("code", {}, "portby link"), " in a git repository."));
        return;
      }
      el.replaceChildren(...list.projects.map((p) => project(p)));
    };

    // button runs fn (a request) once, disabled until it finishes.
    const button = (key, label, fn) => {
      const b = h("button", { type: "button", disabled: pending.has(key) }, pending.has(key) ? label + "…" : label);
      b.addEventListener("click", async () => {
        pending.add(key);
        render(last);
        try {
          await fn();
          dash.report("");
        } catch (err) {
          dash.report(err.message);
        } finally {
          pending.delete(key);
          render(last);
        }
      });
      return b;
    };

    const details = (key, summary, ...body) => {
      const d = h("details", { open: open.has(key) }, h("summary", {}, summary), ...body);
      d.addEventListener("toggle", () => (d.open ? open.add(key) : open.delete(key)));
      return d;
    };

    const controls = (key, worktree, process) => h("span", { class: "controls" },
      ...["up", "down", "restart"].map((action) =>
        button(`${key}:${action}`, { up: "Start", down: "Stop", restart: "Restart" }[action],
          () => dash.control(action, worktree, process))));

    const extra = (key, actions) => actions.map(({ label, run }) => button(`${key}:${label}`, label, run));

    const project = (p) => h("article", { class: "project" },
      h("header", {}, h("h3", {}, p.name), h("span", { class: "path" }, p.path)),
      h("table", {},
        h("thead", {}, h("tr", {}, ...["Worktree", "Domain", "Readiness", "State", "Processes", ""].map((c) => h("th", {}, c)))),
        h("tbody", {}, ...p.worktrees.map((w) => worktree(p, w)))));

    const worktree = (p, w) => {
      const key = w.path;
      return h("tr", { "data-domain": w.domain ?? "" },
        h("td", {}, w.main ? h("span", {}, "main") : w.name),
        h("td", {}, ...names(w)),
        h("td", {}, setup(key, w.setup)),
        h("td", {}, badge(w.state ?? "-")),
        h("td", {}, processes(p, w)),
        h("td", { class: "actions" }, controls(key, w),
          ...extra(key, dash.worktreeActions.flatMap((fn) => fn(w, p)))));
    };

    const setup = (key, s) => {
      if (!s) {
        return "-";
      }
      if (s.state === "failed") {
        return details(`${key}:setup`, h("span", {}, badge("failed"), " preparing at ", h("code", {}, s.step)),
          h("p", {}, s.error),
          s.output?.length ? h("pre", {}, s.output.join("\n")) : "",
          h("p", {}, "Fix it, then run ", h("code", {}, "portby worktree ready --retry"), " in the Worktree."));
      }
      if (s.state === "preparing") {
        return h("span", {}, badge("preparing"), " ", h("code", {}, s.step ?? ""));
      }
      return badge(s.state);
    };

    // processes lists the declared Processes plus any running one the
    // config no longer declares.
    const processes = (p, w) => {
      const states = new Map((w.processes ?? []).map((pr) => [pr.name, pr.state]));
      const declared = w.declared ?? [];
      const names = new Set(declared.map((d) => d.name));
      const all = [...declared, ...(w.processes ?? []).filter((pr) => !names.has(pr.name)).map((pr) => ({ name: pr.name }))];
      if (all.length === 0) {
        return "-";
      }
      return h("ul", { class: "processes" }, ...all.map((d) => {
        const key = `${w.path}:${d.name}`;
        return h("li", {},
          h("span", { class: "name" }, d.name),
          d.primary ? h("span", { class: "tag" }, "primary") : "",
          d.startMode && d.startMode !== "lazy" ? h("span", { class: "tag" }, d.startMode) : "",
          badge(states.get(d.name) ?? "stopped"),
          d.http && !d.primary && w.domain ? link(`${d.name}.${w.primaryDomain}`) : "",
          controls(key, w, d.name),
          ...extra(key, dash.processActions.flatMap((fn) => fn(w, p, d))));
      }));
    };

    dash.onProjects(render);
  },
};

// names links the Worktree at its Primary Domain (its newest Alias, else
// its Domain), then lists its other names: its Domain, which identifies it,
// and its older Aliases.
function names(w) {
  const others = [w.domain, ...(w.aliases ?? [])].filter((d) => d && d !== w.primaryDomain);
  return [link(w.primaryDomain), ...others.map((d) => h("div", { class: "alias" }, link(d)))];
}

function link(domain) {
  if (!domain) {
    return "-";
  }
  return h("a", { href: `https://${domain}/`, target: "_blank", rel: "noopener" }, domain);
}

function badge(state) {
  return h("span", { class: "badge", "data-state": state }, state);
}
