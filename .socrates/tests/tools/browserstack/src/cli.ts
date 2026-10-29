import { execFileSync, spawn } from "node:child_process";
import { existsSync, openSync, readFileSync, rmSync } from "node:fs";
import { request } from "node:http";
import { join } from "node:path";
import { parseArgs } from "node:util";
import { busyMembers, parseAccount, parseAccounts, planOf } from "./account";
import { ACCOUNTS_SECRET, EXIT_QUEUED, LEASE_MEMBER, LEASE_VALUE, LOG_LINES_MAX, ROOT, START_WAIT_MS, STOP_WAIT_MS, cellEnv, evidenceDir, runtimePaths, serviceUnit, vaultFile } from "./config";
import type { Answer, Command, SessionFacts } from "./daemon";
import { deviceById } from "./devices";
import { Evidence, redactUrl } from "./evidence";
import { KEY_NAMES } from "./keys";
import { sessionLinks } from "./session";
import { readVault, requireSecret } from "./vault";
import { WebDriver } from "./webdriver";

const USAGE = `usage: qa <command> [options]          (instructions: TOOL.md)

The cell (device, test instance, account) comes from Socrates' environment; every action is written
into the cell's evidence with its screenshot.

  start                                           start the device (prints "device ready"); exit 3 = still queued, run it again
  stop                                            close the BrowserStack session (Socrates runs it after every attempt)
  open <path>                                     open a page of the test instance (/admin, /#home, or a full URL of its origin), then settle
  find <text> [--role r] [--nth n] [--exact] [--any] [--css sel]   scroll a control into view without touching it
  tap <text> [--role r] [--nth n] [--exact] [--any] [--css sel] [--native] [--force]   (with a text, --css is the area to search)
  type <value> [--into <text>|--css sel] [--role r] [--native] [--clear] [--submit]
  type --secret QA_NAME [--into <text>|--css sel] ...   type a login of the vault without it passing through you
  key <name>                                      ${KEY_NAMES} (combinations on desktops only)
  keyboard                                        close the on-screen keyboard as a person would
  scroll <down|up|top|bottom>   rotate <portrait|landscape>   back | forward | reload   wait <seconds>
  look                                            screenshot and the controls on screen
  shot | env                                      screenshot only, device facts
  logs                                            the test instance's server log (journalctl) since the device started
  pool-status                                     Socrates' pool hook: {"busy": [...]} members busy outside Socrates`;

const OPTIONS = {
  role: { type: "string" },
  nth: { type: "string" },
  exact: { type: "boolean" },
  any: { type: "boolean" },
  css: { type: "string" },
  native: { type: "boolean" },
  force: { type: "boolean" },
  into: { type: "string" },
  clear: { type: "boolean" },
  submit: { type: "boolean" },
  secret: { type: "string" },
  help: { type: "boolean" },
} as const;

function print(text: string): void {
  process.stdout.write(`${text}\n`);
}

