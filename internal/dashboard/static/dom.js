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
