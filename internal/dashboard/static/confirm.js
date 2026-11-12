// The Dashboard's modal dialogs: a header (a title and a line under it), a
// body, and a footer with the buttons.
//
// confirm asks before a destructive action (dropping a database, Unlinking
// a Project, Removing a Worktree): what will happen (strings become
// paragraphs; nodes are added as they are) and a button labelled action.
// Cancel has the focus, so pressing Enter by habit doesn't destroy
// anything, and Cancel, Escape or closing the dialog do nothing.
//
// Without run, it resolves true when action is chosen, and the caller does
// the work. With run (an async function), choosing action runs it with the
// dialog still open ("Removing…"): if it succeeds the dialog closes and
// confirm resolves true; if it throws, the error is shown in the dialog,
// action stays disabled (what was confirmed may no longer be true) and
// confirm resolves false once the dialog is closed. A dialog asking about
// several things at once (Orphans' "Drop all now") lists them with list().
import { h } from "./dom.js";

export function confirm({ title, subtitle, body = [], action, busy, run, className = "" }) {
  return new Promise((resolve) => {
    const yes = h("button", { type: "button", class: "danger" }, action);
    const no = h("button", { type: "button" }, "Cancel");
    const error = h("p", { class: "dlg-error", role: "alert", hidden: true });
    const dialog = modal({ title, subtitle, body, className, footer: [error, no, yes] });
    let running = null; // the promise of run, once action is chosen
    yes.addEventListener("click", async () => {
      if (!run) {
        running = Promise.resolve(true);
        dialog.close();
        return;
      }
      yes.disabled = true;
      no.disabled = true;
      yes.textContent = busy ?? action + "…";
      error.hidden = true;
      running = run().then(() => true, (err) => {
        error.textContent = err.message;
        error.hidden = false;
        yes.textContent = action;
        no.textContent = "Close";
        no.disabled = false;
        no.focus();
        running = null;
        return false;
      });
      if (await running) {
        dialog.close();
      }
    });
    no.addEventListener("click", () => dialog.close());
    // While run is in flight, Escape can't close the dialog: what it
    // resolves must say whether the action happened.
    dialog.addEventListener("cancel", (e) => running && e.preventDefault());
    dialog.addEventListener("close", async () => {
      resolve(running ? await running : false);
    });
    no.focus();
  });
}

// modal shows a dialog with title, a line under it (subtitle: a string or
// nodes), body (strings become paragraphs; empty ones are left out) and
// footer (its buttons, which close it as they see fit). The dialog is
// removed once closed.
export function modal({ title, subtitle, body = [], footer = [], className = "" }) {
  const dialog = h("dialog", { class: `confirm ${className}`.trim() },
    h("header", { class: "dlg-hd" }, h("h3", {}, title), subtitle ? h("p", { class: "dlg-sub" }, subtitle) : ""),
    h("div", { class: "dlg-bd" }, ...body.filter((part) => part !== "" && part != null).map((part) => (typeof part === "string" ? h("p", {}, part) : part))),
    h("footer", { class: "dlg-ft" }, ...footer));
  dialog.addEventListener("close", () => dialog.remove());
  document.body.append(dialog);
  dialog.showModal();
  return dialog;
}

// notice shows a dialog with only a Close button (which has the focus),
// for something the developer can't go ahead with. It resolves when closed.
export function notice({ title, subtitle, body = [], className = "" }) {
  return new Promise((resolve) => {
    const close = h("button", { type: "button" }, "Close");
    const dialog = modal({ title, subtitle, body, className, footer: [close] });
    close.addEventListener("click", () => dialog.close());
    dialog.addEventListener("close", () => resolve());
    close.focus();
  });
}

// list is a bordered list of items, one row each (row(item) returns the
// row's children). With max, it shows the first max of them, then "and N
// more"; without, it shows every item, scrolling once the list is long.
export function list(items, row, { max = Infinity, className = "" } = {}) {
  const shown = items.length > max ? items.slice(0, max) : items;
  const scroll = max === Infinity ? "scroll" : "";
  return h("div", { class: `dlg-list ${scroll} ${className}`.replace(/\s+/g, " ").trim() },
    ...shown.map((item) => h("div", { class: "row" }, ...[row(item)].flat())),
    items.length > shown.length ? h("div", { class: "row more" }, `and ${items.length - shown.length} more`) : "");
}
