import { readFileSync } from "node:fs";
import { join } from "node:path";
import { basicAuth, type Account } from "./account";
import { findMarkers } from "./calibrate";
import { DEVICE_START_TIMEOUT_MS, IDLE_TIMEOUT_S, MARKER_GAP_CSS, PROJECT_NAME, ROOT, SESSION_API, SETTLE_MAX_MS, SETTLE_QUIET_MS, TYPEABLE_SECRET, type CellEnv } from "./config";
import type { Device } from "./devices";
import { redactUrl, type Action, type Evidence, type PageEntry, type Settle, type Target } from "./evidence";
import { KEYS, keyActions, parseKey } from "./keys";
import { onSite, pageUrl } from "./site";
import { requireSecret, type Secrets } from "./vault";
import { WebDriver, type Rect } from "./webdriver";

const script = (name: string) => readFileSync(join(ROOT, "src/browser", `${name}.js`), "utf8");
const COMMON = script("common");
const PAGE = Object.fromEntries(["settle", "collector", "find", "look", "misc", "marker"].map((name) => [name, `${COMMON}\n${script(name)}`])) as Record<
  "settle" | "collector" | "find" | "look" | "misc" | "marker",
  string
>;

/** How often (every 0.5 s) native typing on iOS looks for the keyboard to slide in. */
const KEYBOARD_WAIT_TRIES = 6;

export interface TypeOptions {
  into?: Target;
  native?: boolean;
  clear?: boolean;
  submit?: boolean;
  /** Types a QA_* secret of the vault by name (never recorded). */
  secret?: string;
}

interface FindResult {
  found: boolean;
  nonce?: string;
  name?: string;
  /** The hit's place in the listed candidates (what --nth counts, from 0). */
  index?: number;
  role?: string;
  tag?: string;
  exact?: boolean;
  count: number;
  candidates: { name: string; role: string; tag: string; exact: boolean }[];
  rect?: Rect;
  viewport?: { width: number; height: number };
  disabled?: boolean;
  /** The element on top at the control's centre, when it is not the control. */
  covered?: { by: string } | null;
}

export interface StartOptions {
  cell: CellEnv;
  device: Device;
  account: Account;
}

/** Thrown for an action that cannot be carried out; the action is still recorded with its screenshot. */
export class ActionError extends Error {}

/** One BrowserStack session for one cell (test x device), writing every action into the evidence. */
export class DeviceSession {
  lastCommandAt = Date.now();
  readonly startedAt = Date.now();
  private lastNativeTap: { x: number; y: number } | null = null;

  private constructor(
    readonly driver: WebDriver,
    readonly device: Device,
    readonly options: StartOptions,
    private readonly secrets: Secrets,
    private readonly evidence: Evidence,
  ) {}

  get baseUrl(): string {
    return this.options.cell.baseUrl;
  }

  static async start(options: StartOptions, secrets: Secrets, evidence: Evidence): Promise<DeviceSession> {
    const capabilities = {
      browserName: options.device.browserName,
      // Navigation returns at DOMContentLoaded; the settle wait follows the page.
      pageLoadStrategy: "eager",
      "bstack:options": {
        ...options.device.options,
        buildName: options.cell.run,
        projectName: PROJECT_NAME,
        sessionName: options.cell.cell,
        idleTimeout: IDLE_TIMEOUT_S,
        video: true,
        networkLogs: true,
        consoleLogs: "errors",
      },
    };
    const driver = await WebDriver.create(options.account, capabilities, DEVICE_START_TIMEOUT_MS);
    const session = new DeviceSession(driver, options.device, options, secrets, evidence);
    await driver.setScriptTimeout(SETTLE_MAX_MS + 10_000);
    if (options.device.kind === "desktop") await driver.maximize().catch(() => undefined);
    return session;
  }

  /** Taps, typing and keys act only on a page of the test instance: a link or a redirect to another site is refused. */
  private async onTestInstance(): Promise<void> {
    const url = await this.driver.currentUrl();
    if (!onSite(this.baseUrl, url)) throw new ActionError(`the page is not on the test instance ${this.baseUrl} (${redactUrl(url) || "no page"}): qa back or qa open a page of it first; never act on another site`);
  }

