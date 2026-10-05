// The share panel round (docs/ui-contract.md §3.2, §3.3, §3.5): sharing a
// session from its own row or from the selection bar, the background service
// fixed from the row that says it is broken, the pairing drawer's step strip,
// and the view tabs as buttons.
//
// The owner's rule was that a shared session should never be doing something
// the owner did not choose. So most of what is checked here is that: which
// machines a write goes to, that a block the owner did not touch keeps each
// session's own value, that the working directory is a box of its own, and
// that an undo puts back exactly what each session had — including the
// machines it named.
//
// The whole module runs, wiring and intervals included; the intervals are
// captured so the fifteen-second tick fires when the check says, and
// waitForNode's 750ms pauses are answered at once.
//
//   node frontend/test/inline-publish.mjs

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { document } from "./dom-shim.mjs";
import { answerConfirms } from "./fixtures/confirm-dialog.mjs";
import { attentionRows, latestToast, toastButtons, toastNodes } from "./fixtures/toasts.mjs";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";
import { TEXT as EN } from "../src/i18n/en.js";

globalThis.document = document;
// Every question asked, and how it was asked; answered yes unless a check
// says otherwise.
const questions = [];
let confirmAnswer = () => true;
answerConfirms(document, (question) => {
  questions.push({
    question,
    danger: document.getElementById("confirm-ok").className === "danger",
    focusedCancel: document.activeElement === document.getElementById("confirm-cancel"),
  });
  return confirmAnswer(question);
});

const failures = [];
const el = (id) => document.getElementById(id);
const fill = (template, params) => template.replace(/\{(\w+)\}/g, (_, name) => String(params[name]));

const ticks = [];
globalThis.setInterval = (fn, ms) => {
  ticks.push({ fn, ms });
  return ticks.length;
};
const realSetTimeout = globalThis.setTimeout;
globalThis.setTimeout = (fn, ms, ...rest) => realSetTimeout(fn, ms === 750 ? 0 : ms, ...rest);
const settle = () => new Promise((resolve) => realSetTimeout(resolve, 30));

const now = new Date().toISOString();
const session = (id, audience, provider = id.split(":")[0]) => ({
  id, provider, status: "idle", management: "managed", cwd: "/tmp", title: id,
  audience, lastSeenAt: now,
});
const flags = (on = {}) => ({
  exportCwd: Boolean(on.cwd), acceptMessages: Boolean(on.msg), allowOutbound: Boolean(on.out), autoWake: Boolean(on.wake),
});

// Five situations the menu has to tell apart.
const fresh = () => [
  session("codex:quiet", { mode: "none", nodes: [], ...flags({ cwd: true }) }),
  session("codex:chosen", { mode: "selected", nodes: ["node_a", "node_b"], ...flags({ cwd: true, msg: true }) }),
  session("codex:emptychosen", { mode: "selected", nodes: [], ...flags({ msg: true }) }),
  session("codex:allwake", { mode: "all_paired", nodes: [], ...flags({ msg: true, out: true, wake: true }) }),
  session("codex:silent", { mode: "all_paired", nodes: [], ...flags() }),
  session("claude:only", { mode: "none", nodes: [], ...flags() }),
];
let sessions = fresh();
let nodes = [{ nodeId: "node_a", displayName: "alice" }, { nodeId: "node_b", displayName: "bob" }];
// Whether the node answers Overview. A node that does not keeps the last lists
// on screen (#114), which is what the service checks below need: nothing
// running, so a one-press install is the right press.
let reachable = true;
const overview = () => (reachable ? {
  node: { id: "node_local", displayName: "local", platform: "test", fingerprint: "AAAA", autoWake: true },
  sessions, nodes, peers: [], counts: { total: sessions.length }, nodeUrl: "http://127.0.0.1:7462", reachable: true,
} : {
  sessions: [], nodes: [], peers: [], counts: {}, nodeUrl: "http://127.0.0.1:7462", reachable: false,
  error: "dial tcp 127.0.0.1:7462: connection refused",
});

// SetAudience as the node does it: the audience is copied onto each session,
// and the call itself is recorded.
const setCalls = [];
let setAudienceFails = null;
// A browser drops the keyboard from a button that is disabled under it, which
// withBusy does for the length of the write; the shim does not, so the write
// drops it here, and a check can see whether it is given back.
let blurDuringWrite = false;
const SetAudience = async (ids, audience) => {
  setCalls.push({ ids: [...ids], audience: JSON.parse(JSON.stringify(audience)) });
  if (blurDuringWrite) document.activeElement?.blur();
  if (setAudienceFails) return setAudienceFails(ids, audience);
  for (const s of sessions) if (ids.includes(s.id)) s.audience = JSON.parse(JSON.stringify(audience));
  return { changed: ids.length, failed: 0, errors: [] };
};

let serviceAnswer = { supported: true, installed: true, running: true, pid: 7 };
// Set to an error to make the next ServiceStatus read throw.
let serviceReadFails = null;
// Set to { promise, open } to hold the next ServiceStatus reads until open().
let serviceReadGate = null;
const gate = () => {
  let open;
  const promise = new Promise((resolve) => { open = resolve; });
  return { promise, open };
};
let serviceReads = 0;
const serviceCalls = [];
let installFails = false;
let overviewCalls = 0;
let pairRequestsAnswer = [];
const pairDecisions = [];

const { configure, boot } = await import("../src/app.js");
configure({
  Overview: async () => { overviewCalls++; return overview(); },
  ServiceStatus: async () => {
    serviceReads++;
    if (serviceReadGate) await serviceReadGate.promise;
    if (serviceReadFails) throw serviceReadFails;
    return serviceAnswer;
  },
  InboxCounts: async () => ({ ok: true, counts: {} }),
  Inbox: async (sessionId) => ({ sessionId, messages: [], held: 0, capacity: 500 }),
  Pairing: async () => ({ availability: "on", windowAvailable: true, state: { open: true, remainingSeconds: 200 }, candidates: [] }),
  PairRequests: async () => pairRequestsAnswer,
  OpenPairing: async () => ({ open: true }),
  ClosePairing: async () => ({ open: false }),
  NodeSettings: async () => ({ settings: {}, saved: {} }),
  Discover: async () => ({ claude: 0, codex: 0, total: 0, skipped: 0 }),
  SetAudience,
  InstallService: async (form) => {
    serviceCalls.push(["InstallService", form]);
    if (installFails) throw new Error("ah service install: exit status 1: launchctl refused");
    serviceAnswer = { supported: true, installed: true, running: true, pid: 9 };
    return { command: "ah service install", output: "installed" };
  },
  RestartService: async () => {
    serviceCalls.push(["RestartService"]);
    serviceAnswer = { supported: true, installed: true, running: true, pid: 11 };
    return { command: "ah service restart", output: "restarted" };
  },
  RestartNode: async () => {
    serviceCalls.push(["RestartNode"]);
    return { command: "ah service restart", output: "restarted" };
  },
  ApprovePairRequest: async (id) => { pairDecisions.push(["approve", id]); return { id, direction: "incoming", state: "approved", nextStep: "" }; },
  ConfirmPairRequest: async (id) => { pairDecisions.push(["confirm", id]); return { id, direction: "outgoing", state: "approved", nextStep: "" }; },
  RejectPairRequest: async (id) => { pairDecisions.push(["reject", id]); return { id, state: "rejected", nextStep: "" }; },
  TrustNode: async () => ({}), RevokeNode: async () => ({}), Heartbeat: async () => "",
  ClearInbox: async () => ({}), CopyText: async () => ({}), LocalAddresses: async () => [],
});
const app = boot();
await settle();
if (ticks.length !== 4) failures.push(`${ticks.length} intervals registered, want the four §4 allows`);

