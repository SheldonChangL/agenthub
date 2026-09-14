// Renders hostile provider metadata through the real table renderer and asserts
// that none of it becomes markup.
//
// Provider metadata is untrusted input (docs/architecture.md), and this WebView
// holds live bindings to the Go process, so a working directory or session ID
// containing HTML must render as text. Run with:
//
//   node frontend/test/render-untrusted.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;
const { configure, boot } = await import("../src/app.js");

const noop = async () => ({});
configure({
  Overview: noop, Discover: noop, SetAudience: noop, Heartbeat: noop,
});
const { renderRows } = boot({ start: false });

const hostile = [
  {
    id: 'claude:<img src=x onerror="alert(1)">',
    provider: "claude",
    status: '"><script>steal()</script>',
    management: "unmanaged",
    visibility: "private",
    cwd: '</td><script>window.go.main.App.SetVisibility(["*"],"public")</script>',
    lastSeenAt: new Date().toISOString(),
  },
];

renderRows(hostile);
const html = document.getElementById("rows").serialize();

const failures = [];

// No element may be constructed from provider data. The table's own `</td><td>`
// is legitimate markup, and literal characters such as `onerror=` surviving
// inside escaped text cannot execute, so neither is checked here.
for (const marker of ["<script", "<img", "<iframe", "javascript:"]) {
  if (html.toLowerCase().includes(marker)) failures.push(`hostile metadata produced ${marker}`);
}

// The values must still be visible to the owner, as escaped text.
if (!html.includes("&lt;script&gt;steal()&lt;/script&gt;")) failures.push("hostile status was not rendered as escaped text");
if (!html.includes("&lt;img src=x onerror=&quot;alert(1)&quot;&gt;")) failures.push("hostile session ID was not rendered as escaped text");

// An unrecognized status must not reach a class name.
const pillClass = /class="pill[^"]*"/.exec(html)?.[0] ?? "";
if (pillClass !== 'class="pill"') failures.push(`unknown status leaked into a class name: ${pillClass}`);

if (failures.length > 0) {
  for (const f of failures) console.error("FAIL:", f);
  console.error("\n" + html.slice(0, 400));
  process.exit(1);
}

console.log("ok: hostile provider metadata rendered as text only");
