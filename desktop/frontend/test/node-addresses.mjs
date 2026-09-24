// A paired machine's addresses, edited as the list they are (ADR-005 §4).
//
// The node detail page used to record one address through
// PUT /v1/nodes/{id}/address, and that endpoint keeps the address it replaces
// as a backup. An owner who corrected a mistyped address was left with the
// typo behind the correction, tried on every delivery, and nothing on this
// page could remove it. The page now edits the preferred address and every
// backup, and saves the whole list through SetNodeAddresses: what is removed
// here is what the node forgets.
//
// On a node too old for lists the binding records the first address alone and
// says so, and the page has to say it too rather than report a list as saved.
//
//   node frontend/test/node-addresses.mjs

import { document } from "./dom-shim.mjs";
import { answerConfirms } from "./fixtures/confirm-dialog.mjs";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";

globalThis.document = document;
globalThis.setInterval = () => 0;
answerConfirms(document, () => true);

const failures = [];
const el = (id) => document.getElementById(id);
const settle = () => new Promise((resolve) => setTimeout(resolve, 30));
const fill = (text, values) => text.replace(/\{(\w+)\}/g, (_, key) => values[key]);
const find = (node, className, found = []) => {
  if (!node || typeof node !== "object") return found;
  if ((node.className ?? "").split(" ").includes(className)) found.push(node);
  for (const child of node.children ?? []) find(child, className, found);
  return found;
};
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

const PREFERRED = "192.168.1.20:7463";
const TYPO = "192.168.1.200:7463";
const BACKUP = "10.0.0.2:7463";

const peer = (extra = {}) => ({
  nodeId: "node_peer0000000000",
  displayName: "ubuntu-lab",
  platform: "linux/amd64",
  publicKey: "xxxx",
  fingerprint: "2DCF 9604 DBA9 778A 6DDD 035B",
  pairedAt: new Date().toISOString(),
  ...extra,
});

const calls = [];
let answer = async () => ({ olderNode: false });
const SetNodeAddresses = async (nodeId, addresses) => {
  calls.push([nodeId, addresses]);
  return answer();
};
const singleCalls = [];
const SetNodeAddress = async (...args) => { singleCalls.push(args); };

