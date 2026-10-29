import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { Evidence, evidenceLine, type Action } from "../src/evidence";

const lines = (dir: string) => readFileSync(join(dir, "actions.jsonl"), "utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line) as Record<string, unknown>);

function action(evidence: Evidence, name: string, extra: Partial<Action> = {}): Action {
  return { n: evidence.next(), at: "2026-10-29T09:00:00.000Z", action: name, summary: `${name} done`, ms: 12, ...extra };
}

test("every action is one line with its screenshot beside it", () => {
  const dir = join(mkdtempSync(join(tmpdir(), "qa-evidence-")), "evidence");
  const evidence = new Evidence(dir);
  const open = action(evidence, "open", { url: "https://socrates-qa.example.test/invite?token=secret1&lang=de", settle: { ms: 900, settled: true, busy: [] } });
  evidence.writeScreenshot(open, Buffer.from("png"));
  evidence.append(open);
  const tap = action(evidence, "tap", { target: { text: "Advanced settings", native: true, matched: "Advanced settings" }, error: "native: the touch landed on div", output: "tapped button" });
  evidence.append(tap);
  const [first, second] = lines(dir);
  assert.deepEqual(first, {
    at: "2026-10-29T09:00:00.000Z",
    tool: "browserstack",
    action: "open",
    summary: "open done",
    url: "https://socrates-qa.example.test/invite?token=…&lang=de",
    screenshot: "01-open.png",
    ms: 12,
    error: null,
    interaction: false,
    details: { settle: { ms: 900, settled: true, busy: [] } },
  });
  assert.equal(readFileSync(join(dir, "01-open.png"), "utf8"), "png");
  assert.equal(second?.interaction, true);
  assert.equal(second?.screenshot, null);
  assert.equal(second?.error, "native: the touch landed on div");
  assert.deepEqual(second?.details, { target: { text: "Advanced settings", native: true, matched: "Advanced settings" }, output: "tapped button" });
});

test("tap, type and key are interactions; looking and navigating are not", () => {
  const base: Action = { n: 1, at: "", action: "", summary: "", ms: 0 };
  const flags = ["tap", "type", "key", "find", "look", "open", "scroll", "keyboard"].map((name) => evidenceLine({ ...base, action: name }).interaction);
  assert.deepEqual(flags, [true, true, true, false, false, false, false, false]);
});

test("a daemon started again for the attempt numbers on", () => {
  const dir = mkdtempSync(join(tmpdir(), "qa-evidence-"));
  const first = new Evidence(dir);
  first.append(action(first, "open"));
  first.append(action(first, "look"));
  const again = new Evidence(dir);
  const shot = action(again, "shot");
  assert.equal(shot.n, 3);
  assert.equal(again.screenshotName(shot), "03-shot.png");
});

test("links.json keeps the links BrowserStack gave; logs go to logs/", () => {
  const dir = mkdtempSync(join(tmpdir(), "qa-evidence-"));
  const evidence = new Evidence(dir);
  evidence.writeLinks({ Video: "https://example.test/video.mp4", "Console log": undefined, "Network log (HAR)": "" });
  assert.deepEqual(JSON.parse(readFileSync(join(dir, "links.json"), "utf8")), { Video: "https://example.test/video.mp4" });
  const file = evidence.writeLog("server", ["a", "b"]);
  assert.equal(file, join(dir, "logs", "server.txt"));
  assert.equal(readFileSync(file, "utf8"), "a\nb\n");
  assert.equal(existsSync(join(dir, "actions.jsonl")), false);
});
