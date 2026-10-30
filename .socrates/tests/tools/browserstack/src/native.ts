/**
 * The native screen around the page: what Appium's page source (the native context) shows outside the
 * browser's web view. Safari's and Chrome's own UI lives there and nowhere in the page: permission
 * alerts, the date and time pickers, the paste callout, the file chooser, Android's permission dialogs.
 * `qa native look` lists it, `qa native tap` touches one of its elements, `qa prompt` and `qa pick` use it.
 */
import type { Rect } from "./webdriver";

export type NativePlatform = "ios" | "android";

export interface NativeElement {
  /** The element's class without its platform prefix: Button, StaticText, PickerWheel, TextView, ... */
  type: string;
  /** What a person reads on it: the label (iOS) or the text, else the content description (Android). */
  label: string;
  /** A value beside the label (iOS value, a picker wheel's place). */
  value?: string;
  /** The Android resource id without its package, or the iOS identifier (name) when it differs from the label. */
  id?: string;
  rect: Rect;
  enabled: boolean;
  /** Its place among all elements of the source, for XPath (`(//*)[n]`, from 1). */
  index: number;
  /** iOS: inside an alert (XCUIElementTypeAlert). */
  inAlert?: boolean;
}

export interface NativeScreen {
  /** The elements outside the page, the keyboard and the system bars that carry a label or a value. */
  elements: NativeElement[];
  /** An on-screen keyboard shows. */
  keyboard: boolean;
  /** iOS: an alert (XCUIElementTypeAlert) is open; its texts and buttons are among the elements. */
  alert: boolean;
}

const ENTITIES: Record<string, string> = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'" };
export const decodeXml = (value: string): string =>
  value.replace(/&(#x[0-9a-f]+|#\d+|\w+);/gi, (whole, code: string) => {
    if (code[0] === "#") return String.fromCodePoint(code[1] === "x" || code[1] === "X" ? parseInt(code.slice(2), 16) : parseInt(code.slice(1), 10));
    return ENTITIES[code] ?? whole;
  });

/** The page itself: its subtree is the web content, which the page scripts already see. */
const PAGE = /(^|\.)(WebView|XCUIElementTypeWebView)$/;
/** The on-screen keyboard. */
const KEYBOARD_IOS = /^XCUIElementTypeKeyboard$/;
const KEYBOARD_PACKAGE = /inputmethod/;
/** Elements kept without a label (pickers and their parts, alerts, sheets). */
const STRUCTURAL = /^(DatePicker|Picker|PickerWheel|Alert|Sheet|NumberPicker|TimePicker|CalendarView)$/;
/** Android's status and navigation bars. */
const SYSTEM_PACKAGE = /^com\.android\.systemui$/;

/** Android bounds="[x1,y1][x2,y2]". */
function androidRect(bounds: string | undefined): Rect {
  const m = /\[(-?\d+),(-?\d+)\]\[(-?\d+),(-?\d+)\]/.exec(bounds ?? "");
  if (!m) return { x: 0, y: 0, width: 0, height: 0 };
  const [x1, y1, x2, y2] = m.slice(1).map(Number) as [number, number, number, number];
  return { x: x1, y: y1, width: x2 - x1, height: y2 - y1 };
}

/**
 * Parses a native page source (XCUITest or UiAutomator2) into the elements outside the page, the keyboard
 * and the system bars. Elements that say they are not visible (iOS `visible="false"`, Android
 * `displayed="false"`) or have no size are left out.
 */
