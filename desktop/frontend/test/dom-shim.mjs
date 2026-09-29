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
    // Listeners by event type, for the few things app.js listens for on an
    // element rather than through an on* property (a toast's pointer and
    // focus, which hold its countdown). A check fires one with dispatchEvent.
    this.listeners = {};
  }

  addEventListener(type, listener) {
    (this.listeners[type] ??= []).push(listener);
  }

  // No bubbling and no capture: the listener on this element runs, with the
  // event as given plus its type and target.
  dispatchEvent(event) {
    for (const listener of this.listeners[event.type] ?? []) listener({ target: this, ...event });
    return true;
  }

  // Whether other is this element or inside it, by the parent links append
  // keeps.
  contains(other) {
    for (let node = other; node; node = node.parentNode) if (node === this) return true;
    return false;
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

  // A setter without a getter answered undefined, so a check could only ever
  // assert what a title serialized to — not what it is. paintStatic writes
  // this one from index.html, and reading it back is how test/i18n.mjs knows
  // the write happened.
  get title() {
    return this.attrs.title ?? "";
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
    for (const kid of kids) {
      // An element lives in one place, as in a browser: appending it here takes
      // it out of wherever it was, which is what remove() below relies on.
      if (kid && typeof kid === "object") {
        if (kid.parentNode) kid.remove();
        kid.parentNode = this;
      }
      this.children.push(kid);
    }
  }

  // Takes this element out of its parent, as Element.remove() does. The toast
  // stack removes one toast at a time so the others keep their countdowns; a
  // shim without it could only rebuild the whole stack.
  //
  // remove(index) is the other remove: it drops one option, as
  // HTMLSelectElement's does. The two share a name in the DOM too, told apart
  // by whether an index was given.
  remove(index) {
    if (index !== undefined) {
      const option = this.options[index];
      if (!option) return;
      this.children = this.children.filter((child) => child !== option);
      return;
    }
    const parent = this.parentNode;
    if (!parent) return;
    parent.children = parent.children.filter((child) => child !== this);
    this.parentNode = null;
  }

  // Attributes other than class and title (role, aria-*), serialized with the
  // rest, so a check can read what a screen reader would be told.
  setAttribute(name, value) {
    this.attrs[name] = String(value);
  }

  getAttribute(name) {
    return Object.prototype.hasOwnProperty.call(this.attrs, name) ? this.attrs[name] : null;
  }

  replaceChildren(...kids) {
    for (const child of this.children) if (child && typeof child === "object") child.parentNode = null;
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
const markupSource = (() => {
  try {
    return fs.readFileSync(
      path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "index.html"), "utf8");
  } catch {
    // A shim that cannot find the markup is still usable; it is just back to
    // fabricating bare elements, which is what it did before.
    return "";
  }
})();

const initialClasses = (() => {
  const classes = new Map();
  {
    const markup = markupSource;
    const tag = /<[a-zA-Z][^>]*>/g;
    for (const [element] of markup.matchAll(tag)) {
      const id = /\sid="([^"]+)"/.exec(element);
      if (!id) continue;
      const className = /\sclass="([^"]*)"/.exec(element);
      classes.set(id[1], className ? className[1] : "");
    }
  }
  return classes;
})();

// The elements index.html keys for translation, as real nodes.
//
// paintStatic() walks querySelectorAll("[data-t]") and writes textContent, so
// a shim answering [] would make every assertion about the static text vacuous
// — the same blindness initialClasses exists for. An element that also carries
// an id has to be the very node getElementById hands back, or a check would
// read one object while the paint wrote to another. test/i18n.mjs counts these
// against index.html so a shim that quietly went back to [] is a failure.
const keyedElements = (markup) => {
  const found = { "data-t": [], "data-t-placeholder": [], "data-t-title": [] };
  const property = { "data-t": "t", "data-t-placeholder": "tPlaceholder", "data-t-title": "tTitle" };
  for (const [element] of markup.matchAll(/<[a-zA-Z][^>]*>/g)) {
    for (const attribute of Object.keys(found)) {
      const key = new RegExp(`\\s${attribute}="([^"]*)"`).exec(element);
      if (!key) continue;
      const id = /\sid="([^"]+)"/.exec(element);
      const node = id ? document.getElementById(id[1]) : new Node("span");
      node.dataset[property[attribute]] = key[1];
      found[attribute].push(node);
    }
  }
  return found;
};
let keyed = null;