const clearToasts = () => {
  for (const node of toastNodes(document)) toastButtons(node).at(-1).onclick();
};
const reload = async () => {
  await app.load();
  await app.loadService();
  await settle();
};

const rowFor = (id) => el("rows").children.find((tr) => tr.session?.id === id);
const audienceButton = (id) => rowFor(id)?.sessionParts?.audiencePill;
const walk = (node, visit) => {
  if (!node || typeof node !== "object") return;
  visit(node);
  for (const child of node.children ?? []) walk(child, visit);
};
// The shim does not build #audience-modal's children from the markup, so the
// panel's words are read from the elements the module writes them into.
const panelText = () => ["audience-title", "audience-node-list", "share-who-note", "share-what-note", "share-apply-why"]
  .map((id) => el(id).textContent).join(" ");
const panelOpen = () => app.sharePanelOpen();
const radios = (name) => document.querySelectorAll(`input[name="${name}"]`);
const checkedOf = (name) => radios(name).find((radio) => radio.checked)?.value ?? "";
// Picks an option the way a press does: checked, then its handlers.
const choose = (name, value) => {
  for (const radio of radios(name)) radio.checked = radio.value === value;
  radios(name).find((radio) => radio.value === value).onchange?.();
};
const nodeBox = (nodeId) => {
  let found = null;
  walk(el("audience-node-list"), (node) => { if (node.className === "audience-node-box" && node.value === nodeId) found = node; });
  return found;
};
const tick = (nodeId, on) => {
  const target = nodeBox(nodeId);
  target.checked = on;
  target.onchange?.();
};
const press = async (button) => {
  button.onclick();
  await settle();
};
const openRow = (id) => {
  if (panelOpen()) app.closeSharePanel();
  audienceButton(id).onclick();
};
// What the owner does in the panel for a row: open it, choose, apply.
const share = async (id, { who, what, nodes: ticked = [], cwd } = {}) => {
  openRow(id);
  if (!panelOpen()) {
    failures.push(`the share panel for ${id} did not open`);
    return;
  }
  if (who) choose("share-who", who);
  for (const nodeId of ticked) tick(nodeId, true);
  if (what) choose("share-what", what);
  if (cwd !== undefined) {
    el("audience-cwd").checked = cwd;
    el("audience-cwd").onchange?.();
  }
  await press(el("audience-apply"));
};
// The same for several rows, from the selection bar.
const shareSelected = async (ids, choices = {}) => {
  app.state.selected.clear();
  for (const id of ids) app.state.selected.add(id);
  app.render();
  if (panelOpen()) app.closeSharePanel();
  el("btn-audience").onclick();
  if (choices.who) choose("share-who", choices.who);
  if (choices.what) choose("share-what", choices.what);
  await press(el("audience-apply"));
};
const lastSet = () => setCalls.at(-1);
const audienceOf = (id) => sessions.find((s) => s.id === id).audience;
const undoButton = (toast) => toastButtons(toast.node).find((button) => button.textContent === ZH["popover.undo"]);
app.state.view = "local";
app.render();

/* ---------------- 1. which machines, which flags ---------------- */

// An unshared session goes to the machines picked now; the working directory it
// had stays, and 「可留訊息」 is both directions.
setCalls.length = 0;
await share("codex:quiet", { who: "all_paired" });
{
  const call = lastSet();
  const want = { mode: "all_paired", nodes: [], exportCwd: true, acceptMessages: true, allowOutbound: true, autoWake: false };
  if (!call || JSON.stringify(call.audience) !== JSON.stringify(want) || JSON.stringify(call.ids) !== JSON.stringify(["codex:quiet"])) {
    failures.push(`sharing an unshared session wrote ${JSON.stringify(call)}, want ${JSON.stringify(want)} for it alone`);
  }
}
// A session shared with chosen machines keeps them when only 「可以做什麼」 changes.
await share("codex:chosen", { what: "wake" });
{
  const want = { mode: "selected", nodes: ["node_a", "node_b"], exportCwd: true, acceptMessages: true, allowOutbound: true, autoWake: true };
  if (JSON.stringify(lastSet()?.audience) !== JSON.stringify(want)) {
    failures.push(`留訊息並喚醒 on a session with chosen machines wrote ${JSON.stringify(lastSet()?.audience)}, want ${JSON.stringify(want)}`);
  }
}
// 「指定機器」 with no machines is nobody: it opens as 「不分享」, and sharing it
// goes to whoever the owner picks.
openRow("codex:emptychosen");
if (checkedOf("share-who") !== "none") failures.push(`a 「指定：無」 session's panel ticks who ${checkedOf("share-who")}, want none`);
app.closeSharePanel();
await share("codex:emptychosen", { who: "all_paired" });
if (lastSet()?.audience.mode !== "all_paired" || lastSet()?.audience.nodes.length !== 0) {
  failures.push(`sharing a 「指定：無」 session wrote ${JSON.stringify(lastSet()?.audience)}`);
}
// 「可留訊息」 on a waking session turns waking off, and keeps the machines.
await share("codex:allwake", { what: "messages" });
if (lastSet()?.audience.mode !== "all_paired" || lastSet()?.audience.autoWake) {
  failures.push(`可留訊息 on an all-paired waking session wrote ${JSON.stringify(lastSet()?.audience)}`);
}
// 「不分享」 clears the working directory too, and only when it is chosen.
await share("codex:chosen", { who: "none" });
{
  const want = { mode: "none", nodes: [], exportCwd: false, acceptMessages: false, allowOutbound: false, autoWake: false };
  if (JSON.stringify(lastSet()?.audience) !== JSON.stringify(want)) {
    failures.push(`不分享 wrote ${JSON.stringify(lastSet()?.audience)}, want ${JSON.stringify(want)}`);
  }
  // So sharing it again does not bring the directory back with it.
  await share("codex:chosen", { who: "all_paired" });
  if (lastSet()?.audience.exportCwd !== false) failures.push(`sharing after 不分享 wrote exportCwd ${lastSet()?.audience.exportCwd}`);
}
// The working directory is a box of its own and always shown: turning it on
// for a session that was not sending it writes it, and nothing else changes.
sessions = fresh();
await reload();
await share("codex:allwake", { cwd: true });
{
  const want = { mode: "all_paired", nodes: [], exportCwd: true, acceptMessages: true, allowOutbound: true, autoWake: true };
  if (JSON.stringify(lastSet()?.audience) !== JSON.stringify(want)) {
    failures.push(`ticking only the working directory wrote ${JSON.stringify(lastSet()?.audience)}, want ${JSON.stringify(want)}`);
  }
}
clearToasts();

// The selection bar has 「分享…」 and 「取消選取」 and nothing else; its button
// opens the same panel, about the whole selection.
{
  const markup = fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "index.html"), "utf8");
  if (markup.includes('id="btn-unpublish"') || markup.includes('id="audience-popover"')) failures.push("index.html still has the unpublish button or the inline menu");
  const bar = markup.slice(markup.indexOf('id="selectionbar"'), markup.indexOf("</div>", markup.indexOf('id="selectionbar"')));
  const buttons = [...bar.matchAll(/<button id="([^"]+)"/g)].map((match) => match[1]);
  if (JSON.stringify(buttons) !== JSON.stringify(["btn-audience", "btn-deselect"])) failures.push(`the selection bar has ${JSON.stringify(buttons)}, want 分享… and 取消選取 only`);
}
sessions = fresh();
await reload();
{
  const ids = ["codex:silent", "codex:chosen"];
  for (const id of ids) app.state.selected.add(id);
  app.render();
  el("btn-audience").onclick();
  if (!panelOpen()) failures.push("「分享…」 on the selection bar did not open the panel");
  else if (el("audience-title").textContent !== fill(ZH["share.titleMany.other"], { n: 2 })) failures.push(`the batch panel's title reads ${el("audience-title").textContent}`);
  app.closeSharePanel();
  el("btn-deselect").onclick();
  if (app.state.selected.size !== 0) failures.push("取消選取 left the selection");
  app.render();
}

