// Drives the per-row "MCP 設定" button and the dialog it opens.
//
// The snippet this dialog hands over binds an agent to a session. Getting the
// wrong row's id into it is the failure the button exists to prevent (issue
// #112: an Ubuntu session id was pasted into a config on a mac), and it is
// invisible afterwards — the agent starts, the tools work, and they speak for
// somebody else's session. So the row-to-id path is followed all the way to
// the call, with more than one row on screen.
//
//   node frontend/test/mcp-config.mjs [path-to-main.js]

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { document } from "./dom-shim.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const target = process.argv[2] ?? path.join(here, "..", "src", "main.js");

let source = fs.readFileSync(target, "utf8");
source = source
  .replace(/^import[\s\S]*?;\s*$/m, "")
  .replace(/^import\s+\{[\s\S]*?\}\s+from\s+".*?";\s*$/m, "");
const wiring = source.indexOf("/* ---------------- wiring ---------------- */");
if (wiring > 0) source = source.slice(0, wiring);

const noop = async () => ({});
const calls = [];
let answer = async (sessionId) => ({
  text: `{\n  "mcpServers": {\n    "agenthub": {\n      "command": "/abs/bin/agenthub-mcp",\n      "args": ["-as", ${JSON.stringify(sessionId)}, "-url", "http://127.0.0.1:7462"]\n    }\n  }\n}\n`,
  command: "/abs/bin/agenthub-mcp",
});
const mcpStub = async (sessionId) => {
  calls.push(sessionId);
  return answer(sessionId);
};

// The clipboard is its own binding now, so the window decides what lands there
// and when. Every write is recorded, because a write for the wrong row is the
// half of the bug that leaves the window.
const copied = [];
let copyFails = false;
const copyStub = async (text) => {
  copied.push(text);
  if (copyFails) throw new Error("no clipboard on this display");
};

const scope = new Function(
  "document", "Overview", "Discover", "SetAudience", "TrustNode", "RevokeNode", "Heartbeat",
  "Pairing", "OpenPairing", "ClosePairing", "Inbox", "ClearInbox", "MCPConfig", "CopyText",
  source + "\nreturn { renderRows, openMCPConfig, closeMCPConfig, state };"
)(document, noop, noop, noop, noop, noop, noop, noop, noop, noop, noop, noop, mcpStub, copyStub);

const { renderRows, openMCPConfig, closeMCPConfig } = scope;
const failures = [];
const el = (id) => document.getElementById(id);

function findButtons(node, className, found = []) {
  if (!node || typeof node !== "object") return found;
  if (node.tagName === "button" && (node.className ?? "").split(" ").includes(className)) {
    found.push(node);
  }
  for (const child of node.children ?? []) findButtons(child, className, found);
  return found;
}

// 1. Every row has the button, and it asks about its own row.
scope.state.selected = new Set();
const row = (id) => ({
  id, provider: "claude", status: "idle", management: "unmanaged",
  audience: { mode: "none" }, cwd: "/tmp", lastSeenAt: new Date().toISOString(),
});
renderRows([row("claude:not-this-one"), row("claude:the-one-clicked")]);
const buttons = findButtons(document.getElementById("rows"), "mcp");
if (buttons.length !== 2) {
  failures.push(`rendered ${buttons.length} MCP buttons for two rows, want 2`);
}
if (!findButtons(document.getElementById("rows"), "inbox").length) {
  failures.push("the inbox button disappeared from the row");
}
const button = buttons[1];
if (!button) {
  failures.push("no MCP config button was rendered on a session row");
} else {
  if (button.textContent !== "MCP 設定") {
    failures.push(`the button reads ${JSON.stringify(button.textContent)}`);
  }
  await button.onclick({ stopPropagation() {} });
  if (calls.length !== 1) {
    failures.push(`clicking the button asked the node ${calls.length} times, want 1`);
  } else if (calls[0] !== "claude:the-one-clicked") {
    failures.push(`the button asked about ${calls[0]}, not the row it belongs to`);
  }
  if (el("mcp-modal").classList.contains("hidden")) {
    failures.push("clicking the button did not open the dialog");
  }
}

// 2. The snippet is on screen, as text, and the title names the session.
const shown = el("mcp-text").serialize();
if (!shown.includes("-as") || !shown.includes("claude:the-one-clicked")) {
  failures.push(`the snippet was not displayed: ${shown}`);
}
if (!shown.includes("/abs/bin/agenthub-mcp")) {
  failures.push("the absolute path the config names was not shown");
}
if (!el("mcp-title").textContent.includes("claude:the-one-clicked")) {
  failures.push(`the title does not name the session: ${el("mcp-title").textContent}`);
}
if (!el("mcp-status").textContent.includes("已複製到剪貼簿")) {
  failures.push(`a successful copy was not reported: ${el("mcp-status").textContent}`);
}
if (copied.length !== 1 || !copied[0].includes("claude:the-one-clicked")) {
  failures.push(`the clipboard got ${copied.length} writes: ${JSON.stringify(copied)}`);
}
if (!shown.includes("-url")) {
  failures.push(`the snippet does not pin the node URL: ${shown}`);
}

