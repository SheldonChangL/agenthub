// The pairing exchange, driven from inside the window (issue #63).
//
// What this covers is the half of the exchange a person actually performs. The
// node moves the keys and derives the fingerprints; the only thing that decides
// whether two machines are the ones they say they are is a person reading two
// screens. So the checks here are about what that person is shown and in what
// order:
//
//   - both fingerprints, in the order the node gave them, with the node's own
//     labels. One value per screen is the version people get wrong: they see
//     two different strings, assume that is how it works, and confirm.
//   - the decision below the values it is about, and no affordance anywhere for
//     deciding without comparing.
//   - the three states kept apart. "Waiting for you", "waiting for them" and
//     "finished" look alike from a distance and mean different things about
//     whose turn it is.
//   - a candidate row can pick who is dialled and nothing else. Every field of
//     one was written by whoever sent the packet.
//
// Driven through the whole module, because most of it is button paths: a
// handler that was never wired has to fail here rather than pass because the
// test called the function itself.
//
//   node frontend/test/pairing-exchange.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;

const failures = [];
const el = (id) => document.getElementById(id);
const settle = () => new Promise((resolve) => setTimeout(resolve, 30));

const ticks = [];
globalThis.setInterval = (fn, ms) => {
  ticks.push({ fn, ms });
  return ticks.length;
};
globalThis.confirm = () => true;

const LOCAL_FP = "9F02 1C7A 44D1 0B3E 77A2 C5D9 1E8F 6B30";
const THEIR_FP = "7C21 E0D4 9B8F 3A56 C7D2 1E40 8F9B 6A03";
const fp = (role, machine, whose, fingerprint) => ({ role, machine, whose, fingerprint });

// One of each shape the panel has to keep apart.
const incoming = {
  id: "pair_incoming000001", direction: "incoming", nodeId: "node_remote0001",
  displayName: "ubuntu-lab", platform: "linux/amd64", address: "192.168.50.87:7463",
  state: "pending", fingerprint: THEIR_FP, localFingerprint: LOCAL_FP,
  // Requester first, as the node orders it: on an incoming request the other
  // machine asked.
  fingerprints: [
    fp("requester", "ubuntu-lab", "the other machine", THEIR_FP),
    fp("receiver", "sheldon-mbp", "this machine", LOCAL_FP),
  ],
  nextStep: "Compare the two fingerprints, then on this machine run: ah pair approve pair_incoming000001",
};
const outgoing = {
  id: "pair_outgoing00001", direction: "outgoing", nodeId: "node_remote0002",
  displayName: "win-bench", platform: "windows/amd64", address: "192.168.50.31:7463",
  state: "awaiting-confirm", fingerprint: "1A5B 77C0 E93D 4826", localFingerprint: LOCAL_FP,
  fingerprints: [
    fp("requester", "sheldon-mbp", "this machine", LOCAL_FP),
    fp("receiver", "win-bench", "the other machine", "1A5B 77C0 E93D 4826"),
  ],
  nextStep: "win-bench approved it. On this machine, run: ah pair confirm pair_outgoing00001",
};
const outgoingPending = { ...outgoing, id: "pair_outgoing00003", state: "pending", displayName: "slow-box" };
const expired = {
  id: "pair_expired000001", direction: "outgoing", nodeId: "node_remote0003",
  displayName: "node_remote0003", platform: "", address: "192.168.50.44:7463",
  state: "expired", reason: "expired", fingerprint: "40FE 1C39", localFingerprint: LOCAL_FP,
  fingerprints: [
    fp("requester", "sheldon-mbp", "this machine", LOCAL_FP),
    fp("receiver", "node_remote0003", "the other machine", "40FE 1C39"),
  ],
  nextStep: "It ran out (expired). Nothing was trusted; start again if you still want to pair.",
};

let pending = [incoming, outgoing];
let decided = [expired];
const requestCalls = [];
let requestsThrow = "";
const PairRequests = async (all) => {
  requestCalls.push(all);
  if (requestsThrow) throw new Error(requestsThrow);
  return all ? [...pending, ...decided] : pending;
};

const started = [];
let startThrow = "";
const StartPairRequest = async (address) => {
  started.push(address);
  if (startThrow) throw new Error(startThrow);
  return { ...outgoingPending, address };
};

const decisions = [];
let decideThrow = "";
const decide = (verb) => async (id) => {
  decisions.push([verb, id]);
  if (decideThrow) throw new Error(decideThrow);
  return { ...incoming, id, state: verb === "reject" ? "rejected" : "approved", nextStep: `done: ${verb}` };
};

const copied = [];
let copyThrows = false;
const CopyText = async (text) => {
  copied.push(text);
  if (copyThrows) throw new Error("no clipboard on this display");
};

let pairingAnswer = {
  availability: "on",
  windowAvailable: true,
  state: {
    open: true, remainingSeconds: 200, displayName: "sheldon-mbp", nameIsChosen: false,
    announcing: { announceableAddresses: 1, lastAnnouncedAt: new Date().toISOString() },
  },
  candidates: [],
};
const Pairing = async () => pairingAnswer;

let overviewCalls = 0;
const Overview = async () => {
  overviewCalls += 1;
  return {
    node: { id: "node_local00000", displayName: "sheldon-mbp", platform: "darwin/arm64", fingerprint: LOCAL_FP },
    sessions: [], nodes: [], peers: [], counts: { total: 0 },
    nodeUrl: "http://127.0.0.1:7462", reachable: true,
  };
};
const noop = async () => ({});