// The row's own button is about that row only, whatever is selected.
{
  app.state.selected.add("codex:silent");
  app.render();
  openRow("codex:chosen");
  if (el("audience-title").textContent !== fill(ZH["share.titleOne"], { title: "codex:chosen" })) failures.push(`a row's panel title reads ${el("audience-title").textContent}`);
  app.closeSharePanel();
  app.state.selected.clear();
  app.render();
}

/* ---------------- 2. undo puts back each session's own audience ---------- */

sessions = fresh();
await reload();
{
  const before = JSON.parse(JSON.stringify(audienceOf("codex:chosen")));
  await share("codex:chosen", { who: "none" });
  const toast = latestToast(document);
  if (toast.kind !== "ok") failures.push(`a write that went through left a ${toast.kind} toast: ${toast.textContent}`);
  const undo = undoButton(toast);
  if (!undo) {
    failures.push(`the success toast has no 復原: ${toast.textContent}`);
  } else {
    setCalls.length = 0;
    await press(undo);
    const call = lastSet();
    if (JSON.stringify(call?.audience) !== JSON.stringify(before)) {
      failures.push(`復原 wrote ${JSON.stringify(call?.audience)}, want the session's own ${JSON.stringify(before)}`);
    }
    if (JSON.stringify(audienceOf("codex:chosen")) !== JSON.stringify(before)) {
      failures.push(`after 復原 the session holds ${JSON.stringify(audienceOf("codex:chosen"))}`);
    }
    if (latestToast(document).textContent !== ZH["popover.undone"]) {
      failures.push(`復原 answered ${latestToast(document).textContent}`);
    }
  }
  // An undo pressed while something else is being written says it was not done.
  await share("codex:chosen", { what: "messages" });
  const again = undoButton(latestToast(document));
  app.state.busy = true;
  setCalls.length = 0;
  again.onclick();
  await settle();
  app.state.busy = false;
  if (setCalls.length !== 0) failures.push("an undo pressed while busy wrote anyway");
  if (latestToast(document).textContent !== ZH["popover.undoBusy"]) failures.push(`an undo dropped while busy said ${latestToast(document).textContent}`);
}
clearToasts();

// Several sessions with different audiences go back to different audiences:
// one call per distinct audience, each with its own sessions.
sessions = fresh();
await reload();
{
  const ids = ["codex:quiet", "codex:chosen", "codex:allwake"];
  const before = Object.fromEntries(ids.map((id) => [id, JSON.parse(JSON.stringify(audienceOf(id)))]));
  setCalls.length = 0;
  // Only 「誰看得到」 is chosen, so each session keeps its own flags and its own
  // working directory: three different outcomes, three calls.
  await shareSelected(ids, { who: "all_paired" });
  if (setCalls.length !== 3 || setCalls.some((call) => call.ids.length !== 1)) {
    failures.push(`a batch of three different outcomes made ${JSON.stringify(setCalls)}, want one call each`);
  }
  if (audienceOf("codex:chosen").allowOutbound !== false || audienceOf("codex:chosen").acceptMessages !== true) {
    failures.push(`choosing only who rewrote codex:chosen's flags: ${JSON.stringify(audienceOf("codex:chosen"))}`);
  }
  // Taken now: the next batch's own toast takes the one toast slot (§3.1).
  const undoToast = latestToast(document);
  if (!undoToast.textContent.includes(fill(ZH["share.applied.other"], { n: 3 }))) {
    failures.push(`the three-session batch's toast reads ${undoToast.textContent}`);
  }
  const undo = undoButton(undoToast);
  // And one call when the outcome is the same.
  sessions.find((s) => s.id === "claude:only").audience = { mode: "all_paired", nodes: [], ...flags() };
  const quietCalls = setCalls.length;
  await shareSelected(["codex:silent", "claude:only"], { who: "all_paired", what: "messages" });
  if (setCalls.length !== quietCalls + 1 || setCalls.at(-1).ids.length !== 2) {
    failures.push(`two sessions with the same outcome made ${JSON.stringify(setCalls.slice(quietCalls))}, want one call for both`);
  }
  sessions.find((s) => s.id === "claude:only").audience = { mode: "none", nodes: [], ...flags() };
  sessions.find((s) => s.id === "codex:silent").audience = { mode: "all_paired", nodes: [], ...flags() };
  await reload();
  if (app.state.selected.size !== 0) failures.push("a batch that went through left the selection");
  setCalls.length = 0;
  await press(undo);
  for (const id of ids) {
    if (JSON.stringify(audienceOf(id)) !== JSON.stringify(before[id])) {
      failures.push(`復原 left ${id} at ${JSON.stringify(audienceOf(id))}, want ${JSON.stringify(before[id])}`);
    }
  }
  if (setCalls.length !== 3) failures.push(`復原 of three different audiences made ${setCalls.length} calls, want 3`);
}
clearToasts();

// The one shape SetAudience refuses — selected with no nodes — goes back as
// none with the same flags: nobody could see it before, nobody can after.
sessions = fresh();
await reload();
{
  await share("codex:emptychosen", { who: "all_paired", what: "wake" });
  const undo = undoButton(latestToast(document));
  setCalls.length = 0;
  await press(undo);
  const want = { mode: "none", nodes: [], exportCwd: false, acceptMessages: true, allowOutbound: false, autoWake: false };
  if (JSON.stringify(lastSet()?.audience) !== JSON.stringify(want)) {
    failures.push(`復原 of a 「指定：無」 session wrote ${JSON.stringify(lastSet()?.audience)}, want ${JSON.stringify(want)}`);
  }
}
clearToasts();

