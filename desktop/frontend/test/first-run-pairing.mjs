// The first-run wizard's step 2: connecting to the other machine
// (docs/ui-contract.md §3.2, and §2/§4 for the rules it borrows).
//
// The step is the pairing drawer in different clothes, so what is checked is
// that the drawer's rules came with it: the window opened with OpenPairing(0)
// by being on screen and closed by the drawer's rule when the step is left;
// the two polls running for it and only for it, still four intervals in all;
// a candidate able to choose who is dialled and nothing else; the node's
// fingerprint array shown as it came, the decision under it, and no way
// anywhere to decide without comparing; rows kept across ticks so the button
// under the pointer is the one pressed. And the title bar's pill, which said
// the service was running while the node answered nothing.
//
// The whole module runs, wiring and intervals included; the intervals are
// captured so this file decides when each one ticks.
//
//   node frontend/test/first-run-pairing.mjs

import { document } from "./dom-shim.mjs";
import { latestToast, toastButtons } from "./fixtures/toasts.mjs";
import { FULL_WIDTH } from "./fixtures/in-english.mjs";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";
import { TEXT as EN } from "../src/i18n/en.js";

globalThis.document = document;
const ticks = [];
globalThis.setInterval = (fn, ms) => {
  ticks.push({ fn, ms });
  return ticks.length;
};
globalThis.setTimeout = (fn, ms) => {
  if ((ms ?? 0) < 5000) queueMicrotask(fn);
  return 0;
};
globalThis.clearTimeout = () => {};
const store = new Map();
globalThis.localStorage = {
  getItem: (key) => (store.has(key) ? store.get(key) : null),
  setItem: (key, value) => store.set(key, String(value)),
  removeItem: (key) => store.delete(key),
};

const failures = [];
const el = (id) => document.getElementById(id);
const flush = async () => {
  for (let turn = 0; turn < 8; turn++) await new Promise((resolve) => setImmediate(resolve));
};

/* ---------------- a machine ready for step 2 ---------------- */

const LOCAL_FP = "9F02 1C7A 44D1 0B3E 77A2 C5D9 1E8F 6B30";
const THEIR_FP = "2B91 6E07 AC33 F14D 58E2 0C9A 71BD 4F36";
const fp = (role, machine, whose, fingerprint) => ({ role, machine, whose, fingerprint });
const outgoing = (id, extra = {}) => ({
  id, direction: "outgoing", nodeId: "node_remote0001", displayName: "demo-book", platform: "darwin/arm64",
  address: "192.168.50.31:7463", state: "pending", fingerprint: THEIR_FP, localFingerprint: LOCAL_FP,
  fingerprints: [fp("requester", "studio", "this machine", LOCAL_FP), fp("receiver", "demo-book", "the other machine", THEIR_FP)],
  nextStep: `On demo-book, run: ah pair approve ${id}`,
  ...extra,
});
const incoming = (id, extra = {}) => ({
  ...outgoing(id),
  direction: "incoming",
  // Requester first, as the node orders it: here the other machine asked.
  fingerprints: [fp("requester", "demo-book", "the other machine", THEIR_FP), fp("receiver", "studio", "this machine", LOCAL_FP)],
  nextStep: `Compare the two fingerprints, then on this machine run: ah pair approve ${id}`,
  ...extra,
});

const candidates = [
  { nodeId: "node_cand_anon0001", address: "192.168.50.87:7463", displayName: "", platform: "", fingerprint: "7C21 E0D4 9B8F 3A56", firstSeen: new Date(Date.now() - 40000).toISOString(), lastSeen: new Date(Date.now() - 12000).toISOString() },
  { nodeId: "node_cand_flag0002", address: "192.168.50.22:7463", displayName: "ubuntu-lab", platform: "linux/amd64", fingerprint: "AAAA BBBB CCCC DDDD", firstSeen: new Date(Date.now() - 120000).toISOString(), lastSeen: new Date(Date.now() - 5000).toISOString(), contested: true, duplicate: true },
];

let machine;
const calls = [];
const reset = () => {
  machine = {
    windowOpen: false, remaining: 300, candidates: [...candidates], requests: [], finished: [],
    nodes: [], requestsThrow: "", closeThrows: false, nodeUp: true, reachableAddress: true,
    requestsGate: null,
  };
  calls.length = 0;
};
reset();
const named = (name) => calls.filter((entry) => entry[0] === name);

