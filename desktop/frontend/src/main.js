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
  Inbox,
  ClearInbox,
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
  // inboxSession is whose inbox the modal is showing, so Clear knows what it
  // would empty and a refresh knows what to re-read.
  inboxSession: null,
  localNodeId: "",
  localName: "",
  localNameIsChosen: false,
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
    // Opening an inbox is a read, and a per-row button is where an owner looks
    // for "what has this session been sent". It does not mark anything read and
    // does not hand anything to an agent.
    const openInboxButton = element("button", "ghost inbox", "收件匣");
    openInboxButton.onclick = () => {
      openInbox(session.id).catch((error) => banner(`讀取收件匣失敗：${error}`));
    };
    idCell.append(openInboxButton);

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
  // Needed to tell this machine's own messages from a peer's. Without it a
  // qualified sender naming this node reads as a peer, and a bare one reads as
  // local — which is the dangerous direction.
  state.localNodeId = overview.node?.id || "";
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
// the moment it was read.
//
// Elapsed time rather than a comparison against the node's expiry: the two
// clocks can disagree, and elapsed time is the one thing they agree on.
// performance.now() rather than Date.now() because it does not step — a wall
// clock corrected backwards mid-window would otherwise inflate the countdown
// until the next poll.
function pairingRemaining() {
  const window_ = state.pairing?.state;
  if (!window_?.open) return 0;
  const elapsed = Math.floor((performance.now() - state.pairingReadAt) / 1000);
  return Math.max(0, (window_.remainingSeconds ?? 0) - elapsed);
}

function clock(seconds) {
  const minutes = Math.floor(seconds / 60);
  return `${minutes}:${String(seconds % 60).padStart(2, "0")}`;
}

// renderPairing redraws the whole panel, and is called when a read returns —
// not on the countdown's tick. Rebuilding the rows every second would replace
// the element under the pointer between an owner deciding to click a row and
// clicking it, and if a row above expired in that second they would click a
// different machine.
function renderPairing() {
  renderPairingWindow();
  renderCandidates();
}

// broadcastWarning says what actually goes on the wire, with the name spelled
// out.
//
// "the node's name" is a category, and a person cannot judge a category. The
// name is this machine's hostname, which is often a person's name and an
// employer's domain — and on macOS with no HostName set it is whatever DHCP and
// DNS call the address, which may be a previous occupant's. Nobody discovers
// that from an abstract warning; they discover it by reading their own name off
// a stranger's screen.
function broadcastWarning(lead) {
  const name = state.localName || "（未知）";
  // Not "來自 hostname": on macOS it is ComputerName, and the hostname being
  // the wrong source is the reason this reads the way it does. And not "read
  // from this machine" unconditionally — follow the instruction below and that
  // sentence becomes false, which is the same defect one level down.
  const origin = state.localNameIsChosen
    ? "這個名稱是指定的；"
    : "這個名稱是節點從這台機器讀來的；";
  return [
    element("span", "", lead + "同網段的人都會知道這台機器在跑 AgentHub，並看到它自稱 "),
    element("span", "claimed", name),
    element("span", "",
      `，以及平台與指紋（不含公鑰）。${origin}要換掉就用 -display-name 重新啟動節點。`),
  ];
}

