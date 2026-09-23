// Answer the window's own confirmation dialog the way an owner does: by
// pressing one of its two buttons.
//
// Every check here used to stub globalThis.confirm, and that stub is what hid
// the bug the dialog exists for. In the shipped macOS app window.confirm is
// answered "no" by WebKit without anything appearing on screen (Wails v2's
// WKUIDelegate has no runJavaScriptConfirmPanel), so a clear, an uninstall or
// a database change did nothing — while every check, answering confirm()
// itself, said they worked.
//
// So window.confirm, alert and prompt throw here, and a question is answered
// only through askConfirm's markup: when #confirm-modal loses `hidden`, the
// question is read back from #confirm-title and #confirm-body, handed to
// `decide`, and the matching button's onclick is called on the next
// microtask — after askConfirm has wired it, as a click would be.
//
// `decide(question)` answers true for 確定 and false for 取消; the question is
// the title and body joined by a newline.
export function answerConfirms(document, decide) {
  for (const name of ["confirm", "alert", "prompt"]) {
    globalThis[name] = () => {
      throw new Error(`window.${name} was called; it never shows in the macOS app — use askConfirm`);
    };
  }
  const modal = document.getElementById("confirm-modal");
  let className = modal.className;
  const hidden = (value) => /(^|\s)hidden(\s|$)/.test(value);
  Object.defineProperty(modal, "className", {
    configurable: true,
    get: () => className,
    set: (next) => {
      const opened = hidden(className) && !hidden(next);
      className = String(next);
      if (!opened) return;
      const question = `${document.getElementById("confirm-title").textContent}\n${document.getElementById("confirm-body").textContent}`;
      queueMicrotask(() => {
        const id = decide(question) ? "confirm-ok" : "confirm-cancel";
        document.getElementById(id).onclick?.({ target: document.getElementById(id) });
      });
    },
  });
}
