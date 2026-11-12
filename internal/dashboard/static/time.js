// Times as the pages show them: "3 h ago", "in 21 h", with the full date
// in a title.

// relative says how far t (a Date) is from now, roughly: "just now",
// "5 min ago", "in 21 h", "in 2 days".
export function relative(t, now = new Date()) {
  const s = Math.round((t - now) / 1000);
  const abs = Math.abs(s);
  let text;
  if (abs < 60) {
    return "just now";
  } else if (abs < 3600) {
    text = `${Math.round(abs / 60)} min`;
  } else if (abs < 2 * 86400) {
    text = `${Math.round(abs / 3600)} h`;
  } else {
    text = `${Math.round(abs / 86400)} days`;
  }
  return s < 0 ? `${text} ago` : `in ${text}`;
}

// absolute is t in the browser's locale, for titles.
export function absolute(t) {
  return t.toLocaleString();
}
