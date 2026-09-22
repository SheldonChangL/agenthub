// The audience dialog must not carry a flag from one use to the next, and the
// three presets in front of the flags have to agree with them.
//
// It applies to whatever is selected and reads its values straight from the
// boxes, so a box left ticked from last time is a setting about to be applied
// to a different set of sessions. That was survivable while the flags only
// governed what could be read; one of them now starts turns in an agent with
// nobody watching. Exactly one session is the case where nothing is left over:
// the values shown are that session's own, read from the overview, and blanking
// them meant changing 「誰看得到」 silently withdrew every flag it had.
//
//   node frontend/test/audience-dialog.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;
const { configure, boot } = await import("../src/app.js");

const failures = [];
const el = (id) => document.getElementById(id);
const noop = async () => ({});
configure({
  Overview: async () => ({ sessions: [], nodes: [], node: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }),
  OpenPairing: noop, ClosePairing: noop,
});
const module = boot({ start: false });

const flags = ["audience-cwd", "audience-messages", "audience-outbound", "audience-autowake"];

// Somebody opens the dialog and ticks everything, for one session.
module.openAudienceModal();
for (const id of flags) el(id).checked = true;
if (!module.readAudienceForm().autoWake) {
  failures.push("the dialog does not read the auto-wake box at all");
}

// They come back later, for a different session, and open it again.
module.openAudienceModal();
for (const id of flags) {
  if (el(id).checked) {
    failures.push(`${id} was still ticked when the dialog reopened`);
  }
}
for (const [name, value] of Object.entries(module.readAudienceForm())) {
  if (value === true) {
    failures.push(`${name} came back true from a freshly opened dialog`);
  }
}

/* ---------------- the three presets in front of the four flags ------------ */

// A preset writes the boxes. Nothing else does, and a box never writes another
// box — so what the dialog applies is always what its own advanced section
// shows, whichever half the owner used.
const presetOf = () =>
  [...document.querySelectorAll('input[name="audience-preset"]')].find((radio) => radio.checked)?.value ?? "";

module.applyAudiencePreset("messages");
const afterMessages = module.readAudienceForm();
if (!afterMessages.acceptMessages || afterMessages.autoWake || afterMessages.exportCwd
  || afterMessages.allowOutbound) {
  failures.push(`"let them leave messages" wrote ${JSON.stringify(afterMessages)}`);
}
if (presetOf() !== "messages") {
  failures.push(`the preset radio reads ${presetOf()} after writing the messages preset`);
}

module.applyAudiencePreset("wake");
const afterWake = module.readAudienceForm();
if (!afterWake.acceptMessages || !afterWake.autoWake) {
  failures.push(`"and wake it" did not turn both boxes on: ${JSON.stringify(afterWake)}`);
}

module.applyAudiencePreset("view");
const afterView = module.readAudienceForm();
if (Object.values(afterView).some((value) => value === true)) {
  failures.push(`"let them see it only" left a flag on: ${JSON.stringify(afterView)}`);
}

// A combination no preset names unsets all three radios and says so, rather
// than leaving one of them checked over flags it does not describe.
el("audience-outbound").checked = true;
module.syncAudiencePreset();
if (presetOf() !== "") {
  failures.push(`a hand-set flag left the ${presetOf()} preset selected`);
}
if (!el("audience-preset-note").textContent.includes("自訂")) {
  failures.push(`a hand-set flag was not reported as custom: ${el("audience-preset-note").textContent}`);
}
if (module.presetForFlags({ acceptMessages: true, autoWake: true }) !== "wake") {
  failures.push("the wake combination is not recognised as a preset");
}

/* ---------------- one session opens as itself ----------------------------- */

// Reopening with exactly one session selected shows that session's own
// audience. Blanking it here is not caution: the dialog applies everything it
// shows, so an owner changing who can see a session used to withdraw the flags
// it already had, in the same press, without a word.
const published = {
  id: "codex:already",
  provider: "codex",
  audience: { mode: "all_paired", nodes: [], exportCwd: true, acceptMessages: true, allowOutbound: false, autoWake: false },
};
module.state.sessions = [published];
module.state.selected.clear();
module.state.selected.add(published.id);
module.openAudienceModal();
const loaded = module.readAudienceForm();
if (loaded.mode !== "all_paired") {
  failures.push(`one session opened at mode ${loaded.mode}, not its own`);
}
if (!loaded.exportCwd || !loaded.acceptMessages || loaded.allowOutbound || loaded.autoWake) {
  failures.push(`one session opened with ${JSON.stringify(loaded)}, not its own flags`);
}
if (el("audience-preset-note").textContent.includes("歸零") ||
  el("audience-preset-note").textContent.includes("全關")) {
  failures.push("a single selection was told its flags had been reset");
}

