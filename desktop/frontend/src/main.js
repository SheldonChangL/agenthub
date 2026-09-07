import "./style.css";
import {
  Overview,
  Discover,
  SetAudience,
  TrustNode,
  RevokeNode,
  Heartbeat,
  Pairing,
  OpenPairing,
  ClosePairing,
} from "../wailsjs/go/main/App";

const state = {
  sessions: [],
  counts: {},
  selected: new Set(),
  search: "",
  filters: { provider: null, status: null, audience: null },
  view: "local",
  nodes: [],
  peers: [],
  presenceError: "",
  selectedNode: null,
  busy: false,
  // pairing is loaded separately from the overview: it changes on its own — a
  // window expires, a machine starts or stops advertising — while the session
  // table does not, and a candidate list that only updates when the owner
  // clicks refresh is one they cannot use to find a machine.
  pairing: null,
  // pairingReadAt is when the countdown below was true. The remaining seconds
  // come from the node, so they are subtracted from the moment they were read
  // rather than compared against this machine's own idea of the expiry.
  pairingReadAt: 0,
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
    renderPairing();
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
  state.peers = overview.peers ?? [];
  state.presenceError = overview.presenceError ?? "";

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

/* ---------------- pairing mode and candidates ---------------- */

// Every field of a candidate was chosen by whoever sent the packet, on a
// multicast group anyone on the segment can write to. So this whole panel is
// built with element(), which assigns textContent, and none of these values is
// ever allowed to decide a class name.
function candidateName(candidate) {
  const name = (candidate.displayName || "").trim();
  return name === "" ? "（未提供名稱）" : name;
}

// remainingSeconds arrives from the node and is then counted down locally from
// the moment it was read. Subtracting this machine's clock from the node's
// expiry instead would show a wrong countdown whenever the two disagree, and
// the honest thing to count is elapsed time, which both clocks agree on.
function pairingRemaining() {
  const state_ = state.pairing?.state;
  if (!state_?.open) return 0;
  const elapsed = Math.floor((Date.now() - state.pairingReadAt) / 1000);
  return Math.max(0, (state_.remainingSeconds ?? 0) - elapsed);
}

function clock(seconds) {
  const minutes = Math.floor(seconds / 60);
  return `${minutes}:${String(seconds % 60).padStart(2, "0")}`;
}

function renderPairing() {
  const statusBox = el("pairing-state");
  const note = el("pairing-note");
  const rows = el("candidate-rows");
  const notice = el("candidate-notice");
  statusBox.replaceChildren();
  rows.replaceChildren();
  notice.textContent = "";

  const pairing = state.pairing;
  const on = el("btn-pairing-on");
  const off = el("btn-pairing-off");

  // Nothing read yet.
  if (!pairing) {
    statusBox.append(element("div", "muted", "正在讀取配對狀態…"));
    on.disabled = true;
    off.disabled = true;
    note.textContent = "";
    return;
  }

  // Three different things to say, and a panel that collapses any two of them
  // tells the owner to wait for something that is not coming, or to change a
  // setting that is not the problem.
  if (pairing.availability === "off") {
    statusBox.append(element("div", "stale",
      "這個節點啟動時沒有 -discover，所以它既不廣播，也看不到別人廣播。" +
      "這不代表同網段沒有人在廣播——這台機器只是沒有在看。"));
    note.textContent = "要使用配對模式，請以 -discover 重新啟動節點，並加上 -allow-lan 與一個本機網段位址的 -peer-listen，" +
      "否則廣播沒有對端可連的位址可以送出。";
    on.disabled = true;
    off.disabled = true;
    return;
  }
  if (pairing.availability !== "on") {
    statusBox.append(element("div", "stale",
      "無法向本機節點取得配對狀態，所以這裡不顯示任何內容。" +
      "這是本機的讀取問題，不代表沒有人在廣播。"));
    if (pairing.error) note.textContent = pairing.error;
    on.disabled = true;
    off.disabled = true;
    return;
  }

  const window_ = pairing.state ?? {};
  const announcing = window_.announcing ?? {};
  on.disabled = state.busy;
  off.disabled = state.busy || !window_.open;

  if (window_.open) {
    const left = pairingRemaining();
    statusBox.append(element("div", "line", `正在廣播，剩 ${clock(left)}`));
    // An open window and a machine that is actually sending packets are
    // different facts. This is the one failure an owner cannot see from the
    // other machine, so it is said here rather than left in a log.
    if ((announcing.announceableAddresses ?? 0) === 0) {
      statusBox.append(element("div", "stale",
        "視窗是開的，但這台機器沒有任何對端可連的位址，所以實際上什麼都沒有送出。"));
      if (announcing.lastError) statusBox.append(element("div", "muted", announcing.lastError));
    } else if (announcing.lastAnnouncedAt) {
      statusBox.append(element("div", "muted",
        `最後一次廣播：${relative(announcing.lastAnnouncedAt)}`));
    }
    note.textContent = "時間到會自動停止。在這段時間內，同網段的人都能看到這台機器在跑 AgentHub，" +
      "以及這個節點的名稱、平台與指紋。";
  } else {
    statusBox.append(element("div", "line", "目前沒有廣播。"));
    if ((announcing.announceableAddresses ?? 0) === 0) {
      statusBox.append(element("div", "stale",
        "這台機器目前沒有對端可連的位址，開啟配對模式也送不出任何廣播。"));
    }
    note.textContent = "開啟後，同網段的人都會知道這台機器在跑 AgentHub，並看到這個節點的名稱、平台與指紋" +
      "（不含公鑰）。這是為了配對而明確接受的取捨，時間到會自動停止。";
  }

  // The list.
  if (pairing.candidatesError) {
    rows.append(element("div", "stale",
      "無法取得候選清單，所以這裡不顯示任何內容。這是本機的讀取問題，不代表沒有人在廣播。"));
    rows.append(element("div", "muted", pairing.candidatesError));
    return;
  }
  const candidates = pairing.candidates ?? [];
  if (candidates.length === 0) {
    rows.append(element("div", "empty", "目前沒有看到任何機器在廣播。"));
  }
  for (const candidate of candidates) {
    rows.append(candidateRow(candidate));
  }
  if (pairing.full) {
    rows.append(element("div", "stale",
      "候選清單已滿。同網段有人可以持續送出封包把清單佔滿，" +
      "所以你要找的機器有可能因此沒有出現，而不是因為它沒在廣播。"));
  }
  // The node's own words about what this list is worth, so the warning here
  // cannot drift from the guarantees the node actually makes.
  if (pairing.notice) notice.textContent = pairing.notice;
}

function candidateRow(candidate) {
  const row = element("div", "candidaterow");
  const line = element("div", "line");
  line.append(element("span", "name", candidateName(candidate)));
  // A flag is how impersonation is visible at all from this side, so it is
  // shown on the row rather than in a detail view someone has to open.
  if (candidate.contested) line.append(pill("身分有爭用", "bad"));
  if (candidate.duplicate) line.append(pill("名稱或指紋重複", "bad"));
  row.append(line);
  row.append(element("div", "meta", `${candidate.platform || "平台未提供"} · ${candidate.address}`));
  // The node id and the fingerprint in full, never a prefix: comparing the
  // first few groups is exactly what a forger can defeat, and these are the two
  // values that decide which machine gets trusted. `ah candidates` prints every
  // field, and #61 asks the two surfaces to agree, so nothing is omitted here
  // either.
  row.append(element("div", "fingerprint", candidate.nodeId));
  row.append(element("div", "fingerprint", candidate.fingerprint));
  row.append(element("div", "muted",
    `首次看到 ${relative(candidate.firstSeen)} · 最後 ${relative(candidate.lastSeen)}`));
  const use = element("button", "ghost", "用這一列開始配對…");
  use.onclick = () => prefillPairFrom(candidate);
  row.append(use);
  return row;
}

// prefillPairFrom copies the announced claims into the pairing form.
//
// It cannot complete a pairing, and it must not look as though it could: the
// announcement carries no public key by design, so the owner still has to get
// that from the other machine and compare the fingerprint there. Prefilling the
// fingerprint saves typing and proves nothing — an announced fingerprint is a
// hint for finding the right machine, and the whole point of comparing one is
// that it comes from the machine itself, not from the network.
function prefillPairFrom(candidate) {
  el("pair-node-id").value = candidate.nodeId;
  el("pair-display-name").value = candidate.displayName || "";
  el("pair-platform").value = candidate.platform || "";
  el("pair-public-key").value = "";
  el("pair-fingerprint").value = "";
  const note = el("pair-prefill-note");
  note.textContent =
    "節點 ID、名稱與平台是從廣播帶進來的，全都是對方自己宣稱的，沒有經過任何驗證。" +
    "公鑰不在廣播內容裡，必須在對方機器上執行 ah node 取得；" +
    `指紋也請看對方螢幕上顯示的那一組，並逐組核對，不要用清單上這一組（${candidate.fingerprint}）當作依據。`;
  note.classList.remove("hidden");
  openPairModal();
}

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
    const presence = presenceFor(node.nodeId);
    const label = presenceLabel(presence);
    const line = element("div", "line");
    line.append(element("span", `dot ${label.className}`), element("span", "name", node.displayName));
    line.append(element("span", `presence ${label.className}`, label.text));
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
  el("node-sessions").replaceChildren(...(selected ? nodeSessions(selected) : []));
}

