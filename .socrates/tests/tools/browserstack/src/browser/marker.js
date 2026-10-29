// Native touch calibration. "show" places two magenta markers on the control marked data-qa-hit, `gap` css px
// apart diagonally around its centre and positioned with the page content, so the screenshot shows the
// control's centre where the device really draws it (the midpoint of the two), whatever the keyboard, zoom or
// browser bars do. It also notes which element the next touch reaches, in sessionStorage so a same-origin
// navigation keeps it. "hide" removes the markers and returns that note.
function main(command, gap, nonce) {
  var old = document.querySelectorAll("[data-qa-marker]");
  for (var i = 0; i < old.length; i++) old[i].remove();
  var KEY = "qa-native-hit";
  if (command !== "show") {
    var note = null;
    try { note = JSON.parse(sessionStorage.getItem(KEY) || "null"); sessionStorage.removeItem(KEY); } catch (e) { note = null; }
    return note;
  }
  var target = document.querySelector('[data-qa-hit="' + nonce + '"]');
  if (!target) return { placed: false };
  var r = target.getBoundingClientRect();
  var cx = r.left + r.width / 2 + scrollX;
  var cy = r.top + r.height / 2 + scrollY;
  [-gap / 2, gap / 2].forEach(function (d) {
    var m = document.createElement("div");
    m.setAttribute("data-qa-marker", "");
    m.style.cssText = "position:absolute;left:" + (cx + d) + "px;top:" + (cy + d) + "px;width:8px;height:8px;margin:0;background:#ff00ff;z-index:2147483647;pointer-events:none;";
    document.documentElement.appendChild(m);
  });
  try { sessionStorage.removeItem(KEY); } catch (e) { /* storage off: the hit stays unknown */ }
  var record = function (event) {
    document.removeEventListener("pointerdown", record, true);
    document.removeEventListener("touchstart", record, true);
    var hit = event.target && event.target.closest ? event.target : null;
    var noted = { on: !!(hit && hit.closest('[data-qa-hit="' + nonce + '"]')), by: hit ? qaDescribe(hit) : "", name: hit ? qaName(hit).slice(0, 60) : "" };
    try { sessionStorage.setItem(KEY, JSON.stringify(noted)); } catch (e) { /* see above */ }
  };
  document.addEventListener("pointerdown", record, true);
  document.addEventListener("touchstart", record, true);
  return { placed: true };
}
