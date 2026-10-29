import assert from "node:assert/strict";
import { test } from "node:test";
import { onSite, pageUrl, parseBaseUrl } from "../src/site";

const BASE = "https://socrates-qa.example.test";

test("the base URL is an http(s) origin, without a trailing slash", () => {
  assert.equal(parseBaseUrl("https://socrates-qa.example.test/"), BASE);
  assert.equal(parseBaseUrl(" https://socrates-qa.example.test:8443 "), "https://socrates-qa.example.test:8443");
  assert.equal(parseBaseUrl("https://host.test/prefix/"), "https://host.test/prefix");
  assert.throws(() => parseBaseUrl("socrates-qa.example.test"), /not a URL/);
  assert.throws(() => parseBaseUrl("ftp://host.test"), /not an http/);
  assert.throws(() => parseBaseUrl("https://user:pw@host.test"), /credentials/);
});

test("a path opens under the base URL, a hash included", () => {
  assert.equal(pageUrl(BASE, "/admin"), `${BASE}/admin`);
  assert.equal(pageUrl(BASE, "admin"), `${BASE}/admin`);
  assert.equal(pageUrl(BASE, "/#home"), `${BASE}/#home`);
  assert.equal(pageUrl(BASE, ""), `${BASE}/`);
  assert.equal(pageUrl(BASE, "/"), `${BASE}/`);
  assert.equal(pageUrl("https://host.test/prefix", "/admin"), "https://host.test/prefix/admin");
});

test("a full URL is taken only on the base URL's own origin", () => {
  assert.equal(pageUrl(BASE, `${BASE}/admin#x`), `${BASE}/admin#x`);
  assert.throws(() => pageUrl(BASE, "https://socrates.example.test/admin"), /not on the test instance/);
  assert.throws(() => pageUrl(BASE, "http://socrates-qa.example.test/admin"), /not on the test instance/);
  assert.throws(() => pageUrl(BASE, "https://socrates-qa.example.test:444/"), /not on the test instance/);
  assert.throws(() => pageUrl(BASE, "//evil.example/x"), /not on the test instance/);
  assert.throws(() => pageUrl(BASE, "javascript:alert(1)"), /not on the test instance/);
  assert.throws(() => pageUrl(BASE, "about:blank"), /not on the test instance/);
});

test("a page is on the site when its origin is the base URL's", () => {
  assert.ok(onSite(BASE, `${BASE}/#chat/1`));
  assert.ok(!onSite(BASE, "https://github.com/x"));
  assert.ok(!onSite(BASE, "about:blank"));
  assert.ok(!onSite(BASE, ""));
});
