// Waits until the page is loaded and quiet: no DOM change outside ads for quietMs and no visible skeleton or busy region.
function main(quietMs, maxMs) {
  return new Promise(function (resolve) {
    var start = Date.now();
    var last = Date.now();
    var observer = new MutationObserver(function (records) {
      for (var i = 0; i < records.length; i++) {
        var node = records[i].target.nodeType === 1 ? records[i].target : records[i].target.parentElement;
        if (!qaIsAd(node) && !(node && node.closest && node.closest(QA_CONSENT))) { last = Date.now(); return; }
      }
    });
    observer.observe(document.documentElement, { subtree: true, childList: true, attributes: true, characterData: true });
    function busy() {
      var out = [];
      var nodes = document.querySelectorAll('[aria-busy="true"], .skeleton, .animate-pulse');
      for (var i = 0; i < nodes.length && out.length < 5; i++) {
        var r = nodes[i].getBoundingClientRect();
        if (qaVisible(nodes[i]) && r.bottom > 0 && r.top < innerHeight && !qaIsAd(nodes[i])) out.push(qaDescribe(nodes[i]));
      }
      return out;
    }
    function tick() {
      var now = Date.now();
      var waiting = busy();
      var quiet = document.readyState === "complete" && now - last >= quietMs && waiting.length === 0;
      if (quiet || now - start >= maxMs) {
        observer.disconnect();
        resolve({ ms: now - start, settled: quiet, busy: waiting, readyState: document.readyState });
      } else setTimeout(tick, 150);
    }
    tick();
  });
}
