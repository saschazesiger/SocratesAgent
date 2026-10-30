import { readFileSync } from "node:fs";
import { join } from "node:path";
import { basicAuth, type Account } from "./account";
import { findMarkers } from "./calibrate";
import { DEVICE_START_TIMEOUT_MS, IDLE_TIMEOUT_S, MARKER_GAP_CSS, PROJECT_NAME, ROOT, SESSION_API, SETTLE_MAX_MS, SETTLE_QUIET_MS, TYPEABLE_SECRET, type CellEnv } from "./config";
import type { Device } from "./devices";
import { redactUrl, type Action, type Evidence, type PageEntry, type Settle, type Target } from "./evidence";
import { KEYS, keyActions, parseKey } from "./keys";
import { answerButton, describePrompt, findNativePrompt, xpathLiteral, type NativePrompt } from "./prompt";
import { onSite, pageUrl, sameDocument } from "./site";
import { requireSecret, type Secrets } from "./vault";
import { WebDriver, type Rect } from "./webdriver";

const script = (name: string) => readFileSync(join(ROOT, "src/browser", `${name}.js`), "utf8");
const COMMON = script("common");
const PAGE = Object.fromEntries(["settle", "collector", "find", "look", "misc", "marker", "scroll"].map((name) => [name, `${COMMON}\n${script(name)}`])) as Record<
  "settle" | "collector" | "find" | "look" | "misc" | "marker" | "scroll",
  string
>;

/** How often (every 0.5 s) native typing on iOS looks for the keyboard to slide in. */
const KEYBOARD_WAIT_TRIES = 6;
/** How long one switch between the page and Appium's native layer may take; it is tried twice. */
const CONTEXT_TIMEOUT_MS = 25_000;
/** How long one command in the native layer (screenshot, page source, a touch) may take. */
const NATIVE_TIMEOUT_MS = 40_000;
/** How long a native tap waits before it looks for its markers on the screen a second time. */
const MARKER_RETRY_MS = 800;
/** How often (every 0.5 s) `qa keyboard` looks for the keyboard to be gone. */
const KEYBOARD_GONE_TRIES = 6;
/** A page shorter than the window by this much (css px) has the Android keyboard over it (Chrome resizes the visual viewport). */
const KEYBOARD_MIN_CSS = 120;
/** How long `qa back` and `qa forward` wait (in steps of 0.25 s) for desktop Safari's history to move. */
const HISTORY_WAIT_MS = 10_000;

/** What a simulated tap says about itself on a phone or tablet, so nobody takes it for a finger. */
const SIMULATED_NOTE =
  "simulated: a WebDriver click, not a finger. It can focus the control (focus ring, tooltip) where a touch would not, and on iOS it sends mouse events without pointerdown; judge such effects with a native tap";

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