// nodeSessions renders what one peer has published to this node.
//
// The three cases are deliberately distinct rather than collapsed into an empty
// table. "Never heard from", "went quiet at 10:04", and "reachable and sharing
// nothing" mean different things to an owner, and showing the same empty list
// for all three would hide the difference — in particular it would let a stale
// view pass for a current one, which is what issue #15 asks not to happen.
function nodeSessions(node) {
  const presence = presenceFor(node.nodeId);
  const heading = element("h3", "", "這個節點公開給我的 session");

  // A failure to read presence is a fact about this node, not about the peer.
  // Saying "we have not heard from it" here would turn a transport error into a
  // confident claim that happens to be unfounded.
  if (state.presenceError) {
    return [heading, element(
      "div",
      "stale",
      "無法向本機節點取得對端狀態，所以這裡不顯示任何內容。這是本機的讀取問題，不代表對方離線或沒有公開 session。"
    )];
  }
  if (!heardFrom(presence)) {
    return [heading, element(
      "div",
      "empty",
      "尚未收到這個節點的心跳。配對只確認身分，對方仍須主動發布，而且必須把 session 公開給這個節點。"
    )];
  }
  if (!presence.online) {
    return [heading, element(
      "div",
      "stale",
      `這個節點目前離線，最後一次心跳在${relative(presence.receivedAt)}。` +
      "先前的內容已不再顯示，因為那是過去的狀態，不是現在的。"
    )];
  }
  const sessions = presence.sessions ?? [];
  if (presence.sessionsWithheld) {
    return [heading, element("div", "empty",
      "本機拒絕了這個節點送來的 session 清單——不符合本機接受的規則，" +
      "或其中有不屬於它的 session——因此整份都不顯示。" +
      "這是本機的判斷，不是對方離線；下一次有效的心跳會取代它。")];
  }
  if (sessions.length === 0) {
    return [heading, element("div", "empty", "這個節點線上，但沒有公開任何 session 給我。")];
  }

  const table = element("table", "peer-sessions");
  const head = element("tr");
  for (const title of ["SESSION", "節點", "PROVIDER", "狀態", "最後活動"]) {
    head.append(element("th", "", title));
  }
  const header = element("thead");
  header.append(head);
  table.append(header);
  const body = element("tbody");
  for (const session of sessions) {
    const row = element("tr");
    row.append(
      element("td", "mono", session.id ?? ""),
      element("td", "", node.displayName),
      element("td", "", session.provider ?? ""),
      cell(element("td"), pill(session.status ?? "unknown", statusPillClass(session.status))),
      element("td", "muted", session.lastSeenAt ? relative(session.lastSeenAt) : "—")
    );
    body.append(row);
  }
  table.append(body);
  return [heading, table];
}