function renderPairingWindow() {
  const headline = el("pairing-headline");
  const detail = el("pairing-detail");
  const note = el("pairing-note");
  detail.replaceChildren();
  tickCountdown();

  const pairing = state.pairing;
  const on = el("btn-pairing-on");
  const off = el("btn-pairing-off");

  if (!pairing) {
    headline.textContent = "正在讀取配對狀態…";
    on.disabled = true;
    off.disabled = true;
    note.textContent = "";
    return;
  }

  // Three different things to say, and a panel that collapses any two of them
  // tells the owner to wait for something that is not coming, or to change a
  // setting that is not the problem.
  if (pairing.availability === "off") {
    headline.textContent = "這台機器沒有在看，也不會廣播。";
    detail.append(element("div", "stale",
      "這個節點啟動時沒有 -discover，所以它既不廣播，也看不到別人廣播。" +
      "這不代表同網段沒有人在廣播——這台機器只是沒有在看。"));
    note.textContent = "要使用配對模式，請以 -discover 重新啟動節點；還需要 -allow-lan 與一個本機網段位址的 " +
      "-peer-listen，因為廣播帶的就是 peer listener 綁定的那個位址，而對端只接受「位址與來源相符」的廣播。";
    on.disabled = true;
    off.disabled = true;
    return;
  }
  if (pairing.availability !== "on") {
    headline.textContent = "配對狀態讀不到。";
    detail.append(element("div", "stale",
      "無法向本機節點取得配對狀態，所以這裡不顯示任何內容。" +
      "這是本機的讀取問題，不代表沒有人在廣播。"));
    note.textContent = pairing.error || "";
    on.disabled = true;
    off.disabled = true;
    return;
  }

  const window_ = pairing.state ?? {};
  const announcing = window_.announcing ?? {};
  // The node refuses to open a window it could not announce on — the same
  // condition, checked there. Leaving the button live would invite the owner to
  // press it and read a 409 to learn what this panel already knows.
  const canAnnounce = (announcing.announceableAddresses ?? 0) > 0;
  on.disabled = state.busy || !canAnnounce;
  off.disabled = state.busy || !window_.open;

  if (window_.open) {
    const left = pairingRemaining();
    // The window and the announcing are two lines because they are two facts.
    // Saying "正在廣播" for an open window asserts the second from the first,
    // and the whole point of carrying `announcing` is that it does not follow.
    // The node closes the window itself; this machine only knows the count
    // reached zero. Saying so beats counting "剩 0:00" until the next read.
    headline.textContent = left === 0 ? "配對視窗已到期，正在向節點確認…" : "配對視窗開啟中";
    detail.append(announceLine(announcing));
    note.replaceChildren(...broadcastWarning("時間到會自動停止。在這段時間內，"));
  } else if (!canAnnounce) {
    // Not "opening it would achieve nothing" — the node will not open it. Two
    // different sentences, and the earlier one sat directly above a note
    // promising what opening would reveal, which contradicted it.
    headline.textContent = "配對視窗無法開啟。";
    detail.append(element("div", "stale",
      "這台機器沒有任何可以廣播的位址，節點會拒絕開啟配對模式。"));
    detail.append(element("div", "muted", announcing.lastError || "節點沒有說明原因。"));
    note.textContent = "修正後重新啟動節點，這裡就會可以開啟。在那之前仍可用 ah pair 手動配對。";
  } else {
    headline.textContent = "配對視窗未開啟。";
    note.replaceChildren(...broadcastWarning("開啟後，"));
  }
}

// announceLine says what the announce loop actually managed to do.
//
// This is the one failure an owner cannot see from the other machine: the
// window is open, this panel looks fine, and the other machine waits for a
// candidate that never arrives. So a failure is shown whenever the node reports
// one, not only when there is no address — a node with an address whose every
// send fails is exactly as silent.
function announceLine(announcing) {
  if ((announcing.announceableAddresses ?? 0) === 0) {
    // The node says why, and it is not always the same why: a loopback
    // listener is unreachable, an IPv6 one is perfectly reachable and merely
    // cannot be discovered on the IPv4 group, and an address this build will
    // not deliver to is a third thing. Writing one sentence here for all of
    // them would tell most owners something untrue.
    const box = element("div", "stale", "這台機器沒有任何可以廣播的位址，所以實際上什麼都沒有送出。");
    box.append(element("div", "muted", announcing.lastError || "節點沒有說明原因。"));
    return box;
  }
  if (announcing.lastError) {
    const box = element("div", "stale", "最後一次廣播失敗了，所以現在可能沒有任何人看到這台機器。");
    box.append(element("div", "muted", announcing.lastError));
    return box;
  }
  if (announcing.lastAnnouncedAt) {
    return element("div", "muted", `最後一次廣播：${relative(announcing.lastAnnouncedAt)}`);
  }
  return element("div", "muted", "還沒有送出第一次廣播。");
}

