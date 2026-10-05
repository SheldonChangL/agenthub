// The share panel's rules (docs/ui-contract.md §3.2): what it shows for the
// sessions it is opened over, and what it writes back.
//
// The panel replaced an inline menu and a full dialog that said the same thing
// in two vocabularies. Its one safety rule is the one the dialog had to learn:
// it applies to whatever is selected, so a block the owner has not touched must
// keep each session's own value — otherwise opening it over a session whose
// settings no option names, or over several that disagree, would rewrite them
// to something nobody chose, and one of those flags starts turns in an agent
// with nobody watching.
//
//   node frontend/test/audience-dialog.mjs

import fs from "node:fs";
import { document } from "./dom-shim.mjs";
import { latestToast, toastButtons } from "./fixtures/toasts.mjs";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";

globalThis.document = document;
globalThis.setInterval = () => 0;
const { configure, boot } = await import("../src/app.js");

const failures = [];
const el = (id) => document.getElementById(id);
const fill = (template, params) => template.replace(/\{(\w+)\}/g, (_, name) => String(params[name]));
const settle = () => new Promise((resolve) => setTimeout(resolve, 10));
const walk = (node, visit) => {
  if (!node || typeof node !== "object") return;
  visit(node);
  for (const child of node.children ?? []) walk(child, visit);
};

const now = new Date().toISOString();
const session = (id, audience, provider = id.split(":")[0]) => ({
  id, provider, status: "idle", management: "managed", cwd: "/tmp", title: `title of ${id}`, audience, lastSeenAt: now,
});
const aud = (mode, nodes, on = {}) => ({
  mode, nodes, exportCwd: Boolean(on.cwd), acceptMessages: Boolean(on.msg), allowOutbound: Boolean(on.out), autoWake: Boolean(on.wake),
});

let sessions = [];
let nodes = [{ nodeId: "node_a", displayName: "alice" }, { nodeId: "node_b", displayName: "bob" }];
const calls = [];
let setAudienceFails = null;
configure({
  Overview: async () => ({
    reachable: true, node: { id: "node_local", displayName: "local", autoWake: true },
    sessions, nodes, peers: [], counts: { total: sessions.length },
  }),
  Discover: async () => ({}), TrustNode: async () => ({}), RevokeNode: async () => ({}), Heartbeat: async () => "",
  Pairing: async () => ({ availability: "unknown", candidates: [] }),
  OpenPairing: async () => ({}), ClosePairing: async () => ({}), PairRequests: async () => [],
  InboxCounts: async () => ({ ok: true, counts: {} }), NodeSettings: async () => ({ settings: {}, saved: {} }),
  ServiceStatus: async () => ({ supported: true, installed: true, running: true, pid: 1 }),
  SetAudience: async (ids, audience) => {
    calls.push({ ids: [...ids], audience: JSON.parse(JSON.stringify(audience)) });
    if (setAudienceFails) return setAudienceFails(ids, audience);
    for (const s of sessions) if (ids.includes(s.id)) s.audience = JSON.parse(JSON.stringify(audience));
    return { changed: ids.length, failed: 0, errors: [] };
  },
});
const module = boot({ start: false });

const radios = (name) => document.querySelectorAll(`input[name="${name}"]`);
const checkedOf = (name) => radios(name).find((radio) => radio.checked)?.value ?? "";
// Picks an option the way a press does: checked, then its handlers.
const choose = (name, value) => {
  for (const radio of radios(name)) radio.checked = radio.value === value;
  const picked = radios(name).find((radio) => radio.value === value);
  picked.onchange?.();
};
const hidden = (id) => el(id).classList.contains("hidden");
const whoNote = () => el("share-who-note").textContent;
const whatNote = () => el("share-what-note").textContent;
const boxes = () => {
  const found = [];
  walk(el("audience-node-list"), (node) => { if (node.className === "audience-node-box") found.push(node); });
  return found;
};
const box = (nodeId) => boxes().find((candidate) => candidate.value === nodeId);
const tick = (nodeId, on) => {
  const target = box(nodeId);
  target.checked = on;
  target.onchange?.();
};
const open = (list, { nodeAutoWake = true } = {}) => {
  sessions = list;
  module.state.sessions = list;
  module.state.nodes = nodes;
  module.state.nodesError = "";
  module.state.nodeAutoWake = nodeAutoWake;
  module.state.selected.clear();
  module.state.busy = false;
  if (module.sharePanelOpen()) module.closeSharePanel();
  module.openSharePanel(list.map((s) => s.id));
};
const apply = async () => {
  calls.length = 0;
  el("audience-apply").onclick();
  await settle();
};
const plain = (value) => JSON.parse(JSON.stringify(value));
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

