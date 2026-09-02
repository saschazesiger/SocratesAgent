# Implementer rules (every work package)

Repo: /root/SocratesAgent, branch `terminal-rewrite`. Spec: docs/design/DESIGN.md in the repo
(copied from the scratchpad, revision 3) — read the whole section for your WP plus every section
it references. DECISIONS.md is binding product intent.

1. Scope: implement exactly your WP's scope and acceptance criteria. Do not start later WPs.
   If the spec is impossible or wrong at a point, do the smallest correct deviation, and record it
   in docs/design/DEVIATIONS.md (append: WP, spec section, what, why).
2. Quality: idiomatic Go 1.25 / vanilla ES modules; no new dependencies beyond those the spec
   names (creack/pty, coder/websocket, vendored xterm.js); `gofmt`, `go vet ./...`,
   `go build ./...`, `go test -race ./...` green at the end. Everything shipped is in English.
   No commented-out code, no TODOs left behind, no debug prints.
3. Tests: write the tests the WP lists; use real tmux on isolated `-S` sockets under t.TempDir(),
   `t.Skip` when tmux < 3.3 is absent. Never touch the user's tmux server or start real paid CLI
   sessions; fake CLIs only.
4. Verification: before you finish, run the full `go test -race ./...` and, for frontend WPs, the
   e2e scenarios the WP names (`node e2e/run.mjs <name>`; Playwright per e2e/harness.mjs). Report
   the actual output. If something fails and you cannot fix it, say so explicitly.
5. Commit: one or a few focused commits on `terminal-rewrite`, message body says what and why,
   ending with
   Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
   Claude-Session: https://claude.ai/code/session_01PPnbcVdXE2T5MGTDXsDBXm
   Never push. Never rewrite history of earlier commits.
6. Report: return (a) what was built, files touched, (b) test/e2e output summary, (c) deviations,
   (d) anything the next WP or the reviewer must know. Under 400 words.
