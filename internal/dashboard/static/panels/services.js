// The Services panel: every shared Service (redis:7, postgres:18,
// mailpit:v1…) with its engine, version, address, login and state, and
// Start/Stop buttons (POST /v1/services/{up|down}). Stopping keeps the data
// (it is in the Service's volume). Each row also shows the links and badges
// of the Plugin that adds its Engine, such as Mailpit's Inbox (see
// pluginitems.js). It re-renders on every `services` event.
import { h } from "../dom.js";
import { pluginBadges, pluginLinks } from "../pluginitems.js";

export default {
  id: "services",
  title: "Services",
  mount(el, dash) {
    const pending = new Set(); // "<service>:<action>" in flight
    let last = null;

    const render = (list) => {
      last = list;
      if (list.services.length === 0) {
        el.replaceChildren(h("p", { class: "empty" },
          "No Services yet. Declare one in the ", h("code", {}, "[services]"), " table of ",
          h("code", {}, "laneway.toml"), ", such as ", h("code", {}, 'redis = "7"'), "."));
        return;
      }
      el.replaceChildren(h("table", {},
        h("thead", {}, h("tr", {}, ...["Service", "Engine", "Version", "Address", "Login", "State", ""].map((c) => h("th", {}, c)))),
        h("tbody", {}, ...list.services.map(row))));
    };

    const row = (s) => h("tr", { "data-service": s.name },
      h("td", {}, h("strong", {}, s.name)),
      h("td", {}, s.engine),
      h("td", {}, s.version),
      h("td", {}, h("code", {}, `${s.host}:${s.port}`)),
      h("td", {}, login(s)),
      h("td", {}, badge(s.state), ...pluginBadges(s), s.error ? h("div", { class: "error" }, s.error) : ""),
      h("td", { class: "actions" },
        button(s, "up", "Start"), button(s, "down", "Stop"), ...pluginLinks(s)));

    const button = (s, action, label) => {
      const key = `${s.name}:${action}`;
      const b = h("button", { type: "button", disabled: pending.has(key) }, pending.has(key) ? label + "…" : label);
      b.addEventListener("click", async () => {
        pending.add(key);
        render(last);
        try {
          await dash.api.post(`/v1/services/${action}`, { name: s.name });
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

    dash.onServices(render);
  },
};

function login(s) {
  if (!s.password) {
    return "-";
  }
  return h("code", {}, s.user ? `${s.user} / ${s.password}` : s.password);
}

function badge(state) {
  return h("span", { class: "badge", "data-state": state }, state);
}
