/**
 * The background process `qa start` launches for one cell: it holds the cell's BrowserStack session,
 * answers the CLI over $SOCRATES_TEST_RUNTIME/qa.sock one command at a time, keeps the session alive
 * while the agent thinks and ends it on `qa stop`, on SIGTERM or when nobody has asked anything for too long.
 */
import { existsSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { parseAccount, type Account } from "./account";
import { DAEMON_IDLE_EXIT_MS, KEEPALIVE_MS, LEASE_MEMBER, LEASE_VALUE, SESSION_WARN_MS, cellEnv, evidenceDir, runtimePaths, vaultFile } from "./config";
import { deviceById } from "./devices";
import { Evidence, type Action, type Target } from "./evidence";
import { DeviceSession, type TypeOptions } from "./session";
import { onSite, originOf } from "./site";
import { readVault, requireSecret } from "./vault";

export interface Command {
  name: string;
  args: Record<string, unknown>;
}

export type DaemonState = "starting" | "running" | "stopping";

export interface Answer {
  ok: boolean;
  text: string;
  state?: DaemonState;
}

/** What `qa logs` and `qa stop` read when the daemon cannot answer. */
export interface SessionFacts {
  sessionId: string;
  startedAt: string;
}

const paths = runtimePaths();

function startFailed(message: string): never {
  writeFileSync(paths.startError, `${message}\n`);
  rmSync(paths.pid, { force: true });
  process.stderr.write(`${message}\n`);
  if (existsSync(paths.socket)) rmSync(paths.socket);
  process.exit(1);
}

let setup: { cell: ReturnType<typeof cellEnv>; device: ReturnType<typeof deviceById>; account: Account; evidence: Evidence; secrets: Record<string, string> };
try {
  const cell = cellEnv();
  const secrets = readVault(vaultFile());
  const account = parseAccount(requireSecret(secrets, LEASE_VALUE), Number(secrets[LEASE_MEMBER]) || 0);
  setup = { cell, device: deviceById(cell.device), account, evidence: new Evidence(evidenceDir()), secrets };
} catch (error) {
  startFailed(`the device did not start: ${(error as Error).message}`);
}
const { cell, device, account, evidence, secrets } = setup;

let session: DeviceSession | null = null;
let state: DaemonState = "starting";
let busy: Promise<unknown> = Promise.resolve();

/** Runs tasks one after the other; a failed task answers its caller and leaves the queue running. */
function queue<T>(task: () => Promise<T>): Promise<T> {
  const result = busy.then(task);
  busy = result.catch(() => undefined);
  return result;
}

function describe(action: Action): string {
  const lines = [`action ${action.n} ${action.action} ${action.ms} ms${action.error ? ` ERROR ${action.error}` : ""}`];
  if (action.output) lines.push(action.output);
  if (action.settle && !action.settle.settled) lines.push(`not settled after ${action.settle.ms} ms${action.settle.busy.length ? `: ${action.settle.busy.join(", ")}` : ""}`);
  if (action.pageErrors?.length) lines.push(`page errors:\n${action.pageErrors.map((entry) => `  ${entry.kind}: ${entry.message}`).join("\n")}`);
  if (action.url) lines.push(`url: ${action.url}`);
  if (action.url && originOf(action.url) && !onSite(cell.baseUrl, action.url)) lines.push(`WARNING: the page left the test instance (${originOf(action.url)}); go back to ${cell.baseUrl} (qa back or qa open) and never act on another site`);
  if (action.screenshot) lines.push(`screenshot: ${evidence.dir}/${action.screenshot}`);
  if (session && Date.now() - session.startedAt > SESSION_WARN_MS) lines.push("WARNING: the session nears BrowserStack's 2 h limit; finish this cell soon");
  return lines.join("\n");
}

function target(args: Record<string, unknown>): Target {
  return { text: args.text as string | undefined, role: args.role as string | undefined, css: args.css as string | undefined, nth: args.nth as number | undefined, exact: Boolean(args.exact), any: Boolean(args.any), native: Boolean(args.native), simulated: Boolean(args.simulated) };
}

function answer(action: Action): Answer {
  return { ok: !action.error, text: describe(action), state };
}

function started(s: DeviceSession): string {
  return `device ready: ${device.label} (${device.kind}${device.options.deviceOrientation ? `, ${device.options.deviceOrientation}` : ""}), test instance ${cell.baseUrl}, BrowserStack account ${account.user}, session ${s.driver.sessionId}`;
}

/** Quits the session, writes its links into the evidence and exits; safe to call more than once. */
let stopping: Promise<string> | null = null;
function stop(reason: string): Promise<string> {
  stopping ??= (async () => {
    state = "stopping";
    if (!session) return `stopped before the device started (${reason})`;
    const links = await session.stop().catch(() => ({}));
    evidence.writeLinks(links);
    return `stopped: the BrowserStack session ${session.driver.sessionId} is closed (${reason}); its video and logs are in links.json`;
  })();
  return stopping;
}

async function handle(command: Command): Promise<Answer> {
  const a = command.args;
  if (command.name === "ping") return { ok: true, state, text: session ? started(session) : "the device is still starting (BrowserStack may queue it)" };
  if (command.name === "stop") return { ok: true, state: "stopping", text: await stop("qa stop") };
  if (!session || state !== "running") return { ok: false, state, text: state === "stopping" ? "the device session is closing" : "the device is still starting: run qa start until it says the device is ready" };
  const s = session;
  s.lastCommandAt = Date.now();
  switch (command.name) {
    case "open":
      return answer(await s.open(String(a.path)));
    case "find":
      return answer(await s.find(target(a)));
    case "tap":
      return answer(await s.tap(target(a), { force: Boolean(a.force) }));
    case "type":
      return answer(await s.type(String(a.value ?? ""), { into: a.text || a.css ? target(a) : undefined, ...(a.options as TypeOptions) }));
    case "keyboard":
      return answer(await s.keyboard());
    case "key":
      return answer(await s.key(String(a.key)));
    case "scroll":
      return answer(await s.scroll(String(a.where)));
    case "rotate":
      return answer(await s.rotate(a.orientation === "landscape" ? "landscape" : "portrait"));
    case "back":
    case "forward":
    case "reload":
      return answer(await s.navigation(command.name));
    case "wait":
      return answer(await s.wait(Number(a.seconds)));
    case "look":
      return answer(await s.look());
    case "shot":
      return answer(await s.shot());
    case "env":
      return { ok: true, state, text: JSON.stringify(await s.env(), null, 1) };
    default:
      return { ok: false, state, text: `unknown command ${command.name}` };
  }
}

function shutdown(code: number): never {
  rmSync(paths.socket, { force: true });
  rmSync(paths.pid, { force: true });
  process.exit(code);
}

const server = createServer((request: IncomingMessage, response: ServerResponse) => {
  let body = "";
  request.on("data", (chunk: Buffer) => (body += chunk.toString()));
  request.on("end", () => {
    const command = JSON.parse(body || "{}") as Command;
    // A ping and a stop never wait behind a running action or the device start.
    const result = command.name === "ping" || command.name === "stop" ? handle(command) : queue(() => handle(command));
    void result
      .catch((error: unknown): Answer => ({ ok: false, state, text: error instanceof Error ? error.message : String(error) }))
      .then((value) => {
        response.writeHead(200, { "content-type": "application/json" });
        response.end(JSON.stringify(value));
        if (command.name === "stop") setTimeout(() => shutdown(0), 200);
      });
  });
});

rmSync(paths.socket, { force: true });
rmSync(paths.startError, { force: true });
writeFileSync(paths.pid, `${process.pid}\n`);
server.listen(paths.socket);
process.on("SIGTERM", () => {
  void stop("SIGTERM").finally(() => shutdown(0));
});

DeviceSession.start({ cell, device, account }, secrets, evidence).then(
  (begun) => {
    session = begun;
    if (stopping) {
      // qa stop came while BrowserStack was still queueing: close what it handed over after all.
      void begun.stop().finally(() => shutdown(0));
      return;
    }
    state = "running";
    const facts: SessionFacts = { sessionId: begun.driver.sessionId, startedAt: new Date(begun.startedAt).toISOString() };
    writeFileSync(paths.session, `${JSON.stringify(facts)}\n`);
    setInterval(() => {
      const idle = Date.now() - begun.lastCommandAt;
      if (idle > DAEMON_IDLE_EXIT_MS) {
        void stop(`no command for ${Math.round(idle / 60_000)} min`).finally(() => shutdown(0));
      } else if (idle > KEEPALIVE_MS) {
        void queue(() => begun.driver.currentUrl().catch(() => undefined));
      }
    }, KEEPALIVE_MS / 2).unref();
  },
  (error: unknown) => {
    if (stopping) shutdown(0);
    startFailed(`the device did not start: ${error instanceof Error ? error.message : String(error)}`);
  },
);