  /** Settles the page and collects page errors after any action. */
  private async afterAction(entry: Action): Promise<void> {
    entry.settle = await this.settle();
    const collected = await this.driver.run<{ installed: boolean; entries: PageEntry[] }>(PAGE.collector).catch(() => ({ installed: false, entries: [] }));
    if (collected.entries.length) entry.pageErrors = collected.entries;
    entry.url = redactUrl(await this.driver.currentUrl().catch(() => ""));
  }

  private async settle(): Promise<Settle> {
    const result = await this.driver.runAsync<{ ms: number; settled: boolean; busy: string[]; __error?: string }>(PAGE.settle, SETTLE_QUIET_MS, SETTLE_MAX_MS).catch((error: Error) => ({ ms: 0, settled: false, busy: [`settle failed: ${error.message}`] }));
    return { ms: result.ms, settled: result.settled, busy: result.busy ?? [] };
  }

  /**
   * Runs one action, then settle, errors and a screenshot, whatever happened, and appends it to
   * the evidence. `summary` says what was asked; the body's first output line, when it gives one, replaces it.
   */
  async act(action: string, summary: string, body: (entry: Action) => Promise<string | undefined | void>, options: { screenshot?: boolean; settle?: boolean } = {}): Promise<Action> {
    this.lastCommandAt = Date.now();
    const entry: Action = { n: this.evidence.next(), at: new Date().toISOString(), action, summary, ms: 0 };
    const begin = Date.now();
    try {
      const output = await body(entry);
      if (output) {
        entry.output = output;
        entry.summary = output.split("\n")[0]!;
      }
    } catch (error) {
      entry.error = error instanceof Error ? error.message : String(error);
    }
    entry.ms = Date.now() - begin;
    if (options.settle !== false) await this.afterAction(entry).catch((error: Error) => (entry.error ??= `after action: ${error.message}`));
    if (options.screenshot !== false) {
      await this.driver
        .screenshot()
        .then((png) => this.evidence.writeScreenshot(entry, png))
        .catch((error: Error) => (entry.error ??= `screenshot: ${error.message}`));
    }
    this.evidence.append(entry);
    this.lastCommandAt = Date.now();
    return entry;
  }

  /** Opens a path of the test instance or a full URL of its own origin; any other site is refused (and recorded). */
  async open(path: string): Promise<Action> {
    return this.act("open", `opened ${redactUrl(path)}`, async () => {
      let url: string;
      try {
        url = pageUrl(this.baseUrl, path);
      } catch (error) {
        throw new ActionError((error as Error).message);
      }
      await this.driver.navigate(url);
      return `opened ${redactUrl(url)}`;
    });
  }

  /** Finds a control by text, role or CSS selector (text and selector together narrow each other). */
  private async locate(target: Target): Promise<FindResult> {
    const query = { text: target.text, role: target.role, css: target.css, nth: target.nth, exact: target.exact, any: target.any };
    return this.driver.run<FindResult>(PAGE.find, query);
  }

  private describeMiss(found: FindResult, target: Target): string {
    const near = found.candidates.map((candidate) => `${candidate.role} "${candidate.name}"`).join(", ");
    return `no ${target.role ?? "control"} "${target.text ?? target.css}"${near ? `; similar: ${near}` : ""}`;
  }

  /** Scrolls a control into view without touching it: shows that it is there, and what covers it. */
  async find(target: Target): Promise<Action> {
    return this.act("find", `find ${describeTarget(target)}`, async (entry) => {
      entry.target = { ...target };
      const found = await this.locate(target);
      if (!found.found) throw new ActionError(this.describeMiss(found, target));
      entry.target.matched = found.name;
      const notes = [`found ${found.role} "${found.name}"${found.disabled ? " (disabled)" : ""}`];
      if (found.count > 1) notes.push(`${found.count} matches, showing #${(found.index ?? 0) + 1} (--nth): ${found.candidates.map((candidate) => `"${candidate.name}"`).join(", ")}`);
      if (found.covered) notes.push(`covered by ${found.covered.by}`);
      return notes.join("\n");
    });
  }

