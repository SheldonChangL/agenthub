// Run a panel the rest of a check renders in Chinese once in English too.
//
// Everything under test/ except i18n.mjs boots in Traditional Chinese
// (dom-shim.mjs says why), so the English half of the window was rendered by
// exactly one check — and that one drives the static markup, not the panels.
// The bug that made this necessary was in a panel: switching language left the
// service line, the service button and the address list showing either the old
// language or index.html's placeholder key.
//
// So: switch, let the window repaint, and read the elements back. Two things
// are failures. Han under English means something was painted before the
// switch and never re-derived. A whole word that is a key in the table means
// t() was handed a key that language does not carry, or paintStatic's
// placeholder is still sitting there — either way the owner reads
// "service.lineLoading" where a sentence belongs.
import { document } from "../dom-shim.mjs";
import { TEXT as EN } from "../../src/i18n/en.js";

const HAN = /\p{Script=Han}/u;

// Text of an element, plus the text of a <select>'s options: an option list
// rebuilt in the wrong language is invisible to textContent alone.
function readable(node) {
  const parts = [String(node.textContent ?? "")];
  for (const option of node.options ?? []) parts.push(String(option.textContent ?? ""));
  return parts.join(" ");
}

export function inEnglish(app, failures, label, ids, repaint = () => {}) {
  const before = app.language();
  app.setUILanguage("en");
  repaint();
  for (const id of ids) {
    const text = readable(document.getElementById(id));
    if (HAN.test(text)) {
      failures.push(`${label}: #${id} is still Chinese after switching to English: ${text.trim().slice(0, 120)}`);
    }
    for (const word of text.split(/\s+/)) {
      if (Object.hasOwn(EN, word)) {
        failures.push(`${label}: #${id} shows the raw key ${word} instead of what it stands for`);
      }
    }
  }
  app.setUILanguage(before);
  repaint();
}