const noop = async () => ({});
const { configure, boot } = await import("../src/app.js");
configure({
  Overview: async () => ({
    reachable: true, node: { id: "node_local000000000" }, sessions: [], counts: {},
    nodes: [peer({ address: PREFERRED, alternateAddresses: [TYPO, BACKUP] })],
    peers: [{ nodeId: "node_peer0000000000", displayName: "ubuntu-lab", online: false, sessions: [] }],
  }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop,
  SetNodeAddress, SetNodeAddresses,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, ServiceStatus: noop,
  InstallService: noop, UninstallService: noop, LocalAddresses: noop,
});
const scope = boot();
const { state, nodeDetail } = scope;
await settle();

const detailOf = (node) => {
  const container = document.createElement("div");
  container.replaceChildren(...nodeDetail(node));
  return container;
};
const fields = (container) => find(container, "addressinput");
const values = (container) => fields(container).map((field) => field.value);
const labels = (container) => find(container, "addresslabel").map((label) => label.textContent);

/* ---------- 1. every address is on the page, preferred first ---------- */

let page = detailOf(peer({ address: PREFERRED, alternateAddresses: [TYPO, BACKUP] }));
if (!same(values(page), [PREFERRED, TYPO, BACKUP])) {
  failures.push(`the fields hold ${JSON.stringify(values(page))}; want the preferred address, then each backup`);
}
if (!same(labels(page), [ZH["network.addressPreferred"], fill(ZH["network.addressBackup"], { n: 1 }),
  fill(ZH["network.addressBackup"], { n: 2 })])) {
  failures.push(`the rows are labelled ${JSON.stringify(labels(page))}`);
}
if (find(page, "removeaddress").length !== 3) {
  failures.push(`${find(page, "removeaddress").length} rows can be removed, want all three`);
}

/* ---------- 2. removing the typo sends the list without it ---------- */

find(page, "removeaddress")[1].onclick();
if (!same(values(page), [PREFERRED, BACKUP])) {
  failures.push(`after removing the second row the fields hold ${JSON.stringify(values(page))}`);
}
await find(page, "setaddress")[0].onclick();
await settle();
if (calls.length !== 1 || !same(calls[0], ["node_peer0000000000", [PREFERRED, BACKUP]])) {
  failures.push(`removing the typo sent ${JSON.stringify(calls)}; want the whole list without it`);
}
if (singleCalls.length !== 0) {
  failures.push("the page recorded through the one-address binding, which would keep the typo as a backup");
}
if (!el("banner").textContent.includes(`${PREFERRED}, ${BACKUP}`) || !el("banner").className.includes("ok")) {
  failures.push(`the saved list is not what the banner reports: ${el("banner").textContent}`);
}
if (state.addressDraft !== null) failures.push("a saved list left its draft behind");

/* ---------- 3. the preferred address can be removed and corrected too ---------- */

calls.length = 0;
page = detailOf(peer({ address: TYPO, alternateAddresses: [BACKUP] }));
find(page, "removeaddress")[0].onclick();
if (!same(values(page), [BACKUP]) || labels(page)[0] !== ZH["network.addressPreferred"]) {
  failures.push(`removing the preferred row left ${JSON.stringify(values(page))} labelled ${JSON.stringify(labels(page))}; ` +
    "the first backup should move up to preferred");
}
// One row left: removing it would leave nothing to send.
if (find(page, "removeaddress").length !== 0) {
  failures.push("the last row can be removed, which leaves a list the button refuses");
}
fields(page)[0].value = `  ${PREFERRED}  `;
await find(page, "setaddress")[0].onclick();
await settle();
if (!same(calls[0], ["node_peer0000000000", [PREFERRED]])) {
  failures.push(`correcting the preferred address sent ${JSON.stringify(calls[0])}`);
}

/* ---------- 4. rows are added up to four, and typing survives a reshape ---------- */

page = detailOf(peer({ address: PREFERRED }));
if (values(page).length !== 1 || find(page, "removeaddress").length !== 0) {
  failures.push(`a machine with one address shows ${values(page).length} rows`);
}
const add = find(page, "addaddress")[0];
add.onclick();
fields(page)[1].value = BACKUP; // typed, with no input event in between
add.onclick();
add.onclick();
if (values(page).length !== 4 || values(page)[1] !== BACKUP) {
  failures.push(`after three adds the fields hold ${JSON.stringify(values(page))}; want four, the typed one kept`);
}
if (!add.classList.contains("hidden")) {
  failures.push("a fifth row can be added; the node refuses more than four addresses");
}
// Empty rows are rows not filled in, not addresses.
calls.length = 0;
await find(page, "setaddress")[0].onclick();
await settle();
if (!same(calls[0], ["node_peer0000000000", [PREFERRED, BACKUP]])) {
  failures.push(`empty rows reached the node: ${JSON.stringify(calls[0])}`);
}

/* ---------- 5. a draft of several rows survives a re-render ---------- */

page = detailOf(peer({ address: PREFERRED, alternateAddresses: [TYPO] }));
const typing = fields(page)[1];
typing.value = "10.0.0.";
typing.oninput({ target: typing });
const redrawn = detailOf(peer({ address: PREFERRED, alternateAddresses: [TYPO] }));
if (!same(values(redrawn), [PREFERRED, "10.0.0."])) {
  failures.push(`a re-render replaced a half-typed backup: ${JSON.stringify(values(redrawn))}`);
}
if (!same(values(detailOf(peer({ nodeId: "node_other000000000", address: BACKUP }))), [BACKUP])) {
  failures.push("a draft typed on one machine leaked into another machine's fields");
}
state.addressDraft = null;

/* ---------- 6. nothing filled in is not sent ---------- */

calls.length = 0;
page = detailOf(peer({ address: PREFERRED }));
fields(page)[0].value = "   ";
await find(page, "setaddress")[0].onclick();
await settle();
if (calls.length !== 0) failures.push(`an empty list was sent: ${JSON.stringify(calls)}`);
if (el("banner").textContent !== ZH["network.addressEmpty"]) {
  failures.push(`an empty list gave ${JSON.stringify(el("banner").textContent)}`);
}

/* ---------- 7. an older node records the first address and the page says so ---------- */

calls.length = 0;
answer = async () => ({ olderNode: true });
page = detailOf(peer({ address: TYPO }));
fields(page)[0].value = PREFERRED;
find(page, "addaddress")[0].onclick();
fields(page)[1].value = BACKUP;
await find(page, "setaddress")[0].onclick();
await settle();
if (!same(calls[0], ["node_peer0000000000", [PREFERRED, BACKUP]])) {
  failures.push(`the page sent ${JSON.stringify(calls[0])} to an older node; the binding decides the fallback`);
}
const olderText = fill(ZH["network.addressSavedOlderNode"], { name: "ubuntu-lab", address: PREFERRED });
if (el("banner").textContent !== olderText) {
  failures.push(`an older node's fallback was reported as ${JSON.stringify(el("banner").textContent)}`);
}
if (el("banner").className.includes("ok")) {
  failures.push("an older node's fallback was shown as a plain success, which promises the backups were kept");
}
answer = async () => ({ olderNode: false });

/* ---------- 8. peer-chosen text stays text ---------- */

const hostile = detailOf(peer({ address: PREFERRED, alternateAddresses: ['<img src=x onerror="alert(1)">'] }))
  .serialize().toLowerCase();
if (hostile.includes("<img")) failures.push("a hostile backup address was rendered as markup");

if (failures.length > 0) {
  console.error("node addresses:\n  " + failures.join("\n  "));
  process.exit(1);
}
console.log("node addresses: every address is edited and removed as one list, and an older node's fallback is said");
