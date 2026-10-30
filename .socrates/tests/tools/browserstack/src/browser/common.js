// Shared helpers of the page scripts. Plain ES2017 that every tested browser parses (iOS 16 included);
// each script is sent as is, prefixed with this file, and must define `main`.

function qaVisible(el) {
  if (!el || !el.getBoundingClientRect) return false;
  var r = el.getBoundingClientRect();
  if (r.width < 1 || r.height < 1) return false;
  var s = getComputedStyle(el);
  return s.visibility !== "hidden" && s.display !== "none" && parseFloat(s.opacity || "1") > 0.05;
}

function qaText(value) {
  return String(value || "").replace(/\s+/g, " ").trim();
}

function qaName(el) {
  var aria = el.getAttribute("aria-label");
  if (aria) return qaText(aria);
  var by = el.getAttribute("aria-labelledby");
  if (by) {
    var parts = by.split(/\s+/).map(function (id) { var n = document.getElementById(id); return n ? n.textContent : ""; });
    if (qaText(parts.join(" "))) return qaText(parts.join(" "));
  }
  var tag = el.tagName.toLowerCase();
  if (tag === "input" || tag === "textarea" || tag === "select") {
    if (el.labels && el.labels.length) return qaText(el.labels[0].textContent);
    if (el.placeholder) return qaText(el.placeholder);
    if (el.type === "submit" || el.type === "button") return qaText(el.value);
  }
  var text = qaText(el.innerText || el.textContent);
  if (text) return text;
  var img = el.querySelector && el.querySelector("img[alt], svg[aria-label]");
  if (img) return qaText(img.getAttribute("alt") || img.getAttribute("aria-label"));
  return qaText(el.getAttribute("title"));
}

function qaRole(el) {
  var role = el.getAttribute("role");
  if (role) return role;
  var tag = el.tagName.toLowerCase();
  if (tag === "a") return "link";
  if (tag === "button" || tag === "summary") return "button";
  if (tag === "select") return "combobox";
  if (tag === "textarea") return "textbox";
  if (tag === "input") {
    var t = (el.type || "text").toLowerCase();
    if (t === "checkbox" || t === "radio") return t;
    if (t === "submit" || t === "button" || t === "reset") return "button";
    return "textbox";
  }
  if (/^h[1-6]$/.test(tag)) return "heading";
  return tag;
}

function qaDescribe(el) {
  if (!el) return "";
  var id = el.id ? "#" + el.id : "";
  var cls = typeof el.className === "string" && el.className ? "." + el.className.trim().split(/\s+/).slice(0, 3).join(".") : "";
  return el.tagName.toLowerCase() + id + cls;
}

// What a person's swipe in the middle of the visible screen scrolls, innermost first: the boxes there that
// scroll (a dialog's body, an app's main column), then the page (document.scrollingElement). Many apps keep the
// page itself still and scroll a column, so window.scrollY alone stays 0.
function qaScrollChain() {
  var page = document.scrollingElement || document.documentElement;
  var vv = window.visualViewport;
  var x = vv ? vv.offsetLeft + vv.width / 2 : innerWidth / 2;
  var y = vv ? vv.offsetTop + vv.height / 2 : innerHeight / 2;
  var chain = [];
  for (var el = document.elementFromPoint(x, y); el && el !== page; el = el.parentElement) {
    var overflow = getComputedStyle(el).overflowY;
    if (/(auto|scroll|overlay)/.test(overflow) && el.scrollHeight > el.clientHeight + 1) chain.push(el);
  }
  chain.push(page);
  return chain;
}

// A page locked with overflow hidden (behind a modal dialog, in an app shell) moves for a script, not for a finger.
function qaPageLocked() {
  return [document.documentElement, document.body].some(function (node) { return node && /(hidden|clip)/.test(getComputedStyle(node).overflowY); });
}

// A scrolling box's position: where it is, how far it can go, how much of it shows.
function qaScrollState(el) {
  var page = el === (document.scrollingElement || document.documentElement);
  var visible = page ? (window.visualViewport ? window.visualViewport.height : innerHeight) : el.clientHeight;
  var height = el.scrollHeight;
  var top = page ? scrollY : el.scrollTop;
  return { box: page ? "the page" : qaDescribe(el), top: Math.round(top), max: Math.max(0, Math.round(height - (page ? innerHeight : el.clientHeight))), visible: Math.round(visible) };
}