const { configure, boot } = await import("../src/app.js");
configure({
  Overview, Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop,
  SetNodeAddress: noop, Heartbeat: noop, Pairing, OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText, ServiceStatus: noop,
  InstallService: noop, UninstallService: noop, LocalAddresses: noop,
  PairRequests, StartPairRequest,
  ApprovePairRequest: decide("approve"), ConfirmPairRequest: decide("confirm"),
  RejectPairRequest: decide("reject"),
});
const scope = boot();
const { state, PAIR_TEXT } = scope;
state.view = "network";

await settle();

/* ---------------- 1. the drawer asks, and only while it is open ---------- */

// Reading the rows makes the node dial every machine it is waiting on, so the
// poll is tied to the drawer rather than to the view.
const fastTick = ticks.find((tick) => tick.ms === 2000);
if (!fastTick) {
  failures.push("no two-second interval was registered for the pairing requests");
}
const beforeOpen = requestCalls.length;
fastTick?.fn();
await settle();
if (requestCalls.length !== beforeOpen) {
  failures.push("the pairing requests were polled with the drawer closed; each read dials every waiting machine");
}

scope.openPairingDrawer();
await settle();
if (requestCalls.length === beforeOpen) {
  failures.push("opening the drawer did not read the pairing requests");
}
if (requestCalls.at(-1) !== false) {
  failures.push(`the default read asked for all=${requestCalls.at(-1)}, want the undecided rows only`);
}

const rowsHTML = () => el("pair-requests").serialize();

/* ---------------- 2. both fingerprints, in the node's order -------------- */

let html = rowsHTML();
if (!html.includes(THEIR_FP) || !html.includes(LOCAL_FP)) {
  failures.push("an incoming request did not show both fingerprints; one per screen is the version people get wrong");
}
// The order is the node's, and it is the same on both machines by
// construction. Re-deriving or sorting here breaks the only check the exchange
// has, so the requester's value must come first in the DOM.
if (html.indexOf(THEIR_FP) > html.indexOf(LOCAL_FP)) {
  failures.push("the incoming row printed this machine's fingerprint before the requester's, against the node's order");
}
// The node's labels, so the two screens name the same machine the same way.
for (const required of ["ubuntu-lab", "sheldon-mbp", PAIR_TEXT.whose["this machine"], PAIR_TEXT.whose["the other machine"]]) {
  if (!html.includes(required)) failures.push(`the fingerprint block omits ${required}`);
}
for (const sentence of PAIR_TEXT.compare) {
  if (!html.includes(sentence)) {
    failures.push(`an undecided row carries no instruction about comparing before deciding: ${sentence}`);
  }
}
// Once, and above the two lines it is about — the layout the node's own wording
// assumes ("two fingerprints are shown: the requester first…"). Below them it
// reads as a comment on the decision instead of as the instruction for reading.
if (html.indexOf(PAIR_TEXT.compare[0]) > html.indexOf(THEIR_FP)) {
  failures.push("the fingerprint notice is printed below the fingerprints it describes");
}
const firstRow = el("pair-requests").children[0].serialize();
for (const sentence of PAIR_TEXT.compare) {
  if (firstRow.split(sentence).length - 1 !== 1) {
    failures.push(`the fingerprint notice is printed more than once on one row: ${sentence}`);
  }
}
// The step sentence sits UNDER the fingerprint block, so it cannot tell the
// owner to compare the two groups 下面. It said exactly that, and a sentence
// pointing the wrong way sends people looking for a pair that is not there.
const compareLine = PAIR_TEXT.step["pending-incoming"];
if (compareLine.includes("比對") && firstRow.indexOf(compareLine) > firstRow.indexOf(THEIR_FP)
  && !compareLine.includes("上面")) {
  failures.push(`the instruction under the fingerprints points 下面 at them: ${compareLine}`);
}
for (const sentence of [PAIR_TEXT.step["pending-incoming"], PAIR_TEXT.step["awaiting-confirm"]]) {
  if (sentence.includes("比對下面")) {
    failures.push(`a step rendered below the fingerprints says 比對下面: ${sentence}`);
  }
}
// And the node's `ah pair approve <id>` sentence is NOT on it: correct advice
// for the terminal it was written for, and directly above an approve button it
// is an instruction that contradicts the screen.
if (html.includes("ah pair approve pair_incoming000001")) {
  failures.push("an undecided row tells the owner to run a CLI command next to the button that does it");
}

/* ---------------- 3. the three states stay apart ------------------------- */

if (!html.includes(PAIR_TEXT.state["pending-incoming"]) || !html.includes(PAIR_TEXT.state["awaiting-confirm"])) {
  failures.push("the incoming and the awaiting-confirm rows are not labelled differently");
}
if (!html.includes(PAIR_TEXT.approve)) failures.push("an incoming pending row offers no 核准 button");
if (!html.includes(PAIR_TEXT.confirm)) failures.push("an awaiting-confirm row offers no 確認 button");
if (!html.includes(PAIR_TEXT.reject)) failures.push("an undecided row offers no 拒絕 button");

