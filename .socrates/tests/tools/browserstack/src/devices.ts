import { DESKTOP_RESOLUTION } from "./config";

export type DeviceKind = "ios" | "android" | "desktop";

export interface Device {
  id: string;
  label: string;
  kind: DeviceKind;
  /** The W3C browserName and BrowserStack's bstack:options without the session fields. */
  browserName: string;
  options: Record<string, string>;
  /** Outside the matrix: run only by tests that name it. */
  extra?: boolean;
}

/** The matrix columns in report order, then the extra devices (the same ids and labels as web/AREA.md). */
export const DEVICES: Device[] = [
  { id: "iphone", label: "iPhone 15 · Safari", kind: "ios", browserName: "safari", options: { deviceName: "iPhone 15", osVersion: "27", realMobile: "true" } },
  { id: "ipad", label: "iPad 10th · Safari", kind: "ios", browserName: "safari", options: { deviceName: "iPad 10th", osVersion: "27", realMobile: "true" } },
  { id: "pixel", label: "Pixel 9 · Chrome", kind: "android", browserName: "chrome", options: { deviceName: "Google Pixel 9", osVersion: "16.0", realMobile: "true" } },
  // A stand-in for a Tesla's car screen: a wide Android tablet held in landscape, Chrome.
  { id: "tesla", label: "Galaxy Tab S9 landscape · Chrome (car screen)", kind: "android", browserName: "chrome", options: { deviceName: "Samsung Galaxy Tab S9", osVersion: "13.0", realMobile: "true", deviceOrientation: "landscape" } },
  { id: "win-chrome", label: "Windows · Chrome", kind: "desktop", browserName: "chrome", options: { os: "Windows", osVersion: "11", resolution: DESKTOP_RESOLUTION } },
  { id: "mac-safari", label: "macOS · Safari", kind: "desktop", browserName: "safari", options: { os: "OS X", osVersion: "Tahoe", resolution: DESKTOP_RESOLUTION } },
  { id: "iphone-old", label: "iPhone 13 · iOS 16", kind: "ios", browserName: "safari", options: { deviceName: "iPhone 13", osVersion: "16", realMobile: "true" }, extra: true },
  { id: "moto", label: "Moto G71 · Chrome", kind: "android", browserName: "chrome", options: { deviceName: "Motorola Moto G71 5G", osVersion: "11.0", realMobile: "true" }, extra: true },
  { id: "samsung", label: "Galaxy S25 · Samsung Internet", kind: "android", browserName: "samsung", options: { deviceName: "Samsung Galaxy S25", osVersion: "15.0", realMobile: "true" }, extra: true },
  { id: "win-firefox", label: "Windows · Firefox", kind: "desktop", browserName: "firefox", options: { os: "Windows", osVersion: "11", resolution: DESKTOP_RESOLUTION }, extra: true },
  { id: "win-edge", label: "Windows · Edge", kind: "desktop", browserName: "edge", options: { os: "Windows", osVersion: "11", resolution: DESKTOP_RESOLUTION }, extra: true },
];

export const MATRIX_DEVICES = DEVICES.filter((device) => !device.extra);

export function deviceById(id: string): Device {
  const device = DEVICES.find((entry) => entry.id === id);
  if (!device) throw new Error(`unknown device ${id} (known: ${DEVICES.map((entry) => entry.id).join(", ")})`);
  return device;
}

/** The permissions `qa start --grant` can give a desktop browser in advance (its prompts are outside WebDriver's reach). */
export const GRANTS = ["microphone", "camera", "notifications", "clipboard", "location"] as const;
export type Grant = (typeof GRANTS)[number];

/**
 * Parses `--grant microphone,notifications`: only desktop Chrome, Edge and Firefox take permissions in advance
 * (their profile's preferences). Safari has no such setting, and a phone or tablet answers its prompts natively
 * (qa prompt).
 */
export function parseGrants(spec: string | undefined, device: Device): Grant[] {
  if (spec === undefined) return [];
  const grants = spec
    .split(",")
    .map((word) => word.trim().toLowerCase())
    .filter(Boolean)
    .map((word) => (word === "mic" ? "microphone" : word));
  for (const grant of grants) if (!(GRANTS as readonly string[]).includes(grant)) throw new Error(`--grant takes ${GRANTS.join(", ")}, not ${grant}`);
  if (device.kind !== "desktop") throw new Error(`--grant is for desktop browsers, whose prompts the tool cannot reach; on ${device.label} answer the browser's own prompt with qa prompt allow or block`);
  if (device.browserName === "safari") throw new Error("--grant: Safari takes no permission in advance through WebDriver; judge a step that needs its prompt blocked");
  return [...new Set(grants)] as Grant[];
}

/**
 * The capabilities that give a desktop browser the permissions in advance: Chrome's and Edge's content settings
 * (1: allow) with a fake microphone and camera (the cloud machines have none), Firefox's preferences.
 */
export function grantCapabilities(device: Device, grants: Grant[]): Record<string, unknown> {
  if (!grants.length) return {};
  const media = grants.includes("microphone") || grants.includes("camera");
  if (device.browserName === "firefox") {
    const prefs: Record<string, unknown> = {};
    if (media) Object.assign(prefs, { "media.navigator.permission.disabled": true, "media.navigator.streams.fake": true });
    if (grants.includes("notifications")) prefs["permissions.default.desktop-notification"] = 1;
    if (grants.includes("clipboard")) prefs["dom.events.asyncClipboard.readText"] = true;
    if (grants.includes("location")) prefs["permissions.default.geo"] = 1;
    return { "moz:firefoxOptions": { prefs } };
  }
  const setting: Record<Grant, string> = { microphone: "media_stream_mic", camera: "media_stream_camera", notifications: "notifications", clipboard: "clipboard", location: "geolocation" };
  const prefs: Record<string, number> = {};
  for (const grant of grants) prefs[`profile.default_content_setting_values.${setting[grant]}`] = 1;
  const options = { prefs, args: media ? ["--use-fake-device-for-media-stream", "--use-fake-ui-for-media-stream"] : [] };
  return { [device.browserName === "edge" ? "ms:edgeOptions" : "goog:chromeOptions"]: options };
}
