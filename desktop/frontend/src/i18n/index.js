// The window's words, and the one place they live.
//
// Two flat tables keyed by dotted ids, a lookup that substitutes {named}
// placeholders, and a language picked from the OS locale unless the owner said
// otherwise. Flat and expression-free on purpose: desktop/frontend_test.go
// parses both tables with a regex and fails on a key one language has and the
// other does not, which is the check that keeps the second language from
// rotting. Nothing in the tables may interpolate at runtime.
//
// What is NOT in here: anything the node said. nextStep, notice and the node's
// refusal bodies arrive as data and are rendered verbatim (docs/ui-contract.md
// section 12); a table keyed on the node's prose would silently stop matching
// the moment the node reworded a sentence.
import { TEXT as zhHant } from "./zh-Hant.js";
import { TEXT as en } from "./en.js";

const TABLES = { "zh-Hant": zhHant, en };

// What the language control offers. Each language names itself in itself,
// because that label is read by somebody who wants that one.
export const LANGUAGES = Object.freeze([
  { value: "en", label: "English" },
  { value: "zh-Hant", label: "繁體中文" },
]);

export const DEFAULT_LANGUAGE = "en";

let current = DEFAULT_LANGUAGE;

// pickLanguage is the whole of the choice: a stored override wins, otherwise a
// locale beginning with "zh" gets Traditional Chinese and everything else gets
// English. The node, the CLI, the API's own text and the README are English
// already, so English is the honest default for a machine that did not ask for
// Chinese — and a zh machine still opens in Chinese, which is where this app
// started.
export function pickLanguage(navigatorLanguage, override = "") {
  const wanted = String(override ?? "").trim();
  if (wanted !== "" && Object.hasOwn(TABLES, wanted)) return wanted;
  return String(navigatorLanguage ?? "").toLowerCase().startsWith("zh") ? "zh-Hant" : DEFAULT_LANGUAGE;
}

export function setLanguage(next) {
  current = Object.hasOwn(TABLES, next) ? next : DEFAULT_LANGUAGE;
  return current;
}

export function language() {
  return current;
}

// t returns the KEY when there is no string for it, never undefined and never
// blank: a missing key has to be a visible bug rather than an empty panel, and
// the static test in frontend_test.go is what stops one reaching a release.
export function t(key, params) {
  const table = TABLES[current] ?? TABLES[DEFAULT_LANGUAGE];
  const value = table[key];
  if (typeof value !== "string") return String(key);
  if (!params) return value;
  return value.replace(/\{(\w+)\}/g, (whole, name) =>
    Object.hasOwn(params, name) ? String(params[name]) : whole);
}

// plural covers the count strings English needs two forms for. No
// Intl.PluralRules: two forms is the whole of what English wants here, and
// Chinese resolves both to the same string.
export function plural(n, key, params = {}) {
  return t(key + "." + (n === 1 ? "one" : "other"), { n, ...params });
}

// The static markup's own words, by attribute. index.html carries the key and
// this writes the string, so the markup holds no sentence in any language and
// the enforcement test can be blunt about it: no Han in index.html, at all.
const STATIC_ATTRIBUTES = Object.freeze([
  ["[data-t]", "t", "textContent"],
  ["[data-t-placeholder]", "tPlaceholder", "placeholder"],
  ["[data-t-title]", "tTitle", "title"],
]);

export function paintStatic(root = globalThis.document) {
  if (!root?.querySelectorAll) return 0;
  let painted = 0;
  for (const [selector, property, target] of STATIC_ATTRIBUTES) {
    for (const node of root.querySelectorAll(selector)) {
      const key = node?.dataset?.[property];
      if (!key) continue;
      node[target] = t(key);
      painted++;
    }
  }
  return painted;
}