/* ---------------- 1. one session, all paired, messages ---------------- */

{
  const all = session("codex:one", aud("all_paired", [], { msg: true, out: true }));
  open([all]);
  if (checkedOf("share-who") !== "all_paired") failures.push(`an all-paired session opened with who ${checkedOf("share-who")}`);
  if (checkedOf("share-what") !== "messages") failures.push(`a session that takes and sends messages opened with what ${checkedOf("share-what")}`);
  if (whatNote().includes(ZH["share.whatDiffers"].slice(0, 5))) failures.push(`a session that is exactly 「可留訊息」 got a whatDiffers line: ${whatNote()}`);
  if (el("audience-title").textContent !== fill(ZH["share.titleOne"], { title: all.title })) {
    failures.push(`the title reads ${el("audience-title").textContent}`);
  }
  if (!hidden("audience-nodes")) failures.push("the machine list is open over an all-paired session");
  if (hidden("share-what-block")) failures.push("the what block is hidden over a shared session");
  if (el("audience-apply").disabled) failures.push("套用 is disabled over an all-paired session");
  module.closeSharePanel();
}

/* ---------------- 2. flags no option names stay as they are ---------------- */

// Selected, accept only: ticks 「指定機器」 and the one machine, ticks
// 「可留訊息」 because that is the nearest, and says what is really on. Applied
// without touching anything it writes the session back as it was — not with
// allowOutbound switched on under it.
{
  const only = session("codex:accept", aud("selected", ["node_a"], { msg: true }));
  open([only]);
  if (checkedOf("share-who") !== "selected") failures.push(`a selected session opened with who ${checkedOf("share-who")}`);
  if (!box("node_a")?.checked || box("node_b")?.checked) failures.push("the machine boxes do not match the session's machines");
  if (hidden("audience-nodes")) failures.push("the machine list is hidden over a selected session");
  if (checkedOf("share-what") !== "messages") failures.push(`an accept-only session opened with what ${checkedOf("share-what")}`);
  if (!whatNote().includes(fill(ZH["share.whatDiffers"], { detail: fill(ZH["share.detail"], { accept: ZH["share.on"], reply: ZH["share.off"], wake: ZH["share.off"] }) }))) {
    failures.push(`the real flags are not said: ${whatNote()}`);
  }
  const original = plain(only.audience);
  await apply();
  if (calls.length !== 1 || !same(calls[0].audience, original) || !same(calls[0].ids, ["codex:accept"])) {
    failures.push(`applying an untouched panel wrote ${JSON.stringify(calls)}, want the session as it was ${JSON.stringify(original)}`);
  }
  if (module.sharePanelOpen()) failures.push("the panel stayed open after a write that went through");
}

// Clicking the option — even the one already ticked — writes the option's own
// definition: 「可留訊息」 is both directions.
{
  const only = session("codex:accept", aud("selected", ["node_a"], { msg: true }));
  open([only]);
  const messages = radios("share-what").find((radio) => radio.value === "messages");
  messages.onclick?.();
  if (whatNote().includes("目前實際是")) failures.push("the note about the real flags stays after the owner picked an option");
  await apply();
  const want = aud("selected", ["node_a"], { msg: true, out: true });
  if (!same(calls[0]?.audience, want)) failures.push(`clicking 「可留訊息」 wrote ${JSON.stringify(calls[0]?.audience)}, want ${JSON.stringify(want)}`);
}

/* ---------------- 3. not shared, with a working directory ---------------- */

