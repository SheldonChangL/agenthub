// The first-run wizard: what a fresh install opens on (docs/ui-contract.md §3.2).
//
// The wizard presses nothing of its own. Every write it makes is one another
// part of the window already makes — the service install and restart the
// title bar's pill makes, the save-and-restart the pairing drawer's repair
// makes, the publish the inline audience menu makes — so what is checked here
// is the joins: that each step is worked out from state rather than stored,
// that the one button runs what is left in order and stops at the first thing
// that fails, that no identity guard is walked around on the way, that a
// decision about privacy is the owner's, and that nothing else on screen
// offers a second button for what the wizard is in the middle of.
//
//   node frontend/test/first-run.mjs

import { document } from "./dom-shim.mjs";
import { attentionRows, latestToast, toastButtons } from "./fixtures/toasts.mjs";
import { inEnglish } from "./fixtures/in-english.mjs";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";

globalThis.document = document;
globalThis.setInterval = () => 0;
// Waits the window makes for the node (waitForNode, 750 ms) run at once; the
// toasts' own timers (six seconds and up) never fire, so a toast and its 復原
// stay on screen to be read.
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
const PREFS = "agenthub.desktop.ui.v1";

const failures = [];
const el = (id) => document.getElementById(id);
const flush = async () => {
  for (let turn = 0; turn < 6; turn++) await new Promise((resolve) => setImmediate(resolve));
};

/* ---------------- a machine the bindings describe ---------------- */

const DIAL = "Get \"http://127.0.0.1:7462/v1/node\": dial tcp 127.0.0.1:7462: connect: connection refused";
const LOOPBACK = "127.0.0.1:7463";
const ONE = [{ interface: "en0", address: "192.168.50.10", subnet: "192.168.50.0/24", private: true }];
const TWO = [
  { interface: "en0", address: "192.168.50.10", subnet: "192.168.50.0/24", private: true },
  { interface: "en7", address: "10.0.0.5", subnet: "10.0.0.0/24", private: true },
];
const session = (id, provider, extra = {}) => ({
  id: `${provider}:${id}`, provider, status: "active", cwd: `/home/demo/${id}`, title: "",
  lastSeenAt: new Date(Date.now() - 60000).toISOString(),
  audience: { mode: "none", nodes: [], exportCwd: false, acceptMessages: false, allowOutbound: false, autoWake: false },
  ...extra,
});

const blank = () => ({
  nodeUp: false,
  supported: true,
  installed: false,
  running: false,
  toolError: "",
  sessions: [],
  nodes: [],
  addresses: ONE,
  saved: { peerListen: LOOPBACK, peerListens: [LOOPBACK], allowLan: false, discover: true, treatAsPrivate: [], autoWake: false },
  installError: "",
});
let machine = blank();
// Every write binding, in the order the window called it.
const writes = [];
const audienceCalls = [];

const counts = () => {
  const published = machine.sessions.filter((s) => s.audience.mode !== "none");
  return {
    total: machine.sessions.length,
    all_paired: published.filter((s) => s.audience.mode === "all_paired").length,
    selected: published.filter((s) => s.audience.mode === "selected").length,
    none: machine.sessions.length - published.length,
  };
};
const nodeView = () => ({
  settings: JSON.parse(JSON.stringify(machine.saved)),
  saved: JSON.parse(JSON.stringify(machine.saved)),
  sources: {},
  restartRequired: false,
  peerListeners: machine.saved.peerListens.map((address) => ({ address, state: "bound" })),
});
const lanAddress = () => machine.saved.peerListens.find((address) => !address.startsWith("127."));

const serviceStatus = () => (machine.toolError
    ? { toolError: machine.toolError, supported: false, installed: false, running: false }
    : {
      tool: "/usr/local/bin/ah", supported: machine.supported, installed: machine.installed,
      running: machine.installed && machine.running, pid: machine.installed && machine.running ? 41 : 0,
      nodeAnswering: machine.nodeUp, logHint: "~/agenthub.log", unitPath: "~/unit",
      dbPath: machine.installed ? "~/agenthub.db" : "", dbPathKnown: machine.installed, pinnedSettings: [],
    });

