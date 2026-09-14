// The fake <select> has to lie about nothing that a form bug could hide behind.
//
// This exists because of a real miss: the shim used to report selectedIndex 0
// when its value matched no option, so `options[selectedIndex]` resolved to
// whatever happened to be first — a LAN address — where a browser resolves to
// the loopback placeholder and reports "". Two P1 defects in the settings form
// (allowLan being forced on, a non-default loopback port read as LAN) passed
// their tests on exactly that difference. A shim that lies is worse than no
// shim, so its select behaviour is pinned here rather than left to whichever
// test happens to lean on it.
//
//   node frontend/test/dom-shim-select.mjs

import { document } from "./dom-shim.mjs";

const failures = [];
const check = (cond, message) => { if (!cond) failures.push(message); };

const option = (value, text = value) => {
  const node = document.createElement("option");
  node.value = value;
  node.textContent = text;
  return node;
};

const select = document.createElement("select");
select.append(option("", "只在本機"), option("192.168.50.10:7463"), option("10.0.0.5:7463"));

// Untouched: the first option, as a browser reports for a select nobody set.
check(select.value === "", `untouched value = ${JSON.stringify(select.value)}, want ""`);
check(select.selectedIndex === 0, `untouched selectedIndex = ${select.selectedIndex}, want 0`);

// A value an option carries selects it.
select.value = "10.0.0.5:7463";
check(select.value === "10.0.0.5:7463", "a real option's value did not stick");
check(select.selectedIndex === 2, `selectedIndex = ${select.selectedIndex}, want 2`);

// A value NO option carries is dropped, exactly as HTMLSelectElement does — and
// selectedIndex is -1, never a fallback to the first entry.
select.value = "203.0.113.9:7463";
check(select.value === "", `an unknown value survived as ${JSON.stringify(select.value)}, want ""`);
check(select.selectedIndex === -1, `unknown value gave selectedIndex ${select.selectedIndex}, want -1`);
check(select.options[select.selectedIndex] === undefined,
  "options[selectedIndex] resolved to an option for a value no option carries");

// options reflects the children, and remove() drops one.
check(select.options.length === 3, `options.length = ${select.options.length}, want 3`);
select.remove(1);
check(select.options.length === 2 && select.options[1].value === "10.0.0.5:7463",
  "remove(index) did not drop that option");

// replaceChildren empties it, and an empty select reports -1 and "".
select.replaceChildren();
check(select.options.length === 0, "replaceChildren left options behind");
check(select.selectedIndex === -1, `an empty select gave selectedIndex ${select.selectedIndex}, want -1`);

// dataset survives on an option, which is where the form keeps the subnet and
// whether the address is private.
const tagged = option("122.122.0.7:7463");
tagged.dataset.private = "";
tagged.dataset.subnet = "122.122.0.0/16";
check(tagged.dataset.private === "" && tagged.dataset.subnet === "122.122.0.0/16",
  "dataset did not survive on an option");

if (failures.length > 0) {
  console.error(failures.join("\n"));
  process.exit(1);
}
console.log("dom shim: the fake select reports -1 and \"\" for a value no option carries");