// An outgoing request nobody has answered yet must NOT offer a confirm: there
// is nothing to confirm, and a button there is a button that invites somebody
// to decide before the other owner has.
pending = [outgoingPending];
await scope.loadPairRequests();
const outgoingOnly = rowsHTML();
// The buttons, not the text: the instruction on that row names 確認 on purpose,
// because a requester told only "they will approve" stalls without knowing a
// confirm is still coming back here.
const outgoingButtons = (el("pair-requests").children[0]?.serialize() ?? "").match(/<button[^>]*>([^<]*)</g) ?? [];
if (outgoingButtons.some((b) => b.includes(PAIR_TEXT.confirm))) {
  failures.push("an outgoing request still waiting for the other owner offered a 確認 button");
}
if (!outgoingOnly.includes(PAIR_TEXT.state["pending-outgoing"])) {
  failures.push("an outgoing pending row is not labelled as waiting for the other machine");
}
// ...and it does say that a confirm is coming back here.
if (!outgoingOnly.includes("確認")) {
  failures.push("an outgoing pending row does not say the pairing still needs a confirm on this machine");
}

// The decided rows are out of the default list and in the all list. A row
// needing a decision under four finished ones is how an owner misses their own
// pairing.
pending = [incoming, outgoing];
await scope.loadPairRequests();
if (rowsHTML().includes("pair_expired000001")) {
  failures.push("a finished request is in the default list");
}
el("pair-requests-all").checked = true;
await el("pair-requests-all").onchange();
await settle();
if (requestCalls.at(-1) !== true) {
  failures.push(`the 顯示已結束 toggle asked for all=${requestCalls.at(-1)}`);
}
const withDecided = rowsHTML();
if (!withDecided.includes("pair_expired000001")) {
  failures.push("the 顯示已結束 toggle did not bring the finished rows in");
}
if (!withDecided.includes(PAIR_TEXT.step.expired)) {
  failures.push("a finished row does not say what became of it");
}
// On a finished row the node's own next step is kept: a refusal it could not
// deliver names the machine that may still be trusting this one, and there is
// nowhere else that says so.
if (!withDecided.includes("Nothing was trusted; start again")) {
  failures.push("a finished row dropped the node's own account of what became of it");
}
// And a finished row is not still asking to be decided.
const expiredRow = withDecided.slice(withDecided.indexOf("pair_expired000001"));
if ((expiredRow.match(/<button/g) ?? []).length > 0) {
  failures.push("a finished request still offers a decision");
}
el("pair-requests-all").checked = false;
await el("pair-requests-all").onchange();
await settle();

// A refusal on the fingerprints is not an ordinary 已拒絕: it is the one
// outcome that says something about the network rather than about a decision,
// and the node carries the reason for exactly that purpose.
const mismatched = {
  ...outgoing, id: "pair_mismatch00001", state: "rejected", reason: "fingerprint_mismatch",
  displayName: "lab-box", nextStep: "lab-box rejected it: the fingerprints did not match.",
};
const mismatchBox = document.createElement("div");
mismatchBox.replaceChildren(scope.pairRequestRow(mismatched));
const mismatchHTML = mismatchBox.serialize();
if (!mismatchHTML.includes(PAIR_TEXT.step["rejected-fingerprint-mismatch"])) {
  failures.push("a rejection the node blamed on the fingerprints was shown as an ordinary refusal");
}
if (mismatchHTML.includes(PAIR_TEXT.step.rejected)) {
  failures.push("a fingerprint mismatch was also given the wording for a plain 拒絕");
}

/* ---------------- 4. the buttons reach the bindings ---------------------- */

function find(node, className, found = []) {
  if (!node || typeof node !== "object") return found;
  if ((node.className ?? "").split(" ").includes(className)) found.push(node);
  for (const child of node.children ?? []) find(child, className, found);
  return found;
}
const buttonsIn = (row) => find(row, "decide").flatMap((box) => box.children ?? []);

const rendered = () => el("pair-requests").children.filter((child) => (child.className ?? "").includes("pairrow"));

// The id comes from the row the button was built on. An id read from a field
// is an id that can name somebody else's request.
let [incomingRow, outgoingRow] = rendered();
const approveButton = buttonsIn(incomingRow).find((b) => b.textContent === PAIR_TEXT.approve);
// The decision sits after the values it is about: a button level with the
// fingerprints can be pressed before they have been read.
const incomingText = incomingRow.serialize();
if (incomingText.indexOf(PAIR_TEXT.approve) < incomingText.indexOf(THEIR_FP)) {
  failures.push("the 核准 button is rendered before the fingerprints it is about");
}
await approveButton.onclick();
await settle();
if (decisions.length !== 1 || decisions[0][0] !== "approve" || decisions[0][1] !== incoming.id) {
  failures.push(`核准 called ${JSON.stringify(decisions)}, want one approve of ${incoming.id}`);
}

[incomingRow, outgoingRow] = rendered();
const confirmButton = buttonsIn(outgoingRow).find((b) => b.textContent === PAIR_TEXT.confirm);
await confirmButton.onclick();
await settle();
if (decisions.at(-1)?.[0] !== "confirm" || decisions.at(-1)?.[1] !== outgoing.id) {
  failures.push(`確認 called ${JSON.stringify(decisions.at(-1))}, want a confirm of ${outgoing.id}`);
}

