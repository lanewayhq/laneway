// The Logs panel: one Process's output, or all of a Worktree's Processes
// together labelled by name, followed live. It adds a "Logs" button to every
// Worktree and Process in the Worktrees panel, which emit a `logs` event
// ({ worktree, process? }); the panel then shows the recent output the daemon
// kept and streams new lines from GET /v1/logs?follow=true.
import { h } from "../dom.js";

// maxLines matches the daemon's kept output per Process
// (supervisor.RecentLines); older lines are dropped from the page.
const maxLines = 1000;

// colors is how many distinct process label colours style.css defines.
const colors = 6;

// ansi matches terminal escape sequences (colours, cursor moves), which
// tools like Vite write even into a pipe.
const ansi = /\x1b\[[0-9;?]*[ -\/]*[@-~]/g;

export default {
  id: "logs",
  title: "Logs",
  mount(el, dash) {
    dash.worktreeActions.push((w) => [{ label: "Logs", run: () => dash.emit("logs", { worktree: w }) }]);
    dash.processActions.push((w, p, proc) => [
      { label: "Logs", run: () => dash.emit("logs", { worktree: w, process: proc.name }) },
    ]);

    let following = null; // the AbortController of the open stream
    let retry = null; // the timer that reopens a stream that ended

    const empty = () => el.replaceChildren(h("p", { class: "empty" },
      "Choose Logs on a Worktree or a Process above to follow its output here."));

    const close = () => {
      following?.abort();
      following = null;
      clearTimeout(retry);
    };

    const show = ({ worktree, process }) => {
      close();
      const controller = new AbortController();
      following = controller;
      const all = !process;
      const status = h("span", { class: "status" }, "connecting…");
      const closeButton = h("button", { type: "button" }, "Close");
      closeButton.addEventListener("click", () => {
        close();
        empty();
      });
      const lines = h("div", { class: "lines", role: "log", "aria-live": "off", tabindex: "0" });
      el.replaceChildren(
        h("header", { class: "logs-header" },
          h("strong", {}, all ? "All Processes" : process),
          " of ", worktree.main ? "main" : worktree.name, " ",
          h("span", { class: "path" }, worktree.domain ?? worktree.path),
          status, closeButton),
        lines);
      el.closest(".panel")?.scrollIntoView({ block: "nearest" });

      const labels = new Map(); // Process name -> colour index
      const append = (line) => {
        const atBottom = lines.scrollHeight - lines.scrollTop - lines.clientHeight < 4;
        if (!labels.has(line.process)) {
          labels.set(line.process, labels.size % colors);
        }
        lines.append(h("div", { class: "line", title: new Date(line.time).toLocaleString() },
          all ? h("span", { class: "process", "data-color": labels.get(line.process) }, line.process) : "",
          h("span", { class: "text" }, line.text.replace(ansi, ""))));
        while (lines.childElementCount > maxLines) {
          lines.firstElementChild.remove();
        }
        if (atBottom) {
          lines.scrollTop = lines.scrollHeight;
        }
      };

      const query = new URLSearchParams({ dir: worktree.path, follow: "true" });
      if (process) {
        query.set("process", process);
      }
      const open = async () => {
        // A reopened stream starts again from the kept output.
        lines.replaceChildren();
        status.textContent = "live";
        status.dataset.state = "live";
        try {
          await dash.api.stream("/v1/logs?" + query, append, controller.signal);
        } catch (err) {
          if (controller.signal.aborted) {
            return;
          }
          if (!(err instanceof TypeError)) {
            // The daemon answered with an error (the Process is gone, the
            // Project was Unlinked): reopening wouldn't help.
            status.textContent = err.message;
            status.dataset.state = "down";
            return;
          }
        }
        if (controller.signal.aborted) {
          return;
        }
        // The stream ended (the daemon stopped or restarted): try again, as
        // the live events do.
        status.textContent = "reconnecting…";
        status.dataset.state = "down";
        retry = setTimeout(open, 2000);
      };
      open();
    };

    dash.on("logs", show);
    empty();
  },
};