const bindings = {
  Overview: async () => (machine.nodeUp
    ? {
      reachable: true, nodeUrl: "http://127.0.0.1:7462",
      node: { id: "node_local", displayName: "demo-mac", platform: "darwin/arm64" },
      sessions: JSON.parse(JSON.stringify(machine.sessions)), nodes: [...machine.nodes], peers: [], counts: counts(),
    }
    : { reachable: false, nodeUrl: "http://127.0.0.1:7462", error: DIAL, sessions: [], nodes: [], peers: [], counts: {} }),
  ServiceStatus: async () => {
    // A node somebody starts by hand between two reads of the status.
    if (typeof machine.upAfterReads === "number" && --machine.upAfterReads < 0) machine.nodeUp = true;
    return serviceStatus();
  },
  InstallService: async (form) => {
    writes.push(["InstallService", form]);
    if (machine.installError) throw new Error(machine.installError);
    machine.installed = true;
    machine.running = true;
    machine.nodeUp = true;
    return { command: "ah service install", output: "installed" };
  },
  RestartService: async () => {
    writes.push(["RestartService"]);
    machine.running = true;
    machine.nodeUp = true;
    return { command: "ah service restart", output: "restarted" };
  },
  RestartNode: async () => {
    writes.push(["RestartNode"]);
    machine.nodeUp = true;
    if (machine.installed) machine.running = true;
    return { command: "restart", output: "restarted" };
  },
  NodeSettings: async () => {
    if (!machine.nodeUp) throw new Error("dial tcp 127.0.0.1:7462: connect: connection refused");
    return nodeView();
  },
  SaveNodeSettings: async (patch) => {
    writes.push(["SaveNodeSettings", patch]);
    const next = { ...machine.saved, ...patch };
    if (patch.peerListens) next.peerListen = patch.peerListens[0];
    machine.saved = next;
    return nodeView();
  },
  LocalAddresses: async () => machine.addresses,
  Pairing: async () => {
    if (!machine.nodeUp) return { availability: "unknown", candidates: [], error: "dial tcp" };
    const lan = machine.saved.allowLan ? lanAddress() : "";
    // A default node on loopback sends no peerAddress at all (§3.3).
    return {
      availability: "on", windowAvailable: true, candidates: [],
      state: lan ? { open: false, peerAddress: lan, peerAddressReachable: true } : { open: false },
    };
  },
  SetAudience: async (ids, audience) => {
    audienceCalls.push({ ids: [...ids], audience: { ...audience } });
    for (const s of machine.sessions) if (ids.includes(s.id)) s.audience = { ...audience };
    return { changed: ids.length, failed: 0, errors: [] };
  },
  Discover: async () => { writes.push(["Discover"]); return { claude: 0, codex: 0, total: 0, skipped: 0 }; },
  InboxCounts: async () => ({ ok: true, counts: {} }),
  PairRequests: async () => [],
  OpenPairing: async () => ({ open: true }), ClosePairing: async () => ({ open: false }),
  TrustNode: async () => ({}), RevokeNode: async () => ({}), Heartbeat: async () => "",
  Inbox: async () => ({ messages: [] }), ClearInbox: async () => ({}), CopyText: async () => ({}),
  UninstallService: async () => ({}), HostPlatform: async () => "darwin", Version: async () => ({ release: "unreleased" }),
};

const { configure, boot } = await import("../src/app.js");
configure(bindings);

// A new window on the machine as `setup` describes it, after its first read.
async function start(setup = {}, { prefs = null, read = true } = {}) {
  store.clear();
  if (prefs) store.set(PREFS, JSON.stringify(prefs));
  machine = { ...blank(), ...setup };
  writes.length = 0;
  audienceCalls.length = 0;
  const app = boot({ start: false });
  if (read) {
    await app.load();
    await flush();
  }
  return app;
}

/* ---------------- reading the wizard back ---------------- */

const walk = (node, visit, skipHidden = true) => {
  if (!node || typeof node !== "object") return;
  if (skipHidden && String(node.className ?? "").split(/\s+/).includes("hidden")) return;
  visit(node);
  for (const child of node.children ?? []) walk(child, visit, skipHidden);
};
const stage = () => el("first-run-stage");
const shown = () => !el("first-run").classList.contains("hidden");
const stageText = () => {
  const parts = [];
  walk(stage(), (node) => { if (node._text) parts.push(node._text); });
  return parts.join(" ");
};
const buttons = () => {
  const found = [];
  walk(stage(), (node) => { if (node.tagName === "button") found.push(node); });
  return found;
};
const button = (label) => buttons().find((node) => node.textContent === label);
const primary = () => buttons().find((node) => String(node.className).split(/\s+/).includes("primary"));
const inputs = (type) => {
  const found = [];
  walk(stage(), (node) => { if (node.tagName === "input" && node.type === type) found.push(node); });
  return found;
};
// Step 1's three lines, by position: the node, the login item, the network.
const checkRows = () => {
  const found = [];
  walk(stage(), (node) => { if (String(node.className).split(/\s+/)[0] === "frcheck") found.push(node); });
  return found;
};
const rowState = (index) => String(checkRows()[index]?.className ?? "").split(/\s+/)[1] ?? "";
const railRows = () => [...el("first-run-steps").children];
const writeNames = () => writes.map((entry) => entry[0]);
const ready = { nodeUp: true, installed: true, running: true };

