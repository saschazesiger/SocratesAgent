import { readFileSync } from "node:fs";

export type Secrets = Record<string, string>;

const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

/**
 * Parses `NAME=value` lines as Socrates writes the vault and the secrets file holds them: split at the first
 * `=`, the value taken raw (quotes and spaces kept), an `export ` prefix dropped, other lines ignored.
 */
export function parseSecrets(text: string): Secrets {
  const secrets: Secrets = {};
  for (const raw of text.split("\n")) {
    const line = raw.endsWith("\r") ? raw.slice(0, -1) : raw;
    const at = line.indexOf("=");
    if (at <= 0) continue;
    const name = line.slice(0, at).replace(/^export\s+/, "");
    if (NAME.test(name)) secrets[name] = line.slice(at + 1);
  }
  return secrets;
}

/** The vault file Socrates wrote for this cell or run. Never printed, logged or recorded. */
export function readVault(file: string): Secrets {
  return parseSecrets(readFileSync(file, "utf8"));
}

export function requireSecret(secrets: Secrets, name: string): string {
  const value = secrets[name];
  if (!value) throw new Error(`secret ${name} is not in the vault (TOOL.md names the secrets; its secrets command reads them from $SOCRATES_QA_SECRETS or /etc/socrates-qa/secrets.env)`);
  return value;
}
