// The audience dialog must not carry a flag from one use to the next.
//
// It applies to whatever is selected and reads its values straight from the
// boxes, so a box left ticked from last time is a setting about to be applied
// to a different set of sessions. That was survivable while the flags only
// governed what could be read; one of them now starts turns in an agent with
// nobody watching.
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

// The box itself stays usable throughout: an owner may set a session up before
// restarting the node, and a disabled box would take that away.
if (el("audience-autowake").disabled) {
  failures.push("the auto-wake box was disabled instead of explained");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("the audience dialog starts every flag off and names what auto-wake depends on");