// tickCountdown updates only the countdown's own text, leaving the rows alone.
function tickCountdown() {
  const line = el("pairing-countdown");
  const left = pairingRemaining();
  if (!state.pairing?.state?.open) {
    line.textContent = "";
    return;
  }
  line.textContent = left === 0 ? "" : `剩 ${clock(left)}`;
}

function renderCandidates() {
  const rows = el("candidate-rows");
  const notice = el("candidate-notice");
  const full = el("candidate-full");
  rows.replaceChildren();
  full.replaceChildren();
  notice.textContent = "";

  const pairing = state.pairing;
  if (!pairing) return;
  if (pairing.availability === "off") {
    rows.append(element("div", "empty",
      "這台機器沒有在看，所以這裡不會有任何內容——不論同網段有誰在廣播。"));
    return;
  }
  if (pairing.availability !== "on") {
    rows.append(element("div", "empty",
      "配對狀態讀不到，所以這份清單也不可信，這裡不顯示任何內容。"));
    return;
  }
  if (pairing.candidatesError) {
    rows.append(element("div", "stale",
      "無法取得候選清單，所以這裡不顯示任何內容。這是本機的讀取問題，不代表沒有人在廣播。"));
    rows.append(element("div", "muted", pairing.candidatesError));
    return;
  }
  // In its own element above the list, not the first row of it. A full list is
  // long by definition — that is what full means — and the rows scroll, so a
  // warning inside them is scrolled away by the reader who most needs it.
  // Measured: with 64 rows it left the view after 600px of scrolling.
  if (pairing.full) {
    full.append(element("div", "stale",
      "候選清單已滿。同網段有人可以持續送出封包把清單佔滿，" +
      "所以你要找的機器有可能因此沒有出現，而不是因為它沒在廣播。"));
  }
  const candidates = pairing.candidates ?? [];
  if (candidates.length === 0) {
    rows.append(element("div", "empty", "目前沒有看到任何機器在廣播。"));
  }
  for (const candidate of candidates) {
    rows.append(candidateRow(candidate));
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
// It cannot complete a pairing, and it must not look as though it could. The
// announcement carries no public key by design, so the owner still has to get
// that from the other machine — and the fingerprint field is their statement
// that they compared one there, so filling it from the network would make that
// statement on their behalf.
//
// The announced fingerprint is deliberately NOT repeated here. Printing it one
// line above the field the note tells the owner not to fill from the list would
// put the exact string to type on screen. It stays on the row, where it reads
// as a claim rather than as an instruction.
function prefillPairFrom(candidate) {
  el("pair-node-id").value = candidate.nodeId;
  el("pair-display-name").value = candidate.displayName || "";
  el("pair-platform").value = candidate.platform || "";
  el("pair-public-key").value = "";
  el("pair-fingerprint").value = "";

  const note = el("pair-prefill-note");
  note.replaceChildren();
  // The flags follow the owner into the dialog. The row is where impersonation
  // is visible, and leaving that behind at the moment of deciding to trust is
  // leaving it behind at the only moment it matters.
  const flags = [];
  if (candidate.contested) flags.push("身分有爭用");
  if (candidate.duplicate) flags.push("名稱或指紋重複");
  if (flags.length > 0) {
    // Both, when both. A ternary picked one, so a row the list flags twice
    // arrived in the dialog — where trust is granted — flagged once.
    note.append(element("div", "stale",
      "這一列被標記為" + flags.join("、") +
      "：同網段有另一份廣播與它衝突，其中至少一份是假的。除非你能在對方機器上直接核對，否則不要信任它。"));
  }
  note.append(element("div", "",
    "節點 ID、名稱與平台是從廣播帶進來的，全都是對方自己宣稱的，沒有經過任何驗證。"));
  note.append(element("div", "",
    "公鑰不在廣播內容裡，必須在對方機器上執行 ah node 取得。指紋也請看對方螢幕上顯示的那一組，" +
    "逐組核對後再填進來——這個欄位的意思就是「我核對過了」。"));
  // The node id is what trust is keyed on, and the node only checks that the
  // key matches the fingerprint, never that either belongs to this id.
  note.append(element("div", "",
    "同時請確認對方 ah node 顯示的節點 ID 與上面這一組完全相同：信任是記在節點 ID 上的，" +
    "而本機只會檢查公鑰與指紋相符，不會檢查它們屬於這個 ID。"));
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

/* ---------------- inbox ---------------- */

// A message body is the most hostile input this app renders. Candidate metadata
// at least describes a machine; this is free text written by whoever is on the
// other end, chosen to be read by a person. It reaches the DOM through
// element(), which assigns textContent, and nothing about it decides a class.
function renderInbox(view) {
  const meta = el("inbox-meta");
  const body = el("inbox-body");
  body.replaceChildren();

  if (view.loading) {
    // Not an empty list: those render identically, and the read can take
    // fifteen seconds.
    meta.textContent = view.sessionId;
    body.append(element("div", "muted", "正在讀取…"));
    return;
  }
  if (view.error) {
    // A failed read is not an empty inbox, and only one of them means there is
    // nothing to come back for. Shown here rather than in a banner: the dialog
    // covers the banner, so an error there is an error nobody sees.
    meta.textContent = view.sessionId ?? "";
    body.append(element("div", "stale", "讀不到這個 session 的收件匣，所以這裡不顯示任何內容。"));
    body.append(element("div", "muted", view.error));
    return;
  }

  if (view.cleared) {
    // What the destructive action did, where the person who pressed it is
    // looking. The count matters because messages can arrive between reading
    // the list and confirming, and those go with the rest.
    body.append(view.cleared.error
      ? element("div", "stale", `清空失敗，收件匣沒有變動：${view.cleared.error}`)
      : element("div", "muted", `已清空，移除 ${view.cleared.removed} 則。`));
  }
  meta.textContent = view.more
    ? `${view.sessionId} · 顯示最舊的 ${view.showing} 則，共 ${view.held} / ${view.capacity} 則`
    : `${view.sessionId} · ${view.held} / ${view.capacity} 則`;
  if (view.full) {
    // A full inbox refuses new messages, which is a thing happening now rather
    // than a list that happens to be long.
    body.append(element("div", "stale",
      "收件匣已滿，新的訊息會被退回。清空之後才會再收得到。"));
  }
  if (view.messages.length === 0) {
    body.append(element("div", "empty", "還沒有任何訊息。"));
    return;
  }
  if (view.more) {
    // The oldest end, because the node returns them in arrival order. An owner
    // looking for what just came in has to empty some of this first.
    body.append(element("div", "stale",
      `收件匣裡還有更多訊息，這裡只顯示最舊的 ${view.showing} 則。` +
      "新到的訊息排在後面，要先清掉一些才看得到。"));
  }
  for (const message of view.messages) {
    const row = element("div", "inboxrow");
    row.append(senderLine(message.from));
    row.append(element("div", "muted", relative(message.createdAt)));
    row.append(element("div", "inboxbody", message.body));
    body.append(row);
  }
}

// Reads are numbered, for the same reason the pairing panel numbers its own: a
// slow answer must not repaint the dialog after a fast one. Here it is worse
// than a stale display — the clear button reads state.inboxSession, so an
// out-of-order answer could show one session's messages above a button that
// empties another's, irreversibly.
let inboxRequest = 0;
let inboxApplied = 0;

// senderLine splits who sent it from what they called themselves.
//
// `from` is `<node id>/<session id>` for another machine, and the two halves
// are not equally trustworthy: the node id was proven by the TLS pin and the
// signature, while the session id is up to 128 bytes the sender chose. Printed
// as one string they read as one fact — and a sender can pad theirs so it looks
// like a separate field, or start it with a bidi override. Split, labelled, and
// the id given the same monospace treatment as a fingerprint.
function senderLine(from) {
  const line = element("div", "sender");
  const value = String(from ?? "");
  const slash = value.indexOf("/");

  if (slash >= 0) {
    const nodeId = value.slice(0, slash);
    const session = value.slice(slash + 1);
    if (nodeId === state.localNodeId && state.localNodeId !== "") {
      // This machine's own queue. The owner's API qualifies with the local node
      // id, so this is what an ordinary local message looks like — not the
      // bare form below.
      line.append(element("span", "muted", "本機 "));
      line.append(element("span", "claimed", session));
      return line;
    }
    line.append(element("span", "fingerprint", nodeId));
    line.append(element("span", "muted", " 自稱 "));
    // The half they chose, marked as such.
    line.append(element("span", "claimed", session));
    return line;
  }

  // No separator. This is where getting it backwards is dangerous, so it
  // follows describeSender in internal/mcpserver/inbox.go rather than guessing:
  // a peer may omit its sending session, and the node then stores the bare
  // proven peer node id. Reading that as local would put a hostile message
  // behind the most trustworthy label the envelope can carry.
  if (value === "") {
    // qualifiedSender never yields empty for a peer — it falls back to the
    // proven node id — so empty means the owner's API queued this unnamed.
    line.append(element("span", "muted", "本機"));
    return line;
  }
  if (value === state.localNodeId) {
    line.append(element("span", "muted", "本機"));
    return line;
  }
  if (state.nodes.some((node) => node.nodeId === value)) {
    // A paired node's own id settles a bare value whatever shape it has.
    line.append(element("span", "fingerprint", value));
    line.append(element("span", "muted", " 未指明 session"));
    return line;
  }
  if (looksLikeNodeId(value)) {
    line.append(element("span", "fingerprint", value));
    line.append(element("span", "muted", " 未指明 session"));
    return line;
  }
  // Session-shaped and not paired: a local message from before senders were
  // self-describing, or a peer paired under an older rule and since revoked.
  // Indistinguishable, so claim no origin rather than the wrong one.
  line.append(element("span", "muted", "來源不明 "));
  line.append(element("span", "claimed", value));
  return line;
}

// looksLikeNodeId is model.ValidateNodeID's shape test: 16 to 128 printable
// ASCII characters, no separator, and not readable as a session id.
function looksLikeNodeId(value) {
  if (value.length < 16 || value.length > 128) return false;
  if (value.includes("/")) return false;
  for (const character of value) {
    const code = character.codePointAt(0);
    if (code < 0x21 || code > 0x7e) return false;
  }
  return !/^(claude|codex):/.test(value);
}

async function openInbox(sessionId, cleared) {
  const sequence = ++inboxRequest;
  // Cleared, not pointed at the new session. inboxSession is what the clear
  // button empties, so it may only ever name the session whose messages are on
  // screen — setting it here would aim an irreversible action at a session
  // whose content has not arrived, while the dialog still shows another's.
  // While it is null the button is a no-op.
  state.inboxSession = null;
  el("inbox-modal").classList.remove("hidden");
  // Loading is its own state. Rendering an empty list here is byte-identical to
  // an inbox with nothing in it, and the client waits up to fifteen seconds.
  renderInbox({ sessionId, loading: true, messages: [] });

  let view;
  try {
    view = await Inbox(sessionId);
  } catch (error) {
    view = { sessionId, messages: [], error: String(error) };
  }
  if (sequence <= inboxApplied) {
    // A later read already landed. This one describes an older moment, and
    // painting it would put its messages above a button aimed elsewhere.
    return;
  }
  inboxApplied = sequence;
  // Armed only once an answer for this session has come back — never while one
  // is in flight, which is when the dialog still shows another session's
  // messages.
  //
  // Including a failed answer, deliberately: a read that fails because the
  // inbox is too large to decode is exactly when emptying it is the way out,
  // and the truncation message tells the owner so. What must not happen is the
  // button aiming at a session other than the one asked for.
  state.inboxSession = view.sessionId ?? sessionId;
  renderInbox({ ...view, cleared });
}

function closeInbox() {
  // Retire whatever is in flight. Otherwise its answer still applies on
  // arrival, repainting a hidden card and re-arming the clear target — which
  // is safe today only because `.hidden` makes the button unclickable, and a
  // guard that leans on a CSS rule is not one.
  inboxApplied = inboxRequest;
  state.inboxSession = null;
  el("inbox-modal").classList.add("hidden");
  el("inbox-body").replaceChildren();
  el("inbox-meta").textContent = "";
  state.inboxSession = null;
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
// Reads are numbered so a slow one cannot overwrite a fast one.
//
// The client's timeout is 15 seconds and the poll runs every 5, so three can be
// in flight at once. Without this, clicking Stop showed the window closed and
// then an older reply put it back to "open, 2:30 left" with the Stop button
// live again — the panel asserting, from a stale read, something the owner had
// just changed.
let pairingRequest = 0;
let pairingApplied = 0;

async function loadPairing() {
  const sequence = ++pairingRequest;
  let result;
  try {
    result = await Pairing();
  } catch (error) {
    // A binding that threw is not a fact about the network either.
    result = { availability: "unknown", candidates: [], error: String(error) };
  }
  if (sequence <= pairingApplied) {
    // A later read already landed. This one describes an older moment.
    return;
  }
  pairingApplied = sequence;
  state.pairing = result;
  // Only on a read that reached the node. An unreachable node answers with no
  // name, and blanking the warning to "（未知）" because one poll failed would
  // drop the one string the warning exists to show.
  if (result.state?.displayName) {
    state.localName = result.state.displayName;
    state.localNameIsChosen = Boolean(result.state.nameIsChosen);
  }
  // Stamped when the answer is applied, from the monotonic clock the countdown
  // is subtracted against.
  state.pairingReadAt = performance.now();
  if (state.view === "network") renderPairing();
}

el("inbox-close").onclick = closeInbox;
el("inbox-modal").onclick = (event) => {
  if (event.target === el("inbox-modal")) closeInbox();
};
el("inbox-clear").onclick = () => {
  const session = state.inboxSession;
  if (!session) return;
  // Not undoable, so it is asked rather than assumed. The node has no
  // "unclear", and messages that arrive between this dialog and the confirm go
  // with the rest.
  if (!confirm(`清空 ${session} 的收件匣？這個動作無法復原。`)) return;
  withBusy("清空收件匣", async () => {
    const cleared = await ClearInbox(session);
    // Re-read through openInbox, so the answer is sequence-guarded like every
    // other read and lands on the session it was asked about. The outcome is
    // carried into the dialog rather than a banner, which the dialog covers —
    // and a failure has to be visible there, or the owner is left with an
    // unchanged list and no sign the action did not happen.
    await openInbox(session, cleared);
  });
};

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

// The panel is polled only while it is on screen.
setInterval(() => {
  if (state.view !== "network") return;
  loadPairing().catch(() => {});
}, 5000);

// And the countdown ticks in between, so an expiring window is visibly
// expiring. Only the countdown's own text is touched: redrawing the panel here
// would rebuild the candidate rows every second, replacing the row an owner is
// about to click.
setInterval(() => {
  if (state.view !== "network" || !state.pairing?.state?.open) return;
  const before = pairingRemaining();
  tickCountdown();
  // The node is what closes the window. When the count reaches zero, ask it
  // rather than waiting up to five seconds to stop claiming an open window.
  if (before === 0) {
    renderPairingWindow();
    loadPairing().catch(() => {});
  }
}, 1000);

load()
  .then(loadPairing)
  .catch((error) => banner(`載入失敗：${error}`));
