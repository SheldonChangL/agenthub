// The window says which build it is, in both states.
//
// Every desktop artifact of the first release said 1.0.0 — wails' default —
// and the window itself said nothing at all, so a bug report from a stranger
// could not be mapped to a build. The version now rides on the node line, and
// it has to survive the case that matters most: a node that cannot be reached
// is exactly when someone writes in, and that line is rewritten from scratch
// there.
//
//   node frontend/test/version-line.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;

const failures = [];
const el = (id) => document.getElementById(id);
const noop = async () => ({});

let reachable = true;
const { configure, boot } = await import("../src/app.js");
configure({
  Overview: async () =>
    reachable
      ? { reachable: true, nodeUrl: "http://127.0.0.1:7462", node: { id: "node_local", displayName: "local", platform: "darwin/arm64" }, sessions: [], nodes: [], peers: [], counts: {} }
      : { reachable: false, nodeUrl: "http://127.0.0.1:7462", error: "connection refused", sessions: [], nodes: [], peers: [], counts: {} },
  Discover: noop, SetAudience: noop, TrustNode: noop, RevokeNode: noop, Heartbeat: noop, SetNodeAddress: noop,
  Pairing: async () => ({ availability: "unknown", candidates: [] }), OpenPairing: noop, ClosePairing: noop,
  Inbox: noop, ClearInbox: noop, MCPConfig: noop, CopyText: noop, Outbound: noop, Wakes: noop,
  ServiceStatus: async () => ({ supported: false }), InstallService: noop, UninstallService: noop,
  LocalAddresses: async () => [], NodeSettings: async () => ({ error: "not needed" }), SaveNodeSettings: noop,
  RestartNode: noop, HostPlatform: async () => "darwin",
  Version: async () => ({ release: "0.0.3-rc.test2", goos: "darwin", goarch: "arm64" }),
});

const app = boot({ start: false });
// The binding is a promise; let it land before anything is asserted about it.
await new Promise((resolve) => setTimeout(resolve, 0));

if (app.state.appVersion !== "v0.0.3-rc.test2") {
  failures.push(`the window did not keep the build it was told: ${JSON.stringify(app.state.appVersion)}`);
}

await app.load();
if (!el("node-line").textContent.endsWith("· v0.0.3-rc.test2")) {
  failures.push(`a reachable node line carries no version: ${el("node-line").textContent}`);
}

reachable = false;
await app.load();
const offline = el("node-line").textContent;
if (!offline.includes("無法連線") || !offline.endsWith("· v0.0.3-rc.test2")) {
  failures.push(`an unreachable node line carries no version: ${offline}`);
}

// An unstamped local build says so rather than showing a version nothing made.
if (failures.length === 0) {
  const { configure: configure2, boot: boot2 } = await import("../src/app.js");
  configure2({
    Overview: async () => ({ reachable: true, nodeUrl: "u", node: { id: "n", displayName: "d", platform: "p" }, sessions: [], nodes: [], peers: [], counts: {} }),
    Pairing: async () => ({ availability: "unknown", candidates: [] }),
    ServiceStatus: async () => ({ supported: false }),
    Version: async () => ({ release: "unreleased", goos: "linux", goarch: "amd64" }),
  });
  const plain = boot2({ start: false });
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (plain.state.appVersion !== "unreleased") {
    failures.push(`an unstamped build should say unreleased, not ${JSON.stringify(plain.state.appVersion)}`);
  }
}

if (failures.length) {
  for (const failure of failures) console.error("FAIL:", failure);
  process.exit(1);
}
console.log("ok: the node line names the build, reachable or not");
