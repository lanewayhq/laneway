// What Plugins show on a Project, Worktree or Service: the links, badges and
// actions their manifests declare (the api.PluginItems fields of a Project,
// Worktree or Service). Plugins supply no UI of their own; this draws them.
// An action runs the Plugin's command (POST
// /v1/plugins/{plugin}/commands/{command}) and shows its result in a dialog.
import { modal } from "./confirm.js";
import { h } from "./dom.js";

// pluginLinks are the item's links, opening in a new tab.
export function pluginLinks(item) {
  return (item.links ?? []).map((l) =>
    h("a", { class: "plugin-link", href: href(l.url), target: "_blank", rel: "noopener", title: `from Plugin ${l.plugin}` }, l.label));
}

// pluginBadges are the item's badges.
export function pluginBadges(item) {
  return (item.badges ?? []).map((b) =>
    h("span", { class: "badge plugin-badge", title: b.title || `from Plugin ${b.plugin}` }, b.text));
}

// pluginActions are the item's actions as { label, run() } (see
// dash.worktreeActions), each running its command in the Worktree at dir.
export function pluginActions(dash, item, dir) {
  return (item.actions ?? []).map((a) => ({
    label: a.label,
    run: async () => {
      const res = await dash.api.post(`/v1/plugins/${encodeURIComponent(a.plugin)}/commands/${encodeURIComponent(a.command)}`,
        { dir, args: a.args ?? [] });
      showResult(a, res);
    },
  }));
}

// href is url, keeping the port the Dashboard is served on for a URL under
// the Dashboard's Domain (such as Mailpit's Inbox at mail.<its Domain>),
// which the daemon serves on the same port.
function href(url) {
  try {
    const u = new URL(url);
    if (location.port && (u.hostname === location.hostname || u.hostname.endsWith("." + location.hostname))) {
      u.port = location.port;
    }
    return u.href;
  } catch {
    return url;
  }
}

// showResult shows what an action's command did: each command it ran with
// its output, its warnings, and why it failed if it did.
function showResult(a, res) {
  const close = h("button", { type: "button" }, "Close");
  const dialog = modal({
    title: a.label,
    subtitle: h("span", { class: "mono" }, `lane ${a.plugin} ${[a.command, ...(a.args ?? [])].join(" ")}`),
    className: "result",
    body: [
      ...(res.effects ?? []).map((e) => h("div", {},
        e.command ? h("code", {}, "$ " + e.command.join(" ")) : h("code", {}, e.kind),
        e.output ? h("pre", {}, e.output) : "")),
      ...(res.warnings ?? []).map((w) => h("p", { class: "warning" }, "Warning: ", w)),
      res.error ? h("p", { class: "error" }, res.error) : "",
      !res.error && !(res.effects ?? []).length && !(res.warnings ?? []).length ? h("p", {}, "Done.") : "",
    ],
    footer: [close],
  });
  close.addEventListener("click", () => dialog.close());
  close.focus();
}
