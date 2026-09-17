// The window speaks two languages, and English is what a stranger gets.
//
// Everything else under test/ boots in Traditional Chinese (dom-shim.mjs says
// why). This check is the other half: the locale rule, the lookup's behaviour
// on a key nobody wrote, the named placeholders, that the static markup is
// keyed rather than worded, and that changing the language repaints what is
// already on screen instead of waiting for a reload.
//
//   node frontend/test/i18n.mjs

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { document, useLocale } from "./dom-shim.mjs";
import { pickLanguage, t, plural, setLanguage, language, LANGUAGES } from "../src/i18n/index.js";
import { TEXT as EN } from "../src/i18n/en.js";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";

globalThis.document = document;
globalThis.setInterval = () => 0;

const failures = [];
const el = (id) => document.getElementById(id);

/* ---------------- the locale rule ---------------- */

for (const [locale, want] of [
  ["zh-TW", "zh-Hant"],
  ["zh", "zh-Hant"],
  ["zh-Hans-CN", "zh-Hant"],
  ["en-GB", "en"],
  ["de-DE", "en"],
  ["", "en"],
  [undefined, "en"],
]) {
  const got = pickLanguage(locale);
  if (got !== want) failures.push(`pickLanguage(${JSON.stringify(locale)}) = ${got}, want ${want}`);
}

// The stored override wins over the locale, and only for a language this build
// actually has: a value from a newer build must fall back to the locale rather
// than leaving the window painted with key names.
if (pickLanguage("zh-TW", "en") !== "en") failures.push("a stored override of en did not win over a zh locale");
if (pickLanguage("en-US", "zh-Hant") !== "zh-Hant") failures.push("a stored override of zh-Hant did not win over an en locale");
if (pickLanguage("zh-TW", "klingon") !== "zh-Hant") failures.push("an unknown stored language did not fall back to the locale");

if (LANGUAGES.length !== 2) failures.push(`the language control offers ${LANGUAGES.length} entries, not two`);

/* ---------------- the lookup ---------------- */

setLanguage("en");
if (language() !== "en") failures.push("setLanguage did not take");
if (t("missing.key.nobody.wrote") !== "missing.key.nobody.wrote") {
  failures.push("a missing key did not answer with itself, so a typo would render as a blank panel");
}
if (t("pair.approve") !== EN["pair.approve"]) failures.push("t did not read the English table");
setLanguage("zh-Hant");
if (t("pair.approve") !== ZH["pair.approve"]) failures.push("t did not read the Traditional Chinese table");
setLanguage("nonsense");
if (language() !== "en") failures.push("an unknown language did not fall back to English");

// Named placeholders, because the two languages order their clauses
// differently and a positional one would have to be reordered per language.
setLanguage("en");
const substituted = t("footer.counts",
  { shown: 3, total: 9, allPaired: 1, selected: 2, none: 6 });
if (substituted.includes("{") || !substituted.includes("3")) {
  failures.push(`named placeholders were not substituted: ${substituted}`);
}
// A placeholder the caller did not supply is left alone rather than printed as
// "undefined": a visible {name} is a bug somebody reports.
if (!t("footer.counts", {}).includes("{shown}")) {
  failures.push("an unsupplied placeholder was not left visible");
}
if (plural(1, "table.sessionCount") === plural(2, "table.sessionCount")) {
  failures.push("plural() returned the same English string for one and for many");
}
if (!plural(1, "table.sessionCount").includes("1")) {
  failures.push("plural() did not substitute the count it was given");
}
if (ZH["table.sessionCount.one"] !== ZH["table.sessionCount.other"]) {
  failures.push("Chinese carries two forms for a count, which it has no use for");
}

/* ---------------- the static markup is keyed, not worded ---------------- */

// paintStatic walks querySelectorAll("[data-t]"), which the shim used to answer
// with []. Counting the shim's answer against index.html is what stops this
// whole section going quietly vacuous again.
const markup = fs.readFileSync(
  path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "index.html"), "utf8");
for (const attribute of ["data-t", "data-t-placeholder", "data-t-title"]) {
  const inFile = (markup.match(new RegExp(`\\s${attribute}="`, "g")) ?? []).length;
  const inShim = document.querySelectorAll(`[${attribute}]`).length;
  if (inFile === 0) {
    failures.push(`index.html carries no ${attribute}, so that part of the static text is not keyed`);
  }
  if (inShim !== inFile) {
    failures.push(`the shim answers ${inShim} ${attribute} elements and index.html has ${inFile}; `
      + "a shim that answers [] makes every assertion about the static text vacuous");
  }
}
// An element whose text is written by paintStatic cannot also hold children:
// textContent would delete them. This is the rule that a <code> or a <b> in a
// translated sentence has to be split around.
for (const [element] of markup.matchAll(/<([a-zA-Z][a-zA-Z0-9]*)\b[^>]*\sdata-t="[^"]*"[^>]*>([\s\S]*?)<\/\1>/g)) {
  if (/<[a-zA-Z]/.test(element.replace(/^<[^>]*>/, ""))) {
    failures.push(`an element carrying data-t holds child elements, which paintStatic would delete: ${element.slice(0, 90)}`);
  }
}

