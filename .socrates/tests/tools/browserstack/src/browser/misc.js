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
  if (command === "focus") {
    var el = document.querySelector('[data-qa-hit="' + value + '"]');
    if (el) el.focus();
    return { focused: !!el && document.activeElement === el };
  }
  return null;
}
