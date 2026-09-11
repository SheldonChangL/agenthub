// Pure functions over the session list: search, grouped filters, facet counts,
// and sorting. No DOM, no state, no bindings — this file is what the tests in
// test/sessions-filter.mjs import, and what render code calls.
//
// Filter model (docs/ui-contract.md §5.1–5.3): three groups — provider, status,
// audience. Within a group the chosen values are OR'd; across groups AND. An
// empty group means "no restriction". Counts on a chip are facet counts: how
// many sessions would match if that chip were the only choice in ITS group,
// with the other groups' choices and the search term still applied. That is
// what makes the number next to "Codex" agree with the table when "active" is
// on: it answers "how many Codex among what you are already looking at".

export const GROUPS = Object.freeze(["provider", "status", "audience"]);

export const CHIPS = Object.freeze([
  { group: "provider", value: "claude", label: "Claude" },
  { group: "provider", value: "codex", label: "Codex" },
  { group: "status", value: "active", label: "active" },
  { group: "status", value: "idle", label: "idle" },
  { group: "status", value: "inactive", label: "inactive" },
  { group: "audience", value: "all_paired", label: "所有已配對" },
  { group: "audience", value: "selected", label: "指定節點" },
  { group: "audience", value: "none", label: "不公開" },
]);

export function emptyFilters() {
  return { provider: new Set(), status: new Set(), audience: new Set() };
}

// audienceMode is the value the audience group filters on. A "selected" mode
// with no nodes is not published to anyone, but it is still what the owner
// chose, so it filters as "selected" — the table cell says 「指定節點（無）」.
export function audienceMode(session) {
  return session?.audience?.mode ?? "none";
}

function groupValue(session, group) {
  if (group === "provider") return session.provider ?? "";
  if (group === "status") return session.status ?? "";
  return audienceMode(session);
}

// matchesSearch looks at every text a row shows, lower-cased. The id is
// matched whole (provider prefix included) so "codex:" narrows to Codex rows
// and a pasted full id finds exactly one row.
export function matchesSearch(session, term) {
  if (!term) return true;
  const haystack = [
    session.id,
    session.cwd,
    session.management,
    session.provider,
    session.status,
    audienceMode(session),
  ].map((value) => String(value ?? "").toLowerCase()).join(" ");
  return haystack.includes(term);
}

// matchesGroups applies every group except `skip` (used for facet counts).
function matchesGroups(session, filters, skip = null) {
  for (const group of GROUPS) {
    if (group === skip) continue;
    const chosen = filters[group];
    if (!chosen || chosen.size === 0) continue;
    if (!chosen.has(groupValue(session, group))) return false;
  }
  return true;
}

export function normalizeTerm(search) {
  return String(search ?? "").trim().toLowerCase();
}

// visible returns the sessions that pass the search and every group.
export function visible(sessions, { search = "", filters = emptyFilters() } = {}) {
  const term = normalizeTerm(search);
  return sessions.filter((session) => matchesSearch(session, term) && matchesGroups(session, filters));
}

// facetCounts answers, for every chip, how many sessions it would match given
// the OTHER groups and the search. Keys are `${group}:${value}`.
export function facetCounts(sessions, { search = "", filters = emptyFilters() } = {}) {
  const term = normalizeTerm(search);
  const counts = {};
  for (const chip of CHIPS) counts[`${chip.group}:${chip.value}`] = 0;
  for (const session of sessions) {
    if (!matchesSearch(session, term)) continue;
    for (const group of GROUPS) {
      if (!matchesGroups(session, filters, group)) continue;
      const key = `${group}:${groupValue(session, group)}`;
      if (key in counts) counts[key] += 1;
    }
  }
  return counts;
}

export function toggleChip(filters, group, value) {
  const next = { provider: new Set(filters.provider), status: new Set(filters.status), audience: new Set(filters.audience) };
  if (next[group].has(value)) next[group].delete(value);
  else next[group].add(value);
  return next;
}