// A batch that partly fails keeps the selection and the panel, and says the
// first error.
sessions = fresh();
await reload();
{
  // The two get different audiences, so two calls: the node refuses the one
  // for codex:quiet and applies the other.
  setAudienceFails = (ids, audience) => {
    if (ids.includes("codex:quiet")) return { changed: 0, failed: 1, errors: ["codex:quiet: refused by the node"] };
    for (const s of sessions) if (ids.includes(s.id)) s.audience = JSON.parse(JSON.stringify(audience));
    return { changed: ids.length, failed: 0, errors: [] };
  };
  app.state.selected.add("codex:quiet");
  app.state.selected.add("codex:silent");
  app.render();
  el("btn-audience").onclick();
  choose("share-who", "all_paired");
  choose("share-what", "messages");
  await press(el("audience-apply"));
  if (app.state.selected.size !== 2) failures.push(`a batch that partly failed left ${app.state.selected.size} selected, want the 2 it had`);
  if (!panelOpen()) failures.push("a batch that partly failed closed the panel");
  const toast = latestToast(document);
  if (toast.kind !== "error" || !toast.textContent.includes("refused by the node")) {
    failures.push(`a partial failure said ${toast.kind}: ${toast.textContent}`);
  }
  // What did go through is undoable: 復原 writes every session's own
  // previous audience, the failed one included (for it, the same value again).
  const undo = undoButton(toast);
  setAudienceFails = null;
  if (!undo) {
    failures.push(`a partial failure offers no 復原 for the part that went through: ${toast.textContent}`);
  } else {
    if (audienceOf("codex:silent").acceptMessages !== true) failures.push("the half of the batch that went through was not written");
    const want = fresh().filter((s) => ["codex:quiet", "codex:silent"].includes(s.id)).map((s) => [s.id, s.audience]);
    setCalls.length = 0;
    await press(undo);
    for (const [id, audience] of want) {
      const call = setCalls.find((c) => c.ids.includes(id));
      if (JSON.stringify(call?.audience) !== JSON.stringify(audience)) {
        failures.push(`復原 after a partial failure wrote ${JSON.stringify(call?.audience)} for ${id}, want ${JSON.stringify(audience)}`);
      }
      if (JSON.stringify(audienceOf(id)) !== JSON.stringify(audience)) failures.push(`after 復原 ${id} holds ${JSON.stringify(audienceOf(id))}`);
    }
  }
  app.closeSharePanel();
  app.state.selected.clear();
  app.render();
}
clearToasts();
// A SetAudience call that throws outright counts every session it carried as
// failed (§2), and the calls for the other audiences are still made: the batch
// does not stop half written, and the count is not one per call.
sessions = fresh();
await reload();
{
  // codex:silent and claude:only get the same audience (every paired machine,
  // no directory), so they share one call; codex:quiet has its own.
  setAudienceFails = (ids, audience) => {
    if (ids.length === 2) throw new Error("SetAudience: node went away");
    for (const s of sessions) if (ids.includes(s.id)) s.audience = JSON.parse(JSON.stringify(audience));
    return { changed: ids.length, failed: 0, errors: [] };
  };
  setCalls.length = 0;
  await shareSelected(["codex:silent", "claude:only", "codex:quiet"], { who: "all_paired", what: "messages" });
  setAudienceFails = null;
  const toast = latestToast(document);
  const want = fill(ZH["audience.partlyApplied"], {
    action: ZH["share.verb"],
    changed: 1,
    failed: 2,
    error: "Error: SetAudience: node went away",
  });
  if (toast.textContent !== want) failures.push(`a call that threw was reported as ${toast.kind}: ${toast.textContent}, want ${want}`);
  if (setCalls.length !== 2) failures.push(`a call that threw stopped the batch after ${setCalls.length} of 2 calls`);
  if (audienceOf("codex:quiet").acceptMessages !== true) failures.push("the audience after the one that threw was not written");
  if (app.state.selected.size !== 3) failures.push(`a batch with a call that threw left ${app.state.selected.size} selected, want 3`);
  app.closeSharePanel();
  app.state.selected.clear();
  app.render();
}
clearToasts();

// A batch where nothing went through has nothing to undo.
sessions = fresh();
await reload();
{
  setAudienceFails = (ids) => ({ changed: 0, failed: ids.length, errors: ["refused by the node"] });
  await share("codex:quiet", { who: "all_paired" });
  setAudienceFails = null;
  if (undoButton(latestToast(document))) failures.push("a batch that changed nothing offered 復原");
  app.closeSharePanel();
}
clearToasts();

/* ---------------- 3. what the panel says, and the toast after it --------- */

sessions = fresh();
await reload();
// One row: its own situation ticked, the others not.
openRow("codex:allwake");
if (checkedOf("share-who") !== "all_paired" || checkedOf("share-what") !== "wake") {
  failures.push(`a waking session's panel ticks ${checkedOf("share-who")} / ${checkedOf("share-what")}`);
}
openRow("codex:chosen");
if (checkedOf("share-who") !== "selected" || !nodeBox("node_a")?.checked || !nodeBox("node_b")?.checked) {
  failures.push("a session with two chosen machines does not tick them both");
}
openRow("codex:quiet");
if (checkedOf("share-who") !== "none" || !el("share-what-block").classList.contains("hidden")) {
  failures.push(`an unshared session's panel ticks ${checkedOf("share-who")} and shows its what block`);
}
// A shared session with every flag off is 「只看得到」: an option of its own now.
openRow("codex:silent");
if (checkedOf("share-what") !== "view") failures.push(`a shared session with every flag off ticks ${checkedOf("share-what")}, want view`);
app.closeSharePanel();

// The table says 「未分享」 for a session with 「指定機器」 and none ticked.
if (audienceButton("codex:emptychosen").textContent !== ZH["audience.cell.none"]) {
  failures.push(`a chosen-none session's cell reads ${audienceButton("codex:emptychosen").textContent}`);
}

// Claude Code alone cannot be woken, so the option is there and cannot be picked.
openRow("claude:only");
if (!el("share-what-wake").disabled) failures.push("a Claude-only panel lets waking be picked");
if (!panelText().includes(ZH["popover.wakeClaudeOnly"])) failures.push(`a Claude-only panel does not say why: ${panelText()}`);
openRow("codex:quiet");
if (el("share-what-wake").disabled) failures.push("a Codex session's panel disabled waking");
app.closeSharePanel();

// The toast after a write says what it did from the other machine's side.
sessions = fresh();
await reload();
clearToasts();
await share("codex:quiet", { who: "selected", nodes: ["node_a"] });
{
  const toast = latestToast(document);
  const seen = sessions.filter((s) => s.audience.mode === "all_paired" || (s.audience.mode === "selected" && s.audience.nodes.includes("node_a"))).length;
  if (toast.kind !== "ok") failures.push(`the share toast is ${toast.kind}`);
  if (!toast.textContent.includes(fill(ZH["share.peerSees.other"], { name: "alice", n: seen }))) {
    failures.push(`the toast does not say what alice sees (${seen}): ${toast.textContent}`);
  }
  if (!toast.textContent.includes(fill(ZH["share.applied.one"], { n: 1 }))) failures.push(`the toast has no title: ${toast.textContent}`);
  const see = toastButtons(toast.node).find((button) => button.textContent === fill(ZH["share.toastSee"], { name: "alice" }));
  if (!see) {
    failures.push(`the toast has no 「在區網頁看 alice」: ${toastButtons(toast.node).map((b) => b.textContent)}`);
  } else {
    app.state.selectedNode = null;
    await press(see);
    if (app.state.view !== "network" || app.state.selectedNode !== "node_a") {
      failures.push(`「在區網頁看」 left the view at ${app.state.view} with ${app.state.selectedNode} selected`);
    }
    app.state.view = "local";
    app.render();
  }
  // The undo is still beside it.
  if (!undoButton(toast)) failures.push("the toast with a peer action lost its 復原");
}
clearToasts();
// Every paired machine, two of them: how many machines, and a button for the page.
await share("codex:silent", { who: "all_paired", what: "messages" });
{
  const toast = latestToast(document);
  if (!toast.textContent.includes(fill(ZH["share.peersSee.other"], { n: 2 }))) failures.push(`an all-paired write does not say how many machines: ${toast.textContent}`);
  if (!toastButtons(toast.node).some((button) => button.textContent === ZH["share.toastSeeAll"])) failures.push("an all-paired write has no 「在區網頁看」");
}
clearToasts();
// Stopping: nobody sees it any more, and there is nowhere to go and look.
await share("codex:allwake", { who: "none" });
{
  const toast = latestToast(document);
  if (!toast.textContent.includes(ZH["popover.appliedNoneBody"])) failures.push(`stopping does not say nobody sees it: ${toast.textContent}`);
  if (toastButtons(toast.node).some((button) => button.textContent.startsWith(ZH["share.toastSeeAll"]))) failures.push("stopping offered a page to go and look at");
}
clearToasts();

