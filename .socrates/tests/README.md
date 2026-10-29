# Socrates' device tests

For people; Socrates ignores this file. The tests check Socrates' own web interface on real phones,
tablets and desktops through BrowserStack, run by Socrates' test mode (`docs/design/TEST-MODE.md`):
every test on every device is one agent session that drives the device step by step and judges each
step on its screenshots.

## What is here

| Path | What |
|---|---|
| `TESTS.md` | Rules for every cell: the test instance only, the shared instance, the agents in it, secrets |
| `web/AREA.md` | The devices, the target URL, how a cell is worked, the layout checks every step makes, verdicts |
| `web/<group>/<test>.md` | 30 tests: `sign-in`, `home`, `sessions`, `chat`, `shell`, `admin`, `pages` |
| `tools/browserstack/` | `qa`, the BrowserStack driver (ported from browser.lol's Device QA), its `TOOL.md` |
| `../../scripts/qa-instance.sh` | Installs, updates, resets and removes the test instance |
| `../../scripts/qa-demo-tests/` | The Demo shop: the test catalog whose runs the instance's Tests page shows |

## The devices

| Id | Device | Why |
|---|---|---|
| `iphone` | iPhone 15 · Safari | Phone, iOS |
| `ipad` | iPad 10th · Safari | Tablet, iOS |
| `pixel` | Pixel 9 · Chrome | Phone, Android |
| `tesla` | Galaxy Tab S9 in landscape · Chrome | Stands in for the Tesla Model 3 screen: large, touch, landscape, Chromium |
| `win-chrome` | Windows 11 · Chrome | Desktop |
| `mac-safari` | macOS · Safari | Desktop |

Extras a test may name: `iphone-old` (iOS 16), `moto`, `samsung` (Samsung Internet), `win-firefox`,
`win-edge`. The full run is 178 cells (every test on every device; rotation only on the four that
rotate).

## The test instance

A second Socrates on the build server, apart from the real one: system user `socrates-qa` (no sudo),
data in `/var/lib/socrates-qa`, unit `socrates-qa.service` on `127.0.0.1:8095`, published through its
own Cloudflare tunnel at **https://socrates-qa.zesiger.net** (no Cloudflare Access, so the devices can
reach it; its password protects it). Its agents: Claude Code (Haiku), Codex (gpt-6-luna), OpenCode
(DeepSeek V4 Flash on OpenRouter) and the free Scripted agent. Secrets are root-only in
`/etc/socrates-qa/` (`secrets.env` holds the BrowserStack accounts and the instance's password; the
tool's secrets command reads it).

Its Tests page has the **Demo shop** (`/var/lib/socrates-qa/work/demo-shop`, a copy of
`scripts/qa-demo-tests` with a commit or three): two areas, ten tests, 21 cells, and four runs with
reviews, bugs and conclusions, worked by the Scripted agent for free (`docs/design/TEST-MODE.md`,
"Test roles on the Scripted agent"). Each test's `fake` parameter decides its outcome, so the history
is the same after every reset: an issue that stays, a cell fixed in run 3, a flaky cell, a blocked
one, a regression and a fix in run 4, a verdict the reviewer corrected. The cells share it: they read
it and may start a quick run of `shop/home` of their own, never change what is there (`TESTS.md`).

```sh
scripts/qa-instance.sh status      # service, public URL, accounts, the Demo shop's runs and bugs
scripts/qa-instance.sh password    # the password, to sign in by hand
scripts/qa-instance.sh reset       # delete every session, reset the demo project, seed again,
                                   # and the Demo shop's four test runs (restarts the instance; < 1 min)
scripts/qa-instance.sh update      # rebuild from origin/main (--ref <ref> for another) and restart
scripts/qa-instance.sh uninstall   # remove it (the tunnel and DNS record stay in Cloudflare)
```

## Running the tests once

1. **Sign the instance's agents in** (once): open https://socrates-qa.zesiger.net/admin (password:
   `scripts/qa-instance.sh password`), Accounts, and press *Sign in* on **Claude QA** and **Codex QA**.
   Without it, the Claude Code and Codex steps end `blocked`.
2. **Bring it up to date and reset it**: `scripts/qa-instance.sh update && scripts/qa-instance.sh reset`.
3. In the real Socrates, open **Tests**, *Add repository* `/root/SocratesAgent` (once), check that the
   Catalog tab shows 28 tests and no errors, and that *Check secrets* finds both names.
4. *Run tests*: everything, the default settings (cell agent Sonnet 5.5, reviewer and analyst Opus
   5.5; `parallel` 0, so the four BrowserStack accounts are the limit). Expect roughly 8 to 12 hours.
5. Read the result on the Tests page: Results (per device), Bugs and the conclusions.

`socrates tests check /root/SocratesAgent` validates the catalog; the tool's own checks are
`npm ci && npm run type-check && npm run lint && npm test` in `tools/browserstack`.

## Not covered

Uploading a file (the devices' file pickers), the usage-limit card (the Scripted agent's `/limit` would
limit its shared account for every cell), real push delivery, real microphone audio, VS Code
(code-server opens in a new tab), backups and restore, signing accounts in, and changing the password:
they need things a device in a cloud lab does not have, or would disturb the other cells.