// The radio groups index.html declares, as real nodes.
//
// The shim used to answer [] for every name selector, which meant the audience
// dialog's own "pick one of these" logic was never executed: openAudienceModal
// wrote its mode into nothing, selectedMode() read a null and answered 不公開,
// and every check that opened the dialog agreed with itself. Deleting the loop
// that resets the mode radios left the suite green.
//
// Only radios, and only by name: everything else the module queries for
// (thead th.sortable, the node-picker boxes) is built by the
// renderers under test or genuinely absent here, and answering those from the
// markup would hand back elements nothing in this shim can keep in step.
const radioGroups = (() => {
  let groups = null;
  return () => {
    if (groups) return groups;
    groups = new Map();
    for (const [tag] of markupSource.matchAll(/<input[^>]*>/g)) {
      const name = /\sname="([^"]+)"/.exec(tag);
      if (!name) continue;
      const id = /\sid="([^"]+)"/.exec(tag);
      const node = id ? document.getElementById(id[1]) : new Node("input");
      node.type = "radio";
      node.value = (/\svalue="([^"]*)"/.exec(tag) ?? ["", ""])[1];
      // The one the markup ships checked, which is what a freshly loaded page
      // has before any script runs.
      node.checked = /\schecked[\s/>]/.test(tag);
      if (!groups.has(name[1])) groups.set(name[1], []);
      groups.get(name[1]).push(node);
    }
    return groups;
  };
})();

// The three view tabs, as real nodes: the tag, the attributes and the
// data-view the markup gives each. The tabs are buttons now (2026-09-29, so
// the keyboard can reach them), and a check that they switch the view and say
// which one is selected needs the elements the wiring was handed — built once,
// so render() and the check read the same three.
const viewTabs = (() => {
  let tabs = null;
  return () => {
    if (tabs) return tabs;
    tabs = [];
    const nav = /<nav[^>]*id="view-switch"[^>]*>([\s\S]*?)<\/nav>/.exec(markupSource)?.[1] ?? "";
    for (const [tag] of nav.matchAll(/<[a-zA-Z][^>]*\sdata-view="[^"]*"[^>]*>/g)) {
      const node = new Node(/^<([a-zA-Z]+)/.exec(tag)[1].toLowerCase());
      node.dataset.view = /\sdata-view="([^"]*)"/.exec(tag)[1];
      node.className = (/\sclass="([^"]*)"/.exec(tag) ?? ["", ""])[1];
      for (const [, name, value] of tag.matchAll(/\s(role|aria-selected|tabindex|type)="([^"]*)"/g)) node.setAttribute(name, value);
      tabs.push(node);
    }
    return tabs;
  };
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
  // markup here for those to be found in. The translation selectors are the
  // exception — those are answered from index.html itself, above.
  querySelectorAll: (selector) => {
    const text = String(selector);
    const attribute = /^\[(data-t(?:-placeholder|-title)?)\]$/.exec(text);
    if (attribute) {
      keyed ??= keyedElements(markupSource);
      return keyed[attribute[1]];
    }
    if (text === "#view-switch [data-view]") return viewTabs();
    const radio = /^input\[name="([^"]+)"\](:checked)?$/.exec(text);
    if (radio) {
      const group = radioGroups().get(radio[1]) ?? [];
      return radio[2] ? group.filter((node) => node.checked) : group;
    }
    return [];
  },
  // And null for a single one, which is what "nothing is selected" looks like.
  // Returning undefined instead made every caller throw on the optional chain
  // that follows, which reads as a broken shim rather than an empty document.
  querySelector: (selector) => document.querySelectorAll(selector)[0] ?? null,
};