{
  const quiet = session("codex:quiet", aud("none", [], { cwd: true }));
  open([quiet]);
  if (checkedOf("share-who") !== "none") failures.push(`an unshared session opened with who ${checkedOf("share-who")}`);
  if (!hidden("share-what-block")) failures.push("the what block is shown over an unshared session");
  if (!el("audience-cwd").checked || el("audience-cwd").indeterminate) failures.push("the working-directory box does not show the session's own value");
  choose("share-who", "all_paired");
  if (hidden("share-what-block")) failures.push("choosing who can see it did not bring the what block back");
  if (checkedOf("share-what") !== "messages") failures.push(`an unshared session opened with what ${checkedOf("share-what")}, want 「可留訊息」`);
  await apply();
  const want = aud("all_paired", [], { cwd: true, msg: true, out: true });
  if (!same(calls[0]?.audience, want)) failures.push(`sharing an unshared session wrote ${JSON.stringify(calls[0]?.audience)}, want ${JSON.stringify(want)}`);
}

// Selected with no nodes is nobody, so it opens as 「不分享」.
{
  open([session("codex:nobody", aud("selected", [], { msg: true }))]);
  if (checkedOf("share-who") !== "none") failures.push(`a selected session with no machines opened with who ${checkedOf("share-who")}`);
  module.closeSharePanel();
}

/* ---------------- 4. several that disagree ---------------- */

{
  const a = session("codex:a", aud("all_paired", [], { cwd: true, msg: true, out: true }));
  const b = session("codex:b", aud("none", []));
  open([a, b]);
  if (checkedOf("share-who") !== "") failures.push(`two sessions shared differently opened with who ${checkedOf("share-who")}`);
  if (!whoNote().includes(ZH["share.mixedWho"])) failures.push(`a mixed selection is not told so: ${whoNote()}`);
  if (checkedOf("share-what") !== "") failures.push(`two sessions that allow different things opened with what ${checkedOf("share-what")}`);
  if (!whatNote().includes(ZH["share.mixedWhat"])) failures.push(`a mixed selection's permissions are not said to differ: ${whatNote()}`);
  if (!el("audience-cwd").indeterminate || el("audience-cwd").checked) failures.push("the working-directory box is not indeterminate over sessions that differ");
  if (el("audience-title").textContent !== fill(ZH["share.titleMany.other"], { n: 2 })) failures.push(`the title reads ${el("audience-title").textContent}`);
  const before = [plain(a.audience), plain(b.audience)];
  await apply();
  if (!same(sessions.map((s) => s.audience), before)) {
    failures.push(`an untouched panel over two different sessions wrote ${JSON.stringify(sessions.map((s) => s.audience))}, want each as it was`);
  }
}

// A selection that disagrees about permissions as well as audience: the what
// group shows nothing ticked and says each is kept.
{
  const a = session("codex:a", aud("selected", ["node_a"], { msg: true }));
  const b = session("codex:b", aud("all_paired", [], { msg: true, out: true, wake: true }));
  open([a, b]);
  if (checkedOf("share-who") !== "" || checkedOf("share-what") !== "") failures.push("a selection that disagrees twice ticked an option");
  // Touching only 「誰看得到」 keeps each session's own flags.
  choose("share-who", "all_paired");
  await apply();
  if (!same(plain(a.audience), aud("all_paired", [], { msg: true })) || !same(plain(b.audience), aud("all_paired", [], { msg: true, out: true, wake: true }))) {
    failures.push(`changing only who rewrote the flags: ${JSON.stringify(sessions.map((s) => s.audience))}`);
  }
}

/* ---------------- 5. Claude Code cannot be woken ---------------- */

