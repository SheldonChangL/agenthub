import "./style.css";
import {
  Overview,
  Discover,
  SetAudience,
  TrustNode,
  RevokeNode,
  Heartbeat,
} from "../wailsjs/go/main/App";

const state = {
  sessions: [],
  counts: {},
  selected: new Set(),
  search: "",
  filters: { provider: null, status: null, audience: null },
  view: "local",
  nodes: [],
  selectedNode: null,
  busy: false,
};

const el = (id) => document.getElementById(id);

/* ---------------- filtering ---------------- */

function visible() {
  const term = state.search.trim().toLowerCase();
  const { provider, status, audience } = state.filters;
  return state.sessions.filter((s) => {
    if (provider && s.provider !== provider) return false;
    if (status && s.status !== status) return false;
    if (audience && (s.audience?.mode ?? "none") !== audience) return false;
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
  { key: "audience", value: "all_paired", label: "所有已配對" },
  { key: "audience", value: "selected", label: "指定節點" },
  { key: "audience", value: "none", label: "不公開" },
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

// describeAudience answers "published to whom" in one cell.
function describeAudience(audience) {
  const mode = audience?.mode ?? "none";
  if (mode === "all_paired") return { text: "所有已配對", published: true };
  if (mode === "selected") {
    const count = audience?.nodes?.length ?? 0;
    return { text: count === 0 ? "指定節點（無）" : `${count} 個節點`, published: count > 0 };
  }
  return { text: "不公開", published: false };
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
    const audience = describeAudience(session.audience);

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
      cell(element("td"), pill(audience.text, audience.published ? "public" : "")),
      cwdCell,
      element("td", "muted", relative(session.lastSeenAt))
    );
    fragment.append(tr);
  }

  body.replaceChildren(fragment);
  el("empty").classList.toggle("hidden", rows.length > 0);
}

function render() {
  const network = state.view === "network";
  el("network-view").classList.toggle("hidden", !network);
  el("local-view").classList.toggle("hidden", network);
  for (const segment of document.querySelectorAll("#view-switch span")) {
    segment.className = segment.dataset.view === state.view ? "on" : "";
  }
  if (network) {
    renderNodes();
  }

  const rows = visible();
  renderChips();
  renderRows(rows);

  const count = state.selected.size;
  el("selection-count").textContent = count ? `已選取 ${count} 個` : "未選取";
  el("btn-audience").disabled = count === 0 || state.busy;
  el("btn-unpublish").disabled = count === 0 || state.busy;

  const allPicked = rows.length > 0 && rows.every((s) => state.selected.has(s.id));
  const box = el("select-all");
  box.checked = allPicked;
  box.indeterminate = !allPicked && rows.some((s) => state.selected.has(s.id));
  el("select-label").textContent = `全選目前篩選結果（${rows.length}）`;

  el("footer-left").textContent =
    `顯示 ${rows.length} / ${state.counts.total ?? 0} 個 session` +
    ` · 所有已配對 ${state.counts.all_paired ?? 0}` +
    ` · 指定節點 ${state.counts.selected ?? 0}` +
    ` · 不公開 ${state.counts.none ?? 0}`;
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
  state.nodes = overview.nodes || [];
  state.counts = overview.counts || {};
  state.localFingerprint = overview.node?.fingerprint || "";
  if (state.selectedNode && !state.nodes.some((node) => node.nodeId === state.selectedNode)) {
    state.selectedNode = null;
  }

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

async function applyAudience(audience, noun) {
  const ids = [...state.selected];
  await withBusy(noun, async () => {
    const result = await SetAudience(ids, audience);
    await load();
    if (result.failed > 0) {
      banner(`${noun}：${result.changed} 個成功、${result.failed} 個失敗 — ${(result.errors || [])[0] || ""}`);
    } else {
      state.selected.clear();
      closeAudienceModal();
      banner(`已${noun} ${result.changed} 個 session。`, true);
    }
  });
}

/* ---------------- network view ---------------- */

function renderNodes() {
  const container = el("node-rows");
  container.replaceChildren();

  if (state.nodes.length === 0) {
    container.append(element("div", "empty", "尚未配對任何節點。"));
    el("node-detail-body").replaceChildren(
      element("div", "empty", "配對一個節點後，這裡會顯示它的身分與最後聯繫時間。")
    );
    return;
  }

  for (const node of state.nodes) {
    const row = element("div", state.selectedNode === node.nodeId ? "noderow on" : "noderow");
    const line = element("div", "line");
    line.append(element("span", "dot"), element("span", "name", node.displayName));
    const meta = element("div", "meta", `${node.platform} · ${lastSeen(node)}`);
    row.append(line, meta);
    row.onclick = () => {
      state.selectedNode = node.nodeId;
      render();
    };
    container.append(row);
  }

  const selected = state.nodes.find((node) => node.nodeId === state.selectedNode);
  el("node-detail-body").replaceChildren(...(selected ? nodeDetail(selected) : [
    element("div", "empty", "選擇左側的節點以檢視詳細資料。"),
  ]));
}

function lastSeen(node) {
  return node.lastSeenAt ? `最後聯繫 ${relative(node.lastSeenAt)}` : "尚未聯繫過";
}

function nodeDetail(node) {
  const heading = element("h2", "", node.displayName);
  const fingerprint = element("div", "fingerprint", node.fingerprint);
  const note = element(
    "p",
    "muted",
    "在對方機器上執行 ah node，確認顯示的指紋與上方逐組相符。不符代表區網上有人冒用這個節點名稱。"
  );

  const rows = [
    ["節點 ID", node.nodeId],
    ["平台", node.platform],
    ["配對時間", node.pairedAt ? relative(node.pairedAt) : "—"],
    ["最後聯繫", node.lastSeenAt ? relative(node.lastSeenAt) : "尚未聯繫過"],
    ["可見的 session", `${grantedCount(node.nodeId)} 個`],
  ].map(([label, value]) => {
    const row = element("div", "detailrow");
    row.append(element("span", "muted", label), element("span", "mono", value));
    return row;
  });

  const revoke = element("button", "btn danger", "撤銷信任");
  revoke.onclick = () => revokeSelected(node);
  const revokeNote = element(
    "p",
    "muted",
    "撤銷會同時移除這個節點持有的所有 session 授權，再次配對不會恢復。"
  );

  return [heading, fingerprint, note, ...rows, element("div", "", ""), revoke, revokeNote];
}

// A node's reach is the owner's real question, so count it rather than making
// them open every session to work it out.
function grantedCount(nodeId) {
  return state.sessions.filter((session) => {
    const audience = session.audience;
    if (!audience) return false;
    if (audience.mode === "all_paired") return true;
    return audience.mode === "selected" && (audience.nodes ?? []).includes(nodeId);
  }).length;
}

async function revokeSelected(node) {
  await withBusy("撤銷", async () => {
    await RevokeNode(node.nodeId);
    state.selectedNode = null;
    await load();
    banner(`已撤銷 ${node.displayName}，並移除它持有的所有授權。`, true);
  });
}

function openPairModal() {
  el("local-fingerprint").textContent = state.localFingerprint || "—";
  el("pair-modal").classList.remove("hidden");
}

function closePairModal() {
  el("pair-modal").classList.add("hidden");
  for (const id of ["pair-node-id", "pair-display-name", "pair-platform", "pair-public-key", "pair-fingerprint"]) {
    el(id).value = "";
  }
}

/* ---------------- audience picker ---------------- */

function selectedMode() {
  const checked = document.querySelector('input[name="audience-mode"]:checked');
  return checked ? checked.value : "none";
}

function syncAudienceForm() {
  el("audience-nodes").classList.toggle("hidden", selectedMode() !== "selected");
}

function openAudienceModal() {
  el("audience-count").textContent = String(state.selected.size);
  el("audience-modal").classList.remove("hidden");
  syncAudienceForm();
}

function closeAudienceModal() {
  el("audience-modal").classList.add("hidden");
}

function readAudienceForm() {
  const mode = selectedMode();
  const nodes =
    mode === "selected"
      ? el("audience-node-input")
          .value.split(/[\s,]+/)
          .map((value) => value.trim())
          .filter(Boolean)
      : [];
  return {
    mode,
    nodes,
    exportCwd: el("audience-cwd").checked,
    acceptMessages: el("audience-messages").checked,
  };
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

for (const segment of document.querySelectorAll("#view-switch span")) {
  segment.onclick = () => {
    state.view = segment.dataset.view;
    render();
  };
}

el("btn-pair").onclick = openPairModal;
el("pair-close").onclick = closePairModal;
el("pair-modal").onclick = (event) => {
  if (event.target === el("pair-modal")) closePairModal();
};
el("pair-submit").onclick = () =>
  withBusy("配對", async () => {
    const node = await TrustNode(
      el("pair-node-id").value.trim(),
      el("pair-display-name").value.trim(),
      el("pair-platform").value.trim(),
      el("pair-public-key").value.trim(),
      el("pair-fingerprint").value.trim()
    );
    closePairModal();
    state.selectedNode = node.nodeId;
    await load();
    banner(`已信任 ${node.displayName}。配對本身不會公開任何 session。`, true);
  });

el("btn-audience").onclick = openAudienceModal;
el("audience-close").onclick = closeAudienceModal;
el("audience-modal").onclick = (event) => {
  if (event.target === el("audience-modal")) closeAudienceModal();
};
for (const radio of document.querySelectorAll('input[name="audience-mode"]')) {
  radio.onchange = syncAudienceForm;
}
el("audience-apply").onclick = () => {
  const audience = readAudienceForm();
  if (audience.mode === "selected" && audience.nodes.length === 0) {
    banner("指定節點需要至少一個節點 ID；要不公開請選「不公開」。");
    return;
  }
  const noun =
    audience.mode === "none" ? "收回" : audience.mode === "all_paired" ? "公開給所有已配對節點" : "公開給指定節點";
  applyAudience(audience, noun);
};

el("btn-unpublish").onclick = () => applyAudience({ mode: "none", nodes: [], exportCwd: false, acceptMessages: false }, "收回");

el("btn-reload").onclick = () => withBusy("重新整理", load);

el("btn-discover").onclick = () =>
  withBusy("掃描", async () => {
    const counts = await Discover();
    await load();
    const skipped = counts.skipped ?? 0;
    const detail = skipped > 0 ? `，另有 ${skipped} 筆無法解析已略過` : "";
    banner(`掃描完成：Claude ${counts.claude}、Codex ${counts.codex}，共 ${counts.total} 個${detail}。`, skipped === 0);
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