/* ---------------- 1. when it comes up ---------------- */

{
  // Before anything has answered, nodeReachable is false only because nothing
  // has been asked. The wizard stays up once it is up, so coming up now would
  // cover a finished machine on every launch.
  const app = await start({ ...ready, sessions: [session("a", "claude")], nodes: ["n"] }, { read: false });
  app.render();
  if (shown()) failures.push("the wizard came up before any read had answered");

  // A node that never answered is what a first run is.
  const down = await start({});
  if (!shown()) failures.push("a node that is not answering did not bring the wizard up");
  if (down.state.firstRun.step !== 1) failures.push(`the wizard opened on step ${down.state.firstRun.step}, want 1`);

  // A finished machine: nothing to set up, nothing shown.
  await start({ ...ready, sessions: [session("a", "claude")], nodes: [{ nodeId: "n" }] });
  if (shown()) failures.push("a finished machine was shown the wizard");

  // Each of the other triggers, alone, after a read that reached the node.
  for (const [name, setup] of [
    ["the service is not installed", { nodeUp: true, installed: false, running: false, sessions: [session("a", "claude")], nodes: [{ nodeId: "n" }] }],
    ["no sessions were found", { ...ready, sessions: [], nodes: [{ nodeId: "n" }] }],
    ["no machine is paired", { ...ready, sessions: [session("a", "claude")], nodes: [] }],
  ]) {
    await start(setup);
    if (!shown()) failures.push(`the wizard stayed down when ${name}`);
  }

  // A platform with no service manager is not a reason: the node runs, and
  // nothing this app can ask holds it.
  await start({ nodeUp: true, supported: false, sessions: [session("a", "claude")], nodes: [{ nodeId: "n" }] });
  if (shown()) failures.push("a platform with no service manager brought the wizard up");

  // Put away, remembered, and honoured on the next launch — including the
  // preference the checklist wrote before the wizard existed.
  await start({}, { prefs: { onboardingDismissed: true } });
  if (shown()) failures.push("a window whose owner put setup away brought it back up on the next launch");

  // Up once it is up: the owner pressing something makes it theirs, and the
  // triggers the first step turns off do not take it away.
  const latched = await start({});
  latched.state.firstRun.touched = true;
  Object.assign(machine, ready, { sessions: [session("a", "claude")], nodes: [{ nodeId: "n" }] });
  await latched.load();
  await flush();
  if (!shown()) failures.push("the wizard vanished once the owner had started it and the triggers went");

  // And a wizard nobody touched goes once there is nothing to set up: a node
  // slow to answer at launch on a finished machine.
  const slow = await start({ sessions: [session("a", "claude")], nodes: [{ nodeId: "n" }], installed: true, running: true });
  if (!shown()) failures.push("the slow-node case never brought the wizard up, so the next line proves nothing");
  machine.nodeUp = true;
  await slow.load();
  await flush();
  if (shown()) failures.push("an untouched wizard stayed over a machine with nothing to set up");
}

/* ---------------- 2. the parts it replaces are gone while it is up ---------------- */