{
  const claude = session("claude:one", aud("all_paired", [], { msg: true, out: true, wake: true }));
  open([claude]);
  if (!el("share-what-wake").disabled) failures.push("the wake option is live over a Claude-only selection");
  if (!whatNote().includes(ZH["popover.wakeClaudeOnly"])) failures.push(`a Claude-only selection is not told why waking is off: ${whatNote()}`);
  if (!whatNote().includes(ZH["audience.autoWakeWillTurnOff"])) failures.push(`a Claude session with waking on is not told applying turns it off: ${whatNote()}`);
  if (checkedOf("share-what") === "wake") failures.push("a Claude session opened on 「可留訊息並喚醒」");
  await apply();
  if (claude.audience.autoWake !== false) failures.push("applying over a Claude session left autoWake on");
  if (!claude.audience.acceptMessages || !claude.audience.allowOutbound) failures.push("applying over a Claude session lost its message flags");

  // A Codex session next to it can be woken.
  open([session("codex:other", aud("all_paired", [], { msg: true, out: true }))]);
  if (el("share-what-wake").disabled) failures.push("the wake option is disabled over a Codex session");
  if (whatNote().includes(ZH["popover.wakeClaudeOnly"])) failures.push("a Codex session was told it cannot be woken");
}

// Claude and Codex together: waking can be picked, and it says which only
// take messages; the write turns it on for Codex alone.
{
  const claude = session("claude:mix", aud("all_paired", [], { msg: true, out: true }));
  const codex = session("codex:mix", aud("all_paired", [], { msg: true, out: true }));
  open([claude, codex]);
  if (el("share-what-wake").disabled) failures.push("the wake option is disabled over a mixed selection");
  choose("share-what", "wake");
  if (!whatNote().includes(fill(ZH["share.wakeSomeClaude.one"], { n: 1 }))) failures.push(`a mixed selection is not told which one only takes messages: ${whatNote()}`);
  await apply();
  if (codex.audience.autoWake !== true || claude.audience.autoWake !== false) {
    failures.push(`waking was written Codex ${codex.audience.autoWake}, Claude ${claude.audience.autoWake}; want true and false`);
  }
}

/* ---------------- 6. a node that will not wake anything ---------------- */

{
  open([session("codex:nodeoff", aud("all_paired", [], { msg: true, out: true }))], { nodeAutoWake: false });
  choose("share-what", "wake");
  if (!whatNote().includes(ZH["share.wakeNodeOff"])) failures.push(`waking without the node's auto-wake is not said: ${whatNote()}`);
  choose("share-what", "messages");
  if (whatNote().includes(ZH["share.wakeNodeOff"])) failures.push("the node-off line stays after waking is unpicked");
  module.closeSharePanel();
}

/* ---------------- 7. grants the read did not list (#194) ---------------- */

{
  nodes = [{ nodeId: "node_paired", displayName: "bench" }];
  const granted = session("codex:granted", aud("selected", ["node_paired", "node_gone"], { msg: true, out: true }));
  open([granted]);
  if (!box("node_paired")?.checked) failures.push("the paired grant was not ticked");
  if (!box("node_gone")?.checked) failures.push("a grant to a machine the read did not list has no ticked box");
  const list = el("audience-node-list").serialize();
  if (!list.includes(ZH["audience.unlistedNode"])) failures.push(`the unlisted grant is not marked as such: ${list}`);
  if (!list.includes("node_gone")) failures.push("the unlisted grant's id is nowhere in its row");
  if (!/title="node_gone"/.test(list)) failures.push("the unlisted grant's id is not in its tooltip");
  // Applied untouched, it is kept; unticked on purpose, it is withdrawn.
  await apply();
  if (!same(calls[0]?.audience.nodes, ["node_paired", "node_gone"])) failures.push(`an untouched panel dropped a grant: ${JSON.stringify(calls[0]?.audience.nodes)}`);
  open([session("codex:granted2", aud("selected", ["node_paired", "node_gone"], { msg: true, out: true }))]);
  tick("node_gone", false);
  await apply();
  if (!same(calls[0]?.audience.nodes, ["node_paired"])) failures.push(`unticking the unlisted grant wrote ${JSON.stringify(calls[0]?.audience.nodes)}`);
  // Several sessions never list one session's unlisted grant.
  open([granted, session("codex:other", aud("selected", ["node_gone"], { msg: true }))]);
  if (el("audience-node-list").serialize().includes("node_gone")) failures.push("a multi-session panel listed an unlisted grant");

  // The read that really produces that row: Overview reached the node but the
  // pairing list failed and came back as no nodes.
  nodes = [];
  open([session("codex:granted3", aud("selected", ["node_gone"], { msg: true, out: true }))]);
  module.state.nodesError = "trusted nodes: 500 Internal Server Error";
  module.renderSharePanel();
  if (!whoNote().includes(fill(ZH["audience.nodesReadFailed"], { error: module.state.nodesError }))) failures.push(`a failed pairing-list read is not said: ${whoNote()}`);
  if (whoNote().includes(ZH["popover.noNodes"])) failures.push("a failed pairing-list read was described as no machine being paired");
  if (!box("node_gone")?.checked) failures.push("the grant is not kept while the pairing list is unreadable");
  module.state.nodesError = "";
  module.closeSharePanel();
  nodes = [{ nodeId: "node_a", displayName: "alice" }, { nodeId: "node_b", displayName: "bob" }];
}