const rejectButton = buttonsIn(rendered()[0]).find((b) => b.textContent === PAIR_TEXT.reject);
await rejectButton.onclick();
await settle();
if (decisions.at(-1)?.[0] !== "reject") {
  failures.push(`拒絕 called ${JSON.stringify(decisions.at(-1))}`);
}
// And the banner answers in this window's language. The node's own sentence is
// English and names `ah` subcommands; shown alone it is the one reply in the
// whole window nobody here can read, and dropped it takes with it the one thing
// this window cannot work out — whether the other machine could be told.
const rejectBanner = el("banner").textContent;
if (!/[\u4e00-\u9fff]/.test(rejectBanner)) {
  failures.push(`a decision was answered only in the node's English: ${rejectBanner}`);
}
if (!rejectBanner.includes("done: reject")) {
  failures.push(`the node's own account of the decision was dropped: ${rejectBanner}`);
}

// Nothing anywhere decides for the owner. An auto-approve or a "skip the
// comparison" affordance would be the whole exchange defeated, so the strings
// are pinned rather than left to a reviewer noticing one being added.
const panelText = el("pair-requests").serialize() + el("pairing-note").serialize();
for (const word of ["自動核准", "略過比對", "全部核准", "不比對"]) {
  if (panelText.includes(word)) failures.push(`the panel offers ${word}`);
}

/* ---------------- 5. sending a request ---------------------------------- */

el("pair-address").value = "  192.168.50.99:7463  ";
await el("btn-pair-send").onclick();
await settle();
if (started.at(-1) !== "192.168.50.99:7463") {
  failures.push(`the form sent ${JSON.stringify(started.at(-1))}; a typed address is trimmed before it is sent`);
}
if (el("pair-address").value !== "") {
  failures.push("the address field was not cleared after a request went out");
}

// An empty field must not reach the node, and must say what to do instead.
const before = started.length;
el("pair-address").value = "   ";
await el("btn-pair-send").onclick();
await settle();
if (started.length !== before) {
  failures.push("an empty address field was sent to the node");
}
if (!el("banner").textContent.includes("host:port")) {
  failures.push(`an empty field gave no usable message: ${el("banner").textContent}`);
}

// A candidate row can pick who is dialled. It carries the announced address and
// nothing else: everything that decides identity arrives over the connection
// this opens, and is then compared by two people.
const candidate = {
  nodeId: "node_candidate01", displayName: "lab-box", platform: "linux/amd64",
  address: "192.168.50.12:7463", fingerprint: "BBBB CCCC",
  firstSeen: new Date().toISOString(), lastSeen: new Date().toISOString(),
};
const candidateRow = scope.candidateRow(candidate);
const send = buttonsIn(candidateRow).find((b) => b.textContent === PAIR_TEXT.sendFromCandidate);
if (!send) {
  failures.push("a candidate row has no one-click 送出配對請求");
} else {
  await send.onclick();
  await settle();
  if (started.at(-1) !== candidate.address) {
    failures.push(`the candidate button sent ${JSON.stringify(started.at(-1))}, want its own row's address`);
  }
}
// And the manual five-field path is still reachable from the row.
if (!buttonsIn(candidateRow).some((b) => b.textContent === PAIR_TEXT.sendManual)) {
  failures.push("the manual path is no longer reachable from a candidate row");
}

/* ---------------- 6. the node's refusals become sentences ---------------- */

for (const [code, expected] of Object.entries(PAIR_TEXT.errors)) {
  startThrow = `${code}: something the node said in English about ah subcommands`;
  el("pair-address").value = "192.168.1.5:7463";
  await el("btn-pair-send").onclick();
  await settle();
  if (el("banner").textContent !== expected) {
    failures.push(`${code} was shown as ${JSON.stringify(el("banner").textContent)}`);
  }
}
startThrow = "";
// PEER_PAIRING_BUSY is deliberately NOT translated: its body carries the peer's
// own reason and the remedy, and the 429 it relays covers two refusals that are
// undone in different places. A sentence written here would pick one and be
// wrong about the other, so the node's words are surfaced as they are.
startThrow = "PEER_PAIRING_BUSY: 192.168.1.5:7463 will not take another pairing request right now: " +
  "no more than 3 pairing requests from one address may be pending at once. Reject what is waiting " +
  "there or let it expire, then try again";
el("pair-address").value = "192.168.1.5:7463";
await el("btn-pair-send").onclick();
await settle();
for (const fragment of ["no more than 3 pairing requests from one address", "Reject what is waiting"]) {
  if (!el("banner").textContent.includes(fragment)) {
    failures.push(`the peer's own reason for refusing was lost: ${el("banner").textContent}`);
  }
}
if (el("banner").textContent.startsWith("PEER_PAIRING_BUSY")) {
  failures.push("the error code was shown to the owner; it is for a log, not for a person");
}
startThrow = "";
// A code nobody translated keeps the node's own words: a refusal swallowed
// leaves the owner with nothing at all.
startThrow = "SOMETHING_NEW: the node explained itself";
el("pair-address").value = "192.168.1.5:7463";
await el("btn-pair-send").onclick();
await settle();
if (!el("banner").textContent.includes("the node explained itself")) {
  failures.push(`an untranslated refusal was swallowed: ${el("banner").textContent}`);
}
startThrow = "";

/* ---------------- 7. a failed read is not a fact about the peer ---------- */

requestsThrow = "contact node: connection refused";
await scope.loadPairRequests();
const failed = rowsHTML();
if (!failed.includes(PAIR_TEXT.requestsFailed)) {
  failures.push("a failed read of the requests was rendered as an empty list");
}
if (!failed.includes("connection refused")) {
  failures.push("the reason the read failed was dropped");
}
requestsThrow = "";
pending = [];
await scope.loadPairRequests();
if (!rowsHTML().includes(PAIR_TEXT.requestsEmpty)) {
  failures.push("an empty list does not say it is empty");
}

