import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// A minimal DOM good enough for the table renderer, plus a serializer.
//
// `textContent` and `append` escape on serialization, while `innerHTML` keeps
// its input as raw markup — exactly how a browser treats them. That difference
// is what the render test measures.

// Which element has the keyboard, for the one guard that asks. A browser
// answers <body> when nothing is focused; null is this shim's stand-in for
// "nothing", since there is no body here to hand back.
let focused = null;

class Node {
  constructor(tag) {
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