// Nothing paired yet: the panel says so and offers the way to pair, and sharing
// is still allowed (a machine paired later sees it).
{
  nodes = [];
  open([session("codex:lonely", aud("none", []))]);
  if (!whoNote().includes(ZH["popover.noNodes"])) failures.push(`with nothing paired the panel does not say so: ${whoNote()}`);
  let pair = null;
  walk(el("share-who-note"), (node) => { if (node.tagName === "button" && node.textContent === ZH["popover.pairAction"]) pair = node; });
  if (!pair) failures.push("with nothing paired the panel offers no way to pair");
  else {
    pair.onclick();
    await settle();
    if (module.sharePanelOpen()) failures.push("配對另一台機器 left the panel open");
    if (module.state.view !== "network") failures.push(`配對另一台機器 left the view at ${module.state.view}`);
    module.closePairingDrawer?.();
    module.state.view = "local";
    module.render();
  }
  nodes = [{ nodeId: "node_a", displayName: "alice" }, { nodeId: "node_b", displayName: "bob" }];
}

/* ---------------- 8. 「指定機器」 with no machine ticked ---------------- */

{
  open([session("codex:pick", aud("none", []))]);
  choose("share-who", "selected");
  if (!el("audience-apply").disabled) failures.push("套用 is live with 「指定機器」 and no machine ticked");
  if (el("share-apply-why").textContent !== ZH["share.pickAMachine"]) failures.push(`the reason reads ${el("share-apply-why").textContent}`);
  tick("node_b", true);
  if (el("audience-apply").disabled) failures.push("套用 stayed disabled after a machine was ticked");
  if (el("share-apply-why").textContent !== "") failures.push("the reason stayed after a machine was ticked");
  await apply();
  if (!same(calls[0]?.audience.nodes, ["node_b"]) || calls[0]?.audience.mode !== "selected") failures.push(`wrote ${JSON.stringify(calls[0]?.audience)}, want selected [node_b]`);
}

/* ---------------- 9. the two explicit choices ---------------- */

// 「不分享」 is every flag off, the working directory included — but only when
// the owner chose it.
{
  const shared = session("codex:stop", aud("selected", ["node_a"], { cwd: true, msg: true, out: true, wake: true }));
  open([shared]);
  choose("share-who", "none");
  if (!hidden("share-what-block")) failures.push("the what block stays over 「不分享」");
  await apply();
  if (!same(calls[0]?.audience, aud("none", []))) failures.push(`「不分享」 wrote ${JSON.stringify(calls[0]?.audience)}`);
  if (!latestToast(document).textContent.includes(fill(ZH["audience.applied.none.one"], { n: 1 }))) failures.push(`the toast after 「不分享」 reads ${latestToast(document).textContent}`);
}