// Nothing paired yet: still usable, and the panel says who will see it, with
// the way to pair. The write's own toast says so too, and has nowhere to go.
nodes = [];
await reload();
openRow("codex:quiet");
if (!panelText().includes(ZH["popover.noNodes"])) failures.push(`with nothing paired the panel does not say so: ${panelText()}`);
{
  let pair = null;
  walk(el("share-who-note"), (node) => { if (node.tagName === "button" && node.textContent === ZH["popover.pairAction"]) pair = node; });
  if (!pair) failures.push("with nothing paired the panel offers no way to pair");
  else {
    pair.onclick();
    await settle();
    if (app.state.view !== "network") failures.push(`配對另一台機器 left the view at ${app.state.view}`);
    if (panelOpen()) failures.push("配對另一台機器 left the panel open");
    app.closePairingDrawer();
    app.state.view = "local";
    app.render();
  }
}
if (el("audience-apply").disabled) failures.push("with nothing paired the panel refused to share");
await share("codex:quiet", { who: "all_paired" });
if (!latestToast(document).textContent.includes(ZH["popover.noNodes"])) failures.push(`the toast after sharing with nothing paired reads ${latestToast(document).textContent}`);
clearToasts();
// A failed read of the pairing list is not "nothing paired".
app.state.nodesError = "trusted nodes: 500";
openRow("codex:silent");
if (panelText().includes(ZH["popover.noNodes"])) failures.push("a failed pairing-list read was described as nothing paired");
app.closeSharePanel();
app.state.nodesError = "";
nodes = [{ nodeId: "node_a", displayName: "alice" }, { nodeId: "node_b", displayName: "bob" }];
sessions = fresh();
await reload();

/* ---------------- 4. the panel holds still, and answers the keyboard ------ */

openRow("codex:quiet");
if (!app.interactionInProgress()) failures.push("an open panel is not an interaction in progress");
if (document.activeElement !== radios("share-who").find((radio) => radio.checked)) failures.push("the panel opened without the keyboard on the ticked 「誰看得到」 option");
{
  const tick15 = ticks.find((tick) => tick.ms === 15000);
  const before = overviewCalls;
  const anchor = audienceButton("codex:quiet");
  tick15.fn();
  await settle();
  if (overviewCalls !== before) failures.push("the fifteen-second tick read the list with the panel open");
  if (!panelOpen()) failures.push("the fifteen-second tick closed the panel");
  if (audienceButton("codex:quiet") !== anchor) failures.push("the row the panel was opened from was replaced");
}
// Esc, 取消 and the backdrop close it and give the keyboard back to the button.
{
  const anchor = audienceButton("codex:quiet");
  document.activeElement?.blur();
  const event = { key: "Escape", defaultPrevented: false, preventDefault() { this.prevented = true; } };
  if (app.overlayKey(event) !== true) failures.push("Esc was not taken by the panel");
  if (panelOpen()) failures.push("Esc did not close the panel");
  if (document.activeElement !== anchor) failures.push("Esc did not give the keyboard back to the row's button");

  openRow("codex:quiet");
  document.activeElement?.blur();
  el("audience-close").onclick();
  if (panelOpen()) failures.push("取消 did not close the panel");
  if (document.activeElement !== anchor) failures.push("取消 did not give the keyboard back to the row's button");

  openRow("codex:quiet");
  document.activeElement?.blur();
  el("audience-modal").onclick({ target: el("audience-modal") });
  if (panelOpen()) failures.push("a press on the backdrop did not close the panel");
  if (document.activeElement !== anchor) failures.push("the backdrop did not give the keyboard back to the row's button");
  // A press inside the card is not a press on the backdrop.
  openRow("codex:quiet");
  el("audience-modal").onclick({ target: el("audience-apply") });
  if (!panelOpen()) failures.push("a press inside the panel closed it");
  app.closeSharePanel();
}
// Cancelling writes nothing.
setCalls.length = 0;
openRow("codex:quiet");
choose("share-who", "all_paired");
el("audience-close").onclick();
if (setCalls.length !== 0) failures.push("取消 wrote something");
// Applying gives the keyboard back to the button: withBusy disables it for the
// length of the write, which drops the keyboard in a browser.
blurDuringWrite = true;
await share("codex:quiet", { who: "all_paired" });
blurDuringWrite = false;
if (document.activeElement !== audienceButton("codex:quiet")) failures.push("after applying, the keyboard is not on the row's button");
if (panelOpen()) failures.push("the panel stayed open after a write that went through");
clearToasts();
// From the selection bar: the selection is cleared by a batch that went through.
sessions = fresh();
await reload();
await shareSelected(["codex:silent", "claude:only"], { who: "all_paired", what: "messages" });
if (app.state.selected.size !== 0) failures.push("a batch from the selection bar left the selection");
clearToasts();

/* ---------------- 5. the icons ---------------- */

{
  sessions = fresh();
  sessions.find((s) => s.id === "claude:only").audience = { mode: "all_paired", nodes: [], ...flags({ msg: true, out: true, wake: true }) };
  await reload();
  const waking = rowFor("codex:allwake").sessionParts;
  if (waking.iconWake.classList.contains("hidden")) failures.push("a waking row shows no bell");
  if (waking.iconWake.title !== ZH["share.icon.wake"]) failures.push(`the bell's title is ${waking.iconWake.title}`);
  if (waking.iconWake.classList.contains("off")) failures.push("a Codex row's bell is drawn as off");
  if (waking.iconMsg.title !== ZH["share.icon.messages"]) failures.push(`the messages icon over a two-way session says ${waking.iconMsg.title}`);
  const claude = rowFor("claude:only").sessionParts;
  if (claude.iconWake.classList.contains("hidden")) failures.push("a Claude row set to wake shows no bell");
  if (!claude.iconWake.classList.contains("off")) failures.push("a Claude row's bell is not drawn as off");
  if (claude.iconWake.title !== ZH["share.icon.wakeClaude"]) failures.push(`a Claude row's bell says ${claude.iconWake.title}`);
  if (!rowFor("codex:chosen").sessionParts.iconWake.classList.contains("hidden")) failures.push("a row that does not wake shows the bell");
  if (rowFor("codex:chosen").sessionParts.iconMsg.title !== ZH["share.icon.messagesNoReply"]) failures.push("an accept-only row's messages icon does not say it cannot reply");
  if (!rowFor("codex:quiet").sessionParts.shareIcons.classList.contains("hidden")) failures.push("an unshared row shows icons");
}
// The caveat is not in the panel or the toast: it lives where the switch is.
await share("codex:quiet", { who: "all_paired", what: "wake" });
if (latestToast(document).textContent.includes(ZH["wake.caveat"])) {
  failures.push(`the toast after turning waking on carries the caveat: ${latestToast(document).textContent}`);
}
clearToasts();
{
  const markup = fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "index.html"), "utf8");
  const settings = markup.slice(markup.indexOf('id="node-autowake"'), markup.indexOf("</label>", markup.indexOf('id="node-autowake"')));
  if (!settings.includes('data-t="wake.caveat"')) failures.push("the node settings' auto-wake switch does not carry the caveat");
  if (EN["wake.caveat"] === ZH["wake.caveat"] || !/not guaranteed/i.test(EN["wake.caveat"])) {
    failures.push(`the English caveat does not say waking is not guaranteed: ${EN["wake.caveat"]}`);
  }
}

/* ---------------- 6. the background service in one press ---------------- */