{
  const app = await start({});
  if (!el("local-view").classList.contains("hidden")) failures.push("the sessions table is under the wizard");
  if (!el("app").classList.contains("firstrun-on")) failures.push("the title bar still carries the tabs, the pill and 重新掃描");
  // The attention strip would say 「節點沒有回應」 with 重試 beside the step
  // that starts the node: one thing, two buttons.
  if (!el("attention").classList.contains("hidden")) failures.push("the attention strip is on screen beside the wizard");
  if (attentionRows(document).length !== 0) failures.push("attention rows are readable while the wizard is up");
  if (!app.state.notices.some((notice) => notice.title === ZH["attention.nodeDown.title"])) {
    failures.push("the node row the wizard stands in for never reached the bell");
  }
  // The node's dial error is not the sentence. It is under 「說明」.
  const nodeRow = checkRows()[0];
  const sentence = [];
  walk(nodeRow, (node) => { if (node.tagName !== "p" && node._text) sentence.push(node._text); });
  if (sentence.join(" ").includes("dial tcp")) failures.push(`the dial error is on the node line itself: ${sentence.join(" ")}`);
  if (!sentence.join(" ").includes(ZH["firstRun.node.down"])) failures.push(`the node line does not say AgentHub is not running: ${sentence.join(" ")}`);
  let inDetails = false;
  walk(nodeRow, (node) => { if (node.tagName === "details") walk(node, (child) => { if (String(child._text).includes("dial tcp")) inDetails = true; }); });
  if (!inDetails) failures.push("the dial error is not under 「說明」 either");
  // One primary button.
  const primaries = buttons().filter((node) => String(node.className).split(/\s+/).includes("primary"));
  if (primaries.length !== 1) failures.push(`step 1 shows ${primaries.length} primary buttons, want 1`);

  // Put away, the main window is back, with the strip.
  el("first-run-later").onclick();
  if (shown()) failures.push("「稍後再設定」 left the wizard up");
  if (el("local-view").classList.contains("hidden")) failures.push("「稍後再設定」 did not bring the main window back");
  if (el("attention").classList.contains("hidden")) failures.push("the strip did not come back with the main window");
}

/* ---------------- 3. step 1's three lines are state, not memory ---------------- */

{
  const app = await start({ nodeUp: true, installed: true, running: true, sessions: [session("a", "claude")] });
  // Node answering, service installed and running, loopback: two of three.
  if (rowState(0) !== "ok") failures.push(`an answering node's line reads ${rowState(0)}`);
  if (rowState(1) !== "ok") failures.push(`a running service's line reads ${rowState(1)}`);
  if (rowState(2) === "ok") failures.push("a node on loopback was called reachable from the network");
  if (primary()?.textContent !== ZH["firstRun.openLan"]) {
    failures.push(`with only the network left the button reads ${primary()?.textContent}`);
  }
  // The service removed from a terminal: the line comes back by itself.
  machine.installed = false;
  machine.running = false;
  await app.load();
  await flush();
  if (rowState(1) === "ok") failures.push("a service removed behind the window's back stayed ticked");
  if (primary()?.textContent !== ZH["firstRun.prepare"]) {
    failures.push(`with the service gone the button reads ${primary()?.textContent}`);
  }
  // The node's own word on its address decides the third line (§3.3).
  machine.installed = true;
  machine.running = true;
  app.state.pairing = { windowAvailable: true, state: { peerAddress: "192.168.50.10:7463", peerAddressReachable: true } };
  app.renderFirstRun();
  if (rowState(2) !== "ok") failures.push(`a node that says its address is reachable reads ${rowState(2)}`);
  app.state.pairing = { windowAvailable: true, state: { peerAddress: "192.168.50.10:7463", peerAddressReachable: false, peerAddressProblem: "bind failed" } };
  app.renderFirstRun();
  if (rowState(2) === "ok") failures.push("a node that says its address cannot be reached was ticked anyway");
  // Everything done: the step says so and offers the next one.
  app.state.pairing = { windowAvailable: true, state: { peerAddress: "192.168.50.10:7463", peerAddressReachable: true } };
  await app.loadService();
  app.renderFirstRun();
  if (primary()?.textContent !== ZH["firstRun.next"]) failures.push(`a finished step 1 offers ${primary()?.textContent}, not 下一步`);
  // No service manager: explained, not a failure, and not in the way.
  app.state.service = { supported: false, installed: false, running: false };
  app.renderFirstRun();
  if (rowState(1) !== "na") failures.push(`a platform with no service manager reads ${rowState(1)}, want na`);
  if (!app.firstRunStepComplete(1)) failures.push("a platform with no service manager held step 1 back");
}

/* ---------------- 4. one press, in order, stopping at a failure ---------------- */