// 「只看得到」 is the three message flags off, with the machines kept.
{
  const shared = session("codex:view", aud("selected", ["node_a"], { cwd: true, msg: true, out: true, wake: true }));
  open([shared]);
  choose("share-what", "view");
  await apply();
  const want = aud("selected", ["node_a"], { cwd: true });
  if (!same(calls[0]?.audience, want)) failures.push(`「只看得到」 wrote ${JSON.stringify(calls[0]?.audience)}, want ${JSON.stringify(want)}`);
  // And such a session opens as itself.
  open([session("codex:viewonly", aud("all_paired", [], {}))]);
  if (checkedOf("share-what") !== "view") failures.push(`a shared session with every flag off opened with what ${checkedOf("share-what")}`);
  if (whatNote().includes("目前實際是")) failures.push(`a view-only session got a whatDiffers line: ${whatNote()}`);
  // allowOutbound alone cannot be received from, so it is 「只看得到」 too.
  open([session("codex:outonly", aud("all_paired", [], { out: true }))]);
  if (checkedOf("share-what") !== "view") failures.push(`an outbound-only session opened with what ${checkedOf("share-what")}`);
  if (!whatNote().includes("目前實際是")) failures.push(`an outbound-only session's real flags are not said: ${whatNote()}`);
}

/* ---------------- 10. nothing is carried to the next opening ---------------- */

{
  const first = session("codex:first", aud("none", []));
  const second = session("codex:second", aud("all_paired", [], { cwd: true, msg: true }));
  open([first]);
  choose("share-who", "selected");
  tick("node_a", true);
  el("audience-cwd").checked = true;
  el("audience-cwd").onchange();
  el("audience-close").onclick();
  if (module.sharePanelOpen()) failures.push("取消 left the panel open");
  open([second]);
  if (checkedOf("share-who") !== "all_paired" || checkedOf("share-what") !== "messages") failures.push("the second opening did not show the second session");
  if (box("node_a")?.checked) failures.push("a machine ticked in the last opening is still ticked");
  const original = plain(second.audience);
  await apply();
  if (!same(calls[0]?.audience, original)) failures.push(`an untouched second opening wrote ${JSON.stringify(calls[0]?.audience)}, want ${JSON.stringify(original)}`);
}

/* ---------------- 11. what the panel never says ---------------- */

// The wake caveat belongs on the settings page and the wizard, not here.
{
  open([session("codex:wake", aud("all_paired", [], { msg: true, out: true }))]);
  choose("share-what", "wake");
  if (el("audience-modal").textContent.includes(ZH["wake.caveat"])) failures.push("the panel carries the wake caveat");
}

// A write that partly fails keeps the panel open and the sessions as they are.
{
  const a = session("codex:fail-a", aud("none", [], { cwd: true }));
  const b = session("codex:fail-b", aud("none", []));
  open([a, b]);
  choose("share-who", "all_paired");
  setAudienceFails = (ids) => ({ changed: 0, failed: ids.length, errors: ["refused by the node"] });
  await apply();
  setAudienceFails = null;
  if (!module.sharePanelOpen()) failures.push("a write that failed closed the panel");
  const toast = latestToast(document);
  if (toast.kind !== "error" || !toast.textContent.includes("refused by the node")) failures.push(`a failed write said ${toast.kind}: ${toast.textContent}`);
  if (toastButtons(toast.node).some((button) => button.textContent === ZH["popover.undo"])) failures.push("a write that changed nothing offered 復原");
  if (el("audience-apply").disabled) failures.push("套用 stayed disabled after a failed write");
  module.closeSharePanel();
}

// Every key the panel names exists, and the retired ones are gone from the markup.
{
  const markup = fs.readFileSync(new URL("../index.html", import.meta.url), "utf8");
  for (const gone of ["audience-popover", "btn-unpublish", "audience-messages", "audience-outbound", "audience-autowake", "audience-advanced", "audience-presets"]) {
    if (markup.includes(`id="${gone}"`)) failures.push(`index.html still has #${gone}`);
  }
  for (const radio of ["share-who-none", "share-who-all", "share-who-selected", "share-what-view", "share-what-messages", "share-what-wake"]) {
    if (!markup.includes(`id="${radio}"`)) failures.push(`index.html has no #${radio}`);
  }
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("the share panel: one session opens as itself, an untouched block keeps each session's own value, "
  + "and Claude Code is never written as waking");
