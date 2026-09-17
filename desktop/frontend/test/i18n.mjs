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

/* ---------------- the panels a switch has to catch up ---------------- */

// The Settings tab is the panel the language control itself sits in, and it
// was the one a switch left behind. renderService() and the node settings form
// are not called from render(), so paintStatic() put index.html's placeholder
// keys back on them and nothing wrote over them again: a machine with the
// service installed and RUNNING read "Reading background service status…" and
// offered a button saying "Install as a background service".
//
// Booted here with a service that is installed and running and a settings form
// the node answered, because those are the states where a placeholder is not
// merely stale but false.
configure({
  ServiceStatus: async () => ({
    tool: "/usr/local/bin/ah", supported: true, installed: true, running: true,
    pid: 41872, unitPath: "~/Library/LaunchAgents/tw.jet-opto.agenthub-node.plist",
    logHint: "~/Library/Logs/agenthub-node.log", nodeAnswering: true,
    dbPath: "~/agenthub.db", dbPathKnown: true,
  }),
  NodeSettings: async () => ({
    settings: { peerListen: "192.168.50.10:7463", allowLan: true, discover: true, treatAsPrivate: [], autoWake: false },
    saved: { peerListen: "192.168.50.10:7463", allowLan: true, discover: true, treatAsPrivate: [], autoWake: false },
    sources: { peerListen: "remembered", allowLan: "remembered", discover: "remembered", treatAsPrivate: "default", autoWake: "default" },
    restartRequired: false,
  }),
  LocalAddresses: async () => [
    { interface: "en0", address: "192.168.50.10", subnet: "192.168.50.0/24", private: true },
  ],
});
app.state.nodeReachable = true;
app.setUILanguage("en");
await app.loadService();
await app.loadNodeSettings();

const optionText = () => el("node-peerlisten").options.map((option) => option.textContent);
const han = /\p{Script=Han}/u;
// A key rendered where a sentence belongs. Both tables are checked, because
// the failure that started this is English text under a Chinese window as much
// as the other way round.
const rawKey = (value) => Object.hasOwn(EN, String(value).trim()) || Object.hasOwn(ZH, String(value).trim());

for (const [lang, table, other] of [["en", EN, ZH], ["zh-Hant", ZH, EN], ["en", EN, ZH]]) {
  app.setUILanguage(lang);
  const where = `after switching to ${lang}`;

  const line = el("service-line").textContent;
  if (line !== table["service.lineRunning"].replace("{pid}", "41872")
      .replace("{unit}", "~/Library/LaunchAgents/tw.jet-opto.agenthub-node.plist")) {
    failures.push(`${where} the service line is not the running one: ${line}`);
  }
  if (line === table["service.lineLoading"] || rawKey(line)) {
    failures.push(`${where} the service line fell back to its placeholder: ${line}`);
  }

  const pill = el("service-pill-text").textContent;
  if (pill !== table["service.pillRunning"]) {
    failures.push(`${where} the service pill says ${pill}, not that the service is running`);
  }

  // The button whose label lied: its handler always opens the install form, so
  // on an installed machine the label has to be the reinstall one.
  const open = el("service-open").textContent;
  if (open !== table["service.reinstallFlags"]) {
    failures.push(`${where} the service button reads ${open}, not the reinstall label`);
  }
  if (open === table["service.install"] || open === table["service.install2"]) {
    failures.push(`${where} an installed machine is offered a fresh install: ${open}`);
  }

  const options = optionText();
  if (options.length < 2) failures.push(`${where} the address list holds ${options.length} options`);
  for (const option of options) {
    if (rawKey(option)) failures.push(`${where} an address option shows a raw key: ${option}`);
    // Each option is part address, part sentence; the sentence has to be the
    // language in use and not the one before it.
    if (lang === "en" && han.test(option)) {
      failures.push(`${where} an address option is still Chinese: ${option}`);
    }
    if (lang === "zh-Hant" && !han.test(option) && !/^\d|^127\./.test(option)) {
      failures.push(`${where} an address option is still English: ${option}`);
    }
  }
  if (!options.some((option) => option.includes(table["nodeSettings.optionLoopback"].replace("{address}", "127.0.0.1:7463")))) {
    failures.push(`${where} the loopback placeholder is not in the address list: ${options.join(" | ")}`);
  }
  // Nothing in either panel may read as "still loading" once it has loaded.
  for (const id of ["service-line", "service-pill-text", "service-open", "node-settings-hint"]) {
    const value = el(id).textContent;
    if (value === table["service.lineLoading"] || value === table["service.pillLoading"]
        || value === table["common.loadingShort"] || rawKey(value)) {
      failures.push(`${where} #${id} reads as loading or as a key: ${value}`);
    }
  }
}

// The MANAGED column is the node's enum, and it used to reach the screen raw.
//
// While en.js answered "managed" for session.managed.managed this check could
// not tell a table lookup from the raw enum going straight through, so the
// English table carries the capitalised word and both facts are asserted.
app.setUILanguage("en");
if (app.managementLabel("managed") !== EN["session.managed.managed"]) {
  failures.push("the managed column did not go through the table in English");
}
if (EN["session.managed.managed"] === "managed" || EN["session.managed.unmanaged"] === "unmanaged") {
  failures.push("en.js repeats the node's enum, so the assertion above cannot see a raw value reaching the screen");
}
if (app.managementLabel("managed") === "managed") {
  failures.push("the node's enum reached the English screen unchanged");
}
app.setUILanguage("zh-Hant");
if (app.managementLabel("unmanaged") !== ZH["session.managed.unmanaged"]) {
  failures.push("the managed column did not go through the table in Chinese");
}
// A value this build has never heard of is the node saying something new, and
// is shown as it arrived rather than blanked or guessed at.
if (app.managementLabel("supervised") !== "supervised") {
  failures.push("an unknown management value was not shown verbatim");
}
app.setUILanguage("en");