{
  // What a .dmg leaves: nothing running, nothing registered. The node comes
  // first — its settings live in it — then the address, whose save restarts
  // it through the service that is now there.
  const app = await start({ addresses: ONE });
  await primary().onclick();
  await flush();
  const order = writeNames().filter((name) => name !== "Discover");
  if (JSON.stringify(order) !== JSON.stringify(["InstallService", "SaveNodeSettings", "RestartNode"])) {
    failures.push(`the one press ran ${JSON.stringify(order)}, want InstallService, SaveNodeSettings, RestartNode`);
  }
  const install = writes.find((entry) => entry[0] === "InstallService")?.[1];
  // Registered with nothing baked into the unit (#116): the settings are the
  // node's own, saved afterwards.
  if (install && (install.allowLan || install.peerListen)) failures.push(`the service was installed with settings in it: ${JSON.stringify(install)}`);
  const save = writes.find((entry) => entry[0] === "SaveNodeSettings")?.[1] ?? {};
  if (save.allowLan !== true || JSON.stringify(save.peerListens) !== JSON.stringify(["192.168.50.10:7463"])) {
    failures.push(`the save sent ${JSON.stringify(save)}, not the address and the switch the line above the button named`);
  }
  if (app.state.firstRun.step !== 2) failures.push(`a step 1 that finished left the wizard on step ${app.state.firstRun.step}, want 2`);
  if (!railRows()[0]?.className.includes("done")) failures.push("the rail did not tick step 1");

  // A failure stops everything after it, and says so where it happened.
  const failing = await start({ addresses: ONE, installError: "launchctl bootstrap: 5: Input/output error" });
  await primary().onclick();
  await flush();
  if (writeNames().includes("SaveNodeSettings")) failures.push("the address was saved after the service failed to install");
  if (failing.state.firstRun.step !== 1) failures.push(`a failed install moved the wizard on to step ${failing.state.firstRun.step}`);
  if (rowState(1) !== "fail") failures.push(`the login line after a failed install reads ${rowState(1)}`);
  const text = stageText();
  if (!text.includes(ZH["firstRun.login.failed"])) failures.push(`the failure has no readable sentence: ${text}`);
  let raw = false;
  walk(checkRows()[1], (node) => { if (node.tagName === "details") walk(node, (child) => { if (String(child._text).includes("Input/output error")) raw = true; }); });
  if (!raw) failures.push("the install's own words are not under 「說明」");
  if (!button(ZH["firstRun.retry"])) failures.push("a failed step offers no 重試");
  // 重試 runs it again, and this time it goes through.
  machine.installError = "";
  await button(ZH["firstRun.retry"]).onclick();
  await flush();
  if (!writeNames().includes("SaveNodeSettings")) failures.push("重試 did not carry on to the address once the install went through");
}

/* ---------------- 5. the identity guards hold ---------------- */

{
  // A node answering with nothing registered was started by hand, on a
  // database this window cannot see. Installing the default one over it is a
  // new identity and every pairing gone — so no install: the form that asks.
  const app = await start({ nodeUp: true, installed: false, running: false });
  await primary().onclick();
  await flush();
  if (writeNames().includes("InstallService")) failures.push("the wizard installed over a node that is running but is not a service");
  if (app.state.view !== "settings" || app.state.settingsSection !== "settings-service") {
    failures.push(`the wizard did not go to the service form: ${app.state.view}/${app.state.settingsSection}`);
  }
  if (el("service-form").classList.contains("hidden")) failures.push("the service form was not opened to ask about the database");
  if (shown()) failures.push("the wizard stayed on top of the form it sent the owner to");
  // Said, too: the owner pressed 「準備好這台電腦」 and is looking at Settings.
  if (!latestToast(document).textContent.includes(ZH["firstRun.toServiceForm"])) {
    failures.push(`nothing said why the wizard went to the service form: ${latestToast(document).textContent}`);
  }
  if (el("btn-resume-setup").classList.contains("hidden")) failures.push("no 繼續設定 to come back by");
  if (writeNames().includes("SaveNodeSettings")) failures.push("the address was saved while the service question was open");

  // And the same when the status on screen is stale: read again before
  // anything is installed, and a node found answering is not installed over.
  const stale = await start({});
  machine.nodeUp = true;
  await primary().onclick();
  await flush();
  if (writeNames().includes("InstallService")) failures.push("a stale 「nothing is running」 installed over a node that had since started");

  // And when the node starts between the wizard's own read and the press:
  // the install goes through the pill's quick action, which reads the status
  // once more itself before installing anything.
  await start({});
  machine.upAfterReads = 1;
  await primary().onclick();
  await flush();
  if (writeNames().includes("InstallService")) {
    failures.push("a node started between the wizard's read and the install was installed over: the quick action's own re-read was skipped");
  }

  // Installed and stopped is started, not installed again.
  await start({ installed: true, running: false });
  await primary().onclick();
  await flush();
  if (writeNames().includes("InstallService")) failures.push("an installed service was installed again instead of started");
  if (!writeNames().includes("RestartService")) failures.push(`an installed, stopped service was not started: ${JSON.stringify(writeNames())}`);
  void stale;
}