function fail(message: string): never {
  process.stderr.write(`qa: ${message}\n`);
  process.exit(2);
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

function send(socket: string, command: Command, timeoutMs = 30 * 60_000): Promise<Answer> {
  return new Promise((resolve, reject) => {
    const req = request({ socketPath: socket, path: "/", method: "POST", timeout: timeoutMs }, (response) => {
      let body = "";
      response.on("data", (chunk: Buffer) => (body += chunk.toString()));
      response.on("end", () => {
        try {
          resolve(JSON.parse(body) as Answer);
        } catch (error) {
          reject(error as Error);
        }
      });
    });
    req.on("timeout", () => req.destroy(new Error("the daemon did not answer in time")));
    req.on("error", reject);
    req.end(JSON.stringify(command));
  });
}

/** The daemon's pid while it lives. */
function daemonPid(file: string): number | null {
  if (!existsSync(file)) return null;
  const pid = Number(readFileSync(file, "utf8").trim());
  if (!pid) return null;
  try {
    process.kill(pid, 0);
    return pid;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === "EPERM" ? pid : null;
  }
}

async function ping(socket: string): Promise<Answer | null> {
  if (!existsSync(socket)) return null;
  return send(socket, { name: "ping", args: {} }, 5_000).catch(() => null);
}

async function commandOn(name: string, args: Record<string, unknown>): Promise<void> {
  const { socket } = runtimePaths();
  const alive = await ping(socket);
  if (!alive) fail("no device session: run qa start first");
  const answer = await send(socket, { name, args });
  print(answer.text);
  if (!answer.ok) process.exitCode = 1;
}

/** Starts the cell's daemon unless one runs, then waits for the device at most START_WAIT_MS. */
async function start(): Promise<void> {
  const paths = runtimePaths();
  const cell = cellEnv();
  deviceById(cell.device);
  if (!daemonPid(paths.pid)) {
    rmSync(paths.startError, { force: true });
    const log = openSync(paths.log, "a");
    const daemon = spawn(join(ROOT, "node_modules/.bin/tsx"), [join(ROOT, "src/daemon.ts")], { detached: true, stdio: ["ignore", log, log], cwd: ROOT });
    daemon.unref();
  }
  const until = Date.now() + START_WAIT_MS;
  for (;;) {
    await sleep(3_000);
    const answer = await ping(paths.socket);
    if (answer?.state === "running") return print(`${answer.text}\nnext: open the first page of the test (qa open <path>)`);
    if (!answer && !daemonPid(paths.pid)) {
      const reason = existsSync(paths.startError) ? readFileSync(paths.startError, "utf8").trim() : `the daemon exited before the device started (log: ${paths.log})`;
      fail(reason);
    }
    if (answer?.state === "stopping") fail("the device session is closing (qa stop ran)");
    if (Date.now() > until) {
      print("still queued: BrowserStack has not handed out the device yet and the daemon keeps waiting. Run qa start again.");
      process.exitCode = EXIT_QUEUED;
      return;
    }
  }
}

/** Closes the session the daemon of a cell left behind when the daemon cannot (hung or killed). */
async function closeOrphan(facts: SessionFacts): Promise<string> {
  const secrets = readVault(vaultFile());
  const account = parseAccount(requireSecret(secrets, LEASE_VALUE), Number(secrets[LEASE_MEMBER]) || 0);
  await WebDriver.attach(account, facts.sessionId)
    .quit()
    .catch(() => undefined);
  new Evidence(evidenceDir()).writeLinks(await sessionLinks(facts.sessionId, account));
  return `closed the BrowserStack session ${facts.sessionId} the daemon left behind`;
}

/** The end hook: closes the session and the daemon, whatever state they are in. */
async function stop(): Promise<void> {
  const paths = runtimePaths();
  const pid = daemonPid(paths.pid);
  const facts = existsSync(paths.session) ? (JSON.parse(readFileSync(paths.session, "utf8")) as SessionFacts) : null;
  if (!pid) {
    rmSync(paths.socket, { force: true });
    rmSync(paths.pid, { force: true });
    return print("no device session");
  }
  const answer = await send(paths.socket, { name: "stop", args: {} }, STOP_WAIT_MS).catch(() => null);
  for (let waited = 0; waited < 10_000 && daemonPid(paths.pid); waited += 500) await sleep(500);
  if (answer && !daemonPid(paths.pid)) return print(answer.text);
  process.kill(pid, "SIGKILL");
  rmSync(paths.socket, { force: true });
  rmSync(paths.pid, { force: true });
  print(facts ? await closeOrphan(facts) : "ended the daemon before its device started");
}

/** Socrates' pool hook: the members (1-based) whose every BrowserStack session is taken outside Socrates' leases. */
async function poolStatus(): Promise<void> {
  const accounts = parseAccounts(requireSecret(readVault(vaultFile()), ACCOUNTS_SECRET));
  const plans = await Promise.all(accounts.map(planOf));
  print(JSON.stringify({ busy: busyMembers(accounts, plans) }));
}

/** Seconds since the epoch of an ISO time, for journalctl's --since @<seconds>. */
function epochSeconds(iso: string): number {
  return Math.floor(Date.parse(iso) / 1000);
}

/** The test instance's server log since the device started (journalctl), printed and kept in the evidence as logs/server.txt. */
function logs(): void {
  const paths = runtimePaths();
  if (!existsSync(paths.session)) fail("no device session yet: the window starts when qa start reports the device ready");
  const facts = JSON.parse(readFileSync(paths.session, "utf8")) as SessionFacts;
  const unit = serviceUnit();
  const args = ["-u", unit, "--since", `@${epochSeconds(facts.startedAt)}`, "--no-pager", "-o", "short-iso", "-n", String(LOG_LINES_MAX)];
  let out: string;
  try {
    out = execFileSync("journalctl", args, { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], timeout: 60_000, maxBuffer: 16 * 1024 * 1024 });
  } catch (error) {
    const failure = error as NodeJS.ErrnoException & { stderr?: string };
    const reason = failure.code === "ENOENT" ? "journalctl is not installed here" : (failure.stderr?.trim() || failure.message).split("\n")[0];
    print(`could not read the server log of ${unit} (journalctl ${args.join(" ")}): ${reason}`);
    process.exitCode = 1;
    return;
  }
  const lines = out
    .split("\n")
    .filter((line) => line.trim() && !line.startsWith("-- "))
    .map(redactUrl)
    .slice(-LOG_LINES_MAX);
  const file = new Evidence(evidenceDir()).writeLog("server", lines);
  print(`the server log of ${unit} since the device started (${facts.startedAt}), at most the last ${LOG_LINES_MAX} lines`);
  print("note: other cells of the run use the same test instance at the same time; a line may be theirs");
  print(lines.length ? lines.join("\n") : "no lines since the device started");
  print(`kept in ${file}`);
}

async function main(): Promise<void> {
  const { values, positionals } = parseArgs({ args: process.argv.slice(2), options: OPTIONS, allowPositionals: true });
  const [name, ...rest] = positionals;
  if (!name || name === "help" || values.help) return print(USAGE);
  const text = rest.join(" ");
  if (values.nth !== undefined && !/^[1-9]\d*$/.test(values.nth)) fail("--nth counts from 1");
  const target = { role: values.role, nth: values.nth ? Number(values.nth) - 1 : undefined, exact: values.exact, any: values.any, css: values.css, native: values.native };
  switch (name) {
    case "start":
      return start();
    case "stop":
      return stop();
    case "pool-status":
      return poolStatus();
    case "logs":
      return logs();
    case "open":
      return commandOn("open", { path: text || "/" });
    case "find":
      return commandOn("find", { ...target, text: text || undefined });
    case "tap":
      return commandOn("tap", { ...target, text: text || undefined, force: values.force });
    case "type":
      return commandOn("type", {
        value: text,
        ...(values.into || values.css ? { ...target, text: values.into } : {}),
        options: { native: values.native, clear: values.clear, submit: values.submit, secret: values.secret },
      });
    case "key":
    case "scroll":
    case "rotate":
    case "wait":
      return commandOn(name, { key: text, where: text || "down", orientation: text, seconds: Number(text || 5) });
    case "keyboard":
    case "back":
    case "forward":
    case "reload":
    case "look":
    case "shot":
    case "env":
      return commandOn(name, {});
    default:
      fail(`unknown command ${name}\n${USAGE}`);
  }
}

main().catch((error: unknown) => fail(error instanceof Error ? error.message : String(error)));
