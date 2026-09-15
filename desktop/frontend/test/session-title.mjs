// The SESSION column shows the conversation's name, and falls back to the ID.
//
// A person recognises a session by what their coding agent called the
// conversation, not by a UUID. The ID still has two jobs — resuming a session
// and quoting one — so it stays in the tooltip and in the resume command, and
// it is what a row shows when the provider never named the conversation.
//
//   node frontend/test/session-title.mjs

import { document } from "./dom-shim.mjs";
import { matchesSearch, sorted } from "../src/sessions/filter.js";

globalThis.document = document;
globalThis.setInterval = () => 0;
const { configure, boot } = await import("../src/app.js");

const noop = async () => ({});
configure({ Overview: noop, Discover: noop, SetAudience: noop, Heartbeat: noop });
const { renderRows } = boot({ start: false });

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };

const base = {
  provider: "claude", status: "idle", management: "unmanaged", visibility: "private",
  cwd: "/p/agenthub", lastSeenAt: "2026-09-15T00:00:00Z",
};
const titled = { ...base, id: "claude:d30366c4-260c-453d-add2-de7fb3d8cdae", title: "錄影檔和alert遺失問題" };
const untitled = { ...base, id: "claude:435f4b1e-7915-45ec-a0ac-272782db1e96" };

renderRows([titled, untitled]);
const html = document.getElementById("rows").serialize();

// The named session reads as its name, and its UUID is not in the cell.
check(html.includes("錄影檔和alert遺失問題"), "a titled session does not show its title");
check(!/<b[^>]*>d30366c4/.test(html), "a titled session still shows its UUID as the row label");

// The unnamed one keeps the ID: a row with no handle at all is worse.
check(html.includes("435f4b1e-7915-45ec-a0ac-272782db1e96"), "an untitled session lost its ID");

// The ID stays reachable on a titled row — the tooltip carries both.
check(
  html.includes('title="錄影檔和alert遺失問題\nclaude:d30366c4-260c-453d-add2-de7fb3d8cdae"'),
  "the tooltip does not carry the title and the full ID",
);

// Search reaches the title, because that is now what the row shows.
check(matchesSearch(titled, "錄影檔"), "search does not match a session title");
check(matchesSearch(titled, "d30366c4"), "search stopped matching a session ID");

// The SESSION column sorts by what it displays, mixing names and bare IDs.
const order = sorted([untitled, titled], { key: "id", dir: "asc" }).map((row) => row.id);
check(order[0] === untitled.id, `sorting the SESSION column ignores the displayed label: ${order.join()}`);

if (failures.length > 0) {
  for (const f of failures) console.error("FAIL:", f);
  console.error("\n" + html.slice(0, 600));
  process.exit(1);
}

console.log("ok: the session column shows titles and falls back to IDs");
