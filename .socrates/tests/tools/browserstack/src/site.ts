/**
 * The site under test is one Socrates test instance and nothing else: every page `qa open` opens is
 * resolved against its base URL, and a full URL of another origin is refused (the agent must never
 * drive another site, the owner's live Socrates least of all).
 */

/** Checks SOCRATES_TEST_PARAM_BASE_URL and returns it without a trailing slash, query or hash. */
export function parseBaseUrl(value: string): string {
  let url: URL;
  try {
    url = new URL(value.trim());
  } catch {
    throw new Error(`base_url ${value} is not a URL (expected https://host[:port])`);
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") throw new Error(`base_url ${value} is not an http(s) URL`);
  if (url.username || url.password) throw new Error("base_url must not carry credentials");
  return `${url.origin}${url.pathname}`.replace(/\/+$/, "");
}

/** The origin of a URL, or "" when it is none (an unparsable value, about:blank). */
export function originOf(url: string): string {
  try {
    const origin = new URL(url).origin;
    return origin === "null" ? "" : origin;
  } catch {
    return "";
  }
}

/** True when `url` is on the test instance (the same origin as `base`). */
export function onSite(base: string, url: string): boolean {
  return originOf(url) === originOf(base);
}

/**
 * The URL `qa open` navigates to: a path (`/admin`, `admin`, `/#home`, `?x=1`) under the base URL, or a full
 * URL of the base URL's own origin. Any other origin or scheme is refused.
 */
export function pageUrl(base: string, path: string): string {
  const value = path.trim() || "/";
  if (/^[a-z][a-z0-9+.-]*:/i.test(value) || value.startsWith("//")) {
    if (!onSite(base, value)) throw new Error(`${value} is not on the test instance ${originOf(base)}: qa opens only its own pages`);
    return value;
  }
  const url = `${base}${value.startsWith("/") ? "" : "/"}${value}`;
  if (!onSite(base, url)) throw new Error(`${value} leaves the test instance ${originOf(base)}`);
  return url;
}

/** Whether opening `to` from `from` stays in the document: the same page with a hash (only the hash may differ). */
export function sameDocument(from: string, to: string): boolean {
  try {
    const a = new URL(from);
    const b = new URL(to);
    return a.origin === b.origin && a.pathname === b.pathname && a.search === b.search && b.hash !== "";
  } catch {
    return false;
  }
}