function lastSeen(node) {
  return node.lastSeenAt ? `最後聯繫 ${relative(node.lastSeenAt)}` : "尚未聯繫過";
}

/* ---------------- presence ---------------- */

// presenceFor returns what this node currently believes about a peer.
//
// The node lists every trusted peer, heard from or not, so an entry existing
// says nothing on its own. What separates the states is receivedAt: a peer that
// has never sent a heartbeat has no such moment.
function presenceFor(nodeId) {
  return state.peers.find((peer) => peer.nodeId === nodeId) ?? null;
}

// heardFrom reports whether this node has ever received a heartbeat from a peer.
//
// This is the distinction that matters, and it is not "is there an entry": the
// node reports a row for every trusted peer. Keying off the row's existence
// instead would describe a peer that has never spoken as one that went quiet at
// an unknown time, which asserts a heartbeat that never happened.
function heardFrom(presence) {
  return Boolean(presence && presence.receivedAt);
}

// presenceLabel describes a peer's reachability in words, never as a bare dot.
//
// An offline peer must not have its last snapshot rendered as the current
// state, so the label always says when the information is from.
function presenceLabel(presence) {
  if (state.presenceError) return { text: "節點狀態無法取得", className: "unknown" };
  if (!heardFrom(presence)) return { text: "尚未收到心跳", className: "never" };
  if (presence.online) return { text: "線上", className: "online" };
  return { text: `離線 · 資料截至 ${relative(presence.receivedAt)}`, className: "offline" };
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
  // The note describes where the fields came from, so it must not outlive them.
  const note = el("pair-prefill-note");
  note.textContent = "";
  note.classList.add("hidden");
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
    allowOutbound: el("audience-outbound").checked,
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
    // Read on arrival rather than on the next poll: a candidate list that is up
    // to five seconds stale when the view opens is one the owner will read as
    // "nobody is advertising".
    if (state.view === "network") loadPairing().catch(() => {});
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

el("btn-unpublish").onclick = () => applyAudience({ mode: "none", nodes: [], exportCwd: false, acceptMessages: false, allowOutbound: false }, "收回");

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

/* ---------------- pairing panel wiring ---------------- */

// loadPairing is separate from load() because a failure to read the pairing
// state must not blank the session table, and because this one is polled: a
// window expires and machines come and go on their own.
async function loadPairing() {
  try {
    state.pairing = await Pairing();
    state.pairingReadAt = Date.now();
  } catch (error) {
    // A binding that threw is not a fact about the network either.
    state.pairing = { availability: "unknown", candidates: [], error: String(error) };
    state.pairingReadAt = Date.now();
  }
  if (state.view === "network") renderPairing();
}

el("btn-pairing-on").onclick = () =>
  withBusy("開啟配對模式", async () => {
    // No duration: the node's own default is the one the node documents, and
    // sending a number from here would make this window disagree with `ah`.
    await OpenPairing(0);
    await loadPairing();
  });

el("btn-pairing-off").onclick = () =>
  withBusy("停止廣播", async () => {
    await ClosePairing();
    await loadPairing();
  });

// The panel is polled only while it is on screen, and the countdown ticks in
// between so an expiring window is visibly expiring rather than jumping.
setInterval(() => {
  if (state.view !== "network") return;
  loadPairing().catch(() => {});
}, 5000);
setInterval(() => {
  if (state.view !== "network" || !state.pairing?.state?.open) return;
  renderPairing();
}, 1000);

load()
  .then(loadPairing)
  .catch((error) => banner(`載入失敗：${error}`));
