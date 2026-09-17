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

const Overview = async () => ({
  node: { id: "node_local00000", displayName: "sheldon-mbp", platform: "darwin/arm64", fingerprint: LOCAL_FP },
  sessions: [], nodes: [], peers: [], counts: { total: 0 },
  nodeUrl: "http://127.0.0.1:7462", reachable: true,
});
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
if (!html.includes(PAIR_TEXT.compare)) {
  failures.push("an undecided row carries no instruction about comparing before deciding");
}
// The node's own next step survives: after a refusal it is the only place that
// says the other machine could not be told.
if (!html.includes("ah pair approve pair_incoming000001")) {
  failures.push("the node's own next step was dropped from the row");
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
if (outgoingOnly.includes(PAIR_TEXT.confirm)) {
  failures.push("an outgoing request still waiting for the other owner offered a 確認 button");
}
if (!outgoingOnly.includes(PAIR_TEXT.state["pending-outgoing"])) {
  failures.push("an outgoing pending row is not labelled as waiting for the other machine");
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
// And a finished row is not still asking to be decided.
const expiredRow = withDecided.slice(withDecided.indexOf("pair_expired000001"));
if (expiredRow.includes(PAIR_TEXT.approve) || expiredRow.includes(PAIR_TEXT.confirm)) {
  failures.push("a finished request still offers a decision");
}
el("pair-requests-all").checked = false;
await el("pair-requests-all").onchange();
await settle();

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

// A node that announces nothing still opens a window, and then this address is
// the whole way in. Shown in the key font with a copy button rather than left
// inside the notice's prose, which is how the last hand-carried string lost a
// character.
pairingAnswer = {
  availability: "off",
  windowAvailable: true,
  state: {
    open: true, remainingSeconds: 200, displayName: "sheldon-mbp", nameIsChosen: false,
    announcing: { announceableAddresses: 0 },
    notice: "this node is not announcing itself over mDNS, so a window opened here will not put it in " +
      "anyone's candidate list: discovery is off (this node was started without -discover). The other " +
      "machine can still pair by typing this address: `ah pair request 192.168.50.10:7463`",
    pairAddress: "192.168.50.10:7463",
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

// A node that IS announcing has no notice, and the block stays away: a panel
// that always shows it is one nobody reads.
pairingAnswer = {
  availability: "on", windowAvailable: true,
  state: { open: true, remainingSeconds: 200, displayName: "sheldon-mbp", announcing: { announceableAddresses: 1 } },
  candidates: [],
};
await scope.loadPairing();
if (!el("pair-here").classList.contains("hidden")) {
  failures.push("the typed-address block is shown on a node that is announcing");
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