/* ---------------- the drawers and the title bar ---------------- */

// Three more things a switch has to catch up, none of them re-derived by
// render(): the inbox list (written once per read, by the one call that was
// handed the answer), the MCP dialog's status line (written once, at the moment
// the clipboard answered), and the title bar's node line.

const messageBodies = ["first", "second", "third"];
// configure() replaces the binding set rather than merging into it, so the
// ones this section still needs are named again.
configure({
  Overview: async () => ({
    reachable: true, nodeUrl: "http://127.0.0.1:7462",
    node: { id: "node_local", displayName: "local", platform: "darwin/arm64" },
    sessions: [], nodes: [], peers: [], counts: {},
  }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop,
  SetNodeAddress: noop, Pairing: async () => ({ availability: "unknown", candidates: [] }),
  OpenPairing: noop, ClosePairing: noop, ClearInbox: noop, Outbound: noop, Wakes: noop,
  PairRequests: async () => [], StartPairRequest: noop, ApprovePairRequest: noop,
  ConfirmPairRequest: noop, RejectPairRequest: noop,
  ServiceStatus: async () => ({ supported: false }), InstallService: noop, UninstallService: noop,
  LocalAddresses: async () => [], NodeSettings: async () => ({ error: "not needed" }),
  SaveNodeSettings: noop, RestartNode: noop, HostPlatform: async () => "darwin",
  Version: async () => ({ release: "unreleased" }),
  Inbox: async (sessionId) => ({
    sessionId,
    held: 3, capacity: 32, showing: 3, more: false, full: false,
    messages: messageBodies.map((body, index) => ({
      from: `node_other/agent-${index}`,
      createdAt: new Date(Date.now() - 60000).toISOString(),
      body,
    })),
  }),
  MCPConfig: async () => ({ text: '{"mcpServers":{}}' }),
  CopyText: async () => ({}),
});

app.setUILanguage("zh-Hant");
await app.openInbox("claude:i18n-drawer");
const zhMeta = el("inbox-meta").textContent;
if (!zhMeta.includes(ZH["inbox.meta"].replace("{held}", "3").replace("{capacity}", "32"))) {
  failures.push(`the inbox meta line is not the Chinese one to begin with: ${zhMeta}`);
}
app.setUILanguage("en");
const enMeta = el("inbox-meta").textContent;
if (han.test(enMeta)) {
  failures.push(`switching language left the inbox meta line in Chinese: ${enMeta}`);
}
if (!enMeta.includes(EN["inbox.meta"].replace("{held}", "3").replace("{capacity}", "32"))) {
  failures.push(`the inbox meta line was not re-derived in English: ${enMeta}`);
}
if (!enMeta.includes("claude:i18n-drawer")) {
  failures.push(`the inbox meta line lost the session it belongs to: ${enMeta}`);
}
const enBody = el("inbox-body").textContent;
if (han.test(enBody)) {
  failures.push(`switching language left the inbox list in Chinese: ${enBody.slice(0, 120)}`);
}
for (const word of enBody.split(/\s+/)) {
  if (Object.hasOwn(EN, word)) failures.push(`the inbox list shows the raw key ${word}`);
}
for (const body of messageBodies) {
  if (!enBody.includes(body)) failures.push(`the repaint dropped the message "${body}" from the list`);
}
if (!enBody.includes(EN["sender.claimsToBe"].trim())) {
  failures.push(`the sender lines were not re-derived in English: ${enBody.slice(0, 160)}`);
}

// The status line, after a copy that succeeded: the one sentence in this dialog
// the owner acts on.
app.setUILanguage("zh-Hant");
await app.openMCPConfig("claude:i18n-drawer");
if (el("mcp-status").textContent !== ZH["mcp.copied"]) {
  failures.push(`the MCP status line is not the Chinese "copied": ${el("mcp-status").textContent}`);
}
app.setUILanguage("en");
if (el("mcp-status").textContent !== EN["mcp.copied"]) {
  failures.push(`switching language left the MCP status line behind: ${el("mcp-status").textContent}`);
}
app.closeMCPConfig();
if (el("mcp-status").textContent !== "") {
  failures.push("closing the MCP dialog left its status line on screen");
}

// The title bar's line. index.html keys it "app.connecting", so a switch that
// stopped at paintStatic left a reachable node reading as one still being
// looked for.
await app.load();
app.setUILanguage("zh-Hant");
app.setUILanguage("en");
const nodeLine = el("node-line").textContent;
if (!nodeLine.includes("local")) {
  failures.push(`the node line does not name the node after a switch: ${nodeLine}`);
}
if (nodeLine === EN["app.connecting"] || nodeLine === ZH["app.connecting"] || rawKey(nodeLine)) {
  failures.push(`the node line fell back to index.html's placeholder: ${nodeLine}`);
}
if (han.test(nodeLine)) failures.push(`the node line is still Chinese: ${nodeLine}`);

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