  async tap(target: Target, options: { force?: boolean } = {}): Promise<Action> {
    return this.act("tap", `tap ${describeTarget(target)}`, async (entry) => {
      entry.target = { ...target };
      await this.onTestInstance();
      const found = await this.locate(target);
      if (!found.found || !found.nonce) throw new ActionError(this.describeMiss(found, target));
      entry.target.matched = found.name;
      const notes: string[] = [];
      if (found.count > 1) notes.push(`${found.count} matches, took #${(found.index ?? 0) + 1} (--nth): ${found.candidates.map((candidate) => `"${candidate.name}"`).join(", ")}`);
      if (found.disabled) notes.push("control is disabled");
      if (found.covered && !options.force) throw new ActionError(`"${found.name}" is covered by ${found.covered.by}; use --force to tap anyway`);
      if (target.native && this.device.kind !== "desktop") await this.nativeTap(found.nonce, found.name ?? "");
      else await this.driver.click(await this.driver.find(`[data-qa-hit="${found.nonce}"]`));
      return [`tapped ${found.role} "${found.name}"`, ...notes].join("\n");
    });
  }

  /**
   * A real touch on the device screen at the control's centre (Appium native context). Where the control is
   * drawn is measured, not computed: two markers placed on it with the page content are found in a native
   * screenshot, their midpoint is its centre. Markers missing from the screen mean the control is not visible
   * (under the keyboard, scrolled away). The page then tells which element received the touch.
   */
  private async nativeTap(nonce: string, name: string): Promise<void> {
    const hide = () => this.driver.run<{ on: boolean; by: string; name: string } | null>(PAGE.marker, "hide").catch(() => null);
    const shown = await this.driver.run<{ placed: boolean }>(PAGE.marker, "show", MARKER_GAP_CSS, nonce);
    if (!shown.placed) throw new ActionError(`native: "${name}" is gone from the page`);
    const web = await this.driver.context();
    await this.driver.setContext("NATIVE_APP");
    try {
      // Only the native screenshot shows the whole screen (Android's web one is the page viewport alone).
      const markers = findMarkers(await this.driver.screenshot());
      if (!markers) throw new ActionError(`native: "${name}" is not visible on the screen (under the on-screen keyboard or scrolled away); scroll it into view or close the keyboard first (qa keyboard)`);
      const toNative = (await this.driver.windowRect()).width / markers.imageWidth;
      const x = Math.round(((markers.first.x + markers.second.x) / 2) * toNative);
      const y = Math.round(((markers.first.y + markers.second.y) / 2) * toNative);
      await this.driver.actions([
        { type: "pointer", id: "finger", parameters: { pointerType: "touch" }, actions: [{ type: "pointerMove", duration: 0, origin: "viewport", x, y }, { type: "pointerDown", button: 0 }, { type: "pause", duration: 80 }, { type: "pointerUp", button: 0 }] },
      ]);
      this.lastNativeTap = { x, y };
    } catch (error) {
      await this.driver.setContext(web);
      await hide();
      throw error;
    }
    await this.driver.setContext(web);
    // What the page received: a touch that missed is an error of this action, not a silent success.
    const hit = await hide();
    if (hit && !hit.on) throw new ActionError(`native: the touch landed on ${hit.by}${hit.name ? ` "${hit.name}"` : ""}, not on "${name}"`);
    if (!hit && onSite(this.baseUrl, await this.driver.currentUrl().catch(() => ""))) throw new ActionError(`native: no page element received the touch meant for "${name}" (browser bar or keyboard in the way?)`);
  }

