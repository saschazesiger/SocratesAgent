import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseBaseUrl } from "./site";

/** The tool's own folder (Socrates runs it from the run's copy). */
export const ROOT = fileURLToPath(new URL("..", import.meta.url));

/** The evidence lines name the tool by its folder in .socrates/tests/tools. */
export const TOOL_NAME = "browserstack";
/** The vault line of the leased BrowserStack account (`user:key`), written by Socrates per cell. */
export const LEASE_VALUE = "SOCRATES_LEASE_BROWSERSTACK_VALUE";
/** The member number of the lease (1-based) in BROWSERSTACK_ACCOUNTS. */
export const LEASE_MEMBER = "SOCRATES_LEASE_BROWSERSTACK";
/** The pool's secret: every BrowserStack account as `user:key`, comma separated. */
export const ACCOUNTS_SECRET = "BROWSERSTACK_ACCOUNTS";
/** Only these vault names can be typed into a page (`qa type --secret`): never a BrowserStack key. */
export const TYPEABLE_SECRET = /^QA_[A-Z0-9_]+$/;

/** BrowserStack Automate: the W3C hub and the REST API of sessions and plans. */
export const HUB_URL = "https://hub-cloud.browserstack.com/wd/hub";
export const SESSION_API = "https://api.browserstack.com/automate/sessions";
export const PLAN_API = "https://api.browserstack.com/automate/plan.json";
export const PROJECT_NAME = "Socrates device QA";

/** BrowserStack ends a session after this many seconds without a command (its maximum). */
export const IDLE_TIMEOUT_S = 300;
/** The daemon sends a harmless command after this much silence, so thinking never ends a session. */
export const KEEPALIVE_MS = 60_000;
/** BrowserStack's hard limit is 2 h; the driver warns from here on. */
export const SESSION_WARN_MS = 100 * 60_000;
/** A daemon without any command for this long ends its session and exits. */
export const DAEMON_IDLE_EXIT_MS = 20 * 60_000;
/** How long the daemon waits for a device, BrowserStack's queue included. */
export const DEVICE_START_TIMEOUT_MS = 40 * 60_000;
/** How long one `qa start` waits before it hands back "still queued" (a shell call has its own limit). */
export const START_WAIT_MS = 8 * 60_000;
/** How long `qa stop` waits for the daemon to close the session before it ends the daemon itself. */
export const STOP_WAIT_MS = 2 * 60_000;
/** The exit status of `qa start` while the device is still queued. */
export const EXIT_QUEUED = 3;

/** After every action the page must be quiet this long; past the limit it is reported as not settled. */
export const SETTLE_QUIET_MS = 800;
export const SETTLE_MAX_MS = 20_000;
/** The two calibration markers of a native tap sit this many css px apart (diagonally) around the control's centre. */
export const MARKER_GAP_CSS = 30;
/** Desktop sessions run at this resolution and are maximised. */
export const DESKTOP_RESOLUTION = "1920x1080";
/** The systemd unit of the test instance whose journal `qa logs` reads, unless the area's params name another. */
export const DEFAULT_SERVICE = "socrates-qa";
/** At most this many (the last) server log lines are printed and kept in the evidence. */
export const LOG_LINES_MAX = 200;

function required(name: string, hint = "qa runs inside a Socrates test cell (.socrates/tests/tools/browserstack/TOOL.md)"): string {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is not set: ${hint}`);
  return value;
}

/** The vault file of the cell (or, for `qa pool-status`, of the run). */
export const vaultFile = (): string => required("SOCRATES_TEST_VAULT");
/** The attempt's evidence folder: actions.jsonl, screenshots, links.json, logs/. */
export const evidenceDir = (): string => required("SOCRATES_TEST_EVIDENCE");

/** The test instance every page is opened on (the area's `params: {base_url: ...}`), without a trailing slash. */
export function baseUrl(): string {
  return parseBaseUrl(required("SOCRATES_TEST_PARAM_BASE_URL", "the test instance's URL is set in the area's params (AREA.md, params: {base_url: https://...})"));
}

/** The systemd unit of the test instance (the area's `params: {service: ...}`). */
export function serviceUnit(): string {
  const unit = process.env.SOCRATES_TEST_PARAM_SERVICE || DEFAULT_SERVICE;
  if (!/^[A-Za-z0-9][A-Za-z0-9@._:-]*$/.test(unit)) throw new Error(`SOCRATES_TEST_PARAM_SERVICE=${unit} is not a systemd unit name`);
  return unit;
}

/** The attempt's runtime folder: the daemon's socket, pid, log and session facts. */
export function runtimePaths() {
  const dir = required("SOCRATES_TEST_RUNTIME");
  return { dir, socket: join(dir, "qa.sock"), pid: join(dir, "qa.pid"), log: join(dir, "daemon.log"), session: join(dir, "session.json"), startError: join(dir, "start-error.txt") };
}

/** What Socrates says about the cell this attempt works. */
export interface CellEnv {
  run: string;
  cell: string;
  device: string;
  baseUrl: string;
}

export function cellEnv(): CellEnv {
  return {
    run: required("SOCRATES_TEST_RUN"),
    cell: required("SOCRATES_TEST_CELL"),
    device: required("SOCRATES_TEST_DIM_DEVICE"),
    baseUrl: baseUrl(),
  };
}
