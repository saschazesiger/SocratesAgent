/**
 * Prompts the browser draws itself on Android, outside the page: Chrome's permission questions ("... wants to see
 * text and images copied to the clipboard", Block / Allow), its other dialogs and Android's own permission
 * dialogs. The page's screenshot does not show them; the native UI hierarchy (Appium's page source) does.
 */
export interface NativePrompt {
  /** The prompt's text lines. */
  texts: string[];
  /** Its buttons' labels. */
  buttons: string[];
}

/** The parts of the native screen that are never a prompt: the page itself, the keyboard and the system bars. */
const OUTSIDE = /(^|\.)WebView$/;
const IGNORED_PACKAGE = /inputmethod|^com\.android\.systemui$/;
const IGNORED_ID = /:id\/(url_bar|search_box_text|toolbar|location_bar|menu_button|home_button|tab_switcher_button|optional_toolbar_button)/;

const ENTITIES: Record<string, string> = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'" };
const decode = (value: string) =>
  value.replace(/&(#x[0-9a-f]+|#\d+|\w+);/gi, (whole, code: string) => {
    if (code[0] === "#") return String.fromCodePoint(code[1] === "x" || code[1] === "X" ? parseInt(code.slice(2), 16) : parseInt(code.slice(1), 10));
    return ENTITIES[code] ?? whole;
  });

/**
 * Finds a prompt in a UiAutomator2 page source: buttons with a label outside the page's WebView, the keyboard
 * and the browser's toolbar, with the texts beside them. Null when there is none.
 */
export function findNativePrompt(xml: string): NativePrompt | null {
  const texts: string[] = [];
  const buttons: string[] = [];
  // How deep inside a WebView, the keyboard or a system bar each open element is (0: outside all of them).
  const stack: boolean[] = [];
  const tag = /<(\/?)([A-Za-z_][\w.$-]*)((?:\s+[\w:-]+="[^"]*")*)\s*(\/?)>/g;
  for (let match = tag.exec(xml); match; match = tag.exec(xml)) {
    const [, closing, name, rawAttributes, selfClosing] = match;
    if (closing) {
      stack.pop();
      continue;
    }
    const attributes: Record<string, string> = {};
    for (const [, key, value] of rawAttributes!.matchAll(/([\w:-]+)="([^"]*)"/g)) attributes[key!] = decode(value!);
    const className = attributes.class || name!;
    const hidden = stack.some(Boolean) || OUTSIDE.test(className) || IGNORED_PACKAGE.test(attributes.package ?? "") || IGNORED_ID.test(attributes["resource-id"] ?? "");
    if (!hidden && attributes.displayed !== "false") {
      const label = (attributes.text || "").trim();
      if (label && /Button$/.test(className)) buttons.push(label);
      else if (label && /TextView$/.test(className)) texts.push(label);
    }
    // A WebView's, the keyboard's or a system bar's whole subtree stays out; an ignored toolbar id only itself.
    const subtreeOut = stack.some(Boolean) || OUTSIDE.test(className) || IGNORED_PACKAGE.test(attributes.package ?? "");
    if (!selfClosing) stack.push(subtreeOut);
  }
  if (!buttons.length) return null;
  return { texts: [...new Set(texts)], buttons: [...new Set(buttons)] };
}

/** One line for the action's output. */
export function describePrompt(prompt: NativePrompt): string {
  const said = prompt.texts.length ? prompt.texts.map((text) => `"${text}"`).join(" ") : "(no text)";
  return `${said} ${prompt.buttons.map((button) => `[${button}]`).join(" ")}`;
}

/** The labels that answer a prompt each way, best first (Chrome's own, then Android's permission dialogs). */
const ANSWERS: Record<"allow" | "block", RegExp[]> = {
  allow: [/^allow$/i, /^paste$/i, /^allow paste$/i, /^allow this time$/i, /^only this time$/i, /^while using the app$/i, /^allow\b/i, /^(ok|continue)$/i],
  block: [/^block$/i, /^don[’']t allow$/i, /^deny$/i, /^never allow$/i, /^(block|don[’']t allow)\b/i, /^(cancel|not now|no thanks)$/i],
};

/** The prompt's button that gives `answer`, or null when it has none. */
export function answerButton(prompt: NativePrompt, answer: "allow" | "block"): string | null {
  for (const pattern of ANSWERS[answer]) {
    const label = prompt.buttons.find((button) => pattern.test(button.trim()));
    if (label) return label;
  }
  return null;
}

/** A string as an XPath 1.0 literal (it has no escapes: a value with both quote kinds is concatenated). */
export function xpathLiteral(value: string): string {
  if (!value.includes('"')) return `"${value}"`;
  if (!value.includes("'")) return `'${value}'`;
  return `concat(${value
    .split('"')
    .map((part) => `"${part}"`)
    .join(`, '"', `)})`;
}
