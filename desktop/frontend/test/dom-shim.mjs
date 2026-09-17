import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// A minimal DOM good enough for the table renderer, plus a serializer.
//
// `textContent` and `append` escape on serialization, while `innerHTML` keeps
// its input as raw markup — exactly how a browser treats them. That difference
// is what the render test measures.

// The language these checks are written in.
//
// Every check here asserts the window's actual sentences, and they were written
// against the Traditional Chinese ones. app.js picks its language from
// navigator.language (src/i18n/index.js), and node has no navigator at all — so
// without this the whole suite would silently move to English and every prose
// assertion in it would have to be rewritten to say the same thing twice.
//
// The English half is not left untested: test/i18n.mjs overrides this and boots
// the window in en-US, which is what a machine outside a zh locale gets.
// defineProperty, not assignment: node ships its own navigator as a getter-only
// property, so `globalThis.navigator = …` throws there.
export function useLocale(locale) {
  Object.defineProperty(globalThis, "navigator", {
    value: { language: locale },
    configurable: true,
    writable: true,
  });
}
useLocale("zh-TW");

// Which element has the keyboard, for the one guard that asks. A browser
// answers <body> when nothing is focused; null is this shim's stand-in for
// "nothing", since there is no body here to hand back.
let focused = null;

class Node {
  constructor(tag) {
    // Inline style writes land here, as on a real element; nothing reads it.
    this.style = {};
    // What a <select> carries. Kept on every node rather than only on selects,
    // because the shim never learns an element's tag from the markup — the code
    // under test builds options with createElement("option") and appends them,
    // and `options` has to be the same list `append` wrote to.
    this.dataset = {};
    this.tagName = tag;
    this.children = [];
    this.attrs = {};
    this.className = "";
    this._text = "";
    this._raw = undefined;
  }

  set textContent(value) {
    this._text = String(value);
    this.children = [];
  }

  get textContent() {
    return this._text + this.children.map((c) => (typeof c === "string" ? c : c.textContent)).join("");
  }

  set innerHTML(value) {
    this._raw = String(value);
    this.children = [];
    this._text = "";
  }

  set title(value) {
    this.attrs.title = String(value);
  }

  // Enough of focus for a test that needs to say "the owner is in this field".
  // No focus or blur events, no tabindex rules, no scrolling into view — the
  // code under test reads document.activeElement and nothing else.
  focus() {
    focused = this;
  }

  blur() {
    if (focused === this) focused = null;
  }

  append(...kids) {
    for (const kid of kids) this.children.push(kid);
  }

  replaceChildren(...kids) {
    this.children = [];
    this._text = "";
    this._raw = undefined;
    this.append(...kids);
  }

  // A <select>'s own view of its children. Reading it, rather than storing a
  // second list, keeps append/remove and options from drifting apart.
  get options() {
    return this.children.filter((child) => child && child.tagName === "option");
  }

  // -1 when nothing matches, as a browser reports for a select whose value was
  // set to a string no option carries. Falling back to 0 here would make
  // `options[selectedIndex]` resolve to whatever happens to be first, so a test
  // would read a LAN option where a browser reads the loopback placeholder —
  // and a form bug that turns on LAN access would pass.
  get selectedIndex() {
    const options = this.options;
    if (this._value === undefined) return options.length > 0 ? 0 : -1;
    return options.findIndex((option) => option.value === this._value);
  }

  // remove(index) drops one option, as HTMLSelectElement does.
  remove(index) {
    const option = this.options[index];
    if (!option) return;
    this.children = this.children.filter((child) => child !== option);
  }

  get value() {
    if (this._value === undefined) {
      const options = this.options;
      return options.length > 0 ? options[0].value ?? "" : "";
    }
    // A real select drops a value no option carries and reports "".
    const options = this.options;
    if (options.length > 0 && !options.some((option) => option.value === this._value)) return "";
    return this._value;
  }

  set value(next) {
    this._value = String(next);
  }

  querySelector() {
    const found = this.children.find((c) => c && c.tagName === "input");
    if (found) return found;
    return this._raw !== undefined ? new Node("input") : null;
  }

  // Enough of a classList for the renderers: a set behind add, remove,
  // contains and toggle. Not a browser's — it does not reject a name with a
  // space in it, and nothing here needs `replace` or iteration — but toggle
  // follows the specified rule, so a falsy second argument removes rather than
  // adds. Getting that backwards would let a test pass over code that leaves a
  // panel visible when it should be hidden.
  get classList() {
    const classes = () => new Set(String(this.className || "").split(/\s+/).filter(Boolean));
    const write = (set) => { this.className = [...set].join(" "); };
    return {
      add: (name) => { const set = classes(); set.add(name); write(set); },
      remove: (name) => { const set = classes(); set.delete(name); write(set); },
      contains: (name) => classes().has(name),
      toggle: (name, on) => {
        const set = classes();
        const wanted = on === undefined ? !set.has(name) : Boolean(on);
        if (wanted) set.add(name);
        else set.delete(name);
        write(set);
        return wanted;
      },
    };
  }

  serialize() {
    const esc = (s) =>
      String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
    const attrs = Object.entries(this.attrs).map(([k, v]) => ` ${k}="${esc(v)}"`).join("");
    const cls = this.className ? ` class="${esc(this.className)}"` : "";
    const inner =
      (this._raw ?? "") +
      esc(this._text) +
      this.children.map((c) => (typeof c === "string" ? esc(c) : c.serialize())).join("");
    return `<${this.tagName}${cls}${attrs}>${inner}</${this.tagName}>`;
  }
}

class Fragment extends Node {
  constructor() {
    super("#fragment");
  }
  serialize() {
    return this.children.map((c) => c.serialize()).join("");
  }
}

const byId = new Map();

// The classes each id actually carries in index.html.
//
// Without this every fabricated element starts with className "", so the
// `hidden` class the real markup uses never exists — and an assertion that a
// panel was un-hidden passes whether or not anything un-hid it. Measured:
// deleting the classList.remove("hidden") that opens the pairing dialog left
// the whole suite green, so a click could have opened nothing.
const initialClasses = (() => {
  const classes = new Map();
  try {
    const markup = fs.readFileSync(
      path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "index.html"), "utf8");
    const tag = /<[a-zA-Z][^>]*>/g;
    for (const [element] of markup.matchAll(tag)) {
      const id = /\sid="([^"]+)"/.exec(element);
      if (!id) continue;
      const className = /\sclass="([^"]*)"/.exec(element);
      classes.set(id[1], className ? className[1] : "");
    }
  } catch {
    // A shim that cannot find the markup is still usable; it is just back to
    // fabricating bare elements, which is what it did before.
  }
  return classes;
})();

export const document = {
  get activeElement() {
    return focused;
  },
  createElement: (tag) => new Node(tag),
  createDocumentFragment: () => new Fragment(),
  getElementById: (id) => {
    if (!byId.has(id)) {
      const node = new Node("div");
      node.className = initialClasses.get(id) ?? "";
      byId.set(id, node);
    }
    return byId.get(id);
  },
  // The module's wiring queries for the view switch and the audience radios.
  // Empty is right for a test that drives the renderers directly: there is no
  // markup here for those to be found in.
  querySelectorAll: () => [],
  // And null for a single one, which is what "nothing is selected" looks like.
  // Returning undefined instead made every caller throw on the optional chain
  // that follows, which reads as a broken shim rather than an empty document.
  querySelector: () => null,
};
