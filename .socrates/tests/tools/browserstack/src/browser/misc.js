// Small page actions and facts: the environment, the focus, the visible height (the on-screen keyboard takes
// it), and moving through the history from the page.
function main(command, value) {
  if (command === "viewport") {
    var vv = window.visualViewport;
    var active = document.activeElement;
    return { visible: Math.round(vv ? vv.height * vv.scale : innerHeight), window: innerHeight, active: active ? qaDescribe(active) : "" };
  }
  if (command === "history") {
    // A mark on this document: when it is gone, the history step loaded another one.
    var mark = "qa" + Math.random().toString(36).slice(2, 10);
    window.__qaHistoryMark = mark;
    history.go(value);
    return mark;
  }
  if (command === "marked") return window.__qaHistoryMark === value;
  if (command === "assign") {
    location.href = value;
    return true;
  }
  if (command === "env") {
    return { ua: navigator.userAgent, width: innerWidth, height: innerHeight, dpr: devicePixelRatio, lang: navigator.language, timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone, touch: navigator.maxTouchPoints };
  }
  if (command === "focused") {
    var active = document.activeElement;
    return { focused: !!active && active.getAttribute("data-qa-hit") === value, active: active ? qaDescribe(active) : "" };
  }
  if (command === "blur") {
    var b = document.querySelector('[data-qa-hit="' + value + '"]');
    if (b) b.blur();
    return { blurred: !!b && document.activeElement !== b };
  }
  if (command === "mark-active") {
    var focused = document.activeElement;
    if (!focused || focused === document.body) return null;
    var nonce = "qa" + Math.random().toString(36).slice(2, 10);
    focused.setAttribute("data-qa-hit", nonce);
    return nonce;
  }
  if (command === "select-all" || command === "value" || command === "caret-end") {
    var field = document.querySelector('[data-qa-hit="' + value + '"]');
    if (!field) return null;
    var editable = field.isContentEditable;
    var text = editable ? field.textContent || "" : String(field.value || "");
    if (command === "value") return { length: text.length, value: text.slice(0, 80) };
    var selected = false;
    if (editable) {
      var range = document.createRange();
      range.selectNodeContents(field);
      if (command === "caret-end") range.collapse(false);
      var sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
      selected = true;
    } else {
      try {
        if (command === "caret-end") field.setSelectionRange(text.length, text.length);
        else field.select();
        selected = command === "caret-end" || field.selectionEnd - field.selectionStart === text.length;
      } catch (e) {
        try { field.select(); } catch (e2) { /* a field without a selection */ }
      }
    }
    return { length: text.length, selected: selected, value: text.slice(0, 80) };
  }
  if (command === "field-info") {
    var f = document.querySelector('[data-qa-hit="' + value + '"]');
    if (!f) return null;
    var now = new Date();
    return { tag: f.tagName.toLowerCase(), type: String(f.type || "").toLowerCase(), value: String(f.value || ""), min: f.min || "", max: f.max || "", now: { year: now.getFullYear(), month: now.getMonth() + 1, day: now.getDate(), hour: now.getHours(), minute: now.getMinutes(), second: now.getSeconds() } };
  }
  if (command === "set-value") {
    // The stand-in for a picker the driver cannot use: the value set as the browser sets it when a person picks
    // (the element's own value setter), then the input and change events that choice sends.
    var g = document.querySelector('[data-qa-hit="' + value.nonce + '"]');
    if (!g) return null;
    var setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set;
    setter.call(g, value.value);
    g.dispatchEvent(new Event("input", { bubbles: true }));
    g.dispatchEvent(new Event("change", { bubbles: true }));
    return String(g.value || "");
  }
  if (command === "focus") {
    var el = document.querySelector('[data-qa-hit="' + value + '"]');
    if (el) el.focus();
    return { focused: !!el && document.activeElement === el };
  }
  return null;
}
