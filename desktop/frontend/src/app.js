// The desktop app's behaviour, as one module that a test can import.
//
// Bindings come in through ./api.js (configure) rather than a wailsjs import so
// this file loads under node; the DOM comes from the global document, which the
// tests provide from ./test/dom-shim.mjs. boot() runs once: it wires the
// handlers, starts the intervals and does the first load.
import { api } from "./api.js";
import * as F from "./sessions/filter.js";
import { t, plural, paintStatic, pickLanguage, setLanguage, language, LANGUAGES } from "./i18n/index.js";
export { configure } from "./api.js";

export function boot({ start = true, backdropUrl = "" } = {}) {

  const state = {
    sessions: [],
    counts: {},
    selected: new Set(),
    search: "",
    view: "local",
    nodes: [],
    peers: [],
    presenceError: "",
    // The pairing list's own read error, from a read that otherwise reached the
    // node. Overview answers an empty node list then (desktop/app.go, the
    // trustedNodes branch), so without it the audience dialog would describe
    // every paired machine as unpaired.
    nodesError: "",
    selectedNode: null,
    busy: false,
    // pairing is loaded separately from the overview: it changes on its own — a
    // window expires, a machine starts or stops advertising — while the session
    // table does not, and a candidate list that only updates when the owner
    // clicks refresh is one they cannot use to find a machine.
    pairing: null,
    // The pairing exchange's own rows (#63), read separately from the window
    // and the candidate list because reading them is not free at the node: it
    // polls every pending outgoing request at the far side first. So they are
    // asked for only while the pairing drawer is open.
    //
    // pairRequestsLoaded keeps "no rows" apart from "never asked": the first is
    // a fact about the two machines, the second is this window not having
    // looked yet, and rendering the second as the first tells an owner nobody
    // asked when nobody has checked.
    pairRequests: [],
    pairRequestsAll: false,
    pairRequestsError: "",
    pairRequestsLoaded: false,
    // pairingReadAt is when the countdown below was true. The remaining seconds
    // come from the node, so they are subtracted from the moment they were read
    // rather than compared against this machine's own idea of the expiry.
    pairingReadAt: 0,
    // mcpSession is whose config the MCP dialog is showing. Only the title
    // needs it, and only so a language switch can write that title again.
    mcpSession: null,
    // And what that dialog's status line says, as a state rather than as a
    // sentence — "did the clipboard take it" has to survive a language switch.
    mcpStatus: null,
    // inboxSession is whose inbox the modal is showing, so Clear knows what it
    // would empty and a refresh knows what to re-read.
    inboxSession: null,
    // How much each local session is still holding, for the badges (#146).
    //
    // `ok` is the whole point of the shape. The node leaves a session holding
    // nothing out of the map, so a missing key is 0 — and an answer that never
    // arrived is also an empty map. Those are different facts: the first means
    // "nothing is waiting", the second means "nobody knows", and a badge drawn
    // from the second would be a zero this window invented. `counts` keeps the
    // last numbers that did arrive rather than being cleared, so a read that
    // fails costs the badges and not the history; nothing is painted from them
    // while ok is false.
    inboxCounts: { ok: false, counts: {} },
    localNodeId: "",
    localFingerprint: "",
    // This build's own version, read once from the Go side at boot. It is in
    // the title bar so a bug report from a stranger names a build; empty until
    // the binding answers, and the line simply omits it until then.
    appVersion: "",
    // This node's own public key, as the peer must type it. Read from the node
    // on every overview; empty until one succeeds.
    localPublicKey: "",
    // An address being typed into a node's detail page, kept across the renders
    // the background refresh causes. { nodeId, value } or null.
    addressDraft: null,
    // nodeAutoWake is the node's own -auto-wake flag, which is half of what
    // waking needs. The per-session box in the audience dialog does nothing
    // while this is false, and the dialog says so rather than letting the owner
    // tick it and wait.
    nodeAutoWake: false,
    localName: "",
    localNameIsChosen: false,
    // Whether a read ever reached the node. It decides what an unreachable read
    // says: "this is the last data we had" only means something if there is any.
    loadedOnce: false,
    // What the title bar's node line last said, kept as its parts rather than
    // as the finished sentence: half of that line is a translated string, so a
    // language switch has to build it again and there is nothing else to build
    // it from once load() has returned.
    nodeLine: null,
    // The addresses the settings form's address list was built from. Same
    // reason: the option labels are part translation, and rebuilding them
    // after a switch must not mean another round trip to the node.
    nodeAddresses: { list: [], failure: "" },
    // ---- redesign state ----
    // Grouped filters: a Set of chosen values per group (docs/ui-contract.md
    // §5.1). Empty means "no restriction". Search, filters and sort are kept in
    // localStorage so the table opens the way it was left.
    filters: F.emptyFilters(),
    sort: { ...F.DEFAULT_SORT },
    // Which drawer tab the inbox drawer shows: inbox | outbound | wakes.
    inboxTab: "inbox",
    // Outbound and wake logs, each with its own sequence guard below.
    outbound: { messages: [], next: "", loading: false, error: "", session: null },
    wakes: { wakes: [], limits: null, loading: false, error: "", session: null },
    // Background photo + rain: the owner's switches, and the OS's reduce-motion.
    // The rain starts off. It costs a whole core on an Intel HD 520 (measured:
    // 101.7% with it running, 2.4% with it off, #156), which is not something
    // to spend on a machine whose owner has not asked for it. The photo is
    // free by comparison and stays on.
    // lang is "" until the owner picks one: empty means "follow the OS",
    // which is what pickLanguage does with navigator.language.
    // onboardingDismissed is the checklist's one stored fact. Everything else
    // about that card is computed from state on every render, so a step that
    // regresses comes back — but a card the owner has closed stays closed.
    ui: { backdrop: true, motion: false, lang: "", onboardingDismissed: false },
    // Which settings section is scrolled to.
    settingsSection: "settings-service",
    service: null,
    // The node's remembered start-up settings, as the node last answered.
    nodeSettings: null,
    // Whether a read has been attempted for this view; a failure leaves
    // nodeSettings null, and without this the panel would re-read on every
    // repaint.
    nodeSettingsTried: false,
    nodePrivateSuggested: "",
    nodeReachable: false,
    serviceFormTouched: false,
    // The session the inbox drawer was opened for, set before the read; the
    // clear button's target (inboxSession) is armed only once an answer lands.
    inboxSessionAsked: null,
    // The view renderInbox was last handed, so a language switch can draw the
    // same list again instead of leaving the previous language on screen.
    inboxView: null,
  };

  const el = (id) => document.getElementById(id);

  /* ---------------- filtering ---------------- */

  function visible() {
    return F.sorted(F.visible(state.sessions, { search: state.search, filters: state.filters }), state.sort);
  }

  /* ---------------- preferences ---------------- */

  // localStorage can be absent or throw (a WebView with storage disabled); a
  // failure to remember the filters is not worth a banner.
  const UI_PREFS_KEY = "agenthub.desktop.ui.v1";

  function loadPrefs() {
    let raw = null;
    try {
      raw = globalThis.localStorage?.getItem(F.PREFS_KEY) ?? null;
    } catch {
      raw = null;
    }
    const prefs = F.parsePrefs(raw);
    state.filters = prefs.filters;
    state.sort = prefs.sort;
    state.search = prefs.search;
    let ui = null;
    try {
      ui = JSON.parse(globalThis.localStorage?.getItem(UI_PREFS_KEY) ?? "null");
    } catch {
      ui = null;
    }
    if (ui && typeof ui === "object") {
      state.ui.backdrop = ui.backdrop !== false;
      // Opt-in, so a stored file written before the rain had a switch — or one
      // with the key missing — leaves it off rather than on.
      state.ui.motion = ui.motion === true;
      // Only a language this build has. A stored value from a newer build, or a
      // hand-edited one, falls back to the locale rather than to the key names.
      state.ui.lang = typeof ui.lang === "string" ? ui.lang : "";
      // Opt-out, so a preferences file written before this card existed leaves
      // the checklist showing rather than silently suppressed.
      state.ui.onboardingDismissed = ui.onboardingDismissed === true;
    }
    // Applied here rather than at the call site so every path that reads the
    // preferences — boot, and the node checks that call loadPrefs directly —
    // ends up with the same language the stored override asks for.
    setLanguage(pickLanguage(globalThis.navigator?.language, state.ui.lang));
  }

  // Typing in the search box would otherwise write localStorage on every
  // keystroke; the write is deferred and coalesced.
  let savePrefsTimer = null;
  function savePrefsSoon() {
    if (savePrefsTimer) clearTimeout(savePrefsTimer);
    savePrefsTimer = setTimeout(() => {
      savePrefsTimer = null;
      savePrefs();
    }, 300);
  }

  function savePrefs() {
    try {
      globalThis.localStorage?.setItem(F.PREFS_KEY, F.serializePrefs(state));
      globalThis.localStorage?.setItem(UI_PREFS_KEY, JSON.stringify({ ...state.ui }));
    } catch {
      // Nothing to do: the table still works, it just forgets on restart.
    }
  }

  /* ---------------- rendering ---------------- */

  /* ---------------- DOM helpers ---------------- */

  // Provider metadata is untrusted input (docs/architecture.md). Every value that
  // originates from a provider reaches the DOM as text, never as markup, so a
  // working directory or session ID containing HTML cannot execute in the app.
  // whyDetails is the explanation a state used to carry on screen, folded
  // under a 「說明」 the owner can open.
  //
  // It used to be a `title` naming a section of docs/desktop-window.md (#194).
  // That file is in the repository, not in the installed app, so the tooltip
  // sent the owner to something they did not have; and a title on a
  // paragraph is shown only to a pointer — the keyboard and a screen reader
  // never reach it. A <summary> is focusable and opens with Enter or Space, and
  // the sentences inside are the ones the owner needs, in their language.
  // docs/desktop-window.md stays, for readers of the repository.
  function whyDetails(key) {
    const details = document.createElement("details");
    details.className = "why";
    details.append(element("summary", "", t("common.why")), element("p", "", t(key)));
    return details;
  }

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

  // keepChildren writes a container only when its contents actually differ.
  //
  // replaceChildren detaches every child before re-appending it, so handing it
  // the very same elements in the very same order still drops the focus inside
  // them and still eats a press whose mousedown and mouseup fall on either side
  // of the call. Both polled lists in the pairing drawer are re-rendered twice a
  // second over data that usually did not change, so "did not change" has to
  // mean "not written".
  function keepChildren(container, wanted) {
    const current = container.children ?? [];
    const unchanged = current.length === wanted.length
      && wanted.every((node, index) => current[index] === node);
    if (!unchanged) container.replaceChildren(...wanted);
  }

  // keptMessage returns the child already sitting at that position when it is
  // the very same one-line message, so a list showing an explanation instead of
  // rows is not rewritten on every tick either. A container holding nothing but
  // 「目前沒有等待處理的配對請求。」 is the commonest state this drawer is in.
  function keptMessage(container, index, className, text) {
    const current = (container.children ?? [])[index];
    if (current && current.className === className && (current.children?.length ?? 0) === 0
      && current.textContent === text) {
      return current;
    }
    return element("div", className, text);
  }


  // renderChips draws the three filter groups. Each chip carries a facet count:
  // what it would match with the other groups and the search still applied —
  // so the number beside "active" agrees with the table when "Codex" is on.
  function renderChips() {
    const counts = F.facetCounts(state.sessions, { search: state.search, filters: state.filters });
    for (const group of F.GROUPS) {
      const container = el(`chips-${group}`);
      container.replaceChildren();
      for (const chip of F.CHIPS.filter((c) => c.group === group)) {
        const button = document.createElement("button");
        const on = state.filters[group].has(chip.value);
        const n = counts[`${group}:${chip.value}`] ?? 0;
        button.className = `chip${on ? " on" : ""}${n === 0 && !on ? " zero" : ""}`;
        button.append(chip.labelKey ? t(chip.labelKey) : chip.label, element("span", "n", String(n)));
        button.onclick = () => {
          state.filters = F.toggleChip(state.filters, group, chip.value);
          savePrefs();
          render();
        };
        container.append(button);
      }
    }
    el("btn-clear-filters").classList.toggle("hidden", !F.hasAnyFilter(state.filters) && !state.search.trim());
  }


  // describeAudience answers "published to whom" in one cell.
  function describeAudience(audience) {
    const mode = audience?.mode ?? "none";
    // The table's own wording, shorter than the chip's and the dialog's: at
    // 900px the column holds 88px of pill, and 「Every paired machine」 is
    // 124px, so it was cut to 「Every paired ma」 (#194). The tooltip and the
    // filter chip keep the whole phrase.
    if (mode === "all_paired") {
      return { text: t("audience.cell.allPairedShort"), title: t("audience.cell.allPaired"), published: true };
    }
    if (mode === "selected") {
      const count = audience?.nodes?.length ?? 0;
      return {
        text: count === 0 ? t("audience.cell.selectedNone") : plural(count, "audience.cell.nodeCount"),
        published: count > 0,
      };
    }
    return { text: t("audience.cell.none"), published: false };
  }

  function shortId(id) {
    const [provider, rest = ""] = id.split(":");
    return { provider, rest };
  }

  function relative(iso) {
    const then = new Date(iso).getTime();
    if (!Number.isFinite(then)) return "—";
    const seconds = Math.max(0, (Date.now() - then) / 1000);
    if (seconds < 60) return plural(Math.floor(seconds), "time.secondsAgo");
    if (seconds < 3600) return plural(Math.floor(seconds / 60), "time.minutesAgo");
    if (seconds < 86400) return plural(Math.floor(seconds / 3600), "time.hoursAgo");
    return plural(Math.floor(seconds / 86400), "time.daysAgo");
  }

  // Icons for the row actions are text, not glyphs: the buttons are small but
  // their labels are what the tests, and screen readers, look for.
  function rowActionButton(className, label, title, onclick) {
    const button = element("button", `ghost ${className}`, label);
    button.title = title;
    button.onclick = onclick;
    return button;
  }

  // resumeCommand is what to paste in a terminal to pick this session up again.
  // For Claude the provider id is the jsonl's sessionId, which is what
  // `claude --resume` takes (adapter/claude.go); for Codex it is the thread id.
  function resumeCommand(session) {
    const { rest } = shortId(session.id);
    const id = session.providerSessionId || rest;
    if (session.provider === "codex") return `codex resume ${id}`;
    return `claude --resume ${id}`;
  }

  // Clipboard writes are serialised, not merely numbered: two quick clicks on
  // different rows each start a CopyText call, and the one that resolves last
  // is what the clipboard holds. Chaining them keeps the last click's command
  // on the clipboard, and only the last click gets to report.
  let clipboardQueue = Promise.resolve();
  let resumeRequest = 0;
  async function copyResumeCommand(session) {
    const sequence = ++resumeRequest;
    const command = resumeCommand(session);
    const write = clipboardQueue.then(() => api.CopyText(command));
    clipboardQueue = write.catch(() => {});
    try {
      await write;
    } catch (error) {
      if (sequence !== resumeRequest) return;
      banner(t("row.resumeCopyFailed", { error, command }));
      return;
    }
    if (sequence !== resumeRequest) return;
    banner(session.cwd
      ? t("row.resumeCopiedIn", { command, cwd: session.cwd })
      : t("row.resumeCopied", { command }), true);
  }

  // sessionRows keeps one <tr> per session id, so the fifteen-second tick
  // updates the rows it already drew rather than building new ones. The reason
  // is the one updateCandidateRow exists for and it is sharper here: this table
  // is the one an owner is always pointing at, the inbox button now carries a
  // badge that changes on its own, and a rebuilt row swaps the button out from
  // under a press whose mousedown has already landed.
  const sessionRows = new Map();

  function renderRows(rows) {
    const body = el("rows");
    const wanted = [];
    const seen = new Set();
    for (const session of rows) {
      const key = String(session.id ?? "");
      // A row with no id cannot be matched to a kept one — and must not take
      // somebody else's. The node does not produce those; a duplicate id would
      // be the same hazard, so the second one gets a fresh row too.
      const keyed = key !== "" && !seen.has(key);
      seen.add(key);
      let tr = keyed ? sessionRows.get(key) : undefined;
      if (tr) {
        updateSessionRow(tr, session);
      } else {
        tr = sessionRow(session);
        if (keyed) sessionRows.set(key, tr);
      }
      wanted.push(tr);
    }
    for (const key of [...sessionRows.keys()]) {
      if (!seen.has(key)) sessionRows.delete(key);
    }
    keepChildren(body, wanted);
    el("empty").classList.toggle("hidden", rows.length > 0);
  }

  // relabelSessionRows re-runs the update pass over the rows that are already
  // on screen, from the session each was last drawn from.
  //
  // render() -> renderRows() does this too, but only for the rows `visible()`
  // still returns, and only while the local view is the one being drawn. A
  // language switch has to reach every kept row regardless: a row this switch
  // does not touch keeps the language it was built in until something else
  // evicts it, which is what keeping rows across renders introduced.
  function relabelSessionRows() {
    for (const tr of sessionRows.values()) {
      if (tr.session) updateSessionRow(tr, tr.session);
    }
  }

  // sessionRow builds the eight cells once. Everything that changes between
  // ticks is written by updateSessionRow into these same elements.
  //
  // There is no MANAGED cell. The node sends model.Management, and every
  // session this app has ever listed is "unmanaged" — a column of one repeated
  // word across a thousand rows, costing 128px next to the working directory,
  // which is the column that actually runs out of room. The value is on the
  // provider badge's tooltip, so a node that one day says something else is
  // still readable from the row.
  function sessionRow(session) {
    const tr = document.createElement("tr");

    const checkCell = element("td", "col-check");
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkCell.append(checkbox);

    // The name the session's own app gives the conversation is what a person
    // recognises a row by; the UUID is only ever needed to resume or to quote
    // one, and the copy button and the tooltip both still carry it. Sessions
    // the provider never named keep showing the ID, because a row with no
    // handle at all is worse than a row with an ugly one.
    const idCell = element("td", "sid");
    const providerTag = element("span", "providertag");
    const label = element("b");
    idCell.append(providerTag, label);

    const statusCell = element("td");
    const statusPill = element("span", "pill");
    statusCell.append(statusPill);

    const audienceCell = element("td");
    const audiencePill = element("span", "pill");
    audienceCell.append(audiencePill);

    // The four audience flags, readable without opening the dialog. They say
    // what a peer may do with this session, so on a session no peer can see
    // they say nothing at all — an unpublished row leaves the cell empty rather
    // than showing four dashed boxes that are dim for a reason nothing states.
    const flagsCell = element("td");
    const chips = element("span", "flagchips");
    const flagCwd = element("span", "flag", "CWD");
    const flagIn = element("span", "flag");
    const flagOut = element("span", "flag");
    const flagWake = element("span", "flag");
    chips.append(flagCwd, flagIn, flagOut, flagWake);
    flagsCell.append(chips);

    // The path goes in a <bdi> because the cell is laid out right-to-left so
    // that a path too long for the column loses its head rather than its tail —
    // the project name is the part worth keeping. The isolate stops that
    // direction from reordering the path's own slashes.
    const cwdCell = element("td", "mono muted cwd");
    const cwdText = element("bdi");
    cwdCell.append(cwdText);

    const seenCell = element("td", "muted");

    // Row actions. Opening an inbox is a read; resume writes the clipboard
    // and says so.
    //
    // There is no MCP button here on purpose. It used to sit between these
    // two, and it produced the `.mcp.json` that binds one agent to one
    // session (#112) — but that is a thing an owner needs once, if ever,
    // while this button was on all thousand rows. Everything the four MCP
    // tools do, `ah` does too (list, status, send, inbox), and the
    // agenthub-watch skill already goes through `ah` rather than MCP, so a
    // new owner who never sees this loses nothing they were using.
    //
    // openMCPConfig and the mcp-modal it fills are still here and still
    // tested, so restoring the entry point is one rowActionButton call.
    const actionsCell = element("td", "col-actions");
    const group = element("span", "rowactions");
    //
    // Both buttons are built empty and worded by updateSessionRow. A row is
    // kept across renders now, so a string written here is written once, in
    // whatever language was on screen when the session first appeared — and a
    // language switch repaints everything except it. The labels live in a
    // <span> of their own because the badge is a child of the same button, and
    // writing textContent on the button itself would delete it.
    // (docs/ui-contract.md §3.1: every translated string in a kept row is
    // written in the update pass, never only at creation.)
    const inboxButton = rowActionButton("inbox", "", "", null);
    const inboxLabel = element("span", "label");
    // The badge rides inside the button rather than beside it, so the number
    // and the way to act on it are one target and the sticky column's width
    // does not have to grow.
    const badge = element("span", "inboxbadge hidden");
    inboxButton.append(inboxLabel, badge);
    const resumeButton = rowActionButton("resume", "", "", null);
    const resumeLabel = element("span", "label");
    resumeButton.append(resumeLabel);
    group.append(inboxButton, resumeButton);
    actionsCell.append(group);

    tr.append(checkCell, idCell, statusCell, audienceCell,
      flagsCell, cwdCell, seenCell, actionsCell);
    tr.sessionParts = {
      checkbox, idCell, providerTag, label, statusPill,
      audiencePill, chips, flagCwd, flagIn, flagOut, flagWake, cwdCell, cwdText,
      seenCell, inboxButton, inboxLabel, badge, resumeButton, resumeLabel,
    };
    updateSessionRow(tr, session);
    return tr;
  }

  // updateSessionRow writes this session into an existing row.
  //
  // textContent and className on the elements that are already there, never a
  // rebuilt subtree: the two buttons have to survive a tick, and 「最後 X 秒前」
  // changes on every single one of them.
  function updateSessionRow(tr, session) {
    const parts = tr.sessionParts;
    // Kept so a language switch can re-run this pass over the rows that are
    // already on screen without waiting for the next load (repaintFromState).
    tr.session = session;
    const picked = state.selected.has(session.id);
    tr.className = picked ? "sel" : "";
    parts.checkbox.checked = picked;
    parts.checkbox.onchange = (event) => {
      if (event.target.checked) state.selected.add(session.id);
      else state.selected.delete(session.id);
      render();
    };

    const { rest } = shortId(session.id);
    parts.providerTag.textContent = session.provider;
    // Where the MANAGED column went. It is one word, the same one on every
    // row, and this is the badge it qualifies.
    parts.providerTag.title = t("row.providerTitle", {
      provider: session.provider,
      management: managementLabel(session.management),
    });
    parts.label.className = session.title ? "title" : "";
    parts.label.textContent = session.title ? session.title : rest;
    parts.idCell.title = session.title ? `${session.title}\n${session.id}` : session.id;

    const statusClass = statusPillClass(session.status);
    parts.statusPill.className = statusClass ? `pill ${statusClass}` : "pill";
    parts.statusPill.textContent = session.status;

    const audience = describeAudience(session.audience);
    parts.audiencePill.className = audience.published ? "pill public" : "pill";
    parts.audiencePill.textContent = audience.text;
    // The column is sized for the 900px window, with every table wording
    // measured to fit it (style.css col.c-audience); the tooltip carries the
    // whole of it anyway, and the long form where the cell uses a short one.
    parts.audiencePill.title = audience.title ?? audience.text;

    // The flags describe what a peer is allowed to do with this session, so on
    // one no peer has been given they describe nothing. Hidden rather than
    // drawn dim: four boxes per row, on the rows where they mean least, were
    // most of the ink in this table.
    //
    // "No peer has been given it" is describeAudience's `published`, not the
    // mode: 「指定：無」 — selected, with no nodes — is as unpublished as 不公開,
    // and it used to wear four chips anyway (#194).
    const a = session.audience ?? {};
    parts.chips.classList.toggle("hidden", !audience.published);
    setFlagChip(parts.flagCwd, "CWD", Boolean(a.exportCwd));
    setFlagChip(parts.flagIn, t("row.flagIn"), Boolean(a.acceptMessages));
    setFlagChip(parts.flagOut, t("row.flagOut"), Boolean(a.allowOutbound));
    setFlagChip(parts.flagWake, t("row.flagWake"), Boolean(a.autoWake), true);

    parts.cwdText.textContent = session.cwd ? session.cwd : "—";
    parts.cwdCell.title = session.cwd ? session.cwd : "";
    parts.seenCell.textContent = relative(session.lastSeenAt);
    parts.seenCell.title = parts.seenCell.textContent;

    // The two button labels. Written here and not at creation: see sessionRow.
    //
    // "resume" is the same word in both languages on purpose — it names the
    // command this button copies into a terminal, so it is not in the tables.
    // It is still written here rather than at creation, because the rule is
    // about where a kept row's text is written, and an exception to it is how
    // the next translated string quietly goes back to being written once.
    parts.inboxLabel.textContent = t("inbox.title");
    parts.resumeLabel.textContent = "resume";
    parts.inboxButton.onclick = () => {
      openInbox(session.id).catch((error) => banner(t("inbox.readFailed", { error })));
    };
    parts.resumeButton.title = t("row.resumeTitle", { command: resumeCommand(session) });
    parts.resumeButton.onclick = () => copyResumeCommand(session);
    updateInboxBadge(parts.inboxButton, parts.badge, session.id);
  }

  // setFlagChip writes one of the four audience flags in place.
  function setFlagChip(node, label, on, warn = false) {
    node.className = `flag${on ? " on" : ""}${warn ? " warn" : ""}`;
    node.textContent = label;
  }

  /* ---------------- inbox badges (#146) ---------------- */

  // heldFor answers how much one session is holding, or null when this window
  // does not know.
  //
  // null is not zero and the difference is the whole feature. The node omits a
  // session that holds nothing, so a missing key under a good read is 0; a read
  // that failed leaves no trustworthy map at all, and every session is then
  // null. Painting a 0 in that case would be this window inventing a fact about
  // an inbox it could not see.
  function heldFor(sessionId) {
    const counts = state.inboxCounts;
    if (!counts?.ok) return null;
    const entry = counts.counts?.[sessionId];
    if (!entry) return 0;
    const held = Number(entry.held);
    return Number.isFinite(held) && held >= 0 ? held : null;
  }

  function isFull(sessionId) {
    const counts = state.inboxCounts;
    if (!counts?.ok) return false;
    return Boolean(counts.counts?.[sessionId]?.full);
  }

  // updateInboxBadge writes the number onto a row's inbox button, in place.
  //
  // Hidden at 0 and hidden when unknown: a badge is a thing that has arrived,
  // and a row wearing "0" on every session is noise that hides the rows that do
  // carry something. A full inbox is coloured apart because it is refusing new
  // messages right now, which is a thing to act on rather than a larger number.
  //
  // The count is what the inbox still holds, never "unread". Nothing marks a
  // message read — not the node, and deliberately not this window.
  function updateInboxBadge(button, badge, sessionId) {
    const held = heldFor(sessionId);
    const full = isFull(sessionId);
    const show = held !== null && held > 0;
    badge.className = `inboxbadge${full ? " full" : ""}${show ? "" : " hidden"}`;
    badge.textContent = show ? String(held) : "";
    // A row with nothing waiting keeps the button and loses the word. The
    // stylesheet swaps the label for an envelope; the label element and its
    // text stay in the DOM, so the tooltip, the accessible name and the
    // language switch all still reach it — what shrinks is the ink, on the
    // rows where this button has nothing to report.
    button.className = show ? "ghost inbox" : "ghost inbox compact";
    // The number alone does not say what it counts, and the button's own title
    // is the only place a pointer can ask.
    //
    // This is also where zero and unknown stop looking alike. Neither wears a
    // badge — a "0" on every row would bury the rows that carry something — so
    // without this the two states are indistinguishable on screen, and the
    // difference the whole feature turns on would be invisible: "nothing is
    // waiting" is an answer, "this could not be read" is not.
    button.title = show
      ? plural(held, full ? "inbox.badge.titleFull" : "inbox.badge.title", { held })
      : held === 0 ? t("inbox.badge.titleEmpty") : t("row.inboxTitle");
  }

  // renderInboxTotals writes the two places the whole machine's backlog shows:
  // the 本機 session tab, and the window title.
  //
  // A newcomer is not looking at any particular row. Something that arrived
  // while they were in another view, or in another application, has to be
  // visible without hunting — so the tab carries the sum and the title carries
  // it out of the window entirely.
  //
  // Both disappear at zero and while the counts are unknown, for the same
  // reason the row badges do.
  function renderInboxTotals() {
    const counts = state.inboxCounts;
    let total = 0;
    let anyFull = false;
    if (counts?.ok) {
      for (const entry of Object.values(counts.counts ?? {})) {
        const held = Number(entry?.held);
        if (Number.isFinite(held) && held > 0) total += held;
        if (entry?.full) anyFull = true;
      }
    }
    // Two numbers sat side by side on this tab and read as one: 「本機 session
    // 1119 555」 is a thousands separator, or "1119 of 555", long before it is
    // a session count beside a message count. They are separated now by a gap
    // and by an envelope, which says what the second number counts without a
    // word in either language — and the glyph lives in its own element so the
    // number stays the only thing #tab-local-inbox-n holds.
    //
    // Amber when any single inbox is full, for the reason a row badge is: a
    // machine that is turning messages away right now is not just a larger
    // number, and the tab is the only place it shows from another view.
    const pillNode = el("tab-local-inbox");
    pillNode.className = `inboxbadge tabinbox${anyFull ? " full" : ""}${total > 0 ? "" : " hidden"}`;
    el("tab-local-inbox-n").textContent = total > 0 ? String(total) : "";
    pillNode.title = total > 0 ? plural(total, "inbox.badge.tabTitle", { held: total }) : "";
    setDocumentTitle(total);
  }

  // setDocumentTitle puts the backlog in front of the app's name.
  //
  // Wails does not own document.title — the WebView renders the page and the
  // native window takes its title from it, so writing it here is what a browser
  // does and the window follows. Guarded anyway: the node checks run this
  // module against a fake DOM that may not have a document title at all.
  const BASE_TITLE = "AgentHub";
  function setDocumentTitle(total) {
    try {
      document.title = total > 0 ? `(${total}) ${BASE_TITLE}` : BASE_TITLE;
    } catch {
      // A DOM that will not take a title is not a reason to stop rendering.
    }
  }

  // readInboxCounts asks the node once and never rejects.
  //
  // Every failure — the binding missing, the node unreachable, an answer that
  // says it could not read — becomes the same thing: counts are unknown. There
  // is one reaction to all of them, and it is not an empty map.
  async function readInboxCounts() {
    try {
      const view = await api.InboxCounts();
      if (!view?.ok) return { ok: false, counts: {} };
      return { ok: true, counts: view.counts ?? {} };
    } catch {
      return { ok: false, counts: {} };
    }
  }

  // applyInboxCounts keeps the previous numbers on a failed read and only takes
  // the badges off the screen. Clearing them to zero would say every inbox had
  // just been emptied.
  function applyInboxCounts(result) {
    if (result.ok) {
      state.inboxCounts = { ok: true, counts: result.counts };
      return;
    }
    state.inboxCounts = { ok: false, counts: state.inboxCounts?.counts ?? {} };
  }

  // renderSortHeaders marks the sorted column and wires the click once.
  function renderSortHeaders() {
    for (const th of document.querySelectorAll("thead th.sortable")) {
      const key = th.dataset.sort;
      const on = state.sort.key === key;
      th.classList.toggle("sorted", on);
      let mark = th.querySelector(".sortmark");
      if (!mark) {
        mark = element("span", "sortmark");
        th.append(mark);
      }
      mark.textContent = on ? (state.sort.dir === "desc" ? "▼" : "▲") : "▼";
      mark.className = on ? "sortmark" : "sortmark hint";
      if (!th.onclick) {
        th.onclick = () => {
          state.sort = F.nextSort(state.sort, key);
          savePrefs();
          render();
        };
      }
    }
  }


  // managementLabel turns the node's enum into a word in the language on
  // screen. The node sends model.Management ("managed"/"unmanaged"), which is
  // an identifier, and this column used to render it raw — so the table read
  // "managed" under a Chinese header.
  //
  // Anything the table does not know is shown exactly as it arrived: a value
  // this build has never heard of is the node saying something new, and
  // blanking it or guessing at it would hide that.
  function managementLabel(management) {
    const value = String(management ?? "");
    const label = t("session.managed." + value);
    return label === "session.managed." + value ? value : label;
  }

  /* ---------------- the first-launch checklist ---------------- */

  // What a stranger sees when the .dmg finishes and the table is empty.
  //
  // Every control this card offers exists elsewhere in the window; what did not
  // exist was anything that walked somebody through them in order. So the card
  // is a doorway rather than a second implementation: each step's button calls
  // the same function the panel's own button calls, which is what keeps one
  // validation, one banner and one "did it actually take" check.
  //
  // Nothing here is stored except the dismissal. `done` is derived from state
  // on every render, so uninstalling the service from a terminal brings that
  // step back rather than leaving a tick over a machine that has no node.

  // goToService is the way into the service panel, for the same reason
  // goToNodeSettings exists: the remedy is two tabs away, and an owner who has
  // just been told to install a service should not also have to find it.
  function goToService() {
    state.view = "settings";
    state.settingsSection = "settings-service";
    render();
    el("settings-service")?.scrollIntoView?.({ block: "start", behavior: "smooth" });
    const status = state.service ?? {};
    // Opened for them when there is nothing installed yet: that form is the
    // whole of this step, and leaving it closed means one more thing to find.
    if (status.supported && !status.installed) {
      openServiceForm().catch((error) =>
        banner(t("busy.failed", { action: t("service.busyOpenForm"), error })));
    }
    // In every branch, including the one that opened the form: the button this
    // step came from is on the local view, which is now hidden, so returning
    // early would leave the keyboard on an element nobody can see — exactly
    // what goToNodeSettings's comment forbids.
    el("service-open")?.focus?.();
  }

  // goToPairing switches to the view the drawer belongs to before opening it.
  // The drawer polls the exchange's rows only while that view is on screen, so
  // opening it from the sessions table alone would show rows that never refresh.
  function goToPairing() {
    state.view = "network";
    render();
    openPairingDrawer().catch(() => {});
  }

  // Which of the four situations this card exists for the window is in. The
  // unreachable case is the one that does NOT wait for a successful read: a
  // node that never answered is exactly the state this card is for, and gating
  // it on loadedOnce would hide it precisely then.
  //
  // There is no loadedOnce gate here, because there cannot be one that does
  // anything: load() sets state.loadedOnce and state.nodeReachable together
  // (the reachable branch sets the first, the line after it sets the second),
  // so `reachable && !loadedOnce` never holds and a clause testing it would be
  // an equivalent mutant. The #114 protection that does work — "no sessions" on
  // a read that never reached the node is not a fact about this machine — is
  // the step-level gate in onboardingSteps, which drops the sessions step
  // entirely until a read has landed.
  function onboardingTriggered() {
    if (!state.nodeReachable) return true;
    const status = state.service ?? {};
    if (status.supported === true && !(status.installed && status.running)) return true;
    if (state.sessions.length === 0) return true;
    if (state.nodes.length === 0) return true;
    return false;
  }

  // onboardingSteps is state in, step descriptors out, and nothing else: no
  // DOM, no reads, no writes. Everything that decides what an owner reads here
  // is therefore assertable without a render.
  function onboardingSteps() {
    const status = state.service ?? {};
    const steps = [];

    // 1. The node itself. Without ah this window cannot find out what holds the
    //    node, and restarting the process behind a launchd job or a systemd
    //    unit is how one node becomes two — so while the node is answering that
    //    case explains and offers nothing, exactly as the service panel does.
    //    A node that is NOT answering is the exception; the branch says why.
    if (status.toolError) {
      // Without ah this window cannot say what holds the node, so when the node
      // IS answering there is nothing here worth pressing. When it is not, there
      // is: RestartNode falls through to restartNodeProcess whenever the status
      // is not "supported and installed" (desktop/nodeprocess.go), and that path
      // stops whatever agenthub-node is running and starts the binary shipped
      // beside this app — it never runs ah. A missing ah used to cost the owner
      // the one button the card exists for, on a machine with a dead node.
      const down = !state.nodeReachable;
      steps.push({
        id: "service",
        title: down ? t("onboarding.service.titleStart") : t("onboarding.service.title"),
        body: down
          ? t("onboarding.service.bodyNoAhNodeDown", { error: status.toolError })
          : t("onboarding.service.bodyNoAh", { error: status.toolError }),
        done: false,
        actions: down ? [{
          label: t("onboarding.service.actionStart"),
          primary: true,
          run: () => restartNode().catch(() => {}),
        }] : [],
      });
    } else if (!state.nodeReachable) {
      // A node that is not answering is not a finished step, whatever the
      // service manager says about the job that is supposed to be holding it.
      // Deriving this step from state.service alone put a tick over a machine
      // showing "cannot reach http://127.0.0.1:7462", and left the card with
      // nothing to press on the one situation it exists for.
      steps.push({
        id: "service",
        title: t("onboarding.service.titleStart"),
        body: t("onboarding.service.bodyNodeDown"),
        done: false,
        actions: [{
          label: t("onboarding.service.actionStart"),
          primary: true,
          run: () => restartNode().catch(() => {}),
        }],
      });
    } else if (!state.service) {
      // The status has not landed yet. load() renders before ServiceStatus
      // answers, so for the first seconds of every launch this step knows
      // nothing — and "knows nothing" used to fall through to the else below
      // and read "Install the service" on a machine whose service was installed
      // and running, next to a title-bar pill that said so. A neutral sentence
      // and no button until the fact arrives; loadService() re-renders this
      // card when it does.
      steps.push({
        id: "service",
        title: t("onboarding.service.title"),
        body: t("onboarding.service.bodyChecking"),
        done: false,
        actions: [],
      });
    } else if (status.supported === false) {
      // Windows today: the node runs, nothing this app can ask holds it, and
      // the window starts and stops it itself. The node is answering — the
      // branch above has the case where it is not — so this is the done state,
      // and the button it keeps is the restart rather than the start.
      steps.push({
        id: "service",
        title: t("onboarding.service.titleStart"),
        body: t("onboarding.service.bodyUnsupported"),
        done: true,
        actions: [{
          label: t("onboarding.service.actionRestart"),
          primary: true,
          run: () => restartNode().catch(() => {}),
        }],
      });
    } else {
      const done = Boolean(status.installed && status.running);
      steps.push({
        id: "service",
        title: t("onboarding.service.title"),
        body: t("onboarding.service.body"),
        done,
        actions: done ? [] : [{ label: t("onboarding.service.action"), primary: true, run: () => goToService() }],
      });
    }

    // 2. A doorway to the drawer, which explains the rest itself — including
    //    「這台機器能不能被連到」, which used to be a step of its own here. It was
    //    a step about a listening address, two views from the drawer where the
    //    address is needed and read out, and the repair it offered is now in
    //    that drawer's own first step.
    const paired = state.nodes.length > 0;
    steps.push({
      id: "pair",
      title: t("onboarding.pair.title"),
      body: t("onboarding.pair.body"),
      done: paired,
      actions: paired ? [] : [{ label: t("onboarding.pair.action"), primary: true, run: () => goToPairing() }],
    });

    // 3. Text, and a button that points rather than acts. Opening the audience
    //    dialog with nothing selected is a dead dialog, so this one puts the
    //    keyboard on the checkbox that starts a selection.
    //
    //    With no session at all there is nothing to tick, and the card is on
    //    screen partly for that reason (onboardingTriggered) — so the step says
    //    so, where it would otherwise point at an empty table. Only after a read
    //    that reached the node: an empty list from one that did not is not a
    //    fact about this machine (#114).
    const published = (state.counts.all_paired ?? 0) + (state.counts.selected ?? 0) > 0;
    const noSessions = state.nodeReachable && state.sessions.length === 0;
    steps.push({
      id: "publish",
      title: t("onboarding.publish.title"),
      body: noSessions
        ? `${t("onboarding.publish.body")} ${t("onboarding.publish.noSessions")}`
        : t("onboarding.publish.body"),
      done: published,
      actions: published ? [] : [{
        label: t("onboarding.publish.action"),
        primary: false,
        run: () => el("select-all")?.focus?.(),
      }],
    });

    return steps;
  }

  // The step rows, by step id, and their buttons.
  //
  // The local view is repainted by the fifteen-second load() tick. Rebuilding
  // these rows on every tick would replace the button an owner is halfway
  // through clicking — the same defect updateCandidateRow exists for — so the
  // rows are updated in place and the container is written only when the set of
  // steps itself changes.
  const onboardingNodes = new Map();
  let onboardingAllDoneShown = false;
  // The farewell is spent by a TICK, not by a render.
  //
  // load() fires loadService() and renders; the status lands about fifty
  // milliseconds later and loadService re-renders this card. Hiding on the
  // second render therefore took the farewell off screen in that fifty
  // milliseconds, every time — nobody ever read it. So the flag the hide reads
  // is set by the next load() instead, which is fifteen seconds away.
  let onboardingFarewellSpent = false;

  // Called by load(), once per tick, before it renders. A farewell put up during
  // the previous tick has been on screen for that whole tick by now, so this
  // render is the one that puts the card away.
  function spendOnboardingFarewell() {
    if (onboardingAllDoneShown) onboardingFarewellSpent = true;
  }

  function onboardingStepNode(step, index) {
    let node = onboardingNodes.get(step.id);
    if (!node) {
      const row = element("div", "step");
      const tick = element("span", "steptick");
      const body = element("div", "stepbody");
      const title = element("div", "steptitle");
      const why = element("div", "stepwhy muted");
      const actions = element("div", "stepactions");
      body.append(title, why, actions);
      row.append(tick, body);
      node = { row, tick, title, why, actions, buttons: [] };
      onboardingNodes.set(step.id, node);
    }
    node.row.className = step.done ? "step done" : "step";
    node.tick.textContent = step.done ? "✓" : String(index + 1);
    node.tick.title = step.done ? t("onboarding.done") : t("onboarding.todo");
    node.title.textContent = step.title;
    node.why.textContent = step.body;
    // Buttons kept by position. A label or a handler can change under an
    // existing button — that is how a language switch reaches them — but the
    // element itself survives, so a click already in flight lands on the same
    // node it started on.
    while (node.buttons.length > step.actions.length) node.buttons.pop();
    for (let index_ = 0; index_ < step.actions.length; index_++) {
      const action = step.actions[index_];
      let button = node.buttons[index_];
      if (!button) {
        button = document.createElement("button");
        node.buttons[index_] = button;
      }
      button.className = action.primary ? "primary" : "ghost";
      button.textContent = action.label;
      // Every write in this window goes through the same flag: a second press
      // while a restart or a save is in flight starts a second one whose result
      // lands on top of the first.
      button.disabled = state.busy;
      button.onclick = () => action.run();
    }
    keepChildren(node.actions, node.buttons);
    return node.row;
  }

  function renderOnboarding() {
    const section = el("onboarding");
    const steps = onboardingSteps();
    let show = !state.ui.onboardingDismissed && onboardingTriggered();
    if (show) { onboardingAllDoneShown = false; onboardingFarewellSpent = false; }
    // Finished, and kept up for one full tick with every step ticked before it
    // goes. A card that vanishes under the click that completed it reads as a
    // glitch; one that stays forever is the thing people learn to ignore. The
    // guard is the spent flag, not the shown one, so every render inside that
    // tick — the status landing, a settings read answering — keeps it on screen
    // rather than being the one that takes it away. Never reopened afterwards:
    // once the card is hidden the class test below is false, and the settings
    // panel's own warnings and the pairing drawer's notices are where a
    // regression is said out loud.
    if (!show && !state.ui.onboardingDismissed && !onboardingFarewellSpent
      && !section.classList.contains("hidden") && steps.every((step) => step.done)) {
      show = true;
      onboardingAllDoneShown = true;
    }
    section.classList.toggle("hidden", !show);
    if (!show) return;
    // No node-settings read here any more. The step that needed one — the
    // listening address — is the pairing drawer's own first step now, and that
    // drawer asks for itself when it opens.
    // Said out loud on the one render that shows every tick. Ticks alone do not
    // explain why the card is about to disappear.
    el("onboarding-alldone").classList.toggle("hidden", !onboardingAllDoneShown);
    keepChildren(el("onboarding-steps"), steps.map(onboardingStepNode));
  }

  const VIEWS = ["local", "network", "settings"];

  function render() {
    renderOnboarding();
    for (const view of VIEWS) el(`${view}-view`).classList.toggle("hidden", state.view !== view);
    for (const segment of document.querySelectorAll("#view-switch span[data-view]")) {
      segment.className = segment.dataset.view === state.view ? "tab on" : "tab";
    }
    if (state.view === "network") {
      renderNodes();
      renderPairing();
    }
    if (state.view !== "settings") {
      // Away from the panel, the one-read-per-visit latch clears, so a panel
      // that could not reach the node tries again next time it is opened
      // rather than staying on 讀不到 until the button is pressed.
      state.nodeSettingsTried = false;
    }
    if (state.view === "settings") {
      renderSettings();
      // Read once when the view first opens, then only when asked: these
      // change when the owner changes them, not on their own.
      // Once per view, not once per render: a failed read leaves the baseline
      // null on purpose, and re-firing on every repaint would reset the panel
      // to 「讀取中…」 on every background poll.
      if (!state.nodeSettings && !state.nodeSettingsTried) {
        state.nodeSettingsTried = true;
        loadNodeSettings().catch(() => {});
      }
    }

    const rows = visible();
    renderChips();
    renderSortHeaders();
    renderRows(rows);
    el("tab-local-n").textContent = String(state.counts.total ?? state.sessions.length);
    renderInboxTotals();
    el("tab-network-n").textContent = String(state.nodes.length);
    el("match-count").textContent = rows.length === state.sessions.length
      ? plural(rows.length, "table.sessionCount")
      : t("table.matchCount", { shown: rows.length, total: state.sessions.length });

    // The selection bar floats over the table only while something is picked;
    // #select-all in the header is the way in, the bar is the way to act.
    const count = state.selected.size;
    el("selectionbar").classList.toggle("hidden", count === 0);
    el("selection-count").textContent = count ? plural(count, "table.selectedCount") : t("local.noneSelected");
    el("btn-audience").disabled = count === 0 || state.busy;
    el("btn-unpublish").disabled = count === 0 || state.busy;
    // The settings panel's own read and write are held off while ANY write is
    // in flight, not only a settings one: state.busy is the window's single
    // "something is being changed" flag. The case that matters is a reload
    // clicked during a save — it answers from before the restart but carries a
    // higher sequence number, so it would win the guard and repaint the form
    // with pre-restart values under a success banner.
    el("node-settings-reload").disabled = state.busy;
    el("node-settings-save").disabled = state.busy;
    // The service buttons too, now that pressing one can take several seconds:
    // a restart waits for the node to actually answer before it reports
    // anything, and a second press during that wait starts a second restart
    // whose result lands on top of the first one's. This is the same window in
    // which an owner used to press 「啟動節點」 three times because nothing
    // appeared to happen.
    for (const id of ["service-refresh", "service-restart", "service-open", "service-uninstall", "service-install"]) {
      el(id).disabled = state.busy;
    }

    const allPicked = rows.length > 0 && rows.every((s) => state.selected.has(s.id));
    const some = !allPicked && rows.some((s) => state.selected.has(s.id));
    for (const id of ["select-all", "select-all-visible"]) {
      const box = el(id);
      box.checked = allPicked;
      box.indeterminate = some;
    }
    el("select-label").textContent = t("local.selectAllFilteredCount", { n: rows.length });

    el("footer-left").textContent = t("footer.counts", {
      shown: rows.length,
      total: state.counts.total ?? 0,
      allPaired: state.counts.all_paired ?? 0,
      selected: state.counts.selected ?? 0,
      none: state.counts.none ?? 0,
    });
  }

  /* ---------------- settings view ---------------- */

  function renderSettings() {
    el("identity-node-id").textContent = state.localNodeId || "—";
    el("identity-fingerprint").textContent = state.localFingerprint || "—";
    el("identity-public-key").textContent = state.localPublicKey || "—";
    el("service-panel").classList.remove("hidden");
    el("toggle-backdrop").checked = state.ui.backdrop;
    el("toggle-motion").checked = state.ui.motion;
    el("toggle-motion").disabled = !state.ui.backdrop;
    el("appearance-state").textContent = describeBackdropState();
    // The control shows the language in use, not the stored override: with no
    // override stored the window is following the OS, and a blank box over a
    // window that is plainly in one language reads as a bug.
    el("settings-lang").value = language();
    for (const link of document.querySelectorAll("#settings-nav a")) {
      link.className = link.dataset.target === state.settingsSection ? "on" : "";
    }
  }

  // applyBackdrop paints the owner's switches onto <body>; the stylesheet does
  // the rest, and prefers-reduced-motion wins over the motion switch there.
  // The backdrop is the owner's two switches and nothing else. An earlier
  // version measured frame pacing and dropped the rain by itself; it is gone.
  // Guessing produced a background that changed state without being asked,
  // and the honest version of that judgement is a switch that starts off.
  function backdropPlan() {
    const photo = state.ui.backdrop;
    const rain = photo && state.ui.motion;
    return { photo, rain };
  }

  function applyBackdrop() {
    const plan = backdropPlan();
    // Built the first time it is switched on, not at boot: the rain starts off,
    // and an owner who leaves it off should never pay for 56 columns of DOM.
    // Before the <body> guard, because whether the columns are needed has
    // nothing to do with whether there is a body to put classes on.
    if (plan.rain) buildRain();
    const body = document.body;
    if (!body?.classList) return;
    body.classList.toggle("no-backdrop", !plan.photo);
    body.classList.toggle("no-motion", !plan.rain);
  }

  // describeBackdropState is what the settings page says out loud: what is
  // being drawn, and what the moving one costs, so the switch is a decision
  // and not a surprise.
  function describeBackdropState() {
    if (!state.ui.backdrop) return t("appearance.stateOff");
    if (!state.ui.motion) return t("appearance.statePhotoOnly");
    return t("appearance.stateBoth");
  }

  // buildRain makes the falling 0/1 columns once. Pure CSS animation after
  // that: nothing ticks in JS, and a hidden .rain costs nothing.
  function buildRain() {
    const rain = el("rain");
    if (!rain || rain.children.length > 0) return;
    const columns = 56;
    const fragment = document.createDocumentFragment();
    for (let i = 0; i < columns; i++) {
      const length = 28 + Math.floor(Math.random() * 20);
      let text = "";
      for (let j = 0; j < length; j++) text += (Math.random() < 0.5 ? "0" : "1") + "\n";
      const col = element("div", "col", text);
      col.style.left = `${((i + Math.random() * 0.6) * (100 / columns)).toFixed(2)}%`;
      col.style.animationDuration = `${(7 + Math.random() * 9).toFixed(1)}s`;
      col.style.animationDelay = `${(-Math.random() * 16).toFixed(1)}s`;
      col.style.opacity = (0.25 + Math.random() * 0.45).toFixed(2);
      col.style.fontSize = Math.random() < 0.2 ? "15px" : "12px";
      col.style.whiteSpace = "pre";
      fragment.append(col);
    }
    rain.append(fragment);
  }

  /* ---------------- pairing drawer ---------------- */

  // Opening the drawer opens the window, and dismissing it closes one with
  // nothing pending.
  //
  // The window used to be two buttons inside the drawer whose entire purpose is
  // that the window be open — so the commonest first run was: press 「配對另一
  // 台機器…」, read a panel that says nobody can get in, and not notice that the
  // thing it describes has to be started by a second button further down. The
  // node still owns the window's duration and its expiry; this only removes the
  // step of asking for one.
  async function openPairingDrawer() {
    el("pairing-modal").classList.remove("hidden");
    loadPairRequests().catch(() => {});
    await loadPairing();
    // Step 1 offers the settings form's own repair buttons, and
    // applyPeerListenRepair fills that real form, so the node's answer has to
    // be in it before any of them can be pressed.
    ensurePairingNodeSettings();
    await openPairingWindowIfNeeded();
  }

  // keepBanner is for a caller whose own result is already on the banner — the
  // repair from step 1, whose save and restart said what they did. A refused
  // OpenPairing used to replace that sentence, so the owner learned the window
  // did not open and lost whether the address they had just fixed was saved.
  // The failure is added after it instead.
  async function openPairingWindowIfNeeded({ keepBanner = false } = {}) {
    // The drawer can be dismissed while loadPairing is still on its way: a
    // window opened after that is one nobody asked for, and nothing on screen
    // would then close it.
    if (!pairingDrawerOpen()) return;
    const pairing = state.pairing;
    if (!pairing || state.busy) return;
    // A node that will not answer the window endpoints is not asked. The
    // fallback is for an answer from before that field existed.
    const windowAvailable = pairing.windowAvailable ?? (pairing.availability === "on");
    if (!windowAvailable || pairing.state?.open) return;
    try {
      // No duration: the node's own default is the one the node documents.
      await api.OpenPairing(0);
    } catch (error) {
      const failure = pairErrorMessage(error);
      const shown = el("banner");
      const before = keepBanner && !shown.classList.contains("hidden") ? shown.textContent : "";
      banner(before ? `${before} ${failure}` : failure);
      return;
    }
    await loadPairing();
    // Dismissed while OpenPairing was on its way (#194). dismissPairingDrawer
    // ran then, saw no open window and left; the window this call has just
    // opened would otherwise stay open for the node's whole default duration
    // with no drawer on screen to close it. So the dismissal is run again now
    // that there is something to close — with its own rule intact: a row
    // mid-exchange keeps the window open.
    if (!pairingDrawerOpen()) await dismissPairingDrawer({ windowOpened: true });
  }

  // closePairingDrawer only hides it. The window is left alone, because two
  // callers — the remedy button that goes to node settings, and the one that
  // opens the manual form — are steps in the middle of pairing rather than the
  // end of it.
  function closePairingDrawer() {
    el("pairing-modal").classList.add("hidden");
  }

  // dismissPairingDrawer is the owner saying they are done, and it closes the
  // window too — unless a row is mid-exchange. Somebody at another keyboard is
  // waiting on those, and a window shut under them is the one disappearance the
  // other machine actually feels.
  //
  // The rows are read again first rather than taken from state.pairRequests,
  // which the two-second poll last filled: a request that arrived since would
  // be expired by the node the moment the window closed (ExpirePending,
  // internal/api/pair.go). A read that fails leaves the window to its own
  // timeout, because it cannot say nobody is waiting.
  //
  // windowOpened is the caller knowing better than state.pairing: the drawer's
  // own OpenPairing has just succeeded, and a read of the window that failed
  // afterwards must not be taken as "nothing to close".
  async function dismissPairingDrawer({ windowOpened = false } = {}) {
    closePairingDrawer();
    const open = () => windowOpened || Boolean(state.pairing?.state?.open);
    if (state.busy || !open()) return;
    await loadPairRequests({ render: false });
    // Reopened while the read was out: the owner is not done after all.
    if (pairingDrawerOpen() || state.pairRequestsError) return;
    const pending = (state.pairRequests ?? []).some(
      (request) => request.state === "pending" || request.state === "awaiting-confirm");
    if (pending || state.busy || !open()) return;
    try {
      await api.ClosePairing();
    } catch {
      // The node closes the window itself when its time runs out, so a refusal
      // here costs a few minutes of announcing rather than anything an owner
      // has to act on — and the drawer it would be reported in is gone.
      return;
    }
    await loadPairing();
  }

  // Asked once while the drawer is open, for the same reason the checklist asks
  // once per window: a read on every render would re-fire on every tick.
  let pairingSettingsAsked = false;
  function ensurePairingNodeSettings() {
    if (state.nodeSettings || pairingSettingsAsked || !state.nodeReachable) return;
    pairingSettingsAsked = true;
    const done = () => {
      // A read that produced no baseline releases the latch, so the next time
      // the drawer is opened it asks again rather than offering nothing for the
      // life of the window.
      if (!state.nodeSettings) pairingSettingsAsked = false;
      renderPairHere();
    };
    loadNodeSettings().then(done, done);
  }

  // pairingDrawerOpen is what the two-second poll asks. The exchange's rows are
  // read only while somebody is looking at them, because reading them makes the
  // node dial every machine it is waiting on.
  function pairingDrawerOpen() {
    return !el("pairing-modal").classList.contains("hidden");
  }

  // renderPairingSummary is the one-line state in the node list's foot: it
  // says whether a window is open and how many machines are advertising, so
  // the owner knows whether opening the drawer is worth it.
  function renderPairingSummary() {
    const pill_ = el("pairing-summary");
    const line = el("pairing-summary-line");
    const pairing = state.pairing;
    if (!pairing) {
      pill_.className = "pill";
      pill_.textContent = t("network.pairingLoading");
      line.textContent = "";
      return;
    }
    // The window is open and nobody can find this machine on the network. Its
    // own state because the remedies differ: "off" is a flag and a restart,
    // this one is an address read off this screen and typed on the other.
    if (pairing.availability === "openNotAnnouncing") {
      pill_.className = "pill idle";
      pill_.textContent = t("network.summaryPairing", { left: clock(pairingRemaining()) });
      // Same sentence the drawer had to stop telling: "hand them the address
      // shown there" is advice about an address this node may not have. A
      // default node has none, and the summary line was sending its owner to
      // read out something that was never on the screen.
      line.textContent = pairHereState(pairing.state ?? {}).reachable
        ? t("network.summaryOpenNotAnnouncing")
        : t("network.summaryOpenUnreachable");
      return;
    }
    if (pairing.availability === "off") {
      pill_.className = "pill";
      pill_.textContent = t("network.summaryOff");
      line.textContent = t("network.summaryOffLine");
      return;
    }
    if (pairing.availability !== "on") {
      pill_.className = "pill bad";
      pill_.textContent = t("network.summaryUnreadable");
      line.textContent = t("network.summaryUnreadableLine");
      return;
    }
    const open = Boolean(pairing.state?.open);
    const candidates = pairing.candidates?.length ?? 0;
    const flagged = (pairing.candidates ?? []).filter((c) => c.contested || c.duplicate).length;
    pill_.className = open ? "pill idle" : "pill";
    pill_.textContent = open
      ? t("network.summaryAnnouncing", { left: clock(pairingRemaining()) })
      : t("network.summaryClosed");
    line.textContent = candidates === 0
      ? t("network.summaryNoCandidates")
      : plural(candidates, "network.summaryCandidates") +
        (flagged ? t("network.summaryFlagged", { flagged }) : t("network.summaryStop"));
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

  // Reads are numbered so a slow one cannot overwrite a fast one — the same
  // guard loadPairing() uses, and for the same reason now that this one is polled
  // every 15 seconds as well.
  //
  // The owner changing an audience or revoking a node runs its own load(), and a
  // background read that was already awaiting api.Overview() when they clicked can
  // answer after it. Skipping the tick while state.busy is set only covers reads
  // that have not started yet; one already in flight still lands, describing the
  // moment before the change.
  let overviewRequest = 0;
  let overviewApplied = 0;
  // Set before the scan rather than after it, so a slow Discover cannot be
  // started twice by the fifteen-second tick landing in the middle of it.
  let autoDiscoverTried = false;

  // Anything the owner is in the middle of that a repainted table would pull out
  // from under them: a write in flight, rows selected that a rescan could drop,
  // or a dialog standing on top of the list. The periodic tick and the moment a
  // background read lands both ask this — one list of conditions, checked twice,
  // because the state can change while the read is in the air.
  const MODAL_IDS = ["audience-modal", "pair-modal", "inbox-modal", "pairing-modal", "mcp-modal", "modal", "confirm-modal"];
  function anyModalOpen({ exceptPairingDrawer = false } = {}) {
    return MODAL_IDS.some((id) => {
      if (exceptPairingDrawer && id === "pairing-modal") return false;
      return !el(id).classList.contains("hidden");
    });
  }

  // Typing is one of those conditions. A refresh that rebuilds a panel while the
  // owner is in one of its fields does not merely change text around them: it
  // replaces the element they are typing into, and the replacement arrives
  // unfocused with the caret at the start. The address field kept a draft of what
  // had been typed (see addressSection) and that still could not save the caret,
  // because by then the field the caret was in no longer existed. Asking here
  // instead protects every field in this window, including ones not written yet,
  // and leaves the draft as the second line of defence for the reads that do
  // apply.
  //
  // Every field is covered, the search box included — a search being retyped
  // under the owner is the same annoyance as an address being retyped. The
  // checkboxes and radios are not: they are not typed into, they all live in
  // dialogs that already hold the tick off, and a focus ring is not a caret.
  const EDITABLE_TAGS = new Set(["textarea", "select"]);
  const EDITABLE_INPUT_TYPES = new Set([
    "text", "search", "url", "email", "tel", "number", "password",
  ]);
  function fieldHasFocus() {
    const active = document.activeElement;
    if (!active) return false;
    const tag = String(active.tagName || "").toLowerCase();
    if (EDITABLE_TAGS.has(tag)) return true;
    if (tag !== "input") return false;
    // An <input> with no type attribute is a text input, which is what the
    // property answers in a browser; the fallback is for a DOM that does not.
    return EDITABLE_INPUT_TYPES.has(String(active.type || "text").toLowerCase());
  }
  // exceptPairingDrawer is for the pairing drawer's own two-second tick. The
  // drawer is a modal, so it held the fifteen-second refresh off for as long as
  // it was open — and the list of paired nodes it sits over is exactly what
  // `ah revoke` in a terminal changes while somebody is looking at it. The other
  // guards still apply: a decision in flight, rows selected, or a caret in a
  // field all stop the read, because those are the reads that pull the ground
  // out from under someone.
  function interactionInProgress({ exceptPairingDrawer = false } = {}) {
    return state.busy || state.selected.size > 0
      || anyModalOpen({ exceptPairingDrawer }) || fieldHasFocus();
  }

  // background: this read is the 15-second tick's, not the owner's. A background
  // read is abandoned if the owner started interacting while it was in flight;
  // a foreground read — startup, 「重新整理」, the reload after a mutation — is
  // what the owner asked for and always applies.
  // Answers whether it reached the screen. The pairing drawer's own tick is the
  // one caller that has to know: it draws the request rows itself when this read
  // was abandoned, and leaves them to this render when it was not, so one tick
  // renders them once rather than twice.
  async function load({ background = false, exceptPairingDrawer = false } = {}) {
    const sequence = ++overviewRequest;
    const overview = await api.Overview();
    if (sequence <= overviewApplied) {
      // A later read already landed. This one describes an older moment.
      return false;
    }
    if (background && interactionInProgress({ exceptPairingDrawer })) {
      // The guards passed when this tick fired, but the owner has since selected
      // rows or opened a dialog. Applying it now would redraw the table under
      // them, so the answer is thrown away whole — no state, no render, no
      // banner.
      //
      // overviewApplied deliberately does not move. It means "the newest read
      // whose contents are on screen", and nothing from this one is; advancing it
      // would make the next foreground read — which carries a lower number only
      // because it started later than this abandoned one did — look stale and be
      // dropped too. Leaving it put is also right for a read still in flight from
      // before this one: nothing from this tick reached the screen, so that older
      // read is still newer than what is displayed and should land.
      return false;
    }
    overviewApplied = sequence;
    const reachable = Boolean(overview.reachable);

    // Only a read that reached the node may replace what is on screen. A node
    // that is mid-rescan answers unreachable with no sessions, and copying that
    // emptiness in blanked a window showing 1083 sessions until someone pressed
    // refresh (issue #114). Keeping the last known lists costs a few seconds of
    // staleness, which the banner says out loud; clearing them costs the owner
    // every session they were looking at.
    if (reachable) {
      state.sessions = overview.sessions || [];
      state.nodes = overview.nodes || [];
      state.counts = overview.counts || {};
      state.localFingerprint = overview.node?.fingerprint || "";
      // The string the person at the other keyboard has to type into their own
      // pairing dialog. The node has always answered with it and this window
      // never read it, so the only way to get it was `ah node` in a terminal on
      // this machine — and copying it by hand is how a trailing "=" was lost on
      // 2026-09-10, leaving "public key is not a valid Ed25519 key" as the only
      // explanation anyone got.
      state.localPublicKey = overview.node?.publicKey || "";
      // Needed to tell this machine's own messages from a peer's. Without it a
      // qualified sender naming this node reads as a peer, and a bare one reads as
      // local — which is the dangerous direction.
      state.localNodeId = overview.node?.id || "";
      state.nodeAutoWake = Boolean(overview.node?.autoWake);
      state.peers = overview.peers ?? [];
      state.presenceError = overview.presenceError ?? "";
      // A reachable Overview carries an error only when its pairing-list read
      // failed (desktop/app.go); every other failure answers unreachable.
      state.nodesError = overview.error ?? "";
      state.loadedOnce = true;
      if (state.selectedNode && !state.nodes.some((node) => node.nodeId === state.selectedNode)) {
        state.selectedNode = null;
      }
    }

    el("conn-dot").className = reachable ? "dot ok" : "dot bad";
    state.nodeLine = {
      reachable,
      displayName: overview.node?.displayName ?? "",
      platform: overview.node?.platform ?? "",
      nodeUrl: overview.nodeUrl ?? "",
    };
    renderNodeLine();
    el("footer-right").textContent = reachable ? overview.node.id : "";

    if (!reachable) {
      // The banner has to say which of the two situations this is, or a stale
      // list reads as the current truth.
      const shown = state.loadedOnce ? t("app.showingStale") : t("app.neverLoaded");
      banner(t("app.notConnected", { error: overview.error || "unknown error", shown }));
    } else {
      hideBanner();
    }
    state.nodeReachable = reachable;
    loadService().catch(() => {});

    // The scan that used to be step 2 of the checklist.
    //
    // 「Press this button once」 is not a step. It is the one thing this window
    // can do on its own the moment it has a node to ask, and a first launch is
    // exactly the case: the table is empty because nobody has scanned yet, not
    // because there is nothing on the disk. Once per window, on the first read
    // that actually REACHED the node — an empty list from a read that did not
    // is not a fact about this machine (#114) — and the result goes to the
    // banner discoverSessions already writes, which now also says where
    // AgentHub looks when it finds nothing.
    //
    // Not while something else holds the window: discoverSessions goes through
    // withBusy, which drops a call made while busy, so spending the flag then
    // spent the window's only scan on nothing — and installService calls load()
    // in the middle of its own busy stretch, on exactly the first launch this
    // scan is for (#193 review). Left for the next load that is not busy.
    if (reachable && !autoDiscoverTried && !state.busy && state.sessions.length === 0) {
      autoDiscoverTried = true;
      discoverSessions().catch(() => {});
    }

    // The badges, read once behind the list rather than once per row (#146).
    //
    // Under this load's own sequence number, like everything above: a slow
    // counts read that answers after a newer load has landed describes an older
    // moment, and the rows it would be painted onto are already gone. The read
    // never rejects — a failure is "unknown", which hides the badges rather
    // than zeroing them, and leaves the numbers it had.
    if (reachable) {
      const counts = await readInboxCounts();
      if (sequence < overviewApplied) return false;
      // The interaction guard above ran before this round-trip, not after it.
      // An owner who started selecting rows, opened a dialog or put a caret in
      // a field while the counts were in flight would otherwise be redrawn
      // under by a read they never asked for — the same abandonment as above,
      // asked again on the way back rather than only on the way out.
      if (background && interactionInProgress({ exceptPairingDrawer })) return false;
      applyInboxCounts(counts);
    } else {
      // The node is not answering, so nothing can be said about any inbox.
      applyInboxCounts({ ok: false, counts: {} });
    }

    // Drop selections that no longer exist after a rescan.
    const alive = new Set(state.sessions.map((s) => s.id));
    for (const id of [...state.selected]) if (!alive.has(id)) state.selected.delete(id);

    // One tick, one chance to spend the farewell: whatever this render decides,
    // a card that said goodbye during the previous tick has been read by now.
    spendOnboardingFarewell();
    render();
    return true;
  }

  // renderNodeLine writes the title bar's one line about the node from
  // state.nodeLine, never from a closure over one load(). The unreachable half
  // of it is a translated sentence, so this has to be re-runnable: after a
  // language switch paintStatic has just put the placeholder key back on the
  // element, and there is no second load() coming.
  function renderNodeLine() {
    const line = state.nodeLine;
    if (!line) return;
    // The app's own version rides on this line in both states: the half of the
    // bug reports worth having are the ones where the node is not reachable.
    const version = state.appVersion ? ` · ${state.appVersion}` : "";
    el("node-line").textContent = line.reachable
      ? `${line.displayName} · ${line.platform} · ${line.nodeUrl}${version}`
      : t("app.unreachable", { url: line.nodeUrl, version });
  }

  // askConfirm asks one yes-or-no question in the window's own dialog, and
  // answers whether the owner said yes.
  //
  // Not window.confirm, because in the shipped macOS app window.confirm never
  // asks anything. Wails v2 makes itself the WKWebView's WKUIDelegate and
  // implements only the file picker from that protocol (WailsContext.m,
  // runOpenPanelWithParameters); WebKit treats a delegate without
  // runJavaScriptConfirmPanelWithMessage as the owner pressing Cancel, so every
  // destructive button behind a confirm() — clearing an inbox, removing the
  // service, moving the database — did nothing, silently. The browser mock and
  // the node checks both answered confirm() themselves, so neither could see it.
  //
  // One question at a time: a second one asked while the first is open waits
  // behind it and is shown once the first is answered. It used to answer the
  // first "no" instead, so anything that asked while the owner was reading —
  // a background path, a second button reached by Tab — silently turned their
  // pending decision into a cancel. Esc, 取消 and a click on the backdrop are
  // all "no". The body
  // keeps its line breaks. A dangerous confirm button is drawn red and does
  // not start with the keyboard on it, so a stray Enter cancels rather than
  // deletes.
  //
  // The backdrop answers only a press that began on it after the question was
  // already up. The dialog opens inside the first click and covers the whole
  // window, so the second half of a double-click on 清空收件匣… or 儲存 lands
  // on the backdrop: counted as "no", it closed the question before it could
  // be read, and on 儲存 it answered the pinned-settings question for the
  // owner. So a backdrop click counts only when its pointerdown was on the
  // backdrop too, that pointerdown came CONFIRM_BACKDROP_GRACE_MS or more after
  // the dialog opened, and the click is not the second of a multi-click.
  const CONFIRM_BACKDROP_GRACE_MS = 400;
  const confirmNow = () => globalThis.performance?.now?.() ?? Date.now();
  let confirmPending = null;
  const confirmQueue = [];
  function askConfirm(question) {
    if (confirmPending) return new Promise((resolve) => confirmQueue.push({ question, resolve }));
    return showConfirm(question);
  }
  function showConfirm({ title, body = "", confirmLabel = t("common.confirm"), danger = false }) {
    const modal = el("confirm-modal");
    const ok = el("confirm-ok");
    const cancel = el("confirm-cancel");
    el("confirm-title").textContent = title;
    el("confirm-body").textContent = body;
    el("confirm-body").classList.toggle("hidden", !body);
    ok.textContent = confirmLabel;
    ok.className = danger ? "danger" : "primary";
    const before = document.activeElement;
    modal.classList.remove("hidden");
    return new Promise((resolve) => {
      const finish = (answer) => {
        if (confirmPending !== finish) return;
        confirmPending = null;
        modal.classList.add("hidden");
        if (before && typeof before.focus === "function") before.focus();
        resolve(answer);
        const next = confirmQueue.shift();
        if (next) showConfirm(next.question).then(next.resolve);
      };
      confirmPending = finish;
      ok.onclick = () => finish(true);
      cancel.onclick = () => finish(false);
      const openedAt = confirmNow();
      let pressedBackdrop = false;
      modal.onpointerdown = (event) => {
        pressedBackdrop = event?.target === modal
          && confirmNow() - openedAt >= CONFIRM_BACKDROP_GRACE_MS;
      };
      modal.onclick = (event) => {
        const started = pressedBackdrop;
        pressedBackdrop = false;
        if (event?.target !== modal || !started) return;
        if (Number(event?.detail) > 1) return;
        finish(false);
      };
      (danger ? cancel : ok).focus();
    });
  }
  // Esc answers "no" wherever the keyboard is: a click on the question's own
  // text moves focus out of both buttons, and the dialog must still close.
  //
  // And Tab stays in the question. The dialog says aria-modal, but nothing held
  // the keyboard to it: two presses of Tab reached the title bar's service pill
  // behind the backdrop, where Enter acted on a window the owner could not
  // see. So Tab and Shift-Tab go round the dialog's two buttons, and from
  // anywhere else in it — the question's text, after a click — to the first
  // or last of them.
  function confirmKey(event) {
    if (!confirmPending) return;
    if (event?.key === "Tab") {
      const order = [el("confirm-cancel"), el("confirm-ok")];
      const at = order.indexOf(document.activeElement);
      const step = event.shiftKey ? -1 : 1;
      const next = at === -1 ? (step > 0 ? 0 : order.length - 1) : (at + step + order.length) % order.length;
      event.preventDefault?.();
      order[next].focus();
      return;
    }
    if (event?.key !== "Escape") return;
    event.preventDefault?.();
    event.stopPropagation?.();
    confirmPending(false);
  }
  if (typeof document.addEventListener === "function") document.addEventListener("keydown", confirmKey, true);

  async function withBusy(label, fn) {
    // Ignored while another one is running. Disabling buttons covers only the
    // ids render() knows about, and the repair buttons are created on the fly —
    // so the ones most likely to be pressed twice were the ones not covered.
    // Two of these overlapping is two save-and-restart sequences whose results
    // land on top of each other, and the second answer describes a node the
    // first one has already replaced.
    if (state.busy) return;
    state.busy = true;
    render();
    try {
      await fn();
    } catch (error) {
      banner(t("busy.failed", { action: label, error }));
    } finally {
      state.busy = false;
      render();
    }
  }

  // mode is the audience's own mode, not a sentence: the two languages put the
  // verb and the count in different places, so each one gets its own key rather
  // than a noun glued to a template.
  async function applyAudience(audience, mode) {
    const ids = [...state.selected];
    await withBusy(t("audience.verb." + mode), async () => {
      const result = await api.SetAudience(ids, audience);
      await load();
      if (result.failed > 0) {
        banner(t("audience.partlyApplied", {
          action: t("audience.verb." + mode),
          changed: result.changed,
          failed: result.failed,
          error: (result.errors || [])[0] || "",
        }));
      } else {
        state.selected.clear();
        closeAudienceModal();
        banner(plural(result.changed, "audience.applied." + mode), true);
      }
    });
  }

  /* ---------------- network view ---------------- */

  /* ---------------- pairing mode and candidates ---------------- */

  // Every user-facing string of the pairing exchange, in one object.
  //
  // The object is now a window onto the `pair.*` slice of the active language
  // table (src/i18n/). It is kept because the renderers and the node checks
  // both reach for it by name, and because every property is a live getter the
  // whole panel changes language without anything re-reading this object.
  //
  // The state wording mirrors `ah pair pending` deliberately: two owners on two
  // machines, one in a terminal and one in this window, have to be able to say
  // the same thing to each other about the same row.
  const pairGroup = (prefix, keys) => {
    const group = {};
    for (const key of keys) {
      Object.defineProperty(group, key, {
        get: () => t(prefix + "." + (key.startsWith("_") ? key.slice(1) : key)),
        enumerable: true,
      });
    }
    return group;
  };
  const PAIR_TEXT = pairGroup("pair", [
    "open", "close", "windowOpen", "windowClosed", "windowExpiring", "windowUnavailable",
    "hereHeading", "hereNote", "hereNoteAnnouncing", "hereNoAddress", "hereNoAddressWhy",
    "hereFixHeadline", "hereUnreachable", "hereFix",
    "drawerSubAnnouncing", "drawerSubNotAnnouncing", "drawerSubUnreachable", "drawerSubUnknown",
    "windowOpenUnreachable", "hereCopied", "hereCopyFailed", "closeEnds",
    "send", "sendFromCandidate", "sendManual", "addressEmpty", "addressNote", "sent",
    "showDecided", "requestsEmpty", "requestsEmptyAll", "requestsUnread",
    "requestsFailed", "approve", "confirm", "reject", "nodeSaid",
  ]);
  // The one sentence about comparing, said once per undecided row; the step
  // line under the fingerprints no longer repeats it. An array because that is
  // what reads it. Why it matters is in the 「說明」 folded under it
  // (whyDetails("why.compareFingerprints")).
  Object.defineProperty(PAIR_TEXT, "compare", {
    get: () => [t("pair.compare")],
    enumerable: true,
  });
  PAIR_TEXT.state = pairGroup("pair.state", [
    "pending-incoming", "pending-outgoing", "awaiting-confirm", "approved", "rejected", "expired",
  ]);
  PAIR_TEXT.step = pairGroup("pair.step", [
    "pending-incoming", "pending-outgoing", "awaiting-confirm", "approved-incoming",
    "approved-outgoing", "rejected", "rejected-fingerprint-mismatch", "expired", "displaced",
  ]);
  PAIR_TEXT.decided = pairGroup("pair.decided", ["approve", "confirm", "reject"]);
  // The node's own labels, mapped one-for-one. A fixed table, so a string from
  // the wire can never choose the words around it — and the fingerprint values
  // themselves are rendered exactly as the node ordered them.
  PAIR_TEXT.whose = {};
  Object.defineProperty(PAIR_TEXT.whose, "this machine", {
    get: () => t("pair.whose.this-machine"), enumerable: true,
  });
  Object.defineProperty(PAIR_TEXT.whose, "the other machine", {
    get: () => t("pair.whose.the-other-machine"), enumerable: true,
  });
  PAIR_TEXT.role = pairGroup("pair.role", ["requester", "receiver"]);
  // What the node's refusals mean, and what to do about each. The node's own
  // sentences are English and name `ah` subcommands, which is the wrong advice
  // in a window with buttons. Keyed by the node's code, which is a contract
  // (docs/ui-contract.md §4.3).
  PAIR_TEXT.errors = pairGroup("pair.errors", [
    "PEER_TOO_OLD", "PEER_PAIRING_CLOSED", "PEER_PAIRING_DUPLICATE", "PAIRING_BUSY",
    "PAIRING_DUPLICATE", "PAIRING_STATE", "PAIRING_EXCHANGE_DISABLED", "PEER_UNREACHABLE",
    "PEER_KEY_MISMATCH", "ADDRESS_NOT_ALLOWED", "NOT_FOUND",
  ]);

  // Every field of a candidate was chosen by whoever sent the packet, on a
  // multicast group anyone on the segment can write to. So this whole panel is
  // built with element(), which assigns textContent, and none of these values is
  // ever allowed to decide a class name.
  function candidateName(candidate) {
    const name = (candidate.displayName || "").trim();
    return name === "" ? t("pair.noName") : name;
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
    renderPairHere();
    renderCandidates();
    renderPairRequests();
    renderPairingSummary();
  }

  /* ---------------- the pairing exchange (#63) ---------------- */

  // pairErrorMessage turns one of the node's refusals into a sentence with a
  // remedy in it.
  //
  // The node answers "CODE: message", and its message is English and names `ah`
  // subcommands — right for the terminal it was written for, wrong in a window
  // whose remedy is a button three centimetres away. A code this table does not
  // know keeps the node's own words: a refusal nobody translated is still an
  // answer, and swallowing it would leave the owner with nothing.
  function pairErrorMessage(error) {
    const text = String(error?.message ?? error ?? "");
    // The code the node put at the very start, ahead of ": ". Anchored there
    // rather than searched for anywhere: a substring match had PAIRING_BUSY
    // answering for PEER_PAIRING_BUSY — a sentence about this machine's list
    // shown for the other machine's — and it would also let a peer's message
    // quoting a code pick the sentence shown about it.
    const named = /^([A-Z][A-Z0-9_]{2,}): ([\s\S]+)$/.exec(text.trim());
    if (named && Object.hasOwn(PAIR_TEXT.errors, named[1])) {
      return PAIR_TEXT.errors[named[1]];
    }
    // No sentence for this one, so the node's own words stand — without the
    // code in front of them, which is for a log and not for a person.
    //
    // PEER_PAIRING_BUSY is deliberately not in that table: its body carries the
    // peer's own reason and the remedy, and the 429 it relays covers two
    // different refusals that are undone in different places. A sentence
    // written here would have to pick one of them and be wrong about the other.
    return named ? named[2] : text;
  }

  // sendPairRequest asks the machine at `address` to trust this one.
  //
  // The address is passed in rather than read from the form, because three
  // things call this: the form, a candidate row's button, and nothing else may
  // invent one. It is not validated here — the node holds the ranges this build
  // will talk to, and a second rule in this process could only disagree with
  // the one that actually decides.
  async function sendPairRequest(address) {
    const wanted = String(address ?? "").trim();
    if (wanted === "") {
      banner(PAIR_TEXT.addressEmpty);
      return;
    }
    await withBusy(t("pair.send"), async () => {
      try {
        await api.StartPairRequest(wanted);
      } catch (error) {
        banner(pairErrorMessage(error));
        return;
      }
      el("pair-address").value = "";
      banner(PAIR_TEXT.sent, true);
      await loadPairRequests();
    });
  }

  // decidePairRequest is approve, confirm and reject, which differ only in
  // which binding they call.
  //
  // The id comes from the row the button was built on, never from a field: an
  // id typed or pasted is an id that can name somebody else's request.
  async function decidePairRequest(id, verb) {
    const call = { approve: api.ApprovePairRequest, confirm: api.ConfirmPairRequest, reject: api.RejectPairRequest }[verb];
    if (!call) return;
    const label = t("pair.busy." + verb);
    await withBusy(label, async () => {
      let answer;
      try {
        answer = await call(id);
      } catch (error) {
        banner(pairErrorMessage(error));
        await loadPairRequests();
        return;
      }
      // What happened, said here in the window's own language, with the node's
      // own sentence kept behind it. The node's nextStep is English and names
      // `ah` subcommands, and it is the one thing this window cannot work out:
      // after a refusal it says whether the other machine could be told, and a
      // refusal it could not deliver leaves that machine trusting a key this
      // owner has just refused. So it is neither shown alone nor dropped.
      const said = pairDecisionMessage(answer, verb);
      if (said) banner(said, verb !== "reject");
      await loadPairRequests();
      // Approving or confirming writes the trust store, so the node list is
      // now out of date.
      if (verb !== "reject") await load();
    });
  }

  // pairDecisionMessage is the answer to a button press: this window's sentence
  // first, the node's English detail after it in brackets.
  function pairDecisionMessage(answer, verb) {
    const local = (answer ? pairStepText(answer) : "") || PAIR_TEXT.decided[verb] || "";
    const detail = String(answer?.nextStep ?? "").trim();
    if (local === "") return detail;
    return detail === "" ? local : `${local}（${PAIR_TEXT.nodeSaid}：${detail}）`;
  }

  // pairStateKey is the row's state as this panel talks about it: the two
  // pending states are different situations on the two machines, and one word
  // for both is one word that is wrong on one of them.
  function pairStateKey(request) {
    if (request.state === "pending") return `pending-${request.direction === "outgoing" ? "outgoing" : "incoming"}`;
    return request.state;
  }

  function pairStepText(request) {
    const key = pairStateKey(request);
    if (request.state === "approved") {
      return PAIR_TEXT.step[`approved-${request.direction === "incoming" ? "incoming" : "outgoing"}`];
    }
    if (request.state === "expired" && request.reason === "displaced") return PAIR_TEXT.step.displaced;
    // The node distinguishes a refusal on the fingerprints from any other, and
    // it is the one refusal that says something about the network rather than
    // about somebody's decision. Shown as itself.
    if (request.state === "rejected" && request.reason === "fingerprint_mismatch") {
      return PAIR_TEXT.step["rejected-fingerprint-mismatch"];
    }
    return PAIR_TEXT.step[key] ?? "";
  }

  // fingerprintSignature is what "the same two values, in the same order, with
  // the same labels" means. Anything else and the block is rewritten; the box
  // itself never is, because it is the thing two people are reading off two
  // screens while this window ticks underneath them.
  function fingerprintSignature(request) {
    const rows = Array.isArray(request.fingerprints) ? request.fingerprints : [];
    return JSON.stringify(rows.map((row) => [row.role, row.machine, row.whose, row.fingerprint]));
  }

  function writeFingerprintBlock(box, request) {
    const rows = Array.isArray(request.fingerprints) ? request.fingerprints : [];
    const kids = [];
    if (rows.length === 0) {
      // A node that answered without the ordered pair. Nothing is invented
      // here: two values in an order this window guessed at are exactly the
      // thing the comparison cannot survive.
      kids.push(element("div", "muted", t("pair.noFingerprintPair")));
      box.replaceChildren(...kids);
      return;
    }
    for (const row of rows) {
      const who = element("div", "who");
      const role = PAIR_TEXT.role[row.role] ?? row.role ?? "";
      if (role) who.append(element("span", "", `${role}　`));
      who.append(element("span", "", row.machine || t("pair.noName")));
      const whose = PAIR_TEXT.whose[row.whose];
      // Only the two labels the node documents get the highlight; anything else
      // is shown as the plain text it is.
      if (whose) who.append(element("span", row.whose === "this machine" ? "mine" : "", `（${whose}）`));
      else if (row.whose) who.append(element("span", "", `（${row.whose}）`));
      kids.push(who);
      kids.push(element("div", "fingerprint", row.fingerprint || ""));
    }
    box.replaceChildren(...kids);
  }

  // pairRequestRow is one exchange, with the decision below the values it is
  // about.
  //
  // The order on the row is the order of the acts: who is asking, the two
  // fingerprints, what to do, then the buttons. A button level with the
  // fingerprints is one that can be pressed before they have been read, and
  // there is no affordance anywhere for deciding without comparing — that is
  // the whole of what this exchange asks a person for.
  function pairRequestRow(request) {
    const row = element("div", "pairrow");
    const line = element("div", "line");
    const name = element("span", "name");
    const statePill = pill("");
    line.append(name, statePill);
    const meta = element("div", "meta");
    const nodeId = element("div", "fingerprint");
    // The request id, because it is the handle the other surface uses: an owner
    // holding this window and a terminal has to be able to tell that the row
    // here and the row `ah pair pending` prints are the same exchange.
    const id = element("div", "meta");
    // The notice once, above the block it describes — the layout the node's own
    // wording assumes ("two fingerprints are shown: the requester first…"). Put
    // below them it read as a comment on the decision rather than as the
    // instruction for reading the two lines above it. In its own box so it can
    // be filled and emptied as the row's state changes without the rest of the
    // row being rebuilt around it.
    const compare = element("div", "compare");
    const fingerprints = element("div", "fingerprints");
    const step = element("div", "nextstep");
    // The node's own next step, on a finished row only.
    //
    // It is kept because one finished row carries something this window cannot
    // derive: a refusal the node could not deliver names the machine that may
    // still be trusting this one, and what to ask its owner to run. On an
    // undecided row it says "run: ah pair approve <id>" — correct advice for
    // the terminal it was written for, and in a window whose approve button is
    // three centimetres below it, an instruction that contradicts the screen.
    // Those are the ones people stop reading.
    const nodeStep = element("div", "muted");
    // The decision sits below the values it is about: a button level with the
    // fingerprints is one that can be pressed before they have been read, and
    // there is no affordance anywhere for deciding without comparing — that is
    // the whole of what this exchange asks a person for.
    //
    // Both buttons are built once and kept for the life of the row. The state
    // decides which of them is in the box, never whether they exist: pending →
    // awaiting-confirm relabels this same primary button and rebinds it, so an
    // owner already reaching for it is reaching for the same element when the
    // other machine answers.
    const buttons = element("div", "decide");
    const primary = element("button", "primary");
    const reject = element("button", "ghost", PAIR_TEXT.reject);
    row.append(line, meta, nodeId, id, compare, fingerprints, step, nodeStep, buttons);
    row.pairParts = { name, statePill, meta, nodeId, id, compare, fingerprints, step, nodeStep, buttons, primary, reject };
    // Neither written yet. Both differ from every value updateRequestRow can
    // compute, so the first update fills them.
    row.pairCompare = null;
    row.pairFingerprints = null;
    updateRequestRow(row, request);
    return row;
  }

  // updateRequestRow writes this request into an existing row.
  //
  // Same reason as updateCandidateRow, and more of it. This list is re-rendered
  // twice per two-second tick — loadPairRequests() ends in renderPairRequests and
  // so does the overview render behind it — and what these rows carry is the one
  // irreversible decision in this window. A rebuilt 「指紋一致，核准」 loses the
  // focus on it, and a press whose mousedown and mouseup land on either side of
  // the rebuild never becomes a click at all.
  function updateRequestRow(row, request) {
    const parts = row.pairParts;
    const undecided = request.state === "pending" || request.state === "awaiting-confirm";
    row.className = undecided ? "pairrow waiting" : "pairrow";
    parts.name.textContent = request.displayName || request.nodeId || t("pair.noName");
    parts.statePill.className = undecided ? "pill idle" : "pill";
    parts.statePill.textContent = PAIR_TEXT.state[pairStateKey(request)] ?? request.state;
    parts.meta.textContent =
      `${request.platform || t("pair.noPlatform")} · ${request.address || t("pair.noAddress")}`;
    parts.nodeId.textContent = request.nodeId || "";
    parts.id.textContent = request.id || "";
    if (row.pairCompare !== undecided) {
      row.pairCompare = undecided;
      parts.compare.replaceChildren(
        ...(undecided
          ? [...PAIR_TEXT.compare.map((sentence) => element("div", "stale", sentence)),
            whyDetails("why.compareFingerprints")]
          : []));
    }
    // The two values are what two people are reading off two screens while this
    // ticks underneath them, so the block is rewritten only when the node
    // actually answered with different ones — and the box holding them is never
    // replaced at all.
    const signature = fingerprintSignature(request);
    if (row.pairFingerprints !== signature) {
      row.pairFingerprints = signature;
      writeFingerprintBlock(parts.fingerprints, request);
    }
    parts.step.textContent = pairStepText(request);
    parts.nodeStep.textContent = !undecided && request.nextStep ? String(request.nextStep) : "";

    // "approve" and "confirm" are one button in two states, not two buttons.
    const verb = request.state === "awaiting-confirm" ? "confirm"
      : (request.state === "pending" && request.direction === "incoming" ? "approve" : "");
    const wanted = [];
    if (verb !== "") {
      parts.primary.textContent = verb === "confirm" ? PAIR_TEXT.confirm : PAIR_TEXT.approve;
      parts.primary.disabled = state.busy;
      parts.primary.onclick = () => decidePairRequest(request.id, verb);
      wanted.push(parts.primary);
    }
    if (undecided) {
      parts.reject.disabled = state.busy;
      parts.reject.onclick = () => decidePairRequest(request.id, "reject");
      wanted.push(parts.reject);
    }
    // A finished row carries no button at all, so the box is emptied rather than
    // hidden — and emptied only when it is not already empty.
    keepChildren(parts.buttons, wanted);
  }

  // renderPairWaiting is the one line at the top of the drawer.
  //
  // The requests panel is below the candidate list, which is where the order of
  // prominence puts it and which also puts it below the fold on a short window.
  // A row waiting for this owner is the only time-critical thing in here —
  // somebody at another keyboard is looking at their screen — so its existence
  // is stated where the drawer opens, and the panel itself is where it is
  // acted on.
  function renderPairWaiting() {
    const line = el("pair-waiting");
    line.replaceChildren();
    const waiting = (state.pairRequests ?? []).filter(
      (request) => request.state === "pending" ? request.direction === "incoming" : request.state === "awaiting-confirm");
    if (waiting.length === 0) return;
    line.append(element("div", "stale",
      plural(waiting.length, "pair.waiting", { panel: t("pair.step3Heading") })));
  }

  // The exchange's rows, keyed by request id and kept across renders.
  //
  // Same treatment as the candidate list, for a worse case. The drawer's
  // two-second tick ends in this render twice over, and with byte-identical data
  // the container was written both times — replaceChildren detaches every child
  // before re-appending it, so 指紋一致，核准 / 指紋一致，確認 / 拒絕 lost their
  // identity twice a second. The focus on one was dropped, and a press whose
  // mousedown and mouseup fell on either side of a write was swallowed without
  // ever becoming a click — on the one decision in this window that cannot be
  // taken back.
  const pairRequestRows = new Map();

  function renderPairRequests() {
    const rows = el("pair-requests");
    const note = el("pair-requests-note");
    note.textContent = "";
    el("pair-requests-all").checked = state.pairRequestsAll;
    renderPairWaiting();

    // Every path that shows a message instead of rows forgets the kept rows:
    // they are off screen, and reusing one when the list comes back would put a
    // request's old claims on screen without anything having re-read them.
    const message = (...kids) => {
      pairRequestRows.clear();
      keepChildren(rows, kids);
    };

    if (state.pairRequestsError) {
      // A failed read is not a fact about the other machine. Rendering it as
      // an empty list would tell an owner nobody asked, at the moment somebody
      // is waiting for them.
      message(
        keptMessage(rows, 0, "stale", PAIR_TEXT.requestsFailed),
        keptMessage(rows, 1, "muted", state.pairRequestsError));
      return;
    }
    if (!state.pairRequestsLoaded) {
      message(keptMessage(rows, 0, "empty", PAIR_TEXT.requestsUnread));
      return;
    }
    const requests = state.pairRequests ?? [];
    if (requests.length === 0) {
      message(keptMessage(rows, 0, "empty",
        state.pairRequestsAll ? PAIR_TEXT.requestsEmptyAll : PAIR_TEXT.requestsEmpty));
      return;
    }
    const wanted = [];
    const seen = new Set();
    for (const request of requests) {
      // The request id is what says "the same exchange". A row that carries
      // none — or one another row in this same list already used — cannot be
      // matched to a kept row, so it gets a fresh one rather than somebody
      // else's: a node repeating an id must not be able to take over the row
      // whose fingerprints the owner has already compared.
      const key = String(request.id ?? "");
      const keyed = key !== "" && !seen.has(key);
      seen.add(key);
      let row = keyed ? pairRequestRows.get(key) : undefined;
      // And a kept row whose fingerprints changed is not the row the owner has
      // been reading. Rewriting the block inside it swaps the two values under
      // a pointer that is already there, and keeps the press that was aimed at
      // the old ones; a fresh row cannot be pressed by a mousedown aimed at its
      // predecessor.
      if (row && row.pairFingerprints !== null &&
          row.pairFingerprints !== fingerprintSignature(request)) {
        pairRequestRows.delete(key);
        row = undefined;
      }
      if (row) {
        updateRequestRow(row, request);
      } else {
        row = pairRequestRow(request);
        if (keyed) pairRequestRows.set(key, row);
      }
      wanted.push(row);
    }
    for (const key of [...pairRequestRows.keys()]) {
      if (!seen.has(key)) pairRequestRows.delete(key);
    }
    // And the container is written only on an arrival, a departure or a
    // reorder.
    keepChildren(rows, wanted);
  }

  // pairAddressReachable says whether the address the node answers with is one
  // another machine could actually connect to.
  //
  // A node started without -allow-lan listens on 127.0.0.1:7463, and that is
  // what the API reports. It is a true answer to "where does this node's peer
  // listener answer" and a useless one to "what does the other machine type":
  // handed across, it fails over there as a connection timeout, with nothing on
  // either screen to say why. An unspecified host is the same problem — nobody
  // types 0.0.0.0 — so both are treated as "not yet".
  function pairAddressReachable(address) {
    const value = String(address ?? "").trim();
    if (value === "") return false;
    if (isLoopbackListen(value)) return false;
    const host = hostOf(value).toLowerCase();
    return host !== "0.0.0.0" && host !== "::" && host !== "";
  }

  // pairHereState is everything this window knows about the address the other
  // machine has to type: the string, whether anything out there could reach it,
  // and the node's own sentence about why not.
  //
  // The node answers the last two itself now (`peerAddressReachable`,
  // `peerAddressProblem`). An older node answers neither, and an absent field is
  // NOT read as false: that would put a claim the node never made underneath a
  // remedy. When they are absent the window judges the string the way it always
  // did — and an empty string is unreachable rather than nothing at all, which
  // is the whole of finding 1. A default node's peer listener is on loopback,
  // so the node announces no address whatsoever; the panel used to hide this
  // block for it, leaving a fresh install with an open window, no address, no
  // reason and no button.
  function pairHereState(window_) {
    const state_ = window_ ?? {};
    const address = String(state_.peerAddress ?? "").trim();
    const nodeSaid = typeof state_.peerAddressReachable === "boolean";
    return {
      address,
      // No address is unreachable whatever the node says about it. A node that
      // sent `peerAddressReachable: true` with no string to go with it would
      // otherwise put a blank line where the address belongs, under a live copy
      // button that copies nothing — the one screen that has to carry a value
      // across to another machine, carrying none and saying nothing is wrong.
      reachable: address !== ""
        && (nodeSaid ? state_.peerAddressReachable : pairAddressReachable(address)),
      // Only ever the node's own words, and only when the node also said the
      // address was no good. A problem sentence beside a working address would
      // be a warning about nothing.
      problem: nodeSaid && !state_.peerAddressReachable ? String(state_.peerAddressProblem ?? "").trim() : "",
    };
  }

  // goToNodeSettings is the remedy as a button rather than as a sentence about
  // where to click. The address is fixed two tabs away, and an owner who has
  // just been told their node is unreachable should not also have to find it.
  function goToNodeSettings() {
    closePairingDrawer();
    state.view = "settings";
    state.settingsSection = "settings-node";
    render();
    el("settings-node")?.scrollIntoView?.({ block: "start", behavior: "smooth" });
    // And the keyboard goes with the eye. Scrolling alone leaves focus back on
    // a button in a drawer that has just been closed, so the next Tab or Space
    // acts on nothing the owner can see — and a keyboard-only owner arrives at
    // the remedy with no way to reach it but hunting for it again. 允許區網連線
    // is the setting the button was pressed for.
    el("node-allow-lan")?.focus?.();
  }

  // renderPairHere shows the address the other machine has to type.
  //
  // Shown in the key font with a copy button rather than inside the notice's
  // prose, which is how the last hand-carried string lost a character. Shown
  // whether or not this node is announcing: mDNS that does not carry between
  // two segments is exactly as silent as mDNS that is off, and the sentence
  // beside the address is what differs.
  function renderPairHere() {
    const box = el("pair-here");
    const value = el("pair-local-address");
    const note = el("pair-here-note");
    const window_ = state.pairing?.state ?? {};
    const here = pairHereState(window_);
    // Hidden only when there is no pairing state to describe at all. Not when
    // the address is missing: a node that gave none is the commonest node there
    // is, and hiding the block told its owner nothing about why nobody can
    // reach them.
    if (!state.pairing?.windowAvailable) {
      box.classList.add("hidden");
      el("copy-pair-address-status").textContent = "";
      return;
    }
    box.classList.remove("hidden");
    // Only the list of open addresses hides it, each of its rows carrying its
    // own; every other state has the one address and the one button.
    el("copy-pair-address").classList.remove("hidden");
    // An address nobody can reach — or no address at all — is not shown as the
    // address to type. What goes here instead is what is wrong and the button
    // that fixes it.
    if (!here.reachable) {
      value.textContent = PAIR_TEXT.hereFixHeadline;
      el("copy-pair-address-status").textContent = "";
      const whyText = here.address === "" ? PAIR_TEXT.hereNoAddressWhy : PAIR_TEXT.hereUnreachable;
      const options = pairHereRepairs();
      // Rebuilt only when what it says changes. The network view reloads the
      // pairing state every five seconds and lands here each time; rebuilding
      // the block regardless folded a 「說明」 the owner had just opened and
      // dropped the keyboard from its summary onto the page (#195 review), and
      // did the same to a repair button someone was about to press. The
      // signature is every string and option the block is built from, so a
      // language switch or a new answer from the node still redraws it.
      const signature = JSON.stringify([whyText, t("common.why"), t("why.unreachable"),
        here.problem || "", options, PAIR_TEXT.hereFix]);
      if (note.pairHereSignature === signature) {
        for (const button of note.pairHereRepairButtons ?? []) button.disabled = state.busy;
        el("copy-pair-address").disabled = true;
        return;
      }
      note.pairHereSignature = signature;
      note.pairHereRepairButtons = [];
      const why = element("div", "", whyText);
      note.replaceChildren(why, whyDetails("why.unreachable"));
      // The node's own sentence about this listener, under the remedy rather
      // than in front of it: the remedy is the act, and the node's words are
      // the detail that says which listener it is about.
      if (here.problem) note.append(element("div", "muted", here.problem));
      // The repair itself, here, rather than a button that takes the owner to
      // the settings page to find it. These are peerListenRepairs' own options,
      // so the label names 「允許區網連線」 exactly when pressing would turn it
      // on (docs/ui-contract.md §7.8 rule 4) and the save goes through the form
      // — one validation, one restart, one "did it stick" check.
      const actions = element("div", "repairactions");
      for (const option of options) {
        const button = element("button", option.primary ? "primary" : "ghost", option.label);
        button.disabled = state.busy;
        button.onclick = () => applyPeerListenRepairFromCard(option).catch(() => {});
        actions.append(button);
        note.pairHereRepairButtons.push(button);
      }
      // Always a way through to the form. The options above come from the
      // node's own answer, and a machine with no private address of its own —
      // or one whose settings could not be read — has none to offer.
      const fix = element("button", options.length === 0 ? "primary" : "ghost", PAIR_TEXT.hereFix);
      fix.onclick = () => goToNodeSettings();
      actions.append(fix);
      note.append(actions);
      el("copy-pair-address").disabled = true;
      return;
    }
    el("copy-pair-address").disabled = false;
    // Two sentences, because the two situations have different remedies: on a
    // node that announces nothing this address is the only way in, and on one
    // that announces it is what to fall back on when the other machine's list
    // stays empty anyway.
    const lead = window_.notice ? PAIR_TEXT.hereNote : PAIR_TEXT.hereNoteAnnouncing;
    // A node serving several addresses (ADR-005) is reachable on each of them,
    // and which one the other machine can use depends on which network the two
    // share — something this window cannot know. So every open one is listed,
    // with its interface, and the owner reads out the one on the shared network.
    const open = pairOpenAddresses();
    const notOpen = peerListensNotOpen(state.nodeSettings);
    const multi = open.length >= 2;
    el("copy-pair-address").classList.toggle("hidden", multi);
    // Rebuilt only when what it says changes, like the unreachable block: the
    // five-second reload would otherwise take the keyboard off a copy button.
    const signature = JSON.stringify(["reachable", here.address, open, notOpen, lead,
      t("pair.hereEither"), t("common.copy"), t("pair.hereGoSettings")]);
    if (note.pairHereSignature === signature) return;
    note.pairHereSignature = signature;
    if (multi) {
      value.replaceChildren(...open.map((entry) => {
        const line = element("div", "pairaddr");
        const copy = element("button", "ghost", t("common.copy"));
        copy.onclick = () => copyPairAddress(entry.address);
        line.append(element("span", "pairaddr-value", entry.address),
          element("span", "muted", entry.interface), copy);
        return line;
      }));
      note.replaceChildren(element("div", "", t("pair.hereEither")), element("div", "", lead));
    } else {
      value.textContent = here.address;
      note.replaceChildren();
      note.textContent = lead;
    }
    // Configured and not open is said here too, in one line, because it is the
    // address the owner may be about to read out: which ones, and the way to
    // the rows that say why.
    if (notOpen.length > 0) {
      const actions = element("div", "repairactions");
      const fix = element("button", "ghost", t("pair.hereGoSettings"));
      fix.onclick = () => goToNodeSettings();
      actions.append(fix);
      note.append(element("div", "muted", t("pair.hereNotOpen", { addresses: notOpen.join(", ") })), actions);
    }
  }

  // pairOpenAddresses is every network address the node reports bound, in its
  // own order (the preferred first), with the interface this machine has it on.
  // Empty for a node that reports no per-address state.
  function pairOpenAddresses() {
    const listeners = state.nodeSettings?.peerListeners;
    if (!Array.isArray(listeners)) return [];
    const local = new Map((state.nodeAddresses?.list ?? []).map((item) => [String(item.address).toLowerCase(), item]));
    return listeners
      .filter((entry) => entry.state === "bound" && !isLoopbackListen(entry.address))
      .map((entry) => ({ address: entry.address, interface: local.get(hostOf(entry.address))?.interface ?? "" }));
  }

  // pairHereRepairs is peerListenRepairs' list, minus its last entry.
  //
  // That entry is 「就先只在本機」, which is the checklist's skip: an owner who
  // has decided to stay off the network needs a way to say so there. Here it is
  // the opposite of what was asked for — this block exists because the other
  // machine cannot reach this one — so offering it would be offering to do
  // nothing under the heading that says nothing works.
  function pairHereRepairs() {
    if (!state.nodeSettings) return [];
    const current = state.nodeSettings.saved?.peerListen
      || state.nodeSettings.settings?.peerListen
      || LOOPBACK_LISTEN;
    const allowLanOn = Boolean(state.nodeSettings.saved?.allowLan);
    const repairs = peerListenRepairs(
      { reason: "loopback", address: current },
      state.nodeAddresses ?? { list: [], failure: "" },
      // The node's SAVED answer, never the settings form's live checkbox: that
      // form is two tabs away and editable, and an unsaved tick over there
      // would drop the clause from a label whose click still turns it on.
      allowLanOn,
    ).filter((option) => option.peerListen !== "");
    // 「全部開放」 first, when the node takes a list and this machine has two
    // private networks: the owner does not have to know which one the other
    // machine is on. Every address is named on the button, and so is the
    // switch, exactly when pressing it turns that on (§7.8 rule 4). At most
    // four, which is what the node accepts.
    const privateAddresses = (state.nodeAddresses?.list ?? []).filter((item) => item.private);
    if (peerListensSupported() && privateAddresses.length >= 2) {
      const port = peerListenPort(current);
      const list = privateAddresses.slice(0, 4).map((item) => `${item.address}:${port}`);
      for (const option of repairs) option.primary = false;
      repairs.unshift({
        label: t(allowLanOn ? "nodeSettings.repairAll" : "nodeSettings.repairAllAndLan", { list: list.join(", ") }),
        peerListens: list,
        peerListen: list[0],
        allowLan: true,
        primary: true,
      });
    }
    return repairs;
  }

  // copyPairAddress hands that address to the clipboard, and says so when the
  // clipboard refuses: the address is on screen either way, and a copy silently
  // reported as done is a string typed wrong on the other machine.
  async function copyPairAddress(chosen = "") {
    const status = el("copy-pair-address-status");
    const address = chosen || state.pairing?.state?.peerAddress || "";
    if (!address) {
      status.textContent = PAIR_TEXT.hereNoAddress;
      return;
    }
    try {
      await api.CopyText(address);
      status.textContent = PAIR_TEXT.hereCopied;
    } catch (error) {
      status.textContent = `${PAIR_TEXT.hereCopyFailed}（${error}）`;
    }
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
  function broadcastWarning(lead, tail = "") {
    const known = Boolean(state.localName);
    const name = known ? state.localName : t("pair.unknownName");
    // Not "來自 hostname": on macOS it is ComputerName, and the hostname being
    // the wrong source is the reason this reads the way it does. And not "read
    // from this machine" unconditionally — follow the instruction below and that
    // sentence becomes false, which is the same defect one level down.
    //
    // Nothing at all when the name is unknown. A node that answers the pairing
    // endpoint without a name is one older than this app — ordinary, since the
    // two are launched separately — and stating where an unknown string came
    // from is a confident claim about something not in hand.
    let origin = "";
    if (known) {
      origin = state.localNameIsChosen ? t("pair.nameChosen") : t("pair.nameRead");
    }
    // The space before the name is right for a Latin one and wrong before a
    // fullwidth paren, which carries its own. Dropped in the one case that has
    // one.
    const before = known ? t("pair.announceBefore") : t("pair.announceBeforeUnknown");
    return [
      element("span", "", lead + before),
      element("span", "claimed", name),
      element("span", "", t("pair.announceAfter", { tail, origin })),
    ];
  }

  // renderPairingSubtitle keeps the drawer's own heading honest. "開啟後同網段
  // 的人都會知道這台機器在跑 AgentHub" is true of a node that announces, and a
  // plain falsehood at the top of the panel on one that does not — which is
  // exactly the node whose owner has to read the rest of this panel carefully.
  function renderPairingSubtitle(pairing) {
    const sub = el("pairing-sub");
    const windowAvailable = pairing?.windowAvailable ?? (pairing?.availability === "on");
    if (!pairing || !windowAvailable) {
      sub.textContent = PAIR_TEXT.drawerSubUnknown;
      return;
    }
    const announceable = pairing.state?.announcing?.announceableAddresses ?? 0;
    if (announceable > 0) {
      sub.textContent = PAIR_TEXT.drawerSubAnnouncing;
      return;
    }
    // Two different nodes hide behind "not announcing". One has an address to
    // hand over and the subtitle should send the owner to it; the other — the
    // default install, whose peer listener is on loopback — has none, and
    // pointing at 「下面這個位址」 there points at a block that says nobody can
    // get in and shows no address. Same question #pair-here itself asks.
    sub.textContent = pairHereState(pairing.state ?? {}).reachable
      ? PAIR_TEXT.drawerSubNotAnnouncing
      : PAIR_TEXT.drawerSubUnreachable;
  }

  function renderPairingWindow() {
    const headline = el("pairing-headline");
    const detail = el("pairing-detail");
    const note = el("pairing-note");
    detail.replaceChildren();
    tickCountdown();

    const pairing = state.pairing;
    renderPairingSubtitle(pairing);
    const on = el("btn-pairing-on");
    const off = el("btn-pairing-off");

    if (!pairing) {
      headline.textContent = t("pair.readingState");
      on.disabled = true;
      off.disabled = true;
      showPairingWindowButtons(false);
      note.textContent = "";
      return;
    }

    // Whether the window itself can be opened from here, which since the
    // pairing exchange landed is a different question from whether this node
    // looks at the network. A node started without -discover opens a window
    // all the same and pairs by a typed address — greying the button out there
    // would take the button away from the one owner who has nothing else.
    //
    // The fallback is for an answer from before that field existed: availability
    // "on" is only ever set after the window endpoints answered.
    const windowAvailable = pairing.windowAvailable ?? (pairing.availability === "on");

    // Four different things to say, and a panel that collapses any two of them
    // tells the owner to wait for something that is not coming, or to change a
    // setting that is not the problem. "openNotAnnouncing" is the node with a
    // window open that nobody can find: rendering it as "off" said the window
    // was shut while it was open and collecting requests.
    if (!windowAvailable && pairing.availability === "off") {
      headline.textContent = t("pair.notLookingHeadline");
      detail.append(element("div", "stale", t("pair.notLookingDetail")));
      note.textContent = t("pair.notLookingNote");
      on.disabled = true;
      off.disabled = true;
      showPairingWindowButtons(false);
      return;
    }
    if (!windowAvailable) {
      headline.textContent = PAIR_TEXT.windowUnavailable;
      detail.append(element("div", "stale", t("pair.stateUnreadable")));
      note.textContent = pairing.error || "";
      on.disabled = true;
      off.disabled = true;
      showPairingWindowButtons(false);
      return;
    }

    const window_ = pairing.state ?? {};
    const announcing = window_.announcing ?? {};
    // A window this node cannot announce is still a window: the other machine
    // can be handed this one's address and type it. The node stopped refusing
    // those when the pairing exchange landed (ADR-004), so the button is live
    // whenever the window endpoints answer. What changes is what is said beside
    // it — and #pair-here then carries the address that is the whole way in.
    const canAnnounce = (announcing.announceableAddresses ?? 0) > 0;
    on.disabled = state.busy;
    off.disabled = state.busy || !window_.open;
    showPairingWindowButtons(Boolean(window_.open));

    if (window_.open) {
      const left = pairingRemaining();
      // The window and the announcing are two lines because they are two facts.
      // Saying "正在廣播" for an open window asserts the second from the first,
      // and the whole point of carrying `announcing` is that it does not follow.
      // The node closes the window itself; this machine only knows the count
      // reached zero. Saying so beats counting "剩 0:00" until the next read.
      // "配對視窗開啟中" on its own reads as done. On a node whose peer listener
      // is on loopback it is not: the window is open and there is no way in,
      // and an owner who reads it as done goes to the other machine and waits.
      const reachable = pairHereState(window_).reachable;
      headline.textContent = left === 0
        ? PAIR_TEXT.windowExpiring
        : (reachable ? PAIR_TEXT.windowOpen : PAIR_TEXT.windowOpenUnreachable);
      // The announce line says "nothing at all was sent" — true, and on an
      // unreachable node it is the second of three amber statements of the same
      // bad news, between a headline that already said nobody can get in and a
      // remedy block below that used to say it a third time. Said once, up
      // there; what the node itself reported is still kept, because that is the
      // one part of the line this window could not derive.
      if (reachable || canAnnounce) {
        detail.append(announceLine(announcing));
      } else if (announcing.lastError) {
        detail.append(element("div", "muted", announcing.lastError));
      }
      // Opening the drawer opened the window without a button, so closing it
      // closing the window has to be said, or it reads as merely hiding.
      detail.append(element("div", "muted", PAIR_TEXT.closeEnds));
      // The broadcast tradeoff is only a tradeoff where something is actually
      // broadcast. On a node that announces nothing, the note that matters is
      // the address above, not a warning about a name nobody will hear.
      if (canAnnounce) {
        note.replaceChildren(...broadcastWarning(t("pair.leadWhileOpen")));
      } else {
        note.textContent = "";
      }
    } else if (!canAnnounce) {
      // Not "the node will refuse it" any more — it does not. What is true is
      // that opening it will put this machine in nobody's candidate list, and
      // that the typed address is what is left.
      headline.textContent = PAIR_TEXT.windowClosed;
      detail.append(element("div", "stale", t("pair.nothingToAnnounceClosed")));
      // Only when the node said something. 「節點沒有說明原因。」 on its own line
      // is a sentence about the absence of a sentence, and it is under the one
      // explanation that does say something.
      if (announcing.lastError) detail.append(element("div", "muted", announcing.lastError));
      note.textContent = "";
    } else {
      headline.textContent = PAIR_TEXT.windowClosed;
      note.replaceChildren(...broadcastWarning(t("pair.leadOnOpen"), t("pair.tailTradeoff")));
    }
  }

  // showPairingWindowButtons leaves the one button that is worth pressing.
  //
  // Both were always on screen, side by side, in a drawer that opens the window
  // for you: one of them was therefore always the one that does nothing, and
  // which one changed with a state nobody was reading. The open button is the
  // way back from a window closed by hand or expired; the close button is the
  // way to stop announcing without leaving the drawer. Neither is ever removed
  // from the page — the disabled state above is what the checks read, and a
  // button that vanishes cannot be reported as unavailable.
  function showPairingWindowButtons(open) {
    el("btn-pairing-on").classList.toggle("hidden", open);
    el("btn-pairing-off").classList.toggle("hidden", !open);
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
      const box = element("div", "stale", t("pair.announceNoAddress"));
      if (announcing.lastError) box.append(element("div", "muted", announcing.lastError));
      return box;
    }
    if (announcing.lastError) {
      const box = element("div", "stale", t("pair.announceFailed"));
      box.append(element("div", "muted", announcing.lastError));
      return box;
    }
    if (announcing.lastAnnouncedAt) {
      return element("div", "muted", t("pair.announceLast", { when: relative(announcing.lastAnnouncedAt) }));
    }
    return element("div", "muted", t("pair.announceNeverYet"));
  }

  // tickCountdown updates only the countdown's own text, leaving the rows alone.
  function tickCountdown() {
    const line = el("pairing-countdown");
    const left = pairingRemaining();
    if (!state.pairing?.state?.open) {
      line.textContent = "";
      return;
    }
    // "Pairing open · 4:00": the headline carries the words, this carries the
    // separator and the clock, which are the same in both languages and so do
    // not belong in the tables (docs/ui-contract.md §11 rule 5).
    line.textContent = left === 0 ? "" : `· ${clock(left)}`;
  }

  // The candidate rows, keyed by node id and kept across renders.
  //
  // This list is re-rendered under an owner who is reaching for it. The drawer's
  // own two-second tick ends in a full render (the contract asks it to refresh
  // the trusted-node list the modal sits over), and the five-second pairing poll
  // does the same. A row rebuilt at that rate is a row that cannot be pressed:
  // the focus on 送出配對請求 is dropped, and a press whose mousedown and mouseup
  // land on either side of the rebuild is swallowed without ever becoming a
  // click. So a row that is still about the same machine is the same element it
  // was, with its text written in place, and the list itself is only written
  // when a machine actually arrived, left, or changed places.
  const candidateRows = new Map();

  function renderCandidates() {
    const rows = el("candidate-rows");
    const notice = el("candidate-notice");
    const full = el("candidate-full");
    full.replaceChildren();
    notice.textContent = "";
    // Every path that shows a message instead of rows forgets the kept rows:
    // they are off screen, and reusing one when the list comes back would put a
    // machine's old claims on screen without anything having re-read them.
    const message = (...kids) => {
      candidateRows.clear();
      keepChildren(rows, kids);
    };

    const pairing = state.pairing;
    if (!pairing) {
      message();
      return;
    }
    // "off" and "openNotAnnouncing" are the same node, with and without a
    // window: neither is looking, so neither has a list. The difference is
    // what the window panel above says, not what this region contains.
    if (pairing.availability === "off" || pairing.availability === "openNotAnnouncing") {
      message(keptMessage(rows, 0, "empty", t("candidate.notLooking")));
      return;
    }
    if (pairing.availability !== "on") {
      message(keptMessage(rows, 0, "empty", t("candidate.stateUnreadable")));
      return;
    }
    if (pairing.candidatesError) {
      message(
        keptMessage(rows, 0, "stale", t("candidate.listUnreadable")),
        keptMessage(rows, 1, "muted", pairing.candidatesError));
      return;
    }
    // In its own element above the list, not the first row of it. A full list is
    // long by definition — that is what full means — and the rows scroll, so a
    // warning inside them is scrolled away by the reader who most needs it.
    // Measured: with 64 rows it left the view after 600px of scrolling.
    if (pairing.full) {
      full.append(element("div", "stale", t("candidate.listFull")));
    }
    const candidates = pairing.candidates ?? [];
    const wanted = [];
    if (candidates.length === 0) {
      // The node filters paired nodes out of this list on purpose
      // (internal/discovery/candidates.go), so an empty list does not mean the
      // same thing in both directions. On 2026-09-10 two machines had pairing
      // mode open, both showed this region empty, and the owner read it as
      // broken — they were already paired with each other, which is precisely
      // why neither appeared. So the sentence says which of the two it is, and
      // where the missing machine actually is.
      // Kept across renders like the rows are. A list that is empty stays empty
      // for as long as nobody is broadcasting, and rebuilding this div twice a
      // second is the same pointless write the rows were fixed for.
      wanted.push(keptMessage(rows, 0, "empty", state.nodes.length > 0
        ? t("candidate.emptyWithPairs")
        : t("candidate.emptyNoPairs")));
    }
    const seen = new Set();
    for (const candidate of candidates) {
      // The node id is what says "the same machine". A candidate that announced
      // none — or one another row in this same list already used — cannot be
      // matched to a kept row, so it gets a fresh one rather than somebody
      // else's: a forger who repeats a neighbour's id must not be able to take
      // over the row the owner was about to press.
      const key = String(candidate.nodeId ?? "");
      const keyed = key !== "" && !seen.has(key);
      seen.add(key);
      let row = keyed ? candidateRows.get(key) : undefined;
      if (row) {
        updateCandidateRow(row, candidate);
      } else {
        row = candidateRow(candidate);
        if (keyed) candidateRows.set(key, row);
      }
      wanted.push(row);
    }
    for (const key of [...candidateRows.keys()]) {
      if (!seen.has(key)) candidateRows.delete(key);
    }
    // And the list is written only when it differs.
    keepChildren(rows, wanted);
    // The node's own words about what this list is worth, so the warning here
    // cannot drift from the guarantees the node actually makes.
    //
    // Said in the window's language when the node names the sentence with a
    // code this window knows; the node's English otherwise — a code added
    // after this build, or a node from before codes (#194). A zh-Hant window
    // used to show this paragraph in English whatever it was set to.
    const said = candidateNoticeText(pairing);
    if (said) notice.textContent = said;
  }

  // candidateNoticeText is the candidate list's notice in this window's words.
  // The code is the node's and only ever picks a key; a value that is not a
  // plain code picks nothing, and the English sentence is shown as data.
  function candidateNoticeText(pairing) {
    const code = typeof pairing?.noticeCode === "string" && /^[a-z0-9_]{1,64}$/.test(pairing.noticeCode)
      ? pairing.noticeCode : "";
    const key = `candidate.notice.${code}`;
    if (code && t(key) !== key) return t(key);
    return pairing?.notice || "";
  }

  // candidateRow builds the row once. Everything that changes between renders is
  // written by updateCandidateRow into these same elements, so the row and its
  // two buttons outlive every tick.
  function candidateRow(candidate) {
    const row = element("div", "candidaterow");
    const line = element("div", "line");
    const name = element("span", "name");
    line.append(name);
    row.append(line);
    const meta = element("div", "meta");
    row.append(meta);
    // The node id and the fingerprint in full, never a prefix: comparing the
    // first few groups is exactly what a forger can defeat, and these are the two
    // values that decide which machine gets trusted. `ah candidates` prints every
    // field, and #61 asks the two surfaces to agree, so nothing is omitted here
    // either.
    const nodeId = element("div", "fingerprint");
    const fingerprint = element("div", "fingerprint");
    row.append(nodeId, fingerprint);
    const seen = element("div", "muted");
    row.append(seen);
    // One click, and it carries the announced address and nothing else. The
    // address is where to knock; everything that decides identity — the key,
    // and the fingerprint derived from it — arrives over the connection this
    // opens, and is then compared by two people. So the untrusted row can pick
    // who is dialled without being able to pre-answer the question that
    // matters.
    const actions = element("div", "decide");
    const send = element("button", "primary", PAIR_TEXT.sendFromCandidate);
    actions.append(send);
    // The manual five-field form stays reachable from the row, as a secondary
    // path for two machines that cannot open a connection to each other.
    const use = element("button", "ghost", PAIR_TEXT.sendManual);
    actions.append(use);
    row.append(actions);
    row.candidateParts = { line, name, meta, nodeId, fingerprint, seen, send, use };
    // No flags yet, which is what the empty string means: a row that does carry
    // one differs from this and gets its line written on the first update.
    row.candidateFlags = "";
    updateCandidateRow(row, candidate);
    return row;
  }

  // updateCandidateRow writes this candidate into an existing row.
  //
  // textContent on the element that already holds the text, never a rebuilt
  // subtree: the two buttons must survive, and 「最後 X 秒前」 changes on every
  // single tick, so a row compared as a whole would never be reusable at all.
  function updateCandidateRow(row, candidate) {
    const parts = row.candidateParts;
    parts.name.textContent = candidateName(candidate);
    // A flag is how impersonation is visible at all from this side, so it is
    // shown on the row rather than in a detail view someone has to open. The
    // line is rewritten only when the flags themselves change; the buttons are
    // not in it, so nothing pressable moves when they do.
    const flags = `${candidate.contested ? "c" : ""}${candidate.duplicate ? "d" : ""}`;
    if (row.candidateFlags !== flags) {
      row.candidateFlags = flags;
      const pills = [];
      if (candidate.contested) pills.push(pill(t("candidate.contested"), "bad"));
      if (candidate.duplicate) pills.push(pill(t("candidate.duplicate"), "bad"));
      parts.line.replaceChildren(parts.name, ...pills);
    }
    parts.meta.textContent = `${candidate.platform || t("pair.noPlatform")} · ${candidate.address}`;
    parts.nodeId.textContent = candidate.nodeId;
    parts.fingerprint.textContent = candidate.fingerprint;
    parts.seen.textContent = t("candidate.seen", {
      first: relative(candidate.firstSeen),
      last: relative(candidate.lastSeen),
    });
    parts.send.disabled = state.busy || !candidate.address;
    parts.send.onclick = () => sendPairRequest(candidate.address);
    parts.use.onclick = () => prefillPairFrom(candidate);
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
    if (candidate.contested) flags.push(t("candidate.contested"));
    if (candidate.duplicate) flags.push(t("candidate.duplicate"));
    if (flags.length > 0) {
      // Both, when both. A ternary picked one, so a row the list flags twice
      // arrived in the dialog — where trust is granted — flagged once.
      note.append(element("div", "stale",
        t("candidate.flagged", { flags: flags.join(t("candidate.flagJoin")) })));
    }
    note.append(element("div", "", t("candidate.prefillClaimed")));
    note.append(element("div", "", t("candidate.prefillKey")));
    // The node id is what trust is keyed on, and the node only checks that the
    // key matches the fingerprint, never that either belongs to this id.
    note.append(element("div", "", t("candidate.prefillNodeId")));
    note.classList.remove("hidden");
    openPairModal();
  }

  function renderNodes() {
    const container = el("node-rows");
    container.replaceChildren();

    if (state.nodes.length === 0) {
      container.append(element("div", "empty", t("network.noNodesYet")));
      el("node-detail-body").nodeDetailParts = null;
      el("node-detail-body").replaceChildren(
        element("div", "empty", t("network.noNodesYetDetail"))
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
      const meta = element("div", "meta",
        `${node.platform} · ${lastSeen(node)} · ${plural(grantedCount(node.nodeId), "network.publishedToIt")}`);
      row.append(line, meta);
      // A peer with no address is skipped in silence at delivery time; the
      // list is where that is visible before it happens.
      if (!node.address) row.append(element("div", "noaddr", t("network.noAddressRow")));
      row.onclick = () => {
        state.selectedNode = node.nodeId;
        render();
      };
      container.append(row);
    }

    const selected = state.nodes.find((node) => node.nodeId === state.selectedNode);
    renderNodeDetail(selected);
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
    const heading = element("h3", "", t("network.sessionsHeading"));

    // A failure to read presence is a fact about this node, not about the peer.
    // Saying "we have not heard from it" here would turn a transport error into a
    // confident claim that happens to be unfounded.
    if (state.presenceError) {
      return [heading, element("div", "stale", t("network.presenceUnreadable"))];
    }
    if (!heardFrom(presence)) {
      return [heading, element(
        "div",
        "empty",
        t(heartbeatSilenceReasonsKey) + t("network.silenceAlsoAudience")
      )];
    }
    if (!presence.online) {
      return [heading, element(
        "div",
        "stale",
        t("network.peerOffline", { when: relative(presence.receivedAt) })
      )];
    }
    const sessions = presence.sessions ?? [];
    if (presence.sessionsWithheld) {
      return [heading, element("div", "empty", t("network.sessionsWithheld"))];
    }
    if (sessions.length === 0) {
      return [heading, element("div", "empty", t("network.peerPublishesNothing"))];
    }

    const table = element("table", "peer-sessions");
    const head = element("tr");
    for (const title of ["SESSION", t("network.colNode"), "PROVIDER", t("local.colStatus"),
      t("local.colLastSeen")]) {
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
    return node.lastSeenAt
      ? t("network.lastContact", { when: relative(node.lastSeenAt) })
      : t("network.neverInContact");
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
    if (state.presenceError) return { text: t("network.presenceUnknown"), className: "unknown" };
    // Silence has two causes and this side cannot tell them apart. /v1/peers
    // reports what arrived, and nothing arrives either when the peer has not
    // paired back — trust is recorded per machine — or when it has and is simply
    // not sending. Naming one would be a guess; the row names both, and the
    // detail below says where the answer is.
    if (!heardFrom(presence)) return { text: t("network.presenceNever"), className: "never" };
    if (presence.online) return { text: t("network.presenceOnline"), className: "online" };
    return {
      text: t("network.presenceOffline", { when: relative(presence.receivedAt) }),
      className: "offline",
    };
  }

  // heartbeatSilenceReasons spells out both causes of silence, because this node
  // has no way to choose between them.
  //
  // The node lists a peer it trusts whether or not that peer trusts it back, and
  // a heartbeat that never arrives looks identical in both cases. The only place
  // the difference is visible is the other machine's own `ah nodes`.
  // A getter, not a constant: it is read while rendering, and a constant
  // evaluated at module load would freeze the language the window started in.
  const heartbeatSilenceReasonsKey = "network.heartbeatSilence";

  // renderNodeDetail fills the pane beside the node list for the selected node.
  //
  // Every render of the network view lands here — the fifteen-second tick
  // included — and it used to rebuild the whole pane each time. Its two
  // 「說明」 were rebuilt with it: one the owner had opened folded shut under
  // them, and the keyboard on its summary fell to the page (#195 review). So
  // the top of the pane, whys included, is built once per node and language
  // and then only has its text rewritten; what is below it (nodeDetailRest) is
  // still rebuilt, and the address field in it keeps its own draft.
  function renderNodeDetail(selected) {
    const body = el("node-detail-body");
    if (!selected) {
      body.nodeDetailParts = null;
      body.replaceChildren(element("div", "empty", t("network.pickANode")));
      return;
    }
    const key = JSON.stringify([selected.nodeId, t("common.why"), t("why.fingerprint"), t("why.heartbeat")]);
    let parts = body.nodeDetailParts;
    if (!parts || parts.key !== key) {
      parts = nodeDetailParts(key);
      body.nodeDetailParts = parts;
      body.replaceChildren(...parts.list);
    }
    fillNodeDetail(parts, selected);
  }

  // nodeDetail is the pane built fresh, for the checks that read what it says.
  function nodeDetail(node) {
    const parts = nodeDetailParts("");
    fillNodeDetail(parts, node);
    return parts.list;
  }

  function nodeDetailParts(key) {
    const parts = {
      key,
      heading: element("h2"),
      fingerprint: element("div", "fingerprint"),
      note: element("p", "muted"),
      noteWhy: whyDetails("why.fingerprint"),
      // Trust is recorded per machine, and this page shows only this machine's
      // half. Pairing on the mac left the Ubuntu box answering "No paired
      // nodes" on 2026-09-10, and nothing here said that was half-done — the
      // row simply sat there having never been heard from, which reads as the
      // peer being off.
      mutualNote: element("p", "stale"),
      mutualWhy: whyDetails("why.heartbeat"),
      rest: element("div"),
    };
    parts.list = [parts.heading, parts.fingerprint, parts.note, parts.noteWhy,
      parts.mutualNote, parts.mutualWhy, parts.rest];
    return parts;
  }

  function fillNodeDetail(parts, node) {
    parts.heading.textContent = node.displayName;
    parts.fingerprint.textContent = node.fingerprint;
    parts.note.textContent = t("network.fingerprintNote");
    parts.mutualNote.textContent = t("network.mutualNote");
    parts.rest.replaceChildren(...nodeDetailRest(node));
  }

  function nodeDetailRest(node) {
    const rows = [
      [t("identity.nodeId"), node.nodeId],
      [t("pairManual.platform"), node.platform],
      [t("network.detailPairedAt"), node.pairedAt ? relative(node.pairedAt) : "—"],
      [t("network.detailLastContact"),
        node.lastSeenAt ? relative(node.lastSeenAt) : t("network.neverInContact")],
      [t("network.detailVisibleSessions"), plural(grantedCount(node.nodeId), "network.sessionCount")],
      [t("network.detailAddress"), node.address ? node.address : t("network.detailNoAddress")],
      // What delivery tries after the recorded address, in order (ADR-005 §4).
      ...(node.address && (node.alternateAddresses ?? []).length > 0
        ? [[t("network.detailAlternates"), node.alternateAddresses.join(", ")]]
        : []),
    ].map(([label, value]) => {
      const row = element("div", "detailrow");
      row.append(element("span", "muted", label), element("span", "mono", value));
      return row;
    });

    const revoke = element("button", "btn danger", t("network.revoke"));
    revoke.onclick = () => revokeSelected(node);
    const revokeNote = element("p", "muted", t("network.revokeNote"));

    const grid = element("div", "detailgrid");
    grid.append(...rows);
    return [
      grid,
      ...addressSection(node),
      element("div", "", ""), revoke, revokeNote,
    ];
  }

  // addressSection is where a paired node's address is read and written.
  //
  // An address is not a trust decision — pairing says who a node is, this says
  // where it currently answers — and the two are separated here because only the
  // second changes when a laptop moves between networks.
  //
  // The missing case is the loud one, and deliberately so: delivery skips a peer
  // with no address without a word, and the sender's own `ah send` still answers
  // `queued`. Nothing anywhere else in this app or the CLI says it happened. With
  // `--discover` running the address is learned from the peer's announcements; on
  // a segment with no broadcast — a direct cable, a network that drops multicast
  // — it has to be typed, and until now that meant a hand-written `curl -X PUT`.
  function addressSection(node) {
    const parts = [];
    if (node.address) {
      parts.push(element("p", "muted", t("network.addressRecorded", { address: node.address })));
      if ((node.alternateAddresses ?? []).length > 0) {
        parts.push(element("p", "muted", t("network.addressAlternates", { list: node.alternateAddresses.join(", ") })));
      }
    } else {
      parts.push(element("p", "noaddress", t("network.addressMissing")));
    }

    const input = element("input", "addressinput");
    input.type = "text";
    input.placeholder = "192.168.1.20:7463";
    // What is half-typed survives a re-render. This page is rebuilt from scratch
    // every fifteen seconds by the background refresh; a refresh while the field
    // has focus is now held off entirely (interactionInProgress), so this is the
    // second line of defence — it covers a re-render the owner caused themselves,
    // and a refresh landing after they clicked away from a field they had not
    // finished. Without it an address being entered is deleted under the owner's
    // hands and the field silently reverts to the value they are replacing.
    const draft = state.addressDraft?.nodeId === node.nodeId ? state.addressDraft.value : null;
    input.value = draft ?? node.address ?? "";
    input.oninput = (event) => {
      state.addressDraft = { nodeId: node.nodeId, value: event.target.value };
    };

    const submit = element("button", "btn setaddress", t("network.recordAddress"));
    submit.onclick = () => recordAddress(node, input.value);

    const form = element("div", "addressform");
    form.append(input, submit);
    parts.push(form);
    parts.push(element("p", "muted", t("network.addressFormat")));
    return parts;
  }

  async function recordAddress(node, raw) {
    const address = String(raw ?? "").trim();
    if (address === "") {
      banner(t("network.addressEmpty"));
      return;
    }
    await withBusy(t("network.recordAddress"), async () => {
      await api.SetNodeAddress(node.nodeId, address);
      // Only once the node has it. A draft cleared before the call would leave a
      // refused address nowhere, with the field back to the value the owner was
      // replacing and nothing to correct.
      state.addressDraft = null;
      await load();
      banner(t("network.addressSaved", { name: node.displayName, address }), true);
    });
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

  // Asked first, in the window's own dialog, because it cannot be undone: the
  // node's grants go in the same transaction as its trust, and pairing again
  // does not bring them back. docs/ui-contract.md had always said 「confirm 後
  // 執行」 and the button went straight to RevokeNode. A dangerous question,
  // so the keyboard starts on 取消.
  async function revokeSelected(node) {
    if (state.busy) return;
    const ok = await askConfirm({
      title: t("network.revokeConfirmTitle", { name: node.displayName }),
      body: t("network.revokeConfirmBody", { name: node.displayName, nodeId: node.nodeId }),
      confirmLabel: t("network.revoke"),
      danger: true,
    });
    if (!ok) return;
    await withBusy(t("network.revoke"), async () => {
      await api.RevokeNode(node.nodeId);
      state.selectedNode = null;
      await load();
      banner(t("network.revoked", { name: node.displayName }), true);
    });
  }

  function openPairModal() {
    el("local-fingerprint").textContent = state.localFingerprint || "—";
    // The two are shown together and described differently on purpose. The key
    // is carried to the other machine and typed in; the fingerprint is compared
    // on two screens and never carried. Presenting them the same way is how
    // someone ends up pasting a fingerprint into a key field.
    el("local-public-key").textContent = state.localPublicKey || "—";
    el("copy-public-key-status").textContent = "";
    el("pair-modal").classList.remove("hidden");
  }

  // copyLocalPublicKey hands this node's key to the clipboard.
  //
  // A clipboard that refuses says so rather than leaving the owner believing a
  // copy happened: the key is on screen either way, and the failure mode this
  // replaces is a hand-retyped key missing its last character.
  async function copyLocalPublicKey(statusId = "copy-public-key-status") {
    const status = el(statusId);
    if (!state.localPublicKey) {
      status.textContent = t("identity.noKeyYet");
      return;
    }
    try {
      await api.CopyText(state.localPublicKey);
      status.textContent = t("identity.keyCopied");
    } catch (error) {
      status.textContent = t("identity.keyCopyFailed", { error });
    }
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

  // renderAudienceNodeList offers every paired node as a checkbox, so the
  // owner picks a name rather than typing an id; the text field stays for an
  // id the list does not show. Unchecked every time, like the flags.
  //
  // formerNodes are ids the one session being edited is already published to
  // but that are not in state.nodes. They get a row of their own, ticked
  // (#194): with no box, readAudienceForm left them out and 套用 withdrew the
  // grant without a word. Unticking one is how the owner withdraws it on
  // purpose.
  //
  // Not "a machine no longer paired", though that is what the row first said.
  // A revoke deletes the node's grants in the same transaction
  // (internal/registry/trust.go, RevokeNode) and SetAudience refuses a node
  // that is not paired (internal/registry/registry.go), so a grant to a node
  // that really is gone does not survive to be shown here. The one way this
  // row appears is an Overview whose pairing-list read failed and came back
  // as no nodes (desktop/app.go) — and then the machine is still paired. So
  // the label says only that this read did not list it, and when the read is
  // known to have failed, the dialog says that instead of 「還沒有配對任何機器」.
  //
  // The boxes are kept in audienceNodeBoxes, which openAudienceModal ticks and
  // readAudienceForm reads, so the three agree on one list.
  let audienceNodeBoxes = [];
  function renderAudienceNodeList(formerNodes = []) {
    const list = el("audience-node-list");
    list.replaceChildren();
    audienceNodeBoxes = [];
    const row = (nodeId, className, ...parts) => {
      const label = element("label", className);
      const box = document.createElement("input");
      box.type = "checkbox";
      box.value = nodeId;
      box.className = "audience-node-box";
      box.onchange = () => label.classList.toggle("on", box.checked);
      label.append(box, ...parts);
      list.append(label);
      audienceNodeBoxes.push(box);
    };
    for (const node of state.nodes) {
      const presence = presenceLabel(presenceFor(node.nodeId));
      row(node.nodeId, "nodepick", element("span", `dot ${presence.className}`),
        element("span", "", node.displayName), element("span", "mono", node.nodeId));
    }
    if (state.nodesError) {
      list.append(element("p", "stale", t("audience.nodesReadFailed", { error: state.nodesError })));
    } else if (state.nodes.length === 0) {
      list.append(element("p", "muted", t("audience.noNodesYet")));
    }
    for (const nodeId of formerNodes) {
      row(nodeId, "nodepick former", element("span", "muted", t("audience.unlistedNode")),
        element("span", "mono", nodeId));
    }
    el("audience-node-input").value = "";
  }

  // renderAudienceCount says how many sessions the dialog is about.
  //
  // One sentence through plural() rather than two fragments around a bold
  // number: English wanted "session(s)" for the singular, which is the one
  // place a table of finished sentences gives up, and (s) in a dialog that
  // publishes things reads as a draft.
  function renderAudienceCount() {
    el("audience-count").textContent = plural(state.selected.size, "audience.count");
  }

  // The four flag boxes, in the order the dialog lists them.
  const AUDIENCE_FLAG_IDS = ["audience-cwd", "audience-messages", "audience-outbound", "audience-autowake"];

  // The three situations the presets name, as the flags they write.
  //
  // A preset answers "what may they do with messages", so it writes the three
  // message flags and all three of them — an answer that leaves one wherever
  // the last one left it is not one. Whether the working directory is shown is
  // a different question, and a preset leaves that box as it found it: writing
  // it off silently withdrew a setting the owner could not see change, because
  // the box is in the collapsed advanced section (#193 review).
  //
  // Waking includes replying. A woken agent is told to answer the peer
  // (AGENTS.md, "When AgentHub wakes you"), and without allowOutbound the node
  // refuses that send, so a wake preset without it wakes an agent that cannot
  // do the one thing it was woken for.
  const AUDIENCE_PRESETS = {
    view: { acceptMessages: false, allowOutbound: false, autoWake: false },
    messages: { acceptMessages: true, allowOutbound: false, autoWake: false },
    wake: { acceptMessages: true, allowOutbound: true, autoWake: true },
  };

  function audienceFlagsOnForm() {
    return {
      exportCwd: el("audience-cwd").checked,
      acceptMessages: el("audience-messages").checked,
      allowOutbound: el("audience-outbound").checked,
      autoWake: el("audience-autowake").checked,
    };
  }

  // presetForFlags names the combination on screen, or "" for one no preset
  // covers — which is what the advanced section is for, and what the line under
  // the presets says out loud.
  function presetForFlags(flags) {
    for (const [name, wanted] of Object.entries(AUDIENCE_PRESETS)) {
      if (Object.keys(wanted).every((key) => Boolean(flags[key]) === wanted[key])) return name;
    }
    return "";
  }

  function applyAudiencePreset(name) {
    const wanted = AUDIENCE_PRESETS[name];
    if (!wanted) return;
    el("audience-messages").checked = wanted.acceptMessages;
    el("audience-outbound").checked = wanted.allowOutbound;
    el("audience-autowake").checked = wanted.autoWake;
    syncAudiencePreset();
  }

  // syncAudiencePreset points the radios at whatever the boxes say, and says so
  // in words when nothing fits. It writes no box: the presets write the boxes,
  // the boxes never write each other.
  function syncAudiencePreset() {
    const name = presetForFlags(audienceFlagsOnForm());
    for (const radio of document.querySelectorAll('input[name="audience-preset"]')) {
      radio.checked = radio.value === name;
    }
    const note = el("audience-preset-note");
    const lines = [];
    if (name === "") lines.push(t("audience.presetCustom"));
    // Only on a selection of more than one. A single session opens showing its
    // own settings, so the sentence about them being reset is untrue there —
    // and it was the sentence the whole dialog was read through.
    if (state.selected.size > 1) lines.push(t("audience.resetNote"));
    note.textContent = lines.join(" ");
  }

  function openAudienceModal() {
    renderAudienceCount();
    const picked = state.sessions.filter((session) => state.selected.has(session.id));
    el("audience-selected").replaceChildren(...picked.map((session) => element("span", "", session.id)));
    // Exactly one session: see below. Worked out before the list is drawn,
    // because it decides what the list holds — that session's grants that no
    // paired node accounts for.
    const only = picked.length === 1 && state.selected.size === 1 ? picked[0] : null;
    const listed = new Set(state.nodes.map((node) => node.nodeId));
    const former = only?.audience?.mode === "selected"
      ? [...new Set(only.audience.nodes ?? [])].filter((nodeId) => nodeId && !listed.has(nodeId))
      : [];
    renderAudienceNodeList(former);

    // One session opens showing what that session already is; several open at
    // 不公開 with every flag off.
    //
    // The reset was there because the dialog applies to whatever is selected
    // and reads its values from the boxes, so a value left over from the last
    // time it was opened is a setting about to be applied to a different set of
    // sessions — and one of these flags starts turns in an agent with nobody
    // watching. That argument is about leftovers, and it is untouched: several
    // sessions may disagree, and there is no honest way to show one state for
    // many, so off stays the safe half of that disagreement. For exactly one
    // session there is no disagreement and nothing left over — the values shown
    // are that session's own, read from the overview — and blanking them meant
    // that changing 「誰看得到」 silently withdrew every flag the session had.
    const current = only?.audience ?? {};
    const mode = only ? (current.mode ?? "none") : "none";
    for (const radio of document.querySelectorAll('input[name="audience-mode"]')) {
      radio.checked = radio.value === mode;
    }
    if (only && mode === "selected") {
      const granted = new Set(current.nodes ?? []);
      for (const box of audienceNodeBoxes) {
        box.checked = granted.has(box.value);
        // renderAudienceNodeList paints the row from the box's own onchange, so
        // a box ticked here has to say so itself.
        box.onchange?.();
      }
    }
    el("audience-cwd").checked = Boolean(only && current.exportCwd);
    el("audience-messages").checked = Boolean(only && current.acceptMessages);
    el("audience-outbound").checked = Boolean(only && current.allowOutbound);
    el("audience-autowake").checked = Boolean(only && current.autoWake);

    renderAutoWakeNote();
    syncAudiencePreset();
    // A session whose flags are no preset's opens with the flags in view: the
    // radios are all empty then, and the only place that says what the session
    // actually does is the section that would otherwise start collapsed.
    el("audience-advanced").open = presetForFlags(audienceFlagsOnForm()) === "";
    el("audience-modal").classList.remove("hidden");
    syncAudienceForm();
  }

  // What the auto-wake box will actually do, next to the auto-wake box.
  //
  // Waking needs more than the one switch this dialog offers, and every missing
  // piece fails silently: the message lands in the inbox and nothing else
  // happens, which is indistinguishable from the box not having been ticked. The
  // node's own -auto-wake flag is one piece; for a Claude Code session there is
  // also its agenthub-mcp -channel, and beyond that a push that was measured
  // arriving at Claude Code and never being injected (docs/channel-push-not-
  // observed.md). None of it is a reason to disable the box — an owner may
  // reasonably set a session up before restarting the node — so this only says
  // what will happen, and never blocks the dialog.
  //
  // It sits above the collapsed advanced section, not inside it: the sentence
  // that says the box was turned off is about something the owner is about to
  // apply, and folded away it was a flag withdrawn without a word (#193
  // review). Hidden when it has nothing to say.
  function renderAutoWakeNote() {
    const note = el("audience-autowake-note");
    note.replaceChildren();
    renderAutoWakeNoteLines(note);
    note.classList.toggle("hidden", note.children.length === 0);
  }

  function renderAutoWakeNoteLines(note) {
    // Which providers are selected decides which of the remaining obstacles
    // apply, and a mixed selection gets both sentences: the owner is about to
    // apply one setting to sessions that will behave differently.
    const picked = state.sessions.filter((session) => state.selected.has(session.id));
    const providers = new Set(picked.map((session) => session.provider));
    // A selection that is nothing but Claude Code is the one case where this
    // box cannot work and no restart will change that: the push was measured
    // arriving and never being injected (docs/channel-push-not-observed.md).
    // So it is turned off here rather than explained — the argument for leaving
    // it live is that an owner may set a session up before restarting the node,
    // and there is nothing here to restart into.
    const claudeOnly = providers.size === 1 && providers.has("claude");
    el("audience-autowake").disabled = claudeOnly;
    el("audience-preset-wake").disabled = claudeOnly;
    if (claudeOnly) {
      el("audience-autowake").checked = false;
      // Read from the sessions, not the box, which the line above has just
      // cleared — and a language switch repaints this after it.
      if (picked.some((session) => session.audience?.autoWake)) {
        note.append(element("div", "warning", t("audience.autoWakeWillTurnOff")));
      }
      note.append(element("div", "muted", t("audience.autoWakeClaude")));
      return;
    }
    if (!state.nodeAutoWake) {
      note.append(element("div", "muted", t("audience.autoWakeNodeOff")));
      return;
    }
    if (providers.has("codex")) {
      note.append(element("div", "muted", t("audience.autoWakeCodex")));
    }
    if (providers.has("claude")) {
      note.append(element("div", "muted", t("audience.autoWakeClaude")));
    }
  }

  function closeAudienceModal() {
    el("audience-modal").classList.add("hidden");
  }

  function readAudienceForm() {
    const mode = selectedMode();
    const typed = el("audience-node-input")
      .value.split(/[\s,]+/)
      .map((value) => value.trim())
      .filter(Boolean);
    const checked = audienceNodeBoxes
      .filter((box) => box.checked)
      .map((box) => box.value);
    const nodes = mode === "selected" ? [...new Set([...checked, ...typed])] : [];
    return {
      mode,
      nodes,
      exportCwd: el("audience-cwd").checked,
      acceptMessages: el("audience-messages").checked,
      allowOutbound: el("audience-outbound").checked,
      autoWake: el("audience-autowake").checked,
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
    // Kept so a language switch can paint this list again. Every sentence below
    // comes out of t(), and none of them is re-derived by render() — the drawer
    // is filled by one call per read, so without the argument nothing could
    // rebuild it and the meta line stayed in the language before the switch.
    state.inboxView = view;
    body.replaceChildren();

    if (view.loading) {
      // Not an empty list: those render identically, and the read can take
      // fifteen seconds.
      meta.textContent = view.sessionId;
      body.append(element("div", "muted", t("common.loading")));
      return;
    }
    if (view.cleared) {
      // What the destructive action did, where the person who pressed it is
      // looking. The count matters because messages can arrive between reading
      // the list and confirming, and those go with the rest. Before the error
      // branch: a clear that succeeded and a re-read that then failed are two
      // facts, and the irreversible one must not be the one that goes unsaid.
      body.append(view.cleared.error
        ? element("div", "stale", t("inbox.clearFailed", { error: view.cleared.error }))
        : element("div", "muted", plural(view.cleared.removed, "inbox.cleared")));
    }

    if (view.error) {
      // A failed read is not an empty inbox, and only one of them means there is
      // nothing to come back for. Shown here rather than in a banner: the dialog
      // covers the banner, so an error there is an error nobody sees.
      meta.textContent = view.sessionId ?? "";
      body.append(element("div", "stale", t("inbox.unreadable")));
      body.append(element("div", "muted", view.error));
      return;
    }
    meta.textContent = `${view.sessionId} · ` + (view.more
      ? t("inbox.metaMore", { showing: view.showing, held: view.held, capacity: view.capacity })
      : t("inbox.meta", { held: view.held, capacity: view.capacity }));
    if (view.full) {
      // A full inbox refuses new messages, which is a thing happening now rather
      // than a list that happens to be long.
      body.append(element("div", "stale", t("inbox.full")));
    }
    if (view.messages.length === 0) {
      body.append(element("div", "empty", t("inbox.empty")));
      return;
    }
    if (view.more) {
      // The oldest end, because the node returns them in arrival order. An owner
      // looking for what just came in has to empty some of this first.
      body.append(element("div", "stale", t("inbox.moreHeld", { showing: view.showing })));
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
        line.append(element("span", "muted", t("sender.localPrefix")));
        line.append(element("span", "claimed", session));
        return line;
      }
      line.append(element("span", "fingerprint", nodeId));
      line.append(element("span", "muted", t("sender.claimsToBe")));
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
      line.append(element("span", "muted", t("sender.local")));
      return line;
    }
    if (value === state.localNodeId) {
      line.append(element("span", "muted", t("sender.local")));
      return line;
    }
    if (state.nodes.some((node) => node.nodeId === value)) {
      // A paired node's own id settles a bare value whatever shape it has.
      line.append(element("span", "fingerprint", value));
      line.append(element("span", "muted", t("sender.noSession")));
      return line;
    }
    if (looksLikeNodeId(value)) {
      line.append(element("span", "fingerprint", value));
      line.append(element("span", "muted", t("sender.noSession")));
      return line;
    }
    // Session-shaped and not paired: a local message from before senders were
    // self-describing, or a peer paired under an older rule and since revoked.
    // Indistinguishable, so claim no origin rather than the wrong one.
    line.append(element("span", "muted", t("sender.unknownOrigin")));
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
    state.inboxSessionAsked = sessionId;
    el("inbox-title").textContent = sessionId;
    el("inbox-modal").classList.remove("hidden");
    showInboxTab("inbox");
    // Loading is its own state. Rendering an empty list here is byte-identical to
    // an inbox with nothing in it, and the client waits up to fifteen seconds.
    renderInbox({ sessionId, loading: true, messages: [] });

    let view;
    try {
      view = await api.Inbox(sessionId);
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
    state.inboxSessionAsked = null;
    outboundApplied = outboundRequest;
    wakesApplied = wakesRequest;
    state.outbound = { messages: [], next: "", loading: false, error: "", session: null };
    state.wakes = { wakes: [], limits: null, loading: false, error: "", session: null };
    el("inbox-modal").classList.add("hidden");
    el("inbox-body").replaceChildren();
    el("outbound-body").replaceChildren();
    el("wakes-body").replaceChildren();
    el("inbox-meta").textContent = "";
    state.inboxView = null;
    state.inboxSession = null;
  }

  /* ---------------- MCP config ---------------- */

  // Numbered like the inbox and the pairing read, and for a sharper reason: this
  // dialog's title is painted before the call and its body after it, so a reply
  // that arrives once the owner has closed it and opened another row would put
  // one session's snippet under the other session's name. The copy hangs off the
  // same check — the window writes the clipboard, and only for the reply it
  // decided to show, so a retired reply takes it from nobody.
  let mcpRequest = 0;
  let mcpApplied = 0;

  // True while this call is still the one the dialog belongs to: nothing newer
  // was asked for, and nothing has retired it. Checked again after every await,
  // because each one is a moment the owner can click elsewhere.
  function mcpIsCurrent(sequence) {
    return sequence === mcpRequest && sequence > mcpApplied;
  }

  // The snippet is Go's, built with encoding/json from a path this process found
  // and the id of the row that was clicked, and it reaches the DOM through
  // textContent like everything else a provider had a hand in.
  async function openMCPConfig(sessionId) {
    const sequence = ++mcpRequest;
    // Kept because half this title is a translated word: after a language
    // switch the dialog has to be able to write it again, and the id is not
    // anywhere else by then.
    state.mcpSession = sessionId;
    el("mcp-title").textContent = `${t("mcp.title")} · ${sessionId}`;
    el("mcp-text").textContent = "";
    renderMCPStatus({ kind: "generating" });
    el("mcp-modal").classList.remove("hidden");

    let result;
    let failure = null;
    try {
      result = await api.MCPConfig(sessionId);
    } catch (error) {
      failure = error;
    }
    if (!mcpIsCurrent(sequence)) {
      // This reply is for a row nobody is looking at any more. Do not show it,
      // and above all do not copy it: the clipboard is the part that travels out
      // of the window and into a file.
      return;
    }
    if (failure !== null) {
      mcpApplied = sequence;
      // Shown here rather than in a banner: this dialog covers the banner, so an
      // error there is an error nobody reads. The most likely one is that
      // agenthub-mcp was not found, and the message says where it looked.
      renderMCPStatus({ kind: "failed", error: String(failure) });
      return;
    }
    // Title and body come from the same answer: this is the reply to the call
    // this line's id was asked for, and no other reply reaches here.
    el("mcp-title").textContent = `${t("mcp.title")} · ${sessionId}`;
    el("mcp-text").textContent = result.text;

    let copied = true;
    try {
      await api.CopyText(result.text);
    } catch {
      copied = false;
    }
    if (!mcpIsCurrent(sequence)) return;
    mcpApplied = sequence;
    // Whether the clipboard took it. Saying "已複製" when it did not is the one
    // outcome that sends someone to paste nothing into a file.
    renderMCPStatus({ kind: copied ? "copied" : "copyFailed" });
  }

  // renderMCPStatus is the only writer of #mcp-status, and it writes from
  // state.mcpStatus rather than from the moment it was called.
  //
  // The line is the answer to "did the clipboard take it", which is the one
  // thing in this dialog the owner acts on — and it was written once, in the
  // language of that moment. A switch with the dialog still open left the
  // Chinese sentence under an English window, or index.html's placeholder key.
  function renderMCPStatus(status) {
    state.mcpStatus = status ?? null;
    const node = el("mcp-status");
    if (!status) {
      node.textContent = "";
      return;
    }
    if (status.kind === "failed") {
      node.replaceChildren(
        element("div", "stale", t("mcp.failed")),
        element("div", "muted", String(status.error ?? ""))
      );
      return;
    }
    node.textContent = t({
      generating: "mcp.generating",
      copied: "mcp.copied",
      copyFailed: "mcp.copyFailed",
    }[status.kind] ?? "mcp.generating");
  }

  function closeMCPConfig() {
    // Retire whatever is in flight, so its answer cannot paint a hidden dialog
    // or take the clipboard from whatever the owner copied next.
    mcpApplied = mcpRequest;
    state.mcpSession = null;
    el("mcp-modal").classList.add("hidden");
    el("mcp-text").textContent = "";
    renderMCPStatus(null);
  }

  /* ---------------- inbox drawer: tabs, outbound, wakes ---------------- */

  const INBOX_TABS = ["inbox", "outbound", "wakes"];

  function showInboxTab(tab) {
    state.inboxTab = INBOX_TABS.includes(tab) ? tab : "inbox";
    for (const name of INBOX_TABS) {
      el(`inbox-tab-${name}`).className = `dtab${name === state.inboxTab ? " on" : ""}`;
      el(`inbox-pane-${name}`).classList.toggle("hidden", name !== state.inboxTab);
    }
    // Clearing empties the inbox and nothing else, so the button belongs to
    // that tab alone.
    el("inbox-clear").classList.toggle("hidden", state.inboxTab !== "inbox");
    el("inbox-foot-note").textContent = state.inboxTab === "inbox"
      ? t("inbox.footNote")
      : state.inboxTab === "outbound" ? t("inbox.outboundFootNote") : t("inbox.wakesFootNote");
    if (state.inboxTab === "outbound" && state.outbound.session !== state.inboxSessionAsked) {
      loadOutbound({ reset: true }).catch(() => {});
    }
    if (state.inboxTab === "wakes" && state.wakes.session !== state.inboxSessionAsked) {
      loadWakes().catch(() => {});
    }
  }

  // Both logs are numbered like every other read here: a slow page must not
  // land under a different session's title.
  let outboundRequest = 0;
  let outboundApplied = 0;

  async function loadOutbound({ reset = false } = {}) {
    const session = state.inboxSessionAsked;
    const sequence = ++outboundRequest;
    if (reset) state.outbound = { messages: [], next: "", loading: true, error: "", session };
    else state.outbound.loading = true;
    renderOutbound();
    let page;
    try {
      // The node narrows the list (agenthub#132), so an empty page means this
      // session sent nothing — not that the newest fifty belonged to someone
      // else. A continuation repeats the session: without it the second page
      // would be the node-wide one, appended under this session's name.
      page = await api.Outbound(session ?? "", 50, reset ? "" : state.outbound.next);
    } catch (error) {
      page = { messages: [], error: String(error) };
    }
    if (sequence <= outboundApplied) return;
    outboundApplied = sequence;
    const rows = page.messages ?? [];
    state.outbound = {
      session,
      messages: reset ? rows : [...state.outbound.messages, ...rows],
      next: page.next ?? "",
      loading: false,
      error: page.error ?? "",
    };
    renderOutbound();
  }

  // lastError is text the peer chose, with no length limit on the node yet
  // (#127); it is clipped here and never decides a class.
  const ERROR_CLIP = 200;

  function renderOutbound() {
    const body = el("outbound-body");
    const more = el("outbound-more");
    const o = state.outbound;
    body.replaceChildren();
    if (o.error) {
      body.append(element("div", "stale", t("outbound.unreadable")), element("div", "muted", o.error));
      more.classList.add("hidden");
      return;
    }
    if (o.loading && o.messages.length === 0) {
      body.append(element("div", "muted", t("common.loading")));
      more.classList.add("hidden");
      return;
    }
    if (o.messages.length === 0) {
      // The node filtered to this session, so empty is an answer, not a page
      // that happened to hold someone else's messages.
      body.append(element("div", "empty", t("outbound.empty")));
      more.classList.add("hidden");
      return;
    }
    for (const m of o.messages) {
      const row = element("div", "logrow");
      const head = element("div", "head");
      head.append(pill(m.state ?? "unknown", outboundStateClass(m.state)));
      head.append(element("span", "mono", `→ ${nodeName(m.destinationNodeId)} · ${m.to ?? ""}`));
      row.append(head, element("span", "when", relative(m.updatedAt ?? m.createdAt)));
      const meta = element("div", "meta");
      meta.append(element("span", "", plural(m.attempts ?? 0, "outbound.attempts")));
      if (m.from) meta.append(element("span", "", t("outbound.from", { from: m.from })));
      if (m.wakeHops) meta.append(element("span", "", `wake hops ${m.wakeHops}`));
      meta.append(element("span", "", t("outbound.created", { when: relative(m.createdAt) })));
      row.append(meta);
      if (m.lastError) {
        const text = String(m.lastError);
        const err = element("div", "err clipped", text.length > ERROR_CLIP ? text.slice(0, ERROR_CLIP) + "…" : text);
        if (text.length > ERROR_CLIP) {
          const expand = element("button", "link", t("outbound.expand"));
          expand.onclick = () => {
            err.textContent = text;
            err.className = "err";
            expand.remove();
          };
          row.append(err, expand);
        } else {
          row.append(err);
        }
      }
      body.append(row);
    }
    more.classList.toggle("hidden", !o.next);
    more.disabled = o.loading;
    more.textContent = o.loading ? t("common.loadingShort") : t("inbox.loadMore");
  }

  function outboundStateClass(s) {
    if (s === "pending" || s === "delivered" || s === "refused") return s;
    return "";
  }

  function nodeName(nodeId) {
    const node = state.nodes.find((n) => n.nodeId === nodeId);
    return node ? node.displayName : (nodeId || t("outbound.unknownNode"));
  }

  let wakesRequest = 0;
  let wakesApplied = 0;

  async function loadWakes() {
    const session = state.inboxSessionAsked;
    const sequence = ++wakesRequest;
    state.wakes = { wakes: [], limits: null, loading: true, error: "", session };
    renderWakes();
    let page;
    try {
      page = await api.Wakes(session ?? "", 50);
    } catch (error) {
      page = { wakes: [], error: String(error) };
    }
    if (sequence <= wakesApplied) return;
    wakesApplied = sequence;
    // Limits only count as present when they carry a rule; a node that omits
    // them must not be quoted as "0 hops".
    const limits = page.limits && (page.limits.hops > 0 || page.limits.pair > 0 || page.limits.session > 0 || page.limits.node > 0) ? page.limits : null;
    state.wakes = { session, wakes: page.wakes ?? [], limits, loading: false, error: page.error ?? "" };
    renderWakes();
  }

  const WAKE_OUTCOMES = new Set(["woken", "refused_hops", "refused_pair_rate", "refused_session_rate", "refused_node_rate", "failed"]);

  function wakeOutcomeClass(outcome) {
    if (outcome === "woken") return "woken";
    if (outcome === "failed") return "failed";
    if (typeof outcome === "string" && outcome.startsWith("refused_") && WAKE_OUTCOMES.has(outcome)) return "refused";
    return "";
  }

  // The rule that produced a refusal, beside it: a refusal is only legible
  // next to the limit.
  function wakeLimitText(outcome, limits) {
    if (!limits) return "";
    switch (outcome) {
      case "refused_hops": return t("wakes.limitHops", { hops: limits.hops });
      case "refused_pair_rate":
        return t("wakes.limitPair", { n: limits.pair, window: limits.pairWindow });
      case "refused_session_rate":
        return t("wakes.limitSession", { n: limits.session, window: limits.sessionWindow });
      case "refused_node_rate":
        return t("wakes.limitNode", { n: limits.node, window: limits.nodeWindow });
      default: return "";
    }
  }

  function renderWakes() {
    const body = el("wakes-body");
    const limitsBox = el("wakes-limits");
    const w = state.wakes;
    body.replaceChildren();
    limitsBox.textContent = "";
    if (w.error) {
      body.append(element("div", "stale", t("wakes.unreadable")), element("div", "muted", w.error));
      return;
    }
    if (w.loading) {
      body.append(element("div", "muted", t("common.loading")));
      return;
    }
    if (w.limits) {
      limitsBox.textContent = t("wakes.limits", {
        hops: w.limits.hops,
        pair: w.limits.pair, pairWindow: w.limits.pairWindow,
        session: w.limits.session, sessionWindow: w.limits.sessionWindow,
        node: w.limits.node, nodeWindow: w.limits.nodeWindow,
      });
    }
    if (w.wakes.length === 0) {
      body.append(element("div", "empty", t("wakes.empty")));
      return;
    }
    for (const e of w.wakes) {
      const row = element("div", "logrow");
      const head = element("div", "head");
      head.append(pill(e.outcome ?? "unknown", wakeOutcomeClass(e.outcome)));
      // sourceSession is the sender's own label; sourceNodeId was proven.
      const who = element("span", "");
      if (e.sourceNodeId) {
        who.append(element("span", "fingerprint", nodeName(e.sourceNodeId)),
          element("span", "muted", t("sender.claimsToBe")));
      } else {
        who.append(element("span", "muted", t("sender.localPrefix")));
      }
      who.append(element("span", "claimed", e.sourceSession || t("wakes.noSourceSession")));
      head.append(who);
      row.append(head, element("span", "when", relative(e.at)));
      const meta = element("div", "meta");
      meta.append(element("span", "", `→ ${e.destinationSession ?? ""}`), element("span", "", `${e.hops ?? 0} hops`));
      const limit = wakeLimitText(e.outcome, w.limits);
      if (limit) meta.append(element("span", "", limit));
      row.append(meta);
      if (e.detail) row.append(element("div", "err", String(e.detail)));
      body.append(row);
    }
  }

  /* ---------------- wiring ---------------- */

  el("search").oninput = (event) => {
    state.search = event.target.value;
    savePrefsSoon();
    render();
  };

  function setSelectionForVisible(event) {
    const rows = visible();
    if (event.target.checked) rows.forEach((s) => state.selected.add(s.id));
    else rows.forEach((s) => state.selected.delete(s.id));
    render();
  }
  el("select-all").onchange = setSelectionForVisible;

  /* ---------------- background service ---------------- */

  // The node should belong to the operating system, not to this window: a node
  // that dies with a terminal drops messages while the sender is told "queued".
  // This panel is the desktop face of `ah service`; the app runs that command
  // rather than reimplementing it, so the two cannot disagree.

  let serviceRequest = 0;

  async function loadService() {
    const sequence = ++serviceRequest;
    const status = await api.ServiceStatus();
    if (sequence !== serviceRequest) return;
    state.service = status;
    renderService();
    // The checklist's first step is derived from this status, and load()
    // renders the card before this read answers. Without this line the card
    // keeps whatever it was built with — which on every launch is "no status
    // yet" — while the title bar's pill, painted by renderService above,
    // already says the service is running.
    renderOnboarding();
  }

  // renderServicePill is the title bar's one-line version of the panel below.
  function renderServicePill(status) {
    const pill_ = el("service-pill");
    const text = el("service-pill-text");
    if (status.toolError) { pill_.className = "servicepill warn"; text.textContent = t("service.pillNoAh"); return; }
    if (!status.supported) { pill_.className = "servicepill"; text.textContent = t("service.pillUnsupported"); return; }
    if (status.installed && status.running) { pill_.className = "servicepill ok"; text.textContent = t("service.pillRunning"); return; }
    if (status.installed) { pill_.className = "servicepill warn"; text.textContent = t("service.pillStopped"); return; }
    pill_.className = "servicepill warn";
    text.textContent = state.nodeReachable ? t("service.pillNotAService") : t("service.pillNodeDown");
  }

  function renderService() {
    const panel = el("service-panel");
    const status = state.service;
    if (!status) return;
    renderServicePill(status);
    panel.classList.remove("hidden");
    const line = el("service-line");
    const open = el("service-open");
    const uninstall = el("service-uninstall");
    const restart = el("service-restart");
    line.className = "line";
    // Shown only where this panel says it is; every branch below decides.
    restart.classList.add("hidden");
    if (status.toolError) {
      line.textContent = t("service.lineNoAh", { error: status.toolError });
      line.classList.add("warn");
      open.classList.add("hidden");
      uninstall.classList.add("hidden");
      // Deliberately not offered: without ah this window cannot find out
      // whether a service holds the node, and restarting the process behind a
      // launchd job or a systemd unit is how one node becomes two.
      return;
    }
    if (!status.supported) {
      // Windows, today. The node still runs here — the installer starts it and
      // puts a shortcut in the Startup folder — it is just not registered with
      // anything this app can ask. So the one thing an owner actually needs
      // from this panel, applying a setting the node only reads at start-up,
      // is done by this app itself (desktop/nodeprocess.go).
      line.textContent = state.nodeReachable
        ? t("service.lineUnsupportedRunning")
        : t("service.lineUnsupportedStopped");
      line.classList.add(state.nodeReachable ? "ok" : "warn");
      open.classList.add("hidden");
      uninstall.classList.add("hidden");
      restart.classList.remove("hidden");
      restart.textContent = state.nodeReachable ? t("service.restart") : t("service.start");
      return;
    }
    if (status.installed && status.running) {
      line.textContent = t("service.lineRunning", { pid: status.pid, unit: status.unitPath });
      line.classList.add("ok");
      open.textContent = t("service.reinstallFlags");
      restart.classList.remove("hidden");
      restart.textContent = t("service.restart");
    } else if (status.installed) {
      line.textContent = t("service.lineInstalledStopped", { log: status.logHint });
      line.classList.add("warn");
      open.textContent = t("service.reinstall");
      restart.classList.remove("hidden");
      restart.textContent = t("service.start");
    } else if (state.nodeReachable) {
      line.textContent = t("service.lineNotAService");
      line.classList.add("warn");
      open.textContent = t("service.install");
      restart.classList.remove("hidden");
      restart.textContent = t("service.restart");
    } else {
      line.textContent = t("service.lineNothing");
      line.classList.add("warn");
      open.textContent = t("service.install");
    }
    open.classList.remove("hidden");
    uninstall.classList.toggle("hidden", !status.installed);
    renderServiceRepair(status);
    // The form opens itself only when nothing is running: that is the moment
    // the owner has nothing else to do here.
    if (!state.nodeReachable && !status.installed && !state.serviceFormTouched) openServiceForm().catch(() => {});
  }

  // renderServiceRepair states the one fact this panel knows and the settings
  // form cannot work out for itself.
  //
  // A unit that passes a node setting on every start wins over everything the
  // settings page saves, and the node's own log is the only other place that
  // says so. It is a statement here and no longer a button: the repair reinstalls
  // the service, which is not a thing to do while reading a status line, and the
  // moment it matters is the moment a write is about to be undone. That moment
  // is Save on the node settings form, which is where it is now asked.
  function renderServiceRepair(status) {
    const repair = el("service-repair");
    repair.replaceChildren();
    const pinned = status.pinnedSettings ?? [];
    if (!status.installed || pinned.length === 0) return;
    repair.append(
      element("div", "stale",
        t("service.pinnedSettings", { pinned: pinned.join(t("candidate.flagJoin")) })));
  }

  // reinstallWithoutPinnedSettings re-registers the service with the database it
  // already uses and nothing else.
  //
  // The database path is carried over deliberately and is the whole reason this
  // is not just "press install": a reinstall that dropped it would put the node
  // on a different database, which is a different identity and no pairings.
  //
  // Answers whether it ran, so the save that asked for it knows whether to go
  // ahead: an owner who says no to re-registering has not agreed to a write
  // that a unit flag would silently undo a second later.
  //
  // `touched` is the pinned flags the save that asks is about to write. The
  // question lists every flag the unit pins, not only those: InstallService
  // re-registers with the database path alone, so a yes unpins all of them —
  // their values carry over because the node remembers what it was last given
  // (docs/ui-contract.md §7.8 rule 3), but an owner asked about one setting
  // should not learn afterwards that four were affected (#194). The ones this
  // save changes are marked, because those are what a "no" leaves out.
  async function reinstallWithoutPinnedSettings(status, touched = []) {
    if (status.installed && !status.dbPathKnown) {
      banner(t("service.unpinNeedsDbPath"));
      return false;
    }
    const ok = await askConfirm({
      title: t("service.unpinConfirmTitle"),
      body: t("service.unpinConfirm", {
        path: status.dbPath || t("service.nodeDefaultLocation"),
        pinned: pinnedSettingsList(status.pinnedSettings ?? [], touched),
      }),
      confirmLabel: t("service.unpinConfirmAction"),
    });
    if (!ok) return false;
    const previousPid = state.service?.pid ?? 0;
    // Whether the unit was actually re-registered, which is not whether this
    // function got to the end: withBusy catches a failed InstallService and
    // shows it in a banner, and answering true after that sent the save on as
    // if the unit had been cleared — for the pinned flags to undo it at the
    // next start (#194). Set once the install has answered, because from
    // there the unit is the new one whatever the read-back says; and never set
    // when withBusy did not run at all, which is another write in flight.
    let reregistered = false;
    await withBusy(t("service.busyReregister"), async () => {
      const result = await api.InstallService({ dbPath: status.dbPath });
      reregistered = true;
      showServiceOutput(result);
      const up = await waitForNode({ previousPid });
      await load();
      banner(up.answering ? t("service.reregistered") : t("service.reregisteredNoAnswer"), up.answering);
    });
    return reregistered;
  }

  // pinnedSettingsList names each flag the unit pins by the form's own label,
  // and marks the ones in `touched`.
  function pinnedSettingsList(pinned, touched) {
    const changing = new Set(touched);
    // peer-listen is pinned under one flag and has two spellings here; the
    // one the form shows is the one it is named by.
    const other = peerListensSupported() ? "peerListen" : "peerListens";
    const keyFor = Object.fromEntries(Object.entries(NODE_SETTING_FLAGS)
      .filter(([key]) => key !== other).map(([key, flag]) => [flag, key]));
    return pinned.map((flag) => {
      const label = keyFor[flag] ? nodeSettingLabel(keyFor[flag]) : flag;
      return changing.has(flag) ? t("service.pinnedChanging", { label }) : label;
    }).join(t("candidate.flagJoin"));
  }

  async function openServiceForm() {
    state.serviceFormTouched = true;
    el("service-form").classList.remove("hidden");
    el("service-output").classList.add("hidden");
    // Prefilled with the path the service already uses, because the field's
    // meaning is "which database", not "change the database": an owner who
    // opens this to reinstall means to keep the node they have. Blank meant the
    // node's default, which is a different database, a new identity and no
    // pairings — reached once by someone who read the empty field as
    // "unchanged".
    const status = state.service ?? {};
    const note = el("service-db-note");
    note.replaceChildren();
    // Three states, and they are not two. "There is no service" and "there is a
    // service and this window cannot read what database it uses" both leave the
    // field empty, and only the first of them is safe to install from without
    // asking: the second is a reinstall that would move a running node onto the
    // default database. That is not an edge case — it is every Windows machine,
    // where the scheduled task's XML is not read back.
    state.serviceDB = {
      installed: Boolean(status.installed),
      known: Boolean(status.installed && status.dbPathKnown),
      path: status.installed && status.dbPathKnown ? status.dbPath : "",
    };
    // Cleared, not left: this element outlives the panel it was filled for, so
    // an install form opened after reading a machine that had a unit would
    // otherwise propose that machine's database on a machine with none.
    el("service-db").value = state.serviceDB.path;
    if (state.serviceDB.known) {
      note.textContent = state.serviceDB.path
        ? t("service.dbNoteKnown")
        : t("service.dbNoteDefault");
      return;
    }
    note.textContent = status.installed ? t("service.dbNoteUnknown") : t("service.dbNoteFirstInstall");
  }

  function readServiceForm() {
    return {
      dbPath: el("service-db").value.trim(),
      peerListen: "",
      allowLan: false,
      discover: false,
      treatAsPrivate: [],
      autoWake: false,
    };
  }

  function showServiceOutput(result) {
    const output = el("service-output");
    output.textContent = `$ ${result.command}\n${result.output}`;
    output.classList.remove("hidden");
  }

  async function installService() {
    // The one change on this form that cannot be undone by changing it back.
    //
    // A different database is a different node.key, so this machine gets a new
    // identity, every paired node stops recognising it, and the sessions the
    // old one published are gone from every peer's list. None of that announces
    // itself: the panel would go green and the 區網 page would simply be empty.
    // So it is said before it happens, with both paths named, and only when the
    // owner actually changed the field.
    const baseline = state.serviceDB ?? { installed: false, known: false, path: "" };
    const wanted = el("service-db").value.trim();
    if (baseline.installed && !baseline.known) {
      // Asked without being able to say what the current value is, because the
      // alternative is installing over a running node's database on a guess.
      // The question names that uncertainty rather than hiding it.
      const ok = await askConfirm({
        title: t("service.reinstallUnknownDbConfirmTitle", { wanted: wanted || t("service.nodeDefaultLocation") }),
        body: t("service.reinstallUnknownDbConfirm"),
        confirmLabel: t("service.reinstallConfirmAction"),
        danger: true,
      });
      if (!ok) return;
    } else if (baseline.installed && wanted !== baseline.path) {
      const ok = await askConfirm({
        title: t("service.changeDbConfirmTitle", {
          current: baseline.path || t("service.nodeDefaultLocation"),
          wanted: wanted || t("service.nodeDefaultLocation"),
        }),
        body: t("service.changeDbConfirm"),
        confirmLabel: t("service.changeDbConfirmAction"),
        danger: true,
      });
      if (!ok) return;
    }
    await withBusy(t("service.busyInstall"), async () => {
      let result;
      try {
        result = await api.InstallService(readServiceForm());
      } catch (error) {
        // ah's own words are the useful part of a failure; the banner is too
        // small for them, so they go where the output goes.
        const output = el("service-output");
        output.textContent = String(error);
        output.classList.remove("hidden");
        throw error;
      }
      showServiceOutput(result);
      el("service-form").classList.add("hidden");
      banner(t("service.installed"), true);
      await load();
    });
  }

  // restartNode applies settings the node only reads at start-up, by whatever
  // means this machine has: a registered service through ah, and otherwise the
  // app stopping and starting the node itself. Which of the two happened is
  // the Go side's decision (desktop/nodeprocess.go); what comes back names the
  // command either way, and it goes on screen verbatim.
  async function restartNode() {
    // Read before anything is asked of the service manager: it is what says
    // whether the node answering afterwards is a new one.
    const previousPid = state.service?.pid ?? 0;
    await withBusy(t("service.restart"), async () => {
      let result;
      try {
        result = await api.RestartNode();
      } catch (error) {
        // The failures here are the ones an owner has to act on — a node that
        // would not stop, or one that did not come back because of the setting
        // they just saved — and each names where to look. Too long for the
        // banner, so they go where every other command's words go.
        const output = el("service-output");
        output.textContent = String(error);
        output.classList.remove("hidden");
        throw error;
      }
      showServiceOutput(result);
      // Not "restarted" yet. `ah service restart` answers as soon as the
      // service manager accepts the job, and a node that exits two seconds
      // later leaves that sentence on screen as the only thing anyone said —
      // which is how a machine came to restart into the same failure 107 times
      // with the panel showing one unchanging line.
      const up = await waitForNode({ previousPid });
      await load();
      if (up.answering) {
        banner(up.degraded ? t("service.restartedDegraded") : t("service.restarted"), !up.degraded);
        return;
      }
      banner(t("service.restartedNoAnswer", {
        seconds: up.seconds,
        log: state.service?.logHint ?? t("service.logHintFallback"),
      }));
    });
  }

  // waitForNode asks the node whether it is there, for a few seconds, because
  // "the service manager accepted the job" and "the node is running" are
  // different facts and only the second one is what the owner asked for.
  //
  // The answer also says whether it came back degraded — up, serving loopback,
  // and unreachable by every peer — which is a success that must not be
  // reported as an ordinary one.
  async function waitForNode({ attempts = 8, delay = 750, previousPid = 0 } = {}) {
    for (let attempt = 0; attempt < attempts; attempt++) {
      // The wait comes first. `ah service restart` answers as soon as the
      // service manager accepts the job, and the process being replaced is
      // still up for a moment after that — so an immediate probe is answered by
      // the node that is on its way out, and the restart is reported as a
      // success on the strength of the settings the old process was running.
      await new Promise((resolve) => setTimeout(resolve, delay));
      if (previousPid) {
        // And a pid still equal to the one from before is that same process,
        // whatever it answers. This is the difference between "it came back"
        // and "it has not gone yet", and reporting the second as the first is
        // how a machine restarting into the same failure every ten seconds
        // reads as a machine that is fine.
        const status = await serviceStatusOrUnknown();
        if (!status.unknown && state.service?.pid === previousPid) continue;
      }
      try {
        const view = await api.NodeSettings();
        if (!view.error) return { answering: true, degraded: Boolean(view.peerListenProblem) };
      } catch {
        // Not there yet, or not there at all. The loop decides which.
      }
    }
    return { answering: false, seconds: Math.round((attempts * delay) / 1000) };
  }

  async function uninstallService() {
    const ok = await askConfirm({
      title: t("service.uninstallConfirmTitle"),
      body: t("service.uninstallConfirm"),
      confirmLabel: t("service.uninstallConfirmAction"),
      danger: true,
    });
    if (!ok) return;
    await withBusy(t("service.busyUninstall"), async () => {
      const result = await api.UninstallService();
      showServiceOutput(result);
      banner(t("service.uninstalled"), true);
      await load();
    });
  }

  for (const segment of document.querySelectorAll("#view-switch span[data-view]")) {
    segment.onclick = () => {
      state.view = segment.dataset.view;
      render();
      // Read on arrival rather than on the next poll: a candidate list that is up
      // to five seconds stale when the view opens is one the owner will read as
      // "nobody is advertising".
      if (state.view === "network") loadPairing().catch(() => {});
    };
  }

  // The node list's primary button opens the EXCHANGE, not the five-field form.
  //
  // It used to open the manual dialog, which asks for a base64 public key
  // carried across by hand — so the most prominent button on the network view
  // put a newcomer in front of the fallback while the flow that finds the other
  // machine for them sat behind a secondary link. The form is still one click
  // away, from the drawer's own footer, which is where the sentence explaining
  // when to use it already is.
  el("btn-pair").onclick = () => openPairingDrawer().catch(() => {});
  el("pair-close").onclick = closePairModal;
  el("copy-local-public-key").onclick = () => copyLocalPublicKey();
  el("pair-modal").onclick = (event) => {
    if (event.target === el("pair-modal")) closePairModal();
  };
  el("pair-submit").onclick = () =>
    withBusy(t("pairManual.busy"), async () => {
      const node = await api.TrustNode(
        el("pair-node-id").value.trim(),
        el("pair-display-name").value.trim(),
        el("pair-platform").value.trim(),
        el("pair-public-key").value.trim(),
        el("pair-fingerprint").value.trim()
      );
      closePairModal();
      state.selectedNode = node.nodeId;
      await load();
      // Not marked successful, so it stays on screen. A four-second green
      // banner is how "you are half done" gets missed, and half-done pairing is
      // exactly what happened on 2026-09-10: the mac was paired, the Ubuntu box
      // still answered `No paired nodes`, and nothing said so.
      banner(t("pairManual.trusted", { name: node.displayName }));
    });

  el("btn-audience").onclick = openAudienceModal;
  el("audience-close").onclick = closeAudienceModal;
  el("audience-modal").onclick = (event) => {
    if (event.target === el("audience-modal")) closeAudienceModal();
  };
  for (const radio of document.querySelectorAll('input[name="audience-mode"]')) {
    radio.onchange = syncAudienceForm;
  }
  // A preset writes the boxes; a box unsets the preset. Never the other way
  // round, so nothing this dialog shows is a value it invented.
  for (const radio of document.querySelectorAll('input[name="audience-preset"]')) {
    radio.onchange = () => applyAudiencePreset(radio.value);
  }
  for (const id of AUDIENCE_FLAG_IDS) {
    el(id).onchange = syncAudiencePreset;
  }
  el("audience-apply").onclick = () => {
    const audience = readAudienceForm();
    if (audience.mode === "selected" && audience.nodes.length === 0) {
      banner(t("audience.needsANode"));
      return;
    }
    applyAudience(audience, audience.mode);
  };

  el("btn-unpublish").onclick = () =>
    applyAudience(
      { mode: "none", nodes: [], exportCwd: false, acceptMessages: false, allowOutbound: false, autoWake: false },
      "none",
    );

  el("btn-reload").onclick = () => withBusy(t("app.reload"), load);

  // discoverSessions is a named function rather than a handler body because the
  // first-launch checklist presses the same button. A second copy of this would
  // be a second rescan with its own banner, its own skipped count and its own
  // bugs.
  async function discoverSessions() {
    return withBusy(t("app.rescan"), async () => {
      const counts = await api.Discover();
      await load();
      const skipped = counts.skipped ?? 0;
      const total = counts.total ?? 0;
      // A scan that found nothing carries the explanation that used to be a
      // checklist step: AgentHub reads what those two tools leave on disk, so
      // an empty answer is a fact about this machine's disk rather than about
      // AgentHub. Said once, here, where the answer is.
      banner(t("app.rescanned", {
        claude: counts.claude, codex: counts.codex, total: counts.total,
      }) + (skipped > 0 ? t("app.rescanSkipped", { skipped }) : "")
        + (total === 0 ? t("app.rescanNothingFound") : ""), skipped === 0 && total > 0);
    });
  }

  el("btn-discover").onclick = () => discoverSessions().catch(() => {});

  el("btn-heartbeat").onclick = () =>
    withBusy(t("heartbeat.busy"), async () => {
      el("modal-body").textContent = await api.Heartbeat();
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
      result = await api.Pairing();
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

  // loadPairRequests reads the exchange's own rows.
  //
  // Separate from loadPairing, and asked for only while the drawer is open,
  // because it is not a free read: the node polls every pending outgoing
  // request at the far side before answering. A window that asked for it every
  // five seconds forever would keep dialling machines for as long as it was
  // left running.
  //
  // Sequence-guarded like every other read here. Two are in flight at once
  // whenever a decision's re-read overlaps the two-second tick, and the older
  // answer describes the moment before the decision.
  let pairRequestsRequest = 0;
  let pairRequestsApplied = 0;

  // render: false is for the drawer's own tick, whose second leg ends in the
  // overview's render() and therefore in renderPairRequests anyway. Rendering
  // here as well wrote the rows twice per tick over data nothing had changed.
  async function loadPairRequests({ render = true } = {}) {
    const sequence = ++pairRequestsRequest;
    const all = state.pairRequestsAll;
    let rows;
    try {
      rows = await api.PairRequests(all);
    } catch (error) {
      if (sequence <= pairRequestsApplied) return;
      pairRequestsApplied = sequence;
      // A failed read is not a fact about the other machine, so the rows are
      // left alone and the panel says which of the two this is.
      state.pairRequestsError = String(error);
      if (render) renderPairRequests();
      return;
    }
    if (sequence <= pairRequestsApplied) return;
    pairRequestsApplied = sequence;
    state.pairRequests = Array.isArray(rows) ? rows : [];
    state.pairRequestsError = "";
    state.pairRequestsLoaded = true;
    if (render) renderPairRequests();
  }

  el("mcp-close").onclick = closeMCPConfig;
  el("mcp-modal").onclick = (event) => {
    if (event.target === el("mcp-modal")) closeMCPConfig();
  };
  el("inbox-close").onclick = closeInbox;
  el("inbox-modal").onclick = (event) => {
    if (event.target === el("inbox-modal")) closeInbox();
  };
  el("inbox-clear").onclick = async () => {
    const session = state.inboxSession;
    if (!session) return;
    // Not undoable, so it is asked rather than assumed. The node has no
    // "unclear", and messages that arrive between this dialog and the confirm go
    // with the rest. The session is the one the question named, captured
    // before it was asked.
    const ok = await askConfirm({
      title: t("inbox.clearConfirmTitle"),
      body: t("inbox.clearConfirm", { session }),
      confirmLabel: t("inbox.clearConfirmAction"),
      danger: true,
    });
    if (!ok) return;
    await withBusy(t("inbox.busyClear"), async () => {
      const cleared = await api.ClearInbox(session);
      // Re-read through openInbox, so the answer is sequence-guarded like every
      // other read and lands on the session it was asked about. The outcome is
      // carried into the dialog rather than a banner, which the dialog covers —
      // and a failure has to be visible there, or the owner is left with an
      // unchanged list and no sign the action did not happen.
      await openInbox(session, cleared);
    });
  };

  el("btn-pairing-on").onclick = () =>
    withBusy(t("pair.busyOpen"), async () => {
      // No duration: the node's own default is the one the node documents, and
      // sending a number from here would make this window disagree with `ah`.
      await api.OpenPairing(0);
      await loadPairing();
    });

  el("btn-pairing-off").onclick = () =>
    withBusy(t("pair.busyClose"), async () => {
      await api.ClosePairing();
      await loadPairing();
    });

  /* ---------------- the pairing exchange's own controls ---------------- */

  el("copy-pair-address").onclick = () => copyPairAddress();
  el("btn-pair-send").onclick = () => sendPairRequest(el("pair-address").value);
  el("pair-address").onkeydown = (event) => {
    // Enter in the address field sends, because that is what a field with one
    // button beside it is for.
    if (event?.key === "Enter") sendPairRequest(el("pair-address").value);
  };
  el("pair-requests-all").onchange = () => {
    state.pairRequestsAll = Boolean(el("pair-requests-all").checked);
    loadPairRequests().catch(() => {});
  };


  /* ---------------- node settings ---------------- */

  // The node's own start-up settings (#116). They live in the node's database,
  // not in the service unit, so changing one does not mean reinstalling the
  // service — but the node only reads them when it starts, so a write that is
  // not followed by a restart changes nothing about the running process.
  //
  // Reads and writes are numbered like every other read here: a slow answer
  // must not repaint a form the owner has since edited or saved.
  let nodeSettingsRequest = 0;
  let nodeSettingsApplied = 0;

  // The address a node serves when nobody has named one.
  const LOOPBACK_LISTEN = "127.0.0.1:7463";

  // isPrivateByDefinition is nodeconfig's "private by definition" half: RFC 1918,
  // RFC 4193 and link-local. The owner's declared ranges are the other half and
  // are read from the form, so an address this machine no longer offers is
  // still judged — the interface list is a convenience, not the rule.
  function isPrivateByDefinition(address) {
    const host = hostOf(address);
    const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
    if (v4) {
      const [a, b] = [Number(v4[1]), Number(v4[2])];
      if (a === 10) return true;
      if (a === 172 && b >= 16 && b <= 31) return true;
      if (a === 192 && b === 168) return true;
      if (a === 169 && b === 254) return true;
      return false;
    }
    // fc00::/7 (unique local) and fe80::/10 (link-local).
    return /^f[cd]/.test(host) || /^fe[89ab]/.test(host);
  }

  // hostOf strips the port and any brackets, lower-cased.
  function hostOf(address) {
    const value = String(address ?? "").trim();
    const cut = value.lastIndexOf(":");
    if (cut < 0) return value.toLowerCase();
    let host = value.slice(0, cut).trim().toLowerCase();
    if (host.startsWith("[") && host.endsWith("]")) host = host.slice(1, -1);
    return host;
  }

  // isLoopbackListen follows nodeconfig.ValidateLoopback: the host decides, the
  // port never does. 127.0.0.1:9999 and [::1]:7463 are as much off-network as
  // the default, and treating them as LAN is how a form can turn allowLan on
  // for a node that was never exposed.
  function isLoopbackListen(address) {
    const value = String(address ?? "").trim();
    if (value === "") return true;
    const cut = value.lastIndexOf(":");
    if (cut < 0) return false;
    let host = value.slice(0, cut).trim().toLowerCase();
    if (host.startsWith("[") && host.endsWith("]")) host = host.slice(1, -1);
    if (host === "localhost" || host === "::1") return true;
    // An IPv4-mapped address is the same address, in either spelling:
    // ::ffff:127.0.0.1 and ::ffff:7f00:1 both parse to 127.0.0.1 for Go, whose
    // IsLoopback is the rule this follows.
    if (host.startsWith("::ffff:")) {
      const mapped = host.slice(7);
      const hex = /^([0-9a-f]{1,4}):([0-9a-f]{1,4})$/.exec(mapped);
      if (hex) {
        const high = parseInt(hex[1], 16);
        const low = parseInt(hex[2], 16);
        return (high >> 8) === 127;
      }
      host = mapped;
    }
    return /^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(host);
  }

  async function loadNodeSettings() {
    const sequence = ++nodeSettingsRequest;
    el("node-settings-state").textContent = t("common.loadingShort");
    let view;
    try {
      view = await api.NodeSettings();
    } catch (error) {
      view = { error: String(error) };
    }
    // Both awaits happen before anything is touched. Painting used to await in
    // the middle of itself, so two overlapping reads ran interleaved: the
    // baseline ended up from one and the form from the other, and the next save
    // wrote fields the owner never touched.
    const addresses = view.error ? { list: [], failure: "" } : await fetchLocalAddresses();
    if (sequence <= nodeSettingsApplied) return;

    nodeSettingsApplied = sequence;
    applyNodeSettings(view, addresses);
  }

  // fetchLocalAddresses never throws: a failure is a thing to say, not a thing
  // to stop the form appearing.
  async function fetchLocalAddresses() {
    try {
      return { list: (await api.LocalAddresses()) ?? [], failure: "" };
    } catch (error) {
      return { list: [], failure: String(error) };
    }
  }

  // applyNodeSettings paints the WHOLE form from the node's answer, in one go.
  //
  // Never a merge of just what was sent: turning allowLan off pulls peerListen
  // back to loopback in the same write, and since #134 any write does it when
  // the stored allowLan is already false. A form that kept its own idea of the
  // fields it did not send would show a LAN address the node no longer has.
  //
  // Synchronous on purpose — see loadNodeSettings.
  function applyNodeSettings(view, addresses = { list: [], failure: "" }) {
    const notice = el("node-settings-notice");
    notice.replaceChildren();

    if (view.error) {
      // The baseline is NOT replaced. state.nodeSettings is what the next save
      // diffs against, and the form still shows the last good values: adopting
      // an empty answer here would make every field look changed, so a later
      // save would write fields the owner never touched and drop the ones they
      // did.
      el("node-settings-state").textContent = t("nodeSettings.unreadable");
      notice.append(
        element("div", "stale", state.nodeSettings
          ? t("nodeSettings.unreadableStale")
          : t("nodeSettings.unreadableEmpty")),
        element("div", "muted", view.error),
      );
      return;
    }

    state.nodeSettings = view;
    state.nodeAddresses = addresses;
    // The form edits what the NEXT start will use, which is `saved`, not what
    // is running. The node merges a write onto the saved configuration and
    // judges it there — its own comment in internal/api/settings.go says so —
    // so a form painted from the running values disagrees with the node
    // wherever a command-line flag pinned something: it would show allowLan
    // ticked over a stored false, never be able to send allowLan:true (the box
    // already matches what is on screen), and let an unrelated save withdraw a
    // remembered LAN address without a word.
    const running = view.settings ?? {};
    const sources = view.sources ?? {};
    const saved = view.saved ?? {};
    el("node-settings-state").textContent = "";

    fillPeerListenOptions(saved.peerListen ?? "", addresses.list);
    const multi = peerListensSupported(view);
    el("node-peerlisten-field").classList.toggle("hidden", multi);
    el("node-peerlistens-field").classList.toggle("hidden", !multi);
    peerListenTicks = [];
    if (multi) fillPeerListenRows(saved.peerListens, addresses.list);
    else {
      el("node-peerlistens").replaceChildren();
      peerListenRows = [];
    }
    el("node-allow-lan").checked = Boolean(saved.allowLan);
    el("node-discover").checked = Boolean(saved.discover);
    el("node-autowake").checked = Boolean(saved.autoWake);
    el("node-private").value = (saved.treatAsPrivate ?? []).join(", ");
    // Whatever the node holds is the owner's, not a suggestion of ours.
    state.nodePrivateSuggested = "";

    for (const [id, key] of [
      ["node-peerlisten-source", "peerListen"],
      ["node-allowlan-source", "allowLan"],
      ["node-discover-source", "discover"],
      ["node-private-source", "treatAsPrivate"],
      ["node-autowake-source", "autoWake"],
    ]) {
      el(id).textContent = describeSource(sources[key], running[key], saved[key]);
    }
    // The list's tag: the node keeps one provenance for both spellings.
    el("node-peerlistens-source").textContent = peerListensSupported(view)
      ? describeSource(sources.peerListen, running.peerListens, saved.peerListens)
      : "";

    // The node's own sentence about what the write did, verbatim: it names the
    // address it pulled back and what to send to keep it.
    if (view.message) notice.append(element("div", "muted", view.message));
    // Only while a withdrawal stands. Absent is not false-with-a-reason, it is
    // "no withdrawal" — so nothing is said unless the node says it.
    if (view.peerListenWithdrawn) {
      notice.append(element("div", "stale", t("nodeSettings.peerListenWithdrawn")));
    }
    renderPeerListenProblem(notice, view, addresses);
    // Said out loud rather than swallowed: without the list the owner sees only
    // 「只在本機」 and no way to pick the address they are looking for, which
    // reads as the app having decided for them.
    if (addresses.failure) {
      notice.append(
        element("div", "stale", t("nodeSettings.addressesUnreadable")),
        element("div", "muted", addresses.failure),
      );
    }
    el("node-settings-hint").textContent = view.restartRequired
      ? t("nodeSettings.hintRestart")
      : t("nodeSettings.hint");
    // A suggestion belongs to a change the owner makes, never to a repaint:
    // the node's own ranges are on screen and must not be edited behind them.
    syncNodeSettingsForm();
  }

  // renderPeerListenProblem says the node is up but unreachable, and offers the
  // way out.
  //
  // This state used to be unreachable from this window, because it used to kill
  // the node: an address that will not bind ended the process, the owner's API
  // died with it, and the settings page could not be read, let alone written.
  // The node now degrades to loopback and stays up, which is what makes a
  // button here possible at all — so this is where that fix gets spent.
  //
  // What is offered is an address, not a diagnosis. The owner does not have to
  // know what a peer listener is to understand "the cable this used is gone,
  // use the Wi-Fi instead", and each button is the whole repair: it fills the
  // form in, presses save, restarts, and reports what the node came back as.
  function renderPeerListenProblem(notice, view, addresses) {
    const problem = view.peerListenProblem;
    if (!problem) return;
    notice.append(
      element("div", "stale",
        problem.reason === "address_gone"
          ? t("nodeSettings.problemAddressGone",
            { address: problem.address, runningOn: problem.runningOn })
          : problem.reason === "port_in_use"
            ? t("nodeSettings.problemPortInUse",
              { address: problem.address, runningOn: problem.runningOn })
            : t("nodeSettings.problemBind",
              { address: problem.address, runningOn: problem.runningOn })),
      element("div", "muted", problem.detail || problem.message),
    );
    const actions = document.createElement("div");
    actions.className = "repairactions";
    for (const option of peerListenRepairs(problem, addresses, el("node-allow-lan").checked)) {
      const button = document.createElement("button");
      button.className = option.primary ? "primary" : "ghost";
      button.textContent = option.label;
      button.onclick = () => applyPeerListenRepair(option);
      actions.append(button);
    }
    notice.append(actions);
  }

  // peerListenRepairs is what this machine can actually offer right now.
  //
  // Only addresses the node would accept without a further declaration: an
  // address on a network that is private by its numbers needs nothing else,
  // and one that is not needs 「視為私有網段」 filled in too. A one-click repair
  // that lands on a refusal is worse than no button, so the ones that need a
  // second decision are left to the dropdown, where that decision is visible.
  function peerListenRepairs(problem, addresses, allowLanAlreadyOn) {
    const repairs = [];
    const port = peerListenPort(problem.address);
    if (problem.reason === "port_in_use") {
      const host = problem.address.slice(0, problem.address.lastIndexOf(":"));
      const next = Number(port) + 1;
      // Not offered below 1024. Down there the more likely refusal is
      // permission rather than a neighbour — the node classifies by asking
      // whether this machine will serve the address at all, and an ephemeral
      // port answers yes for a process that still cannot have 443 — and the
      // next port up is just as privileged, so the button would land on the
      // identical failure.
      // With the list every entry shares the port, so the next one up moves
      // them all; the button names each address it will ask for.
      const moved = peerListensSupported()
        ? (state.nodeSettings?.saved?.peerListens ?? [])
          .filter((address) => !isLoopbackListen(address))
          .map((address) => `${address.slice(0, address.lastIndexOf(":"))}:${next}`)
        : [];
      if (Number.isFinite(next) && next < 65536 && Number(port) >= 1024) {
        repairs.push({
          label: t("nodeSettings.repairPort", { address: moved.length > 1 ? moved.join(", ") : `${host}:${next}` }),
          ...(moved.length > 1 ? { peerListens: moved } : {}),
          peerListen: `${host}:${next}`,
          // Carried, not set. A port number is not a decision about whether
          // anything may leave this machine, and turning that switch on here
          // would be the silent tick the address branch goes out of its way to
          // avoid: a listener on 127.0.0.1:9001 with allowLan off is off the
          // network, and clicking "use the next port" must not change that.
          allowLan: allowLanAlreadyOn,
          primary: true,
        });
      }
    } else {
      // Filtered before it is trimmed, not after. A Mac with a few utun VPN
      // interfaces enumerates them ahead of en0, so taking the first three and
      // then dropping the non-private ones can leave nothing at all — on a
      // machine that has a perfectly good Wi-Fi address.
      const usable = (addresses.list ?? []).filter((item) => item.private && `${item.address}:${port}` !== problem.address);
      for (const item of usable.slice(0, 3)) {
        const address = `${item.address}:${port}`;
        repairs.push({
          // The switch is named only when clicking this would turn it on. This
          // window does not tick that box behind anyone, and a button that did
          // so silently would be doing exactly that — but a label that promises
          // to turn on something already on is its own kind of wrong, and it is
          // the one an owner notices, because nothing happens.
          label: allowLanAlreadyOn
            ? t("nodeSettings.repairAddress",
              { interface: item.interface, address: item.address })
            : t("nodeSettings.repairAddressAndLan",
              { interface: item.interface, address: item.address }),
          peerListen: address,
          allowLan: true,
          primary: repairs.length === 0,
        });
      }
    }
    // Always last, and always there. An owner who has decided to be off the
    // network for now needs a way to say so, or this banner returns on every
    // start and becomes the thing they learn to ignore.
    repairs.push({ label: t("nodeSettings.repairLoopback"), peerListen: "", allowLan: false, primary: false });
    return repairs;
  }

  function peerListenPort(address) {
    const index = address.lastIndexOf(":");
    const port = index < 0 ? "" : address.slice(index + 1);
    return /^\d+$/.test(port) ? port : "7463";
  }

  // applyPeerListenRepair fills the form in and presses save.
  //
  // Through the same path a person's own edit takes, rather than a second one
  // of its own: that path validates, saves, restarts, re-reads and says whether
  // what was asked for survived. A shortcut here would be a second way to
  // change this setting, with its own bugs, reporting success on its own terms.
  async function applyPeerListenRepair(option) {
    if (peerListensSupported()) {
      // A repair names the whole set it stands for: every address for 「全部開放」,
      // one for a single address, none for 「就先只在本機」. The rows are rebuilt
      // with exactly those ticked, so an address the list did not carry (the
      // next port up) gets its row rather than being dropped.
      const list = option.peerListens ?? (option.peerListen ? [option.peerListen] : []);
      peerListenTicks = [];
      fillPeerListenRows(list, state.nodeAddresses?.list);
      el("node-allow-lan").checked = option.allowLan;
      syncNodeSettingsForm();
      await saveNodeSettings();
      return;
    }
    const select = el("node-peerlisten");
    // The option has to exist before it can be selected. The list is built from
    // this machine's addresses at the node's default port, so any repair that
    // changes the port — which is the whole of the port_in_use case — names an
    // address no option carries, and assigning an unmatched value to a <select>
    // selects nothing. "Nothing" in this list is loopback: the owner would click
    // 改用 …:7464, the node would be saved as local-only, and the panel would
    // report it as a success.
    if (option.peerListen && ![...select.options].some((existing) => existing.value === option.peerListen)) {
      const added = document.createElement("option");
      added.value = option.peerListen;
      added.textContent = `${option.peerListen} · ${t("nodeSettings.optionRepairChosen")}`;
      added.dataset.private = "1";
      select.append(added);
    }
    select.value = option.peerListen;
    el("node-allow-lan").checked = option.allowLan;
    syncNodeSettingsForm();
    await saveNodeSettings();
  }

  // applyPeerListenRepairFromCard is the pairing drawer's way into the same
  // repair. (It was the checklist's; that step is now the drawer's first one.)
  //
  // applyPeerListenRepair fills the settings form and presses save, which is
  // right when the button is IN that form: what is on screen is what the owner
  // means to send. From the drawer it is not — that panel is elsewhere,
  // and the form may be holding edits nobody has saved. Pressing save there
  // would commit a private range or an auto-wake tick the owner was still
  // thinking about, as a side effect of a button about a listening address.
  // So a dirty form is refused and named, rather than silently carried along.
  async function applyPeerListenRepairFromCard(option) {
    // One of those fields may not be the owner's at all — see below.
    withdrawUntouchedPrivateSuggestion();
    // Every field the save would send, minus the two this button is here to
    // set. Anything left is an edit that belongs to the owner, not to us.
    const carried = Object.keys(readNodeSettingsPatch())
      .filter((field) => field !== "peerListen" && field !== "peerListens" && field !== "allowLan");
    if (carried.length > 0) {
      // Named, not just counted. "There are unsaved changes" over a form the
      // owner does not remember editing is a dead end; the field's own label is
      // what takes them to the thing to save or read again.
      banner(t("pair.formDirty") + " "
        + t("pair.formDirtyFields", { fields: carried.map(nodeSettingsFieldLabel).join(", ") }));
      goToNodeSettings();
      return;
    }
    await applyPeerListenRepair(option);
    // The save restarts the node, and a node that restarts comes back with its
    // pairing window closed — so the owner was left in an open drawer that
    // said "open to pairing" about a window that no longer existed (#194).
    // Read what came back and open a window again, but only if the drawer is
    // still there to show it: one opened behind a closed drawer is a window
    // nothing on screen would close.
    if (!pairingDrawerOpen()) return;
    await loadPairing();
    await openPairingWindowIfNeeded({ keepBanner: true });
  }

  const NODE_SETTINGS_FIELD_LABELS = {
    peerListen: "nodeSettings.peerListenLabel",
    peerListens: "nodeSettings.peerListensLabel",
    allowLan: "nodeSettings.allowLan",
    discover: "nodeSettings.discover",
    autoWake: "nodeSettings.autoWake",
    treatAsPrivate: "nodeSettings.privateLabel",
  };

  function nodeSettingsFieldLabel(field) {
    const key = NODE_SETTINGS_FIELD_LABELS[field];
    return key ? t(key) : field;
  }

  // The private-range field is the one thing in that form this window may have
  // filled in by itself: suggestPrivateRange writes the chosen interface's own
  // subnet into it the moment the owner picks an address, without them typing
  // anything. That counted as an unsaved change, so an owner who had merely
  // looked at the address list was refused by step 3 over a value they never
  // entered — and the refusal named no field, so there was nothing to go and
  // undo.
  //
  // A suggestion still standing untouched is ours to withdraw, and withdrawing
  // it is the right half of the fix on its own: carrying it into this save
  // would declare a private range nobody asked for, and that range decides
  // where the node is willing to send data. Anything the owner typed over it
  // does not match the suggestion and is left exactly where it is.
  function withdrawUntouchedPrivateSuggestion() {
    const field = el("node-private");
    if (state.nodePrivateSuggested === "" || field.value.trim() !== state.nodePrivateSuggested) return;
    field.value = (state.nodeSettings?.saved?.treatAsPrivate ?? []).join(", ");
    state.nodePrivateSuggested = "";
    syncNodeSettingsForm();
  }

  // describeSource relates what is running to what the form is editing.
  //
  // The form shows the saved configuration — what the next start uses. The
  // node's `sources` describes the RUNNING one, so the only case that needs
  // saying is "flag": the value on screen is not what this process is using,
  // and an owner who does not know that reads the form as a description of
  // right now.
  function describeSource(source, running, savedValue) {
    if (source === "flag") {
      const differs = running !== undefined && describeStored(running) !== describeStored(savedValue);
      return differs
        ? t("nodeSettings.sourceFlagDiffers", { running: describeStored(running) })
        : t("nodeSettings.sourceFlagSame");
    }
    if (source === "remembered") return t("nodeSettings.sourceRemembered");
    if (source === "default") return t("nodeSettings.sourceDefault");
    return "";
  }

  function describeStored(value) {
    if (Array.isArray(value)) return value.length > 0 ? value.join(", ") : t("nodeSettings.valueEmpty");
    if (typeof value === "boolean") return value ? t("nodeSettings.valueOn") : t("nodeSettings.valueOff");
    return String(value === "" || value === undefined ? LOOPBACK_LISTEN : value);
  }

  // fillPeerListenOptions rebuilds the list from scratch, placeholder included.
  //
  // Rebuilt whole rather than trimmed down to a placeholder that is assumed to
  // be there: the option at index 0 decides what a select with no match falls
  // back to, and a list whose first entry is a LAN address is one that can hand
  // back an address nobody chose.
  //
  // An address the node has but this machine no longer offers is kept
  // selectable: a cable unplugged right now has not changed what the node is
  // configured to serve.
  function fillPeerListenOptions(current, addresses) {
    const select = el("node-peerlisten");
    select.replaceChildren();
    const placeholder = document.createElement("option");
    placeholder.value = "";
    placeholder.textContent = t("nodeSettings.optionLoopback", { address: LOOPBACK_LISTEN });
    placeholder.dataset.private = "1";
    select.append(placeholder);

    const value = String(current ?? "");
    const isDefault = value === "" || value === LOOPBACK_LISTEN;
    // A loopback address that is not the default is still off the network, and
    // still the node's actual setting, so it gets its own entry.
    if (!isDefault && isLoopbackListen(value)) {
      const option = document.createElement("option");
      option.value = value;
      option.textContent = `${value} · ${t("nodeSettings.optionLoopbackOtherPort")}`;
      option.dataset.private = "1";
      select.append(option);
    }

    const offered = new Set();
    for (const item of addresses ?? []) {
      const address = `${item.address}:7463`;
      offered.add(address);
      const option = document.createElement("option");
      option.value = address;
      option.textContent = `${address} · ${item.interface} · ${item.subnet}` +
        (item.private ? "" : ` · ${t("nodeSettings.optionNotPrivate")}`);
      option.dataset.private = item.private ? "1" : "";
      option.dataset.subnet = item.subnet;
      select.append(option);
    }
    if (!isLoopbackListen(value) && !offered.has(value)) {
      const option = document.createElement("option");
      option.value = value;
      option.textContent = `${value} · ${t("nodeSettings.optionGone")}`;
      option.dataset.private = "1";
      select.append(option);
    }
    select.value = isDefault ? "" : value;
  }

  // ---- The address list (ADR-005 §5) ----
  //
  // A node that knows the list sends `peerListens` in `saved` (always at least
  // one entry — [127.0.0.1:7463] when it is on this machine only). One that
  // does not sends nothing, and it takes one address: a list sent to it is a
  // field it refuses, and the scalar it does take REPLACES a new node's whole
  // list. So the mode is decided by the node's own answer, per paint, and the
  // dropdown above is what an older node keeps.
  function peerListensSupported(view = state.nodeSettings) {
    const list = view?.saved?.peerListens;
    return Array.isArray(list) && list.length > 0;
  }

  // The rows on screen, each with its checkbox, rebuilt by fillPeerListenRows.
  // Kept here rather than queried back out of the DOM: the address is the
  // row's identity, and a label's text is not something to parse it from.
  let peerListenRows = [];
  // The order the owner ticked rows in since the last paint. The private-range
  // suggestion follows the most recent non-private row still ticked, as it
  // followed the dropdown's one choice.
  let peerListenTicks = [];

  // listenPort is the one port every network entry shares (nodeconfig
  // refuses a list with two), read from the first one. Without one it is the
  // default, as the dropdown offered: a loopback entry on another port is off
  // the network, and its port says nothing about where a LAN listener goes.
  function listenPort(list) {
    const lan = (list ?? []).filter(Boolean).find((address) => !isLoopbackListen(address));
    return lan ? peerListenPort(lan) : peerListenPort(LOOPBACK_LISTEN);
  }

  // samePeerListens compares two lists as the node does: as sets, with an
  // empty list meaning the default. Rule 1 of §7.8 is "the same set is not
  // sent", and the preferred entry cannot differ between two equal sets from
  // this form, because checkedPeerListens keeps the saved order.
  function samePeerListens(asked, held) {
    const norm = (list) => {
      const values = (list ?? []).map((value) => String(value).trim()).filter(Boolean);
      return (values.length > 0 ? values : [LOOPBACK_LISTEN]).sort();
    };
    const left = norm(asked);
    const right = norm(held);
    return left.length === right.length && left.every((value, index) => value === right[index]);
  }

  // fillPeerListenRows builds one row per address the owner could mean, with
  // exactly the entries of `checked` ticked.
  //
  // Every IPv4 this machine has (private ones first, the rest under their own
  // heading), every saved entry this machine does not have now — still ticked,
  // because a cable unplugged right now has not changed what the node is
  // configured to serve — and a loopback entry that is not the default. The
  // default loopback has no row: nothing ticked IS that address.
  function fillPeerListenRows(checked, addresses) {
    const view = state.nodeSettings ?? {};
    const saved = view.saved?.peerListens ?? [];
    const ticked = new Set((checked ?? []).map(String));
    const port = listenPort([...(checked ?? []), ...saved]);
    const local = new Map((addresses ?? []).map((item) => [String(item.address).toLowerCase(), item]));
    const main = [];
    const other = [];
    const seen = new Set();
    const add = (row) => {
      if (seen.has(row.address)) return;
      seen.add(row.address);
      (row.private ? main : other).push(row);
    };
    for (const item of addresses ?? []) {
      add({ address: `${item.address}:${port}`, interface: item.interface, subnet: item.subnet,
        private: Boolean(item.private), kind: "local" });
    }
    for (const address of [...saved, ...(checked ?? [])]) {
      const value = String(address ?? "").trim();
      if (value === "" || value === LOOPBACK_LISTEN) continue;
      if (isLoopbackListen(value)) {
        add({ address: value, interface: "", subnet: "", private: true, kind: "loopback" });
        continue;
      }
      // Same host at another port is still this machine's address, not a
      // missing one: a port repair moves every entry at once.
      const item = local.get(hostOf(value));
      add(item
        ? { address: value, interface: item.interface, subnet: item.subnet, private: Boolean(item.private), kind: "local" }
        : { address: value, interface: "", subnet: "", private: isPrivateByDefinition(value), kind: "gone" });
    }

    const list = el("node-peerlistens");
    list.replaceChildren();
    peerListenRows = [];
    const build = (row) => {
      const label = element("label", "listenrow");
      const box = document.createElement("input");
      box.type = "checkbox";
      box.checked = ticked.has(row.address);
      const body = element("span", "listenbody");
      const line = element("span", "listenline");
      line.append(element("span", "listenaddr", row.address));
      if (row.interface) line.append(element("span", "listenmeta", `${row.interface} · ${row.subnet}`));
      const status = element("span", "listenstate");
      const mark = element("span", "listenmark");
      line.append(status, mark);
      body.append(line);
      // "Gone" is said once: by the node's own state when it tried the
      // address and found it missing, by this note when it has not said so.
      const reportedGone = (view.peerListeners ?? [])
        .some((entry) => entry.address === row.address && entry.reason === "address_gone");
      const note = row.kind === "gone"
        ? (reportedGone ? "" : t("nodeSettings.rowGone"))
        : row.kind === "loopback"
          ? t("nodeSettings.optionLoopbackOtherPort")
          : row.private ? "" : t("nodeSettings.optionNotPrivate");
      if (note) body.append(element("span", "why", note));
      label.append(box, body);
      const entry = { ...row, box, status, mark };
      box.onchange = () => {
        peerListenTicks = peerListenTicks.filter((address) => address !== row.address);
        if (box.checked) peerListenTicks.push(row.address);
        suggestPrivateRange();
        syncNodeSettingsForm();
      };
      peerListenRows.push(entry);
      paintPeerListenRowState(entry, view);
      return label;
    };
    for (const row of main) list.append(build(row));
    if (other.length > 0) {
      list.append(element("div", "listenhead", t("nodeSettings.peerListensOther")));
      for (const row of other) list.append(build(row));
      list.append(element("p", "why", t("nodeSettings.otherNeedsRange")));
    }
  }

  // The classes a row's state may carry: a fixed set, never the node's string.
  const LISTEN_STATE_CLASSES = { open: "listenstate ok", pending: "listenstate", failed: "listenstate warn" };

  // peerListenRowState says what the node is doing with one address, against
  // what is saved — never against the boxes on screen, which are an unsaved
  // intention rather than a fact about the node.
  //
  // Observed where it can be: "Open" only from a listener the node reports
  // bound, and a failure in the node's own terms. A node that reports no
  // listeners gets no claim about the addresses it is running.
  function peerListenRowState(address, view) {
    const saved = view.saved?.peerListens ?? [];
    const configured = view.settings?.peerListens ?? [];
    const listeners = Array.isArray(view.peerListeners) ? view.peerListeners : null;
    const listener = listeners?.find((entry) => entry.address === address);
    if (saved.includes(address)) {
      if (!configured.includes(address)) return { text: t("nodeSettings.rowOpensAfterRestart"), kind: "pending" };
      if (!listener) return null;
      if (listener.state === "bound") return { text: t("nodeSettings.rowOpen"), kind: "open" };
      if (listener.state === "pending") return { text: t("nodeSettings.rowPending"), kind: "pending" };
      if (listener.reason === "address_gone") return { text: t("nodeSettings.rowNotOpenGone"), kind: "failed" };
      if (listener.reason === "port_in_use") return { text: t("nodeSettings.rowNotOpenPort"), kind: "failed" };
      return { text: t("nodeSettings.rowNotOpenOther",
        { message: String(listener.message || listener.detail || listener.reason || "").trim() }), kind: "failed" };
    }
    if (listener?.state === "bound") return { text: t("nodeSettings.rowClosesAfterRestart"), kind: "pending" };
    return null;
  }

  function paintPeerListenRowState(row, view) {
    const said = peerListenRowState(row.address, view);
    row.status.textContent = said ? said.text : "";
    row.status.className = said ? LISTEN_STATE_CLASSES[said.kind] : "listenstate";
  }

  // checkedPeerListens is the list the boxes on screen describe: the saved
  // entries still ticked, in their saved order, then the newly ticked ones.
  // The saved order is kept because its first entry is the preferred one —
  // the address this machine broadcasts from — and unticking and re-ticking a
  // box is not a decision to change that.
  function checkedPeerListens() {
    const saved = state.nodeSettings?.saved?.peerListens ?? [];
    const on = peerListenRows.filter((row) => row.box.checked).map((row) => row.address);
    const ticked = new Set(on);
    return [...saved.filter((address) => ticked.has(address)), ...on.filter((address) => !saved.includes(address))];
  }

  // peerListensToSend is never empty: nothing ticked is this machine only,
  // which the node spells as its default loopback address.
  function peerListensToSend() {
    const list = checkedPeerListens();
    return list.length > 0 ? list : [LOOPBACK_LISTEN];
  }

  // peerListensNotOpen names the saved network addresses the node is not
  // serving, from its own per-address answer. Nothing when it gave none.
  function peerListensNotOpen(view) {
    if (!Array.isArray(view?.peerListeners)) return [];
    const bound = new Set(view.peerListeners.filter((entry) => entry.state === "bound").map((entry) => entry.address));
    return (view.saved?.peerListens ?? []).filter((address) => !isLoopbackListen(address) && !bound.has(address));
  }

  // syncPeerListensForm is syncNodeSettingsForm for the list: the same four
  // warnings, judged over every ticked address, plus the two things only a
  // list has — which row is broadcast from, and loopback beside a network
  // address, which the node refuses.
  function syncPeerListensForm(warning, allowLan) {
    const list = checkedPeerListens();
    const lan = list.filter((address) => !isLoopbackListen(address));
    const stored = state.nodeSettings?.saved?.peerListens ?? [];
    const storedLan = stored.filter((address) => !isLoopbackListen(address));
    const naming = !samePeerListens(peerListensToSend(), stored);

    for (const row of peerListenRows) row.mark.textContent = row.address === lan[0] ? t("nodeSettings.rowBroadcast") : "";
    el("node-peerlistens-none").classList.toggle("hidden", lan.length > 0);

    if (!allowLan && lan.length > 0 && naming) {
      warning.append(element("div", "stale", t("nodeSettings.warnLanOff", { address: lan.join(", ") })));
    } else if (!allowLan && storedLan.length > 0) {
      warning.append(element("div", "stale", t("nodeSettings.warnWithdraw", { stored: storedLan.join(", ") })));
    }
    const loopback = list.filter((address) => isLoopbackListen(address));
    if (lan.length > 0 && loopback.length > 0) {
      warning.append(element("div", "stale", t("nodeSettings.warnMixLoopback", { address: loopback.join(", ") })));
    }
    const declared = el("node-private").value.split(/[\s,]+/).map((value) => value.trim()).filter(Boolean);
    for (const address of lan) {
      if (!canJudgePrivacy(address, declared) || isPrivateByDefinition(address) ||
          declared.some((range) => coversAddress(range, address))) continue;
      const subnet = peerListenRows.find((row) => row.address === address)?.subnet ?? "";
      warning.append(element("div", "stale",
        t("nodeSettings.warnNotPrivate", { address }) +
        (subnet ? t("nodeSettings.warnNotPrivateSubnet", { subnet }) : "")));
    }
  }

  // relabelNodeSettings puts this form back into the language in use without
  // reading the node again and without touching a single thing the owner
  // typed.
  //
  // Not applyNodeSettings: that repaints every field from `saved`, so calling
  // it here would throw away a half-edited form because somebody changed the
  // language. What is rebuilt is only what is translated — the address list's
  // labels (the option VALUES are addresses and survive, and the current
  // selection is passed back in as `current`), the source tags beside each
  // field, the restart hint, and the warnings syncNodeSettingsForm derives.
  function relabelNodeSettings() {
    const view = state.nodeSettings;
    if (!view) return;
    const select = el("node-peerlisten");
    // The selection as it stands on screen, and nothing else. Falling back to
    // `saved.peerListen` here undid the owner's own choice: the loopback option
    // carries the value "" (fillPeerListenOptions writes `select.value = ""` for
    // it), so an owner who had moved a saved LAN address BACK to "this machine
    // only" and then switched language had the LAN address re-selected under
    // them — readNodeSettingsPatch would then find nothing changed and Save
    // would leave the node listening on the LAN. relabelNodeSettings only ever
    // runs once applyNodeSettings has populated the form, so "" is always the
    // loopback option and never "the form has not been filled in yet".
    const chosen = select.value;
    fillPeerListenOptions(chosen, state.nodeAddresses?.list);
    // The boxes as they stand on screen, for the same reason.
    if (peerListensSupported(view)) fillPeerListenRows(checkedPeerListens(), state.nodeAddresses?.list);
    const running = view.settings ?? {};
    const sources = view.sources ?? {};
    const saved = view.saved ?? {};
    for (const [id, key] of [
      ["node-peerlisten-source", "peerListen"],
      ["node-allowlan-source", "allowLan"],
      ["node-discover-source", "discover"],
      ["node-private-source", "treatAsPrivate"],
      ["node-autowake-source", "autoWake"],
    ]) {
      el(id).textContent = describeSource(sources[key], running[key], saved[key]);
    }
    // The list's tag: the node keeps one provenance for both spellings.
    el("node-peerlistens-source").textContent = peerListensSupported(view)
      ? describeSource(sources.peerListen, running.peerListens, saved.peerListens)
      : "";
    el("node-settings-hint").textContent = view.restartRequired
      ? t("nodeSettings.hintRestart")
      : t("nodeSettings.hint");
    syncNodeSettingsForm();
  }

  // syncNodeSettingsForm explains the combination on screen. It writes no field
  // the owner controls.
  //
  // An earlier version ticked allowLan whenever a LAN address was selected, and
  // it was wired to allowLan's own onchange — so the box could not be unticked,
  // which made the node's headline behaviour (turn it off, the listener is
  // withdrawn) unreachable from this window. A later one auto-filled the
  // private range from here, so clearing that field was undone on the same
  // keystroke and a range suggested for one address outlived it. Predicting the
  // node's answer is this function's job; making the owner's choice for them is
  // not — the one suggestion this form offers lives in suggestPrivateRange,
  // which runs only when the owner changes the address.
  function syncNodeSettingsForm() {
    const address = el("node-peerlisten").value || LOOPBACK_LISTEN;
    const lanAddress = !isLoopbackListen(address);
    const allowLan = el("node-allow-lan").checked;

    el("node-lan-note").classList.toggle("hidden", !allowLan);
    if (peerListensSupported()) {
      const warning = el("node-settings-combination");
      warning.replaceChildren();
      syncPeerListensForm(warning, allowLan);
      syncPrivateNote();
      return;
    }

    // What the node will do with this combination, before the owner finds out
    // by being refused or by losing an address.
    //
    // The two outcomes are not the same and turn on one thing —
    // nodeconfig.WithdrawPeerListen takes `peerListenNamed`: a write that NAMES
    // a non-loopback address with allowLan off is refused, and one that leaves
    // the address alone withdraws it to loopback instead. Saying "will be
    // refused" for the second would send the owner looking for a mistake that
    // is not there, and saying "will be withdrawn" for the first would promise
    // a save that does not happen.
    const stored = state.nodeSettings?.saved?.peerListen ?? "";
    const naming = address !== (stored || LOOPBACK_LISTEN);
    const warning = el("node-settings-combination");
    warning.replaceChildren();
    if (!allowLan && lanAddress && naming) {
      warning.append(element("div", "stale",
        t("nodeSettings.warnLanOff", { address })));
    } else if (!allowLan && !isLoopbackListen(stored)) {
      warning.append(element("div", "stale",
        t("nodeSettings.warnWithdraw", { stored })));
    }

    // A non-private address needs its range declared or the node refuses it.
    // Said, not filled in: the ranges on screen are the owner's, and a form
    // that edits them to make its own warning go away has taken the decision.
    const select = el("node-peerlisten");
    const option = select.options[select.selectedIndex];
    const declared = el("node-private").value.split(/[\s,]+/).map((value) => value.trim()).filter(Boolean);
    // Judged from the address, not from the interface entry: a stored address
    // whose cable is unplugged has no entry, and one marked private by an
    // entry is still refused if the range it needs was never declared.
    if (lanAddress && canJudgePrivacy(address, declared) && !isPrivateByDefinition(address) &&
        !declared.some((range) => coversAddress(range, address))) {
      const subnet = option?.dataset?.subnet ?? "";
      warning.append(element("div", "stale",
        t("nodeSettings.warnNotPrivate", { address }) +
        (subnet ? t("nodeSettings.warnNotPrivateSubnet", { subnet }) : "")));
    }

    syncPrivateNote();
  }

  // The note explains the field's current contents, so it is shown whenever
  // those contents are the suggestion — never left behind after the address
  // that justified it is gone.
  function syncPrivateNote() {
    const note = el("node-private-note");
    const showing = state.nodePrivateSuggested !== "" &&
      el("node-private").value.trim() === state.nodePrivateSuggested;
    // A declared range is one half of a connection across it: the machine on
    // the other end of that cable has to declare the same range, or it will not
    // send back. Said only with the list, whose rows put the non-private
    // addresses in front of the owner.
    note.textContent = showing
      ? t("nodeSettings.privateSuggested", { subnet: state.nodePrivateSuggested }) +
        (peerListensSupported() ? ` ${t("nodeSettings.otherNeedsRange")}` : "")
      : "";
    note.classList.toggle("hidden", !showing);
  }

  // suggestPrivateRange runs when the owner picks a different address, and only
  // then.
  //
  // A non-private address needs its range declared or the node refuses it, so
  // the interface's own subnet is offered — never a wider guess, because the
  // range decides who the node will deliver to. What the owner typed is left
  // alone, and an empty field stays empty: clearing it is how a declared range
  // is withdrawn, and a form that refills it has taken that away.
  function suggestPrivateRange() {
    let suggestion = "";
    if (peerListensSupported()) {
      // The most recently ticked non-private row that is still ticked.
      const source = [...peerListenTicks].reverse()
        .map((address) => peerListenRows.find((row) => row.address === address))
        .find((row) => row && row.box.checked && !row.private && row.subnet);
      suggestion = source ? source.subnet : "";
    } else {
      const select = el("node-peerlisten");
      const option = select.options[select.selectedIndex];
      suggestion = option && option.value && option.dataset.private === "" ? option.dataset.subnet : "";
    }
    const field = el("node-private");
    const previous = state.nodePrivateSuggested;
    const holdsPrevious = previous !== "" && field.value.trim() === previous;

    if (suggestion === previous) return;
    // The old suggestion goes with the address that justified it; anything the
    // owner typed stays.
    if (holdsPrevious) field.value = suggestion;
    else if (suggestion !== "" && field.value.trim() === "" && previous === "") field.value = suggestion;
    state.nodePrivateSuggested = field.value.trim() === suggestion ? suggestion : "";
  }

  // coversAddress answers whether a declared CIDR contains an address, for the
  // IPv4 case this can decide. Anything else is answered "no" so the warning
  // stands: a wrong "yes" would hide a refusal the node is about to make.
  //
  // A range the node itself rejects never counts as covering, or the form would
  // wave through a write the node 400s — 0.0.0.0/0 and anything containing the
  // unspecified, broadcast or multicast addresses (internal/nodeconfig/
  // privateranges.go).
  function coversAddress(range, address) {
    const parts = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(String(range).trim());
    const host = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(hostOf(address));
    if (!parts || !host) return false;
    const bits = Number(parts[5]);
    if (bits <= 0 || bits > 32) return false;
    const pack = (a, b, c, d) => (((a << 24) >>> 0) + (b << 16) + (c << 8) + d) >>> 0;
    const network = pack(Number(parts[1]), Number(parts[2]), Number(parts[3]), Number(parts[4]));
    const mask = (0xffffffff << (32 - bits)) >>> 0;
    const covers = (value) => ((network & mask) >>> 0) === ((value & mask) >>> 0);
    // The three the node names by hand.
    if (covers(0) || covers(pack(255, 255, 255, 255)) || covers(pack(224, 0, 0, 1))) return false;
    return covers(pack(Number(host[1]), Number(host[2]), Number(host[3]), Number(host[4])));
  }

  // canJudgePrivacy says whether this window can predict the node's private-range
  // decision at all. It cannot for IPv6 — neither the address nor the ranges —
  // and a prediction it cannot make is one it must not show.
  function canJudgePrivacy(address, declared) {
    if (!/^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(hostOf(address))) return false;
    return declared.every((range) => /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\/\d{1,2}$/.test(String(range).trim()));
  }

  // readNodeSettingsPatch sends only what the owner changed.
  //
  // A partial write is what the endpoint takes, and sending everything would
  // make an unchanged LAN peerListen part of a write that turns allowLan off —
  // which the node refuses, naming an address the owner never touched.
  //
  // The baseline is the SAVED configuration, which is what the form is painted
  // from and what the node merges a write onto. Diffing against the running
  // values instead is what let an unrelated save withdraw a remembered address,
  // and made a flag-pinned switch impossible to write.
  // Each field of the patch as the flag a service unit pins it with — the names
  // ServiceStatus reports in pinnedSettings (desktop/service.go).
  const NODE_SETTING_FLAGS = {
    peerListen: "peer-listen",
    // The list is the same setting, and a unit pins it with the same flag.
    peerListens: "peer-listen",
    allowLan: "allow-lan",
    discover: "discover",
    treatAsPrivate: "treat-as-private",
    autoWake: "auto-wake",
  };

  function readNodeSettingsPatch() {
    // The saved configuration, because that is what the node merges a write
    // onto — see applyNodeSettings.
    const before = state.nodeSettings?.saved ?? {};
    const patch = {};
    if (peerListensSupported()) {
      // The list, never the scalar: the scalar replaces the node's whole list.
      // Compared as a set against the saved one (§7.8 rule 1).
      const list = peerListensToSend();
      if (!samePeerListens(list, before.peerListens)) patch.peerListens = list;
    } else {
      const peerListen = el("node-peerlisten").value || LOOPBACK_LISTEN;
      if (peerListen !== (before.peerListen || LOOPBACK_LISTEN)) patch.peerListen = peerListen;
    }
    if (el("node-allow-lan").checked !== Boolean(before.allowLan)) patch.allowLan = el("node-allow-lan").checked;
    if (el("node-discover").checked !== Boolean(before.discover)) patch.discover = el("node-discover").checked;
    if (el("node-autowake").checked !== Boolean(before.autoWake)) patch.autoWake = el("node-autowake").checked;
    const ranges = el("node-private").value.split(/[\s,]+/).map((value) => value.trim()).filter(Boolean);
    const previous = before.treatAsPrivate ?? [];
    if (ranges.length !== previous.length || ranges.some((value, index) => value !== previous[index])) {
      // Always an array, never omitted: an empty one is how a declared range is
      // withdrawn, and leaving the key out would mean "leave it alone".
      patch.treatAsPrivate = ranges;
    }
    return patch;
  }

  async function saveNodeSettings() {
    if (!state.nodeSettings || state.nodeSettings.error) {
      banner(t("nodeSettings.noBaseline"));
      return;
    }
    const patch = readNodeSettingsPatch();
    if (Object.keys(patch).length === 0) {
      banner(t("nodeSettings.noChange"));
      return;
    }
    // A unit that carries start-up flags is given them on every start and the
    // node writes back what it was given, so a value saved here is replaced a
    // second or two later and nothing in the node's answer says why — the
    // sources column reads "flag" for a value that is genuinely stored.
    //
    // This used to be a warning and a button in the background service section,
    // two panels away from the form whose writes it undoes. Asked here instead,
    // at the moment it decides the outcome, and before the write rather than
    // after it: the re-registered unit restarts the node, so the values saved
    // below are read by a process that is no longer given anything.
    //
    // Outside withBusy because re-registering has its own.
    //
    // Asked only when this save touches a setting the unit pins: a unit that
    // pins the peer listener does nothing to a change of discovery, and asking
    // about it there made every save a question about the service (#193
    // review). A refusal — or a unit whose database path this window cannot
    // read, so re-registering it could move the node onto another identity —
    // leaves out the pinned fields, which the unit would quietly undo, and
    // still saves the rest; a banner names what was left out.
    const pinned = new Set(state.service?.installed ? (state.service.pinnedSettings ?? []) : []);
    const touched = Object.keys(patch).filter((key) => pinned.has(NODE_SETTING_FLAGS[key]));
    let skippedNote = "";
    if (touched.length > 0) {
      const reregistered = Boolean(state.service.dbPathKnown)
        && (await reinstallWithoutPinnedSettings(state.service, touched.map((key) => NODE_SETTING_FLAGS[key])));
      if (!reregistered) {
        skippedNote = [
          state.service.dbPathKnown ? "" : t("service.unpinNeedsDbPath"),
          t("nodeSettings.pinnedNotSaved", { pinned: touched.map(nodeSettingLabel).join(t("candidate.flagJoin")) }),
        ].filter(Boolean).join(" ");
        for (const key of touched) delete patch[key];
      }
    }
    if (Object.keys(patch).length === 0) {
      banner(skippedNote);
      return;
    }
    await saveNodeSettingsPatch(patch);
    // After the save's own report, which is a single banner that each outcome
    // replaces: said last, so the fields that were left out are not overwritten
    // by the sentence about the ones that were saved.
    if (skippedNote) {
      const said = el("banner").classList.contains("hidden") ? "" : el("banner").textContent;
      banner([said, skippedNote].filter(Boolean).join(" "));
    }
  }

  async function saveNodeSettingsPatch(patch) {
    await withBusy(t("nodeSettings.busySave"), async () => {
      const sequence = ++nodeSettingsRequest;
      let view;
      try {
        view = await api.SaveNodeSettings(patch);
      } catch (error) {
        view = { error: String(error) };
      }

      if (view.error) {
        // The node's refusal names the address that was sent and what to send
        // with it, so it is shown as it came. The form is left as the owner
        // typed it: nothing was saved, and repainting it would throw away what
        // they were in the middle of fixing. The baseline is left alone too —
        // nothing changed at the node.
        if (sequence > nodeSettingsApplied) nodeSettingsApplied = sequence;
        el("node-settings-notice").replaceChildren(
          element("div", "stale", t("nodeSettings.refused")),
          element("div", "muted", view.error),
        );
        return;
      }

      // The write landed, so the restart has to happen whatever else has been
      // read since: skipping it here would leave the node holding settings it
      // has not read, with nothing on screen saying so. Only the painting below
      // is subject to the guard.
      // The status read and the restart are both allowed to fail without
      // turning this into "the save failed": the write already landed, and an
      // owner told otherwise will try again and re-send it.
      const status = await serviceStatusOrUnknown();
      if (status.unknown) {
        // Not the same as "this is not a service". `ah` missing, or a status
        // command that failed, leaves this window without the fact — and a
        // node that IS a service then never gets restarted while the owner is
        // told to restart something they were told is not a service.
        await paintAfterSave(sequence, view);
        banner(
          t("nodeSettings.savedNoServiceStatus", { reason: status.reason }),
        );
        return;
      }
      // A node no service manager holds is restarted by this app itself, which
      // is the only way a setting saved here takes effect on Windows: the
      // installer starts the node from the Startup folder, and what used to be
      // here told the owner to go and restart it — a sentence whose only
      // meaning on that platform was Task Manager.
      try {
        const restarted = await api.RestartNode();
        showNodeSettingsOutput(restarted);
      } catch (error) {
        await paintAfterSave(sequence, view);
        banner(
          t("nodeSettings.savedRestartFailed", { error }),
        );
        return;
      }
      // Outside the try: the restart already succeeded, so a status read that
      // fails here is not a failed restart. Reporting it as one would tell the
      // owner the node is still on the old settings when it is not.
      const live = await serviceStatusOrUnknown();

      // Read the node again now that it has restarted, rather than painting the
      // answer the write gave: that answer describes the process that has since
      // been replaced, so its `settings`, `sources` and `restartRequired` are
      // all about a dead node — the form would still say "restart to apply"
      // after the restart, and name a command line that is gone.
      const after = await readNodeSettingsAfterRestart();
      // The re-read is the fresher truth about the node, but only the write's
      // own answer carries `message` — what this write did, including the
      // address it pulled back. Merged, so neither is lost.
      await paintAfterSave(sequence, after.view.error
        ? view
        : { ...after.view, message: after.view.message || view.message });

      if (after.view.error) {
        // The tags beside each field name what is RUNNING, and after a restart
        // this window could not read, that is a process that no longer exists.
        // Cleared rather than left to describe the dead one.
        for (const id of ["node-peerlisten-source", "node-peerlistens-source", "node-allowlan-source",
          "node-discover-source", "node-private-source", "node-autowake-source"]) {
          el(id).textContent = "";
        }
        el("node-settings-hint").textContent = t("nodeSettings.hint");
        // Without the re-read there is no way to tell whether what was asked
        // for is what the node now holds, and the most common reason it would
        // not be — a flag in the service unit — looks exactly like success.
        // Say the check did not happen rather than implying it passed.
        banner(
          t("nodeSettings.savedRereadFailed", { error: after.view.error }),
        );
        return;
      }

      // Did what the owner asked for survive the restart?
      //
      // A flag in the service unit is given on every start and the node stores
      // what it was given, so a setting the unit carries is silently replaced a
      // second or two after being saved. Nothing in the node's answer says that
      // happened — the values simply are not the ones that were sent — so the
      // only honest check is to compare them.
      const lost = after.view.error ? [] : didNotStick(patch, after.view.saved);
      if (lost.length > 0) {
        banner(
          t("nodeSettings.savedDidNotStick", { lost: lost.join(t("candidate.flagJoin")) }),
        );
        return;
      }

      // Saved and read back is not the same as open. An address the node holds
      // and could not bind — the cable is out, something else has the port —
      // is said in a sentence of its own, from the node's per-address answer,
      // after whatever the restart itself came to: every outcome below is
      // true, and none of them says it.
      const notOpen = peerListensNotOpen(after.view);
      const notOpenNote = notOpen.length > 0
        ? t("nodeSettings.savedNotOpen", { addresses: notOpen.join(", ") })
        : "";
      const say = (text, ok = false) => banner([text, notOpenNote].filter(Boolean).join(" "), ok && notOpenNote === "");

      // What `ah service status` says afterwards, not what was sent.
      //
      // This is the moment a node is most likely not to come back: the values
      // that were just saved are the ones it reads at start-up, and a service
      // set to restart on failure will crash-loop on a combination it refuses.
      // Announcing "restarted" because the restart command returned would be a
      // claim about the one thing this window can actually check and did not.
      if (live.unknown) {
        say(
          t("nodeSettings.savedStatusUnknown", { reason: live.reason }),
        );
        return;
      }
      const back = state.service ?? {};
      // Without a service manager there is no `running` to ask about — the
      // process this app started is registered with nothing — so whether the
      // node answers is the whole of the question. Asking for `running` too
      // would report every successful restart on Windows as a failure.
      if (!live.installed) {
        if (back.nodeAnswering) {
          say(t("nodeSettings.savedNodeAnswering"), true);
          return;
        }
        say(
          t("nodeSettings.savedNodeSilent"),
        );
        return;
      }
      if (back.running && back.nodeAnswering) {
        say(t("nodeSettings.savedServiceAnswering"), true);
        return;
      }
      // Not marked successful, so it stays on screen: the settings just saved
      // are the first thing to suspect, and they are still on the form above.
      say(
        back.running
          ? t("nodeSettings.savedServiceUpNodeSilent",
            { log: back.logHint || t("nodeSettings.noLogPath") })
          : t("nodeSettings.savedServiceDown",
            { log: back.logHint || t("nodeSettings.noLogPath") }),
      );
    });
  }

  // readNodeSettingsAfterRestart re-reads the node once it has been restarted.
  // A failure is not fatal: the write landed, and the form falls back to the
  // answer the write gave rather than showing nothing.
  async function readNodeSettingsAfterRestart() {
    try {
      return { view: await api.NodeSettings() };
    } catch (error) {
      return { view: { error: String(error) } };
    }
  }

  // paintAfterSave repaints from `view`, with the address list, and only if
  // nothing newer has landed.
  //
  // The guard is re-checked AFTER the address lookup: checking before it and
  // assigning the sequence number then would let a read that started later,
  // and answered from before the restart, paint over this one.
  async function paintAfterSave(sequence, view) {
    const addresses = await fetchLocalAddresses();
    if (sequence <= nodeSettingsApplied) return;
    nodeSettingsApplied = sequence;
    applyNodeSettings(view, addresses);
  }

  // serviceStatusOrUnknown answers what this window knows about the service,
  // and says so when it knows nothing. `ah` missing or a failed status command
  // is not evidence that the node is not a service.
  async function serviceStatusOrUnknown() {
    try {
      await loadService();
    } catch (error) {
      return { unknown: true, reason: String(error) };
    }
    const status = state.service;
    if (!status) return { unknown: true, reason: t("nodeSettings.noStatusRead") };
    if (status.toolError) return { unknown: true, reason: status.toolError };
    if (!status.supported) return { unknown: false, installed: false };
    return { unknown: false, installed: Boolean(status.installed) };
  }

  // didNotStick names the fields the owner asked for that the node is not
  // holding once it has restarted.
  //
  // Observed, not inferred. The obvious cause is a flag baked into the service
  // unit — it is given on every start and the node writes what it was given
  // back into its own store (cmd/agenthub-node/main.go), so after the restart
  // the stored value IS the unit's and nothing in the answer distinguishes it
  // from a value the owner saved. An earlier version tried to spot it by
  // comparing the running value against the stored one and could never fire,
  // because the restart had already made them agree. Comparing what was asked
  // for against what is there needs no theory about why.
  function didNotStick(patch, savedAfter) {
    return Object.keys(patch)
      .filter((key) => !sameSettingValue(patch[key], savedAfter?.[key], key))
      .map(nodeSettingLabel);
  }

  // nodeSettingLabel is the form's own name for a field of the patch.
  function nodeSettingLabel(key) {
    const labels = {
      peerListen: t("nodeSettings.peerListenLabel"),
      peerListens: t("nodeSettings.peerListensLabel"),
      allowLan: t("nodeSettings.allowLan"),
      discover: t("nodeSettings.discoverShort"),
      treatAsPrivate: t("nodeSettings.privateLabel"),
      autoWake: t("nodeSettings.autoWakeShort"),
    };
    return labels[key] ?? key;
  }

  // sameSettingValue compares one field the way the node does: a list of ranges
  // is a set, so the same ranges in another order are the same setting, and an
  // empty peerListen is the default rather than a different address.
  function sameSettingValue(asked, held, key) {
    if (key === "treatAsPrivate") {
      const left = [...(asked ?? [])].map((value) => String(value).trim()).sort();
      const right = [...(held ?? [])].map((value) => String(value).trim()).sort();
      return left.length === right.length && left.every((value, index) => value === right[index]);
    }
    if (key === "peerListen") {
      return (asked || LOOPBACK_LISTEN) === (held || LOOPBACK_LISTEN);
    }
    if (key === "peerListens") return samePeerListens(asked, held);
    return Boolean(asked) === Boolean(held);
  }

  function showNodeSettingsOutput(result) {
    const output = el("node-settings-output");
    output.textContent = `$ ${result.command}\n${result.output}`;
    output.classList.remove("hidden");
  }

  // setUILanguage repaints in place rather than reloading. A reload would drop
  // the pairing drawer's state, the filter selection and anything half-typed,
  // which is a high price for a string swap.
  function setUILanguage(next) {
    state.ui.lang = setLanguage(next);
    savePrefs();
    paintStatic();
    render();
    repaintFromState();
  }

  // repaintFromState is the other half of a language switch, and the half that
  // was missing.
  //
  // paintStatic writes t(key) onto every [data-t] element in index.html —
  // including the ones JS later overwrites with something derived from state.
  // For those, the key in the markup is a placeholder ("reading the service
  // status…"), so a switch that stopped at paintStatic() + render() left the
  // Settings panel — the panel the language control itself sits in — claiming
  // to be loading, and left a button reading "Install as a background service"
  // on a machine where the service is installed and running.
  //
  // render() re-derives what it owns (the rows, the counts, the selection bar,
  // the node list and the pairing drawer while their view is on screen). This
  // covers everything else that is written from state: the title bar's node
  // line, the service panel and its pill, the settings form, and the two
  // dialogs that can be open across a switch. The rule for anything added
  // later: if JS writes a [data-t] element, it is re-derived here.
  function repaintFromState() {
    renderNodeLine();
    // The table's kept rows. render() has already redrawn the ones on screen,
    // but a row held out of `visible()` by a filter is still in the map and
    // still carries the previous language.
    relabelSessionRows();
    if (state.service) renderService();
    if (state.nodeSettings) relabelNodeSettings();
    if (state.inboxSessionAsked) {
      el("inbox-title").textContent = state.inboxSessionAsked;
      showInboxTab(state.inboxTab);
      // The list itself, from the view renderInbox was last handed. Every line
      // in it — the meta line's counts, "full", "empty", a failed read — is a
      // translated sentence that only this call writes.
      if (state.inboxView) renderInbox(state.inboxView);
      renderOutbound();
    }
    if (!el("audience-modal").classList.contains("hidden")) {
      renderAudienceCount();
      // Both are sentences this dialog derives rather than reads off a key, so
      // paintStatic cannot reach them.
      renderAutoWakeNote();
      syncAudiencePreset();
    }
    if (state.mcpSession) {
      el("mcp-title").textContent = `${t("mcp.title")} · ${state.mcpSession}`;
      renderMCPStatus(state.mcpStatus);
    }
  }

  /* ---------------- redesign wiring ---------------- */

  el("select-all-visible").onchange = setSelectionForVisible;
  el("btn-deselect").onclick = () => {
    state.selected.clear();
    render();
  };
  el("btn-clear-filters").onclick = () => {
    state.filters = F.emptyFilters();
    state.search = "";
    el("search").value = "";
    savePrefs();
    render();
  };

  // Closing the checklist is remembered, and it is the only thing about the
  // card that is. A WebView with storage disabled shows it again next launch,
  // which is annoying rather than wrong.
  el("onboarding-dismiss").onclick = () => {
    state.ui.onboardingDismissed = true;
    savePrefs();
    render();
  };
  // And it is reversible, because the card is the only screen that explains
  // what this app needs in order to do anything at all.
  el("settings-show-onboarding").onclick = () => {
    state.ui.onboardingDismissed = false;
    savePrefs();
    state.view = "local";
    render();
  };

  el("pairing-close").onclick = () => dismissPairingDrawer().catch(() => {});
  el("pairing-modal").onclick = (event) => {
    if (event.target === el("pairing-modal")) dismissPairingDrawer().catch(() => {});
  };
  el("btn-pair-manual").onclick = () => {
    closePairingDrawer();
    openPairModal();
  };

  for (const name of INBOX_TABS) {
    el(`inbox-tab-${name}`).onclick = () => showInboxTab(name);
  }
  el("outbound-more").onclick = () => loadOutbound().catch(() => {});

  for (const link of document.querySelectorAll("#settings-nav a")) {
    link.onclick = () => {
      state.settingsSection = link.dataset.target;
      render();
      el(link.dataset.target)?.scrollIntoView?.({ block: "start", behavior: "smooth" });
    };
  }
  el("node-settings-reload").onclick = () => (state.nodeSettingsTried = true, loadNodeSettings())
    .catch((error) => banner(t("busy.failed", { action: t("nodeSettings.busyRead"), error })));
  el("node-settings-save").onclick = () => saveNodeSettings()
    .catch((error) => banner(t("busy.failed", { action: t("nodeSettings.busySave"), error })));
  // Changing the address is the one moment this form offers a value; every
  // other handler only re-explains what is already on screen.
  el("node-peerlisten").onchange = () => {
    suggestPrivateRange();
    syncNodeSettingsForm();
  };
  el("node-allow-lan").onchange = syncNodeSettingsForm;
  el("node-private").oninput = syncNodeSettingsForm;

  el("service-pill").onclick = () => {
    state.view = "settings";
    state.settingsSection = "settings-service";
    render();
  };
  el("copy-identity-key").onclick = () => copyLocalPublicKey("identity-copy-status");
  el("toggle-backdrop").onchange = (event) => {
    state.ui.backdrop = Boolean(event.target.checked);
    savePrefs();
    applyBackdrop();
    render();
  };
  el("toggle-motion").onchange = (event) => {
    state.ui.motion = Boolean(event.target.checked);
    savePrefs();
    applyBackdrop();
  };
  // Each language names itself in itself, so these two labels are the one pair
  // of strings in the window that is never translated: whoever needs the other
  // one has to be able to read the entry that leads there.
  for (const choice of LANGUAGES) {
    const option = document.createElement("option");
    option.value = choice.value;
    option.textContent = choice.label;
    el("settings-lang").append(option);
  }
  el("settings-lang").onchange = (event) => setUILanguage(event.target.value);

  loadPrefs();
  // The markup's own words, before the first render: index.html carries keys,
  // not sentences.
  paintStatic();
  el("search").value = state.search;
  applyBackdrop();
  if (backdropUrl) el("backdrop-photo").src = backdropUrl;
  // macOS draws the window buttons over the page's top-left corner, so the
  // title bar has to leave room for them. Asked of the host rather than guessed
  // from the node's platform: they are different facts, and the node's is
  // missing exactly when it cannot be reached.
  api.HostPlatform?.()
    .then((platform) => {
      if (platform === "darwin") document.body?.classList?.add("mac");
    })
    .catch(() => {});
  // Which build this is. Asked once — it cannot change while the window is
  // open — and painted onto the node line directly as well as stored, because
  // this answer can arrive after the first load() has already written it.
  api.Version?.()
    .then((info) => {
      const release = info?.release || "";
      if (!release) return;
      state.appVersion = release === "unreleased" ? "unreleased" : `v${release}`;
      const line = el("node-line");
      const painted = line?.textContent || "";
      if (painted !== "" && painted !== t("app.connecting")) {
        if (!painted.endsWith(state.appVersion)) line.textContent = `${painted} · ${state.appVersion}`;
      }
    })
    .catch(() => {});
  // The rain is pure CSS, but the compositor still pays for it while the
  // window is hidden; pause it there and let it resume on return.
  if (typeof document.addEventListener === "function") {
    document.addEventListener("visibilitychange", () => {
      document.body?.classList?.toggle("bg-paused", Boolean(document.hidden));
    });
  }

  // What the tests reach for. Nothing here is for main.js: the app is driven
  // through the DOM, and these are the same functions the handlers call.
  const internals = {
    state, load, loadPairing, render, renderRows, renderInbox, renderPairing, askConfirm, confirmKey,
    openAudienceModal, renderAudienceCount, readAudienceForm, presetForFlags, applyAudiencePreset,
    syncAudiencePreset, openInbox, openMCPConfig, closeMCPConfig,
    candidateRow, candidateNoticeText, prefillPairFrom, nodeDetail, nodeSessions, presenceLabel, heardFrom,
    loadPairRequests, renderPairRequests, pairRequestRow, sendPairRequest, decidePairRequest,
    pairErrorMessage, renderPairHere, copyPairAddress, pairingDrawerOpen, PAIR_TEXT,
    pairAddressReachable, pairHereState, goToNodeSettings, renderPairingSubtitle, pairDecisionMessage,
    pairingRemaining, tickCountdown, visible, managementLabel, showInboxTab, loadOutbound, loadWakes, resumeCommand,
    copyResumeCommand, openPairingDrawer, closePairingDrawer, dismissPairingDrawer, pairHereRepairs,
    didNotStick, sameSettingValue, paintAfterSave,
    serviceStatusOrUnknown, loadService, renderService, restartNode, waitForNode,
    openServiceForm, installService, renderServiceRepair, reinstallWithoutPinnedSettings,
    renderPeerListenProblem, peerListenRepairs, applyPeerListenRepair, applyPeerListenRepairFromCard,
    loadNodeSettings, saveNodeSettings, applyNodeSettings, readNodeSettingsPatch,
    renderNodeLine, relabelNodeSettings, repaintFromState, renderMCPStatus,
    onboardingSteps, onboardingTriggered, renderOnboarding, spendOnboardingFarewell, goToService, goToPairing, discoverSessions,
    backdropPlan, describeBackdropState, buildRain, applyBackdrop, loadPrefs, savePrefs,
    t, plural, setUILanguage, paintStatic, pickLanguage, setLanguage, language,
    isLoopbackListen, isPrivateByDefinition, coversAddress, canJudgePrivacy, syncNodeSettingsForm, suggestPrivateRange, fetchLocalAddresses,
    peerListensSupported, checkedPeerListens, samePeerListens, peerListenRowState, pairOpenAddresses, peerListensNotOpen,
    peerListenRows: () => peerListenRows,
  };
  if (!start) return internals;
  // The panel is polled only while it is on screen.
  setInterval(() => {
    if (state.view !== "network") return;
    loadPairing().catch(() => {});
  }, 5000);

  // The exchange's rows, faster and only while they are on screen.
  //
  // Two seconds because this is the one list where the owner is waiting on
  // somebody at another keyboard: the gap between them pressing 核准 and this
  // row offering 確認 is dead time in front of two people. And only while the
  // drawer is open, because each read makes the node dial every machine it is
  // waiting on — a poll that outlived the panel would keep doing that for as
  // long as the window was left running.
  setInterval(() => {
    if (state.view !== "network" || !pairingDrawerOpen()) return;
    // Not while a decision is in flight: the answer would repaint the rows
    // under the button that is still being pressed.
    if (state.busy) return;
    // The trusted-node list behind the drawer goes with it. A decision made in
    // here refreshes that list, but a revoke run in a terminal did not: the
    // drawer is a modal, so it held the fifteen-second refresh off, and the
    // list sat there naming a node this machine had already stopped trusting.
    // One render of the rows per tick, not two: this read used to end in
    // renderPairRequests and so does the overview's render() right behind it.
    // The rows are drawn here only when that read was abandoned — a dialog
    // opened, a caret in a field — and never reached the screen.
    // The overview's own failure must not take the rows with it. Without the
    // inner catch a rejected load() skips the step below, so a read that did
    // reach the node never reaches the screen and the request rows freeze at
    // whatever they last showed — on the one panel where a row going stale is
    // somebody at another keyboard waiting.
    loadPairRequests({ render: false })
      .then(() => load({ background: true, exceptPairingDrawer: true }).catch(() => false))
      .then((rendered) => {
        if (!rendered) renderPairRequests();
      })
      .catch(() => {});
  }, 2000);

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

  // The session table and the node list refresh on their own too.
  //
  // They used to load once at startup and then only when someone pressed
  // refresh. A node that was still rescanning at that moment left the window
  // empty for as long as nobody noticed — the window said "0 sessions" while the
  // node was serving 1083 (issue #114). Fifteen seconds is the same order as the
  // node's publish interval, so the table is never more than one interval behind.
  //
  // Skipped whenever a refresh would pull the ground out from under someone: a
  // write is in flight, a dialog is open on top of the table, or rows are
  // selected and a rescan would drop the selection out from under the next click.
  // The same check runs again inside load() before a background answer is
  // applied, because the owner can start any of those while the read is in the
  // air.
  setInterval(() => {
    if (interactionInProgress()) return;
    load({ background: true }).catch((error) => banner(t("busy.failed", { action: t("app.busyLoad"), error })));
  }, 15000);

  load()
    .then(loadPairing)
    .catch((error) => banner(t("busy.failed", { action: t("app.busyLoad"), error })));

  el("service-refresh").onclick = () => loadService()
    .catch((error) => banner(t("busy.failed", { action: t("service.busyRead"), error })));
  el("service-open").onclick = () => openServiceForm()
    .catch((error) => banner(t("busy.failed", { action: t("service.busyOpenForm"), error })));
  el("service-cancel").onclick = () => el("service-form").classList.add("hidden");
  el("service-install").onclick = installService;
  el("service-uninstall").onclick = uninstallService;
  el("service-restart").onclick = restartNode;

  return internals;
}
