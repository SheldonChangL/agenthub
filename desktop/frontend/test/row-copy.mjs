// The two copy controls on a row of the Local table.
//
// The owner asked for two things (2026-10-01): the resume button copies the
// session's ID and nothing else — no `claude --resume` in front of it — and
// the working directory can be copied too. Both report in place, on the
// control that was pressed, as every other copy in this window does
// (docs/ui-contract.md §2, CopyText), and both live in a row that the
// fifteen-second tick updates rather than rebuilds (§3.1).
//
//   node frontend/test/row-copy.mjs

import { document } from "./dom-shim.mjs";
import { TEXT as ZH } from "../src/i18n/zh-Hant.js";
import { TEXT as EN } from "../src/i18n/en.js";

globalThis.document = document;
globalThis.setInterval = () => 0;

// The flash ends on a timer; the timers are held here and run by hand, so a
// check can look at the row both during the flash and after it.
const timers = [];
const realSetTimeout = globalThis.setTimeout;
globalThis.setTimeout = (fn, ms, ...rest) => {
  if (ms === 1500) {
    timers.push(fn);
    return timers.length;
  }
  return realSetTimeout(fn, ms, ...rest);
};
const runFlashTimers = () => {
  for (const fn of timers.splice(0)) fn();
};

const { configure, boot } = await import("../src/app.js");

const copied = [];
let refuse = null;
let hold = null;
const noop = async () => ({});
configure({
  Overview: noop, Discover: noop, SetAudience: noop, Heartbeat: noop,
  CopyText: async (text) => {
    if (hold) await hold;
    copied.push(text);
    if (refuse) throw new Error(refuse);
  },
});
const app = boot({ start: false });

const failures = [];
const check = (cond, message) => { if (!cond) failures.push(message); };
const settle = () => new Promise((resolve) => realSetTimeout(resolve, 0));
const el = (id) => document.getElementById(id);

const base = { status: "idle", management: "unmanaged", audience: { mode: "none" }, lastSeenAt: "2026-10-01T00:00:00Z" };
const longPath = "/home/alex/projects/a-very-long-directory-name-that-the-column-clips/agenthub";
const claude = { ...base, provider: "claude", id: "claude:d30366c4-260c-453d-add2-de7fb3d8cdae",
  providerSessionId: "d30366c4-260c-453d-add2-de7fb3d8cdae", cwd: longPath };
const codex = { ...base, provider: "codex", id: "codex:019a2b3c-thread", providerSessionId: "019a2b3c-thread", cwd: "/p/codex" };
// No providerSessionId: the ID is what follows the first colon of the id.
const bare = { ...base, provider: "claude", id: "claude:435f4b1e-7915-45ec-a0ac-272782db1e96", cwd: "" };

const rowOf = (session) => [...el("rows").children].find((tr) => tr.session?.id === session.id);
const partsOf = (session) => rowOf(session)?.sessionParts;

app.renderRows([claude, codex, bare]);
const noticesBefore = app.state.notices.length;

/* ---------------- Copy ID copies the bare ID ---------------- */

{
  check(app.resumeId(claude) === claude.providerSessionId, `resumeId(claude) = ${app.resumeId(claude)}`);
  check(app.resumeId(bare) === "435f4b1e-7915-45ec-a0ac-272782db1e96",
    `a session without providerSessionId falls back to ${JSON.stringify(app.resumeId(bare))}, want what follows the colon`);

  for (const session of [claude, codex, bare]) {
    copied.length = 0;
    partsOf(session).resumeButton.onclick();
    await settle();
    const want = app.resumeId(session);
    check(copied.length === 1 && copied[0] === want,
      `Copy ID on ${session.id} copied ${JSON.stringify(copied)}, want exactly ${JSON.stringify(want)}`);
    check(!/resume|claude|codex|\s/.test(copied[0] ?? ""),
      `Copy ID on ${session.id} copied a command, not the ID: ${JSON.stringify(copied[0])}`);
  }
  runFlashTimers();
}

/* ---------------- the working directory copies whole ---------------- */

{
  copied.length = 0;
  partsOf(claude).cwdButton.onclick();
  await settle();
  check(copied.length === 1 && copied[0] === longPath,
    `the working directory copied ${JSON.stringify(copied)}, want the whole path ${JSON.stringify(longPath)}`);
  runFlashTimers();

  // No directory, nothing to copy: the cell is the plain dash.
  const parts = partsOf(bare);
  check(parts.cwdButton.classList.contains("hidden"), "an empty working directory still shows a copy button");
  check(parts.cwdButton.onclick === null, "an empty working directory's copy button still copies something");
  check(!parts.cwdEmpty.classList.contains("hidden") && parts.cwdEmpty.textContent === "—",
    "an empty working directory does not read 「—」");
  check(partsOf(claude).cwdEmpty.classList.contains("hidden"),
    "a row with a working directory also shows 「—」");
}