/* ---------------- 8. the address the other machine types ----------------- */

function buttonsUnder(node, found = []) {
  if (!node || typeof node !== "object") return found;
  if (node.tagName === "button") found.push(node);
  for (const child of node.children ?? []) buttonsUnder(child, found);
  return found;
}

// A node that announces nothing still opens a window, and then this address is
// the whole way in. Shown in the key font with a copy button rather than left
// inside the notice's prose, which is how the last hand-carried string lost a
// character.
pairingAnswer = {
  // Not "off": the window is OPEN and collecting requests, it just cannot be
  // found on the network. Rendered as off, the panel said the window was shut
  // while it was open, and sent the owner to reopen what was already there.
  availability: "openNotAnnouncing",
  windowAvailable: true,
  state: {
    open: true, remainingSeconds: 200, displayName: "sheldon-mbp", nameIsChosen: false,
    announcing: { announceableAddresses: 0 },
    notice: "this node is not announcing itself over mDNS, so a window opened here will not put it in " +
      "anyone's candidate list: discovery is off (this node was started without -discover). The other " +
      "machine can still pair by typing this address: `ah pair request 192.168.50.10:7463`",
    peerAddress: "192.168.50.10:7463",
  },
  candidates: [],
};
await scope.loadPairing();
if (el("pair-here").classList.contains("hidden")) {
  failures.push("a node that announces nothing did not show the address the other machine has to type");
}
if (el("pair-local-address").textContent !== "192.168.50.10:7463") {
  failures.push(`the address shown is ${JSON.stringify(el("pair-local-address").textContent)}`);
}
// The window opens there. Greying the button out takes the only way in away
// from the one owner who has nothing else.
if (el("btn-pairing-on").disabled) {
  failures.push("the open button is dead on a node whose only way to pair is the window it opens");
}
await el("copy-pair-address").onclick();
if (copied.at(-1) !== "192.168.50.10:7463") {
  failures.push(`the copy button wrote ${JSON.stringify(copied.at(-1))}`);
}
if (!el("copy-pair-address-status").textContent.includes("已複製")) {
  failures.push("a successful copy was not reported");
}
copyThrows = true;
await el("copy-pair-address").onclick();
if (el("copy-pair-address-status").textContent.includes("已複製")) {
  failures.push("a failed copy was reported as a success");
}
copyThrows = false;

// The candidate region says the same thing "off" does — this node is not
// looking — while the window panel above says the window is open.
if (el("candidate-rows").serialize().includes("讀不到")) {
  failures.push("a node with an open window and no discovery had its empty candidate list blamed on a failed read");
}

// A node that IS announcing still shows the address: mDNS not carrying between
// two segments is exactly as silent as mDNS being off, and the owner whose node
// announces perfectly well is the one left with nothing to say when the other
// machine's list stays empty. What changes is the sentence beside it.
pairingAnswer = {
  availability: "on", windowAvailable: true,
  state: {
    open: true, remainingSeconds: 200, displayName: "sheldon-mbp",
    announcing: { announceableAddresses: 1 }, peerAddress: "192.168.50.10:7463",
  },
  candidates: [],
};
await scope.loadPairing();
if (el("pair-here").classList.contains("hidden")) {
  failures.push("a node that is announcing did not show the address the other machine can still be given");
}
if (el("pair-here-note").textContent !== PAIR_TEXT.hereNoteAnnouncing) {
  failures.push("an announcing node was told the address is its only way in");
}

// 8a. The node every fresh install is: no -allow-lan, a peer listener on
//     loopback, and therefore NO peerAddress key in the answer at all — the node
//     does not offer 127.0.0.1 as somewhere another machine could dial. The
//     panel used to hide the whole block here, so the owner of a default node
//     saw an open window, no address, no reason and nothing to press, and the
//     only reason the tests passed was that they fed an address the node never
//     sends.
pairingAnswer = {
  availability: "on", windowAvailable: true,
  state: {
    open: true, remainingSeconds: 200, displayName: "sheldon-mbp",
    announcing: { announceableAddresses: 0 },
  },
  candidates: [],
};
await scope.loadPairing();
if (el("pair-here").classList.contains("hidden")) {
  failures.push("a node that gave no address hid the block, so nothing on screen says why nobody can connect");
}
const noAddress = el("pair-here").serialize() + el("pair-here-note").serialize();
for (const required of ["允許區網連線", "節點設定"]) {
  if (!noAddress.includes(required)) {
    failures.push(`the no-address explanation omits ${required}, so it names no remedy`);
  }
}
if (!buttonsUnder(el("pair-here-note")).some((b) => b.textContent === PAIR_TEXT.hereFix)) {
  failures.push("a node that gave no address offers no button to the setting that fixes it");
}
if (!el("copy-pair-address").disabled) {
  failures.push("the copy button is live on a node that has no address at all");
}
if (el("pairing-headline").textContent !== PAIR_TEXT.windowOpenUnreachable) {
  failures.push(`a window nobody can reach is headlined ${JSON.stringify(el("pairing-headline").textContent)}`);
}

