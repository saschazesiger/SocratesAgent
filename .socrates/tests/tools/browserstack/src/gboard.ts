/**
 * Typing on Android's own on-screen keyboard (Gboard) key by key, as a finger does. Setting a field's text
 * through accessibility (UiAutomator's setText) gives the page one bare `input` event without data, which a
 * terminal (xterm.js reads `insertText` input and key events) ignores; Gboard's keys give what a person's
 * typing gives. Its keys are in the native source only with Appium's `enableMultiWindows`, each with its
 * label as content-desc ("q", "Shift", "Space", "Enter", "Dash", "Symbol keyboard", ...).
 */
import { decodeXml } from "./native";
import type { Rect } from "./webdriver";

export interface Key {
  label: string;
  rect: Rect;
}

const IME = /inputmethod/;

/** The keys of the on-screen keyboard in a UiAutomator2 source (multi-window), by label. */
export function keyboardKeys(xml: string): Map<string, Key> {
  const keys = new Map<string, Key>();
  const tag = /<[A-Za-z_][\w.$-]*((?:\s+[\w:-]+="[^"]*")*)\s*\/?>/g;
  for (let match = tag.exec(xml); match; match = tag.exec(xml)) {
    const attributes: Record<string, string> = {};
    for (const [, key, value] of match[1]!.matchAll(/([\w:-]+)="([^"]*)"/g)) attributes[key!] = decodeXml(value!);
    if (!IME.test(attributes.package ?? "") || !/:id\/key_pos_/.test(attributes["resource-id"] ?? "")) continue;
    const label = (attributes["content-desc"] || attributes.text || "").trim();
    const m = /\[(-?\d+),(-?\d+)\]\[(-?\d+),(-?\d+)\]/.exec(attributes.bounds ?? "");
    if (!label || !m) continue;
    const [x1, y1, x2, y2] = m.slice(1).map(Number) as [number, number, number, number];
    if (x2 > x1 && y2 > y1 && !keys.has(label)) keys.set(label, { label, rect: { x: x1, y: y1, width: x2 - x1, height: y2 - y1 } });
  }
  return keys;
}

/** The labels Gboard gives the key that types `char` (on whichever layout has it). */
const NAMED: Record<string, string[]> = {
  " ": ["Space"],
  "\n": ["Enter", "Done", "Go", "Search", "Send", "Next"],
  "-": ["Dash", "Minus", "Hyphen"],
  "+": ["Plus"],
  "(": ["Left parenthesis"],
  ")": ["Right parenthesis"],
  "/": ["slash", "Slash"],
  "\\": ["Backslash", "backslash"],
  _: ["underline", "Underscore"],
  "'": ["Apostrophe"],
  '"': ["Quotation mark", "Double quote", "Quote"],
  ":": ["Colon"],
  ";": ["Semicolon"],
  "!": ["Exclamation", "Exclamation mark"],
  "?": ["Question mark"],
  "=": ["Equals", "Equal sign"],
  "~": ["Tilde"],
  "`": ["Grave accent", "Backtick"],
  "|": ["Vertical bar", "Pipe"],
  "<": ["Less than", "Less-than sign"],
  ">": ["Greater than", "Greater-than sign"],
  "[": ["Left bracket", "Left square bracket"],
  "]": ["Right bracket", "Right square bracket"],
  "{": ["Left brace", "Left curly bracket"],
  "}": ["Right brace", "Right curly bracket"],
  "%": ["Percent", "%"],
  "^": ["Caret", "^"],
  "&": ["&", "Ampersand"],
};

/** The labels of the key that types `char`: a letter or digit is its own label, symbols are named. */
export function labelsFor(char: string): string[] {
  if (NAMED[char]) return [...NAMED[char]!, char];
  return [char];
}

/** The keys that switch between Gboard's layouts, in the order they are tried. */
export const TO_SYMBOLS = ["Symbol keyboard", "Symbols"];
export const TO_MORE_SYMBOLS = ["More symbols"];
export const TO_LETTERS = ["Letter keyboard", "Letters", "ABC"];

export function find(keys: Map<string, Key>, labels: string[]): Key | null {
  for (const label of labels) {
    const key = keys.get(label);
    if (key) return key;
  }
  return null;
}

/** A letter's key on the letters layout, whatever case it shows now (it shows upper case while Shift is on). */
export function letterKey(keys: Map<string, Key>, char: string): { key: Key; shifted: boolean } | null {
  const lower = keys.get(char.toLowerCase());
  if (lower) return { key: lower, shifted: false };
  const upper = keys.get(char.toUpperCase());
  return upper ? { key: upper, shifted: true } : null;
}

export const isLetter = (char: string): boolean => /^\p{L}$/u.test(char) && char.toLowerCase() !== char.toUpperCase();
