import "./style.css";
import {
  Overview,
  Discover,
  SetVisibility,
  Heartbeat,
} from "../wailsjs/go/main/App";

const state = {
  sessions: [],
  counts: {},
  selected: new Set(),
  search: "",
  filters: { provider: null, status: null, visibility: null },
  busy: false,
};

const el = (id) => document.getElementById(id);

/* ---------------- filtering ---------------- */

function visible() {
  const term = state.search.trim().toLowerCase();
  const { provider, status, visibility } = state.filters;
  return state.sessions.filter((s) => {
    if (provider && s.provider !== provider) return false;
    if (status && s.status !== status) return false;
    if (visibility && s.visibility !== visibility) return false;
    if (term) {
      const haystack = `${s.id} ${s.cwd || ""}`.toLowerCase();
      if (!haystack.includes(term)) return false;
    }
    return true;
  });
}

/* ---------------- rendering ---------------- */

/* ---------------- DOM helpers ---------------- */

// Provider metadata is untrusted input (docs/architecture.md). Every value that
// originates from a provider reaches the DOM as text, never as markup, so a
// working directory or session ID containing HTML cannot execute in the app.
function element(tag, className = "", text = "") {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== "") node.textContent = text;
  return node;
}

function cell(td, ...children) {
  td.append(...children);
  return td;
}

// Only a fixed set of statuses earns a color class, so an unexpected status
// string can never inject a class name.
function statusPillClass(status) {
  return status === "active" || status === "idle" ? status : "";
}

function pill(text, extraClass) {
  return element("span", extraClass ? `pill ${extraClass}` : "pill", text);
}

const CHIPS = [
  { key: "provider", value: "claude", label: "Claude" },
  { key: "provider", value: "codex", label: "Codex" },
  { key: "status", value: "active", label: "active" },
  { key: "status", value: "idle", label: "idle" },
  { key: "status", value: "inactive", label: "inactive" },
  { key: "visibility", value: "public", label: "已公開" },
  { key: "visibility", value: "private", label: "私密" },
];

function renderChips() {
  const container = el("chips");
  container.replaceChildren();
  for (const chip of CHIPS) {
    const button = document.createElement("button");
    const on = state.filters[chip.key] === chip.value;
    button.className = on ? "chip on" : "chip";
    button.append(chip.label, element("span", "n", String(state.counts[chip.value] ?? 0)));
    button.onclick = () => {
      state.filters[chip.key] = on ? null : chip.value;
      render();
    };
    container.append(button);
  }
}

function shortId(id) {
  const [provider, rest = ""] = id.split(":");
  return { provider, rest };
}

function relative(iso) {
  const then = new Date(iso).getTime();
  if (!Number.isFinite(then)) return "—";
  const seconds = Math.max(0, (Date.now() - then) / 1000);
  if (seconds < 60) return `${Math.floor(seconds)} 秒前`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分鐘前`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小時前`;
  return `${Math.floor(seconds / 86400)} 天前`;
}

function renderRows(rows) {
  const body = el("rows");
  const fragment = document.createDocumentFragment();

  for (const session of rows) {
    const tr = document.createElement("tr");
    const picked = state.selected.has(session.id);
    if (picked) tr.className = "sel";

    const { rest } = shortId(session.id);
    const isPublic = session.visibility === "public";

    const checkCell = element("td", "col-check");
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.checked = picked;
    checkbox.onchange = (event) => {
      if (event.target.checked) state.selected.add(session.id);
      else state.selected.delete(session.id);
      render();
    };
    checkCell.append(checkbox);

    const idCell = element("td", "mono sid");
    idCell.append(element("b", "", rest));

    const cwdCell = element("td", "mono muted", session.cwd || "—");
    if (session.cwd) cwdCell.title = session.cwd;

    tr.append(
      checkCell,
      idCell,
      element("td", "", session.provider),
      cell(element("td"), pill(session.status, statusPillClass(session.status))),
      element("td", "muted", session.management),
      cell(element("td"), pill(isPublic ? "公開" : "私密", isPublic ? "public" : "")),
      cwdCell,
      element("td", "muted", relative(session.lastSeenAt))
    );
    fragment.append(tr);
  }

  body.replaceChildren(fragment);
  el("empty").classList.toggle("hidden", rows.length > 0);
}