// 8a-ii. The same node once it answers for itself (#172): the node says the
//        address is no use and why, and an answer from the node beats this
//        window's own guess at the string. Its sentence rides under the remedy,
//        not instead of it.
pairingAnswer = {
  availability: "on", windowAvailable: true,
  state: {
    open: true, remainingSeconds: 200, displayName: "sheldon-mbp",
    announcing: { announceableAddresses: 0 },
    peerAddress: "192.168.50.10:7463",
    peerAddressReachable: false,
    peerAddressProblem: "the peer listener answers on 127.0.0.1 only",
  },
  candidates: [],
};
await scope.loadPairing();
const nodeSaidStuck = el("pair-here").serialize() + el("pair-here-note").serialize();
if (el("pair-local-address").textContent.includes("192.168.50.10")) {
  failures.push("an address the node itself called unreachable was handed over as the one to type");
}
if (!nodeSaidStuck.includes("the peer listener answers on 127.0.0.1 only")) {
  failures.push("the node's own account of what is wrong with its address was dropped");
}
if (!el("copy-pair-address").disabled) {
  failures.push("the copy button is live on an address the node called unreachable");
}

// 8a-iii. And the node's word is taken the other way too: a real LAN address it
//         vouched for is shown as the address, not second-guessed here.
pairingAnswer = {
  availability: "on", windowAvailable: true,
  state: {
    open: true, remainingSeconds: 200, displayName: "sheldon-mbp",
    announcing: { announceableAddresses: 1 },
    peerAddress: "192.168.50.10:7463", peerAddressReachable: true,
  },
  candidates: [],
};
await scope.loadPairing();
if (el("pair-local-address").textContent !== "192.168.50.10:7463") {
  failures.push(`a reachable LAN address is shown as ${JSON.stringify(el("pair-local-address").textContent)}`);
}
if (el("copy-pair-address").disabled) {
  failures.push("the copy button is dead on an address that works");
}
if (el("pairing-headline").textContent !== PAIR_TEXT.windowOpen) {
  failures.push(`an open window with a reachable address is headlined ${JSON.stringify(el("pairing-headline").textContent)}`);
}

// 8a-iv. And a node that vouches for an address it did not send. The two
//        fields are independent on the wire, so `peerAddressReachable: true`
//        can arrive with no string beside it — and read on its own it put a
//        blank line where the address belongs, under a live copy button that
//        copies nothing. There is nothing to hand across, so it is unreachable.
if (scope.pairHereState({ peerAddress: "", peerAddressReachable: true }).reachable) {
  failures.push("an empty address was called reachable because the node said so, leaving nothing to type");
}
pairingAnswer = {
  availability: "on", windowAvailable: true,
  state: {
    open: true, remainingSeconds: 200, displayName: "sheldon-mbp",
    announcing: { announceableAddresses: 0 },
    peerAddress: "", peerAddressReachable: true,
  },
  candidates: [],
};
await scope.loadPairing();
if (el("pair-local-address").textContent.trim() === "") {
  failures.push("a node that vouched for an empty address showed a blank line as the address to type");
}
if (!el("copy-pair-address").disabled) {
  failures.push("the copy button is live on an empty address the node happened to vouch for");
}

/* ---------------- 8b. an address nobody can reach ------------------------ */

// The default node has no -allow-lan and listens on 127.0.0.1:7463, which is
// exactly what the API answers with. It is a true answer to "where does this
// node's peer listener answer" and a useless one to "what does the other
// machine type": handed across, it fails over there as a connection timeout
// with nothing on either screen to say why. So it is not shown as the address,
// and the window is not described as done.
for (const unreachable of ["127.0.0.1:7463", "localhost:7463", "[::1]:7463", "0.0.0.0:7463"]) {
  if (scope.pairAddressReachable(unreachable)) {
    failures.push(`${unreachable} was treated as an address another machine could type`);
  }
}
for (const reachable of ["192.168.50.10:7463", "10.0.0.7:7463", "[fd00::1]:7463"]) {
  if (!scope.pairAddressReachable(reachable)) {
    failures.push(`${reachable} was treated as unreachable, so a working address would be hidden`);
  }
}

pairingAnswer = {
  availability: "on", windowAvailable: true,
  state: {
    open: true, remainingSeconds: 200, displayName: "sheldon-mbp",
    announcing: { announceableAddresses: 0, lastError: "the peer listener is on loopback" },
    peerAddress: "127.0.0.1:7463",
  },
  candidates: [],
};
await scope.loadPairing();
if (el("pair-local-address").textContent.includes("127.0.0.1")) {
  failures.push("a loopback address was handed to the owner as the one the other machine types");
}
const stuck = el("pair-here").serialize() + el("pair-here-note").serialize();
for (const required of ["允許區網連線", "節點設定"]) {
  if (!stuck.includes(required)) {
    failures.push(`the unreachable-address explanation omits ${required}, so it names no remedy`);
  }
}
// The window is open and there is no way in. Saying only 開啟中 reads as done,
// and an owner who reads it as done goes to the other machine and waits.
if (el("pairing-headline").textContent === PAIR_TEXT.windowOpen) {
  failures.push("an open window nobody can reach was described as simply open");
}
if (el("pairing-headline").textContent !== PAIR_TEXT.windowOpenUnreachable) {
  failures.push(`the headline on an unreachable open window is ${JSON.stringify(el("pairing-headline").textContent)}`);
}
// And copying it is off: the whole point is that this string must not travel.
if (!el("copy-pair-address").disabled) {
  failures.push("the copy button is live on an address that cannot work on the other machine");
}

