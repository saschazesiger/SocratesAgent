// Installs (once per document) a collector of page errors, rejections and console errors and warnings;
// returns and clears what it gathered since the last call.
function main() {
  if (!window.__qa) {
    var store = { entries: [], installedAt: Date.now() };
    window.__qa = store;
    var push = function (kind, message) {
      if (store.entries.length < 200) store.entries.push({ kind: kind, message: String(message).slice(0, 500), at: Date.now() });
    };
    window.addEventListener("error", function (e) { push("error", (e.message || "error") + (e.filename ? " @ " + e.filename.split("/").pop() + ":" + e.lineno : "")); });
    window.addEventListener("unhandledrejection", function (e) { push("rejection", e.reason && e.reason.message ? e.reason.message : e.reason); });
    ["error", "warn"].forEach(function (level) {
      var original = console[level];
      console[level] = function () {
        push("console." + level, Array.prototype.map.call(arguments, function (a) { try { return typeof a === "string" ? a : JSON.stringify(a); } catch (_) { return String(a); } }).join(" "));
        return original.apply(console, arguments);
      };
    });
    return { installed: true, entries: [] };
  }
  var entries = window.__qa.entries;
  window.__qa.entries = [];
  return { installed: false, entries: entries };
}
