// A minimal DOM good enough for the table renderer, plus a serializer.
//
// `textContent` and `append` escape on serialization, while `innerHTML` keeps
// its input as raw markup — exactly how a browser treats them. That difference
// is what the render test measures.

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

  // Enough of a classList for the renderers: a set, so add/remove/toggle
  // compose the way they do in a browser. toggle keeps its previous observable
  // behaviour for a single class, which is all the table renderer uses.
  get classList() {
    const classes = () => new Set(String(this.className || "").split(/\s+/).filter(Boolean));
    const write = (set) => { this.className = [...set].join(" "); };
    return {
      add: (name) => { const set = classes(); set.add(name); write(set); },
      remove: (name) => { const set = classes(); set.delete(name); write(set); },
      contains: (name) => classes().has(name),
      toggle: (name, on) => {
        const set = classes();
        if (on === undefined ? set.has(name) : on === false) set.delete(name);
        else set.add(name);
        write(set);
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

export const document = {
  createElement: (tag) => new Node(tag),
  createDocumentFragment: () => new Fragment(),
  getElementById: (id) => {
    if (!byId.has(id)) byId.set(id, new Node("div"));
    return byId.get(id);
  },
};