const bindings = {
  Overview: async () => {
    calls.push(["Overview"]);
    return machine.nodeUp ? {
      reachable: true, nodeUrl: "http://127.0.0.1:7462",
      node: { id: "node_local", displayName: "studio", platform: "darwin/arm64", fingerprint: LOCAL_FP },
      sessions: [{ id: "claude:a", provider: "claude", status: "active", cwd: "/demo/a", lastSeenAt: new Date().toISOString(),
        audience: { mode: "none", nodes: [], exportCwd: false, acceptMessages: false, allowOutbound: false, autoWake: false } }],
      nodes: [...machine.nodes], peers: [], counts: { total: 1, none: 1 },
    } : { reachable: false, nodeUrl: "http://127.0.0.1:7462", error: "dial tcp 127.0.0.1:7462: connect: connection refused", sessions: [], nodes: [], peers: [], counts: {} };
  },
  ServiceStatus: async () => {
    calls.push(["ServiceStatus"]);
    return { tool: "/usr/local/bin/ah", supported: true, installed: true, running: true, pid: 41, nodeAnswering: machine.nodeUp, unitPath: "~/unit", logHint: "~/log", pinnedSettings: [] };
  },
  Pairing: async () => {
    calls.push(["Pairing"]);
    return {
      availability: "on", windowAvailable: true,
      candidates: machine.windowOpen ? JSON.parse(JSON.stringify(machine.candidates)) : [],
      notice: "Every field here was chosen by whoever sent the packet.", noticeCode: "candidates_unverified",
      state: {
        open: machine.windowOpen, remainingSeconds: machine.windowOpen ? machine.remaining : 0,
        ...(machine.reachableAddress ? { peerAddress: "192.168.50.10:7463", peerAddressReachable: true } : {}),
        announcing: { announceableAddresses: 1 },
      },
    };
  },
  OpenPairing: async (...args) => {
    calls.push(["OpenPairing", ...args]);
    if (machine.openThrows) throw new Error("PAIRING_EXCHANGE_DISABLED: pairing exchange is disabled");
    machine.windowOpen = true;
    return { open: true };
  },
  ClosePairing: async (...args) => {
    calls.push(["ClosePairing", ...args]);
    if (machine.closeThrows) throw new Error("PAIRING_STATE: no window");
    machine.windowOpen = false;
    return { open: false };
  },
  PairRequests: async (all) => {
    calls.push(["PairRequests", all]);
    if (machine.requestsGate) await machine.requestsGate;
    if (machine.requestsThrow) throw new Error(machine.requestsThrow);
    const live = JSON.parse(JSON.stringify(machine.requests));
    return all ? [...live, ...JSON.parse(JSON.stringify(machine.finished))] : live;
  },
  StartPairRequest: async (...args) => {
    calls.push(["StartPairRequest", ...args]);
    const row = outgoing("pair_sent000001", { address: args[0] });
    machine.requests = [row];
    return row;
  },
  ApprovePairRequest: async (...args) => {
    calls.push(["ApprovePairRequest", ...args]);
    const row = machine.requests.find((r) => r.id === args[0]);
    machine.requests = machine.requests.filter((r) => r.id !== args[0]);
    machine.nodes = [{ nodeId: row?.nodeId ?? "n", displayName: "demo-book" }];
    return { ...row, state: "approved", nextStep: "trusted; waiting for their confirm" };
  },
  ConfirmPairRequest: async (...args) => {
    calls.push(["ConfirmPairRequest", ...args]);
    const row = machine.requests.find((r) => r.id === args[0]);
    machine.requests = machine.requests.filter((r) => r.id !== args[0]);
    machine.nodes = [{ nodeId: row?.nodeId ?? "n", displayName: "demo-book" }];
    return { ...row, state: "approved", nextStep: "both machines trust each other" };
  },
  RejectPairRequest: async (...args) => {
    calls.push(["RejectPairRequest", ...args]);
    const row = machine.requests.find((r) => r.id === args[0]);
    machine.requests = machine.requests.filter((r) => r.id !== args[0]);
    return { ...row, state: "rejected", reason: "declined", nextStep: "the other machine was told" };
  },
  NodeSettings: async () => ({ settings: { peerListen: "192.168.50.10:7463", allowLan: true }, saved: { peerListen: "192.168.50.10:7463", allowLan: true } }),
  LocalAddresses: async () => [{ interface: "en0", address: "192.168.50.10", subnet: "192.168.50.0/24", private: true }],
  CopyText: async (text) => {
    calls.push(["CopyText", text]);
    if (machine.copyThrows) throw new Error("clipboard denied");
  },
  InboxCounts: async () => ({ ok: true, counts: {} }),
  Discover: async () => ({ claude: 0, codex: 0, total: 0, skipped: 0 }),
  SetAudience: async () => ({ changed: 0, failed: 0, errors: [] }),
  TrustNode: async () => ({}), RevokeNode: async () => ({}), Heartbeat: async () => "",
  Inbox: async () => ({ messages: [] }), ClearInbox: async () => ({}),
  InstallService: async () => ({}), UninstallService: async () => ({}), RestartService: async () => ({}), RestartNode: async () => ({}),
  HostPlatform: async () => "darwin", Version: async () => ({ release: "unreleased" }),
};

const { configure, boot } = await import("../src/app.js");
configure(bindings);
const app = boot();
const { state, PAIR_TEXT } = app;
await flush();

const tick = (ms) => ticks.find((entry) => entry.ms === ms);

/* ---------------- reading the wizard back ---------------- */

const hidden = (node) => String(node?.className ?? "").split(/\s+/).includes("hidden");
const walk = (node, visit, skipHidden = true) => {
  if (!node || typeof node !== "object") return;
  if (skipHidden && hidden(node)) return;
  visit(node);
  for (const child of node.children ?? []) walk(child, visit, skipHidden);
};
const stage = () => el("first-run-stage");
const all = (predicate, root = stage(), skipHidden = true) => {
  const found = [];
  walk(root, (node) => { if (predicate(node)) found.push(node); }, skipHidden);
  return found;
};
const hasClass = (node, name) => String(node?.className ?? "").split(/\s+/).includes(name);
const byClass = (name, root) => all((node) => hasClass(node, name), root);
const buttons = (root) => all((node) => node.tagName === "button", root);
const button = (label, root) => buttons(root).find((node) => node.textContent === label);
// A press on a button that has to be there; one that is not is a failure
// said in words rather than a TypeError.
const press = (label, root) => {
  const found = button(label, root);
  if (!found) {
    failures.push(`no 「${label}」 to press; the buttons are ${buttons(root).map((node) => node.textContent).join(" | ")}`);
    return;
  }
  found.onclick();
};
const shownText = (root = stage()) => {
  const parts = [];
  walk(root, (node) => { if (node._text) parts.push(node._text); });
  return parts.join(" ");
};
const allText = (root = stage()) => {
  const parts = [];
  walk(root, (node) => { if (node._text) parts.push(node._text); }, false);
  return parts.join(" ");
};
// Document order, for "below".
const order = (root = stage()) => {
  const list = [];
  walk(root, (node) => list.push(node), false);
  return list;
};
const cards = () => byClass("frrequest");
const machines = () => byClass("frmachine");
const primaryOf = (root) => buttons(root).find((node) => hasClass(node, "primary"));
const step = () => state.firstRun.step;

async function toStep2() {
  // A ready machine nobody is paired with: the wizard is up on step 1 with
  // everything done, and its 下一步 is step 2.
  state.firstRun.engaged = true;
  state.firstRun.suspended = false;
  state.firstRun.localOnly = false;
  // The owner's step from here on, as after 上一步: an untouched wizard is
  // moved on to the first step not yet done (syncFirstRun).
  state.firstRun.touched = true;
  state.firstRun.step = 1;
  app.render();
  await flush();
  const next = button(ZH["firstRun.next"]);
  if (!next) {
    failures.push(`step 1 offered no 下一步: ${shownText()}`);
    return;
  }
  next.onclick();
  await flush();
}

/* ---------------- 0. still four intervals ---------------- */

if (ticks.length !== 4) failures.push(`${ticks.length} intervals registered, want the four §4 allows`);
for (const ms of [5000, 2000, 1000, 15000]) if (!tick(ms)) failures.push(`no ${ms} ms interval`);

