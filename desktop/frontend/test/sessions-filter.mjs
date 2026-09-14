// The filter model behind the session table, tested without a DOM.
//
// Within a group the chosen chips OR; across groups they AND; a chip's count
// is a facet count — what it would match with the OTHER groups still applied.
// The old chips showed global counts, so filtering to Codex still showed
// "active 3" while the table held one active Codex row. That mismatch is the
// thing these assertions pin.
//
//   node frontend/test/sessions-filter.mjs

import {
  CHIPS, emptyFilters, visible, facetCounts, toggleChip, sorted, nextSort,
  DEFAULT_SORT, serializePrefs, parsePrefs, hasAnyFilter,
} from "../src/sessions/filter.js";

const failures = [];
const check = (cond, msg) => { if (!cond) failures.push(msg); };

const s = (id, provider, status, mode, extra = {}) => ({
  id: `${provider}:${id}`, provider, status,
  audience: { mode, nodes: extra.nodes ?? [] },
  cwd: extra.cwd ?? "", management: extra.management ?? "由 CLI 管理",
  lastSeenAt: extra.at ?? "2026-09-11T00:00:00Z",
});
const rows = [
  s("a1", "claude", "active", "all_paired", { at: "2026-09-11T03:00:00Z", cwd: "/p/agenthub" }),
  s("a2", "claude", "idle", "selected", { nodes: ["n1"], at: "2026-09-11T02:00:00Z", cwd: "/p/prm" }),
  s("a3", "claude", "inactive", "none", { at: "2026-09-10T00:00:00Z" }),
  s("c1", "codex", "active", "none", { at: "2026-09-11T02:30:00Z", management: "由 app-server 管理", cwd: "/p/serial" }),
  s("c2", "codex", "idle", "selected", { nodes: [], at: "2026-09-11T01:00:00Z" }),
];

// No filter: everything.
check(visible(rows).length === 5, "no filter should show every row");

// One chip: OR within a group is trivially that chip.
let f = toggleChip(emptyFilters(), "provider", "codex");
check(visible(rows, { filters: f }).map((r) => r.id).join() === "codex:c1,codex:c2", "provider=codex");

// Two chips in the same group OR.
f = toggleChip(toggleChip(emptyFilters(), "status", "active"), "status", "idle");
check(visible(rows, { filters: f }).length === 4, "active OR idle should be 4");

// Across groups AND.
f = toggleChip(f, "provider", "codex");
check(visible(rows, { filters: f }).map((r) => r.id).join() === "codex:c1,codex:c2", "(active|idle) AND codex");

// Toggling off removes it.
f = toggleChip(f, "provider", "codex");
check(f.provider.size === 0 && hasAnyFilter(f), "toggle off leaves the other group");

// Facet counts: with provider=codex on, the status chips count only Codex rows,
// while the provider chips ignore their own group and count all statuses.
f = toggleChip(emptyFilters(), "provider", "codex");
let counts = facetCounts(rows, { filters: f });
check(counts["status:active"] === 1, `status:active under codex should be 1, got ${counts["status:active"]}`);
check(counts["status:inactive"] === 0, "status:inactive under codex should be 0");
check(counts["provider:claude"] === 3, `provider:claude should still be 3 (own group ignored), got ${counts["provider:claude"]}`);
check(counts["audience:selected"] === 1, "audience:selected under codex should be 1 (c2, empty node list still counts as selected)");

// Facet counts respect the search term too.
counts = facetCounts(rows, { search: "app-server" });
check(counts["provider:codex"] === 1 && counts["provider:claude"] === 0, "search narrows facet counts");

// Search covers id, cwd, management, provider, status, audience mode.
check(visible(rows, { search: "codex:" }).length === 2, "search by provider prefix in id");
check(visible(rows, { search: "/p/prm" }).length === 1, "search by cwd");
check(visible(rows, { search: "APP-SERVER" }).length === 1, "search is case-insensitive over management");
check(visible(rows, { search: "all_paired" }).length === 1, "search over audience mode");

// A chip value that no row has still appears in counts as 0, never undefined.
for (const chip of CHIPS) check(typeof counts[`${chip.group}:${chip.value}`] === "number", `count missing for ${chip.value}`);

// Sorting: default newest first; ties broken by id so order is stable.
let order = sorted(rows).map((r) => r.id);
check(order[0] === "claude:a1" && order[1] === "codex:c1" && order[4] === "claude:a3", `default sort newest first, got ${order}`);
order = sorted(rows, { key: "status", dir: "asc" }).map((r) => r.id);
check(order.slice(0, 2).join() === "claude:a1,codex:c1" && order[4] === "claude:a3", `status asc active..inactive, got ${order}`);
order = sorted(rows, { key: "audience", dir: "asc" }).map((r) => r.id);
check(order[0] === "claude:a1", "audience asc puts all_paired first");
check(order[1] === "claude:a2" && order[2] === "codex:c2", "within selected, more nodes first");
order = sorted(rows, { key: "cwd", dir: "asc" }).map((r) => r.id);
check(order[0] === "claude:a3" && order[1] === "codex:c2", `cwd asc puts empty cwd first, got ${order}`);
check(sorted(rows, { key: "bogus", dir: "asc" }).length === 5, "unknown key falls back, never throws");
// A row with an unparsable date sorts last when descending.
const bad = sorted([...rows, s("z", "claude", "idle", "none", { at: "not a date" })]);
check(bad[bad.length - 1].id === "claude:z", "bad date sorts last");

// nextSort: same key flips; a new key starts asc except lastSeenAt.
check(nextSort(DEFAULT_SORT, "lastSeenAt").dir === "asc", "flip desc->asc");
check(nextSort(DEFAULT_SORT, "status").dir === "asc", "new key starts asc");
check(nextSort({ key: "status", dir: "asc" }, "lastSeenAt").dir === "desc", "lastSeenAt starts desc");

// Prefs round-trip, and garbage in the store is dropped field by field.
const prefs = parsePrefs(serializePrefs({ filters: toggleChip(emptyFilters(), "status", "idle"), sort: { key: "cwd", dir: "asc" }, search: "x" }));
check(prefs.filters.status.has("idle") && prefs.sort.key === "cwd" && prefs.search === "x", "prefs round-trip");
const junk = parsePrefs('{"filters":{"status":["idle","<script>"]},"sort":{"key":"evil","dir":"up"},"search":5}');
check(junk.filters.status.size === 1 && junk.sort.key === DEFAULT_SORT.key && junk.search === "", "junk prefs are sanitised");
check(parsePrefs("not json").sort.key === DEFAULT_SORT.key, "unparsable prefs fall back");
check(parsePrefs(null).filters.provider.size === 0, "null prefs fall back");

if (failures.length) {
  console.error(failures.map((f) => `FAIL: ${f}`).join("\n"));
  process.exit(1);
}
console.log("ok sessions-filter");
