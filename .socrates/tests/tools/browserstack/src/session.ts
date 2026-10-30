import { readFileSync } from "node:fs";
import { join } from "node:path";
import { basicAuth, type Account } from "./account";
import { findMarkers } from "./calibrate";
import { DEVICE_START_TIMEOUT_MS, IDLE_TIMEOUT_S, MARKER_GAP_CSS, PROJECT_NAME, ROOT, SESSION_API, SETTLE_MAX_MS, SETTLE_QUIET_MS, TYPEABLE_SECRET, type CellEnv } from "./config";
import { grantCapabilities, type Device, type Grant } from "./devices";
import { redactUrl, type Action, type Evidence, type PageEntry, type Settle, type Target } from "./evidence";
import { type At, type Direction, type Point, clampPoint, dragActions, holdActions, swipeEnd, tapActions, vector, wheelActions } from "./gesture";
import { KEYS, keyActions, parseKey } from "./keys";
import { MONTHS, PICK_KINDS, dayLabelMatches, formatValue, hour12, monthSteps, parseMonthYear, wantedMoment, type Moment, type PickKind } from "./picker";
import { type NativeElement, type NativePlatform, type NativeScreen, describeElement, iosPrompt, matchNative, middle, nativeOverlay, parseNative } from "./native";
import { answerButton, describePrompt, findNativePrompt, xpathLiteral, type NativePrompt } from "./prompt";
import { onSite, pageUrl, sameDocument } from "./site";
import { requireSecret, type Secrets } from "./vault";
import { WebDriver, type ActionSequence, type Rect } from "./webdriver";

const script = (name: string) => readFileSync(join(ROOT, "src/browser", `${name}.js`), "utf8");
const COMMON = script("common");
const PAGE_SCRIPTS = ["settle", "collector", "find", "look", "misc", "marker", "scroll", "gesture"] as const;
const PAGE = Object.fromEntries(PAGE_SCRIPTS.map((name) => [name, `${COMMON}\n${script(name)}`])) as Record<(typeof PAGE_SCRIPTS)[number], string>;

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

/** `qa native look` lists at most this many elements. */
const NATIVE_LIST_MAX = 40;
/** How long a native touch waits before it looks at the native screen again. */
const NATIVE_AFTER_TAP_MS = 800;

/** How long a picker's step (a month, a day, opening the wheels) waits before the native screen is read again. */
const PICKER_STEP_MS = 500;

/** How long a gesture waits for the page to follow (a scroll's momentum, a selection) before it looks again. */
const GESTURE_REST_MS = 700;

/** What can move at a point (src/browser/gesture.js "probe"). */
interface Probe {
  page: { top: number; left: number };
  /** The pinch zoom (visualViewport.scale) as text: a key named "scale" does not come back from iOS Safari. */
  zoom: string;
  boxes: { box: string; top: number; left: number; maxTop: number }[];
  selection: string;
}

export interface GestureOptions {
  at?: At;
  /** A swipe's length in CSS px. */
  distance?: number;
  /** The wheel's amount in CSS px. */
  amount?: number;
  /** How long a hold lasts, or a drag rests before it moves. */
  seconds?: number;
  /** How long the movement takes. */
  ms?: number;
}

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

/** Where a native gesture is anchored on the screen (Appium's native units) and how CSS px map onto it. */
interface NativeAnchor extends Point {
  /** Native units per CSS px. */
  scale: number;
  /** The native screen. */
  screen: Rect;
  /** The page's visible part on the screen. */
  page: Rect;
  /** The anchor in the page (viewport CSS px). */
  css: Point;
  /** The anchor in the native screenshot's pixels (on iOS the action's screenshot is the same picture). */
  shot: Point;
}

export interface StartOptions {
  cell: CellEnv;
  device: Device;
  account: Account;
  /** Permissions given in advance (desktop Chrome, Edge, Firefox: qa start --grant). */
  grants?: Grant[];
}

/** Thrown for an action that cannot be carried out; the action is still recorded with its screenshot. */
export class ActionError extends Error {}

/** A native tap that failed before any touch reached the device (Appium, the native screenshot): a simulated click may stand in. */
class NativeUnavailable extends Error {
  constructor(message: string) {
    super(`native: ${message}`);
  }
}

