import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { parseSecrets, readVault, requireSecret } from "../src/vault";

test("a value is everything after the first =, taken raw", () => {
  assert.deepEqual(parseSecrets("A=x y\nB=a=b\nC=\"quoted\" 'too'\nD=\n\n"), { A: "x y", B: "a=b", C: "\"quoted\" 'too'", D: "" });
});

test("an export prefix is dropped, CRLF endings and other lines are ignored", () => {
  assert.deepEqual(parseSecrets("export QA_X=1\r\n# comment\n=nameless\nnot a line\n1BAD=x\nQA_Y=2"), { QA_X: "1", QA_Y: "2" });
});

test("the vault file holds the tool's secrets and the lease", () => {
  const file = join(mkdtempSync(join(tmpdir(), "qa-vault-")), "vault");
  writeFileSync(file, "BROWSERSTACK_ACCOUNTS=a:k1,b:k2\nSOCRATES_LEASE_BROWSERSTACK=2\nSOCRATES_LEASE_BROWSERSTACK_VALUE=b:k2\n", { mode: 0o600 });
  const secrets = readVault(file);
  assert.equal(requireSecret(secrets, "SOCRATES_LEASE_BROWSERSTACK_VALUE"), "b:k2");
  assert.equal(secrets.SOCRATES_LEASE_BROWSERSTACK, "2");
  assert.throws(() => requireSecret(secrets, "QA_SOCRATES_PASSWORD"), /QA_SOCRATES_PASSWORD is not in the vault/);
});