/** A native tap that failed before any touch reached the device (Appium, the native screenshot): a simulated click may stand in. */
class NativeUnavailable extends Error {
  constructor(message: string) {
    super(`native: ${message}`);
  }
}

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

  /**
   * Settles the page and collects page errors after any action. On Android a page that lost the focus may sit
   * under a prompt Chrome draws itself (a permission question): it is looked for in the native screen.
   */
  private async afterAction(entry: Action): Promise<NativePrompt | null> {
    entry.settle = await this.settle();
    const collected = await this.driver.run<{ installed: boolean; entries: PageEntry[]; focused?: boolean }>(PAGE.collector).catch(() => ({ installed: false, entries: [], focused: undefined }));
    if (collected.entries.length) entry.pageErrors = collected.entries;
    entry.url = redactUrl(await this.driver.currentUrl().catch(() => ""));
    return this.device.kind === "android" && collected.focused === false ? this.nativePrompt() : null;
  }

  /**
   * A prompt Chrome or Android draws over the page (Android only: iOS screenshots show the whole screen anyway),
   * from the native screen's hierarchy; null when there is none or it cannot be read.
   */
  private async nativePrompt(): Promise<NativePrompt | null> {
    if (this.device.kind !== "android") return null;
    return this.inNative(async () => findNativePrompt(await this.driver.source(NATIVE_TIMEOUT_MS))).catch(() => null);
  }

  /** A screenshot of the whole screen (Appium's native context), browser prompts and bars included. */
  private async nativeScreenshot(): Promise<Buffer> {
    return this.inNative(() => this.driver.screenshot(NATIVE_TIMEOUT_MS));
  }

  /**
   * Runs `work` in Appium's native layer and comes back to the page. Appium sometimes hangs on the switch:
   * each one is bounded and tried twice. A switch there that fails is NativeUnavailable (nothing was done on the
   * device yet); failing to come back is an ActionError, the session may be broken.
   */
  private async inNative<T>(work: () => Promise<T>): Promise<T> {
    const web = await this.retried("reading the page's context", () => this.driver.context(CONTEXT_TIMEOUT_MS)).catch((error: Error) => Promise.reject(new NativeUnavailable(error.message)));
    await this.retried("switching to the native screen", () => this.driver.setContext("NATIVE_APP", CONTEXT_TIMEOUT_MS)).catch(async (error: Error) => {
      // A switch that timed out here may still have happened on Appium's side: make sure the page has it back.
      await this.driver.setContext(web, CONTEXT_TIMEOUT_MS).catch(() => undefined);
      throw new NativeUnavailable(error.message);
    });
    let result: T;
    try {
      result = await work();
    } catch (error) {
      await this.retried("returning to the page", () => this.driver.setContext(web, CONTEXT_TIMEOUT_MS)).catch(() => undefined);
      throw error;
    }
    await this.retried("returning to the page", () => this.driver.setContext(web, CONTEXT_TIMEOUT_MS)).catch((error: Error) =>
      Promise.reject(new ActionError(`native: ${error.message}; the session may be broken (TOOL.md, "A broken session")`)),
    );
    return result;
  }

  /** One Appium command, tried a second time after a second's pause when it fails or hangs. */
  private async retried<T>(what: string, command: () => Promise<T>): Promise<T> {
    try {
      return await command();
    } catch {
      await sleep(1000);
      try {
        return await command();
      } catch (error) {
        throw new Error(`${what} failed twice: ${(error as Error).message}`, { cause: error });
      }
    }
  }

  /** Notes a browser prompt in the action's output; its screenshot then shows the whole screen. */
  private notePrompt(entry: Action, prompt: NativePrompt | null): boolean {
    if (!prompt) return false;
    const line = `native prompt over the page (the browser draws it, the page cannot see or answer it; the screenshot shows the whole screen): ${describePrompt(prompt)}`;
    entry.output = entry.output ? `${entry.output}\n${line}` : line;
    if (!entry.summary.includes("native prompt")) entry.summary = `${entry.summary} (native prompt: ${describePrompt(prompt)})`;
    return true;
  }

  private async settle(): Promise<Settle> {
    const result = await this.driver.runAsync<{ ms: number; settled: boolean; busy: string[]; __error?: string }>(PAGE.settle, SETTLE_QUIET_MS, SETTLE_MAX_MS).catch((error: Error) => ({ ms: 0, settled: false, busy: [`settle failed: ${error.message}`] }));
    return { ms: result.ms, settled: result.settled, busy: result.busy ?? [] };
  }

  /**
   * Runs one action, then settle, errors and a screenshot, whatever happened, and appends it to
   * the evidence. `summary` says what was asked; the body's first output line, when it is words rather than JSON, replaces it.
   */
  async act(action: string, summary: string, body: (entry: Action) => Promise<string | undefined | void>, options: { screenshot?: boolean; settle?: boolean; prompt?: boolean } = {}): Promise<Action> {
    this.lastCommandAt = Date.now();
    const entry: Action = { n: this.evidence.next(), at: new Date().toISOString(), action, summary, ms: 0 };
    const begin = Date.now();
    try {
      const output = await body(entry);
      if (output) {
        entry.output = output;
        // A line of words replaces the summary; the opening bracket of a JSON answer (look) does not.
        const first = output.split("\n")[0]!.trim();
        if (first && !/^[[{]/.test(first)) entry.summary = first;
      }
    } catch (error) {
      entry.error = error instanceof Error ? error.message : String(error);
    }
    entry.ms = Date.now() - begin;
    let prompt: NativePrompt | null = null;
    if (options.settle !== false) prompt = await this.afterAction(entry).catch((error: Error) => ((entry.error ??= `after action: ${error.message}`), null));
    else if (options.prompt && this.device.kind === "android") {
      const focused = await this.driver.run<boolean>("function main() { return document.hasFocus(); }").catch(() => true);
      if (!focused) prompt = await this.nativePrompt();
    }
    const whole = this.notePrompt(entry, prompt);
    if (options.screenshot !== false) {
      await (whole ? this.nativeScreenshot().catch(() => this.driver.screenshot()) : this.driver.screenshot())
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
      // Desktop Safari's driver waits for a page load that a step within the document (only the hash differs)
      // never brings, and the whole session hangs: the page moves there itself, as a link to it would.
      if (this.desktopSafari() && sameDocument(await this.driver.currentUrl().catch(() => ""), url)) {
        await this.driver.run(PAGE.misc, "assign", url);
        return `opened ${redactUrl(url)} (within the page: only its hash changed)`;
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

  /**
   * How a tap reaches the device: a desktop is clicked through WebDriver; a phone or tablet is touched natively
   * (WebDriver clicks behave like a mouse there, and like a keyboard in iOS Safari: focus rings, tooltips, no
   * pointerdown), unless --simulated asks for the WebDriver click.
   */
  private touches(target: Target): boolean {
    return this.device.kind !== "desktop" && !target.simulated;
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
      const tapped = `tapped ${found.role} "${found.name}"`;
      if (this.touches(target)) {
        try {
          await this.nativeTap(found.nonce, found.name ?? "");
          entry.target.via = "touch";
          return [`${tapped} with a touch`, ...notes].join("\n");
        } catch (error) {
          // Only an explicit --native insists; the default falls back when no touch reached the device.
          if (!(error instanceof NativeUnavailable) || target.native) throw error;
          notes.unshift(`the native touch was not possible (${error.message})`);
        }
      }
      await this.driver.click(await this.driver.find(`[data-qa-hit="${found.nonce}"]`));
      entry.target.via = "webdriver";
      if (this.device.kind === "desktop") return [tapped, ...notes].join("\n");
      return [`${tapped} (simulated click, not a finger)`, SIMULATED_NOTE, ...notes].join("\n");
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
    const shown = await this.driver.run<{ placed: boolean; layer?: string }>(PAGE.marker, "show", MARKER_GAP_CSS, nonce);
    if (!shown.placed) throw new ActionError(`native: "${name}" is gone from the page`);
    let touched = false;
    try {
      await this.inNative(async () => {
        // Only the native screenshot shows the whole screen (Android's web one is the page viewport alone).
        const shoot = () => this.driver.screenshot(NATIVE_TIMEOUT_MS).catch((error: Error) => Promise.reject(new NativeUnavailable(`native screenshot: ${error.message}`)));
        let markers = findMarkers(await shoot());
        // A page still moving (an opening keyboard, a bar sliding away) may hide them for a moment: look once more.
        if (!markers) {
          await sleep(MARKER_RETRY_MS);
          markers = findMarkers(await shoot());
        }
        if (!markers) throw new ActionError(`native: "${name}" is not visible on the screen (under the on-screen keyboard or scrolled away); scroll it into view or close the keyboard first (qa keyboard)`);
        const rect = await this.driver.windowRect(NATIVE_TIMEOUT_MS).catch((error: Error) => Promise.reject(new NativeUnavailable(`screen size: ${error.message}`)));
        const toNative = rect.width / markers.imageWidth;
        const x = Math.round(((markers.first.x + markers.second.x) / 2) * toNative);
        const y = Math.round(((markers.first.y + markers.second.y) / 2) * toNative);
        touched = true;
        await this.driver.actions(
          [{ type: "pointer", id: "finger", parameters: { pointerType: "touch" }, actions: [{ type: "pointerMove", duration: 0, origin: "viewport", x, y }, { type: "pointerDown", button: 0 }, { type: "pause", duration: 80 }, { type: "pointerUp", button: 0 }] }],
          NATIVE_TIMEOUT_MS,
        );
        this.lastNativeTap = { x, y };
      });
    } catch (error) {
      await hide();
      if (error instanceof ActionError) throw error;
      // A touch that may have reached the device is never stood in for: a second tap could act twice.
      if (touched) throw new ActionError(`native: the touch on "${name}" failed: ${(error as Error).message}`);
      throw error instanceof NativeUnavailable ? error : new NativeUnavailable((error as Error).message);
    }
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
      let path = native ? await this.nativeType(value) : null;
      if (!path) {
        await this.driver.sendKeys(element, value);
        // Chrome publishes a web field to Android's accessibility only now and then (not at all in some dialogs):
        // the field was touched natively and has the focus, the text then comes through WebDriver.
        path = native ? "WebDriver into the touched field (Chrome did not show the field to Android, so not through the keyboard)" : "WebDriver";
      }
      if (options.submit) await this.driver.sendKeys(element, KEYS.enter!);
      if (options.into && !native && this.device.kind !== "desktop") return `typed ${shown} via ${path} (the field focused by a simulated click, not a finger; --native types as a person would)`;
      return `typed ${shown} via ${path}`;
    });
  }

  /**
   * Types through the device's own input stack after the native tap on the field: on iOS the on-screen
   * keyboard types it; on Android the focused field's accessibility node (EditText) receives it. Null when
   * Android shows no text field at all (the caller types through WebDriver and says so).
   */
  private async nativeType(value: string): Promise<string | null> {
    return this.inNative(async () => {
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
      if (!nearest) return null;
      await this.driver.sendKeys(nearest.field, value);
      return `the Android text field nearest the tap (${Number.isFinite(nearest.distance) ? `${Math.round(nearest.distance)} px away` : "distance unknown"})`;
    });
  }

  /**
   * Closes the on-screen keyboard as a person would: the Done button above it on iOS, Back on Android. Back is
   * pressed only while the keyboard shows (Appium's word for it, or a visible page shorter than the window):
   * with the keyboard up Android hands Back to the keyboard, which closes; without it Back reaches the page and
   * closes its dialog or goes back. Appium's hide_keyboard is not used on Android for that reason: it sends Back
   * when it cannot tell the keyboard closed.
   */
  async keyboard(): Promise<Action> {
    return this.act("keyboard", "close the on-screen keyboard", async () => {
      if (this.device.kind === "desktop") throw new ActionError("a desktop has no on-screen keyboard");
      const viewport = () => this.driver.run<{ visible: number; window: number; active: string }>(PAGE.misc, "viewport").catch(() => null);
      const before = await viewport();
      if (this.device.kind === "ios") {
        return this.inNative(async () => {
          const shows = async () => (await this.driver.findXPath("//XCUIElementTypeKeyboard", NATIVE_TIMEOUT_MS)).length > 0;
          if (!(await shows())) return "no on-screen keyboard was open: nothing to close";
          const [done] = await this.driver.findXPath('//XCUIElementTypeToolbar//XCUIElementTypeButton[@name="Done"]', NATIVE_TIMEOUT_MS);
          if (done) await this.driver.click(done, NATIVE_TIMEOUT_MS);
          else await this.driver.hideKeyboard();
          for (let attempt = 0; attempt < KEYBOARD_GONE_TRIES && (await shows()); attempt++) await sleep(500);
          if (await shows()) throw new ActionError(`the keyboard is still open after ${done ? "Done" : "Appium's hide_keyboard"}`);
          return done ? "tapped Done above the keyboard" : "closed the keyboard (Appium's hide_keyboard: no Done button above it)";
        });
      }
      const pressed = await this.inNative(async () => {
        const said = await this.driver.isKeyboardShown(NATIVE_TIMEOUT_MS);
        const shorter = before ? before.window - before.visible : 0;
        const facts = `Appium says the keyboard ${said === null ? "is unknown" : said ? "shows" : "is hidden"}; the page shows ${before ? `${before.visible} of ${before.window}` : "?"} css px`;
        if (!(said ?? shorter >= KEYBOARD_MIN_CSS)) return { pressed: false, facts, gone: true };
        await this.driver.pressKeycode(4, NATIVE_TIMEOUT_MS);
        let gone = false;
        for (let attempt = 0; attempt < KEYBOARD_GONE_TRIES && !gone; attempt++) {
          await sleep(500);
          gone = (await this.driver.isKeyboardShown(NATIVE_TIMEOUT_MS)) === false;
        }
        return { pressed: true, facts, gone };
      });
      if (!pressed.pressed) return `no on-screen keyboard was open: nothing pressed (Back would have reached the page) (${pressed.facts})`;
      const after = await viewport();
      const heights = before && after ? `; the page shows ${before.visible} css px before, ${after.visible} after` : "";
      if (!pressed.gone && !(before && after && after.visible - before.visible >= KEYBOARD_MIN_CSS)) throw new ActionError(`pressed Back, but the keyboard still shows${heights}`);
      return `pressed Back while the keyboard showed: the keyboard closed${heights}`;
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

  /**
   * Scrolls what a swipe in the middle of the screen would: the innermost box there that can still move that
   * way (a dialog's body, the app's main column), else the page; says which one and where it stands.
   */
  async scroll(where: string): Promise<Action> {
    if (!["down", "up", "top", "bottom"].includes(where)) throw new Error(`scroll down, up, top or bottom, not ${where}`);
    return this.act("scroll", `scrolled ${where}`, async () => {
      const result = await this.driver.runAsync<{ box: string; top: number; from: number; max: number; visible: number; moved: boolean; locked?: boolean; __error?: string }>(PAGE.scroll, where);
      if (result.__error) throw new Error(result.__error);
      const place = `${result.top} of ${result.max} px`;
      if (result.moved) return `scrolled ${where}: ${result.box} from ${result.from} to ${place}`;
      const end = where === "down" || where === "bottom" ? "bottom" : "top";
      return `nothing scrolled ${where}: ${result.box} is at its ${end} (${place})${result.locked ? ", and the page itself is locked (overflow hidden)" : ""}`;
    });
  }

  /**
   * Answers the prompt Chrome draws over the page on Android (the clipboard's Block / Allow, notifications,
   * Android's own permission dialogs) by pressing its button in the native layer, as a finger would.
   */
  async prompt(answer: "allow" | "block"): Promise<Action> {
    return this.act("prompt", `answer the browser's prompt: ${answer}`, async () => {
      if (this.device.kind !== "android") {
        throw new ActionError(`qa prompt answers the prompts Chrome draws on Android; ${this.device.kind === "ios" ? "on iOS Safari's own prompts show in the screenshot and are part of the page's flow (tap them with qa tap when the page offers them, or judge the step blocked)" : "a desktop browser here shows none the driver can answer"}`);
      }
      return this.inNative(async () => {
        const prompt = findNativePrompt(await this.driver.source(NATIVE_TIMEOUT_MS));
        if (!prompt) throw new ActionError("no native prompt is open over the page: nothing to answer");
        const label = answerButton(prompt, answer);
        if (!label) throw new ActionError(`the prompt has no button that answers ${answer}: ${describePrompt(prompt)}`);
        const [button] = await this.driver.findXPath(`//*[contains(@class, "Button") and @text=${xpathLiteral(label)} and not(ancestor::android.webkit.WebView)]`, NATIVE_TIMEOUT_MS);
        if (!button) throw new ActionError(`the prompt's [${label}] button is gone: ${describePrompt(prompt)}`);
        await this.driver.click(button, NATIVE_TIMEOUT_MS);
        await sleep(700);
        const still = findNativePrompt(await this.driver.source(NATIVE_TIMEOUT_MS).catch(() => ""));
        return `pressed [${label}] on the native prompt ${describePrompt(prompt)}${still ? `\na native prompt is still open: ${describePrompt(still)}` : ""}`;
      });
    });
  }

  async rotate(orientation: "portrait" | "landscape"): Promise<Action> {
    return this.act("rotate", `rotated to ${orientation}`, async () => {
      if (this.device.kind === "desktop") throw new ActionError("rotation needs a real device");
      await this.driver.setOrientation(orientation === "landscape" ? "LANDSCAPE" : "PORTRAIT");
    });
  }

  /**
   * Browser navigation. Desktop Safari's driver waits for a page load after Back and Forward, and a step that
   * stays in the document (a hash or pushState entry, as in a single-page app) never loads one: it hung for
   * the driver's two minutes. There the history moves from the page (history.back(), as the browser's own
   * button) and the driver waits until the address or the document changed.
   */
  async navigation(kind: "back" | "forward" | "reload"): Promise<Action> {
    return this.act(kind, kind === "reload" ? "reloaded the page" : `went ${kind}`, async () => {
      if (kind === "reload") return void (await this.driver.refresh());
      if (!this.desktopSafari()) return void (await (kind === "back" ? this.driver.back() : this.driver.forward()));
      const before = await this.driver.currentUrl();
      const mark = await this.driver.run<string>(PAGE.misc, "history", kind === "back" ? -1 : 1);
      for (const until = Date.now() + HISTORY_WAIT_MS; Date.now() < until; ) {
        await sleep(250);
        const url = await this.driver.currentUrl().catch(() => before);
        const same = await this.driver.run<boolean>(PAGE.misc, "marked", mark).catch(() => false);
        if (url !== before || !same) return;
      }
      return `nothing to go ${kind} to: the address stayed ${redactUrl(before)}`;
    });
  }

  private desktopSafari(): boolean {
    return this.device.kind === "desktop" && this.device.browserName === "safari";
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
      { settle: false, prompt: true },
    );
  }

  /** A screenshot only, without waiting for the page (a browser prompt on Android is still looked for). */
  async shot(): Promise<Action> {
    return this.act("shot", "took a screenshot", () => Promise.resolve(undefined), { settle: false, prompt: true });
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

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

function describeTarget(target: Target): string {
  const parts = [target.role, target.text !== undefined ? JSON.stringify(target.text) : undefined, target.css ? `css ${target.css}` : undefined, target.nth !== undefined ? `#${target.nth + 1}` : undefined, target.native ? "native" : undefined, target.simulated ? "simulated" : undefined];
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