export function parseNative(xml: string, platform: NativePlatform): NativeScreen {
  const elements: NativeElement[] = [];
  let keyboard = false;
  let alert = false;
  // For each open element: whether its subtree is out (page, keyboard, system bars).
  const stack: boolean[] = [];
  // For each open element: whether it is an alert or inside one.
  const alerts: boolean[] = [];
  const tag = /<(\/?)([A-Za-z_][\w.$-]*)((?:\s+[\w:-]+="[^"]*")*)\s*(\/?)>/g;
  let index = 0;
  for (let match = tag.exec(xml); match; match = tag.exec(xml)) {
    const [, closing, name, rawAttributes, selfClosing] = match;
    if (closing) {
      stack.pop();
      alerts.pop();
      continue;
    }
    if (name === "hierarchy" || name === "AppiumAUT" || name?.startsWith("?")) {
      if (!selfClosing) {
        stack.push(false);
        alerts.push(false);
      }
      continue;
    }
    index += 1;
    const attributes: Record<string, string> = {};
    for (const [, key, value] of rawAttributes!.matchAll(/([\w:-]+)="([^"]*)"/g)) attributes[key!] = decodeXml(value!);
    const className = (platform === "ios" ? attributes.type : attributes.class) || name!;
    const parentOut = stack.some(Boolean);
    const isKeyboard = platform === "ios" ? KEYBOARD_IOS.test(className) : KEYBOARD_PACKAGE.test(attributes.package ?? "");
    if (isKeyboard && attributes.visible !== "false" && attributes.displayed !== "false") keyboard = true;
    const out = parentOut || PAGE.test(className) || isKeyboard || (platform === "android" && SYSTEM_PACKAGE.test(attributes.package ?? ""));
    const isAlert = platform === "ios" && className === "XCUIElementTypeAlert" && !parentOut;
    if (isAlert) alert = true;
    const inAlert = isAlert || alerts.some(Boolean);
    if (!out) {
      const rect =
        platform === "ios"
          ? { x: Number(attributes.x) || 0, y: Number(attributes.y) || 0, width: Number(attributes.width) || 0, height: Number(attributes.height) || 0 }
          : androidRect(attributes.bounds);
      // XCUITest's `visible` is unreliable in Safari (the web view and Safari's bars say false while shown): iOS
      // keeps every element with a size; Android's `displayed` is kept.
      const shown = (platform === "ios" || attributes.displayed !== "false") && rect.width > 0 && rect.height > 0;
      const label = (platform === "ios" ? attributes.label || attributes.name : attributes.text || attributes["content-desc"]) || "";
      const value = platform === "ios" ? attributes.value : undefined;
      // A picker, its wheels, a toolbar or an alert counts without a label: their parts are what a step looks for.
      const structural = STRUCTURAL.test(className.replace(/^XCUIElementType/, "").replace(/^.*\./, ""));
      if (shown && (label.trim() || value?.trim() || structural)) {
        const element: NativeElement = {
          type: className.replace(/^XCUIElementType/, "").replace(/^.*\./, ""),
          label: label.trim(),
          rect,
          enabled: attributes.enabled !== "false",
          index,
        };
        if (value?.trim() && value.trim() !== label.trim()) element.value = value.trim();
        if (inAlert) element.inAlert = true;
        const id = platform === "ios" ? (attributes.name && attributes.name !== label.trim() ? attributes.name : undefined) : attributes["resource-id"]?.replace(/^.*:id\//, "");
        if (id) element.id = id;
        elements.push(element);
      }
    }
    if (!selfClosing) {
      stack.push(out);
      alerts.push(inAlert);
    }
  }
  return { elements, keyboard, alert };
}

/** Types a person presses (buttons, menu items, cells, links, switches, picker wheels and fields). */
const PRESSABLE = /^(Button|MenuItem|Cell|Link|Switch|Toggle|PickerWheel|TextField|SecureTextField|SegmentedControl|Image|Key|CheckBox|RadioButton|ImageButton|EditText|NumberPicker|Spinner|CheckedTextView|Tab|Other|View|TextView|StaticText|LinearLayout|FrameLayout)$/;

/** The parts of Safari's and Chrome's own frame that are always there: never what a step is about. */
const BROWSER_FRAME_IOS = /^(Address|URL|Back|Forward|Share|Tabs|Bookmarks|Page Menu|Show Tab Overview|Reader|Reload|refresh|Stop|Show page menu|Tab Bar|Page Format Menu|Format Options|Lock|Smart Search Field|Hide toolbar|Show Sidebar|Toggle sidebar|New tab|add)$/i;
const BROWSER_FRAME_IOS_ID = /^(TabBarItemTitle|BackButton|ForwardButton|MoreMenuButton|ReloadButton|StopButton|TabOverviewButton|ShareButton|BookmarksButton|SidebarButton|NewTabButton|sidebar\..*|plus|CapsuleNavigationBar.*|SafariWindow.*|TabDocument.*|CapsuleViewController|UnifiedBar.*)$/;
const BROWSER_FRAME_ANDROID_ID = /^(url_bar|search_box_text|toolbar|location_bar|menu_button|home_button|tab_switcher_button|optional_toolbar_button|location_bar_status_icon|security_button|url_action_container|toolbar_buttons|control_container|tab_strip|bottom_container|coordinator)$/;

/** Whether an element is part of the browser's permanent frame (address bar, tab buttons). */
export function browserFrame(element: NativeElement, platform: NativePlatform): boolean {
  if (platform === "android") return Boolean(element.id && BROWSER_FRAME_ANDROID_ID.test(element.id));
  if ((element.id && BROWSER_FRAME_IOS_ID.test(element.id)) || BROWSER_FRAME_IOS_ID.test(element.label)) return true;
  // Scroll bars are the page's, drawn by WebKit beside the web view.
  return BROWSER_FRAME_IOS.test(element.label) || / scroll bar, /.test(element.label) || /^(TabBar|NavigationBar|Window|Application)$/.test(element.type);
}

/** What `qa native look` lists: the labelled elements outside the page, the browser's frame left out. */
export function nativeOverlay(screen: NativeScreen, platform: NativePlatform): NativeElement[] {
  const kept = screen.elements.filter((element) => !browserFrame(element, platform));
  // A text inside a button that already says it (a calendar's "30" in "Friday, 30 October") adds nothing.
  const within = (inner: NativeElement, outer: NativeElement) =>
    inner.rect.x >= outer.rect.x && inner.rect.y >= outer.rect.y && inner.rect.x + inner.rect.width <= outer.rect.x + outer.rect.width && inner.rect.y + inner.rect.height <= outer.rect.y + outer.rect.height;
  return kept.filter((element, index) => {
    if (!/^(StaticText|TextView)$/.test(element.type)) return kept.findIndex((other) => other.type === element.type && other.label === element.label && other.value === element.value && other.id === element.id && other.rect.x === element.rect.x && other.rect.y === element.rect.y) === index;
    return !kept.some((other) => other !== element && /Button|Cell|MenuItem|Alert/.test(other.type) && within(element, other) && (other.label.includes(element.label) || (other.value ?? "").includes(element.label)));
  });
}

/** One element as a line: `Button "Allow"`, with its value and state. */
export function describeElement(element: NativeElement): string {
  const parts = [`${element.type} "${element.label}"`];
  if (element.value) parts.push(`value "${element.value}"`);
  if (element.id) parts.push(`id ${element.id}`);
  if (!element.enabled) parts.push("disabled");
  return parts.join(" ");
}

/**
 * The element a `qa native tap <label>` means: exact labels before partial ones (case-insensitive), pressable
 * types before plain text, then the smaller one (the button rather than the sheet that holds it). `nth` picks
 * among the matches (from 0).
 */
export function matchNative(elements: NativeElement[], wanted: string, nth?: number): { pick: NativeElement | null; matches: NativeElement[] } {
  const norm = (value: string) => value.replace(/\s+/g, " ").trim().toLowerCase();
  const target = norm(wanted);
  const score = (element: NativeElement) => {
    const label = norm(element.label);
    const value = norm(element.value ?? "");
    if (label === target || value === target) return 2;
    if (label.includes(target) || value.includes(target)) return 1;
    return 0;
  };
  const matches = elements
    .map((element) => ({ element, score: score(element) }))
    .filter((entry) => entry.score > 0)
    .sort((a, b) => b.score - a.score || Number(PRESSABLE.test(b.element.type) && !/^(StaticText|TextView|Other|View)$/.test(b.element.type)) - Number(PRESSABLE.test(a.element.type) && !/^(StaticText|TextView|Other|View)$/.test(a.element.type)) || a.element.rect.width * a.element.rect.height - b.element.rect.width * b.element.rect.height)
    .map((entry) => entry.element);
  const pick = nth !== undefined ? (matches[nth] ?? null) : (matches[0] ?? null);
  return { pick, matches };
}

/** The middle of an element, in the native screen's coordinates. */
export function middle(rect: Rect): { x: number; y: number } {
  return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2) };
}

