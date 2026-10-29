// Small page actions: scroll by a share of the viewport or to the top or bottom, and the environment facts.
function main(command, value) {
  if (command === "scroll") {
    if (value === "top") window.scrollTo(0, 0);
    else if (value === "bottom") window.scrollTo(0, document.documentElement.scrollHeight);
    else window.scrollBy(0, (value === "up" ? -1 : 1) * Math.round(innerHeight * 0.8));
    return { scrollY: Math.round(scrollY) };
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
