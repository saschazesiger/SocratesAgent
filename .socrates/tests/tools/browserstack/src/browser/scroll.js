// Scrolls as a person's swipe in the middle of the screen would: the innermost box there that can still move
// that way (qaScrollChain: a dialog's body, the app's main column, then the page), by most of what it shows, or
// to its top or bottom. Resolves once the position stopped changing (smooth scrolling included).
function main(where) {
  var down = where === "down" || where === "bottom";
  var chain = qaScrollChain();
  var page = document.scrollingElement || document.documentElement;
  var position = function (el) { return el === page ? scrollY : el.scrollTop; };
  var moveTo = function (el, top) {
    var scroller = el === page ? window : el;
    try { scroller.scrollTo({ top: top, behavior: "instant" }); } catch (e) { if (el === page) window.scrollTo(0, top); else el.scrollTop = top; }
  };
  for (var i = 0; i < chain.length; i++) {
    var el = chain[i];
    var state = qaScrollState(el);
    var before = position(el);
    if ((el === page && qaPageLocked()) || (down ? before >= state.max - 1 : before <= 1)) continue;
    var goal = where === "top" ? 0 : where === "bottom" ? state.max : before + (down ? 1 : -1) * Math.round(state.visible * 0.8);
    moveTo(el, Math.max(0, Math.min(state.max, goal)));
    var from = Math.round(before);
    return qaScrollRest(el, position).then(function (box) {
      var after = qaScrollState(box);
      after.from = from;
      after.moved = after.top !== from;
      return after;
    });
  }
  // Nothing can move that way: every box is at its end, or the page is locked.
  var first = qaScrollState(chain[0]);
  first.moved = false;
  first.from = first.top;
  first.locked = chain.length === 1 && qaPageLocked();
  return Promise.resolve(first);
}

// Resolves with the box once its position held still for 150 ms (at most 1.5 s).
function qaScrollRest(el, position) {
  return new Promise(function (resolve) {
    var start = Date.now();
    var last = position(el);
    var still = Date.now();
    (function tick() {
      var now = position(el);
      if (now !== last) { last = now; still = Date.now(); }
      if (Date.now() - still >= 150 || Date.now() - start >= 1500) resolve(el);
      else setTimeout(tick, 50);
    })();
  });
}