const serviceRow = () => attentionRows(document).find((row) => row.row.className.includes("alert")
  && (row.title === ZH["attention.service.stoppedTitle"] || row.title === ZH["attention.service.noneTitle"]));

// Supported, nothing installed, nothing running: install, through the
// settings page's own path, with the database path blank (the node's default).
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
reachable = false;
await reload();
if (app.serviceQuickAction() !== "install") failures.push(`nothing installed answers ${app.serviceQuickAction()}, want install`);
serviceCalls.length = 0;
await press(serviceRow().action);
await settle();
if (JSON.stringify(serviceCalls.map((call) => call[0])) !== JSON.stringify(["InstallService"])) {
  failures.push(`the install row called ${JSON.stringify(serviceCalls)}, want InstallService alone`);
} else if (serviceCalls[0][1].dbPath !== "") {
  failures.push(`the one-press install sent the database path ${JSON.stringify(serviceCalls[0][1].dbPath)}, want blank`);
}
reachable = true;
await reload();
if (serviceRow()) failures.push("the service row outlived the install");
if (latestToast(document).textContent !== ZH["service.installed"]) failures.push(`the install answered ${latestToast(document).textContent}`);
clearToasts();

// The status on screen is stale — the tick stays away while rows are selected
// or a dialog is open — and `ah service install` replaces a registration. So
// the press reads it again and acts on what it reads: here the service was
// installed and started from a terminal since, and the press installs nothing.
app.state.service = { supported: true, installed: false, running: false, pid: 0 };
serviceAnswer = { supported: true, installed: true, running: true, pid: 21 };
reachable = false;
app.state.nodeReachable = false;
app.state.view = "local";
serviceCalls.length = 0;
{
  const reads = serviceReads;
  await press(el("service-pill"));
  if (serviceReads === reads) failures.push("the one-press fix acted without reading the service status again");
}
if (serviceCalls.length !== 0) failures.push(`a stale "nothing installed" was acted on: ${JSON.stringify(serviceCalls)}`);
if (app.state.view !== "settings") failures.push(`a service found running on the re-read left the view at ${app.state.view}`);
// Found installed and stopped instead: started, not installed over.
app.state.service = { supported: true, installed: false, running: false, pid: 0 };
serviceAnswer = { supported: true, installed: true, running: false, pid: 0 };
app.state.view = "local";
serviceCalls.length = 0;
await press(el("service-pill"));
await settle();
if (JSON.stringify(serviceCalls.map((call) => call[0])) !== JSON.stringify(["RestartService"])) {
  failures.push(`a stale "nothing installed" over a stopped service called ${JSON.stringify(serviceCalls)}, want RestartService`);
}
reachable = true;
await reload();
clearToasts();

// A node running but not as a service was started by hand, on a database this
// window cannot see: installing the default one over it would be a new
// identity. The press goes to the form, which asks.
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
await reload();
el("service-form").classList.add("hidden");
app.state.view = "local";
// With the first-run wizard up the strip is hidden whole: the wizard's first
// step is this same fix (first-run.mjs covers the wizard's own path to it).
app.openFirstRun();
if (serviceRow()) failures.push("the first-run wizard is up and the strip repeats its fix beside it");
app.state.firstRun.engaged = false;
app.state.firstRun.forced = false;
app.state.ui.onboardingDismissed = true;
app.render();
{
  const row = serviceRow();
  if (!row) failures.push("a node that is not a service put no row on the strip");
  else {
    if (row.action.textContent !== ZH["attention.service.formAction"]) {
      failures.push(`a running node that is not a service is offered ${row.action.textContent}, want ${ZH["attention.service.formAction"]}`);
    }
    serviceCalls.length = 0;
    await press(row.action);
    await settle();
    if (serviceCalls.length !== 0) failures.push(`a running node that is not a service was installed over: ${JSON.stringify(serviceCalls)}`);
    if (app.state.view !== "settings" || app.state.settingsSection !== "settings-service") {
      failures.push(`a running node that is not a service went to ${app.state.view} / ${app.state.settingsSection}`);
    }
    if (el("service-form").classList.contains("hidden")) failures.push("a running node that is not a service did not open the form");
    // The form says what the field is for here: the --db the node was started
    // with, and what a blank costs. Not 「第一次安裝：留空就用預設位置」, which
    // walked the owner into a new identity.
    if (el("service-db-note").textContent !== ZH["service.dbNoteRunningNotService"]) {
      failures.push(`the form for a running node that is not a service says: ${el("service-db-note").textContent}`);
    }
  }
}
// Installing from that form with the field blank is asked first — red, with
// the keyboard on 取消 — and a 取消 installs nothing.
questions.length = 0;
confirmAnswer = () => false;
serviceCalls.length = 0;
el("service-db").value = "";
await app.installService();
await settle();
if (questions.length !== 1 || !questions[0].question.includes(ZH["service.runningNotServiceConfirmTitle"])) {
  failures.push(`a blank install over a running node asked ${JSON.stringify(questions)}`);
} else if (!questions[0].danger || !questions[0].focusedCancel) {
  failures.push(`a blank install over a running node was not asked as a danger with 取消 focused: ${JSON.stringify(questions[0])}`);
}
if (serviceCalls.length !== 0) failures.push(`取消 on a blank install over a running node still ran ${JSON.stringify(serviceCalls)}`);
// A path typed in is the owner's answer already: no question, and that path.
questions.length = 0;
confirmAnswer = () => true;
el("service-db").value = "/data/by-hand.db";
await app.installService();
await settle();
if (questions.length !== 0) failures.push(`an install over a running node with its path typed asked ${JSON.stringify(questions)}`);
if (serviceCalls.length !== 1 || serviceCalls[0][0] !== "InstallService" || serviceCalls[0][1].dbPath !== "/data/by-hand.db") {
  failures.push(`an install over a running node with its path typed ran ${JSON.stringify(serviceCalls)}`);
}
// And a yes to the question installs, blank.
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
await reload();
await app.openServiceForm();
questions.length = 0;
serviceCalls.length = 0;
el("service-db").value = "";
await app.installService();
await settle();
if (questions.length !== 1) failures.push(`a confirmed blank install over a running node asked ${questions.length} questions`);
if (serviceCalls.length !== 1 || serviceCalls[0][0] !== "InstallService" || serviceCalls[0][1].dbPath !== "") {
  failures.push(`a confirmed blank install over a running node ran ${JSON.stringify(serviceCalls)}`);
}
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
await reload();
clearToasts();
// The status command's own probe says the same when the last Overview did not.
reachable = false;
await reload();
serviceAnswer = { supported: true, installed: false, running: false, pid: 0, nodeAnswering: true };
el("service-form").classList.add("hidden");
app.state.view = "local";
serviceCalls.length = 0;
await press(el("service-pill"));
await settle();
if (serviceCalls.length !== 0) failures.push(`a node the status says is answering was installed over: ${JSON.stringify(serviceCalls)}`);
if (app.state.view !== "settings") failures.push(`a node the status says is answering went to ${app.state.view}`);
// A re-read that fails decides nothing: the settings page, and a toast that
// says why.
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
await reload();
serviceReadFails = new Error("ah service status: exit status 2");
app.state.view = "local";
serviceCalls.length = 0;
clearToasts();
await press(el("service-pill"));
await settle();
serviceReadFails = null;
if (serviceCalls.length !== 0) failures.push(`a failed status read was acted on: ${JSON.stringify(serviceCalls)}`);
if (app.state.view !== "settings") failures.push(`a failed status read left the view at ${app.state.view}`);
{
  const toast = latestToast(document);
  if (toast.kind !== "error" || !toast.textContent.includes("exit status 2")
    || !toast.textContent.startsWith(ZH["service.quickReadFailed"].split("{error}")[0])) {
    failures.push(`a failed status read said ${toast.kind}: ${toast.textContent}`);
  }
}
clearToasts();
reachable = true;
app.state.ui.onboardingDismissed = false;
el("service-form").classList.add("hidden");