/* ---------------- the result is shown in place ---------------- */

{
  const parts = partsOf(claude);
  parts.resumeButton.onclick();
  await settle();
  check(parts.resumeLabel.textContent === ZH["row.copied"],
    `after a copy the Copy ID button reads ${JSON.stringify(parts.resumeLabel.textContent)}, want ${ZH["row.copied"]}`);
  check(parts.resumeButton.getAttribute("aria-label") === ZH["row.copied"],
    "the Copy ID button's accessible name does not say it copied");
  runFlashTimers();
  check(parts.resumeLabel.textContent === ZH["row.copyId"],
    `after the flash the Copy ID button reads ${JSON.stringify(parts.resumeLabel.textContent)}`);
  check(parts.resumeButton.getAttribute("aria-label") === parts.resumeButton.title,
    "after the flash the Copy ID button's accessible name is not its sentence again");

  parts.cwdButton.onclick();
  await settle();
  check(!parts.cwdFlash.classList.contains("hidden") && parts.cwdFlash.textContent === ZH["row.copied"],
    `after a copy the working directory does not say 「已複製 ✓」: ${JSON.stringify(parts.cwdFlash.textContent)}`);
  check(parts.cwdText.textContent === longPath, "the flash replaced the path rather than covering it");
  runFlashTimers();
  check(parts.cwdFlash.classList.contains("hidden"), "the working directory's flash did not end");

  check(app.state.notices.length === noticesBefore,
    `a copy that worked raised ${app.state.notices.length - noticesBefore} toast(s); the result belongs on the control`);
}

/* ---------------- a refused copy says so, with the text to select ---------------- */

{
  const parts = partsOf(codex);
  refuse = "clipboard is busy";
  parts.resumeButton.onclick();
  await settle();
  refuse = null;
  const box = el("copy-fallback");
  check(app.copyFallbackOpen(), "a refused copy opened nothing beside the button");
  const field = box.children.find((child) => child?.tagName === "input");
  check(field?.value === codex.providerSessionId && field?.readOnly === true,
    `the fallback does not hold the ID to select: ${JSON.stringify(field?.value)}`);
  check(document.activeElement === field, "the fallback's field does not have the keyboard, so the ID is not selected");
  check(box.textContent.includes("clipboard is busy") && box.textContent.includes(ZH["row.copyFallbackClose"]),
    `the fallback does not say why, or cannot be closed: ${JSON.stringify(box.textContent)}`);
  check(parts.resumeLabel.textContent === ZH["row.copyId"], "a refused copy flashed 「已複製 ✓」");
  check(app.state.notices.length === noticesBefore, "a refused copy went to a toast instead of beside the button");
  // Even once the keyboard has left its field: the box is drawn against this
  // row, and a tick that re-sorted the table would leave it beside another.
  field?.blur?.();
  check(app.interactionInProgress(), "the open fallback does not hold the tick off the row it is drawn against");
  app.closeCopyFallback();
  check(!app.copyFallbackOpen(), "the fallback did not close");

  // Tab walks the field and Close; past the end it closes the box and gives
  // the keyboard back to the button, as the audience menu does.
  refuse = "clipboard is busy";
  parts.resumeButton.onclick();
  await settle();
  refuse = null;
  {
    const tabBox = el("copy-fallback");
    const [tabField, tabClose] = ["input", "button"].map((tag) => tabBox.children.find((child) => child?.tagName === tag));
    const tab = (shiftKey = false) => app.copyFallbackKey({ key: "Tab", shiftKey, preventDefault() {}, stopPropagation() {} });
    tabField?.focus?.();
    tab();
    check(app.copyFallbackOpen() && document.activeElement === tabClose, "Tab from the field did not move to Close inside the box");
    tab(true);
    check(app.copyFallbackOpen() && document.activeElement === tabField, "Shift+Tab from Close did not move back to the field");
    tab(true);
    check(!app.copyFallbackOpen(), "Shift+Tab out of the box left it open over the page");
    check(document.activeElement === parts.resumeButton, "leaving the box by Tab did not give the keyboard back to the button");
    refuse = "clipboard is busy";
    parts.resumeButton.onclick();
    await settle();
    refuse = null;
    el("copy-fallback").children.find((child) => child?.tagName === "button")?.focus?.();
    tab();
    check(!app.copyFallbackOpen(), "Tab past Close left the box open, holding the tick off");
    check(!app.interactionInProgress(), "the tick is still held off after the box closed by Tab");
  }

  // The working directory's copy, refused, offers the path — as a field's
  // value, which is text whatever it holds.
  const hostile = { ...codex, cwd: '/tmp/<img src=x onerror="alert(1)">' };
  app.renderRows([claude, hostile, bare]);
  refuse = "denied";
  partsOf(hostile).cwdButton.onclick();
  await settle();
  refuse = null;
  const hostileField = el("copy-fallback").children.find((child) => child?.tagName === "input");
  check(copied.at(-1) === hostile.cwd, `the hostile path was not copied as written: ${JSON.stringify(copied.at(-1))}`);
  check(hostileField?.value === hostile.cwd, "the fallback does not hold the hostile path as the field's value");
  check(!el("copy-fallback").serialize().includes("<img"), "the hostile path became markup in the fallback");
  check(!el("rows").serialize().includes("<img"), "the hostile path became markup in the row");
  app.closeCopyFallback();
}