// The remedy is a button, not a sentence about where to click: the fix is two
// tabs away and the owner has just been told their node is unreachable.
const fix = buttonsUnder(el("pair-here-note")).find((b) => b.textContent === PAIR_TEXT.hereFix);
if (!fix) {
  failures.push("no button takes the owner to the setting that makes this node reachable");
} else {
  fix.onclick();
  if (state.view !== "settings" || state.settingsSection !== "settings-node") {
    failures.push(`the fix button left the window on ${state.view}/${state.settingsSection}`);
  }
  if (!el("pairing-modal").classList.contains("hidden")) {
    failures.push("the fix button left the pairing drawer open on top of the settings it opened");
  }
  // The keyboard goes where the eye was sent. Scrolling alone leaves focus on a
  // button inside the drawer this just closed, so the owner arrives at the
  // remedy with the next keystroke landing on something they cannot see.
  if (document.activeElement !== el("node-allow-lan")) {
    failures.push("the fix button scrolled the node settings into view but focused nothing there");
  }
  el("node-allow-lan").blur();
  state.view = "network";
  scope.openPairingDrawer();
  await settle();
}

/* ---------------- 8c. the list behind the drawer keeps up ---------------- */

// The drawer is a modal, so it holds the fifteen-second refresh off for as long
// as it is open. Everything decided inside it reloads that list, and a change
// made outside — `ah revoke` in a terminal — did not, leaving the node list
// naming a node this machine had already stopped trusting.
scope.openPairingDrawer();
await settle();
const beforeTick = overviewCalls;
fastTick?.fn();
await settle();
if (overviewCalls === beforeTick) {
  failures.push("the drawer's own poll does not refresh the trusted-node list it sits over");
}
// ...and not while a decision is in flight, which is what state.busy is for.
state.busy = true;
const whileBusy = overviewCalls;
fastTick?.fn();
await settle();
if (overviewCalls !== whileBusy) {
  failures.push("the drawer polled while a decision was in flight");
}
state.busy = false;

/* ---------------- 8d. the rows survive a tick ---------------------------- */

// The drawer's two-second tick ends in a full render, because the contract asks
// it to refresh the trusted-node list the modal sits over. That render used to
// rebuild every candidate row: the focus on 送出配對請求 was dropped twice a
// second, and a press whose mousedown and mouseup fell on either side of one
// never became a click at all. The contract asks for element identity across a
// tick, so it is measured on the element the owner actually presses.
const candidateRowsOf = () =>
  el("candidate-rows").children.filter((child) => (child.className ?? "").includes("candidaterow"));
const sendButtonOf = (row) => buttonsUnder(row).find((b) => b.textContent === PAIR_TEXT.sendFromCandidate);

const ticking = {
  nodeId: "node_ticking0001", displayName: "lab-box", platform: "linux/amd64",
  address: "192.168.50.12:7463", fingerprint: "BBBB CCCC",
  firstSeen: new Date().toISOString(), lastSeen: new Date().toISOString(),
};
const neighbour = {
  ...ticking, nodeId: "node_ticking0002", displayName: "win-bench",
  address: "192.168.50.13:7463", fingerprint: "DDDD EEEE",
};
const listing = (candidates) => ({
  availability: "on", windowAvailable: true,
  state: {
    open: true, remainingSeconds: 200, displayName: "sheldon-mbp",
    announcing: { announceableAddresses: 1 },
    peerAddress: "192.168.50.10:7463", peerAddressReachable: true,
  },
  candidates,
});

pairingAnswer = listing([ticking, neighbour]);
await scope.loadPairing();
const kept = candidateRowsOf()[0];
const keptSend = kept && sendButtonOf(kept);
if (!kept || !keptSend) {
  failures.push("the candidate list rendered no row with a 送出配對請求 button to measure");
} else {
  // The tick with the very same candidates: nothing here changed, so nothing
  // here may be written. Identity alone is not enough to measure that —
  // replaceChildren hands the same elements back, and a browser still detaches
  // and re-attaches every one of them, which is what costs the focus. So the
  // write itself is counted.
  const rowsBox = el("candidate-rows");
  const realReplace = rowsBox.replaceChildren;
  let writes = 0;
  rowsBox.replaceChildren = function spy(...kids) {
    writes += 1;
    return realReplace.apply(this, kids);
  };
  fastTick?.fn();
  await settle();
  scope.tickCountdown();
  await scope.loadPairing();
  if (writes !== 0) {
    failures.push(`a tick over unchanged candidates rewrote the list ${writes} times, detaching every row`);
  }
  delete rowsBox.replaceChildren;
  const afterTick = candidateRowsOf()[0];
  if (afterTick !== kept) {
    failures.push("the drawer's tick rebuilt the candidate row the owner is reaching for");
  }
  if (sendButtonOf(afterTick ?? {}) !== keptSend) {
    failures.push("the tick replaced 送出配對請求, so a press that spans a tick is swallowed");
  }

  // A row whose announcement DID change still updates — in place, on the row
  // that was already there, because it is still the same machine.
  pairingAnswer = listing([
    { ...ticking, displayName: "lab-box-renamed", address: "192.168.50.99:7463" },
    neighbour,
  ]);
  await scope.loadPairing();
  const changed = candidateRowsOf()[0];
  if (changed !== kept) {
    failures.push("a candidate that changed was given a new row instead of its own being written");
  }
  const changedHTML = (changed ?? kept).serialize();
  if (!changedHTML.includes("lab-box-renamed") || !changedHTML.includes("192.168.50.99:7463")) {
    failures.push(`a changed candidate still shows its old announcement: ${changedHTML}`);
  }
  // ...and the button on it now dials the new address rather than the old one.
  await sendButtonOf(changed ?? kept)?.onclick();
  await settle();
  if (started.at(-1) !== "192.168.50.99:7463") {
    failures.push(`the kept row's button still dials ${JSON.stringify(started.at(-1))}`);
  }

  // A machine that stopped announcing takes its row with it.
  pairingAnswer = listing([neighbour]);
  await scope.loadPairing();
  if (el("candidate-rows").serialize().includes("lab-box-renamed")) {
    failures.push("a candidate that stopped announcing kept its row");
  }
  if (candidateRowsOf().length !== 1) {
    failures.push(`the list kept ${candidateRowsOf().length} rows for one candidate`);
  }
}

