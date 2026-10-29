// Shared helpers of the page scripts. Plain ES2017 that every tested browser parses (iOS 15 included);
// each script is sent as is, prefixed with this file, and must define `main`.
var QA_AD = '[id^="pw-"], [class*="pw-"], [id*="google_ads"], [id^="div-gpt-ad"], ins.adsbygoogle, [aria-label="Advertisement"], video[title="Advertisement"], [id*="ramp"], [class*="ramp-"]';
var QA_CONSENT = ".fc-consent-root";

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

function qaIsAd(el) {
  return !!(el && el.closest && el.closest(QA_AD));
}