/* ---------------- the tick keeps the buttons ---------------- */

{
  const before = partsOf(claude);
  const tr = rowOf(claude);
  const { resumeButton, cwdButton } = before;
  before.resumeButton.onclick();
  await settle();
  // A tick in the middle of the flash, with a changed directory.
  const moved = { ...claude, cwd: "/home/alex/projects/moved", lastSeenAt: "2026-10-01T00:00:15Z" };
  app.renderRows([moved, codex, bare]);
  const after = partsOf(moved);
  check(rowOf(moved) === tr, "the tick rebuilt the row");
  check(after.resumeButton === resumeButton, "the tick swapped the Copy ID button out from under a press");
  check(after.cwdButton === cwdButton, "the tick swapped the working directory's button");
  check(after.resumeLabel.textContent === ZH["row.copied"], "the tick cut the 「已複製 ✓」 short");
  runFlashTimers();
  copied.length = 0;
  after.cwdButton.onclick();
  await settle();
  check(copied[0] === "/home/alex/projects/moved", `the kept button copies the old directory: ${JSON.stringify(copied)}`);
  runFlashTimers();

  // A directory that goes away and comes back keeps the same button.
  app.renderRows([{ ...moved, cwd: "" }, codex, bare]);
  check(after.cwdButton.classList.contains("hidden") && !after.cwdEmpty.classList.contains("hidden"),
    "a directory that went away still offers a copy");
  app.renderRows([moved, codex, bare]);
  check(partsOf(moved).cwdButton === cwdButton && !cwdButton.classList.contains("hidden"),
    "a directory that came back did not get its button back");
}

/* ---------------- only the last of two quick copies reports ---------------- */

{
  let release;
  hold = new Promise((resolve) => { release = resolve; });
  partsOf(claude).resumeButton.onclick();
  hold = null;
  partsOf(codex).resumeButton.onclick();
  release();
  await settle();
  await settle();
  check(partsOf(claude).resumeLabel.textContent === ZH["row.copyId"], "the earlier of two copies reported");
  check(partsOf(codex).resumeLabel.textContent === ZH["row.copied"], "the later of two copies did not report");
  check(copied.at(-1) === codex.providerSessionId, `the clipboard ends on ${JSON.stringify(copied.at(-1))}, not the last click`);
  runFlashTimers();
}

/* ---------------- a language switch reaches the flash and the tooltips ---------------- */

{
  const parts = partsOf(claude);
  parts.resumeButton.onclick();
  await settle();
  app.setLanguage("en");
  app.repaintFromState();
  check(parts.resumeLabel.textContent === EN["row.copied"], `mid-flash, English reads ${JSON.stringify(parts.resumeLabel.textContent)}`);
  runFlashTimers();
  check(parts.resumeLabel.textContent === EN["row.copyId"], `after the flash, English reads ${JSON.stringify(parts.resumeLabel.textContent)}`);
  check(parts.resumeButton.title.startsWith(EN["row.copyId"]) && parts.resumeButton.title.includes("claude --resume")
    && parts.resumeButton.title.includes(claude.providerSessionId),
    `the English Copy ID tooltip is ${JSON.stringify(parts.resumeButton.title)}`);
  check(partsOf(codex).resumeButton.title.includes("codex resume"),
    `a Codex row's tooltip does not name codex resume: ${JSON.stringify(partsOf(codex).resumeButton.title)}`);
  check(parts.cwdButton.title === EN["row.copyCwdTitle"].replace("{cwd}", rowOf(claude).session.cwd),
    `the English working-directory tooltip is ${JSON.stringify(parts.cwdButton.title)}`);
  app.setLanguage("zh-Hant");
  app.repaintFromState();
}

if (failures.length > 0) {
  for (const f of failures) console.error("FAIL:", f);
  process.exit(1);
}
console.log("row copy: Copy ID copies the bare ID, the directory copies whole, both say so in place, and the tick keeps the buttons");
