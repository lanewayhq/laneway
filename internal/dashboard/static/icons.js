// The Dashboard's icons: small stroke icons drawn as inline SVG, since the
// Dashboard is served offline and its Content-Security-Policy allows no
// other sources. icon("logs") returns an <svg class="i">; style.css sizes
// and colours it (currentColor). Add an icon as a list of its shapes.
const shapes = {
  overview: [
    ["rect", { x: 3.5, y: 3.5, width: 7, height: 7, rx: 1.5 }], ["rect", { x: 13.5, y: 3.5, width: 7, height: 7, rx: 1.5 }],
    ["rect", { x: 3.5, y: 13.5, width: 7, height: 7, rx: 1.5 }], ["rect", { x: 13.5, y: 13.5, width: 7, height: 7, rx: 1.5 }],
  ],
  search: [["circle", { cx: 11, cy: 11, r: 7 }], ["path", { d: "m20 20-3.5-3.5" }]],
  logs: [["path", { d: "m5 16 5-4-5-4" }], ["path", { d: "M12 18h7" }]],
  services: [
    ["ellipse", { cx: 12, cy: 6, rx: 7, ry: 3 }],
    ["path", { d: "M5 6v12c0 1.7 3.1 3 7 3s7-1.3 7-3V6M5 12c0 1.7 3.1 3 7 3s7-1.3 7-3" }],
  ],
  orphans: [["path", { d: "M4 7h16M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3" }]],
  plugins: [["path", { d: "M9 3v4M15 3v4M7 7h10v5a5 5 0 0 1-10 0zM12 17v4" }]],
  branch: [
    ["circle", { cx: 6, cy: 6, r: 2.5 }], ["circle", { cx: 6, cy: 18, r: 2.5 }], ["circle", { cx: 18, cy: 7, r: 2.5 }],
    ["path", { d: "M6 8.5v7M18 9.5c0 4-4 5-10 6.5" }],
  ],
  start: [["path", { d: "M7 4.5v15l12-7.5z" }]],
  stop: [["rect", { x: 6, y: 6, width: 12, height: 12, rx: 2 }]],
  restart: [["path", { d: "M20 12a8 8 0 1 1-2.34-5.66L20 8.5" }], ["path", { d: "M20 3.5v5h-5" }]],
  more: [["circle", { cx: 5, cy: 12, r: 1 }], ["circle", { cx: 12, cy: 12, r: 1 }], ["circle", { cx: 19, cy: 12, r: 1 }]],
  remove: [
    ["path", { d: "M4 7h16M10 11v6M14 11v6" }], ["path", { d: "M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12" }],
    ["path", { d: "M9 7V4h6v3" }],
  ],
  check: [["path", { d: "m5 12.5 4.5 4.5L19 7.5" }]],
  changed: [["path", { d: "M12 20h8" }], ["path", { d: "M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z" }]],
  expand: [["path", { d: "m9 6 6 6-6 6" }]],
  collapse: [["path", { d: "m6 9 6 6 6-6" }]],
  // The Remove dialog's notes and its "Removing it" list.
  merged: [["circle", { cx: 12, cy: 12, r: 9 }], ["path", { d: "m8 12.5 3 3 5-6" }]],
  warning: [["path", { d: "M10.3 3.9 2.4 18a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" }], ["path", { d: "M12 9v4M12 17h.01" }]],
  commit: [["circle", { cx: 12, cy: 12, r: 3 }], ["path", { d: "M12 3v6M12 15v6" }]],
  lock: [["rect", { x: 5, y: 11, width: 14, height: 10, rx: 2 }], ["path", { d: "M8 11V7a4 4 0 0 1 8 0v4" }]],
  database: [["ellipse", { cx: 12, cy: 6, rx: 7, ry: 3 }], ["path", { d: "M5 6v12c0 1.7 3.1 3 7 3s7-1.3 7-3V6" }]],
};

const svg = "http://www.w3.org/2000/svg";

// icon returns the named icon, hidden from screen readers (the text or
// label next to it says what it is).
export function icon(name) {
  const el = document.createElementNS(svg, "svg");
  el.setAttribute("class", "i");
  el.setAttribute("viewBox", "0 0 24 24");
  el.setAttribute("aria-hidden", "true");
  for (const [tag, attrs] of shapes[name] ?? []) {
    const shape = document.createElementNS(svg, tag);
    for (const [k, v] of Object.entries(attrs)) {
      shape.setAttribute(k, String(v));
    }
    el.append(shape);
  }
  return el;
}
