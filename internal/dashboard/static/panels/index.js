// The Dashboard's panels, in display order. See app.js for what a panel is.
import worktrees from "./worktrees.js";
import logs from "./logs.js";
import services from "./services.js";
import orphans from "./orphans.js";
import plugins from "./plugins.js";

export default [worktrees, logs, services, orphans, plugins];
