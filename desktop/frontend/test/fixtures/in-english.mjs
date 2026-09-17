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
import { TEXT as ZH } from "../../src/i18n/zh-Hant.js";

const HAN = /\p{Script=Han}/u;

// Text of an element, plus the text of a <select>'s options: an option list
// rebuilt in the wrong language is invisible to textContent alone.
function readable(node) {
  const parts = [String(node.textContent ?? "")];
  for (const option of node.options ?? []) parts.push(String(option.textContent ?? ""));
  return parts.join(" ");
}

// A fourth failure, and the one the first three could not see.
//
// An element JS writes from state carries a data-t key in index.html too, and
// that key's value is a PLACEHOLDER — "reading the service status…", "looking
// for the node…". paintStatic puts it back on every switch. If nothing
// re-derives the element afterwards, the owner reads a correctly translated
// English sentence that is false: no Han, no raw key, and the check passed.
//
// `derived` names the ids whose text is owned by state rather than by the
// markup. Their text after a switch may not be the static placeholder for their
// own key, in either language — which is exactly what dropping renderService()
// or relabelNodeSettings() from repaintFromState leaves behind.
function placeholderFailure(label, id, node, failures) {
  const key = node?.dataset?.t;
  if (!key) {
    failures.push(`${label}: #${id} was named as state-derived but carries no data-t in index.html`);
    return;
  }
  const text = String(node.textContent ?? "").trim();
  for (const [lang, table] of [["en", EN], ["zh-Hant", ZH]]) {
    if (Object.hasOwn(table, key) && text === String(table[key]).trim()) {
      failures.push(`${label}: #${id} still reads its ${lang} placeholder for ${key} `
        + `(${text.slice(0, 80)}), so nothing re-derived it from state`);
    }
  }
}

export function inEnglish(app, failures, label, ids, repaint = () => {}, derived = []) {
  const before = app.language();
  app.setUILanguage("en");
  repaint();
  for (const id of ids) {
    const node = document.getElementById(id);
    const text = readable(node);
    if (HAN.test(text)) {
      failures.push(`${label}: #${id} is still Chinese after switching to English: ${text.trim().slice(0, 120)}`);
    }
    for (const word of text.split(/\s+/)) {
      if (Object.hasOwn(EN, word)) {
        failures.push(`${label}: #${id} shows the raw key ${word} instead of what it stands for`);
      }
    }
    if (derived.includes(id)) placeholderFailure(label, id, node, failures);
  }
  app.setUILanguage(before);
  repaint();
  for (const id of derived) {
    placeholderFailure(`${label} (back in ${before})`, id, document.getElementById(id), failures);
  }
}
