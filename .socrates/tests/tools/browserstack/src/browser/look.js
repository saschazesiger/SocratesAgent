// A compact description of what is on screen: title, headings, controls in the viewport, notices, dialogs.
function main() {
  var inView = function (el) { var r = el.getBoundingClientRect(); return r.bottom > 0 && r.top < innerHeight && r.right > 0 && r.left < innerWidth; };
  var pick = function (selector, limit, map) {
    var out = [];
    var nodes = document.querySelectorAll(selector);
    for (var i = 0; i < nodes.length && out.length < limit; i++) if (qaVisible(nodes[i]) && inView(nodes[i])) out.push(map(nodes[i]));
    return out;
  };
  var controls = pick('a[href], button, input:not([type="hidden"]), select, textarea, summary, [role="button"], [role="link"], [role="tab"], [role="menuitem"], [role="checkbox"], [role="switch"], [role="option"], [role="combobox"]', 60, function (el) {
    var item = { role: qaRole(el), name: qaName(el).slice(0, 60) };
    if (el.disabled || el.getAttribute("aria-disabled") === "true") item.disabled = true;
    if (el.checked || el.getAttribute("aria-checked") === "true" || el.getAttribute("aria-pressed") === "true" || el.getAttribute("aria-selected") === "true") item.on = true;
    if (el.getAttribute("aria-expanded")) item.expanded = el.getAttribute("aria-expanded") === "true";
    if ((el.tagName === "INPUT" || el.tagName === "TEXTAREA") && el.type !== "checkbox" && el.type !== "radio") item.value = el.type === "password" ? (el.value ? "(hidden)" : "") : String(el.value).slice(0, 60);
    return item;
  });
  return {
    title: document.title,
    path: (location.pathname + location.search + location.hash).replace(/([?&#](?:token|auth_token|code|key|auth|state|sig|signature|secret|password)=)[^&#]*/gi, "$1…"),
    lang: document.documentElement.lang,
    viewport: { width: innerWidth, height: innerHeight, scrollY: Math.round(scrollY), pageHeight: document.documentElement.scrollHeight },
    headings: pick("h1, h2, h3, [role=heading]", 12, function (el) { return qaText(el.innerText).slice(0, 80); }),
    controls: controls,
    notices: pick('[role="alert"], [role="status"], [aria-live="assertive"], [aria-live="polite"]', 8, function (el) { return qaText(el.innerText).slice(0, 160); }).filter(Boolean),
    dialogs: pick('[role="dialog"], [role="alertdialog"], dialog[open]', 4, function (el) { return qaName(el).slice(0, 120); }),
  };
}
