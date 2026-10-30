// Finds the control an action names (visible text or accessible name, optional role, a CSS selector alone
// or as the area the text is searched in, nth match), scrolls it into sight when it is not (the least scroll), checks what covers it, and marks
// it with data-qa-hit for WebDriver. Among equal names the one a person sees on top wins (an open menu's link
// before the same link in the footer).
var QA_INTERACTIVE = 'a[href], button, input:not([type="hidden"]), select, textarea, summary, label, [role="button"], [role="link"], [role="tab"], [role="menuitem"], [role="menuitemradio"], [role="menuitemcheckbox"], [role="option"], [role="checkbox"], [role="radio"], [role="switch"], [role="combobox"], [role="textbox"], [role="slider"], [tabindex]:not([tabindex="-1"]), [onclick]';

// How a control's name matches the wanted text: 2 exactly, 1 from the start of a word ("settings" in
// "Advanced settings", never "es" inside "acceso"), 0 not at all. Scripts without word spaces (Japanese) match anywhere.
function qaMatch(name, wanted) {
  if (name === wanted) return 2;
  for (var at = name.indexOf(wanted); at >= 0; at = name.indexOf(wanted, at + 1)) {
    if (at === 0 || !/[a-z0-9\u00c0-\u024f\u0370-\u03ff\u0400-\u04ff]/i.test(name.charAt(at - 1))) return 1;
  }
  return 0;
}

// In the viewport and not covered by anything else at its centre.
function qaOnTop(el) {
  var r = el.getBoundingClientRect();
  var x = r.left + r.width / 2;
  var y = r.top + r.height / 2;
  if (x < 0 || y < 0 || x > innerWidth || y > innerHeight) return false;
  var hit = document.elementFromPoint(x, y);
  return !!hit && (hit === el || el.contains(hit));
}

function main(query) {
  var norm = function (value) { return qaText(value).toLowerCase(); };
  var wanted = norm(query.text);
  var pool = [];
  if (query.css && !wanted) pool = Array.prototype.slice.call(document.querySelectorAll(query.css));
  else if (query.css) {
    // With a text the selector is the area to search: the controls it matches or contains.
    Array.prototype.forEach.call(document.querySelectorAll(query.css), function (scope) {
      if (scope.matches(QA_INTERACTIVE)) pool.push(scope);
      pool = pool.concat(Array.prototype.slice.call(scope.querySelectorAll(QA_INTERACTIVE)));
    });
  } else {
    pool = Array.prototype.slice.call(document.querySelectorAll(query.role === "heading" ? "h1, h2, h3, h4, h5, h6, [role=heading]" : QA_INTERACTIVE));
    if (query.any) pool = pool.concat(Array.prototype.slice.call(document.querySelectorAll("p, span, div, li, td, dt, dd, h1, h2, h3, h4, h5, h6")));
  }
  var matches = [];
  for (var i = 0; i < pool.length; i++) {
    var el = pool[i];
    if (pool.indexOf(el) !== i) continue;
    if (!qaVisible(el)) continue;
    if (query.role && query.role !== "heading" && qaRole(el) !== query.role) continue;
    var name = qaName(el);
    var exact = true;
    if (wanted) {
      // The accessible name or the visible text: a person taps what they read ("ES" on a language switcher named "Choose language").
      var shown = qaText(el.innerText || "");
      var match = Math.max(qaMatch(norm(name), wanted), shown && shown !== name ? qaMatch(norm(shown), wanted) : 0);
      exact = match === 2;
      if (!match || (query.exact && !exact)) continue;
    }
    matches.push({ el: el, name: name, exact: exact, area: el.offsetWidth * el.offsetHeight, top: qaOnTop(el) });
  }
  // Keep the innermost of nested matches with the same name (a label around its input, a link around its button).
  matches = matches.filter(function (m) {
    return !matches.some(function (o) { return o !== m && m.el.contains(o.el) && norm(o.name) === norm(m.name); });
  });
  // The listed order (and --nth) does not depend on the scroll position; without --nth the best one on top wins.
  matches.sort(function (a, b) { return (b.exact ? 1 : 0) - (a.exact ? 1 : 0) || a.area - b.area; });
  var candidates = matches.slice(0, 8).map(function (m) { return { name: m.name.slice(0, 80), role: qaRole(m.el), tag: qaDescribe(m.el), exact: m.exact }; });
  var pick = typeof query.nth === "number" ? matches[query.nth] : matches.filter(function (m) { return m.exact === matches[0].exact && m.top; })[0] || matches[0];
  if (!pick) return { found: false, count: matches.length, candidates: candidates };
  var target = pick.el;
  Array.prototype.forEach.call(document.querySelectorAll("[data-qa-hit]"), function (node) { node.removeAttribute("data-qa-hit"); });
  var nonce = "qa" + Math.random().toString(36).slice(2, 10);
  target.setAttribute("data-qa-hit", nonce);
  // On phones the visible part can be smaller than the layout viewport (on-screen keyboard, zoom).
  var vv = window.visualViewport;
  // A control already in sight stays where it is. Centring it moved what a person sees: with Android's keyboard
  // up only the visual viewport shrinks, and scrolling to the centre slid the visible part into empty page and
  // the key bar sideways. Otherwise the least scroll that brings it into sight (instant, also on pages with
  // smooth scrolling: the rect below must be where the control stays).
  var seen = target.getBoundingClientRect();
  var top = vv ? vv.offsetTop : 0;
  var left = vv ? vv.offsetLeft : 0;
  var height = vv ? vv.height : innerHeight;
  var width = vv ? vv.width : innerWidth;
  var inside = seen.top >= top && seen.left >= left && seen.bottom <= top + height && seen.right <= left + width;
  var cxSeen = seen.left + seen.width / 2;
  var cySeen = seen.top + seen.height / 2;
  var bigAndCentred = (seen.height > height || seen.width > width) && cySeen >= top && cySeen <= top + height && cxSeen >= left && cxSeen <= left + width;
  if (!inside && !bigAndCentred) target.scrollIntoView({ block: "nearest", inline: "nearest", behavior: "instant" });
  if (vv) {
    var box = target.getBoundingClientRect();
    var middle = box.top + box.height / 2;
    if (middle < vv.offsetTop || middle > vv.offsetTop + vv.height) window.scrollBy({ top: middle - (vv.offsetTop + vv.height / 2), behavior: "instant" });
  }
  var r = target.getBoundingClientRect();
  var cx = Math.min(Math.max(r.left + r.width / 2, 1), innerWidth - 1);
  var cy = Math.min(Math.max(r.top + r.height / 2, 1), innerHeight - 1);
  var hit = document.elementFromPoint(cx, cy);
  var covered = null;
  if (hit && hit !== target && !target.contains(hit) && !hit.contains(target)) {
    covered = { by: qaDescribe(hit) };
  }
  return {
    found: true,
    nonce: nonce,
    name: pick.name.slice(0, 120),
    index: matches.indexOf(pick),
    role: qaRole(target),
    tag: qaDescribe(target),
    exact: pick.exact,
    count: matches.length,
    candidates: matches.length > 1 ? candidates : [],
    rect: { x: r.left, y: r.top, width: r.width, height: r.height },
    viewport: { width: innerWidth, height: innerHeight },
    disabled: !!(target.disabled || target.getAttribute("aria-disabled") === "true"),
    covered: covered,
  };
}
