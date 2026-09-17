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
    // Whether the last rescan came back with nothing. "No sessions yet" and
    // "we looked and there are none" are different sentences, and only the
    // second one is worth spending a paragraph on where AgentHub looks.
    discoveredNothing: false,
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
    if (mode === "all_paired") return { text: t("audience.cell.allPaired"), published: true };
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

  function flagChip(label, on, warn = false) {
    return element("span", `flag${on ? " on" : ""}${warn ? " warn" : ""}`, label);
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

      // The name the session's own app gives the conversation is what a
      // person recognises a row by; the UUID is only ever needed to resume or
      // to quote one, and the copy button and the tooltip both still carry
      // it. Sessions the provider never named keep showing the ID, because a
      // row with no handle at all is worse than a row with an ugly one.
      const idCell = element("td", "sid");
      const label = session.title
        ? element("b", "title", session.title)
        : element("b", "", rest);
      idCell.append(element("span", "providertag", session.provider), label);
      idCell.title = session.title ? `${session.title}\n${session.id}` : session.id;

      // The path goes in a <bdi> because the cell is laid out right-to-left so
      // that a path too long for the column loses its head rather than its
      // tail — the project name is the part worth keeping. The isolate stops
      // that direction from reordering the path's own slashes.
      const cwdCell = element("td", "mono muted cwd");
      if (session.cwd) {
        cwdCell.append(element("bdi", "", session.cwd));
        cwdCell.title = session.cwd;
      } else {
        cwdCell.append(element("bdi", "", "—"));
      }

      // The four audience flags, readable without opening the dialog. Only
      // meaningful when something is published; a private row shows them dim.
      const flags = element("td");
      const a = session.audience ?? {};
      const chips = element("span", "flagchips");
      chips.append(
        flagChip("CWD", Boolean(a.exportCwd)),
        flagChip(t("row.flagIn"), Boolean(a.acceptMessages)),
        flagChip(t("row.flagOut"), Boolean(a.allowOutbound)),
        flagChip(t("row.flagWake"), Boolean(a.autoWake), true),
      );
      flags.append(chips);

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
      const actions = element("td", "col-actions");
      const group = element("span", "rowactions");
      group.append(
        rowActionButton("inbox", t("inbox.title"), t("row.inboxTitle"), () => {
          openInbox(session.id).catch((error) => banner(t("inbox.readFailed", { error })));
        }),
        rowActionButton("resume", "resume", t("row.resumeTitle", { command: resumeCommand(session) }), () =>
          copyResumeCommand(session)),
      );
      actions.append(group);

      tr.append(
        checkCell,
        idCell,
        cell(element("td"), pill(session.status, statusPillClass(session.status))),
        element("td", "muted mgmt", managementLabel(session.management)),
        cell(element("td"), pill(audience.text, audience.published ? "public" : "")),
        flags,
        cwdCell,
        element("td", "muted", relative(session.lastSeenAt)),
        actions
      );
      fragment.append(tr);
    }

    body.replaceChildren(fragment);
    el("empty").classList.toggle("hidden", rows.length > 0);
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
      return;
    }
    el("service-open")?.focus?.();
  }

  // goToPairing switches to the view the drawer belongs to before opening it.
  // The drawer polls the exchange's rows only while that view is on screen, so
  // opening it from the sessions table alone would show rows that never refresh.
  function goToPairing() {
    state.view = "network";
    render();
    openPairingDrawer();
  }

  // Which of the four situations this card exists for the window is in. The
  // unreachable case is the one that does NOT wait for a successful read: a
  // node that never answered is exactly the state this card is for, and gating
  // it on loadedOnce would hide it precisely then. The other three wait,
  // because "no sessions" on a read that did not reach the node is not a fact
  // about this machine (issue #114).
  function onboardingTriggered() {
    if (!state.nodeReachable) return true;
    if (!state.loadedOnce) return false;
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
    //    unit is how one node becomes two — so that case explains and offers
    //    nothing, exactly as the service panel does.
    if (status.toolError) {
      steps.push({
        id: "service",
        title: t("onboarding.service.title"),
        body: t("onboarding.service.bodyNoAh", { error: status.toolError }),
        done: false,
        actions: [],
      });
    } else if (state.service && status.supported === false) {
      // Windows today: the node runs, nothing this app can ask holds it, and
      // the window starts and stops it itself.
      steps.push({
        id: "service",
        title: t("onboarding.service.titleStart"),
        body: t("onboarding.service.bodyUnsupported"),
        done: state.nodeReachable,
        actions: [{
          label: state.nodeReachable ? t("onboarding.service.actionRestart") : t("onboarding.service.actionStart"),
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

    // 2. Only once a read has reached the node. An empty table on a read that
    //    failed says nothing about what is on this disk.
    if (state.loadedOnce) {
      const done = state.sessions.length > 0;
      steps.push({
        id: "sessions",
        title: t("onboarding.sessions.title"),
        // A scan that came back with nothing turns the step into the
        // explanation it needs: a second press of the same button would find
        // the same nothing.
        body: !done && state.discoveredNothing
          ? t("onboarding.sessions.bodyNoneFound")
          : t("onboarding.sessions.body"),
        done,
        actions: done ? [] : [{
          label: t("onboarding.sessions.action"),
          primary: true,
          run: () => discoverSessions().catch(() => {}),
        }],
      });
    }

    // 3. Reachability, which is the step with a switch behind it.
    //
    //    The buttons are built by peerListenRepairs() and executed by
    //    applyPeerListenRepair(), the same pair the node settings panel uses.
    //    That is deliberate on two counts. peerListenRepairs names "allow LAN
    //    connections" in the label exactly when clicking would turn it on
    //    (docs/ui-contract.md §7.8 rule 4: this window never ticks that box
    //    behind anybody), and applyPeerListenRepair goes through the form, so
    //    the save is validated, restarted and checked for having stuck by the
    //    one path that already does all three.
    const here = pairHereState(state.pairing?.state);
    const reachable = { id: "reachable", title: t("onboarding.reachable.title"), done: here.reachable === true, actions: [] };
    if (reachable.done) {
      reachable.body = t("onboarding.reachable.bodyDone", { address: here.address });
    } else if (!state.nodeSettings) {
      // applyPeerListenRepair fills the real form, so the form has to hold the
      // node's current answer before any of this is offered.
      reachable.body = t("onboarding.reachable.bodyLoading");
    } else {
      const current = state.nodeSettings.saved?.peerListen
        || state.nodeSettings.settings?.peerListen
        || LOOPBACK_LISTEN;
      const options = peerListenRepairs(
        { reason: "loopback", address: current },
        state.nodeAddresses ?? { list: [], failure: "" },
        el("node-allow-lan").checked,
      );
      // peerListenRepairs always ends with "stay local only", which is this
      // step's skip: an owner who has decided to be off the network needs a way
      // to say so, or the card is the thing they learn to ignore.
      const offersAddress = options.some((option) => option.peerListen !== "");
      reachable.body = offersAddress ? t("onboarding.reachable.body") : t("onboarding.reachable.bodyNoAddress");
      reachable.actions = options.map((option) => ({
        label: option.label,
        primary: option.primary,
        run: () => applyPeerListenRepair(option).catch(() => {}),
      }));
      if (!offersAddress) {
        reachable.actions.unshift({
          label: t("onboarding.reachable.openSettings"),
          primary: true,
          run: () => goToNodeSettings(),
        });
      }
    }
    steps.push(reachable);

    // 4. A doorway to the drawer, which explains the rest itself.
    const paired = state.nodes.length > 0;
    steps.push({
      id: "pair",
      title: t("onboarding.pair.title"),
      body: t("onboarding.pair.body"),
      done: paired,
      actions: paired ? [] : [{ label: t("onboarding.pair.action"), primary: true, run: () => goToPairing() }],
    });

    // 5. Text, and a button that points rather than acts. Opening the audience
    //    dialog with nothing selected is a dead dialog — it resets all four
    //    flags every time on purpose (docs/ui-contract.md §3.5) — so this one
    //    puts the keyboard on the checkbox that starts a selection.
    const published = (state.counts.all_paired ?? 0) + (state.counts.selected ?? 0) > 0;
    steps.push({
      id: "publish",
      title: t("onboarding.publish.title"),
      body: t("onboarding.publish.body"),
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
  let onboardingSettingsAsked = false;

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
    if (show) onboardingAllDoneShown = false;
    // Finished, and shown once more with every step ticked before it goes. A
    // card that vanishes under the click that completed it reads as a glitch;
    // one that stays forever is the thing people learn to ignore. Never
    // reopened afterwards — the settings panel's own warnings and the pairing
    // drawer's notices are where a regression is said out loud.
    if (!show && !state.ui.onboardingDismissed && !onboardingAllDoneShown
      && !section.classList.contains("hidden") && steps.every((step) => step.done)) {
      show = true;
      onboardingAllDoneShown = true;
    }
    section.classList.toggle("hidden", !show);
    if (!show) return;
    // The reachability step offers buttons that fill the node settings form, so
    // the form has to hold the node's answer first. Asked once per window: a
    // read on every render would re-fire on every background tick.
    if (!state.nodeSettings && !onboardingSettingsAsked && state.nodeReachable) {
      onboardingSettingsAsked = true;
      loadNodeSettings().then(() => renderOnboarding()).catch(() => {});
    }
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

  function openPairingDrawer() {
    el("pairing-modal").classList.remove("hidden");
    loadPairing().catch(() => {});
    loadPairRequests().catch(() => {});
  }
  function closePairingDrawer() {
    el("pairing-modal").classList.add("hidden");
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

  // Anything the owner is in the middle of that a repainted table would pull out
  // from under them: a write in flight, rows selected that a rescan could drop,
  // or a dialog standing on top of the list. The periodic tick and the moment a
  // background read lands both ask this — one list of conditions, checked twice,
  // because the state can change while the read is in the air.
  const MODAL_IDS = ["audience-modal", "pair-modal", "inbox-modal", "pairing-modal", "mcp-modal", "modal"];
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

    // Drop selections that no longer exist after a rescan.
    const alive = new Set(state.sessions.map((s) => s.id));
    for (const id of [...state.selected]) if (!alive.has(id)) state.selected.delete(id);

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
    "windowOpenUnreachable", "hereCopied", "hereCopyFailed",
    "send", "sendFromCandidate", "sendManual", "addressEmpty", "addressNote", "sent",
    "requestsHeading", "showDecided", "requestsEmpty", "requestsEmptyAll", "requestsUnread",
    "requestsFailed", "approve", "confirm", "reject", "nodeSaid",
  ]);
  // The three sentences a person carries out step by step, as an array because
  // that is what reads them.
  Object.defineProperty(PAIR_TEXT, "compare", {
    get: () => [t("pair.compare.1"), t("pair.compare.2"), t("pair.compare.3")],
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
        ...(undecided ? PAIR_TEXT.compare.map((sentence) => element("div", "stale", sentence)) : []));
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
      plural(waiting.length, "pair.waiting", { panel: PAIR_TEXT.requestsHeading })));
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
    // An address nobody can reach — or no address at all — is not shown as the
    // address to type. What goes here instead is what is wrong and the button
    // that fixes it.
    if (!here.reachable) {
      value.textContent = PAIR_TEXT.hereFixHeadline;
      el("copy-pair-address-status").textContent = "";
      note.replaceChildren(element("div", "",
        here.address === "" ? PAIR_TEXT.hereNoAddressWhy : PAIR_TEXT.hereUnreachable));
      // The node's own sentence about this listener, under the remedy rather
      // than in front of it: the remedy is the act, and the node's words are
      // the detail that says which listener it is about.
      if (here.problem) note.append(element("div", "muted", here.problem));
      const fix = element("button", "primary", PAIR_TEXT.hereFix);
      fix.onclick = () => goToNodeSettings();
      note.append(fix);
      el("copy-pair-address").disabled = true;
      return;
    }
    el("copy-pair-address").disabled = false;
    value.textContent = here.address;
    // Two sentences, because the two situations have different remedies: on a
    // node that announces nothing this address is the only way in, and on one
    // that announces it is what to fall back on when the other machine's list
    // stays empty anyway.
    note.textContent = window_.notice ? PAIR_TEXT.hereNote : PAIR_TEXT.hereNoteAnnouncing;
  }

  // copyPairAddress hands that address to the clipboard, and says so when the
  // clipboard refuses: the address is on screen either way, and a copy silently
  // reported as done is a string typed wrong on the other machine.
  async function copyPairAddress() {
    const status = el("copy-pair-address-status");
    const address = state.pairing?.state?.peerAddress || "";
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
      return;
    }
    if (!windowAvailable) {
      headline.textContent = PAIR_TEXT.windowUnavailable;
      detail.append(element("div", "stale", t("pair.stateUnreadable")));
      note.textContent = pairing.error || "";
      on.disabled = true;
      off.disabled = true;
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
    line.textContent = left === 0 ? "" : t("pair.timeLeft", { left: clock(left) });
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
    if (pairing.notice) notice.textContent = pairing.notice;
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
    el("node-detail-body").replaceChildren(...(selected ? nodeDetail(selected) : [
      element("div", "empty", t("network.pickANode")),
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

  function nodeDetail(node) {
    const heading = element("h2", "", node.displayName);
    const fingerprint = element("div", "fingerprint", node.fingerprint);
    const note = element("p", "muted", t("network.fingerprintNote"));

    // Trust is recorded per machine, and this page shows only this machine's
    // half. Pairing on the mac left the Ubuntu box answering "No paired nodes"
    // on 2026-09-10, and nothing here said that was half-done — the row simply
    // sat there having never been heard from, which reads as the peer being off.
    const mutualNote = element("p", "stale", t("network.mutualNote"));

    const rows = [
      [t("identity.nodeId"), node.nodeId],
      [t("pairManual.platform"), node.platform],
      [t("network.detailPairedAt"), node.pairedAt ? relative(node.pairedAt) : "—"],
      [t("network.detailLastContact"),
        node.lastSeenAt ? relative(node.lastSeenAt) : t("network.neverInContact")],
      [t("network.detailVisibleSessions"), plural(grantedCount(node.nodeId), "network.sessionCount")],
      [t("network.detailAddress"), node.address ? node.address : t("network.detailNoAddress")],
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
      heading, fingerprint, note, mutualNote, grid,
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

  async function revokeSelected(node) {
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
  function renderAudienceNodeList() {
    const list = el("audience-node-list");
    list.replaceChildren();
    for (const node of state.nodes) {
      const label = element("label", "nodepick");
      const box = document.createElement("input");
      box.type = "checkbox";
      box.value = node.nodeId;
      box.className = "audience-node-box";
      box.onchange = () => label.classList.toggle("on", box.checked);
      const presence = presenceLabel(presenceFor(node.nodeId));
      label.append(box, element("span", `dot ${presence.className}`), element("span", "", node.displayName), element("span", "mono", node.nodeId));
      list.append(label);
    }
    if (state.nodes.length === 0) list.append(element("p", "muted", t("audience.noNodesYet")));
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

  function openAudienceModal() {
    renderAudienceCount();
    const picked = state.sessions.filter((session) => state.selected.has(session.id));
    el("audience-selected").replaceChildren(...picked.map((session) => element("span", "", session.id)));
    renderAudienceNodeList();
    // The mode starts at 不公開 every time, like the flags: a mode left over from
    // the last selection is a publication about to happen to a different one.
    for (const radio of document.querySelectorAll('input[name="audience-mode"]')) {
      radio.checked = radio.value === "none";
    }
    // Every flag starts off, every time.
    //
    // The dialog applies to whatever is selected and reads its values from the
    // boxes, so a box left ticked from the last time it was opened is a setting
    // about to be applied to a different set of sessions. That was survivable
    // while the flags only governed what could be read; one of them now starts
    // turns in an agent with nobody watching, and inheriting that from a
    // previous dialog is not something anyone would choose on purpose.
    //
    // Off rather than the current value: these apply to a selection, which may
    // hold sessions that disagree, and there is no honest way to show one state
    // for several. Off is the safe half of that disagreement.
    for (const id of ["audience-cwd", "audience-messages", "audience-outbound", "audience-autowake"]) {
      el(id).checked = false;
    }
    renderAutoWakeNote();
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
  function renderAutoWakeNote() {
    const note = el("audience-autowake-note");
    note.replaceChildren();
    if (!state.nodeAutoWake) {
      note.append(element("div", "muted", t("audience.autoWakeNodeOff")));
      return;
    }
    // Which providers are selected decides which of the remaining obstacles
    // apply, and a mixed selection gets both sentences: the owner is about to
    // apply one setting to sessions that will behave differently.
    const providers = new Set(
      state.sessions.filter((session) => state.selected.has(session.id)).map((session) => session.provider),
    );
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
    const checked = [...document.querySelectorAll("#audience-node-list input.audience-node-box")]
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

  // renderServiceRepair offers the one action that fixes a unit which overrides
  // this window.
  //
  // A unit that passes a node setting on every start wins over everything the
  // settings page saves, and the node's own log is the only place that says so.
  // An owner reading a form whose writes do nothing has no way to reach that
  // conclusion, so the panel says it and offers the repair: register the
  // service again with nothing but the database path, which is what this app
  // installs today.
  function renderServiceRepair(status) {
    const repair = el("service-repair");
    repair.replaceChildren();
    const pinned = status.pinnedSettings ?? [];
    if (!status.installed || pinned.length === 0) return;
    repair.append(
      element("div", "stale",
        t("service.pinnedSettings", { pinned: pinned.join(t("candidate.flagJoin")) })),
      element("div", "muted",
        status.dbPathKnown && status.dbPath
          ? t("service.unpinExplainsDb", { path: status.dbPath })
          : t("service.unpinExplains")),
    );
    const button = document.createElement("button");
    button.id = "service-unpin";
    button.className = "primary";
    button.textContent = t("service.unpin");
    button.onclick = () => reinstallWithoutPinnedSettings(status);
    repair.append(button);
  }

  // reinstallWithoutPinnedSettings re-registers the service with the database it
  // already uses and nothing else.
  //
  // The database path is carried over deliberately and is the whole reason this
  // is not just "press install": a reinstall that dropped it would put the node
  // on a different database, which is a different identity and no pairings.
  async function reinstallWithoutPinnedSettings(status) {
    if (status.installed && !status.dbPathKnown) {
      banner(t("service.unpinNeedsDbPath"));
      return;
    }
    const ok = confirm(t("service.unpinConfirm", {
      path: status.dbPath || t("service.nodeDefaultLocation"),
    }));
    if (!ok) return;
    const previousPid = state.service?.pid ?? 0;
    await withBusy(t("service.busyReregister"), async () => {
      const result = await api.InstallService({ dbPath: status.dbPath });
      showServiceOutput(result);
      const up = await waitForNode({ previousPid });
      await load();
      banner(up.answering ? t("service.reregistered") : t("service.reregisteredNoAnswer"), up.answering);
    });
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
      const ok = confirm(t("service.reinstallUnknownDbConfirm", {
        wanted: wanted || t("service.nodeDefaultLocation"),
      }));
      if (!ok) return;
    } else if (baseline.installed && wanted !== baseline.path) {
      const ok = confirm(t("service.changeDbConfirm", {
        current: baseline.path || t("service.nodeDefaultLocation"),
        wanted: wanted || t("service.nodeDefaultLocation"),
      }));
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
    const ok = confirm(t("service.uninstallConfirm"));
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
  el("btn-pair").onclick = openPairingDrawer;
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
      // Kept so the checklist can tell "nothing found yet" from "we looked and
      // there is nothing here", which are different things to say.
      state.discoveredNothing = (counts.total ?? 0) === 0;
      const skipped = counts.skipped ?? 0;
      banner(t("app.rescanned", {
        claude: counts.claude, codex: counts.codex, total: counts.total,
      }) + (skipped > 0 ? t("app.rescanSkipped", { skipped }) : ""), skipped === 0);
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
  el("inbox-clear").onclick = () => {
    const session = state.inboxSession;
    if (!session) return;
    // Not undoable, so it is asked rather than assumed. The node has no
    // "unclear", and messages that arrive between this dialog and the confirm go
    // with the rest.
    if (!confirm(t("inbox.clearConfirm", { session }))) return;
    withBusy(t("inbox.busyClear"), async () => {
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
      if (Number.isFinite(next) && next < 65536 && Number(port) >= 1024) {
        repairs.push({
          label: t("nodeSettings.repairPort", { address: `${host}:${next}` }),
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

    // The note explains the field's current contents, so it is shown whenever
    // those contents are the suggestion — never left behind after the address
    // that justified it is gone.
    const note = el("node-private-note");
    const showing = state.nodePrivateSuggested !== "" &&
      el("node-private").value.trim() === state.nodePrivateSuggested;
    note.textContent = showing
      ? t("nodeSettings.privateSuggested", { subnet: state.nodePrivateSuggested })
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
    const select = el("node-peerlisten");
    const option = select.options[select.selectedIndex];
    const suggestion = option && option.value && option.dataset.private === "" ? option.dataset.subnet : "";
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
  function readNodeSettingsPatch() {
    // The saved configuration, because that is what the node merges a write
    // onto — see applyNodeSettings.
    const before = state.nodeSettings?.saved ?? {};
    const patch = {};
    const peerListen = el("node-peerlisten").value || LOOPBACK_LISTEN;
    if (peerListen !== (before.peerListen || LOOPBACK_LISTEN)) patch.peerListen = peerListen;
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
        for (const id of ["node-peerlisten-source", "node-allowlan-source", "node-discover-source",
          "node-private-source", "node-autowake-source"]) {
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

      // What `ah service status` says afterwards, not what was sent.
      //
      // This is the moment a node is most likely not to come back: the values
      // that were just saved are the ones it reads at start-up, and a service
      // set to restart on failure will crash-loop on a combination it refuses.
      // Announcing "restarted" because the restart command returned would be a
      // claim about the one thing this window can actually check and did not.
      if (live.unknown) {
        banner(
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
          banner(t("nodeSettings.savedNodeAnswering"), true);
          return;
        }
        banner(
          t("nodeSettings.savedNodeSilent"),
        );
        return;
      }
      if (back.running && back.nodeAnswering) {
        banner(t("nodeSettings.savedServiceAnswering"), true);
        return;
      }
      // Not marked successful, so it stays on screen: the settings just saved
      // are the first thing to suspect, and they are still on the form above.
      banner(
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
    const labels = {
      peerListen: t("nodeSettings.peerListenLabel"),
      allowLan: t("nodeSettings.allowLan"),
      discover: t("nodeSettings.discoverShort"),
      treatAsPrivate: t("nodeSettings.privateLabel"),
      autoWake: t("nodeSettings.autoWakeShort"),
    };
    return Object.keys(patch)
      .filter((key) => !sameSettingValue(patch[key], savedAfter?.[key], key))
      .map((key) => labels[key] ?? key);
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
    if (!el("audience-modal").classList.contains("hidden")) renderAudienceCount();
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

  el("btn-open-pairing").onclick = openPairingDrawer;
  el("pairing-close").onclick = closePairingDrawer;
  el("pairing-modal").onclick = (event) => {
    if (event.target === el("pairing-modal")) closePairingDrawer();
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
    state, load, loadPairing, render, renderRows, renderInbox, renderPairing,
    openAudienceModal, renderAudienceCount, readAudienceForm, openInbox, openMCPConfig, closeMCPConfig,
    candidateRow, prefillPairFrom, nodeDetail, nodeSessions, presenceLabel, heardFrom,
    loadPairRequests, renderPairRequests, pairRequestRow, sendPairRequest, decidePairRequest,
    pairErrorMessage, renderPairHere, copyPairAddress, pairingDrawerOpen, PAIR_TEXT,
    pairAddressReachable, pairHereState, goToNodeSettings, renderPairingSubtitle, pairDecisionMessage,
    pairingRemaining, tickCountdown, visible, managementLabel, showInboxTab, loadOutbound, loadWakes, resumeCommand,
    copyResumeCommand, openPairingDrawer, closePairingDrawer, didNotStick, sameSettingValue, paintAfterSave,
    serviceStatusOrUnknown, loadService, renderService, restartNode, waitForNode,
    openServiceForm, installService, renderServiceRepair, reinstallWithoutPinnedSettings,
    renderPeerListenProblem, peerListenRepairs, applyPeerListenRepair,
    loadNodeSettings, saveNodeSettings, applyNodeSettings, readNodeSettingsPatch,
    renderNodeLine, relabelNodeSettings, repaintFromState, renderMCPStatus,
    onboardingSteps, onboardingTriggered, renderOnboarding, goToService, goToPairing, discoverSessions,
    backdropPlan, describeBackdropState, buildRain, applyBackdrop, loadPrefs, savePrefs,
    t, plural, setUILanguage, paintStatic, pickLanguage, setLanguage, language,
    isLoopbackListen, isPrivateByDefinition, coversAddress, canJudgePrivacy, syncNodeSettingsForm, suggestPrivateRange, fetchLocalAddresses,
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