/* ---------------- 1. entering: OpenPairing(0), and only on step 2 ---------------- */

{
  if (!state.firstRun.engaged) failures.push("the wizard did not come up on a machine with nothing paired");
  // Step 1 is already done here, so the wizard comes up on step 2 — not on a
  // step that has nothing left to do — and step 2 opens the window by being
  // on screen, at launch as anywhere else.
  if (step() !== 2) failures.push(`the wizard opened on step ${step()} over a step 1 that is done, want 2`);
  if (named("OpenPairing").length !== 1) failures.push(`opening on step 2 called OpenPairing ${named("OpenPairing").length} times, want once`);
  // 上一步 still goes back, and step 1 stays the owner's: nothing moves it on.
  press(ZH["firstRun.back"]);
  await flush();
  if (step() !== 1) failures.push(`上一步 from the step the wizard opened on went to step ${step()}, want 1`);
  app.render();
  await flush();
  if (step() !== 1) failures.push(`a repaint after 上一步 moved the wizard on to step ${step()}`);
  if (state.pairing?.state?.open) failures.push("leaving step 2 for step 1 with nothing pending left the window open");
  calls.length = 0;
  app.render();
  await flush();
  if (named("OpenPairing").length !== 0) failures.push("step 1 opened the pairing window");
  // Step 1 is not step 2: neither poll runs for it.
  calls.length = 0;
  tick(5000).fn();
  tick(2000).fn();
  await flush();
  if (named("Pairing").length !== 0) failures.push("the five-second poll read Pairing on step 1");
  if (named("PairRequests").length !== 0) failures.push("the two-second poll read PairRequests on step 1");

  calls.length = 0;
  await toStep2();
  if (step() !== 2) failures.push(`下一步 went to step ${step()}, want 2`);
  const opens = named("OpenPairing");
  if (opens.length !== 1) failures.push(`entering step 2 called OpenPairing ${opens.length} times, want once`);
  else if (JSON.stringify(opens[0].slice(1)) !== "[0]") failures.push(`OpenPairing was called with ${JSON.stringify(opens[0].slice(1))}, want [0]`);
  if (named("PairRequests").length === 0) failures.push("entering step 2 did not read the requests");
  if (!state.pairing?.state?.open) failures.push("the window is not open after entering step 2");
  // A render on the same step is not a second entry.
  app.render();
  await flush();
  if (named("OpenPairing").length !== 1) failures.push("a repaint of step 2 opened the window again");
}

/* ---------------- 2. the polls run for step 2 ---------------- */

{
  calls.length = 0;
  tick(5000).fn();
  await flush();
  if (named("Pairing").length !== 1) failures.push(`the five-second poll read Pairing ${named("Pairing").length} times on step 2, want 1`);
  calls.length = 0;
  tick(2000).fn();
  await flush();
  const reads = named("PairRequests");
  if (reads.length !== 1 || reads[0][1] !== false) failures.push(`the two-second poll on step 2 read ${JSON.stringify(reads)}, want one PairRequests(false)`);
  // Not while a write is in flight.
  calls.length = 0;
  state.busy = true;
  tick(2000).fn();
  await flush();
  state.busy = false;
  if (named("PairRequests").length !== 0) failures.push("the two-second poll read the rows while a write was in flight");
  // The fifteen-second tick leaves the rows to the step's own poll.
  calls.length = 0;
  await app.refreshIncomingPairRequests();
  if (named("PairRequests").length !== 0) failures.push("the fifteen-second tick read the rows while step 2 polls them");
}

/* ---------------- 3. what was found ---------------- */

{
  const rows = machines();
  if (rows.length !== 2) failures.push(`${rows.length} machine rows, want 2`);
  const [anon, flagged] = rows;
  const anonText = shownText(anon);
  if (!anonText.includes(ZH["pair.noName"])) failures.push(`a machine with no name is not 「（未提供名稱）」: ${anonText}`);
  if (!anonText.includes(ZH["firstRun.pair.claimed"])) failures.push("the name is not marked as the machine's own claim");
  if (!anonText.includes(ZH["pair.noPlatform"])) failures.push(`no platform is not said: ${anonText}`);
  const flaggedText = shownText(flagged);
  for (const flag of ["candidate.contested", "candidate.duplicate"]) {
    if (!flaggedText.includes(ZH[flag])) failures.push(`the row does not show ${ZH[flag]}: ${flaggedText}`);
  }
  if (!flaggedText.includes("linux/amd64")) failures.push(`platform missing from the row: ${flaggedText}`);
  if (!/最後看到 .*前/.test(flaggedText)) failures.push(`last seen missing from the row: ${flaggedText}`);
  // Folded, not gone: the id, the announced fingerprint and the address are
  // in the row's own 詳細資料.
  const details = all((node) => node.tagName === "details", flagged)[0];
  if (!details) failures.push("the row has no 詳細資料");
  else {
    const inside = allText(details);
    for (const value of ["node_cand_flag0002", "AAAA BBBB CCCC DDDD", "192.168.50.22:7463"]) {
      if (!inside.includes(value)) failures.push(`${value} is not in the row's 詳細資料`);
    }
  }
  const beside = shownText(flagged).replace(allText(details ?? {}), "");
  if (beside.includes("node_cand_flag0002") && !details) failures.push("the node id is printed outside the details");
  // No candidate string reaches a class.
  for (const node of all(() => true, flagged, false)) {
    if (/ubuntu|linux|node_cand|AAAA/.test(String(node.className ?? ""))) failures.push(`candidate data reached a class: ${node.className}`);
  }
  // Each flag is a line with its reason, shared with the drawer.
  if (!flaggedText.includes(ZH["candidate.contestedWhy"])) failures.push(`the contested flag has no reason in the row: ${flaggedText}`);
  // The notice, in the window's own words, folded under "can this list be
  // trusted?".
  if (!shownText().includes(ZH["candidate.notice.candidates_unverified"])) failures.push("the candidate notice is not shown");
  const noticeFold = all((node) => hasClass(node, "frnoticefold"))[0];
  if (!noticeFold) failures.push("the candidate notice is not in a fold");
  else {
    if (noticeFold.tagName !== "details") failures.push(`the notice fold is a ${noticeFold.tagName}, want details`);
    if (!allText(noticeFold).includes(ZH["candidate.notice.candidates_unverified"])) failures.push("the notice fold does not hold the notice");
  }

  // Send carries the address and nothing else.
  calls.length = 0;
  press(ZH["pair.sendFromCandidate"], flagged);
  await flush();
  const sent = named("StartPairRequest");
  if (sent.length !== 1 || JSON.stringify(sent[0].slice(1)) !== JSON.stringify(["192.168.50.22:7463"])) {
    failures.push(`送出配對請求 called StartPairRequest with ${JSON.stringify(sent.map((entry) => entry.slice(1)))}, want only the address`);
  }
}

