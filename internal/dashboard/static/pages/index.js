// The Dashboard's pages, each at its own path. See app.js for what a page
// is, and router.js for how a path picks one.
import overview from "./overview.js";
import project from "./project.js";
import logs from "./logs.js";
import services from "./services.js";
import orphans from "./orphans.js";
import plugins from "./plugins.js";

export default [overview, project, logs, services, orphans, plugins];
