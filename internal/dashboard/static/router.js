// The router picks the page the URL names and shows it, with the History
// API: following a link to one of the Dashboard's own pages, or
// dash.navigate(href), changes the URL and the page without reloading, and
// Back and Forward go between them. The daemon serves the Dashboard for
// every such path, so reloading or opening a bookmark shows the same page.
//
// A page's path is matched segment by segment; ":name" matches any one
// segment, which the page gets decoded in route.params.name.
import { h } from "./dom.js";

export function createRouter(pages, el, scoped, dash) {
  const routes = pages.map((page) => ({ page, segments: split(page.path) }));
  let leaving = null; // aborts when the page shown is left

  const router = {
    route: null,
    start() {
      document.addEventListener("click", follow);
      window.addEventListener("popstate", show);
      show();
    },
    navigate(href, { replace = false } = {}) {
      const url = new URL(href, location.href);
      if (url.origin !== location.origin || url.pathname.startsWith("/api/")) {
        location.assign(url.href);
        return;
      }
      if (url.href !== location.href) {
        history[replace ? "replaceState" : "pushState"](null, "", url.href);
      }
      show();
      window.scrollTo(0, 0);
    },
  };

  // follow turns a plain click on a link to one of the Dashboard's pages
  // into navigate, leaving the browser everything else: links to other
  // sites and Domains, new tabs, downloads and modified clicks.
  function follow(e) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) {
      return;
    }
    const a = e.target.closest?.("a[href]");
    if (!a || (a.target && a.target !== "_self") || a.hasAttribute("download") || a.origin !== location.origin ||
      a.pathname.startsWith("/api/")) {
      return;
    }
    if (a.pathname === location.pathname && a.search === location.search && a.hash) {
      return; // a jump within the page
    }
    e.preventDefault();
    router.navigate(a.href);
  }

  function show() {
    leaving?.abort();
    leaving = new AbortController();
    const url = new URL(location.href);
    const found = match(url.pathname);
    const page = found?.page ?? notFound;
    const route = { page: page.id, path: url.pathname, params: found?.params ?? {}, query: url.searchParams };
    router.route = route;
    const title = typeof page.title === "function" ? page.title(route) : page.title;
    document.title = title ? `${title} · Laneway` : "Laneway";
    const container = h("div", { class: "page", "data-page": page.id });
    el.replaceChildren(container);
    dash.report("");
    try {
      page.show(container, scoped(leaving.signal), route);
    } catch (err) {
      console.error(err);
      dash.report(err.message);
    }
    dash.emit("route", route);
  }

  function match(pathname) {
    const got = split(pathname);
    for (const { page, segments } of routes) {
      if (segments.length !== got.length) {
        continue;
      }
      const params = {};
      let ok = true;
      for (let i = 0; i < segments.length && ok; i++) {
        if (segments[i].startsWith(":")) {
          try {
            params[segments[i].slice(1)] = decodeURIComponent(got[i]);
          } catch {
            ok = false;
          }
        } else {
          ok = segments[i] === got[i];
        }
      }
      if (ok) {
        return { page, params };
      }
    }
    return null;
  }

  return router;
}

function split(path) {
  return path.split("/").filter(Boolean);
}

// notFound is shown for a path that names none of the pages.
const notFound = {
  id: "not-found",
  title: "Not found",
  show(el, dash, route) {
    el.append(
      h("header", { class: "ph" }, h("div", {}, h("h1", {}, "No such page"),
        h("div", { class: "meta" }, h("span", { class: "mono" }, route.path)))),
      h("div", { class: "body" }, h("p", { class: "empty" },
        "The Dashboard has no page here. Go to the ", h("a", { href: "/" }, "Overview"), ".")));
  },
};