/* ---------------- 4. waiting, and cancelling ---------------- */

{
  const [card] = cards();
  if (!card) failures.push("no waiting card after sending");
  else {
    const text = shownText(card);
    if (!text.includes("等 demo-book 按「核准」")) failures.push(`the waiting card does not name who to wait for: ${text}`);
    if (!text.includes(ZH["firstRun.pair.waitSay"])) failures.push("the waiting card does not say what the other side sees");
    if (!text.includes(ZH["firstRun.pair.waitThen"])) failures.push("the waiting card does not say this side compares too");
    if (byClass("frfingerprints", card).length !== 0) failures.push("the fingerprints are on the waiting card");
    // While a request is out, the list of machines is off screen.
    if (machines().length !== 0) failures.push("the machine list is still shown during the exchange");
    calls.length = 0;
    press(ZH["firstRun.pair.cancel"], card);
    await flush();
    const rejects = named("RejectPairRequest");
    if (rejects.length !== 1 || rejects[0][1] !== "pair_sent000001") failures.push(`取消這次請求 called ${JSON.stringify(rejects)}, want RejectPairRequest(pair_sent000001)`);
    if (named("ApprovePairRequest").length + named("ConfirmPairRequest").length !== 0) failures.push("cancelling approved or confirmed something");
  }
  if (cards().length !== 0) failures.push("the cancelled request is still on screen");
  if (machines().length !== 2) failures.push("the machine list did not come back after cancelling");
}

/* ---------------- 5. typing an address ---------------- */

{
  const input = all((node) => node.tagName === "input" && node.type === "text", stage(), false)[0];
  const send = button(ZH["firstRun.pair.send"], stage()) ?? all((node) => node.tagName === "button" && node.textContent === ZH["firstRun.pair.send"], stage(), false)[0];
  if (!input || !send) failures.push("no address field in 找不到另一台？");
  else {
    calls.length = 0;
    input.value = "   ";
    send.onclick();
    await flush();
    if (named("StartPairRequest").length !== 0) failures.push("an empty address was sent");
    if (latestToast(document).kind !== "error" || !latestToast(document).textContent.includes(ZH["pair.addressEmpty"])) {
      failures.push(`an empty address did not give the error toast: ${latestToast(document).textContent}`);
    }
    input.value = "  192.168.50.44:7463 ";
    input.onkeydown({ key: "Enter" });
    await flush();
    const sent = named("StartPairRequest");
    if (sent.length !== 1 || JSON.stringify(sent[0].slice(1)) !== JSON.stringify(["192.168.50.44:7463"])) {
      failures.push(`Enter sent ${JSON.stringify(sent.map((entry) => entry.slice(1)))}, want the trimmed address alone`);
    }
    if (input.value !== "") failures.push("the field was not emptied after the request went out");
  }
  // This machine's address, with its copy button.
  const here = allText(stage());
  if (!here.includes("192.168.50.10:7463")) failures.push("this machine's address is not in 找不到另一台？");
  machine.requests = [];
  await app.loadPairRequests();
  await flush();
}

/* ---------------- 6. comparing: theirs first, ours after approval ---------------- */