function render() {
  const rows = visible();
  renderChips();
  renderRows(rows);

  const count = state.selected.size;
  el("selection-count").textContent = count ? `已選取 ${count} 個` : "未選取";
  el("btn-publish").disabled = count === 0 || state.busy;
  el("btn-unpublish").disabled = count === 0 || state.busy;

  const allPicked = rows.length > 0 && rows.every((s) => state.selected.has(s.id));
  const box = el("select-all");
  box.checked = allPicked;
  box.indeterminate = !allPicked && rows.some((s) => state.selected.has(s.id));
  el("select-label").textContent = `全選目前篩選結果（${rows.length}）`;

  el("footer-left").textContent =
    `顯示 ${rows.length} / ${state.counts.total ?? 0} 個 session` +
    ` · 公開 ${state.counts.public ?? 0} · 私密 ${state.counts.private ?? 0}`;
}

/* ---------------- banner ---------------- */

let bannerTimer = null;
function banner(message, ok = false) {
  const node = el("banner");
  node.textContent = message;
  node.className = ok ? "banner ok" : "banner";
  clearTimeout(bannerTimer);
  if (ok) bannerTimer = setTimeout(() => node.classList.add("hidden"), 4000);
}

function hideBanner() {
  el("banner").classList.add("hidden");
}

/* ---------------- data ---------------- */

async function load() {
  const overview = await Overview();
  state.sessions = overview.sessions || [];
  state.counts = overview.counts || {};

  el("conn-dot").className = overview.reachable ? "dot ok" : "dot bad";
  el("node-line").textContent = overview.reachable
    ? `${overview.node.displayName} · ${overview.node.platform} · ${overview.nodeUrl}`
    : `無法連線到 ${overview.nodeUrl}`;
  el("footer-right").textContent = overview.reachable ? overview.node.id : "";

  if (!overview.reachable) {
    banner(`節點未連線：${overview.error || "unknown error"}。請先啟動 agenthub-node。`);
  } else {
    hideBanner();
  }

  // Drop selections that no longer exist after a rescan.
  const alive = new Set(state.sessions.map((s) => s.id));
  for (const id of [...state.selected]) if (!alive.has(id)) state.selected.delete(id);

  render();
}

async function withBusy(label, fn) {
  state.busy = true;
  render();
  try {
    await fn();
  } catch (error) {
    banner(`${label}失敗：${error}`);
  } finally {
    state.busy = false;
    render();
  }
}

async function applyVisibility(visibility) {
  const ids = [...state.selected];
  const noun = visibility === "public" ? "公開" : "收回";
  await withBusy(noun, async () => {
    const result = await SetVisibility(ids, visibility);
    await load();
    if (result.failed > 0) {
      banner(`${noun} ${result.changed} 個成功、${result.failed} 個失敗：${(result.errors || [])[0] || ""}`);
    } else {
      state.selected.clear();
      banner(`已${noun} ${result.changed} 個 session。`, true);
    }
  });
}

/* ---------------- wiring ---------------- */

el("search").oninput = (event) => {
  state.search = event.target.value;
  render();
};

el("select-all").onchange = (event) => {
  const rows = visible();
  if (event.target.checked) rows.forEach((s) => state.selected.add(s.id));
  else rows.forEach((s) => state.selected.delete(s.id));
  render();
};

el("btn-publish").onclick = () => applyVisibility("public");
el("btn-unpublish").onclick = () => applyVisibility("private");

el("btn-reload").onclick = () => withBusy("重新整理", load);

el("btn-discover").onclick = () =>
  withBusy("掃描", async () => {
    const counts = await Discover();
    await load();
    banner(`掃描完成：Claude ${counts.claude}、Codex ${counts.codex}，共 ${counts.total} 個。`, true);
  });

el("btn-heartbeat").onclick = () =>
  withBusy("讀取 heartbeat", async () => {
    el("modal-body").textContent = await Heartbeat();
    el("modal").classList.remove("hidden");
  });

el("modal-close").onclick = () => el("modal").classList.add("hidden");
el("modal").onclick = (event) => {
  if (event.target === el("modal")) el("modal").classList.add("hidden");
};

load().catch((error) => banner(`載入失敗：${error}`));