/** A native picker the driver cannot drive (not opened, or not of a kind it knows): the script may stand in. */
class PickerUnavailable extends Error {}

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
      ...grantCapabilities(options.device, options.grants ?? []),
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
    const collected = await this.driver
      .run<{ installed: boolean; entries: PageEntry[]; focused?: boolean; clipboard?: string[] }>(PAGE.collector, this.device.kind === "ios")
      .catch(() => ({ installed: false, entries: [] as PageEntry[], focused: undefined, clipboard: [] as string[] }));
    if (collected.entries.length) entry.pageErrors = collected.entries;
    if (collected.clipboard?.length) {
      const line = `NOTE: the page asked to read the clipboard (navigator.clipboard.${[...new Set(collected.clipboard)].join(", ")}). On iOS Safari answers that with its Paste callout, and a touch that opens it hangs the device session until BrowserStack ends it, so the driver refused the read for the page as Safari does when the callout is dismissed (NotAllowedError). What the page shows now is its answer to a refused clipboard; Safari's Paste callout itself cannot be tested here`;
      entry.output = entry.output ? `${entry.output}\n${line}` : line;
    }
    entry.url = redactUrl(await this.driver.currentUrl().catch(() => ""));
    if (collected.focused !== false) return null;
    return this.device.kind === "android" ? this.nativePrompt() : this.iosAlert();
  }

  /**
   * iOS: an alert over the page (Safari's question for the microphone, notifications, a location), from
   * WebDriver's alert text (cheap) and, when there is one, its buttons from the native screen.
   */
  private async iosAlert(): Promise<NativePrompt | null> {
    if (this.device.kind !== "ios") return null;
    const text = await this.driver.alertText(NATIVE_TIMEOUT_MS).catch(() => null);
    if (text === null) return null;
    const prompt = await this.inNative(async () => iosPrompt((await this.nativeScreen()).screen)).catch(() => null);
    return prompt ?? { texts: [text], buttons: [] };
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
    const line = `native prompt over the page (the browser draws it, the page cannot see or answer it; the screenshot shows the whole screen): ${describePrompt(prompt)}; answer it with qa prompt allow or qa prompt block`;
    entry.output = entry.output ? `${entry.output}\n${line}` : line;
    if (!entry.summary.includes("native prompt")) entry.summary = `${entry.summary} (native prompt: ${describePrompt(prompt)})`;
    return true;
  }

  private async settle(): Promise<Settle> {
    const result = await this.driver.runAsync<{ ms: number; settled: boolean; busy: string[]; __error?: string }>(PAGE.settle, SETTLE_QUIET_MS, SETTLE_MAX_MS).catch((error: Error) => ({ ms: 0, settled: false, busy: [`settle failed: ${error.message}`], __error: undefined }));
    if (result?.__error) return { ms: result.ms ?? 0, settled: false, busy: [`settle failed: ${result.__error}`] };
    // No answer at all: the page did not run the script (a native prompt over it holds it).
    if (!result || typeof result.ms !== "number") return { ms: 0, settled: false, busy: ["the page did not answer (a native prompt or callout over it?)"] };
    return { ms: result.ms, settled: result.settled, busy: result.busy ?? [] };
  }

  /**
   * Runs one action, then settle, errors and a screenshot, whatever happened, and appends it to
   * the evidence. `summary` says what was asked; the body's first output line, when it is words rather than JSON, replaces it.
   */
  async act(action: string, summary: string, body: (entry: Action) => Promise<string | undefined | void>, options: { screenshot?: boolean; settle?: boolean; prompt?: boolean; whole?: boolean } = {}): Promise<Action> {
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
    else if (options.prompt && this.device.kind !== "desktop") {
      const focused = await this.driver.run<boolean>("function main() { return document.hasFocus(); }").catch(() => true);
      if (!focused) prompt = this.device.kind === "android" ? await this.nativePrompt() : await this.iosAlert();
    }
    const whole = this.notePrompt(entry, prompt) || Boolean(options.whole && this.device.kind === "android");
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

  async tap(target: Target, options: { force?: boolean; double?: boolean; at?: At } = {}): Promise<Action> {
    return this.act(options.double ? "double" : "tap", `${options.double ? "double tap" : "tap"} ${describeTarget(target)}`, async (entry) => {
      entry.target = { ...target };
      await this.onTestInstance();
      if (options.double || options.at) {
        if (target.simulated) throw new ActionError("--double and --at are a finger on phones and tablets: leave --simulated out");
        const aimed = await this.aim(target);
        if (aimed.nonce) entry.target.matched = aimed.name;
        entry.target.via = this.device.kind === "desktop" ? "webdriver" : "touch";
        return this.tapAt(target, aimed, options.at, Boolean(options.double));
      }
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
    await this.nativeGesture(nonce, name, undefined, (anchor) => tapActions("touch", anchor));
  }

  /**
   * A gesture of a finger on the device screen (Appium's native context), anchored where the page draws a
   * point: the control's centre, or `at` inside it (without a nonce: inside the viewport). Two markers placed
   * around the point are found in a native screenshot; their midpoint is the point, their distance the scale
   * from CSS px to the screen's units. `build` makes the action sequence from that anchor. The page then tells
   * which element the finger came down on: a gesture meant for a control that landed elsewhere is an error.
   */
  private async nativeGesture(nonce: string | null, name: string, at: At | undefined, build: (anchor: NativeAnchor) => ActionSequence[], timeoutMs = NATIVE_TIMEOUT_MS): Promise<NativeAnchor> {
    const hide = () => this.driver.run<{ on: boolean; by: string; name: string } | null>(PAGE.marker, "hide").catch(() => null);
    const shown = await this.driver.run<{ placed: boolean; layer?: string; point?: Point; inView?: boolean; under?: string }>(PAGE.marker, "show", MARKER_GAP_CSS, nonce, at ?? null, this.device.kind === "ios");
    if (!shown.placed) throw new ActionError(`native: "${name}" is gone from the page`);
    const viewport = await this.driver.run<{ found: boolean; viewport: Rect }>(PAGE.gesture, "resolve", null, null).catch(() => null);
    let touched = false;
    let anchor: NativeAnchor | null = null;
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
        const scale = ((markers.second.x - markers.first.x) / MARKER_GAP_CSS) * toNative;
        const css = shown.point ?? { x: 0, y: 0 };
        const vp = viewport?.viewport;
        // The page's visible part on the screen: the finger stays inside it (not on the browser's bars).
        const page = vp ? { x: x + (vp.x - css.x) * scale, y: y + (vp.y - css.y) * scale, width: vp.width * scale, height: vp.height * scale } : { x: 0, y: 0, width: rect.width, height: rect.height };
        const shot = { x: Math.round((markers.first.x + markers.second.x) / 2), y: Math.round((markers.first.y + markers.second.y) / 2) };
        anchor = { x, y, scale, screen: rect, page, css, shot };
        const actions = build(anchor);
        touched = true;
        await this.driver.actions(actions, timeoutMs);
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
    if (nonce && hit && !hit.on) throw new ActionError(`native: the touch landed on ${hit.by}${hit.name ? ` "${hit.name}"` : ""}, not on "${name}"`);
    if (nonce && !hit && onSite(this.baseUrl, await this.driver.currentUrl().catch(() => ""))) throw new ActionError(`native: no page element received the touch meant for "${name}" (browser bar or keyboard in the way?)`);
    return anchor!;
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
      const notes: string[] = [];
      let element: string;
      let nonce: string | null;
      if (options.into) {
        entry.target = { ...options.into, native: options.native };
        const found = await this.locate({ ...options.into, role: options.into.role ?? "textbox" });
        if (!found.found || !found.nonce) throw new ActionError(this.describeMiss(found, options.into));
        entry.target.matched = found.name;
        nonce = found.nonce;
        element = await this.driver.find(`[data-qa-hit="${found.nonce}"]`);
        if (native) {
          await this.nativeTap(found.nonce, found.name ?? "");
          const focus = await this.driver.run<{ focused: boolean; active: string }>(PAGE.misc, "focused", found.nonce);
          if (!focus.focused) throw new ActionError(`the native tap on "${found.name}" did not focus it (focus is on ${focus.active || "nothing"})`);
          if (this.device.kind === "ios" && !(await this.keyboardShows())) notes.push(await this.raiseIosKeyboard(found.nonce, found.name ?? ""));
        } else await this.driver.click(element);
      } else {
        element = await this.driver.activeElement();
        nonce = await this.driver.run<string | null>(PAGE.misc, "mark-active").catch(() => null);
      }
      if (options.clear) notes.push(await this.clearField(element, nonce, native));
      let path = native ? await this.nativeType(value) : null;
      if (!path) {
        await this.driver.sendKeys(element, value);
        // Chrome publishes a web field to Android's accessibility only now and then (not at all in some dialogs):
        // the field was touched natively and has the focus, the text then comes through WebDriver.
        path = native ? "WebDriver into the touched field (Chrome did not show the field to Android, so not through the keyboard)" : "WebDriver";
      }
      if (options.submit) await this.driver.sendKeys(element, KEYS.enter!);
      const typed = options.into && !native && this.device.kind !== "desktop" ? `typed ${shown} via ${path} (the field focused by a simulated click, not a finger; --native types as a person would)` : `typed ${shown} via ${path}`;
      return [typed, ...notes].join("\n");
    });
  }

  /** iOS: whether the on-screen keyboard shows, waited for a moment (it slides in after the touch). */
  private async keyboardShows(): Promise<boolean> {
    return this.inNative(async () => {
      for (let attempt = 0; attempt < KEYBOARD_WAIT_TRIES; attempt++) {
        if (attempt) await sleep(500);
        if ((await this.driver.findXPath("//XCUIElementTypeKeyboard", NATIVE_TIMEOUT_MS)).length > 0) return true;
      }
      return false;
    });
  }

  /**
   * iOS raises no keyboard for a field the page focused by itself (a dialog that opens on its field), and a touch
   * on such a field shows the edit menu (Cut, Copy, Paste) on its selected text instead of the keyboard. A person
   * then touches beside the field and on it again: the driver takes the focus away from the field (as that touch
   * beside it would) and touches the field again. Reported in the output, because a person meets it.
   */
  private async raiseIosKeyboard(nonce: string, name: string): Promise<string> {
    const menu = await this.inNative(async () => nativeOverlay((await this.nativeScreen()).screen, "ios").filter((element) => /^(cut|copy|paste|look up|select all)$/i.test(element.label))).catch(() => []);
    await this.driver.run(PAGE.misc, "blur", nonce);
    await this.nativeTap(nonce, name);
    const focus = await this.driver.run<{ focused: boolean; active: string }>(PAGE.misc, "focused", nonce);
    if (!focus.focused) throw new ActionError(`iOS raised no keyboard for "${name}", and the second touch did not focus it (focus is on ${focus.active || "nothing"})`);
    if (!(await this.keyboardShows())) throw new ActionError(`no on-screen keyboard opened for "${name}", neither on the first touch nor after taking the focus away and touching it again`);
    const said = menu.length ? `; the touch showed iOS's edit menu (${[...new Set(menu.map((element) => element.label))].join(", ")}) instead` : "";
    return `NOTE: the first touch on "${name}" raised no keyboard (the field had the focus already, given by the page, and iOS raises a keyboard only for a focus a touch gives${said}); a person would touch beside it and on it again, so the driver took the focus away and touched it again: the keyboard opened. A person meets this: judge it`;
  }

  /**
   * Empties a field the way a person does, so the page hears it: its whole text selected (as Select All does),
   * then the Delete key, through the device's keyboard on a phone or tablet typing natively, else WebDriver's
   * keys. WebDriver's own clear sets the value without the key and input events a page listens for.
   */
  private async clearField(element: string, nonce: string | null, native: boolean): Promise<string> {
    const selected = nonce ? await this.driver.run<{ length: number; selected: boolean; value: string }>(PAGE.misc, "select-all", nonce).catch(() => null) : null;
    if (!selected) {
      await this.driver.clear(element);
      return "cleared with WebDriver's clear (the field could not be selected: the page may not have heard it)";
    }
    if (!selected.length) return "the field was empty already";
    const press = async (keys: string) => {
      if (native && this.device.kind === "ios") await this.inNative(() => this.driver.deviceKeys(keys));
      else await this.driver.sendKeys(element, keys);
    };
    await press(KEYS.backspace!);
    let left = await this.driver.run<{ length: number; value: string } | null>(PAGE.misc, "value", nonce).catch(() => null);
    // A field that takes no selection (a number field in some browsers): the caret to its end, then a Delete per character.
    if (left && left.length) {
      await this.driver.run(PAGE.misc, "caret-end", nonce).catch(() => undefined);
      await press(KEYS.backspace!.repeat(left.length));
      left = await this.driver.run<{ length: number; value: string } | null>(PAGE.misc, "value", nonce).catch(() => null);
    }
    if (left && left.length) throw new ActionError(`the field still holds ${JSON.stringify(left.value)} after selecting it all and pressing Delete`);
    return `cleared: selected all ${selected.length} character(s) and pressed Delete${native && this.device.kind === "ios" ? " on the iOS keyboard" : ""}`;
  }

  /**
   * Types through the device's own input stack after the native tap on the field: on iOS the on-screen
   * keyboard types it; on Android the focused field's accessibility node (EditText) receives it. Null when
   * Android shows no text field at all (the caller types through WebDriver and says so).
   */
  private async nativeType(value: string): Promise<string | null> {
    return this.inNative(async () => {
      if (this.device.kind === "ios") {
        if (!(await this.driver.findXPath("//XCUIElementTypeKeyboard", NATIVE_TIMEOUT_MS)).length) throw new ActionError("no on-screen keyboard is open to type with (tap the field with qa type --into ... --native)");
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
   * Finds the control a gesture is aimed at (a text, --css, --role ...), or none: then the gesture is aimed at
   * the visible viewport (and `at` inside it).
   */
  private async aim(target: Target | undefined): Promise<{ nonce: string | null; name: string }> {
    if (!target || (target.text === undefined && !target.css)) return { nonce: null, name: "the page" };
    const found = await this.locate(target);
    if (!found.found || !found.nonce) throw new ActionError(this.describeMiss(found, target));
    // A box found by its selector alone (a terminal's screen) is named by the selector, not by its text.
    return { nonce: found.nonce, name: target.text === undefined && target.css ? target.css : found.name || target.css || "the control" };
  }

  /** Where in the page (viewport CSS px) a gesture at `aim` and `at` starts, and what is there. */
  private async resolvePoint(nonce: string | null, at: At | undefined): Promise<{ point: Point; under: string; viewport: Rect; dpr: number }> {
    const resolved = await this.driver.run<{ found: boolean; point: Point; inView: boolean; under: string; viewport: Rect; dpr: string }>(PAGE.gesture, "resolve", nonce, at ?? null);
    if (!resolved.found) throw new ActionError("the control is gone from the page");
    // On a phone the native screenshot decides (the markers are on the screen or not); iOS reports the
    // rects of a page under the keyboard against a viewport that is not the visible one.
    if (!resolved.inView && this.device.kind === "desktop") throw new ActionError(`the point (${resolved.point.x}, ${resolved.point.y}) is outside the visible part of the page; scroll it into view or pick a point inside it (--at)`);
    return { ...resolved, dpr: Number(resolved.dpr) || 1 };
  }

  private async probe(point: Point): Promise<Probe | null> {
    return this.driver.run<Probe>(PAGE.gesture, "probe", point.x, point.y).catch(() => null);
  }

  /** What a gesture moved, from the probes before and after it. */
  private moved(before: Probe | null, after: Probe | null): string {
    if (!before || !after) return "what moved is unknown (the page could not be read before and after)";
    const lines: string[] = [];
    if (before.page.top !== after.page.top || before.page.left !== after.page.left) lines.push(`the page scrolled from ${before.page.top} to ${after.page.top} px${before.page.left !== after.page.left ? ` (sideways ${before.page.left} to ${after.page.left})` : ""}`);
    else lines.push(`the page itself stayed at ${after.page.top} px`);
    lines.push(before.zoom !== after.zoom ? `the page zoomed from ${before.zoom} to ${after.zoom}` : `zoom ${after.zoom}`);
    for (const box of after.boxes) {
      const was = before.boxes.find((entry) => entry.box === box.box);
      if (was && (was.top !== box.top || was.left !== box.left)) lines.push(`${box.box} scrolled from ${was.top} to ${box.top} of ${box.maxTop} px`);
    }
    if (lines.length === 2 && !after.boxes.some((box) => before.boxes.find((entry) => entry.box === box.box && (entry.top !== box.top || entry.left !== box.left)))) {
      lines.push("no box under the point changed its scroll position (a terminal or a canvas draws its own scrolling: judge it on the screenshot)");
    }
    if (after.selection && after.selection !== before.selection) lines.push(`the page's text selection is now "${after.selection}"`);
    return lines.join("; ");
  }

  /**
   * A gesture's target and point as words: the point in CSS px of the page, and where it is in the action's
   * screenshot (iOS: the whole screen, measured by the markers; elsewhere the page at its pixel ratio), so a
   * miss can be corrected from the screenshot.
   */
  private where(name: string, start: { point: Point; dpr: number }, anchor: NativeAnchor | null): string {
    const shot = this.device.kind === "ios" ? anchor?.shot : { x: Math.round(start.point.x * start.dpr), y: Math.round(start.point.y * start.dpr) };
    const pixel = shot ? `, pixel (${shot.x}, ${shot.y}) of the screenshot` : "";
    return `${name === "the page" ? "the page" : `"${name}"`} at (${start.point.x}, ${start.point.y}) CSS px${pixel}`;
  }

  /**
   * A one-finger swipe on a phone or tablet: the finger comes down on the target (its centre, or `at`), moves
   * `distance` CSS px in `direction` and lifts after a short rest, as a person scrolling would. The content
   * follows the finger: a swipe down brings what is above into view.
   */
  async swipe(direction: Direction, target: Target | undefined, options: GestureOptions): Promise<Action> {
    return this.act("swipe", `swipe ${direction}${target ? ` on ${describeTarget(target)}` : ""}`, async (entry) => {
      if (this.device.kind === "desktop") throw new ActionError("a desktop has no finger: turn the mouse wheel with qa wheel, or drag with qa drag");
      if (target) entry.target = { ...target };
      await this.onTestInstance();
      const aimed = await this.aim(target);
      const start = await this.resolvePoint(aimed.nonce, options.at);
      const before = await this.probe(start.point);
      const vertical = direction === "up" || direction === "down";
      const distance = options.distance ?? Math.round((vertical ? start.viewport.height : start.viewport.width) * 0.4);
      const ms = options.ms ?? Math.max(250, Math.min(900, Math.round(distance * 1.2)));
      let travelled = 0;
      const anchor = await this.nativeGesture(aimed.nonce, aimed.name, options.at, (anchor) => {
        const end = swipeEnd(anchor, direction, distance * anchor.scale, anchor.page, 6 * anchor.scale);
        travelled = Math.round(Math.hypot(end.x - anchor.x, end.y - anchor.y) / anchor.scale);
        return dragActions("touch", anchor, end, ms);
      }, NATIVE_TIMEOUT_MS + ms);
      await sleep(GESTURE_REST_MS);
      const after = await this.probe(start.point);
      const short = travelled + 2 < distance ? ` (${distance} asked; the finger stops at the edge of the page)` : "";
      return `swiped ${direction} with one finger on ${this.where(aimed.name, start, anchor)}: the finger moved ${travelled} CSS px ${direction} in ${ms} ms${short}\n${this.moved(before, after)}`;
    });
  }

  /**
   * The mouse wheel on a desktop, turned over the target: the content moves `amount` CSS px towards `direction`
   * (up: towards its top). Through WebDriver's wheel input; a driver without one gets wheel events dispatched by
   * script on the element there, and the output says so.
   */
  async wheel(direction: Direction, target: Target | undefined, options: GestureOptions): Promise<Action> {
    return this.act("wheel", `wheel ${direction}${target ? ` over ${describeTarget(target)}` : ""}`, async (entry) => {
      if (this.device.kind !== "desktop") throw new ActionError(`a ${this.device.kind === "ios" ? "iPhone or iPad" : "phone or tablet"} has no mouse wheel: scroll with one finger, qa swipe (the content follows the finger: swipe down to see what is above)`);
      if (target) entry.target = { ...target };
      await this.onTestInstance();
      const aimed = await this.aim(target);
      const start = await this.resolvePoint(aimed.nonce, options.at);
      const before = await this.probe(start.point);
      const amount = options.amount ?? 300;
      let via = "the mouse wheel (WebDriver wheel input)";
      try {
        await this.driver.actions(wheelActions(start.point, direction, amount));
      } catch (error) {
        const v = vector(direction);
        const sent = await this.driver.run<{ sent: number; target: string; taken: boolean }>(PAGE.gesture, "wheel", start.point.x, start.point.y, v.x * amount, v.y * amount);
        via = `NOT a real wheel: this browser's driver has no wheel input (${(error as Error).message.slice(0, 120)}), so ${sent.sent} wheel event(s) were dispatched by script on ${sent.target}${sent.taken ? " (the page took them)" : ""}; a script's wheel moves what listens for wheel events (a terminal) but no box the browser scrolls itself`;
      }
      await sleep(GESTURE_REST_MS);
      const after = await this.probe(start.point);
      return `turned the wheel ${direction} by ${amount} CSS px over ${this.where(aimed.name, start, null)} via ${via}\n${this.moved(before, after)}`;
    });
  }

  /**
   * Holds a finger still on the target (a long press) for `seconds`, then lifts it; on a desktop the mouse
   * button is held as long. What the page does with a long press (select a word, a context menu, the
   * browser's callout) is in the screenshot.
   */
  async hold(target: Target | undefined, options: GestureOptions): Promise<Action> {
    const seconds = options.seconds ?? 1;
    return this.act("hold", `hold ${seconds} s${target ? ` on ${describeTarget(target)}` : ""}`, async (entry) => {
      if (target) entry.target = { ...target };
      await this.onTestInstance();
      const aimed = await this.aim(target);
      const start = await this.resolvePoint(aimed.nonce, options.at);
      const before = await this.probe(start.point);
      const ms = Math.round(seconds * 1000);
      let how: string;
      let anchor: NativeAnchor | null = null;
      if (this.device.kind === "desktop") {
        await this.driver.actions(holdActions("mouse", start.point, ms));
        how = "held the mouse button";
      } else {
        anchor = await this.nativeGesture(aimed.nonce, aimed.name, options.at, (a) => holdActions("touch", a, ms), NATIVE_TIMEOUT_MS + ms);
        how = "held a finger still";
      }
      await sleep(GESTURE_REST_MS);
      const after = await this.probe(start.point);
      return `${how} ${seconds} s on ${this.where(aimed.name, start, anchor)} (${start.under}), then lifted it\n${this.moved(before, after)}`;
    });
  }

  /**
   * Drags from the target (its centre, or `at`) by `by` CSS px: a finger on phones and tablets (with `hold`, it
   * rests that long first, as a press that selects and then extends), the mouse with its button down on desktops.
   */
  async drag(target: Target | undefined, options: GestureOptions & { by: { dx: number; dy: number } }): Promise<Action> {
    return this.act("drag", `drag by ${options.by.dx},${options.by.dy}${target ? ` from ${describeTarget(target)}` : ""}`, async (entry) => {
      if (target) entry.target = { ...target };
      await this.onTestInstance();
      const aimed = await this.aim(target);
      const start = await this.resolvePoint(aimed.nonce, options.at);
      const before = await this.probe(start.point);
      const length = Math.hypot(options.by.dx, options.by.dy);
      const ms = options.ms ?? Math.max(300, Math.min(1500, Math.round(length * 2)));
      const holdMs = Math.round((options.seconds ?? 0) * 1000);
      let anchor: NativeAnchor | null = null;
      if (this.device.kind === "desktop") {
        const end = clampPoint({ x: start.point.x + options.by.dx, y: start.point.y + options.by.dy }, start.viewport, 2);
        await this.driver.actions(dragActions("mouse", start.point, end, ms, holdMs));
      } else {
        anchor = await this.nativeGesture(aimed.nonce, aimed.name, options.at, (anchor) => {
          const end = clampPoint({ x: anchor.x + options.by.dx * anchor.scale, y: anchor.y + options.by.dy * anchor.scale }, anchor.page, 4 * anchor.scale);
          return dragActions("touch", anchor, end, ms, holdMs);
        }, NATIVE_TIMEOUT_MS + ms + holdMs);
      }
      await sleep(GESTURE_REST_MS);
      const after = await this.probe(start.point);
      const tool = this.device.kind === "desktop" ? "the mouse" : "a finger";
      return `dragged ${tool} from ${this.where(aimed.name, start, anchor)} by ${options.by.dx},${options.by.dy} CSS px in ${ms} ms${holdMs ? ` after resting ${holdMs} ms` : ""}\n${this.moved(before, after)}`;
    });
  }

  /** A tap at a point inside the target (`at`), or a double tap / double click, through the gesture path. */
  private async tapAt(target: Target, aimed: { nonce: string | null; name: string }, at: At | undefined, double: boolean): Promise<string> {
    const start = await this.resolvePoint(aimed.nonce, at);
    const before = await this.probe(start.point);
    const word = double ? (this.device.kind === "desktop" ? "double-clicked" : "double-tapped") : this.device.kind === "desktop" ? "clicked" : "tapped";
    let anchor: NativeAnchor | null = null;
    if (this.device.kind === "desktop") await this.driver.actions(tapActions("mouse", start.point, double));
    else anchor = await this.nativeGesture(aimed.nonce, aimed.name, at, (a) => tapActions("touch", a, double));
    await sleep(GESTURE_REST_MS);
    const after = await this.probe(start.point);
    return `${word} ${this.where(aimed.name, start, anchor)} (${start.under})${this.device.kind === "desktop" ? "" : " with a touch"}\n${this.moved(before, after)}`;
  }

  /**
   * Answers the prompt Chrome draws over the page on Android (the clipboard's Block / Allow, notifications,
   * Android's own permission dialogs) by pressing its button in the native layer, as a finger would.
   */
  async prompt(answer: "allow" | "block"): Promise<Action> {
    return this.act("prompt", `answer the browser's prompt: ${answer}`, async () => {
      if (this.device.kind === "desktop") {
        throw new ActionError(
          "a desktop browser's own prompts (the microphone, notifications) are outside WebDriver's reach and cannot be answered here; give the permission in advance with qa stop, then qa start --grant microphone,notifications (Chrome, Edge, Firefox; TOOL.md), or judge the step blocked",
        );
      }
      if (this.device.kind === "ios") return this.inNative(() => this.answerIos(answer));
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

  /**
   * iOS (in the native context): an alert Safari or iOS shows over the page (the microphone, notifications) is
   * answered by touching its button; Safari's paste callout by touching Paste (allow) or, for block, by
   * touching the page beside it, which puts the callout away and lets the page's read fail.
   */
  private async answerIos(answer: "allow" | "block"): Promise<string> {
    const { screen } = await this.nativeScreen();
    const prompt = iosPrompt(screen);
    if (!prompt) throw new ActionError("no alert or paste callout is open over the page: nothing to answer (qa native look lists what is drawn outside the page)");
    const described = describePrompt(prompt);
    const label = answerButton(prompt, answer);
    if (!label && !(prompt.kind === "callout" && answer === "block")) throw new ActionError(`the prompt has no button that answers ${answer}: ${described}`);
    if (label) {
      const button = screen.elements.find((element) => element.label === label && (prompt.kind === "callout" || element.inAlert));
      if (!button) throw new ActionError(`the prompt's [${label}] button is gone: ${described}`);
      await this.driver.actions(tapActions("touch", middle(button.rect)), NATIVE_TIMEOUT_MS);
    } else {
      // A touch on the page well away from the callout: the top of the page's area, beside the address bar's end.
      const callout = screen.elements.find((element) => /^(allow )?paste$/i.test(element.label))!;
      const rect = await this.driver.windowRect(NATIVE_TIMEOUT_MS);
      const y = callout.rect.y > rect.height / 2 ? Math.round(rect.height * 0.25) : Math.round(rect.height * 0.75);
      await this.driver.actions(tapActions("touch", { x: Math.round(rect.width * 0.08), y }), NATIVE_TIMEOUT_MS);
    }
    await sleep(NATIVE_AFTER_TAP_MS);
    const still = iosPrompt((await this.nativeScreen().catch(() => null))?.screen ?? { elements: [], keyboard: false, alert: false });
    const done = label ? `pressed [${label}] on ${described}` : `touched the page beside ${described} to put the callout away (block)`;
    return `${done}${still ? `\nstill open over the page: ${describePrompt(still)}` : ""}`;
  }

  /**
   * Sets a date, time or datetime-local field the way a person does on this device: on iOS and Android the
   * field is touched and the browser's own picker is driven in the native layer (the calendar's month and day,
   * the time's wheels or spinners, then its Done or Set), and the field's value is read back. Where the picker
   * cannot be driven (a desktop, whose picker is browser UI out of WebDriver's reach, or a picker the driver does
   * not know), the value is set by script with the input and change events a choice sends, and the output says
   * so in capitals; --native makes that an error instead.
   */
  async pick(spec: string, target: Target, options: { native?: boolean } = {}): Promise<Action> {
    return this.act("pick", `pick ${JSON.stringify(spec)} in ${describeTarget(target)}`, async (entry) => {
      entry.target = { ...target };
      await this.onTestInstance();
      const found = await this.locate({ ...target, role: target.role ?? "textbox" });
      if (!found.found || !found.nonce) throw new ActionError(this.describeMiss(found, target));
      entry.target.matched = found.name;
      const name = found.name ?? "";
      const info = await this.driver.run<{ tag: string; type: string; value: string; min: string; max: string; now: Moment & { second: number } } | null>(PAGE.misc, "field-info", found.nonce);
      if (!info || info.tag !== "input" || !PICK_KINDS.includes(info.type)) throw new ActionError(`"${name}" is no date or time field (${info ? `${info.tag} type ${info.type || "text"}` : "gone"}): qa pick sets <input type=date|time|datetime-local>; type into other fields with qa type`);
      const kind = info.type as PickKind;
      let wanted: Moment;
      try {
        wanted = wantedMoment(spec, kind, info.now);
      } catch (error) {
        throw new ActionError((error as Error).message);
      }
      const value = formatValue(wanted, kind);
      const notes = [`the device's clock: ${formatValue(info.now, "datetime-local")}; wanted ${value}`];
      let how: string | null = null;
      let reason = "";
      if (this.device.kind === "desktop") reason = `a desktop browser's own picker is browser UI, outside WebDriver's reach`;
      else {
        try {
          await this.nativeTap(found.nonce, name);
          entry.target.via = "touch";
          how = this.device.kind === "ios" ? await this.inNative(() => this.pickIos(wanted, kind)) : await this.inNative(() => this.pickAndroid(wanted, kind));
        } catch (error) {
          if (!(error instanceof PickerUnavailable) && !(error instanceof NativeUnavailable)) throw error;
          reason = error.message;
          // Leave no native picker open behind the stand-in.
          await this.inNative(() => this.closePicker()).catch(() => undefined);
        }
      }
      const read = async () => {
        const again = await this.locate({ ...target, role: target.role ?? "textbox" });
        return again.found && again.nonce ? ((await this.driver.run<{ value: string } | null>(PAGE.misc, "field-info", again.nonce))?.value ?? null) : null;
      };
      if (how) {
        await sleep(GESTURE_REST_MS);
        const now = await read();
        notes.unshift(`picked ${value} in "${name}" with the device's own picker: ${how}`);
        notes.push(now === value ? `the field now holds ${now}` : `the field now holds ${now === null ? "nothing readable" : JSON.stringify(now)}, not ${value}: read the screenshot`);
        return notes.join("\n");
      }
      if (options.native) throw new ActionError(`the device's own picker could not be used: ${reason}`);
      const again = await this.locate({ ...target, role: target.role ?? "textbox" });
      if (!again.found || !again.nonce) throw new ActionError(`"${name}" is gone from the page`);
      await this.driver.run(PAGE.misc, "set-value", { nonce: again.nonce, value });
      await sleep(GESTURE_REST_MS);
      const now = await read();
      notes.unshift(`NOT THE DEVICE'S PICKER: set "${name}" to ${value} by script, with the input and change events a choice in the picker sends (${reason}); judge the picker itself only from what the screenshots show`);
      notes.push(`the field now holds ${now === null ? "nothing readable" : JSON.stringify(now)}`);
      return notes.join("\n");
    });
  }

  /**
   * iOS (native context, after the touch on the field): Safari's date picker popover. Its calendar is moved to
   * the month (Previous Month / Next Month) and the day's button touched; the Time button opens the wheels,
   * which are set (Appium adjusts a picker wheel as a finger turning it would); then a touch beside the popovers
   * closes them, as a person does.
   */
  private async pickIos(wanted: Moment, kind: PickKind): Promise<string> {
    const steps: string[] = [];
    // Ready once its parts are there (the popover's frame comes a moment before its calendar or wheels).
    let screen = await this.waitForNative((s) => s.elements.some((element) => element.id === "DatePicker.Show" || element.type === "PickerWheel" || (kind === "time" && /^\d{1,2}:\d{2}/.test(element.label))), "Safari's date and time picker");
    const touch = async (element: NativeElement) => {
      await this.driver.actions(tapActions("touch", middle(element.rect)), NATIVE_TIMEOUT_MS);
      await sleep(PICKER_STEP_MS);
      screen = (await this.nativeScreen()).screen;
    };
    if (kind !== "time") {
      for (let guard = 0; guard < 60; guard++) {
        const header = screen.elements.find((element) => element.id === "DatePicker.Show" || (element.type === "Button" && element.label === "Month"));
        const shown = header ? parseMonthYear(`${header.value ?? ""} ${header.label}`) : null;
        if (!shown) throw new PickerUnavailable("the calendar's month is not shown in Safari's picker");
        const steps_ = monthSteps(shown, wanted);
        if (!steps_) break;
        const arrow = screen.elements.find((element) => element.id === (steps_ > 0 ? "DatePicker.NextMonth" : "DatePicker.PreviousMonth"));
        if (!arrow) throw new PickerUnavailable(`the calendar has no ${steps_ > 0 ? "Next" : "Previous"} Month button`);
        await touch(arrow);
      }
      const dayButton = () => screen.elements.find((element) => element.type === "Button" && dayLabelMatches(element.label, wanted.day, wanted.month));
      if (!dayButton()) {
        await sleep(PICKER_STEP_MS);
        screen = (await this.nativeScreen()).screen;
      }
      const day = dayButton();
      if (!day) throw new PickerUnavailable(`the calendar has no button for ${wanted.day} ${MONTHS[wanted.month - 1]}`);
      await touch(day);
      steps.push(`touched ${MONTHS[wanted.month - 1]} ${wanted.day} in the calendar ("${day.label}")`);
    }
    if (kind !== "date") {
      if (!screen.elements.some((element) => element.type === "PickerWheel")) {
        const chip = screen.elements.find((element) => element.type === "Button" && /^\d{1,2}:\d{2}(\s?[AP]M)?$/i.test(element.label));
        if (!chip) throw new PickerUnavailable("Safari's picker shows no time button to open its wheels");
        // The button spans the whole Time row; the chip a finger presses is drawn at its right end.
        await this.driver.actions(tapActions("touch", { x: Math.round(chip.rect.x + chip.rect.width - Math.min(50, chip.rect.width / 4)), y: middle(chip.rect).y }), NATIVE_TIMEOUT_MS);
        await sleep(PICKER_STEP_MS);
        steps.push(`opened the time wheels (the "${chip.label}" button)`);
      }
      let wheels: string[] = [];
      for (let attempt = 0; attempt < 6 && wheels.length < 2; attempt++) {
        if (attempt) await sleep(500);
        wheels = await this.driver.findXPath("//XCUIElementTypePickerWheel", NATIVE_TIMEOUT_MS);
      }
      if (wheels.length < 2) throw new PickerUnavailable(`Safari's time picker has ${wheels.length} wheel(s), not hours and minutes`);
      const values = await Promise.all(wheels.map((wheel) => this.driver.attribute(wheel, "value", NATIVE_TIMEOUT_MS).catch(() => "")));
      const half = values.findIndex((text) => /^(AM|PM)$/i.test(String(text ?? "").trim()));
      const h12 = hour12(wanted.hour);
      const hourText = half >= 0 ? String(h12.hour) : String(wanted.hour).padStart(2, "0");
      await this.driver.sendKeys(wheels[0]!, hourText, NATIVE_TIMEOUT_MS);
      await this.driver.sendKeys(wheels[1]!, String(wanted.minute).padStart(2, "0"), NATIVE_TIMEOUT_MS);
      if (half >= 0) await this.driver.sendKeys(wheels[half]!, h12.half, NATIVE_TIMEOUT_MS);
      const now = await Promise.all(wheels.map((wheel) => this.driver.attribute(wheel, "value", NATIVE_TIMEOUT_MS).catch(() => "?")));
      steps.push(`turned the wheels to ${now.join(", ")}`);
    }
    await this.closePicker();
    steps.push("closed the picker with a touch beside it");
    return steps.join("; ");
  }

  /**
   * Android (native context, after the touch on the field): Chrome's "Set date and time" dialog. Its spinners
   * (NumberPicker: month, day and year under the date, hour and minute under the time) are stepped with touches
   * on their next or previous number, as a person taps them, the shorter way round, until each shows the wanted
   * value; then Set. Typing into a spinner's number is not used: Chrome's spinners do not keep a value set that way.
   */
  private async pickAndroid(wanted: Moment, kind: PickKind): Promise<string> {
    await this.waitForNative((s) => s.elements.some((element) => /^(Set|OK|Done)$/i.test(element.label)), "Chrome's date and time dialog");
    const steps: string[] = [];
    const spinner = (group: string, n: number) => `(//*[contains(@resource-id,"${group}")]//android.widget.NumberPicker)[${n}]`;
    const read = async (group: string, n: number) => {
      const [field] = await this.driver.findXPath(`${spinner(group, n)}//android.widget.EditText`, NATIVE_TIMEOUT_MS);
      return field ? String((await this.driver.attribute(field, "text", NATIVE_TIMEOUT_MS).catch(() => "")) ?? "").trim() : null;
    };
    const step = async (group: string, n: number, forward: boolean) => {
      const buttons = await this.driver.findXPath(`${spinner(group, n)}/android.widget.Button`, NATIVE_TIMEOUT_MS);
      const field = (await this.driver.findXPath(`${spinner(group, n)}//android.widget.EditText`, NATIVE_TIMEOUT_MS))[0];
      if (!field) throw new PickerUnavailable("a spinner of Chrome's dialog lost its number");
      const fieldY = (await this.driver.rect(field, NATIVE_TIMEOUT_MS)).y;
      for (const button of buttons) {
        const r = await this.driver.rect(button, NATIVE_TIMEOUT_MS);
        if ((r.y > fieldY) === forward) {
          await this.driver.actions(tapActions("touch", middle(r)), NATIVE_TIMEOUT_MS);
          await sleep(250);
          return;
        }
      }
      throw new PickerUnavailable(`a spinner of Chrome's dialog has no ${forward ? "next" : "previous"} value (at its end)`);
    };
    // Steps a spinner to `goal` among `size` values that wrap (or do not: a year), reading it again as it goes.
    const turn = async (group: string, n: number, what: string, toIndex: (text: string) => number | null, goal: number, size: number | null) => {
      for (let guard = 0; guard < 80; guard++) {
        const text = await read(group, n);
        if (text === null) throw new PickerUnavailable(`Chrome's dialog has no ${what} spinner`);
        const now = toIndex(text);
        if (now === null) throw new PickerUnavailable(`the ${what} spinner shows "${text}", which the driver cannot read`);
        if (now === goal) return steps.push(`${what} ${text}`);
        let delta = goal - now;
        if (size) {
          delta = ((delta % size) + size) % size;
          if (delta > size / 2) delta -= size;
        }
        await step(group, n, delta > 0);
      }
      throw new PickerUnavailable(`the ${what} spinner did not reach its value`);
    };
    const monthIndex = (text: string) => {
      const at = MONTHS.findIndex((name) => name.toLowerCase().startsWith(text.toLowerCase().replace(/\.$/, "").slice(0, 3)));
      return at >= 0 ? at + 1 : null;
    };
    const number = (text: string) => (/^\d+$/.test(text) ? Number(text) : null);
    if (kind !== "time") {
      // The date's spinners in the dialog's order: which is which is read from what they show.
      const order: ("month" | "day" | "year")[] = [];
      for (let n = 1; n <= 3; n++) {
        const text = await read("date_picker", n);
        if (text === null) break;
        order.push(monthIndex(text) && !/^\d/.test(text) ? "month" : /^\d{4}$/.test(text) ? "year" : "day");
      }
      if (order.length !== 3) throw new PickerUnavailable(`Chrome's date has ${order.length} spinner(s), not month, day and year`);
      const days = new Date(Date.UTC(wanted.year, wanted.month, 0)).getUTCDate();
      await turn("date_picker", order.indexOf("year") + 1, "year", number, wanted.year, null);
      await turn("date_picker", order.indexOf("month") + 1, "month", monthIndex, wanted.month, 12);
      await turn("date_picker", order.indexOf("day") + 1, "day", number, wanted.day, days);
    }
    if (kind !== "date") {
      const half = await read("time_picker", 3);
      const twelve = half !== null && /^(AM|PM)$/i.test(half);
      const h12 = hour12(wanted.hour);
      if (twelve) await turn("time_picker", 1, "hour", (text) => (number(text) ?? 0) % 12, h12.hour % 12, 12);
      else await turn("time_picker", 1, "hour", number, wanted.hour, 24);
      await turn("time_picker", 2, "minute", number, wanted.minute, 60);
      if (twelve) await turn("time_picker", 3, "half", (text) => (/^AM$/i.test(text) ? 0 : 1), h12.half === "AM" ? 0 : 1, 2);
    }
    const [ok] = await this.driver.findXPath('//android.widget.Button[@text="Set" or @text="SET" or @text="OK" or @text="Done"]', NATIVE_TIMEOUT_MS);
    if (!ok) throw new PickerUnavailable("Chrome's dialog has no Set button");
    await this.driver.actions(tapActions("touch", middle(await this.driver.rect(ok, NATIVE_TIMEOUT_MS))), NATIVE_TIMEOUT_MS);
    await sleep(PICKER_STEP_MS);
    return `stepped Chrome's spinners to ${steps.join(", ")} and pressed Set`;
  }

  /** Waits (a few seconds) for the native screen to show what `ready` looks for; a PickerUnavailable when it never does. */
  private async waitForNative(ready: (screen: NativeScreen) => boolean, what: string): Promise<NativeScreen> {
    let screen: NativeScreen | null = null;
    for (let attempt = 0; attempt < 6; attempt++) {
      if (attempt) await sleep(500);
      screen = (await this.nativeScreen()).screen;
      if (ready(screen)) return screen;
    }
    const seen = screen ? nativeOverlay(screen, this.platform()).slice(0, 8).map(describeElement).join(", ") : "";
    throw new PickerUnavailable(`${what} did not open after the touch${seen ? ` (outside the page: ${seen})` : ""}`);
  }

  /** Closes a native picker left open: Safari's popovers with a touch beside them, Android's dialog with Cancel. */
  private async closePicker(): Promise<void> {
    const { screen } = await this.nativeScreen();
    if (this.device.kind === "android") {
      const cancel = screen.elements.find((element) => /^cancel$/i.test(element.label));
      if (cancel) await this.driver.actions(tapActions("touch", middle(cancel.rect)), NATIVE_TIMEOUT_MS);
      return;
    }
    const popovers = screen.elements.filter((element) => /^(DatePicker|Picker|Toolbar)$/.test(element.type)).map((element) => element.rect);
    const region = screen.elements.find((element) => element.id === "PopoverDismissRegion" || /dismiss popup/i.test(element.label));
    if (!region) return;
    const rect = await this.driver.windowRect(NATIVE_TIMEOUT_MS);
    const inside = (p: Point) => popovers.some((r) => p.x >= r.x - 8 && p.x <= r.x + r.width + 8 && p.y >= r.y - 8 && p.y <= r.y + r.height + 8);
    const candidates = [0.12, 0.2, 0.85, 0.75, 0.3].map((f) => ({ x: Math.round(rect.width * 0.5), y: Math.round(rect.height * f) }));
    const spot = candidates.find((p) => !inside(p)) ?? candidates[0]!;
    await this.driver.actions(tapActions("touch", spot), NATIVE_TIMEOUT_MS);
    await sleep(PICKER_STEP_MS);
    // A calendar popover under the wheels may stay: its Done closes it.
    const left = (await this.nativeScreen()).screen;
    const done = left.elements.find((element) => element.type === "Button" && /^done$/i.test(element.label) && popovers.length > 0);
    if (done && left.elements.some((element) => element.type === "DatePicker")) await this.driver.actions(tapActions("touch", middle(done.rect)), NATIVE_TIMEOUT_MS);
  }

  /** The native screen's elements outside the page, the keyboard and the system bars (Appium's page source). */
  private async nativeScreen(): Promise<{ screen: NativeScreen; xml: string }> {
    const xml = await this.driver.source(NATIVE_TIMEOUT_MS);
    return { screen: parseNative(xml, this.platform()), xml };
  }

  private platform(): NativePlatform {
    if (this.device.kind === "desktop") throw new ActionError("a desktop browser's own UI (its prompts, its pickers) is outside WebDriver's reach: qa native works on phones and tablets");
    return this.device.kind;
  }

  /**
   * Lists what the browser and the system draw outside the page (a permission alert, a date picker, the paste
   * callout, a file chooser): each element with its label, as `qa native tap` names it. The full native source
   * is kept in the evidence (logs/native-<n>.txt).
   */
  async nativeLook(): Promise<Action> {
    return this.act(
      "native",
      "looked at the native screen around the page",
      async (entry) => {
        const platform = this.platform();
        const { screen, xml } = await this.inNative(() => this.nativeScreen());
        const file = this.evidence.writeLog(`native-${entry.n}`, xml.split("\n"));
        const overlay = nativeOverlay(screen, platform);
        const lines = [`the native screen around the page: ${overlay.length ? `${overlay.length} element(s) outside the page and the browser's frame` : "nothing outside the page and the browser's frame"}${screen.keyboard ? "; the on-screen keyboard shows" : ""}${screen.alert ? "; an alert is open" : ""}`];
        for (const element of overlay.slice(0, NATIVE_LIST_MAX)) lines.push(`  ${describeElement(element)}`);
        if (overlay.length > NATIVE_LIST_MAX) lines.push(`  ... and ${overlay.length - NATIVE_LIST_MAX} more (the full source: ${file})`);
        lines.push(`native source kept in ${file}`);
        return lines.join("\n");
      },
      { settle: false, whole: true },
    );
  }

  /**
   * Touches an element of the native screen by its label (`qa native look` lists them): a button of Safari's
   * or Chrome's own UI, a picker's Done, the paste callout's Paste, a file chooser's Cancel. It is a real touch
   * at the element's middle.
   */
  async nativeTouch(label: string, nth?: number): Promise<Action> {
    return this.act(
      "native-tap",
      `native tap "${label}"`,
      async (entry) => {
        entry.target = { text: label, nth, via: "touch" };
        const platform = this.platform();
        return this.inNative(async () => {
          const { screen } = await this.nativeScreen();
          const { pick, matches } = matchNative(nativeOverlay(screen, platform), label, nth);
          if (!pick) {
            const near = nativeOverlay(screen, platform).slice(0, 12).map(describeElement).join(", ");
            throw new ActionError(`no native element "${label}" outside the page${near ? `; there are: ${near}` : " (nothing is drawn outside the page now)"}`);
          }
          entry.target!.matched = pick.label;
          const at = middle(pick.rect);
          await this.driver.actions(tapActions("touch", at), NATIVE_TIMEOUT_MS);
          await sleep(NATIVE_AFTER_TAP_MS);
          const after = await this.nativeScreen().catch(() => null);
          const left = after ? nativeOverlay(after.screen, platform) : [];
          const notes = [`touched the native ${describeElement(pick)} at (${at.x}, ${at.y})`];
          if (matches.length > 1) notes.push(`${matches.length} matches, took #${(nth ?? 0) + 1} (--nth): ${matches.slice(0, 6).map(describeElement).join(", ")}`);
          notes.push(left.length ? `outside the page now: ${left.slice(0, 10).map(describeElement).join(", ")}` : "nothing is drawn outside the page any more");
          return notes.join("\n");
        });
      },
      { whole: true },
    );
  }

  /**
   * Android's Back key for a native chooser or dialog over the page (the file chooser, a picker): refused
   * while nothing native is open, because Back would then reach the page and leave it.
   */
  async nativeBack(): Promise<Action> {
    return this.act(
      "native-back",
      "native back",
      async () => {
        if (this.device.kind !== "android") throw new ActionError("qa native back is Android's Back key; on iOS tap the native element's own Cancel or Done (qa native tap Cancel)");
        return this.inNative(async () => {
          const { screen } = await this.nativeScreen();
          const overlay = nativeOverlay(screen, "android");
          if (!overlay.length && !(await this.chooserOpen())) throw new ActionError("nothing native is open over the page: Back would reach the page itself (use qa back for the browser's history)");
          await this.driver.pressKeycode(4, NATIVE_TIMEOUT_MS);
          await sleep(NATIVE_AFTER_TAP_MS);
          const after = await this.nativeScreen().catch(() => null);
          const left = after ? nativeOverlay(after.screen, "android") : [];
          return `pressed Back over ${overlay.slice(0, 4).map(describeElement).join(", ") || "the native screen"}\n${left.length ? `outside the page now: ${left.slice(0, 10).map(describeElement).join(", ")}` : "nothing is drawn outside the page any more"}`;
        });
      },
      { whole: true },
    );
  }

  /** Android: another app (the file chooser, the camera) is in front of the browser. */
  private async chooserOpen(): Promise<boolean> {
    const pkg = await this.driver.mobile<string>("getCurrentPackage", {}, NATIVE_TIMEOUT_MS).catch(() => "");
    return Boolean(pkg) && !/chrome|browser|samsung\.android\.app\.sbrowser/.test(pkg);
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
  async shot(native = false): Promise<Action> {
    if (native && this.device.kind === "desktop") throw new Error("qa shot --native is for phones and tablets: a desktop's screenshot is the page");
    const what = native && this.device.kind === "android" ? "took a screenshot of the whole screen (the native one: the on-screen keyboard, Chrome's bars and prompts included)" : "took a screenshot";
    return this.act("shot", what, () => Promise.resolve(undefined), { settle: false, prompt: true, whole: native });
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