{
  machine.requests = [incoming("pair_in000000001")];
  await app.loadPairRequests();
  await flush();
  const [card] = cards();
  if (!card) failures.push("no compare card for an incoming request");
  else {
    const text = shownText(card);
    if (!text.includes("demo-book（自稱）想和這台配對")) failures.push(`the incoming title is not the spec's: ${text}`);
    if (!text.includes(ZH["firstRun.pair.compareSay"])) failures.push("the compare card does not say how to compare");
    if (!text.includes(ZH["pair.compare"])) failures.push("the compare card lost PAIR_TEXT.compare");
    // The node's array, in the node's order, with the node's values.
    const values = byClass("fingerprint", byClass("frfingerprints", card)[0]).map((node) => node.textContent);
    if (JSON.stringify(values) !== JSON.stringify([THEIR_FP, LOCAL_FP])) failures.push(`fingerprints drawn as ${JSON.stringify(values)}, want the node's order`);
    const labels = allText(byClass("frfingerprints", card)[0]);
    for (const label of [ZH["pair.role.requester"], ZH["pair.role.receiver"], ZH["pair.whose.this-machine"], ZH["pair.whose.the-other-machine"]]) {
      if (!labels.includes(label)) failures.push(`the fingerprint block lost the label ${label}`);
    }
    // The warning above the block, the buttons below it.
    const nodes = order(card);
    const warn = nodes.findIndex((node) => node._text === ZH["pair.compare"]);
    const block = nodes.indexOf(byClass("frfingerprints", card)[0]);
    const approve = button(ZH["firstRun.pair.approve"], card);
    const different = button(ZH["firstRun.pair.different"], card);
    if (!approve || !different) failures.push(`the incoming card's buttons are ${buttons(card).map((b) => b.textContent).join(" | ")}`);
    else {
      if (!(warn >= 0 && warn < block)) failures.push("PAIR_TEXT.compare is not above the fingerprints");
      if (!(nodes.indexOf(approve) > block && nodes.indexOf(different) > block)) failures.push("a decision button is not below the fingerprints");
      // Different refuses.
      calls.length = 0;
      different.onclick();
      await flush();
      const rejects = named("RejectPairRequest");
      if (rejects.length !== 1 || rejects[0][1] !== "pair_in000000001") failures.push(`不一樣，取消 called ${JSON.stringify(rejects)}`);
      if (named("ApprovePairRequest").length !== 0) failures.push("不一樣 approved");
    }
  }

  // Approve for theirs.
  machine.requests = [incoming("pair_in000000002")];
  await app.loadPairRequests();
  await flush();
  calls.length = 0;
  press(ZH["firstRun.pair.approve"], cards()[0]);
  await flush();
  const approves = named("ApprovePairRequest");
  if (approves.length !== 1 || approves[0][1] !== "pair_in000000002") failures.push(`一樣，核准 called ${JSON.stringify(approves)}`);
  const said = latestToast(document);
  if (!said.textContent.includes(ZH["pair.step.approved-incoming"]) || !said.textContent.includes("（AgentHub 回報：trusted; waiting for their confirm）")) {
    failures.push(`the approve toast is not the window's sentence with the node's in brackets: ${said.textContent}`);
  }
  if (toastButtons(said.node).some((node) => node.textContent === ZH["pair.goPublish"])) {
    failures.push("the wizard's approve toast offers 去公開 session, a way out of the wizard");
  }
  // Paired: the step is done, and 下一步 is the one primary button.
  if (!String(el("first-run-steps").children[1]?.className).includes("done")) failures.push("a paired machine did not tick step 2");
  const primaries = buttons().filter((node) => hasClass(node, "primary"));
  if (primaries.length !== 1 || primaries[0].textContent !== ZH["firstRun.next"]) {
    failures.push(`with a machine paired the primary buttons are ${primaries.map((b) => b.textContent).join(" | ")}, want 下一步 alone`);
  }
  if (machines().length === 0) failures.push("pairing another machine is not offered after the first");

  // Confirm for ours, once they approved.
  machine.nodes = [];
  await app.load();
  machine.requests = [outgoing("pair_out00000003", { state: "awaiting-confirm" })];
  await app.loadPairRequests();
  await flush();
  const [mine] = cards();
  const confirm = mine && button(ZH["firstRun.pair.confirm"], mine);
  if (!confirm) failures.push(`the awaiting-confirm card's buttons are ${mine ? buttons(mine).map((b) => b.textContent).join(" | ") : "none"}`);
  else {
    const values = byClass("fingerprint", byClass("frfingerprints", mine)[0]).map((node) => node.textContent);
    if (JSON.stringify(values) !== JSON.stringify([LOCAL_FP, THEIR_FP])) failures.push(`awaiting-confirm drew ${JSON.stringify(values)}, want the node's order`);
    const nodes = order(mine);
    if (!(nodes.indexOf(confirm) > nodes.indexOf(byClass("frfingerprints", mine)[0]))) failures.push("完成配對 is not below the fingerprints");
    calls.length = 0;
    confirm.onclick();
    await flush();
    const confirms = named("ConfirmPairRequest");
    if (confirms.length !== 1 || confirms[0][1] !== "pair_out00000003") failures.push(`一樣，完成配對 called ${JSON.stringify(confirms)}`);
    if (named("ApprovePairRequest").length !== 0) failures.push("confirming ours called Approve");
  }
}

/* ---------------- 6b. the compare card and the copy line, in English ---------------- */

{
  // The compare screen's words name the button that is on it: 「不一樣，拒絕」,
  // not the drawer's 拒絕 (PAIR_TEXT.compare, shared with the drawer, names
  // no button at all since 2026-10-01).
  for (const [name, table] of [["zh-Hant", ZH], ["en", EN]]) {
    for (const key of ["firstRun.pair.compareSay", "firstRun.pair.compareWhy"]) {
      if (!table[key]?.includes(table["firstRun.pair.different"])) failures.push(`${name} ${key} does not name the button ${table["firstRun.pair.different"]}`);
    }
  }
  machine.nodes = [];
  await app.load();
  machine.requests = [];
  await app.loadPairRequests();
  await flush();
  app.setUILanguage("en");
  // A card drawn in English from the start, so what is read is what the
  // English window writes, not a Chinese one left over from before a switch.
  machine.requests = [incoming("pair_in_english001")];
  await app.loadPairRequests();
  await flush();
  const [card] = cards();
  if (!card) failures.push("no compare card in English");
  else {
    const text = allText(card);
    const mark = text.match(FULL_WIDTH);
    if (mark) failures.push(`the English compare card carries full-width ${JSON.stringify(mark[0])}: ${text.slice(Math.max(0, mark.index - 40), mark.index + 40)}`);
    if (!text.includes(`(${EN["pair.whose.this-machine"]})`)) failures.push(`the English fingerprint labels are not bracketed in ASCII: ${text}`);
    if (!text.includes(EN["firstRun.pair.compareWhy"])) failures.push("the compare card's 「說明」 is not the wizard's own");
  }
  machine.requests = [];
  await app.loadPairRequests();
  await flush();
  // The copy line's failure, which glued the clipboard's error on in
  // full-width brackets.
  machine.copyThrows = true;
  const copy = all((node) => node.tagName === "button" && node.textContent === EN["common.copy"], stage(), false)[0];
  if (!copy) failures.push(`no Copy beside this machine's address in English: ${allText(stage())}`);
  else {
    await copy.onclick();
    await flush();
    const said = allText(stage());
    if (!said.includes(EN["pair.hereCopyFailed"])) failures.push(`a copy that failed did not say so: ${said}`);
    const mark = said.match(FULL_WIDTH);
    if (mark) failures.push(`the English step 2 carries full-width ${JSON.stringify(mark[0])}: ${said.slice(Math.max(0, mark.index - 60), mark.index + 30)}`);
  }
  machine.copyThrows = false;
  app.setUILanguage("zh-Hant");
  await flush();
}

/* ---------------- 7. a node without the ordered pair ---------------- */

{
  machine.nodes = [];
  await app.load();
  machine.requests = [incoming("pair_in000000004", { fingerprints: undefined })];
  await app.loadPairRequests();
  await flush();
  const text = shownText(cards()[0]);
  if (!text.includes(ZH["pair.noFingerprintPair"])) failures.push(`a request without the node's array did not say so: ${text}`);
  if (text.includes(THEIR_FP)) failures.push("the window made up a fingerprint pair the node did not send");
}

/* ---------------- 8. nothing decides by itself ---------------- */

