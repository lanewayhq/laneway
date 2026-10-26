// confirm asks before a destructive action (dropping a database, Unlinking
// a Project) in a modal dialog: a title, what will happen (strings become
// paragraphs; nodes are added as they are) and a button labelled action.
// It resolves true only if that button is chosen; Cancel, Escape or
// closing the dialog resolve false. Cancel has the focus, so pressing
// Enter by habit doesn't destroy anything.
import { h } from "./dom.js";

export function confirm({ title, body = [], action }) {
  return new Promise((resolve) => {
    const yes = h("button", { type: "button", class: "danger" }, action);
    const no = h("button", { type: "button" }, "Cancel");
    const dialog = h("dialog", { class: "confirm" },
      h("h3", {}, title),
      ...body.map((part) => (typeof part === "string" ? h("p", {}, part) : part)),
      h("div", { class: "buttons" }, no, yes));
    let answer = false;
    yes.addEventListener("click", () => {
      answer = true;
      dialog.close();
    });
    no.addEventListener("click", () => dialog.close());
    dialog.addEventListener("close", () => {
      dialog.remove();
      resolve(answer);
    });
    document.body.append(dialog);
    dialog.showModal();
    no.focus();
  });
}