/* ---------------- 6. the network is the owner's choice ---------------- */

{
  // Two private networks: both listed, one pre-selected, and the line above
  // the button names whichever is picked.
  const app = await start({ ...ready, addresses: TWO, sessions: [session("a", "claude")] });
  const radios = inputs("radio");
  if (radios.length !== 2) failures.push(`${radios.length} addresses offered on a machine with two private networks`);
  if (radios.filter((radio) => radio.checked).length !== 1) failures.push("not exactly one address pre-selected");
  if (!stageText().includes("192.168.50.10:7463")) failures.push("the line above the button does not name the pre-selected address");
  const second = radios.find((radio) => radio.value === "10.0.0.5");
  second.checked = true;
  second.onchange();
  if (!stageText().includes(ZH["firstRun.lan.consent"].replace("{address}", "10.0.0.5:7463"))) {
    failures.push(`picking the other network did not change what the button says it opens: ${stageText()}`);
  }
  await primary().onclick();
  await flush();
  const save = writes.find((entry) => entry[0] === "SaveNodeSettings")?.[1] ?? {};
  if (JSON.stringify(save.peerListens) !== JSON.stringify(["10.0.0.5:7463"])) {
    failures.push(`the save opened ${JSON.stringify(save.peerListens)}, not the network the owner picked`);
  }
  if (app.firstRunChosenAddress()?.address !== "10.0.0.5") failures.push("the pick did not stick");

  // One private network: nothing to choose, and it is named.
  await start({ ...ready, addresses: ONE, sessions: [session("a", "claude")] });
  if (inputs("radio").length !== 0) failures.push("a choice was offered with only one network to choose");
  if (!stageText().includes(ZH["firstRun.lan.consent"].replace("{address}", "192.168.50.10:7463"))) {
    failures.push("the only network is not named above the button");
  }

  // None: said, and the way on is to stay on this machine.
  await start({ ...ready, addresses: [{ interface: "en5", address: "122.122.0.7", subnet: "122.122.0.0/16", private: false }], sessions: [session("a", "claude")] });
  if (!stageText().includes(ZH["firstRun.lan.noPrivate"])) failures.push("a machine with no private address was not told so");
  if (primary()?.textContent !== ZH["firstRun.localOnly"]) {
    failures.push(`with no private address the one way on is ${primary()?.textContent}`);
  }

  // 「只在這台用」 writes no setting at all, and goes past pairing.
  const local = await start({ ...ready, addresses: ONE, sessions: [session("a", "claude")] });
  await button(ZH["firstRun.localOnly"]).onclick();
  await flush();
  if (writeNames().includes("SaveNodeSettings")) failures.push("「只在這台用」 saved a node setting");
  if (local.state.firstRun.step !== 3) failures.push(`「只在這台用」 went to step ${local.state.firstRun.step}, want 3`);
  if (railRows()[1]?.children[2]?.textContent !== ZH["firstRun.rail.localOnly"]) {
    failures.push(`the pairing step is not marked as skipped: ${railRows()[1]?.textContent}`);
  }
  if (!stageText().includes(ZH["firstRun.step3.localOnly"])) failures.push("step 3 does not say sharing can wait");
  if (inputs("checkbox").length !== 0) failures.push("step 3 offered sessions to share after 「只在這台用」");
}

/* ---------------- 7. step 3 shares through the menu's own write ---------------- */

