import assert from "node:assert/strict";
import { test } from "node:test";
import { redactUrl } from "../src/evidence";

test("credentials in URLs are cut", () => {
  assert.equal(redactUrl("https://socrates-qa.example.test/s?token=abc&lang=de#code=1"), "https://socrates-qa.example.test/s?token=…&lang=de#code=…");
  assert.equal(redactUrl("https://automate.browserstack.com/builds/b/sessions/s?auth_token=f00d"), "https://automate.browserstack.com/builds/b/sessions/s?auth_token=…");
});

test("plain routes and ids stay readable", () => {
  assert.equal(redactUrl("https://socrates-qa.example.test/#chat/abc?id=7"), "https://socrates-qa.example.test/#chat/abc?id=7");
});
