// The Plugins panel: every installed Plugin, with its version and where it
// came from, and the update `lane plugin update` would install, if there is
// one (GET /v1/plugins: from the Catalog, or for a Plugin added by hand, its
// source). Updating stays in the terminal, where the developer confirms what
// a new version may do. It loads when the page opens and again, at most
// once a minute, when Plugins may have changed (a `projects` event, as
// Linking can fetch Plugins).
import { h } from "../dom.js";

export default {
  id: "plugins",
  title: "Plugins",
  mount(el, dash) {
    let loading = false;
    let loadedAt = 0;

    const render = (list) => {
      if (list.plugins.length === 0) {
        el.replaceChildren(h("p", { class: "empty" },
          "No Plugins are installed. Laneway fetches the ones a Project needs from the Catalog when you Link it, or add one with ",
          h("code", {}, "lane plugin add"), "."));
        return;
      }
      const updates = list.plugins.filter((p) => p.update);
      el.replaceChildren(
        updates.length > 0
          ? h("p", { class: "notice" }, `Plugin update${updates.length === 1 ? " is" : "s are"} available; run `,
            h("code", {}, "lane plugin update"), " to see what each may do and install it.")
          : "",
        h("table", {},
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

    const load = async () => {
      if (loading || Date.now() - loadedAt < 60_000) {
        return;
      }
      loading = true;
      loadedAt = Date.now();
      try {
        render(await dash.api.get("/v1/plugins"));
      } catch (err) {
        dash.report(err.message);
      } finally {
        loading = false;
      }
    };

    load();
    dash.on("projects", load);
  },
};
