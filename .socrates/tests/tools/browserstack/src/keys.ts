/** W3C key codes of the keys `qa key` presses on their own. */
export const KEYS: Record<string, string> = { enter: "", tab: "", escape: "", backspace: "", up: "", down: "", left: "", right: "", space: " " };

/** W3C key codes of the modifiers a combination holds. */
const MODIFIERS: Record<string, string> = { ctrl: "", shift: "", meta: "" };

/** The combinations `qa key` presses besides ctrl+<letter>. */
const COMBOS = new Set(["ctrl+enter", "meta+enter", "shift+enter", "shift+tab"]);

export interface KeyPress {
  /** As the user named it, lower case (`ctrl+c`). */
  name: string;
  /** The modifiers held, in order (W3C codes). */
  modifiers: string[];
  /** The key pressed while they are held (a W3C code or a letter). */
  key: string;
}

export const KEY_NAMES = `${Object.keys(KEYS).join(", ")}, ctrl+<letter>, ${[...COMBOS].join(", ")}`;

/** Parses `enter`, `ctrl+c`, `shift+tab`, ...; throws on anything else. */
export function parseKey(spec: string): KeyPress {
  const name = spec.trim().toLowerCase().replace(/\s+/g, "");
  if (KEYS[name]) return { name, modifiers: [], key: KEYS[name] };
  const [modifier, key, ...rest] = name.split("+");
  if (modifier && key && !rest.length && MODIFIERS[modifier]) {
    if (modifier === "ctrl" && /^[a-z]$/.test(key)) return { name, modifiers: [MODIFIERS.ctrl!], key };
    if (COMBOS.has(name)) return { name, modifiers: [MODIFIERS[modifier]!], key: KEYS[key]! };
  }
  throw new Error(`unknown key ${spec} (${KEY_NAMES})`);
}

/** The W3C key action sequence of a press: the modifiers down, the key down and up, the modifiers up. */
export function keyActions(press: KeyPress): Record<string, unknown>[] {
  return [
    ...press.modifiers.map((value) => ({ type: "keyDown", value })),
    { type: "keyDown", value: press.key },
    { type: "keyUp", value: press.key },
    ...[...press.modifiers].reverse().map((value) => ({ type: "keyUp", value })),
  ];
}