  async type(text: string, options: TypeOptions): Promise<Action> {
    const shown = options.secret ? `<secret ${options.secret}>` : JSON.stringify(text);
    return this.act("type", `type ${shown}${options.into ? ` into ${describeTarget(options.into)}` : ""}`, async (entry) => {
      await this.onTestInstance();
      let value = text;
      if (options.secret) {
        if (!TYPEABLE_SECRET.test(options.secret)) throw new ActionError(`--secret types a QA_* login of the vault, not ${options.secret}`);
        value = requireSecret(this.secrets, options.secret);
      }
      const native = Boolean(options.native) && this.device.kind !== "desktop";
      let element: string;
      if (options.into) {
        entry.target = { ...options.into, native: options.native };
        const found = await this.locate({ ...options.into, role: options.into.role ?? "textbox" });
        if (!found.found || !found.nonce) throw new ActionError(this.describeMiss(found, options.into));
        entry.target.matched = found.name;
        element = await this.driver.find(`[data-qa-hit="${found.nonce}"]`);
        if (native) {
          await this.nativeTap(found.nonce, found.name ?? "");
          const focus = await this.driver.run<{ focused: boolean; active: string }>(PAGE.misc, "focused", found.nonce);
          if (!focus.focused) throw new ActionError(`the native tap on "${found.name}" did not focus it (focus is on ${focus.active || "nothing"})`);
        } else await this.driver.click(element);
      } else element = await this.driver.activeElement();
      if (options.clear) await this.driver.clear(element);
      const path = native ? await this.nativeType(value) : (await this.driver.sendKeys(element, value), "WebDriver");
      if (options.submit) await this.driver.sendKeys(element, KEYS.enter!);
      return `typed ${shown} via ${path}`;
    });
  }

  /**
   * Types through the device's own input stack after the native tap on the field: on iOS the on-screen
   * keyboard types it; on Android the focused field's accessibility node (EditText) receives it.
   */
  private async nativeType(value: string): Promise<string> {
    const web = await this.driver.context();
    await this.driver.setContext("NATIVE_APP");
    try {
      if (this.device.kind === "ios") {
        let keyboard = false;
        for (let attempt = 0; attempt < KEYBOARD_WAIT_TRIES && !keyboard; attempt++) {
          if (attempt) await new Promise((resolve) => setTimeout(resolve, 500));
          keyboard = (await this.driver.findXPath("//XCUIElementTypeKeyboard")).length > 0;
        }
        if (!keyboard) throw new ActionError("no on-screen keyboard opened");
        await this.driver.deviceKeys(value);
        return "the iOS keyboard";
      }
      // Chrome publishes a focused web field to accessibility a moment after the tap: wait for it, and fall back
      // to the field nearest the tap (the opening keyboard can move the page).
      const tap = this.lastNativeTap;
      if (!tap) throw new ActionError("native typing on Android needs a native tap on the field first (--into ... --native)");
      for (let attempt = 0; attempt < 5; attempt++) {
        const [focused] = await this.driver.findXPath('//android.widget.EditText[@focused="true" and not(contains(@resource-id, "url_bar"))]');
        if (focused) {
          await this.driver.sendKeys(focused, value);
          return "the focused Android text field";
        }
        await new Promise((resolve) => setTimeout(resolve, 700));
      }
      let nearest: { field: string; distance: number } | null = null;
      for (const field of await this.driver.findXPath('//android.widget.EditText[not(contains(@resource-id, "url_bar"))]')) {
        const r = await this.driver.rect(field);
        const measured = Math.hypot(Math.max(r.x - tap.x, 0, tap.x - (r.x + r.width)), Math.max(r.y - tap.y, 0, tap.y - (r.y + r.height)));
        const distance = Number.isFinite(measured) ? measured : Infinity;
        if (!nearest || distance < nearest.distance) nearest = { field, distance };
      }
      if (!nearest) throw new ActionError("no text field on the screen");
      await this.driver.sendKeys(nearest.field, value);
      return `the Android text field nearest the tap (${Number.isFinite(nearest.distance) ? `${Math.round(nearest.distance)} px away` : "distance unknown"})`;
    } finally {
      await this.driver.setContext(web);
    }
  }

  /** Closes the on-screen keyboard as a person would: the Done button above it on iOS, Back on Android. */
  async keyboard(): Promise<Action> {
    return this.act("keyboard", "close the on-screen keyboard", async () => {
      if (this.device.kind === "desktop") throw new ActionError("a desktop has no on-screen keyboard");
      const web = await this.driver.context();
      await this.driver.setContext("NATIVE_APP");
      try {
        if (this.device.kind === "ios") {
          const [done] = await this.driver.findXPath('//XCUIElementTypeToolbar//XCUIElementTypeButton[@name="Done"]');
          if (done) {
            await this.driver.click(done);
            return "tapped Done above the keyboard";
          }
        }
        await this.driver.hideKeyboard();
        return "closed the keyboard";
      } finally {
        await this.driver.setContext(web);
      }
    });
  }

