import assert from "node:assert/strict";
import { test } from "node:test";
import { busyMembers, parseAccount, parseAccounts } from "../src/account";

test("member n is the n-th user:key entry", () => {
  assert.deepEqual(parseAccounts(" a_1:k1, b_2:k2 ,"), [
    { member: 1, user: "a_1", key: "k1" },
    { member: 2, user: "b_2", key: "k2" },
  ]);
  assert.deepEqual(parseAccount("u:k:with:colons", 3), { member: 3, user: "u", key: "k:with:colons" });
  assert.throws(() => parseAccounts(""), /no account/);
  assert.throws(() => parseAccounts("a:k,b"), /entry 2/);
  assert.throws(() => parseAccounts("a:"), /entry 1/);
});

test("a member is busy when every parallel session of its plan runs; an unknown plan is free", () => {
  const accounts = parseAccounts("a:1,b:2,c:3,d:4");
  const plans = [{ running: 1, max: 1, queued: 0 }, { running: 0, max: 1, queued: 0 }, null, { running: 2, max: 2, queued: 3 }];
  assert.deepEqual(busyMembers(accounts, plans), [1, 4]);
});