{
  const held = session("held", "claude", {
    title: "Held a directory",
    // Unpublished, but holding the working directory from older data. The
    // wizard never says a directory is going, so it must not go.
    audience: { mode: "none", nodes: [], exportCwd: true, acceptMessages: false, allowOutbound: false, autoWake: false },
  });
  const codex = session("cx", "codex", { lastSeenAt: new Date(Date.now() - 5000).toISOString() });
  // A finished machine brings nothing up by itself; opened by hand, and on to
  // the step under test.
  const app = await start({ ...ready, sessions: [held, codex], nodes: [{ nodeId: "n" }] });
  app.openFirstRun();
  app.state.firstRun.step = 3;
  app.renderFirstRun();
  const boxes = inputs("checkbox");
  if (boxes.length !== 2) failures.push(`step 3 lists ${boxes.length} sessions, want 2`);
  if (boxes.some((box) => box.checked)) failures.push("step 3 started with a session ticked");
  if (!primary()?.disabled || primary()?.textContent !== ZH["firstRun.step3.pickFirst"]) {
    failures.push(`with nothing ticked the share button reads ${primary()?.textContent} (disabled ${primary()?.disabled})`);
  }
  // Newest first: the codex session was seen more recently.
  if (!String(boxes[0]?.parentNode?.textContent ?? "").includes("codex:cx")) failures.push("the list is not newest first");

  // Only Claude Code ticked: waking cannot be picked, and it says why.
  const claudeBox = boxes.find((box) => String(box.parentNode?.textContent).includes("Held a directory"));
  claudeBox.checked = true;
  claudeBox.onchange();
  const wake = () => inputs("radio").find((radio) => radio.value === "wake");
  if (!wake()?.disabled) failures.push("waking could be picked for Claude Code sessions alone");
  if (!stageText().includes(ZH["popover.wakeClaudeOnly"])) failures.push("the disabled wake option does not say why");
  if (!stageText().includes(ZH["wake.caveat"])) failures.push("the wake option lost its caveat");
  const codexBox = inputs("checkbox").find((box) => String(box.parentNode?.textContent).includes("codex:cx"));
  codexBox.checked = true;
  codexBox.onchange();
  if (wake()?.disabled) failures.push("waking stayed disabled with a Codex session ticked");
  if (primary()?.textContent !== ZH["firstRun.step3.share.other"].replace("{n}", "2")) {
    failures.push(`with two ticked the button reads ${primary()?.textContent}`);
  }
  await primary().onclick();
  await flush();
  const call = audienceCalls[0];
  if (audienceCalls.length !== 1) failures.push(`the share made ${audienceCalls.length} SetAudience calls, want 1`);
  if (call && (call.audience.mode !== "all_paired" || call.audience.nodes.length !== 0)) {
    failures.push(`sessions nobody could see were shared as ${JSON.stringify(call.audience)}, not to every paired machine`);
  }
  if (call && call.audience.exportCwd !== false) failures.push("the share published a working directory nothing on screen mentioned");
  if (call && (call.audience.acceptMessages !== true || call.audience.autoWake !== false)) {
    failures.push(`「能留訊息」 wrote ${JSON.stringify(call.audience)}`);
  }
  if (call && JSON.stringify([...call.ids].sort()) !== JSON.stringify(["claude:held", "codex:cx"])) {
    failures.push(`the share wrote ${JSON.stringify(call.ids)}`);
  }
  // The menu's own toast, with its 復原.
  const toast = latestToast(document);
  if (!toast.node || !toastButtons(toast.node).some((node) => node.textContent === ZH["popover.undo"])) {
    failures.push(`the share's toast has no 復原: ${toast.textContent}`);
  }
  if (app.state.firstRun.step !== 4) failures.push(`a share that went through left the wizard on step ${app.state.firstRun.step}`);
  if (!stageText().includes(ZH["firstRun.done.shared.other"].split("{n}")[0])) failures.push(`the done screen does not say what was shared: ${stageText()}`);

  // 開始使用: remembered, the main window back, and no 繼續設定.
  button(ZH["firstRun.done.start"]).onclick();
  const written = JSON.parse(store.get(PREFS) ?? "{}");
  if (written.firstRunFinished !== true || written.onboardingDismissed !== true) {
    failures.push(`開始使用 was not remembered: ${store.get(PREFS)}`);
  }
  if (shown()) failures.push("開始使用 left the wizard up");
  if (!el("btn-resume-setup").classList.contains("hidden")) failures.push("繼續設定 outlived 開始使用");
  const next = boot({ start: false });
  await next.load();
  await flush();
  if (shown()) failures.push("a finished setup came back on the next launch");
}

{
  // No session yet: said, and 重新掃描 is right there.
  const app = await start({ ...ready, nodes: [{ nodeId: "n" }] });
  app.state.firstRun.step = 3;
  app.state.firstRun.touched = true;
  app.renderFirstRun();
  if (!stageText().includes(ZH["firstRun.step3.noSessions"])) failures.push("an empty machine's step 3 does not say to start a session");
  writes.length = 0;
  await button(ZH["app.rescan"])?.onclick();
  await flush();
  if (!writeNames().includes("Discover")) failures.push("重新掃描 on step 3 did not scan");

  // More than eight: the eight most recent, and the rest behind a button.
  machine.sessions = Array.from({ length: 11 }, (_, index) => session(`s${index}`, "codex",
    { lastSeenAt: new Date(Date.now() - index * 1000).toISOString() }));
  await app.load();
  await flush();
  if (inputs("checkbox").length !== 8) failures.push(`${inputs("checkbox").length} sessions shown of 11, want 8`);
  button(ZH["firstRun.step3.showAll"].replace("{n}", "11")).onclick();
  if (inputs("checkbox").length !== 11) failures.push("「顯示全部」 did not show them all");
}

