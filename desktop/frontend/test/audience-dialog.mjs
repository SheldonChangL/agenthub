// The audience dialog must not carry a flag from one use to the next.
//
// It applies to whatever is selected and reads its values straight from the
// boxes, so a box left ticked from last time is a setting about to be applied
// to a different set of sessions. That was survivable while the flags only
// governed what could be read; one of them now starts turns in an agent with
// nobody watching.
//
//   node frontend/test/audience-dialog.mjs [path-to-main.js]

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

const failures = [];
const el = (id) => document.getElementById(id);
const noop = async () => ({});
const Overview = async () => ({ sessions: [], nodes: [], node: {} });

const module = new Function(
  "document", "setInterval", "Overview", "Discover", "SetAudience", "TrustNode", "RevokeNode",
  "Heartbeat", "Pairing", "OpenPairing", "ClosePairing",
  source + "\nreturn { openAudienceModal, readAudienceForm };",
)(document, () => 0, Overview, noop, noop, noop, noop, noop,
  async () => ({ availability: "unknown", candidates: [] }), noop, noop);

const flags = ["audience-cwd", "audience-messages", "audience-outbound", "audience-autowake"];

// Somebody opens the dialog and ticks everything, for one session.
module.openAudienceModal();
for (const id of flags) el(id).checked = true;
if (!module.readAudienceForm().autoWake) {
  failures.push("the dialog does not read the auto-wake box at all");
}

// They come back later, for a different session, and open it again.
module.openAudienceModal();
for (const id of flags) {
  if (el(id).checked) {
    failures.push(`${id} was still ticked when the dialog reopened`);
  }
}
for (const [name, value] of Object.entries(module.readAudienceForm())) {
  if (value === true) {
    failures.push(`${name} came back true from a freshly opened dialog`);
  }
}

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("the audience dialog starts every flag off");