  /**
   * Presses a key or a combination (ctrl+c, shift+tab, ...). A phone or tablet has no hardware modifiers: a
   * combination is refused there, the app's own key bar is what a person would use.
   */
  async key(name: string): Promise<Action> {
    const press = parseKey(name);
    if (press.modifiers.length && this.device.kind !== "desktop") {
      throw new Error(`${press.name}: a ${this.device.kind === "ios" ? "iOS" : "Android"} device has no hardware modifier keys; use the app's own on-screen key bar (qa tap its Ctrl, Tab, ... keys) as a person would`);
    }
    return this.act("key", `pressed ${press.name}`, async () => {
      await this.onTestInstance();
      await this.driver.actions([{ type: "key", id: "keyboard", actions: keyActions(press) }]);
    });
  }

  async scroll(where: string): Promise<Action> {
    return this.act("scroll", `scrolled ${where}`, async () => {
      const result = await this.driver.run<{ scrollY: number }>(PAGE.misc, "scroll", where);
      return `scrolled ${where} to scrollY ${result.scrollY}`;
    });
  }

  async rotate(orientation: "portrait" | "landscape"): Promise<Action> {
    return this.act("rotate", `rotated to ${orientation}`, async () => {
      if (this.device.kind === "desktop") throw new ActionError("rotation needs a real device");
      await this.driver.setOrientation(orientation === "landscape" ? "LANDSCAPE" : "PORTRAIT");
    });
  }

  async navigation(kind: "back" | "forward" | "reload"): Promise<Action> {
    return this.act(kind, kind === "reload" ? "reloaded the page" : `went ${kind}`, async () => {
      if (kind === "back") await this.driver.back();
      else if (kind === "forward") await this.driver.forward();
      else await this.driver.refresh();
    });
  }

  async wait(seconds: number): Promise<Action> {
    return this.act("wait", `waited ${seconds} s`, async () => {
      const until = Date.now() + seconds * 1000;
      while (Date.now() < until) {
        await new Promise((resolve) => setTimeout(resolve, Math.min(30_000, until - Date.now())));
        await this.driver.currentUrl();
      }
    });
  }

  async look(): Promise<Action> {
    return this.act(
      "look",
      "looked at the controls on screen",
      async () => {
        const seen = await this.driver.run<Record<string, unknown>>(PAGE.look);
        return JSON.stringify(seen, null, 1);
      },
      { settle: false },
    );
  }

  async env(): Promise<Record<string, unknown>> {
    return this.driver.run<Record<string, unknown>>(PAGE.misc, "env");
  }

  /** Quits the BrowserStack session; the links of its video and logs are returned. */
  async stop(): Promise<Record<string, string | undefined>> {
    await this.driver.quit().catch(() => undefined);
    return sessionLinks(this.driver.sessionId, this.options.account);
  }
}

function describeTarget(target: Target): string {
  const parts = [target.role, target.text !== undefined ? JSON.stringify(target.text) : undefined, target.css ? `css ${target.css}` : undefined, target.nth !== undefined ? `#${target.nth + 1}` : undefined, target.native ? "native" : undefined];
  return parts.filter(Boolean).join(" ") || "the focused field";
}

/** The session's links for links.json: video, network and text logs; the public link's token is cut. */
export async function sessionLinks(sessionId: string, account: Account): Promise<Record<string, string | undefined>> {
  const response = await fetch(`${SESSION_API}/${sessionId}.json`, { headers: { authorization: basicAuth(account) }, signal: AbortSignal.timeout(30_000) }).catch(() => null);
  if (!response?.ok) return {};
  const body = (await response.json().catch(() => ({}))) as { automation_session?: Record<string, string | undefined> };
  const session = body.automation_session ?? {};
  return {
    "BrowserStack session": session.browser_url,
    Video: session.video_url,
    "Network log (HAR)": session.har_logs_url,
    "Console log": session.browser_console_logs_url,
    "Text log": session.logs,
    "Public link": session.public_url && redactUrl(session.public_url),
  };
}
