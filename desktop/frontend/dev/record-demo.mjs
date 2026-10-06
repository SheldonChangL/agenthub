// Dev-only: makes the README's pictures from dev/mock.html?demo=readme. Not
// part of the build or the tests.
//
// For one language it writes four files into a directory:
//   demo.gif             share a session with a paired machine, see what that
//                        machine shares back, read the notes its agents left
//   first-run.png        the setup's first step
//   network-pairing.png  the pairing drawer with two fingerprints to compare
//   share-panel.png      one session's share panel
// with `.zh-Hant` before the extension for 繁體中文. The data is the mock's
// made-up set, never a real machine's (no real session ids, paths, node ids or
// host names).
//
// Needs a running preview, Playwright and ffmpeg:
//
//   cd desktop/frontend && npx vite --port 5173 --strictPort &
//   PLAYWRIGHT_MODULE=/path/to/node_modules/playwright \
//     node dev/record-demo.mjs en ../../docs/screenshots
//   node dev/record-demo.mjs zh-Hant ../../docs/screenshots
//
// PLAYWRIGHT_MODULE defaults to "playwright" resolved from here; Playwright is
// deliberately not a dependency of this package.

import { createRequire } from "node:module";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const require = createRequire(import.meta.url);
const { chromium } = require(process.env.PLAYWRIGHT_MODULE ?? "playwright");

const lang = process.argv[2] ?? "en";
const outDir = resolve(process.argv[3] ?? ".");
const named = (name) => join(outDir, lang === "en" ? name : name.replace(/\.(\w+)$/, `.${lang}.$1`));
const base = process.env.DEMO_URL ?? "http://localhost:5173";
const url = (extra = "") => `${base}/dev/mock.html?demo=readme&lang=${lang}${extra}`;
// Recorded 1:1, never scaled: scaling blurs the table's small type. 1100px is
// the narrowest window in which nothing the demo shows is cut off or ends in an
// ellipsis (900px, the minimum, shortens the longer titles), and narrower than
// the app's own 1280px default, so the README does not shrink it much.
const W = 1100;
const H = 700;
// A subtitle strip under the window rather than over it, so no caption ever
// covers a toast or a button the pointer is about to press.
const STRIP = 46;
const GIF_WIDTH = W;
const FPS = 10;

// The row and the machine are the mock's data, the same in both languages;
// the captions are what the viewer reads while the pointer moves.
const text = {
  en: {
    row: "Set up UI tests", peer: "ubuntu-lab",
    captions: [
      "Every Claude Code and Codex session on this machine",
      "Share one with a machine you paired",
      "See what that machine shares with you",
      "Read the notes its agents left here",
    ],
  },
  "zh-Hant": {
    row: "Set up UI tests", peer: "ubuntu-lab",
    captions: [
      "這台機器上所有的 Claude Code 與 Codex session",
      "把一個 session 分享給配對過的機器",
      "看那台機器分享給你的 session",
      "讀它的 agent 留給這台的訊息",
    ],
  },
}[lang];
if (!text) throw new Error(`no script for ${lang}`);

const browser = await chromium.launch();

// The three stills: the window alone, at twice the pixels so they stay sharp
// on a high-density screen, with nothing added to the page. 840px is the app's
// default height, and the one at which the pairing card fits whole: at 700px
// either its title or its buttons were cut off.
const STILL_H = 840;
async function still(name, extra, arrange) {
  const context = await browser.newContext({ viewport: { width: W, height: STILL_H }, deviceScaleFactor: 2, colorScheme: "dark" });
  const page = await context.newPage();
  await page.goto(url(extra));
  await arrange(page);
  await page.waitForTimeout(700);
  await page.screenshot({ path: named(name) });
  await context.close();
  console.log(`wrote ${named(name)}`);
}
await still("first-run.png", "&onboarding=fresh", (page) => page.waitForSelector("#first-run:not(.hidden) button.primary"));
await still("network-pairing.png", "", async (page) => {
  await page.waitForSelector("tbody tr .audbtn");
  await page.locator('#view-switch [data-view="network"]').click();
  await page.locator("#btn-pair").click();
  await page.waitForSelector("#pairing-modal:not(.hidden) .pairrow.comparing");
  // The whole card, title to buttons: the picture is about deciding.
  await page.locator("#pairing-modal .pairrow.comparing").first().scrollIntoViewIfNeeded();
});
await still("share-panel.png", "", async (page) => {
  await page.waitForSelector("tbody tr .audbtn");
  await page.locator("tbody tr").first().locator("button.audbtn").click();
  await page.waitForSelector("#audience-modal:not(.hidden)");
});

