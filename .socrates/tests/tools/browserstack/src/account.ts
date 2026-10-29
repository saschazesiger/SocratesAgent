import { ACCOUNTS_SECRET, PLAN_API } from "./config";

/** A BrowserStack account of the pool: member n is the n-th entry of BROWSERSTACK_ACCOUNTS. */
export interface Account {
  member: number;
  user: string;
  key: string;
}

export interface Plan {
  running: number;
  max: number;
  queued: number;
}

/** One `user:key` entry. */
export function parseAccount(entry: string, member: number): Account {
  const value = entry.trim();
  const at = value.indexOf(":");
  if (at <= 0 || at === value.length - 1) throw new Error(`${ACCOUNTS_SECRET} entry ${member} is not user:key`);
  return { member, user: value.slice(0, at), key: value.slice(at + 1) };
}

/** `user:key,user:key,...`, split as Socrates splits the pool (separator ","); empty entries are skipped. */
export function parseAccounts(value: string): Account[] {
  const entries = value.split(",").filter((entry) => entry.trim());
  if (!entries.length) throw new Error(`${ACCOUNTS_SECRET} names no account`);
  return entries.map((entry, index) => parseAccount(entry, index + 1));
}

export function basicAuth(account: Account): string {
  return `Basic ${Buffer.from(`${account.user}:${account.key}`).toString("base64")}`;
}

/** The account's Automate plan and its running sessions; null when BrowserStack does not answer. */
export async function planOf(account: Account): Promise<Plan | null> {
  const response = await fetch(PLAN_API, { headers: { authorization: basicAuth(account) }, signal: AbortSignal.timeout(10_000) }).catch(() => null);
  if (!response?.ok) return null;
  const plan = (await response.json().catch(() => null)) as { parallel_sessions_running?: number; parallel_sessions_max_allowed?: number; queued_sessions?: number } | null;
  if (!plan) return null;
  return { running: plan.parallel_sessions_running ?? 0, max: plan.parallel_sessions_max_allowed ?? 1, queued: plan.queued_sessions ?? 0 };
}

/** The members whose every parallel session is taken; an account BrowserStack did not answer for counts as free. */
export function busyMembers(accounts: Account[], plans: (Plan | null)[]): number[] {
  return accounts.filter((_, index) => {
    const plan = plans[index];
    return plan ? plan.running >= plan.max : false;
  }).map((account) => account.member);
}