{
  // Every tick, several times over, with a request waiting: nothing is
  // approved, confirmed or refused without a press.
  machine.requests = [incoming("pair_in000000005"), outgoing("pair_out00000006", { state: "awaiting-confirm" })];
  await app.loadPairRequests();
  calls.length = 0;
  for (let round = 0; round < 3; round++) {
    for (const entry of ticks) entry.fn();
    await flush();
  }
  const decided = [...named("ApprovePairRequest"), ...named("ConfirmPairRequest"), ...named("RejectPairRequest")];
  if (decided.length !== 0) failures.push(`the ticks decided a request on their own: ${JSON.stringify(decided)}`);
  // And no words for it anywhere: not on the screen, not in any of this
  // step's strings in either language.
  const forbidden = ["自動核准", "略過比對", "全部核准", "不比對", "auto-approve", "approve all", "skip the compar", "without compar"];
  const texts = [allText(stage()),
    ...Object.entries(ZH).filter(([key]) => key.startsWith("firstRun.pair.") || key.startsWith("firstRun.step2.")).map(([, value]) => value),
    ...Object.entries(EN).filter(([key]) => key.startsWith("firstRun.pair.") || key.startsWith("firstRun.step2.")).map(([, value]) => value)];
  for (const text of texts) {
    for (const word of forbidden) if (text.toLowerCase().includes(word)) failures.push(`「${word}」 appears in step 2: ${text.slice(0, 80)}`);
  }
  // Every decision button belongs to one request row and names one verb.
  const decisions = buttons().filter((node) => [ZH["firstRun.pair.approve"], ZH["firstRun.pair.confirm"]].includes(node.textContent));
  if (decisions.length !== 2) failures.push(`${decisions.length} approve/confirm buttons for two requests`);
}

/* ---------------- 9. the tick keeps what the owner is reaching for ---------------- */

{
  const before = cards();
  const beforeButtons = before.map((card) => buttons(card));
  const live = byClass("frlive")[0];
  let writes = 0;
  const realReplace = live.replaceChildren;
  live.replaceChildren = function spy(...kids) {
    writes += 1;
    return realReplace.apply(this, kids);
  };
  for (let round = 0; round < 3; round++) {
    tick(2000).fn();
    tick(5000).fn();
    await flush();
  }
  delete live.replaceChildren;
  const after = cards();
  if (after.length !== before.length || after.some((card, index) => card !== before[index])) failures.push("a tick replaced a request card");
  after.forEach((card, index) => {
    const now = buttons(card);
    if (now.length !== beforeButtons[index].length || now.some((node, i) => node !== beforeButtons[index][i])) {
      failures.push("a tick replaced a decision button");
    }
  });
  if (writes !== 0) failures.push(`the request cards' container was written ${writes} times over unchanged data`);

  // The machines found, likewise.
  machine.requests = [];
  await app.loadPairRequests();
  await flush();
  const rowsBefore = machines();
  const box = byClass("frmachines")[0];
  let boxWrites = 0;
  const realBox = box.replaceChildren;
  box.replaceChildren = function spy(...kids) {
    boxWrites += 1;
    return realBox.apply(this, kids);
  };
  for (let round = 0; round < 3; round++) {
    tick(5000).fn();
    tick(2000).fn();
    await flush();
  }
  delete box.replaceChildren;
  const rowsAfter = machines();
  if (rowsAfter.length !== rowsBefore.length || rowsAfter.some((row, index) => row !== rowsBefore[index])) failures.push("a tick replaced a machine row");
  if (boxWrites !== 0) failures.push(`the machine list was written ${boxWrites} times over unchanged candidates`);

  // The security exception: a kept card whose fingerprints changed is thrown
  // away, so a press aimed at the old values cannot land on the new ones.
  machine.requests = [incoming("pair_in000000007")];
  await app.loadPairRequests();
  await flush();
  const old = cards()[0];
  const oldApprove = button(ZH["firstRun.pair.approve"], old);
  machine.requests = [incoming("pair_in000000007", {
    fingerprints: [fp("requester", "demo-book", "the other machine", "FFFF 0000 FFFF 0000"), fp("receiver", "studio", "this machine", LOCAL_FP)],
  })];
  await app.loadPairRequests();
  await flush();
  const fresh = cards()[0];
  if (fresh === old) failures.push("a card whose fingerprints changed was reused");
  if (button(ZH["firstRun.pair.approve"], fresh) === oldApprove) failures.push("the approve button survived a fingerprint change");
  // And a repeated id does not take over another row.
  machine.requests = [incoming("pair_in000000007"), incoming("pair_in000000007", { displayName: "impostor" })];
  await app.loadPairRequests();
  await flush();
  const pair = cards();
  if (pair.length !== 2 || pair[0] === pair[1]) failures.push("two requests with one id shared a card");
  machine.requests = [];
  await app.loadPairRequests();
  await flush();
}

/* ---------------- 9b. a failed read, and the drawer's checkbox ---------------- */

{
  // A read that fails is not a fact about the other machine, and neither is
  // the last good list any more: nothing is offered to decide on.
  machine.requests = [incoming("pair_in000000011")];
  await app.loadPairRequests();
  await flush();
  if (cards().length !== 1) failures.push("the card for the failed-read case is not up");
  machine.requestsThrow = "dial tcp: refused";
  tick(2000).fn();
  await flush();
  if (cards().length !== 0) failures.push("a failed read left the last list's cards on screen");
  if (buttons().some((node) => node.textContent === ZH["firstRun.pair.approve"])) failures.push("a failed read left 一樣，核准 pressable");
  if (!shownText().includes(ZH["pair.requestsFailed"])) failures.push("a failed read was not said");
  machine.requestsThrow = "";
  tick(2000).fn();
  await flush();
  if (cards().length !== 1) failures.push("the card did not come back once a read answered");
  // The drawer's 「顯示已結束」 is the drawer's: step 2 still reads live rows.
  state.pairRequestsAll = true;
  calls.length = 0;
  tick(2000).fn();
  await flush();
  const reads = named("PairRequests").map((entry) => entry[1]);
  state.pairRequestsAll = false;
  if (JSON.stringify(reads) !== "[false]") failures.push(`with the drawer's box ticked step 2 polled ${JSON.stringify(reads)}, want [false]`);
  machine.requests = [];
  await app.loadPairRequests();
  await flush();
}

/* ---------------- 10. an ending somebody else caused ---------------- */