const frameDir = mkdtempSync(join(tmpdir(), "agenthub-demo-"));
const context = await browser.newContext({ viewport: { width: W, height: H + STRIP }, colorScheme: "dark" });
const page = await context.newPage();
await page.goto(url());
await page.waitForSelector("tbody tr .audbtn");
await page.waitForTimeout(600);

// A pointer the viewer can follow. Playwright's video has none, and a demo
// where things open by themselves teaches nothing about where to press.
await page.evaluate(({ x, y, H, STRIP }) => {
  const style = document.createElement("style");
  style.textContent = `
    #demo-cursor { position: fixed; left: 0; top: 0; z-index: 2147483647; pointer-events: none;
      width: 22px; height: 22px; transform: translate(${x}px, ${y}px);
      transition: transform 650ms cubic-bezier(.3,.7,.2,1); filter: drop-shadow(0 1px 2px rgba(0,0,0,.6)); }
    #demo-cursor.press::after { content: ""; position: absolute; left: -11px; top: -11px; width: 22px; height: 22px;
      border-radius: 50%; border: 2px solid rgba(110, 231, 160, .9); animation: demo-ring 420ms ease-out forwards; }
    @keyframes demo-ring { from { transform: scale(.4); opacity: 1 } to { transform: scale(1.6); opacity: 0 } }
    /* The window keeps the top ${H}px: a transform makes body the box its
       fixed drawers, dialogs and toasts are laid out in, so they stop where
       the window would end. The strip is outside body, on <html>. */
    body { height: ${H}px !important; overflow: hidden; transform: translateZ(0); }
    #demo-caption { position: fixed; left: 0; right: 0; bottom: 0; height: ${STRIP}px; z-index: 2147483646;
      display: flex; align-items: center; justify-content: center; pointer-events: none;
      background: #0c110f; border-top: 1px solid rgba(110, 231, 160, .35); color: #eef5f0;
      font: 600 15px/1.3 -apple-system, "PingFang TC", "Noto Sans TC", system-ui, sans-serif; }`;
  document.head.append(style);
  const cursor = document.createElement("div");
  cursor.id = "demo-cursor";
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  for (const [name, value] of [["width", "22"], ["height", "22"], ["viewBox", "0 0 22 22"]]) svg.setAttribute(name, value);
  const arrow = document.createElementNS(ns, "path");
  for (const [name, value] of [["d", "M3 2l14 8.2-6.1 1.3 3.6 7.1-2.6 1.3-3.6-7.1L3.8 17z"], ["fill", "#fff"],
    ["stroke", "#111"], ["stroke-width", "1.2"], ["stroke-linejoin", "round"]]) arrow.setAttribute(name, value);
  svg.append(arrow);
  cursor.append(svg);
  const caption = document.createElement("div");
  caption.id = "demo-caption";
  document.body.append(cursor);
  document.documentElement.append(caption);
}, { x: W * 0.55, y: H * 0.55, H, STRIP });
// Lossless frames from Chrome's screencast, not Playwright's video: that one is
// lossy VP8, and its noise differs in every frame, so the GIF could reuse no
// pixel and came out at 4-5 MB. A screencast also sends a frame only when the
// page changed, and each one carries the time it was painted.
const frames = [];
const cdp = await context.newCDPSession(page);
cdp.on("Page.screencastFrame", ({ data, metadata, sessionId }) => {
  const file = join(frameDir, `f${String(frames.length).padStart(5, "0")}.png`);
  writeFileSync(file, Buffer.from(data, "base64"));
  frames.push({ file, at: metadata.timestamp });
  cdp.send("Page.screencastFrameAck", { sessionId }).catch(() => {});
});
await cdp.send("Page.startScreencast", { format: "png", maxWidth: W, maxHeight: H + STRIP, everyNthFrame: 1 });

