// The Plugins page, at /plugins: every installed Plugin, with its version
// and where it came from, and the update `lane plugin update` would
// install, if there is one (GET /v1/plugins: from the Catalog, or for a
// Plugin added by hand, its source). Updating stays in the terminal, where
// the developer confirms what a new version may do. The list comes from
// dash.onPlugins, which reads it again at most once a minute when Plugins
// may have changed.
import { h, pageHeader } from "../dom.js";

export default {
  id: "plugins",
  path: "/plugins",
  title: "Plugins",
  show(el, dash) {
    const render = (list) => {
      const updates = list.plugins.filter((p) => p.update);
      const header = pageHeader("Plugins", [
        "Laneway fetches the ones a Project needs from the Catalog when you Link it.",
        updates.length ? `${updates.length} update${updates.length === 1 ? "" : "s"} available` : "",
      ]);
      if (list.plugins.length === 0) {
        el.replaceChildren(header, h("div", { class: "body" }, h("p", { class: "empty" },
          "No Plugins are installed. Add one with ", h("code", {}, "lane plugin add"), ".")));
        return;
      }
      el.replaceChildren(header,
        updates.length > 0
          ? h("p", { class: "notice body" }, `Plugin update${updates.length === 1 ? " is" : "s are"} available; run `,
            h("code", {}, "lane plugin update"), " to see what each may do and install it.")
          : "",
        h("table", { class: "tbl" },
          h("thead", {}, h("tr", {}, ...["Plugin", "Version", "Source", "Update"].map((c) => h("th", {}, c)))),
          h("tbody", {}, ...list.plugins.map(row))));
    };

    const row = (p) => h("tr", { "data-plugin": p.name },
      h("td", {}, h("strong", {}, p.name), p.global ? h("span", { class: "tag" }, " machine-wide") : ""),
      h("td", {}, p.version),
      h("td", {}, p.source),
      h("td", {}, p.update
        ? h("span", { class: "badge", "data-state": "update", title: `SHA-256 ${p.update.sha256}` }, `${p.update.from} → ${p.update.to}`)
        : "up to date"));

    dash.onPlugins(render);
  },
};
