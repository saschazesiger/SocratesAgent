/**
 * The evidence Socrates reads (TOOL.md, the tool contract): one JSON line per action in actions.jsonl
 * with its screenshot beside it, the BrowserStack session's links in links.json, logs in logs/*.txt.
 */
import { appendFileSync, existsSync, mkdirSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { TOOL_NAME } from "./config";

/** How an action named its control, and what it hit. */
export interface Target {
  text?: string;
  role?: string;
  css?: string;
  nth?: number;
  exact?: boolean;
  any?: boolean;
  native?: boolean;
  /** The accessible name of the control that was hit. */
  matched?: string;
}

export interface PageEntry {
  kind: string;
  message: string;
}

export interface Settle {
  ms: number;
  settled: boolean;
  busy: string[];
}

/** One driver command on the device (open, tap, type, look, ...) with what followed it. */
export interface Action {
  n: number;
  at: string;
  action: string;
  /** One line on what was done, never a secret's value. */
  summary: string;
  ms: number;
  target?: Target;
  url?: string;
  screenshot?: string;
  settle?: Settle;
  pageErrors?: PageEntry[];
  output?: string;
  error?: string;
}

export interface EvidenceLine {
  at: string;
  tool: string;
  action: string;
  summary: string;
  url: string | null;
  screenshot: string | null;
  ms: number;
  error: string | null;
  interaction: boolean;
  details: { target?: Target; settle?: Settle; pageErrors?: PageEntry[]; output?: string };
}

/** The actions that act on the page as a person would; Socrates' review rule reads the flag. */
export const INTERACTIONS: ReadonlySet<string> = new Set(["tap", "type", "key"]);

/** Query values that carry credentials (sign-in and invitation links, OAuth, BrowserStack's public link) are cut from every recorded URL. */
const SECRET_PARAMS = /([?&#](?:token|auth_token|code|key|auth|state|sig|signature|secret|password)=)[^&#]*/gi;

export function redactUrl(url: string): string {
  return url.replace(SECRET_PARAMS, "$1…");
}

export function evidenceLine(action: Action): EvidenceLine {
  const details: EvidenceLine["details"] = {};
  if (action.target) details.target = action.target;
  if (action.settle) details.settle = action.settle;
  if (action.pageErrors?.length) details.pageErrors = action.pageErrors;
  if (action.output) details.output = action.output;
  return {
    at: action.at,
    tool: TOOL_NAME,
    action: action.action,
    summary: action.summary,
    url: action.url ? redactUrl(action.url) : null,
    screenshot: action.screenshot ?? null,
    ms: action.ms,
    error: action.error ?? null,
    interaction: INTERACTIONS.has(action.action),
    details,
  };
}

export class Evidence {
  private count: number;

  constructor(readonly dir: string) {
    mkdirSync(dir, { recursive: true });
    // A daemon started again for the same attempt numbers on, so no screenshot is overwritten.
    this.count = existsSync(this.actionsFile) ? readFileSync(this.actionsFile, "utf8").split("\n").filter(Boolean).length : 0;
  }

  get actionsFile(): string {
    return join(this.dir, "actions.jsonl");
  }

  /** The number of the next action. */
  next(): number {
    this.count += 1;
    return this.count;
  }

  screenshotName(action: Action): string {
    return `${String(action.n).padStart(2, "0")}-${action.action}.png`;
  }

  writeScreenshot(action: Action, png: Buffer): void {
    const name = this.screenshotName(action);
    writeFileSync(join(this.dir, name), png);
    action.screenshot = name;
  }

  append(action: Action): void {
    appendFileSync(this.actionsFile, `${JSON.stringify(evidenceLine(action))}\n`);
  }

  /** links.json: label to URL, empty values left out. */
  writeLinks(links: Record<string, string | undefined>): void {
    const kept = Object.fromEntries(Object.entries(links).filter((entry): entry is [string, string] => Boolean(entry[1])));
    const file = join(this.dir, "links.json");
    writeFileSync(`${file}.tmp`, `${JSON.stringify(kept, null, 1)}\n`);
    renameSync(`${file}.tmp`, file);
  }

  /** logs/<name>.txt, shown on the cell page. */
  writeLog(name: string, lines: string[]): string {
    mkdirSync(join(this.dir, "logs"), { recursive: true });
    const file = join(this.dir, "logs", `${name}.txt`);
    writeFileSync(file, lines.length ? `${lines.join("\n")}\n` : "");
    return file;
  }
}