// Installed, not running: RestartService, not RestartNode.
serviceAnswer = { supported: true, installed: true, running: false, pid: 0, logHint: "~/node.log" };
await reload();
if (app.serviceQuickAction() !== "restart") failures.push(`installed and stopped answers ${app.serviceQuickAction()}, want restart`);
serviceCalls.length = 0;
// From the title bar's pill this time: the same press.
await press(el("service-pill"));
await settle();
if (JSON.stringify(serviceCalls.map((call) => call[0])) !== JSON.stringify(["RestartService"])) {
  failures.push(`the stopped service's pill called ${JSON.stringify(serviceCalls)}, want RestartService alone`);
}
await reload();
if (serviceRow()) failures.push("the service row outlived the restart");
clearToasts();

// No ah, no service manager, a status not read yet, or nothing to fix: the
// settings page, and nothing run.
for (const [status, label] of [
  [{ toolError: "ah not found", supported: true }, "no ah"],
  [{ supported: false }, "unsupported"],
  [null, "unknown"],
  [{ supported: true, installed: true, running: true, pid: 3 }, "running"],
]) {
  app.state.service = status;
  serviceAnswer = status;
  app.state.view = "local";
  serviceCalls.length = 0;
  await press(el("service-pill"));
  if (serviceCalls.length !== 0) failures.push(`${label}: the pill ran ${JSON.stringify(serviceCalls)}`);
  if (app.state.view !== "settings" || app.state.settingsSection !== "settings-service") {
    failures.push(`${label}: the pill went to ${app.state.view} / ${app.state.settingsSection}`);
  }
}
app.state.view = "local";

// A failed install says so with a way to the settings page.
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
installFails = true;
reachable = false;
await reload();
clearToasts();
await press(serviceRow().action);
await settle();
{
  const toast = latestToast(document);
  const open = toastButtons(toast.node).find((button) => button.textContent === ZH["service.openSettings"]);
  if (toast.kind !== "error" || !toast.textContent.includes("launchctl refused")) failures.push(`a failed install said ${toast.kind}: ${toast.textContent}`);
  if (!open) failures.push(`a failed install's toast offers no 開啟設定: ${toast.textContent}`);
  else {
    await press(open);
    if (app.state.view !== "settings" || app.state.settingsSection !== "settings-service") {
      failures.push(`開啟設定 went to ${app.state.view} / ${app.state.settingsSection}`);
    }
    // Where ah's own words went, still showing: the settings page's form is
    // not opened again over them.
    if (el("service-output").classList.contains("hidden") || !el("service-output").textContent.includes("launchctl refused")) {
      failures.push(`開啟設定 hid why the install failed: ${el("service-output").textContent}`);
    }
  }
}
// A database path typed into the open form is not reset by a press elsewhere:
// blank is the node's default database, a new identity.
installFails = false;
app.state.view = "local";
await app.openServiceForm();
el("service-db").value = "/data/custom.db";
serviceCalls.length = 0;
await press(el("service-pill"));
if (serviceCalls.length !== 0) failures.push(`with a path typed into the form the pill ran ${JSON.stringify(serviceCalls)}`);
if (el("service-db").value !== "/data/custom.db") failures.push(`the pill reset a typed database path to ${JSON.stringify(el("service-db").value)}`);
if (app.state.view !== "settings") failures.push(`with a path typed the pill went to ${app.state.view}, want the form`);
el("service-db").value = "";
el("service-form").classList.add("hidden");
// And nothing happens while something else is being written — not even the
// form being reset under that write, which is what opening it does.
app.state.service = { supported: true, installed: false, running: false, pid: 0 };
el("service-output").textContent = "the write in flight";
el("service-output").classList.remove("hidden");
app.state.view = "local";
app.state.busy = true;
serviceCalls.length = 0;
await press(el("service-pill"));
if (serviceCalls.length !== 0) failures.push(`the pill ran ${JSON.stringify(serviceCalls)} while another write was out`);
if (el("service-output").classList.contains("hidden") || app.state.view !== "local") {
  failures.push("a press while another write was out still opened the service form");
}
app.state.busy = false;
// The re-read is the press's first wait, and can be the status command's
// whole timeout: the button spins and cannot be pressed from the moment it is
// pressed, through a strip render landing mid-read, and is let go once the
// read has answered.
el("service-output").classList.add("hidden");
for (const [name, button] of [["the pill", () => el("service-pill")], ["the strip's row", () => serviceRow()?.action]]) {
  serviceAnswer = { supported: true, installed: true, running: true, pid: 7 };
  app.state.service = { supported: true, installed: true, running: false, pid: 0 };
  app.state.ui.onboardingDismissed = true;
  app.state.view = "local";
  app.render();
  const pressed = button();
  if (!pressed) {
    failures.push(`${name}: no button to press`);
    continue;
  }
  serviceReadGate = gate();
  const done = app.runServiceQuickAction({ button: pressed });
  await settle();
  if (!pressed.classList.contains("busy") || !pressed.disabled) {
    failures.push(`${name} shows nothing while the status is read again (busy ${pressed.classList.contains("busy")}, disabled ${pressed.disabled})`);
  }
  app.renderAttention();
  if (!pressed.disabled) failures.push(`${name}: a render during the re-read made the button pressable again`);
  serviceReadGate.open();
  serviceReadGate = null;
  await done;
  await settle();
  if (pressed.classList.contains("busy") || pressed.disabled) failures.push(`${name} still spins after the re-read found nothing to do`);
}
app.state.ui.onboardingDismissed = false;
// The pill and the strip's row pressed together are one press: the second
// finds the first still reading and does nothing — no second read, no second
// start. Each button's spinner stops only itself, which is how both used to run.
{
  serviceAnswer = { supported: true, installed: true, running: false, pid: 0 };
  app.state.service = { supported: true, installed: true, running: false, pid: 0 };
  app.state.ui.onboardingDismissed = true;
  app.state.view = "local";
  app.render();
  const pill = el("service-pill");
  const rowButton = serviceRow()?.action;
  if (!rowButton) failures.push("no strip row to press alongside the pill");
  serviceCalls.length = 0;
  const reads = serviceReads;
  serviceReadGate = gate();
  const first = app.runServiceQuickAction({ button: pill });
  await settle();
  const second = app.runServiceQuickAction({ button: rowButton });
  const third = app.runServiceQuickAction({ button: pill });
  await settle();
  serviceReadGate.open();
  serviceReadGate = null;
  await Promise.all([first, second, third]);
  await settle();
  if (serviceReads - reads !== 1 + 1) {
    // One re-read by the press, one by the load() after its restart.
    failures.push(`three presses at once read the status ${serviceReads - reads} times, want the one press's re-read and its reload`);
  }
  if (JSON.stringify(serviceCalls.map((call) => call[0])) !== JSON.stringify(["RestartService"])) {
    failures.push(`three presses at once ran ${JSON.stringify(serviceCalls)}, want one RestartService`);
  }
  // Let go once it is done: a later press is a press.
  serviceAnswer = { supported: true, installed: true, running: false, pid: 0 };
  app.state.service = { supported: true, installed: true, running: false, pid: 0 };
  serviceCalls.length = 0;
  await app.runServiceQuickAction({ button: pill });
  await settle();
  if (serviceCalls.length !== 1) failures.push(`a press after the first had finished ran ${JSON.stringify(serviceCalls)}`);
  app.state.ui.onboardingDismissed = false;
  clearToasts();
}
// And a write that starts while the status is being read wins: the press does
// nothing with what it read — not even open the form under that write.
serviceAnswer = { supported: true, installed: false, running: false, pid: 0 };
app.state.service = { supported: true, installed: false, running: false, pid: 0 };
reachable = false;
app.state.nodeReachable = false;
app.state.view = "local";
el("service-form").classList.add("hidden");
el("service-output").textContent = "the write in flight";
el("service-output").classList.remove("hidden");
serviceCalls.length = 0;
serviceReadGate = gate();
{
  const done = app.runServiceQuickAction({ button: el("service-pill") });
  await settle();
  app.state.busy = true;
  serviceReadGate.open();
  serviceReadGate = null;
  await done;
  await settle();
  app.state.busy = false;
}
if (serviceCalls.length !== 0) failures.push(`a write started during the re-read, and the press still ran ${JSON.stringify(serviceCalls)}`);
if (el("service-output").classList.contains("hidden") || !el("service-form").classList.contains("hidden") || app.state.view !== "local") {
  failures.push("a write started during the re-read, and the press still opened the service form");
}
el("service-output").classList.add("hidden");
// The pill keeps its spinner through a status read that lands mid-press.
el("service-pill").classList.add("busy");
await app.loadService();
if (!el("service-pill").classList.contains("busy")) failures.push("a status read took the pill's spinner away");
el("service-pill").classList.remove("busy");
installFails = false;
serviceAnswer = { supported: true, installed: true, running: true, pid: 7 };
reachable = true;
app.state.view = "local";
await reload();
clearToasts();

