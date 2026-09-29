// The background-service panel, and the one thing it owes an owner on a
// platform that has no background service (#65).
//
// Windows: the node runs — the installer starts it and leaves a Startup
// shortcut — but nothing this app can ask holds it, so `ah service` has
// nothing to restart. A setting the node only reads at start-up was therefore
// unappliable from the window, and the panel's advice amounted to Task
// Manager. The app restarts the node itself instead, and this asserts the
// button that does it is offered exactly where it is safe to.
//
//   node frontend/test/service-panel.mjs

import { document } from "./dom-shim.mjs";
import { answerConfirms } from "./fixtures/confirm-dialog.mjs";
import { TEXT as EN } from "../src/i18n/en.js";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";

globalThis.document = document;
globalThis.setInterval = () => 0;
const { configure, boot } = await import("../src/app.js");

const failures = [];
const el = (id) => document.getElementById(id);
const noop = async () => ({});
let restartCalls = 0;
// Set to an error to make the next RestartNode refuse, the way the Go side
// does when it cannot start the node on the database it is running on.
let restartRefusal = null;
const questions = [];

configure({
  Overview: async () => ({ reachable: true, node: {}, sessions: [], nodes: [], peers: [], counts: {} }),
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, Outbound: noop, Wakes: noop,
  ServiceStatus: async () => ({ supported: false, installed: false, running: false, pid: 0, unitPath: "", logHint: "" }),
  InstallService: noop, UninstallService: noop, NodeSettings: noop, SaveNodeSettings: noop,
  LocalAddresses: async () => [],
  RestartNode: async () => {
    restartCalls += 1;
    if (restartRefusal) throw restartRefusal;
    return { command: "agenthub-node（停止後重新啟動）", output: "節點在 http://127.0.0.1:7462 回應了" };
  },
});
const app = boot({ start: false });
answerConfirms(document, (question) => {
  questions.push(question);
  return false;
});
const hidden = (id) => el(id).classList.contains("hidden");

// 1. No service manager, node running: the panel says so and offers the
//    restart, because that is the only way a saved setting takes effect here.
app.state.service = { supported: false, installed: false, running: false, pid: 0, unitPath: "", logHint: "" };
app.state.nodeReachable = true;
app.renderService();
if (hidden("service-restart")) {
  failures.push("no way to restart the node on a platform with no background service");
}
if (el("service-restart").textContent !== "重新啟動節點") {
  failures.push(`the button on a running node reads "${el("service-restart").textContent}"`);
}
if (!hidden("service-open") || !hidden("service-uninstall")) {
  failures.push("a platform with no service manager was offered an install or an uninstall");
}

// 2. Same platform, node not running: the same button, named for what it does.
app.state.nodeReachable = false;
app.renderService();
if (hidden("service-restart") || el("service-restart").textContent !== "啟動節點") {
  failures.push(`a stopped node was not offered a start: hidden=${hidden("service-restart")} text=${el("service-restart").textContent}`);
}

// 3. Without ah this window cannot find out whether a service holds the node.
//    Restarting the process behind a launchd job or a systemd unit is how one
//    node becomes two, so nothing is offered.
app.state.service = { toolError: "找不到 ah", supported: false, installed: false, running: false };
app.state.nodeReachable = true;
app.renderService();
if (!hidden("service-restart")) {
  failures.push("a node whose service status is unknown was offered a process restart");
}

// 4. A registered service still restarts through ah — the Go side routes it —
//    so the button is offered there too.
app.state.service = { supported: true, installed: true, running: true, pid: 7, unitPath: "/u", logHint: "/l" };
app.renderService();
if (hidden("service-restart")) failures.push("a registered service was not offered a restart");

// 5. Pressing it reports what came back, verbatim, rather than a claim of
//    its own.
await app.restartNode();
if (restartCalls !== 1) failures.push(`RestartNode called ${restartCalls} times, want 1`);
if (!el("service-output").textContent.includes("回應了")) {
  failures.push(`the restart's own words did not reach the screen: ${el("service-output").textContent}`);
}
if (hidden("service-output")) failures.push("the output stayed hidden");

// 6. A node running but not as a service was perhaps started by hand with
//    --db (#205). The Go side reads how it was started and brings it back on
//    the same database, or refuses before stopping anything — so there is
//    nothing for the window to ask, on a node the app started itself or one
//    started by hand. What it owes the owner is the refusal, verbatim, and no
//    claim that anything restarted.
app.state.service = { supported: true, installed: false, running: false, pid: 0, unitPath: "", logHint: "" };
app.state.nodeReachable = true;
app.renderService();
if (app.serviceQuickAction() !== "install" || !app.state.nodeReachable) failures.push("the check below is not about a node running but not as a service");
if (hidden("service-restart")) failures.push("a node running but not as a service was not offered a restart");
restartCalls = 0;
questions.length = 0;
// What the button's onclick runs (wired by boot's start, which this skips).
const pressRestart = () => app.restartNode({ button: el("service-restart") });
await pressRestart();
if (restartCalls !== 1 || questions.length !== 0) {
  failures.push(`a restart the Go side vouches for was asked about (${questions.length} questions, ${restartCalls} restarts)`);
}
restartRefusal = new Error("the running node (pid 4242) cannot be started again as it is: it was started with -db data/x.db, "
  + "a relative path, so this app will not restart it: stop and start the node the way it was started");
el("service-output").textContent = "";
el("banner").textContent = "";
await pressRestart().catch(() => {});
if (!el("service-output").textContent.includes("will not restart it") || hidden("service-output")) {
  failures.push(`a refused restart did not put the refusal on screen: ${el("service-output").textContent}`);
}
if (el("banner").textContent.includes(ZH["service.restarted"])) failures.push("a refused restart was reported as a restart");
if (questions.length !== 0) failures.push("a refused restart asked the owner something first");
restartRefusal = null;

// 7. The form's note under the same state: the default database is a new
//    identity only for a node that was started with --db, and the sentence
//    has to say it that way in both languages — one on its default, like the
//    one the app starts, keeps its identity with the field left blank.
// The restarts above reloaded the status from this file's ServiceStatus, which
// answers for the no-service-manager platform; this is the other one.
app.state.service = { supported: true, installed: false, running: false, pid: 0, unitPath: "", logHint: "" };
app.state.nodeReachable = true;
await app.openServiceForm();
if (el("service-db-note").textContent !== ZH["service.dbNoteRunningNotService"]) {
  failures.push(`the note for a node running but not as a service reads ${el("service-db-note").textContent}`);
}
for (const [language, text, condition, consequence, breaks] of [
  ["en", EN["service.dbNoteRunningNotService"], "If it was started with --db", "new node identity", /[.;] /],
  ["zh", ZH["service.dbNoteRunningNotService"], "若是用 --db 啟動的", "新的節點身分", /[。；]/],
]) {
  const from = text.indexOf(condition);
  const to = text.indexOf(consequence);
  if (from < 0 || to < from || breaks.test(text.slice(from, to))) {
    failures.push(`${language}: "${consequence}" is not said inside "${condition}": ${text}`);
  }
}

if (failures.length > 0) {
  for (const failure of failures) console.error(failure);
  process.exit(1);
}
console.log("service panel: the restart is offered where it is safe, and says what it did");
