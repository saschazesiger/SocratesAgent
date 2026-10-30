// Native touch calibration. "show" places two magenta markers on the control marked data-qa-hit, `gap` css px
// apart diagonally around its centre, so the screenshot shows the control's centre where the device really
// draws it (the midpoint of the two), whatever the keyboard, zoom or browser bars do. The markers must be drawn
// above everything the page shows, the top layer included: an open modal <dialog> or a popover menu covers the
// rest of the page, markers there would not show and a control inside it could not be touched. Where the
// popover API exists each marker is a manual popover of its own, shown last, so it sits on top of the top layer;
// elsewhere (iOS 16) it goes into the open dialog the control is in. Its place is measured and corrected, so a
// transformed or scrolled container cannot shift it. It also notes which element the next touch reaches, in
// sessionStorage so a same-origin navigation keeps it. "hide" removes the markers and returns that note.
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
  var cx = r.left + r.width / 2;
  var cy = r.top + r.height / 2;
  var popover = typeof HTMLElement !== "undefined" && typeof HTMLElement.prototype.showPopover === "function";
  var dialog = target.closest ? target.closest("dialog[open]") : null;
  var layer = popover ? "top layer" : dialog ? "dialog" : "page";
  [-gap / 2, gap / 2].forEach(function (d) {
    var m = document.createElement("div");
    m.setAttribute("data-qa-marker", "");
    m.style.cssText = "position:" + (popover ? "fixed" : "absolute") + ";inset:auto;left:0;top:0;width:8px;height:8px;margin:0;padding:0;border:0;outline:0;box-shadow:none;transform:none;opacity:1;overflow:hidden;background:#ff00ff;z-index:2147483647;pointer-events:none;";
    if (popover) {
      m.setAttribute("popover", "manual");
      document.documentElement.appendChild(m);
      try { m.showPopover(); } catch (e) { layer = "page"; }
    } else (dialog || document.documentElement).appendChild(m);
    // Where left:0/top:0 put it, in the same viewport coordinates as the control's rect: the difference is the shift.
    var at = m.getBoundingClientRect();
    m.style.left = cx + d - at.left + "px";
    m.style.top = cy + d - at.top + "px";
  });
  try { sessionStorage.removeItem(KEY); } catch (e) { /* storage off: the hit stays unknown */ }
  var record = function (event) {
    document.removeEventListener("pointerdown", record, true);
    document.removeEventListener("touchstart", record, true);
    var hit = event.target && event.target.closest ? event.target : null;
    var noted = { on: !!(hit && hit.closest('[data-qa-hit="' + nonce + '"]')), by: hit ? qaDescribe(hit) : "", name: hit ? qaName(hit).slice(0, 60) : "", type: event.type };
    try { sessionStorage.setItem(KEY, JSON.stringify(noted)); } catch (e) { /* see above */ }
  };
  document.addEventListener("pointerdown", record, true);
  document.addEventListener("touchstart", record, true);
  return { placed: true, layer: layer };
}