/* ---------------- 7. the pairing drawer's steps ---------------- */

const row = (direction, state) => ({ id: `pair_${direction}_${state}`, direction, state, nodeId: "node_x", displayName: "x" });
for (const [rows, want] of [
  [[], "find"],
  [[row("outgoing", "rejected"), row("incoming", "expired")], "find"],
  [[row("outgoing", "pending")], "waiting"],
  [[row("outgoing", "awaiting-confirm")], "compare"],
  [[row("incoming", "pending")], "compare"],
  [[row("outgoing", "pending"), row("incoming", "pending")], "compare"],
]) {
  const got = app.pairStepperPhase(rows);
  if (got !== want) failures.push(`the steps read ${got} for ${JSON.stringify(rows.map((r) => `${r.direction}/${r.state}`))}, want ${want}`);
}
{
  const classes = () => (el("pair-stepper").children ?? []).map((item) => item.className);
  app.state.pairRequests = [];
  app.renderPairStepper();
  const items = [...el("pair-stepper").children];
  if (items.length !== 3) failures.push(`the step strip has ${items.length} steps`);
  if (JSON.stringify(classes()) !== JSON.stringify(["pairstepitem now", "pairstepitem", "pairstepitem"])) {
    failures.push(`with nothing sent the steps are ${JSON.stringify(classes())}`);
  }
  app.state.pairRequests = [row("outgoing", "pending")];
  app.renderPairStepper();
  if (JSON.stringify(classes()) !== JSON.stringify(["pairstepitem done", "pairstepitem done", "pairstepitem waiting"])) {
    failures.push(`with a request out the steps are ${JSON.stringify(classes())}`);
  }
  if (!el("pair-stepper").textContent.includes(ZH["pair.stepper.waiting"])) failures.push("a request out does not say it is waiting");
  app.state.pairRequests = [row("incoming", "pending")];
  app.renderPairStepper();
  if (JSON.stringify(classes()) !== JSON.stringify(["pairstepitem done", "pairstepitem done", "pairstepitem now"])) {
    failures.push(`with a request to compare the steps are ${JSON.stringify(classes())}`);
  }
  if ([...el("pair-stepper").children].some((item, index) => item !== items[index])) failures.push("a render replaced the step elements");
  let pressable = false;
  walk(el("pair-stepper"), (node) => { if (node.tagName === "button" || node.onclick) pressable = true; });
  if (pressable) failures.push("the step strip has something to press");
}

// Approving is half of pairing; the toast carries the way to the other half.
{
  pairRequestsAnswer = [row("incoming", "pending")];
  app.state.view = "network";
  app.render();
  await app.decidePairRequest("pair_incoming_pending", "approve");
  await settle();
  const go = toastButtons(latestToast(document).node).find((button) => button.textContent === ZH["pair.goPublish"]);
  if (!go) failures.push(`an approval's toast offers no 去公開 session: ${latestToast(document).textContent}`);
  else {
    await press(go);
    if (app.state.view !== "local") failures.push(`去公開 session left the view at ${app.state.view}`);
  }
  clearToasts();
  await app.decidePairRequest("pair_incoming_pending", "reject");
  await settle();
  if (toastButtons(latestToast(document).node).some((button) => button.textContent === ZH["pair.goPublish"])) {
    failures.push("a rejection offers 去公開 session");
  }
  clearToasts();
  pairRequestsAnswer = [];
}

/* ---------------- 8. the view tabs are buttons ---------------- */

{
  const markup = fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "index.html"), "utf8");
  const nav = /<nav[^>]*id="view-switch"[^>]*>([\s\S]*?)<\/nav>/.exec(markup);
  if (!nav || !/role="tablist"/.test(nav[0].slice(0, nav[0].indexOf(">")))) failures.push("#view-switch is not a tablist");
  const tabs = [...(nav?.[1] ?? "").matchAll(/<([a-z]+)[^>]*\sdata-view="([^"]+)"[^>]*>/g)];
  if (tabs.length !== 3) failures.push(`the view switch has ${tabs.length} tabs`);
  for (const [tag, name, view] of tabs) {
    if (name !== "button" || !/role="tab"/.test(tag)) failures.push(`the ${view} tab is a <${name}> without role=tab`);
  }
  const source = fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "src", "app.js"), "utf8");
  if (source.includes("span[data-view]")) failures.push("app.js still looks for the tabs as spans");

  const shimTabs = document.querySelectorAll("#view-switch [data-view]");
  const byView = (view) => shimTabs.find((tab) => tab.dataset.view === view);
  byView("settings").onclick();
  if (app.state.view !== "settings") failures.push("pressing the settings tab did not open settings");
  if (byView("settings").getAttribute("aria-selected") !== "true" || byView("local").getAttribute("aria-selected") !== "false") {
    failures.push(`aria-selected is ${shimTabs.map((tab) => tab.getAttribute("aria-selected")).join(",")} on settings`);
  }
  if (byView("settings").getAttribute("tabindex") !== "0" || byView("network").getAttribute("tabindex") !== "-1") {
    failures.push("the selected tab is not the strip's one stop for Tab");
  }
  app.viewSwitchKey({ key: "ArrowLeft", preventDefault() {} });
  if (app.state.view !== "network") failures.push(`ArrowLeft from settings went to ${app.state.view}`);
  app.viewSwitchKey({ key: "Home", preventDefault() {} });
  if (app.state.view !== "local") failures.push(`Home went to ${app.state.view}`);
  if (document.activeElement !== byView("local")) failures.push("the arrow keys did not move the keyboard with the tab");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("inline publish: the share panel writes the right machines and undoes to each session's own, "
  + "the service is fixed in one press, the pairing steps follow the rows, and the tabs are buttons");