// Two sessions can disagree, and there is no honest way to show one state for
// many — so they open off, and the dialog says so.
const second = { id: "codex:other", provider: "codex", audience: { mode: "none" } };
module.state.sessions = [published, second];
module.state.selected.add(second.id);
module.openAudienceModal();
const many = module.readAudienceForm();
if (many.mode !== "none" || Object.values(many).some((value) => value === true)) {
  failures.push(`a multiple selection did not open closed: ${JSON.stringify(many)}`);
}
if (!el("audience-preset-note").textContent.includes("全關")) {
  failures.push(`a multiple selection did not say the flags start off: ${el("audience-preset-note").textContent}`);
}

// The dependencies of the auto-wake box, said next to the auto-wake box.
//
// Ticking it does nothing unless the node itself was started with -auto-wake,
// and for a Claude Code session not even then: that path needs the session's
// own agenthub-mcp -channel, and the push was measured arriving at Claude Code
// and never being injected. Every one of those failures looks identical from
// the owner's chair — the message sits in the inbox — so the dialog has to name
// which one applies before they tick the box and wait.
const noteText = () => el("audience-autowake-note").textContent;

const withSelection = (autoWake, sessions) => {
  module.state.nodeAutoWake = autoWake;
  module.state.sessions = sessions;
  module.state.selected.clear();
  for (const session of sessions) module.state.selected.add(session.id);
  module.openAudienceModal();
  return noteText();
};

const codex = { id: "codex:one", provider: "codex" };
const claude = { id: "claude:two", provider: "claude" };

const nodeOff = withSelection(false, [codex]);
if (!nodeOff.includes("-auto-wake") || !nodeOff.includes("不會有任何 session 被叫醒")) {
  failures.push(`a node without -auto-wake is not named: ${nodeOff}`);
}
if (nodeOff.includes("app-server")) {
  failures.push("the node-off note also promised the Codex path would work");
}

const codexOnly = withSelection(true, [codex]);
if (!codexOnly.includes("app-server") || !codexOnly.includes("真機驗過")) {
  failures.push(`an all-Codex selection is not told the wake works: ${codexOnly}`);
}
if (codexOnly.includes("Claude Code")) {
  failures.push("an all-Codex selection was warned about Claude Code");
}

const claudeOnly = withSelection(true, [claude]);
if (!claudeOnly.includes("-channel") || !claudeOnly.includes("channel-push-not-observed.md")) {
  failures.push(`a Claude selection is not told the push was never observed: ${claudeOnly}`);
}
if (claudeOnly.includes("app-server")) {
  failures.push("a Claude-only selection was told the Codex path applies");
}

const mixed = withSelection(true, [codex, claude]);
if (!mixed.includes("app-server") || !mixed.includes("channel-push-not-observed.md")) {
  failures.push(`a mixed selection does not get both sentences: ${mixed}`);
}

// The box stays usable wherever the obstacle is one an owner can clear: a node
// started without -auto-wake is restarted, and setting a session up first is
// reasonable. A selection that is nothing but Claude Code is the one case that
// no restart fixes — the push was measured arriving and never being injected —
// so there the box is turned off rather than explained, and the preset that
// would tick it goes with it.
if (el("audience-autowake").disabled) {
  failures.push("the auto-wake box was disabled on a mixed selection instead of explained");
}
withSelection(false, [codex]);
if (el("audience-autowake").disabled) {
  failures.push("a node without -auto-wake disabled the box; a restart is the remedy, not a dead control");
}
withSelection(true, [claude]);
if (!el("audience-autowake").disabled) {
  failures.push("an all-Claude selection left the wake box live, where ticking it can never do anything");
}
if (!el("audience-preset-wake").disabled) {
  failures.push("an all-Claude selection left the wake preset selectable");
}
if (module.readAudienceForm().autoWake) {
  failures.push("an all-Claude selection could still apply auto-wake");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("the audience dialog presets write the flags, one session opens as itself, "
  + "and auto-wake is off where it cannot work");