async function caption(index) {
  // A cut, not a fade: every frame of a fade repaints the whole strip.
  await page.evaluate((words) => {
    document.getElementById("demo-caption").textContent = words;
  }, text.captions[index]);
}

async function pointAt(locator) {
  await locator.scrollIntoViewIfNeeded();
  const box = await locator.boundingBox();
  if (!box) throw new Error(`nothing to point at: ${locator}`);
  const x = box.x + Math.min(box.width / 2, 40);
  const y = box.y + box.height / 2;
  await page.evaluate(({ x, y }) => {
    document.getElementById("demo-cursor").style.transform = `translate(${x}px, ${y}px)`;
  }, { x, y });
  await page.waitForTimeout(750);
}

async function press(locator, after = 900) {
  await pointAt(locator);
  await page.evaluate(() => {
    const cursor = document.getElementById("demo-cursor");
    cursor.classList.remove("press");
    void cursor.offsetWidth;
    cursor.classList.add("press");
  });
  await page.waitForTimeout(180);
  await locator.click();
  await page.waitForTimeout(after);
}

// 1. The table: every session on this machine, and who can see each one.
await caption(0);
await page.waitForTimeout(2200);

// 2. Share one of them with one paired machine.
await caption(1);
const row = page.locator("tbody tr", { hasText: text.row });
await press(row.locator("button.audbtn"), 1000);
await press(page.locator('input[name="share-who"][value="selected"]'), 600);
await press(page.locator("#audience-node-list label", { hasText: text.peer }).locator("input"), 500);
await press(page.locator('input[name="share-what"][value="messages"]'), 500);
await press(page.locator("#audience-apply"), 1800);

// 3. The toast says what the other machine sees now; go and look.
await caption(2);
await press(page.locator(".toast button", { hasText: text.peer }), 1200);
// Straight to the list of what it shares: the address book above it is for
// another day.
await page.locator("#node-sessions").evaluate((node) => node.scrollIntoView({ block: "start" }));
await page.waitForTimeout(2800);

// 4. Back here, the notes that machine's agents left for this one.
await press(page.locator('#view-switch [data-view="local"]'), 300);
await caption(3);
await page.waitForTimeout(400);
await press(page.locator("tbody tr").first().locator("button.inbox"), 3000);

const ended = Date.now() / 1000;
await cdp.send("Page.stopScreencast");
await context.close();
await browser.close();

// Each frame lasts until the next one was painted; the last until the end.
const list = frames.map((frame, i) => {
  const until = i + 1 < frames.length ? frames[i + 1].at : ended;
  return `file '${frame.file}'\nduration ${Math.max(until - frame.at, 0.01).toFixed(3)}`;
});
list.push(`file '${frames.at(-1).file}'`);
const listFile = join(frameDir, "frames.txt");
writeFileSync(listFile, list.join("\n") + "\n");
const filters = `fps=${FPS},scale=${GIF_WIDTH}:-1:flags=lanczos,split[a][b];` +
  "[a]palettegen=max_colors=64:stats_mode=diff[p];[b][p]paletteuse=dither=none:diff_mode=rectangle";
execFileSync("ffmpeg", ["-y", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", listFile,
  "-vf", filters, "-loop", "0", named("demo.gif")], { stdio: "inherit" });
rmSync(frameDir, { recursive: true, force: true });
console.log(`wrote ${named("demo.gif")} from ${frames.length} frames`);
