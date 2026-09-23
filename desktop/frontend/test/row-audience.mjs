// The AUDIENCE and FLAGS cells of a row in the Local table.
//
// FLAGS say what a peer may do with a session, so they are shown only on a
// row some peer can see. "Some peer" is describeAudience's `published`: mode
// all_paired, or selected with at least one node. 「指定：無」 — selected with
// no nodes — is as unpublished as 不公開 and used to show four chips (#194).
//
//   node frontend/test/row-audience.mjs

import { document } from "./dom-shim.mjs";

globalThis.document = document;
globalThis.setInterval = () => 0;
const { configure, boot } = await import("../src/app.js");

const noop = async () => ({});
configure({ Overview: noop, Discover: noop, SetAudience: noop, Heartbeat: noop });
const app = boot({ start: false });

const failures = [];
const base = { provider: "claude", status: "idle", management: "unmanaged", cwd: "/p", lastSeenAt: "2026-09-15T00:00:00Z" };
const flags = { exportCwd: true, acceptMessages: true, allowOutbound: false, autoWake: false };
const rows = [
  { ...base, id: "claude:none", audience: { mode: "none", nodes: [], ...flags } },
  { ...base, id: "claude:chosen-none", audience: { mode: "selected", nodes: [], ...flags } },
  { ...base, id: "claude:chosen-one", audience: { mode: "selected", nodes: ["node_a"], ...flags } },
  { ...base, id: "claude:all", audience: { mode: "all_paired", nodes: [], ...flags } },
];
app.renderRows(rows);
const shown = (id) => {
  const tr = document.getElementById("rows").children.find((row) => row.sessionParts?.idCell?.title?.includes(id));
  if (!tr) {
    failures.push(`no row was drawn for ${id}`);
    return null;
  }
  return !tr.sessionParts.chips.classList.contains("hidden");
};
const want = { "claude:none": false, "claude:chosen-none": false, "claude:chosen-one": true, "claude:all": true };
for (const [id, visible] of Object.entries(want)) {
  const got = shown(id);
  if (got !== null && got !== visible) {
    failures.push(`${id}: FLAGS ${got ? "shown" : "hidden"}, want ${visible ? "shown" : "hidden"}`);
  }
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("row audience: FLAGS only on a row some peer can see");