/* ---------------- 8. 稍後再設定, 繼續設定 and the settings link ---------------- */

{
  const app = await start({ ...ready, sessions: [session("a", "claude")] });
  el("first-run-later").onclick();
  const written = JSON.parse(store.get(PREFS) ?? "{}");
  if (written.onboardingDismissed !== true) failures.push(`稍後再設定 was not remembered: ${store.get(PREFS)}`);
  if (el("btn-resume-setup").classList.contains("hidden")) failures.push("no 繼續設定 after 稍後再設定, with setup unfinished");
  if (String(el("btn-resume-setup").className).split(/\s+/).includes("primary")) failures.push("繼續設定 is a primary button");
  // Back to the first step not yet settled: here, step 1 (the network).
  el("btn-resume-setup").onclick();
  if (!shown()) failures.push("繼續設定 did not bring the wizard back");
  if (app.state.firstRun.step !== 1) failures.push(`繼續設定 went to step ${app.state.firstRun.step}, want 1`);
  // With step 1 done, to step 2.
  app.state.pairing = { windowAvailable: true, state: { peerAddress: "192.168.50.10:7463", peerAddressReachable: true } };
  el("first-run-later").onclick();
  el("btn-resume-setup").onclick();
  if (app.state.firstRun.step !== 2) failures.push(`繼續設定 with step 1 done went to step ${app.state.firstRun.step}, want 2`);
  // The step 2 stand-in walks on to step 3.
  button(ZH["firstRun.step2.skipDev"]).onclick();
  if (app.state.firstRun.step !== 3) failures.push("the step 2 stand-in did not go on to step 3");
  if (railRows()[1]?.children[2]?.textContent !== ZH["firstRun.rail.skipped"]) failures.push("the skipped pairing step is not marked");
  // A paired machine is step 2 done, from state.
  app.state.nodes = [{ nodeId: "n" }];
  app.renderFirstRun();
  if (!railRows()[1]?.className.includes("done")) failures.push("a paired machine did not tick step 2");

  // Settings → Appearance opens it again, even after 開始使用.
  app.state.firstRun.step = 4;
  app.renderFirstRun();
  button(ZH["firstRun.done.start"]).onclick();
  el("settings-show-onboarding").onclick();
  if (!shown()) failures.push("「顯示首次設定」 did not open the wizard");
  if (app.state.ui.firstRunFinished || app.state.ui.onboardingDismissed) failures.push("「顯示首次設定」 did not clear what was remembered");
}

/* ---------------- 9. a write in flight, and the tick ---------------- */

{
  const app = await start({ ...ready, addresses: ONE, sessions: [session("a", "claude")] });
  app.state.busy = true;
  app.renderFirstRun();
  const live = buttons().filter((node) => !node.disabled && !String(node.className).includes("frretry"));
  if (live.length !== 0) failures.push(`buttons stayed live during a write: ${live.map((node) => node.textContent).join(" | ")}`);
  if (inputs("radio").some((radio) => !radio.disabled)) failures.push("an address could be picked during a write");
  app.state.busy = false;
  // The fifteen-second tick repaints; the button under the pointer survives it.
  app.renderFirstRun();
  const before = primary();
  await app.load();
  await flush();
  app.renderFirstRun();
  if (primary() !== before) failures.push("a repaint replaced the button an owner is about to press");
  // And an opened 「說明」 stays open across it.
  const why = [];
  walk(stage(), (node) => { if (node.tagName === "details") why.push(node); });
  const opened = why.at(-1);
  if (opened) opened.open = true;
  await app.load();
  await flush();
  const after = [];
  walk(stage(), (node) => { if (node.tagName === "details") after.push(node); });
  if (opened && (!after.includes(opened) || opened.open !== true)) failures.push("a repaint folded an opened 「說明」 shut");
}

/* ---------------- 10. the English half ---------------- */

{
  const app = await start({});
  inEnglish(app, failures, "the first-run wizard", ["first-run-stage", "first-run-steps", "first-run-later"], () => app.render());
}

if (failures.length > 0) {
  for (const failure of failures) console.error(` - ${failure}`);
  process.exit(1);
}
console.log("first run: shown for a reason, one press in order that stops where it fails, "
  + "no identity guard walked around, the network is the owner's pick, and sharing is the menu's own write");
