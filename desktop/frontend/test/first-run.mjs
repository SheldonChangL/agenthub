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
  // A node's own defaults (nodeconfig.DefaultSettings): loopback, and not
  // searching the network — the default that left step 2's list empty while
  // this fake said discover: true and every check here passed.
  saved: { peerListen: LOOPBACK, peerListens: [LOOPBACK], allowLan: false, discover: false, treatAsPrivate: [], autoWake: false },
  installError: "",
  // The pairing window, which a restart shuts as a real node's does.
  windowOpen: false,
  // A node that does not keep discover when it is saved: a flag in the unit, a
  // node that refuses it. The save answers, and the value is not there after.
  dropsDiscover: false,
});
let machine = blank();
// The window the bindings are answering, for a fake that has to start a read
// of its own in the middle of one.
let current = null;
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
    // A real status read is a process `ah` runs, and answers after anything
    // already queued: a caller that fires it and moves on does not have it
    // yet (load() does exactly that).
    await new Promise((resolve) => setImmediate(resolve));
    // A newer read started while this one is in flight, which then answers
    // late: this one is overtaken, and loadService does not store it.
    if (machine.parkThis) {
      machine.parkThis = false;
      await machine.gate;
    }
    if (machine.installed && typeof machine.overtakeAfterInstall === "number" && --machine.overtakeAfterInstall === 0) {
      machine.parkThis = true;
      current.loadService().catch(() => {});
    }
    // A node somebody starts by hand between two reads of the status.
    if (typeof machine.upAfterReads === "number" && --machine.upAfterReads < 0) machine.nodeUp = true;
    return serviceStatus();
  },
  InstallService: async (form) => {
    writes.push(["InstallService", form]);
    // Something that lands while the install is out: an address read, a
    // re-read of the list.
    if (machine.onInstall) machine.onInstall();
    if (machine.installError) throw new Error(machine.installError);
    machine.installed = true;
    machine.running = true;
    machine.nodeUp = true;
    return { command: "ah service install", output: "installed" };
  },
  RestartService: async () => {
    writes.push(["RestartService"]);
    // A service manager that will not start the job, once.
    if (machine.restartError) {
      const error = machine.restartError;
      machine.restartError = "";
      throw new Error(error);
    }
    machine.running = true;
    machine.nodeUp = true;
    machine.windowOpen = false;
    return { command: "ah service restart", output: "restarted" };
  },
  RestartNode: async () => {
    writes.push(["RestartNode"]);
    machine.nodeUp = true;
    machine.windowOpen = false;
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
    if (machine.dropsDiscover) next.discover = machine.saved.discover;
    // What the running node does until the restart that follows every save.
    delete machine.searching;
    if (patch.peerListens) next.peerListen = patch.peerListens[0];
    machine.saved = next;
    return nodeView();
  },
  LocalAddresses: async () => {
    // A read that answers late: held until the test lets it through.
    if (machine.addressesGate) await machine.addressesGate;
    return machine.addresses;
  },
  Pairing: async () => {
    if (!machine.nodeUp) return { availability: "unknown", candidates: [], error: "dial tcp" };
    const lan = machine.saved.allowLan ? lanAddress() : "";
    // A node without -discover answers the window and refuses the candidate
    // list, which App.Pairing reports as off, or openNotAnnouncing while a
    // window is open (desktop/app.go). The fake's saved values are the
    // running ones: every save here is followed by its restart.
    // `searching`, when set, is a running node that differs from what it has
    // saved (started with -discover, saved off since, not yet restarted).
    const looking = typeof machine.searching === "boolean" ? machine.searching : machine.saved.discover;
    const availability = looking ? "on" : (machine.windowOpen ? "openNotAnnouncing" : "off");
    // A default node on loopback sends no peerAddress at all (§3.3).
    return {
      availability, windowAvailable: true, candidates: [],
      state: lan ? { open: machine.windowOpen, peerAddress: lan, peerAddressReachable: true } : { open: machine.windowOpen },
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
  OpenPairing: async (...args) => {
    // Counted apart from `writes`, whose order the sections below assert.
    machine.opens = [...(machine.opens ?? []), args];
    machine.windowOpen = true;
    return { open: true };
  },
  ClosePairing: async () => {
    machine.windowOpen = false;
    return { open: false };
  },
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
  current = app;
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

{
  // It comes up by itself only at launch. A set-up machine whose node stops
  // answering later, or whose last pairing is revoked, is the main window's to
  // say (the attention strip): covering the page the owner is working in with
  // the whole wizard would be the checklist's noise again, louder.
  const settled = await start({ ...ready, sessions: [session("a", "claude")], nodes: [{ nodeId: "n" }] });
  settled.state.view = "settings";
  settled.render();
  machine.nodeUp = false;
  await settled.load();
  await flush();
  if (shown()) failures.push("a node that stopped answering mid-session put the wizard over a set-up window");
  if (el("settings-view").classList.contains("hidden")) failures.push("the settings page was hidden by a node dropping out");
  if (!el("btn-resume-setup").classList.contains("hidden")) {
    failures.push("a set-up machine whose node dropped was offered 繼續設定 for a setup it never started");
  }
  machine.nodeUp = true;
  machine.nodes = [];
  await settled.load();
  await flush();
  if (shown()) failures.push("revoking the last pairing mid-session put the wizard up");
}

/* ---------------- 1b. it opens on the first step not yet done ---------------- */

{
  // The network already open and nothing paired (what install.sh and an
  // earlier 開放區網 leave): step 1 has nothing left, so the wizard comes up
  // on step 2 — at launch, as 「繼續設定」 does — not on a step whose one
  // button is 下一步.
  const lan = { peerListen: "192.168.50.10:7463", peerListens: ["192.168.50.10:7463"], allowLan: true, discover: true, treatAsPrivate: [], autoWake: false };
  const open = await start({ ...ready, saved: lan, sessions: [session("a", "claude")] });
  if (!shown()) failures.push("a machine with nothing paired did not bring the wizard up");
  if (open.state.firstRun.step !== 2) failures.push(`the wizard came up on step ${open.state.firstRun.step} over a step 1 that is done, want 2`);
  // 上一步 goes back as ever, and a repaint or a read does not move it on.
  await button(ZH["firstRun.back"])?.onclick();
  await flush();
  if (open.state.firstRun.step !== 1) failures.push(`上一步 from the step it opened on went to ${open.state.firstRun.step}, want 1`);
  await open.load();
  await flush();
  if (open.state.firstRun.step !== 1) failures.push(`a read after 上一步 moved the wizard on to step ${open.state.firstRun.step}`);

  // Paired too, no session yet: step 3.
  const third = await start({ ...ready, saved: lan, nodes: [{ nodeId: "n" }] });
  if (third.state.firstRun.step !== 3) failures.push(`with steps 1 and 2 done the wizard came up on step ${third.state.firstRun.step}, want 3`);

  // Nothing done: step 1, as before.
  const fresh = await start({});
  if (fresh.state.firstRun.step !== 1) failures.push(`a fresh machine's wizard came up on step ${fresh.state.firstRun.step}, want 1`);
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
  // Held on step 1 as an owner who has pressed something is: an untouched
  // wizard moves on by itself once step 1 is done (section 1b).
  app.state.firstRun.touched = true;
  // Node answering, service installed and running, loopback, not searching:
  // two of three, and the network line names both halves it will turn on.
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
  app.state.pairing = { availability: "on", windowAvailable: true, state: { peerAddress: "192.168.50.10:7463", peerAddressReachable: true } };
  app.renderFirstRun();
  if (rowState(2) !== "ok") failures.push(`a node that says its address is reachable reads ${rowState(2)}`);
  app.state.pairing = { availability: "on", windowAvailable: true, state: { peerAddress: "192.168.50.10:7463", peerAddressReachable: false, peerAddressProblem: "bind failed" } };
  app.renderFirstRun();
  if (rowState(2) === "ok") failures.push("a node that says its address cannot be reached was ticked anyway");
  // Reachable and not searching — the network opened by a build before this
  // one, or by hand without -discover: the line is not done, step 1 is not
  // done, and the one button is the switch, named on the line above it.
  await app.loadService();
  for (const availability of ["off", "openNotAnnouncing"]) {
    app.state.pairing = { availability, windowAvailable: true, state: { open: availability !== "off", peerAddress: "192.168.50.10:7463", peerAddressReachable: true } };
    app.renderFirstRun();
    if (rowState(2) === "ok") failures.push(`a reachable node that is not searching (${availability}) was ticked`);
    if (app.firstRunStepComplete(1)) failures.push(`step 1 was complete on a node that is not searching (${availability})`);
    if (primary()?.textContent !== ZH["firstRun.turnOnSearch"]) {
      failures.push(`a reachable node that is not searching offers ${primary()?.textContent}, want ${ZH["firstRun.turnOnSearch"]}`);
    }
    if (!stageText().includes(ZH["firstRun.lan.consentSearch"])) failures.push(`the switch's press is not explained above it (${availability}): ${stageText()}`);
    if (stageText().includes(ZH["firstRun.lan.consent"].split("{address}")[0])) {
      failures.push(`an address already open is offered as opened again (${availability}): ${stageText()}`);
    }
    if (!stageText().includes(ZH["firstRun.lan.notSearching"].replace("{address}", "192.168.50.10:7463"))) {
      failures.push(`the network line does not say what is missing (${availability}): ${stageText()}`);
    }
  }
  // Everything done: the step says so and offers the next one.
  app.state.pairing = { availability: "on", windowAvailable: true, state: { peerAddress: "192.168.50.10:7463", peerAddressReachable: true } };
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
  // Searching the network goes in that same save — the one restart above —
  // or step 2's list stays empty on a node left at its default.
  if (save.discover !== true) failures.push(`the one save did not turn searching the network on: ${JSON.stringify(save)}`);
  if (app.state.pairing?.availability !== "on") failures.push(`after step 1 the node is not searching: ${app.state.pairing?.availability}`);
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

{
  // The service step is judged on its own read's answer. A newer status read
  // started while that one is out — the fifteen-second tick's — overtakes it,
  // and loadService stores only the newest: state.service is then the status
  // from before the install, and a service that installed was reported as one
  // that did not.
  const app = await start({ addresses: ONE });
  let release = () => {};
  machine.gate = new Promise((resolve) => { release = resolve; });
  // The install's own load() reads once; the wizard's read is the next one.
  machine.overtakeAfterInstall = 2;
  await primary().onclick();
  await flush();
  release();
  await flush();
  if (app.state.firstRun.failed.login) {
    failures.push(`a service that installed was reported as not registered: ${app.state.firstRun.failed.login.text}`);
  }
  if (!writeNames().includes("SaveNodeSettings")) failures.push("an overtaken status read stopped the flow before the address");
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

/* ---------------- 6b. what is opened is what was named at the press ---------------- */

{
  const consentFor = (address) => ZH["firstRun.lan.consent"].replace("{address}", address);
  const consentPrefix = ZH["firstRun.lan.consent"].split("{address}")[0];
  const saves = () => writes.filter((entry) => entry[0] === "SaveNodeSettings").map((entry) => entry[1]);

  // A fresh machine whose address read answers only after the press. On
  // screen at the press: 「沒有區網位址」, no consent line, and a button that
  // says 準備好這台電腦. The node and the service are that press's; the network
  // is not, because nothing on screen named an address.
  let release = () => {};
  const late = await start({ addresses: ONE, addressesGate: new Promise((resolve) => { release = resolve; }) });
  if (!stageText().includes(ZH["firstRun.lan.noPrivate"]) || stageText().includes(consentPrefix)) {
    failures.push(`the late-address case did not start from a screen that names no address: ${stageText()}`);
  }
  if (primary()?.textContent !== ZH["firstRun.prepare"]) failures.push(`the late-address case's button reads ${primary()?.textContent}`);
  machine.onInstall = () => { machine.addressesGate = null; release(); };
  await primary().onclick();
  await flush();
  if (saves().length !== 0) {
    failures.push(`a press made with no address on screen opened the network: ${JSON.stringify(writes)}`);
  }
  if (!writeNames().includes("InstallService")) failures.push("the late-address press did not install the service it was for");
  if (late.state.firstRun.step !== 1) failures.push(`a press that opened nothing moved the wizard on to step ${late.state.firstRun.step}`);
  // Now the address is on screen, named, and the next press is the one for it.
  if (!stageText().includes(consentFor("192.168.50.10:7463"))) failures.push(`the address that landed is not named now: ${stageText()}`);
  if (primary()?.textContent !== ZH["firstRun.openLan"]) failures.push(`after the node step the button reads ${primary()?.textContent}, want 開放區網並繼續`);
  const writesBefore = saves().length;
  if (primary()?.textContent === ZH["firstRun.openLan"]) await primary().onclick();
  await flush();
  if (writesBefore === 0 && JSON.stringify(saves().map((save) => save.peerListens)) !== JSON.stringify([["192.168.50.10:7463"]])) {
    failures.push(`the press made with the address on screen saved ${JSON.stringify(saves())}`);
  }

  // The list changes after the press (a re-read landing while the install is
  // out): what is saved is the address the line named when it was pressed,
  // not the one the new list would pick.
  await start({ addresses: TWO });
  if (!stageText().includes(consentFor("192.168.50.10:7463"))) failures.push(`the list-change case did not start by naming 192.168.50.10:7463: ${stageText()}`);
  machine.onInstall = () => { current.state.firstRun.addresses = { list: [TWO[1]], failure: "" }; };
  await primary().onclick();
  await flush();
  const listSaves = saves();
  if (listSaves.length === 0) failures.push("the list-change case never reached the save, so it proves nothing");
  if (listSaves.some((save) => JSON.stringify(save.peerListens) !== JSON.stringify(["192.168.50.10:7463"]))) {
    failures.push(`a list that changed after the press opened ${JSON.stringify(listSaves.map((save) => save.peerListens))}, not the 192.168.50.10:7463 on screen`);
  }

  // The node's saved address, read only once it runs, would pick the other
  // network: still the one on screen.
  await start({ addresses: TWO, saved: { ...blank().saved, peerListens: [LOOPBACK, "10.0.0.5:7463"] } });
  if (!stageText().includes(consentFor("192.168.50.10:7463"))) failures.push(`the saved-address case did not start by naming 192.168.50.10:7463: ${stageText()}`);
  await primary().onclick();
  await flush();
  const savedSaves = saves();
  if (savedSaves.length === 0) failures.push("the saved-address case never reached the save, so it proves nothing");
  if (savedSaves.some((save) => JSON.stringify(save.peerListens) !== JSON.stringify(["192.168.50.10:7463"]))) {
    failures.push(`the node's saved address overrode the one on screen: ${JSON.stringify(savedSaves.map((save) => save.peerListens))}`);
  }

  // The port changes after the press: before the node runs the line names
  // the default port, and the node, once up, has another saved. The one named
  // is the one opened.
  await start({ installed: true, running: false, addresses: ONE, saved: { ...blank().saved, peerListen: "127.0.0.1:7500", peerListens: ["127.0.0.1:7500"] } });
  if (!stageText().includes(consentFor("192.168.50.10:7463"))) failures.push(`the port case did not start by naming 192.168.50.10:7463: ${stageText()}`);
  await primary().onclick();
  await flush();
  const portSaves = saves();
  if (!writeNames().includes("RestartService")) failures.push(`the port case did not start the service: ${JSON.stringify(writeNames())}`);
  if (portSaves.length === 0) failures.push("the port case never reached the save, so it proves nothing");
  if (portSaves.some((save) => JSON.stringify(save.peerListens) !== JSON.stringify(["192.168.50.10:7463"]))) {
    failures.push(`a port that changed after the press opened ${JSON.stringify(portSaves.map((save) => save.peerListens))}, not the :7463 on screen`);
  }
}

{
  // 「只在這台用」 pressed on a machine whose node step then fails: the choice
  // is not taken yet. The network line, its consent and both answers stay up,
  // and 重試 repeats the choice that was made rather than opening anything.
  const app = await start({ addresses: ONE, installError: "launchctl bootstrap: 5: Input/output error" });
  await button(ZH["firstRun.localOnly"]).onclick();
  await flush();
  if (!writeNames().includes("InstallService")) failures.push("「只在這台用」 on a stopped machine did not try to start it");
  if (app.state.firstRun.localOnly) failures.push("「只在這台用」 was taken although its node step failed");
  if (rowState(2) === "skipped") failures.push("the network line reads skipped after a node step that failed");
  if (!button(ZH["firstRun.localOnly"]) || button(ZH["firstRun.localOnly"]).disabled) {
    failures.push(`after the failure 「只在這台用」 is gone or dead: ${buttons().map((node) => node.textContent).join(" | ")}`);
  }
  if (!stageText().includes(ZH["firstRun.lan.consent"].replace("{address}", "192.168.50.10:7463"))) {
    failures.push(`after the failure the network question is gone: ${stageText()}`);
  }
  if (!button(ZH["firstRun.retry"])) failures.push("the failed node step offers no 重試");
  machine.installError = "";
  await button(ZH["firstRun.retry"]).onclick();
  await flush();
  if (writeNames().includes("SaveNodeSettings")) failures.push("重試 after 「只在這台用」 opened the network");
  if (!app.state.firstRun.localOnly) failures.push("重試 after 「只在這台用」 did not take the choice once the node was up");
  if (app.state.firstRun.step !== 3) failures.push(`重試 after 「只在這台用」 went to step ${app.state.firstRun.step}, want 3`);
}

/* ---------------- 6c. searching the network, so the other machine shows up ---------------- */

{
  const saves = () => writes.filter((entry) => entry[0] === "SaveNodeSettings").map((entry) => entry[1]);
  const searchBox = () => {
    let found = null;
    walk(stage(), (node) => { if (String(node.className).split(/\s+/).includes("frsearchoff")) found = node; });
    return found;
  };
  const textOf = (root) => {
    const parts = [];
    walk(root, (node) => { if (node._text) parts.push(node._text); });
    return parts.join(" ");
  };
  const detailsText = (root) => {
    const parts = [];
    walk(root, (node) => { if (node.tagName === "details") walk(node, (child) => { if (child._text) parts.push(child._text); }, false); });
    return parts.join(" ");
  };
  const lan = { peerListen: "192.168.50.10:7463", peerListens: ["192.168.50.10:7463"], allowLan: true, discover: false, treatAsPrivate: [], autoWake: false };

  // The network opened by an earlier build, which never turned searching on:
  // step 1 is not done, the wizard opens on it, and its one button writes the
  // switch alone — the address and allowLan stay as they are.
  let older = await start({ ...ready, saved: { ...lan }, sessions: [session("a", "claude")] });
  if (older.state.firstRun.step !== 1) failures.push(`a reachable node that is not searching opened the wizard on step ${older.state.firstRun.step}, want 1`);
  if (rowState(2) === "ok") failures.push("a reachable node that is not searching has its network line ticked");
  if (primary()?.textContent !== ZH["firstRun.turnOnSearch"]) failures.push(`the older node's button reads ${primary()?.textContent}`);
  // 「不開放區網」 is not a choice left to make on a network already open.
  if (button(ZH["firstRun.localOnly"])) failures.push("「只在這台用，不開放區網」 is offered on a node whose network is already open");

  // 「下一步，先不搜尋」 is the "no" to searching alone. Where the address is
  // still to be opened it is not: going on would leave a node nobody can
  // reach, and the "no" there is 「只在這台用」. A fresh node — nothing
  // running — and the same node once running on loopback, its defaults.
  for (const [name, setup] of [
    ["a fresh node", {}],
    ["a running node on its default loopback address", { ...ready, sessions: [session("a", "claude")] }],
  ]) {
    await start(setup);
    if (button(ZH["firstRun.skipSearch"])) {
      failures.push(`${name} offers 「下一步，先不搜尋」 past an address nobody can reach: ${buttons().map((node) => node.textContent).join(" | ")}`);
    }
    if (setup.nodeUp && !button(ZH["firstRun.localOnly"])) failures.push(`${name} has no 「只在這台用」 to say no with`);
  }

  // The node running on a reachable address and not searching, its service
  // registered and stopped: the one button is 「準備好這台電腦」 under a
  // consent line asking for searching, and that line has its "no" too. It
  // starts the service, writes nothing of the network line, and goes on.
  {
    const stopped = await start({ nodeUp: true, installed: true, running: false, saved: { ...lan }, sessions: [session("a", "claude")] });
    if (primary()?.textContent !== ZH["firstRun.prepare"]) failures.push(`the stopped-service case's button reads ${primary()?.textContent}, want ${ZH["firstRun.prepare"]}`);
    if (!stageText().includes(ZH["firstRun.lan.consentSearch"])) failures.push(`the stopped-service case does not ask for searching: ${stageText()}`);
    const no = button(ZH["firstRun.prepareNoSearch"]);
    if (!no) failures.push(`a consent line asking for searching has no "no" while the service is stopped: ${buttons().map((node) => node.textContent).join(" | ")}`);
    else if (!String(no.className).split(/\s+/).includes("ghost")) failures.push(`「準備好這台電腦，先不搜尋」 is not a ghost button: ${no.className}`);
    if (button(ZH["firstRun.skipSearch"])) failures.push("「下一步，先不搜尋」 is offered beside a service that is not running");
    await no?.onclick();
    await flush();
    if (!writeNames().some((name) => name === "RestartService" || name === "RestartNode")) {
      failures.push(`「準備好這台電腦，先不搜尋」 did not start the service: ${JSON.stringify(writeNames())}`);
    }
    if (saves().length !== 0 || machine.saved.discover !== false) {
      failures.push(`「準備好這台電腦，先不搜尋」 wrote ${JSON.stringify(saves())}, discover now ${machine.saved.discover}`);
    }
    if (!machine.running) failures.push("after 「準備好這台電腦，先不搜尋」 the service is still not running");
    if (stopped.state.firstRun.step !== 2) failures.push(`「準備好這台電腦，先不搜尋」 went to step ${stopped.state.firstRun.step}, want 2`);
    if (stopped.state.firstRun.localOnly) failures.push("「準備好這台電腦，先不搜尋」 was taken as 「只在這台用」");
    if (!searchBox()) failures.push("step 2 after 「準備好這台電腦，先不搜尋」 does not offer the switch again");

    // Its 重試 repeats the "no": a service that fails to start, then 重試,
    // still writes no discover.
    const again = await start({ nodeUp: true, installed: true, running: false, restartError: "launchctl kickstart: 5: Input/output error", saved: { ...lan }, sessions: [session("a", "claude")] });
    await button(ZH["firstRun.prepareNoSearch"])?.onclick();
    await flush();
    if (again.state.firstRun.step !== 1) failures.push(`a service that would not start under 「準備好這台電腦，先不搜尋」 moved the wizard to step ${again.state.firstRun.step}`);
    const retry = button(ZH["firstRun.retry"]);
    if (!retry) failures.push(`a service that failed under 「準備好這台電腦，先不搜尋」 offers no 重試: ${stageText()}`);
    await retry?.onclick();
    await flush();
    if (saves().length !== 0 || machine.saved.discover !== false) {
      failures.push(`重試 after 「準備好這台電腦，先不搜尋」 wrote ${JSON.stringify(saves())}, discover now ${machine.saved.discover}`);
    }
    if (again.state.firstRun.step !== 2) failures.push(`重試 after 「準備好這台電腦，先不搜尋」 went to step ${again.state.firstRun.step}, want 2`);
  }
  // The "no" to searching: on to step 2, nothing written, and the switch is
  // there again in the list's place.
  const declined = await start({ ...ready, saved: { ...lan }, sessions: [session("a", "claude")] });
  const skip = button(ZH["firstRun.skipSearch"]);
  if (!skip) failures.push(`a press that only turns searching on has no way to say no: ${buttons().map((node) => node.textContent).join(" | ")}`);
  await skip?.onclick();
  await flush();
  if (saves().length !== 0) failures.push(`declining the search wrote ${JSON.stringify(saves())}`);
  if (declined.state.firstRun.step !== 2) failures.push(`declining the search went to step ${declined.state.firstRun.step}, want 2`);
  if (!searchBox()) failures.push("step 2 after declining the search does not offer the switch again");
  older = await start({ ...ready, saved: { ...lan }, sessions: [session("a", "claude")] });
  await primary()?.onclick();
  await flush();
  if (JSON.stringify(saves()) !== JSON.stringify([{ discover: true }])) {
    failures.push(`turning searching on for an open node saved ${JSON.stringify(saves())}, want [{"discover":true}]`);
  }
  if (older.state.firstRun.step !== 2) failures.push(`the older node did not move on to step 2 once searching: step ${older.state.firstRun.step}`);
  if (!railRows()[0]?.className.includes("done")) failures.push("the rail did not tick step 1 once the node was searching");

  // A press made while the line above the button did not mention searching
  // writes no discover. The running node searches (started with -discover)
  // and has saved it off since: the line names the address alone, and the
  // save that follows must not carry a switch nobody was told about.
  const unnamed = await start({ ...ready, searching: true, sessions: [session("a", "claude")] });
  if (stageText().includes(ZH["firstRun.lan.consentSearch"])) {
    failures.push(`the consent line names searching on a node that already searches: ${stageText()}`);
  }
  if (!stageText().includes(ZH["firstRun.lan.consent"].replace("{address}", "192.168.50.10:7463"))) {
    failures.push(`the unnamed-search case did not start from the address sentence: ${stageText()}`);
  }
  await primary()?.onclick();
  await flush();
  if (saves().length !== 1) failures.push(`the unnamed-search press saved ${saves().length} times, want once`);
  if (saves().some((save) => "discover" in save)) {
    failures.push(`a press whose consent line did not mention searching wrote discover: ${JSON.stringify(saves())}`);
  }
  // The restart left it not searching; the line now says so, and asks again.
  if (unnamed.firstRunStepComplete(1)) failures.push("step 1 was called complete on a node the restart left not searching");
  if (!stageText().includes(ZH["firstRun.lan.consentSearch"])) failures.push(`the next press is not explained: ${stageText()}`);

  // 「只在這台用」 writes nothing, discover included.
  const alone = await start({ ...ready, addresses: ONE, sessions: [session("a", "claude")] });
  await button(ZH["firstRun.localOnly"])?.onclick();
  await flush();
  if (saves().length !== 0 || machine.saved.discover !== false) {
    failures.push(`「只在這台用」 wrote ${JSON.stringify(saves())}, discover now ${machine.saved.discover}`);
  }
  if (alone.state.firstRun.step !== 3) failures.push(`「只在這台用」 went to step ${alone.state.firstRun.step}, want 3`);

  // A save the node does not keep is said, on the network line, with the
  // window's own report of it under 「說明」 — never ticked.
  const dropped = await start({ addresses: ONE, dropsDiscover: true });
  await primary()?.onclick();
  await flush();
  if (dropped.state.firstRun.step !== 1) failures.push(`a search that did not stick moved the wizard on to step ${dropped.state.firstRun.step}`);
  if (rowState(2) !== "fail") failures.push(`after a search that did not stick the network line reads ${rowState(2)}, want fail`);
  if (!stageText().includes(ZH["firstRun.lan.searchFailed"])) failures.push(`a search that did not stick is not said: ${stageText()}`);
  const said = detailsText(checkRows()[2]);
  if (!said.includes(ZH["nodeSettings.savedDidNotStick"].split("{lost}")[0])) {
    failures.push(`the save's own report is not under 「說明」: ${said}`);
  }
  if (!button(ZH["firstRun.retry"])) failures.push("a search that did not stick offers no 重試");
  // The address is open, so pairing by typing it works: the way on is there.
  if (primary()?.textContent !== ZH["firstRun.next"]) failures.push(`after a search that failed the way on reads ${primary()?.textContent}, want 下一步`);
  await primary()?.onclick();
  await flush();
  if (dropped.state.firstRun.step !== 2) failures.push(`下一步 past a failed search went to step ${dropped.state.firstRun.step}`);

  // Step 2, not searching: the list's place says why, says what searching
  // shows of this machine, and offers the switch; the typed address stays.
  const box = searchBox();
  if (!box) failures.push(`step 2 on a node that is not searching has no 「開始在區網上搜尋」: ${stageText()}`);
  else {
    const boxText = textOf(box);
    if (!boxText.includes(ZH["firstRun.pair.notLooking"])) failures.push(`the empty list is not explained: ${boxText}`);
    if (!boxText.includes(ZH["firstRun.lan.consentSearch"])) failures.push(`the switch is not explained in step 1's words: ${boxText}`);
  }
  const startSearch = button(ZH["firstRun.pair.startSearch"]);
  if (!startSearch) failures.push("step 2 offers no 開始在區網上搜尋 button");
  let manualInput = null;
  walk(stage(), (node) => { if (node.tagName === "input" && node.type === "text") manualInput = node; }, false);
  if (!manualInput || manualInput.disabled) failures.push("the typed address is not open beside the switch");

  // Pressed with the node still dropping it: said there, not ticked.
  writes.length = 0;
  const opensBeforeDropped = (machine.opens ?? []).length;
  await startSearch?.onclick();
  await flush();
  if (JSON.stringify(saves()) !== JSON.stringify([{ discover: true }])) {
    failures.push(`step 2's switch saved ${JSON.stringify(saves())}, want [{"discover":true}]`);
  }
  // The save restarted the node and shut its window; searching did not come
  // on, and the window is opened again all the same — the typed address on
  // this screen needs it as much as the list does. Reopened before the
  // failure is judged, not skipped by it.
  const reopenedDropped = (machine.opens ?? []).slice(opensBeforeDropped);
  if (reopenedDropped.length !== 1 || JSON.stringify(reopenedDropped[0]) !== "[0]") {
    failures.push(`after a search that did not come on, the window the restart shut was opened ${JSON.stringify(reopenedDropped)}, want once with [0]`);
  }
  if (!machine.windowOpen) failures.push("a search that did not come on left step 2 with its window shut");
  if (!searchBox() || !textOf(searchBox()).includes(ZH["firstRun.lan.searchFailed"])) {
    failures.push(`step 2's switch failing is not said: ${searchBox() ? textOf(searchBox()) : "(no box)"}`);
  }

  // And once the node keeps it: the list is back, the switch gone, and the
  // window the restart shut is open again.
  machine.dropsDiscover = false;
  writes.length = 0;
  const opensBefore = (machine.opens ?? []).length;
  await button(ZH["firstRun.pair.startSearch"])?.onclick();
  await flush();
  if (JSON.stringify(saves()) !== JSON.stringify([{ discover: true }])) {
    failures.push(`step 2's second press saved ${JSON.stringify(saves())}, want [{"discover":true}]`);
  }
  if (dropped.state.pairing?.availability !== "on") failures.push(`after step 2's switch the node is ${dropped.state.pairing?.availability}`);
  if (searchBox()) failures.push("the switch stayed on screen over a node that now searches");
  if (!stageText().includes(ZH["firstRun.pair.noCandidates"])) failures.push(`the list did not come back: ${stageText()}`);
  const reopened = (machine.opens ?? []).slice(opensBefore);
  if (reopened.length !== 1 || JSON.stringify(reopened[0]) !== "[0]") {
    failures.push(`the window the restart shut was opened ${JSON.stringify(reopened)}, want once with [0]`);
  }

  // An unsaved edit on the settings page is the owner's: the switch saves
  // discover alone, and an unsaved allowLan is refused and named, not carried.
  const dirty = await start({ ...ready, saved: { ...lan }, sessions: [session("a", "claude")] });
  dirty.state.firstRun.step = 2;
  dirty.state.firstRun.touched = true;
  dirty.render();
  await flush();
  await dirty.loadNodeSettings();
  el("node-allow-lan").checked = false;
  writes.length = 0;
  await button(ZH["firstRun.pair.startSearch"])?.onclick();
  await flush();
  if (saves().length !== 0) failures.push(`step 2's switch carried an unsaved edit along: ${JSON.stringify(saves())}`);
  if (!searchBox() || !textOf(searchBox()).includes(ZH["firstRun.lan.dirty"])) {
    failures.push(`the refused switch does not say why: ${searchBox() ? textOf(searchBox()) : "(no box)"}`);
  }
  if (!shown()) failures.push("the refused switch took the owner out of the wizard");
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
  app.state.pairing = { availability: "on", windowAvailable: true, state: { peerAddress: "192.168.50.10:7463", peerAddressReachable: true } };
  el("first-run-later").onclick();
  el("btn-resume-setup").onclick();
  if (app.state.firstRun.step !== 2) failures.push(`繼續設定 with step 1 done went to step ${app.state.firstRun.step}, want 2`);
  // Step 2's 先跳過 walks on to step 3.
  button(ZH["firstRun.step2.skip"]).onclick();
  if (app.state.firstRun.step !== 3) failures.push("step 2's skip did not go on to step 3");
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