/* ---------------- the window itself ---------------- */

// en-US, which is what a machine outside a zh locale reports.
useLocale("en-US");
const noop = async () => ({});
const { configure, boot } = await import("../src/app.js");
configure({
  Overview: async () => ({
    reachable: true, nodeUrl: "http://127.0.0.1:7462",
    node: { id: "node_local", displayName: "local", platform: "darwin/arm64" },
    sessions: [], nodes: [], peers: [], counts: {},
  }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop,
  SetNodeAddress: noop, Pairing: async () => ({ availability: "unknown", candidates: [] }),
  OpenPairing: noop, ClosePairing: noop, Inbox: noop, ClearInbox: noop, MCPConfig: noop,
  CopyText: noop, Outbound: noop, Wakes: noop, PairRequests: async () => [],
  StartPairRequest: noop, ApprovePairRequest: noop, ConfirmPairRequest: noop, RejectPairRequest: noop,
  ServiceStatus: async () => ({ supported: false }), InstallService: noop, UninstallService: noop,
  LocalAddresses: async () => [], NodeSettings: async () => ({ error: "not needed" }),
  SaveNodeSettings: noop, RestartNode: noop, HostPlatform: async () => "darwin",
  Version: async () => ({ release: "unreleased" }),
});

const app = boot({ start: false });
if (app.language() !== "en") {
  failures.push(`a machine reporting en-US booted in ${app.language()}, not English`);
}
if (app.PAIR_TEXT.approve !== EN["pair.approve"]) {
  failures.push("PAIR_TEXT did not follow the active language");
}
if (el("btn-reload").textContent !== EN["app.reload"]) {
  failures.push(`the static markup was not painted in English: ${el("btn-reload").textContent}`);
}
if (el("search").placeholder !== EN["local.searchPlaceholder"]) {
  failures.push("a data-t-placeholder was not painted");
}
if (el("service-pill").title !== EN["app.servicePillTitle"]) {
  failures.push("a data-t-title was not painted");
}
if (app.paintStatic() === 0) failures.push("paintStatic wrote nothing");

// Switching repaints in place. A reload would drop the pairing drawer's state,
// the filter selection and anything half-typed.
app.renderPairingSubtitle(null);
if (el("pairing-sub").textContent !== EN["pair.drawerSubUnknown"]) {
  failures.push(`the drawer subtitle was not in English: ${el("pairing-sub").textContent}`);
}
app.setUILanguage("zh-Hant");
app.renderPairingSubtitle(null);
if (el("pairing-sub").textContent !== ZH["pair.drawerSubUnknown"]) {
  failures.push(`switching language left the drawer subtitle behind: ${el("pairing-sub").textContent}`);
}
if (el("btn-reload").textContent !== ZH["app.reload"]) {
  failures.push("switching language did not repaint the static markup");
}
// A sentence written once during the wiring is written before loadPrefs() has
// picked a language, and never again after a switch. This one was: it sat in
// English under a Chinese drawer until it became a data-t like the rest.
if (el("pair-address-note").textContent !== ZH["pair.addressNote"]) {
  failures.push(`the pairing form's note did not follow the language: ${el("pair-address-note").textContent}`);
}
if (app.state.ui.lang !== "zh-Hant") failures.push("the language choice was not kept in the preferences");
app.state.view = "settings";
app.render();
if (el("settings-lang").value !== "zh-Hant") {
  failures.push("the language control does not show the language in use");
}

/* ---------------- both tables say the same things ---------------- */

for (const key of Object.keys(ZH)) if (!Object.hasOwn(EN, key)) failures.push(`en.js has no ${key}`);
for (const key of Object.keys(EN)) if (!Object.hasOwn(ZH, key)) failures.push(`zh-Hant.js has no ${key}`);
for (const [key, value] of Object.entries(EN)) {
  if (/\p{Script=Han}/u.test(value)) failures.push(`en.js still holds Han in ${key}`);
}
// Every key the markup names has to exist, or the window paints key names.
for (const [, key] of markup.matchAll(/\sdata-t(?:-placeholder|-title)?="([^"]*)"/g)) {
  if (!Object.hasOwn(EN, key)) failures.push(`index.html names ${key}, which en.js does not define`);
  if (!Object.hasOwn(ZH, key)) failures.push(`index.html names ${key}, which zh-Hant.js does not define`);
}

if (failures.length > 0) {
  console.error(failures.map((line) => ` - ${line}`).join("\n"));
  process.exit(1);
}
console.log("i18n: the locale rule, the lookup, the keyed markup, and a repaint without a reload");
