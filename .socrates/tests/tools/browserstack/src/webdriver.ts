import { HUB_URL } from "./config";
import { basicAuth, type Account } from "./account";

/** The W3C element reference key; Appium's Safari driver still answers the legacy JSON wire key. */
const ELEMENT = "element-6066-11e4-a52e-4f735466cecf";
const elementId = (value: Record<string, string>) => value[ELEMENT] ?? value.ELEMENT!;

export class WebDriverError extends Error {
  constructor(
    readonly code: string,
    message: string,
  ) {
    super(`${code}: ${message.split("\n")[0]!.slice(0, 400)}`);
  }
}

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export type ActionSequence = { type: string; id: string; parameters?: Record<string, unknown>; actions: Record<string, unknown>[] };

/** A minimal W3C WebDriver client for BrowserStack's hub, plus the Appium endpoints real devices add. */
export class WebDriver {
  private constructor(
    readonly sessionId: string,
    readonly capabilities: Record<string, unknown>,
    private readonly auth: string,
  ) {}

  static async create(account: Account, capabilities: Record<string, unknown>, timeoutMs: number): Promise<WebDriver> {
    const auth = basicAuth(account);
    const value = await request(auth, "POST", `${HUB_URL}/session`, { capabilities: { alwaysMatch: capabilities } }, timeoutMs);
    const session = value as { sessionId?: string; capabilities?: Record<string, unknown> };
    if (!session.sessionId) throw new WebDriverError("session not created", JSON.stringify(value).slice(0, 400));
    return new WebDriver(session.sessionId, session.capabilities ?? {}, auth);
  }

  /** A session another process started (the daemon), to close it when that process cannot. */
  static attach(account: Account, sessionId: string): WebDriver {
    return new WebDriver(sessionId, {}, basicAuth(account));
  }

  private call(method: string, path: string, body?: unknown, timeoutMs = 120_000): Promise<unknown> {
    return request(this.auth, method, `${HUB_URL}/session/${this.sessionId}${path}`, body, timeoutMs);
  }

  async navigate(url: string): Promise<void> {
    await this.call("POST", "/url", { url });
  }
  async currentUrl(): Promise<string> {
    return (await this.call("GET", "/url")) as string;
  }
  async back(): Promise<void> {
    await this.call("POST", "/back", {});
  }
  async forward(): Promise<void> {
    await this.call("POST", "/forward", {});
  }
  async refresh(): Promise<void> {
    await this.call("POST", "/refresh", {});
  }
  async setScriptTimeout(ms: number): Promise<void> {
    await this.call("POST", "/timeouts", { script: ms });
  }
  async windowRect(timeoutMs?: number): Promise<Rect> {
    return (await this.call("GET", "/window/rect", undefined, timeoutMs)) as Rect;
  }
  async maximize(): Promise<void> {
    await this.call("POST", "/window/maximize", {});
  }

  /** Runs a page script (src/browser/*.js: a function `main`) and returns its result. */
  async run<T>(source: string, ...args: unknown[]): Promise<T> {
    return (await this.call("POST", "/execute/sync", { script: `${source}\nreturn main.apply(null, arguments);`, args })) as T;
  }
  /** Runs a page script whose `main` returns a promise, bounded by the session's script timeout. */
  async runAsync<T>(source: string, ...args: unknown[]): Promise<T> {
    const script = `${source}\nvar done = arguments[arguments.length - 1];\nmain.apply(null, Array.prototype.slice.call(arguments, 0, -1)).then(done, function (e) { done({ __error: String((e && e.message) || e) }); });`;
    return (await this.call("POST", "/execute/async", { script, args }, 180_000)) as T;
  }
  /** BrowserStack's executor commands (session status, annotations). */
  async executor(action: string, args: Record<string, unknown>): Promise<void> {
    await this.call("POST", "/execute/sync", { script: `browserstack_executor: ${JSON.stringify({ action, arguments: args })}`, args: [] });
  }

  async find(css: string): Promise<string> {
    const value = (await this.call("POST", "/element", { using: "css selector", value: css })) as Record<string, string>;
    return elementId(value);
  }
  /** The element a W3C action may take as its origin. */
  static origin(element: string): Record<string, string> {
    return { [ELEMENT]: element };
  }
  async findXPath(xpath: string, timeoutMs?: number): Promise<string[]> {
    const value = (await this.call("POST", "/elements", { using: "xpath", value: xpath }, timeoutMs)) as Record<string, string>[];
    return value.map(elementId);
  }
  async click(element: string, timeoutMs?: number): Promise<void> {
    await this.call("POST", `/element/${element}/click`, {}, timeoutMs);
  }
  async clear(element: string): Promise<void> {
    await this.call("POST", `/element/${element}/clear`, {});
  }
  async sendKeys(element: string, text: string, timeoutMs?: number): Promise<void> {
    await this.call("POST", `/element/${element}/value`, { text }, timeoutMs);
  }
  async activeElement(): Promise<string> {
    const value = (await this.call("GET", "/element/active")) as Record<string, string>;
    return elementId(value);
  }
  async rect(element: string, timeoutMs?: number): Promise<Rect> {
    return (await this.call("GET", `/element/${element}/rect`, undefined, timeoutMs)) as Rect;
  }
  async attribute(element: string, name: string, timeoutMs?: number): Promise<string | null> {
    return (await this.call("GET", `/element/${element}/attribute/${encodeURIComponent(name)}`, undefined, timeoutMs)) as string | null;
  }
  async actions(actions: ActionSequence[], timeoutMs?: number): Promise<void> {
    await this.call("POST", "/actions", { actions }, timeoutMs);
    await this.call("DELETE", "/actions", undefined, timeoutMs).catch(() => undefined);
  }
  async screenshot(timeoutMs?: number): Promise<Buffer> {
    return Buffer.from((await this.call("GET", "/screenshot", undefined, timeoutMs)) as string, "base64");
  }

