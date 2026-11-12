// h builds an element: h("a", { href: "/" }, "text", child…). Strings become
// text nodes (never HTML), so names from repositories can't inject markup.
// Attributes set to false, null or undefined are left out; true sets an empty
// attribute.
export function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [name, value] of Object.entries(attrs)) {
    if (value === false || value === null || value === undefined) {
      continue;
    }
    el.setAttribute(name, value === true ? "" : String(value));
  }
  for (const child of children.flat()) {
    if (child === "" || child === null || child === undefined) {
      continue;
    }
    el.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return el;
}

// pageHeader is a page's header: its title, a line of facts about it
// (meta: strings or nodes, shown with dots between them) and, at the end,
// its buttons.
export function pageHeader(title, meta = [], end = []) {
  const facts = meta.filter((m) => m !== "" && m !== null && m !== undefined);
  return h("header", { class: "ph" },
    h("div", { class: "title" }, h("h1", {}, title),
      facts.length ? h("div", { class: "meta" }, ...facts.flatMap((m, i) => (i ? [h("span", { class: "sepd" }), m] : [m]))) : ""),
    end.length ? h("div", { class: "end" }, ...end) : "");
}