// 3. A session id is provider metadata. It reaches the snippet through Go's
//    json encoder and the DOM through textContent; neither may produce markup.
await openMCPConfig('claude:<img src=x onerror="alert(1)">');
const hostile = el("mcp-text").serialize() + el("mcp-title").serialize();
for (const marker of ["<img", "<script", 'onerror="', 'src="']) {
  if (hostile.toLowerCase().includes(marker.toLowerCase())) {
    failures.push(`a session id produced ${marker} in the dialog`);
  }
}
if (!hostile.includes("&lt;img")) {
  failures.push("the hostile id was dropped rather than shown escaped");
}

// 4. A clipboard that refused must say so. Reporting "已複製" here sends
//    someone to paste whatever they had copied before into a config file.
answer = async () => ({ text: "{}\n", command: "/abs/bin/agenthub-mcp" });
copyFails = true;
await openMCPConfig("claude:x");
copyFails = false;
const refused = el("mcp-status").textContent;
if (refused.includes("已複製到剪貼簿")) {
  failures.push("a refused clipboard was reported as a successful copy");
}
if (!refused.includes("無法寫入剪貼簿") || !refused.includes("手動複製")) {
  failures.push(`a refused clipboard did not ask for a manual copy: ${refused}`);
}

// 5. A failed call is not an empty snippet. The likeliest one is agenthub-mcp
//    not being found, and the reason says where it looked — in the dialog,
//    because the dialog covers the banner.
answer = async () => { throw new Error("agenthub-mcp was not found (looked: $AGENTHUB_MCP=, PATH)"); };
await openMCPConfig("claude:x");
const failed = el("mcp-status").serialize();
if (!failed.includes("agenthub-mcp was not found")) {
  failures.push(`the reason the config could not be built was not shown: ${failed}`);
}
if (failed.includes("已複製到剪貼簿")) {
  failures.push("a failed call still claimed the snippet was copied");
}
if (el("mcp-text").textContent !== "") {
  failures.push("a failed call left the previous session's snippet on screen");
}

// 6. Closing clears it, so the next row does not open onto the last one's.
answer = async (sessionId) => ({ text: `{"as":"${sessionId}"}`, command: "/abs/bin/agenthub-mcp" });
await openMCPConfig("claude:first");
closeMCPConfig();
if (!el("mcp-modal").classList.contains("hidden")) {
  failures.push("closing did not hide the dialog");
}
if (el("mcp-text").textContent !== "") {
  failures.push("closing left a session's snippet behind");
}

// 8. A reply that arrives late belongs to nobody. Closing the dialog and
//    opening another row while the first call is still in flight used to let
//    the first answer paint its snippet under the second row's title — and,
//    because the copy happened inside the call, hand the owner the other
//    session's config on the clipboard as well.
let releaseFirst;
answer = (sessionId) =>
  sessionId === "claude:slow"
    ? new Promise((resolve) => {
        releaseFirst = () => resolve({ text: `{"as":"claude:slow"}`, command: "/abs/bin/agenthub-mcp" });
      })
    : Promise.resolve({ text: `{"as":"${sessionId}"}`, command: "/abs/bin/agenthub-mcp" });
copied.length = 0;
const slow = openMCPConfig("claude:slow");
closeMCPConfig();
await openMCPConfig("claude:second");
releaseFirst();
await slow;

if (!el("mcp-title").textContent.includes("claude:second")) {
  failures.push(`a late reply took the title: ${el("mcp-title").textContent}`);
}
if (!el("mcp-text").textContent.includes("claude:second")) {
  failures.push(`the dialog shows ${el("mcp-text").textContent}, not the row that is open`);
}
if (el("mcp-text").textContent.includes("claude:slow")) {
  failures.push("a closed row's snippet was painted under another session's name");
}
if (copied.length !== 1) {
  failures.push(`the clipboard got ${copied.length} writes for one open dialog: ${JSON.stringify(copied)}`);
} else if (!copied[0].includes("claude:second")) {
  failures.push(`the clipboard holds ${copied[0]}, not the config the dialog is showing`);
}

// 7. The warnings are in the markup. The snippet is correct and still
//    misleading without them: an MCP config is per-project, so a second Claude
//    Code started in the same directory launches a second agenthub-mcp under
//    this same id (issue #104), and reading is all the config buys until the
//    owner opens the session's outbound gate.
const markup = fs.readFileSync(path.join(here, "..", "index.html"), "utf8");
const card = markup.slice(markup.indexOf('id="mcp-modal"'));
const dialog = card.slice(0, card.indexOf("</div>\n\n"));
for (const phrase of ["per-project", "同一個目錄", "--strict-mcp-config", "--outbound"]) {
  if (!dialog.includes(phrase)) {
    failures.push(`the dialog never mentions ${phrase}`);
  }
}
if (!/<p class="warning">/.test(dialog)) {
  failures.push("the per-project warning is not styled as a warning, so it reads as body text");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("the MCP config button carries its own row's session, and the dialog says what the snippet does not");