export function hasAnyFilter(filters) {
  return GROUPS.some((group) => filters[group].size > 0);
}

/* ---------------- sorting ---------------- */

export const SORT_KEYS = Object.freeze(["lastSeenAt", "id", "provider", "status", "management", "audience", "cwd"]);
export const DEFAULT_SORT = Object.freeze({ key: "lastSeenAt", dir: "desc" });

// Status has a meaning order, not an alphabetical one: what is moving comes
// before what is resting comes before what is gone. Unknown values sort last.
const STATUS_RANK = { active: 0, idle: 1, inactive: 2 };
const AUDIENCE_RANK = { all_paired: 0, selected: 1, none: 2 };

function sortValue(session, key) {
  switch (key) {
    case "lastSeenAt": {
      const t = new Date(session.lastSeenAt).getTime();
      return Number.isFinite(t) ? t : -Infinity;
    }
    case "status":
      return STATUS_RANK[session.status] ?? 9;
    case "audience": {
      const mode = audienceMode(session);
      // Within "selected", more nodes first when descending is natural for
      // "who is most exposed"; keep it simple: rank then node count.
      const nodes = mode === "selected" ? (session.audience?.nodes?.length ?? 0) : 0;
      return (AUDIENCE_RANK[mode] ?? 9) * 1000 - nodes;
    }
    case "provider":
    case "management":
    case "cwd":
    case "id":
      return String(session[key] ?? "").toLowerCase();
    default:
      return "";
  }
}

// sorted returns a new array; ties fall back to id so the order is stable
// across renders and does not shuffle rows while the owner is reading them.
export function sorted(sessions, sort = DEFAULT_SORT) {
  const key = SORT_KEYS.includes(sort?.key) ? sort.key : DEFAULT_SORT.key;
  const sign = sort?.dir === "asc" ? 1 : -1;
  return [...sessions].sort((a, b) => {
    const va = sortValue(a, key);
    const vb = sortValue(b, key);
    if (va < vb) return -1 * sign;
    if (va > vb) return 1 * sign;
    return String(a.id).localeCompare(String(b.id));
  });
}

// nextSort: clicking the current key flips direction; a new key starts in the
// direction that puts the interesting end first (newest, active, published).
export function nextSort(current, key) {
  if (current?.key === key) return { key, dir: current.dir === "desc" ? "asc" : "desc" };
  const descFirst = key === "lastSeenAt";
  return { key, dir: descFirst ? "desc" : "asc" };
}

/* ---------------- persistence ---------------- */

export const PREFS_KEY = "agenthub.desktop.sessions.prefs.v1";

// Serialisable shape of what the owner set up: filters as arrays, sort, and
// the hacker-background switch. Unknown or malformed stored values are
// dropped field by field rather than rejecting the whole record.
export function serializePrefs({ filters, sort, search }) {
  return JSON.stringify({
    filters: { provider: [...filters.provider], status: [...filters.status], audience: [...filters.audience] },
    sort: { key: sort.key, dir: sort.dir },
    search: search ?? "",
  });
}

export function parsePrefs(raw) {
  const prefs = { filters: emptyFilters(), sort: { ...DEFAULT_SORT }, search: "" };
  if (!raw) return prefs;
  let parsed;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return prefs;
  }
  if (parsed && typeof parsed === "object") {
    for (const group of GROUPS) {
      const values = parsed.filters?.[group];
      if (Array.isArray(values)) {
        const allowed = new Set(CHIPS.filter((c) => c.group === group).map((c) => c.value));
        prefs.filters[group] = new Set(values.filter((v) => allowed.has(v)));
      }
    }
    if (SORT_KEYS.includes(parsed.sort?.key)) {
      prefs.sort = { key: parsed.sort.key, dir: parsed.sort.dir === "asc" ? "asc" : "desc" };
    }
    if (typeof parsed.search === "string") prefs.search = parsed.search.slice(0, 200);
  }
  return prefs;
}
