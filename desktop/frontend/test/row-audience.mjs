// The SHARING cell of a row in the Local table: the pill, and the icons after
// it that say what a peer may do with the session.
//
// The icons are shown only on a row some peer can see. "Some peer" is
// describeAudience's `published`: mode all_paired, or selected with at least
// one node. 「指定：無」 — selected with no nodes — is as unshared as 未分享: it
// shows the same pill and no icons (#194, and the share panel).
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
  return !tr.sessionParts.shareIcons.classList.contains("hidden");
};
const want = { "claude:none": false, "claude:chosen-none": false, "claude:chosen-one": true, "claude:all": true };
for (const [id, visible] of Object.entries(want)) {
  const got = shown(id);
  if (got !== null && got !== visible) {
    failures.push(`${id}: the icons are ${got ? "shown" : "hidden"}, want ${visible ? "shown" : "hidden"}`);
  }
}

// The table says "every paired machine" in its short form, because the long
// one was cut to 「Every paired ma」 in the 900px window (#194); the tooltip
// says who can see it, and the filter chip — which has room — keeps the whole
// phrase.
{
  const { TEXT: ZH } = await import("../src/i18n/zh-Hant.js");
  const { TEXT: EN } = await import("../src/i18n/en.js");
  const parts = (id) => document.getElementById("rows").children
    .find((row) => row.sessionParts?.idCell?.title?.includes(id))?.sessionParts;
  const pill = parts("claude:all")?.audiencePill;
  if (pill?.textContent !== ZH["audience.cell.allPairedShort"]) failures.push(`the all-paired cell reads ${pill?.textContent}`);
  if (pill?.title !== ZH["share.who.allTitle"]) failures.push(`the all-paired cell's tooltip is ${pill?.title}`);
  if (EN["audience.cell.allPairedShort"].length >= EN["audience.cell.allPaired"].length) {
    failures.push("the English short form is not shorter than the phrase it stands in for");
  }
  const { CHIPS } = await import("../src/sessions/filter.js");
  const chip = CHIPS.find((c) => c.group === "audience" && c.value === "all_paired");
  if (chip?.labelKey !== "audience.cell.allPaired") failures.push(`the filter chip lost the whole phrase: ${chip?.labelKey}`);

  // 「指定：無」 never had its own wording: it reads 未分享, like none.
  const emptyChosen = parts("claude:chosen-none")?.audiencePill;
  if (emptyChosen?.textContent !== ZH["audience.cell.none"]) failures.push(`a chosen-none session's cell reads ${emptyChosen?.textContent}, want ${ZH["audience.cell.none"]}`);
  if (emptyChosen?.classList.contains("public")) failures.push("a chosen-none session's cell is drawn as shared");
  const one = parts("claude:chosen-one")?.audiencePill;
  if (one?.textContent !== "1 台機器") failures.push(`a one-machine cell reads ${one?.textContent}`);

  // What the icons say. The fixture has accept on and outbound off, so the
  // messages icon says they can leave messages and this session cannot reply.
  for (const id of ["claude:chosen-one", "claude:all"]) {
    const row = parts(id);
    if (row.iconMsg.classList.contains("hidden")) failures.push(`${id}: the messages icon is hidden over a session that takes messages`);
    if (row.iconMsg.title !== ZH["share.icon.messagesNoReply"]) failures.push(`${id}: the messages icon says ${row.iconMsg.title}`);
    if (row.iconCwd.classList.contains("hidden") || row.iconCwd.title !== ZH["share.icon.cwd"]) failures.push(`${id}: the working-directory icon is wrong`);
    if (!row.iconWake.classList.contains("hidden")) failures.push(`${id}: the wake icon is shown over a session that is not set to wake`);
  }
  // Titles and aria-labels are written by the update pass, so a language
  // switch reaches them.
  const row = parts("claude:all");
  if (row.iconMsg.getAttribute("aria-label") !== row.iconMsg.title) failures.push("the messages icon's aria-label is not its title");
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("row audience: icons only on a row some peer can see; the all-paired cell is short; a chosen-none session reads 未分享");