/**
 * The question iOS or Safari asks over the page, as a prompt: an alert's texts and buttons (the microphone,
 * notifications, location), else the paste callout Safari shows for a page reading the clipboard. Null when
 * neither is open.
 */
export function iosPrompt(screen: NativeScreen): { texts: string[]; buttons: string[]; kind: "alert" | "callout" } | null {
  const inAlert = screen.elements.filter((element) => element.inAlert);
  const buttons = inAlert.filter((element) => element.type === "Button").map((element) => element.label);
  if (buttons.length) return { kind: "alert", texts: [...new Set(inAlert.filter((element) => element.type === "StaticText").map((element) => element.label))], buttons: [...new Set(buttons)] };
  const paste = screen.elements.filter((element) => /^(MenuItem|Button)$/.test(element.type) && /^(allow )?paste$/i.test(element.label));
  // The edit menu of a field (Cut, Copy, Paste, Look Up) is no question of the page's: only Paste alone is.
  const editMenu = screen.elements.some((element) => /^(MenuItem|Button)$/.test(element.type) && /^(cut|copy|look up|select all|select)$/i.test(element.label));
  if (paste.length && !editMenu) return { kind: "callout", texts: ["Safari's paste callout (the page asked to read the clipboard)"], buttons: [...new Set(paste.map((element) => element.label))] };
  return null;
}