{
  machine.requests = [outgoing("pair_out00000008")];
  await app.loadPairRequests();
  await flush();
  if (cards().length !== 1) failures.push("the waiting card for the mismatch case is not up");
  machine.requests = [];
  machine.finished = [outgoing("pair_out00000008", { state: "rejected", reason: "fingerprint_mismatch", nextStep: "they said the fingerprints differ" })];
  calls.length = 0;
  tick(2000).fn();
  await flush();
  const reads = named("PairRequests").map((entry) => entry[1]);
  if (!reads.includes(true)) failures.push(`a request that vanished was not looked up: ${JSON.stringify(reads)}`);
  const ended = byClass("frended")[0];
  const text = ended ? shownText(ended) : "";
  if (!text.includes(ZH["pair.step.rejected-fingerprint-mismatch"])) failures.push(`a fingerprint mismatch was not said as itself: ${text}`);
  if (text.includes(ZH["pair.step.rejected"])) failures.push("a fingerprint mismatch was said as an ordinary refusal");
  if (!text.includes("（AgentHub 回報：they said the fingerprints differ）")) failures.push(`the finished row lost the node's next step: ${text}`);
  // An ending this window caused is not looked up again.
  machine.finished = [];
  machine.requests = [outgoing("pair_out00000009")];
  await app.loadPairRequests();
  await flush();
  calls.length = 0;
  press(ZH["firstRun.pair.cancel"], cards()[0]);
  await flush();
  if (named("PairRequests").some((entry) => entry[1] === true)) failures.push("the window looked up the ending of a request it cancelled itself");
  button(ZH["firstRun.pair.dismissEnded"])?.onclick();
  await flush();
}

/* ---------------- 11. the window runs out while the step is up ---------------- */

{
  if (!state.pairing?.state?.open) failures.push("the window is not open before the expiry check");
  calls.length = 0;
  // Its time is up on this machine's clock; the node closes it.
  state.pairingReadAt = performance.now() - 10_000_000;
  machine.windowOpen = false;
  tick(1000).fn();
  await flush();
  if (named("Pairing").length === 0) failures.push("the count reaching zero on step 2 did not ask the node");
  const reopened = named("OpenPairing");
  if (reopened.length !== 1 || JSON.stringify(reopened[0].slice(1)) !== "[0]") {
    failures.push(`an expired window on step 2 was reopened ${JSON.stringify(reopened)}, want one OpenPairing(0)`);
  }

  // A node that refuses is told about once, not on every poll. The window
  // reopened above is seen open by a poll first, as it would be.
  tick(5000).fn();
  await flush();
  machine.openThrows = true;
  state.pairingReadAt = performance.now() - 10_000_000;
  machine.windowOpen = false;
  calls.length = 0;
  tick(1000).fn();
  await flush();
  for (let round = 0; round < 3; round++) {
    tick(5000).fn();
    await flush();
  }
  const refused = named("OpenPairing").length;
  machine.openThrows = false;
  if (refused !== 1) failures.push(`a node refusing the window was asked ${refused} times, want once`);
  // Open again for what follows.
  await app.loadPairing();
  state.firstRun.step = 1;
  app.render();
  await flush();
  state.firstRun.step = 2;
  app.render();
  await flush();
  if (!state.pairing?.state?.open) failures.push("re-entering step 2 did not open the window again");
}

/* ---------------- 11b. entered while a write is in flight ---------------- */

{
  // Off step 2 with nothing pending, so the window closes and entering again
  // is an entry.
  machine.requests = [];
  await app.loadPairRequests();
  state.firstRun.touched = true;
  state.firstRun.step = 1;
  app.render();
  await flush();
  machine.windowOpen = false;
  await app.loadPairing();
  // Settings → Appearance's 「顯示首次設定」 is not pressable during a write,
  // as 「繼續設定」 is not.
  state.busy = true;
  app.render();
  if (!el("settings-show-onboarding").disabled) failures.push("「顯示首次設定」 was pressable while a write was in flight");
  // Step 2 entered during the write anyway (a step that lands on 2): the
  // write holds OpenPairing back, and the opening is owed, not forgotten.
  calls.length = 0;
  state.firstRun.step = 2;
  app.render();
  await flush();
  if (named("OpenPairing").length !== 0) failures.push("step 2 opened the window under a write in flight");
  tick(5000).fn();
  await flush();
  if (named("OpenPairing").length !== 0) failures.push("a tick during the write opened the window");
  state.busy = false;
  app.render();
  if (el("settings-show-onboarding").disabled) failures.push("「顯示首次設定」 stayed disabled after the write");
  tick(5000).fn();
  await flush();
  const owed = named("OpenPairing");
  if (owed.length !== 1) failures.push(`the tick after the write called OpenPairing ${owed.length} times, want once`);
  else if (JSON.stringify(owed[0].slice(1)) !== "[0]") failures.push(`the owed opening passed ${JSON.stringify(owed[0].slice(1))}, want [0]`);
  if (!state.pairing?.state?.open) failures.push("step 2 entered during a write never got its window");
  for (let round = 0; round < 3; round++) {
    tick(5000).fn();
    await flush();
  }
  if (named("OpenPairing").length !== 1) failures.push(`an open window was asked for again: ${named("OpenPairing").length} calls`);

  // A node that refuses the owed opening is asked once, not on every tick.
  state.firstRun.step = 1;
  app.render();
  await flush();
  machine.windowOpen = false;
  await app.loadPairing();
  machine.openThrows = true;
  state.busy = true;
  calls.length = 0;
  state.firstRun.step = 2;
  app.render();
  await flush();
  state.busy = false;
  app.render();
  for (let round = 0; round < 3; round++) {
    tick(5000).fn();
    await flush();
  }
  if (named("OpenPairing").length !== 1) failures.push(`a node refusing the owed opening was asked ${named("OpenPairing").length} times, want once`);
  machine.openThrows = false;
  // Open again for what follows.
  state.firstRun.step = 1;
  app.render();
  await flush();
  state.firstRun.step = 2;
  app.render();
  await flush();
  if (!state.pairing?.state?.open) failures.push("re-entering step 2 after the write cases did not open the window");
}

/* ---------------- 12. leaving ---------------- */

