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