/* ---------------- 8e. the summary line in the node list's foot ----------- */

// The one line an owner reads without opening the drawer. It used to tell every
// openNotAnnouncing node to hand over "the address shown there" — advice about
// an address a default node does not have, sending its owner to read out
// something that was never on the screen. So the two cases say different things.
pairingAnswer = {
  availability: "openNotAnnouncing", windowAvailable: true,
  state: {
    open: true, remainingSeconds: 200, displayName: "sheldon-mbp",
    announcing: { announceableAddresses: 0 },
    peerAddress: "192.168.50.10:7463",
  },
  candidates: [],
};
await scope.loadPairing();
const withAddress = el("pairing-summary-line").textContent;
if (!withAddress.includes("本機位址")) {
  failures.push(`a node that has an address to hand over is not told to hand it over: ${withAddress}`);
}
pairingAnswer = {
  availability: "openNotAnnouncing", windowAvailable: true,
  state: {
    open: true, remainingSeconds: 200, displayName: "sheldon-mbp",
    announcing: { announceableAddresses: 0 },
  },
  candidates: [],
};
await scope.loadPairing();
const withoutAddress = el("pairing-summary-line").textContent;
if (withoutAddress === withAddress) {
  failures.push("a node with no address to give was told to read out the address shown in the panel");
}
if (withoutAddress.includes("本機位址")) {
  failures.push(`a node with no address is still sent to read one out: ${withoutAddress}`);
}
if (!withoutAddress.includes("打開配對面板")) {
  failures.push(`the no-address summary names nowhere to go next: ${withoutAddress}`);
}

/* ---------------- 9. every string from the wire is text ------------------ */

const hostile = {
  id: '<img src=x onerror="alert(1)">', direction: "incoming",
  nodeId: 'node_<script>steal()</script>', displayName: '<script>alert("name")</script>',
  platform: '"><iframe onload="x()"></iframe>', address: '</div><iframe src=javascript:alert(1)>',
  state: "pending", reason: '<b>r</b>',
  fingerprints: [
    fp("requester", '<script>alert("machine")</script>', "the other machine", '<img src=x onerror=1>'),
    fp('<svg onload=1>', "sheldon-mbp", '<b>whose</b>', LOCAL_FP),
  ],
  nextStep: '<script>alert("next")</script>',
};
const hostileBox = document.createElement("div");
hostileBox.replaceChildren(scope.pairRequestRow(hostile));
const hostileHTML = hostileBox.serialize();
for (const marker of ["<script", "<img", "<iframe", "<svg"]) {
  if (hostileHTML.toLowerCase().includes(marker)) {
    failures.push(`the request row produced ${marker} from a peer-chosen string`);
  }
}
// And no attribute came out of one. `javascript:` survives escaping as harmless
// text, so what is checked is the attribute rather than the word — with the
// trailing quote, because the serializer escapes every `"` inside text to
// `&quot;` and a literal one can therefore only come from an attribute it
// really emitted.
for (const attribute of ['href="', 'src="', 'onerror="', 'onload="', 'onclick="']) {
  if (hostileHTML.includes(attribute)) {
    failures.push(`the request row built a ${attribute} attribute, which peer data could fill`);
  }
}
if (!hostileHTML.includes("&lt;script&gt;alert(&quot;name&quot;)&lt;/script&gt;")) {
  failures.push("a hostile display name was not rendered as escaped text");
}
// Nothing a peer chose may decide a class name — including the role and whose
// labels, which are mapped through a fixed table and otherwise shown as text.
for (const cls of hostileHTML.match(/class="[^"]*"/g) ?? []) {
  if (!/^class="(pairrow waiting|pairrow|line|name|meta|fingerprint|fingerprints|who|mine|muted|nextstep|stale|decide|primary|ghost|pill idle|pill|empty)"$/.test(cls)) {
    failures.push(`a peer-supplied value reached a class name: ${cls}`);
  }
}

// A node that answered without the ordered pair must not have one invented for
// it: two values in an order this window guessed at are exactly what the
// comparison cannot survive.
const noPair = document.createElement("div");
noPair.replaceChildren(scope.pairRequestRow({ ...incoming, fingerprints: [] }));
const noPairHTML = noPair.serialize();
if (noPairHTML.includes(THEIR_FP)) {
  failures.push("a row with no ordered fingerprint pair rendered the bare wire fingerprint as the value to compare");
}
if (!noPairHTML.includes("ah pair pending")) {
  failures.push("a row with no ordered pair does not say where the comparison can still be made");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("the pairing exchange shows both fingerprints in the node's order, decides below them, and reaches its bindings");
