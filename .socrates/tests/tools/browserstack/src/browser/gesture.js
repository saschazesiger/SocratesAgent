// Facts and helpers of the gestures (swipe, hold, drag, wheel, double tap). "resolve" finds the page point a
// gesture starts at (the control marked data-qa-hit, or the viewport, and `at` inside it). "probe" notes what
// can move at a point: the page's scroll and zoom, and every box that scrolls among the ancestors of the
// element there, so the gesture can say what it moved. "wheel" is the stand-in for a browser whose driver
// has no wheel: wheel events dispatched by script on the element at the point (a script's wheel moves what
// listens for wheel events, such as a terminal, but no box the browser scrolls itself).
function main(command, a, b, c, d) {
  if (command === "resolve") {
    var target = a ? document.querySelector('[data-qa-hit="' + a + '"]') : null;
    if (a && !target) return { found: false };
    var p = qaPointIn(target, b);
    var under = document.elementFromPoint(p.x, p.y);
    var vv = window.visualViewport;
    return { found: true, point: { x: Math.round(p.x), y: Math.round(p.y) }, inView: p.inView, under: under ? qaDescribe(under) : "", viewport: { x: vv ? vv.offsetLeft : 0, y: vv ? vv.offsetTop : 0, width: vv ? vv.width : innerWidth, height: vv ? vv.height : innerHeight }, dpr: String(window.devicePixelRatio || 1) };
  }
  if (command === "probe") {
    var page = document.scrollingElement || document.documentElement;
    var boxes = [];
    for (var el = document.elementFromPoint(a, b); el && el !== page && el !== document.body; el = el.parentElement) {
      var s = getComputedStyle(el);
      var scrollsY = /(auto|scroll|overlay)/.test(s.overflowY) && el.scrollHeight > el.clientHeight + 1;
      var scrollsX = /(auto|scroll|overlay)/.test(s.overflowX) && el.scrollWidth > el.clientWidth + 1;
      if (scrollsY || scrollsX) boxes.push({ box: qaDescribe(el), top: Math.round(el.scrollTop), left: Math.round(el.scrollLeft), maxTop: Math.max(0, el.scrollHeight - el.clientHeight) });
    }
    var vv2 = window.visualViewport;
    return { page: { top: Math.round(scrollY), left: Math.round(scrollX) }, zoom: vv2 ? String(Math.round(vv2.scale * 100) / 100) : "1", boxes: boxes, selection: String(window.getSelection ? window.getSelection() : "").slice(0, 120) };
  }
  if (command === "wheel") {
    // a, b: the point; c, d: deltaX, deltaY in CSS px, sent in notches of at most 100 px.
    var at = document.elementFromPoint(a, b);
    if (!at) return { sent: 0, target: "" };
    var notches = Math.max(1, Math.round(Math.max(Math.abs(c), Math.abs(d)) / 100));
    var taken = false;
    for (var i = 0; i < notches; i++) {
      var event = new WheelEvent("wheel", { deltaX: c / notches, deltaY: d / notches, deltaMode: 0, clientX: a, clientY: b, bubbles: true, cancelable: true, composed: true });
      at.dispatchEvent(event);
      if (event.defaultPrevented) taken = true;
    }
    return { sent: notches, target: qaDescribe(at), taken: taken };
  }
  return null;
}