  /** Appium (real devices). */
  async setOrientation(orientation: "PORTRAIT" | "LANDSCAPE"): Promise<void> {
    await this.call("POST", "/orientation", { orientation });
  }
  /** Types into whatever the device has focused, through its on-screen keyboard (Appium, native context). */
  async deviceKeys(text: string): Promise<void> {
    await this.call("POST", "/keys", { value: [...text] });
  }
  /**
   * Closes the on-screen keyboard the platform's way (Appium). On Android this also sends Back when it cannot
   * tell the keyboard closed, which an open page dialog takes as its own close: use it on iOS only.
   */
  async hideKeyboard(): Promise<void> {
    await this.call("POST", "/appium/device/hide_keyboard", {});
  }
  /** Whether the on-screen keyboard shows (Appium); null when the driver cannot tell. */
  async isKeyboardShown(timeoutMs?: number): Promise<boolean | null> {
    const value = await this.call("GET", "/appium/device/is_keyboard_shown", undefined, timeoutMs).catch(() => null);
    return typeof value === "boolean" ? value : null;
  }
  /** Presses an Android key (4 is Back) as the hardware or navigation bar does (Appium). */
  async pressKeycode(keycode: number, timeoutMs?: number): Promise<void> {
    await this.call("POST", "/appium/device/press_keycode", { keycode }, timeoutMs);
  }
  /** Appium's driver settings (enableMultiWindows on Android: the keyboard's window in the source too). */
  async settings(values: Record<string, unknown>): Promise<void> {
    await this.call("POST", "/appium/settings", { settings: values });
  }
  /** The native UI hierarchy as XML (Appium, native context). */
  async source(timeoutMs?: number): Promise<string> {
    return String(await this.call("GET", "/source", undefined, timeoutMs));
  }
  async contexts(): Promise<string[]> {
    return (await this.call("GET", "/contexts")) as string[];
  }
  async context(timeoutMs?: number): Promise<string> {
    return (await this.call("GET", "/context", undefined, timeoutMs)) as string;
  }
  async setContext(name: string, timeoutMs?: number): Promise<void> {
    await this.call("POST", "/context", { name }, timeoutMs);
  }

  /** An Appium `mobile:` command or another executor script with one argument object (native context). */
  async mobile<T>(command: string, args: Record<string, unknown> = {}, timeoutMs?: number): Promise<T> {
    return (await this.call("POST", "/execute/sync", { script: `mobile: ${command}`, args: [args] }, timeoutMs)) as T;
  }
  /** The text of an open alert (W3C), or null when none is open. */
  async alertText(timeoutMs?: number): Promise<string | null> {
    return this.call("GET", "/alert/text", undefined, timeoutMs).then(
      (value) => String(value ?? ""),
      (error: Error) => {
        if (error instanceof WebDriverError && /no such alert/i.test(error.code + error.message)) return null;
        throw error;
      },
    );
  }

  async quit(): Promise<void> {
    await this.call("DELETE", "");
  }
}

async function request(auth: string, method: string, url: string, body: unknown, timeoutMs: number): Promise<unknown> {
  const response = await fetch(url, {
    method,
    headers: { authorization: auth, "content-type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(timeoutMs),
  });
  const text = await response.text();
  let parsed: { value?: unknown; sessionId?: string } = {};
  try {
    parsed = JSON.parse(text) as typeof parsed;
  } catch {
    throw new WebDriverError(`http ${response.status}`, text.slice(0, 300));
  }
  const value = parsed.value as { error?: string; message?: string } | null | undefined;
  if (!response.ok || (value && typeof value === "object" && "error" in value && value.error)) {
    throw new WebDriverError(value?.error ?? `http ${response.status}`, value?.message ?? text.slice(0, 300));
  }
  // The legacy JSON wire answer of a new session carries the id beside the value. Only that answer: Appium's
  // Android driver puts the session id beside every value, and a rect read that way lost its x and y.
  if (method === "POST" && /\/session$/.test(url) && parsed.sessionId && value && typeof value === "object" && !("sessionId" in value)) return { sessionId: parsed.sessionId, capabilities: value };
  return parsed.value;
}