{
  // Nothing pending: 先跳過 closes the window, after reading the rows again.
  machine.requests = [];
  await app.loadPairing();
  calls.length = 0;
  press(ZH["firstRun.step2.skip"]);
  await flush();
  if (step() !== 3) failures.push(`先跳過 went to step ${step()}, want 3`);
  const seq = calls.map((entry) => entry[0]).filter((name) => name === "PairRequests" || name === "ClosePairing");
  if (JSON.stringify(seq) !== JSON.stringify(["PairRequests", "ClosePairing"])) failures.push(`leaving step 2 ran ${JSON.stringify(seq)}, want a re-read then ClosePairing`);
  // Off step 2 the polls stop.
  calls.length = 0;
  tick(5000).fn();
  tick(2000).fn();
  await flush();
  if (named("Pairing").length + named("PairRequests").length !== 0) failures.push(`polls ran on step 3: ${JSON.stringify(calls)}`);

  // Back, with a request mid-exchange: the window stays open.
  state.firstRun.step = 2;
  app.render();
  await flush();
  machine.requests = [incoming("pair_in000000010")];
  await app.loadPairRequests();
  calls.length = 0;
  press(ZH["firstRun.back"]);
  await flush();
  if (step() !== 1) failures.push(`上一步 went to step ${step()}, want 1`);
  if (named("ClosePairing").length !== 0) failures.push("leaving with a request pending closed the window under it");

  // 稍後再設定 is not pressable while a write is in flight: leaving then
  // would skip closing the window, and nothing would come back to close it.
  state.firstRun.step = 2;
  app.render();
  await flush();
  state.busy = true;
  app.render();
  const laterBusy = el("first-run-later").disabled;
  // Every other way off step 2 as well, 回到第 1 步 included (shown only when
  // this machine cannot be reached, so looked for among the hidden too).
  const exits = [ZH["firstRun.next"], ZH["firstRun.step2.skip"], ZH["firstRun.back"], ZH["firstRun.pair.backToStep1"]];
  const liveExits = all((node) => node.tagName === "button" && exits.includes(node.textContent) && !node.disabled, stage(), false);
  if (liveExits.length !== 0) failures.push(`ways off step 2 stayed pressable during a write: ${liveExits.map((node) => node.textContent).join(" | ")}`);
  state.busy = false;
  app.render();
  await flush();
  if (!laterBusy) failures.push("稍後再設定 was pressable while a write was in flight");

  // A read that fails cannot say nobody is waiting — even when the last read
  // that did answer said nobody was.
  machine.requests = [];
  state.firstRun.step = 2;
  app.render();
  await flush();
  await app.loadPairRequests();
  await flush();
  if ((state.pairRequests ?? []).length !== 0) failures.push("the failed-read case did not start from an empty list, so it proves nothing");
  if (!state.pairing?.state?.open) failures.push("the failed-read case has no open window to keep, so it proves nothing");
  machine.requestsThrow = "dial tcp: refused";
  calls.length = 0;
  el("first-run-later").onclick();
  await flush();
  machine.requestsThrow = "";
  if (named("ClosePairing").length !== 0) failures.push("leaving on a failed read closed the window");

  // Back on step 2 by the time the read answers: not closed.
  state.firstRun.engaged = true;
  state.firstRun.step = 2;
  app.render();
  await flush();
  let release;
  machine.requestsGate = new Promise((resolve) => { release = resolve; });
  calls.length = 0;
  press(ZH["firstRun.back"]);
  await flush();
  state.firstRun.step = 2;
  app.render();
  machine.requestsGate = null;
  release();
  await flush();
  if (named("ClosePairing").length !== 0) failures.push("the window was closed although step 2 was back on screen when the read answered");

  // A trip to another view is leaving too.
  machine.requests = [];
  await app.loadPairing();
  calls.length = 0;
  app.goToNodeSettings();
  await flush();
  if (named("ClosePairing").length !== 1) failures.push(`stepping out of the wizard from step 2 closed the window ${named("ClosePairing").length} times, want 1`);
}

/* ---------------- 13. only on this machine: nothing opened ---------------- */

{
  machine.windowOpen = false;
  await app.loadPairing();
  state.firstRun.suspended = false;
  state.firstRun.engaged = true;
  state.firstRun.localOnly = true;
  calls.length = 0;
  state.firstRun.step = 2;
  app.render();
  await flush();
  tick(5000).fn();
  tick(2000).fn();
  await flush();
  if (named("OpenPairing").length !== 0) failures.push("step 2 opened the window for an owner who chose this machine only");
  if (named("Pairing").length + named("PairRequests").length !== 0) failures.push("step 2 polled for an owner who chose this machine only");
  state.firstRun.localOnly = false;
  state.firstRun.step = 1;
  app.render();
  await flush();
}

/* ---------------- 14. the pill: running is not answering ---------------- */

{
  // Out of the wizard, where the pill is on screen.
  app.closeFirstRun();
  machine.nodeUp = false;
  await app.load();
  await flush();
  const pill = el("service-pill");
  const words = el("service-pill-text").textContent;
  if (words === ZH["service.pillRunning"]) failures.push("the pill says the service is running while the node answers nothing");
  if (words !== ZH["service.pillRunningNoAnswer"]) failures.push(`the pill says ${words}, want ${ZH["service.pillRunningNoAnswer"]}`);
  if (hasClass(pill, "ok")) failures.push("the pill is green while the node answers nothing");
  if (!hasClass(pill, "warn")) failures.push("the pill is not amber while the node answers nothing");
  // Its press is the quick action, which reads the status again first.
  calls.length = 0;
  pill.onclick();
  await flush();
  if (named("ServiceStatus").length === 0) failures.push("the pill's press did not read the status again");
  // Answering again: green.
  machine.nodeUp = true;
  await app.load();
  await flush();
  if (el("service-pill-text").textContent !== ZH["service.pillRunning"] || !hasClass(pill, "ok")) {
    failures.push(`an answering node's running service is not green: ${el("service-pill-text").textContent}`);
  }
  if (!EN["service.pillRunningNoAnswer"]) failures.push("the pill's new sentence has no English");
}

if (failures.length > 0) {
  for (const failure of failures) console.error(` - ${failure}`);
  process.exit(1);
}
console.log("first-run step 2: the drawer's window rules, its two polls and no fifth interval, "
  + "the address alone sent, the node's fingerprints as they came with the decision under them, "
  + "nothing decided by itself, kept rows, and a pill that does not call a silent node running");
process.exit(0);
