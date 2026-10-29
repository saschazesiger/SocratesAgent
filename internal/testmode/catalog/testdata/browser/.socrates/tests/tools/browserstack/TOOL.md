---
title: BrowserStack devices
bin: bin
setup: npm ci --no-audit --no-fund
secrets:
  command: infra/secretctl show beta device-qa
  names: [BROWSERSTACK_ACCOUNTS, QA_GOOGLE_EMAIL?, QA_GOOGLE_PASSWORD?, QA_GOOGLE_TOTP_SECRET?, QA_MS_EMAIL?, QA_MS_PASSWORD?, QA_MS_TOTP_SECRET?]
pool:
  secret: BROWSERSTACK_ACCOUNTS
  separator: ","
  per_member: 1
  status: bin/qa pool-status
end: bin/qa stop
---
`qa start` opens the cell's device in its locale; run it again when it says "still queued". `qa open <path>`,
`qa tap <text>`, `qa type <text>` (`--secret NAME` for a secret), `qa look` and `qa shot` work the page; every
action leaves a screenshot in the evidence. `qa stop` closes the device.
