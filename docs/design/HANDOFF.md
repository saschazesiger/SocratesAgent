# Rebuild handoff (2026-10-23)

> This copy lives on `main` only as a pointer. The rebuild itself, the plan (`REBUILD.md`),
> `AGENT-HOST.md` and the research are on the branch **`rebuild`** — check it out first.

Read this first if you are taking over the Socrates rebuild. It records where the work stands,
where every piece lives, and what is left. The previous orchestrator's session ended here; nothing
below is in anyone's head any more.

## 1. What the rebuild is

Socrates is moving from "agent TUIs in tmux, screen-scraped" to "agents driven through their
structured protocols, rendered by Socrates' own web components". Binding documents, in order:

1. `docs/design/REBUILD.md`: the plan and the product owner's decisions (§1). Binding.
2. `docs/design/AGENT-HOST.md`: the agent host, its socket protocol, the chat event model, the
   adapter contract (§5; Claude §5.1, Codex §5.2, OpenCode §5.3 on the WP5 branch). Authoritative
   for every package that touches chats.
3. `docs/design/DEVIATIONS.md`, section "Rebuild": every accepted deviation from REBUILD.md.
4. `docs/design/research/01..09-*.md`: verified facts behind the plan (protocols of claude 2.1.309,
   codex 0.164.0, opencode 1.19.4; spikes S1/S3/S4; Claude subagent/workflow event shapes).
5. Older docs (DECISIONS.md, DESIGN.md, HOME.md, ACTIVITY.md, DASHBOARD-STYLE.md) stay valid
   where REBUILD.md does not supersede them. DASHBOARD-STYLE.md is binding for all UI.

The product owner's decisions, in short: agents are fully web-native; the terminal stays for
**Shell only** (plus VS Code); accounts are subscription logins **and** API keys per agent,
switchable in a running chat; auto-switch on limit is a per-chat option, **off** by default; one
local data folder is the truth and S3 is only an encrypted periodic backup **without** working
directories; permissions are **always allowed**; `ask_user` **always waits**; **clean cut** for
old agent terminal sessions; `~/.codex-multi` and `~/.config/opencode-multi-auth` are ignored.

## 2. Branches (all pushed to origin)

`main` is untouched by the rebuild on purpose: the server's nightly update
(`scripts/update.sh`, `SOCRATES_BRANCH` defaults to `main`) builds and deploys `origin/main`.
**Do not merge the rebuild into main until it is finished and tested end to end**; see §6.

| Branch | Content | State |
|---|---|---|
| `rebuild` | Plan + spikes, WP2 (reviewed, fixes in), WP3 (merged; review fixes are on `wp3-fixes`), WP4 (reviewed, fixes in) | Integration branch. Builds. |
| `wp1-backup` | WP1 complete: S3 store-of-record removed, `internal/paths`, `internal/backup`, restore (setup page + `socrates restore`), Admin Backup card. Has `rebuild` merged up to 935ad7f; its migration is **v11** (WP2 is v10). | Implementer done; tests after the merge with WP2–4 **not run**; **no Fable review yet**. |
| `wp3-fixes` | WP3 review fixes on top of `rebuild` at 935ad7f: per-turn cost after resume (summed `SinceLast` costs), endTurn skips live groups, failed subagents stay failed, cancelled messages get a state, recover sets content status, rebuilt events carry timestamps, finished workflow agents dropped, `Options.Binary` from the dashboard. | Committed; the final test run did not report back. |
| `wp5-opencode` | WP5 complete (OpenCode adapter, fakeopencode, real-binary tests) plus a merge of `rebuild` (up to 935ad7f) that **unifies** the two rebuild-from-transcript mechanisms (WP3's `agent.Rebuilder`/`host.json Conversation` vs WP5's `StartSpec.ResumeID/Rebuild`) and the slash-command palette source (`agent.info`, `commands` event, `internal/agent/palette.go`). | The merge was resolved by the WP5 implementer and committed at handoff (e0fcb47); it builds and vets, but its **tests did not run**; **no Fable review yet**. |
| `wp8-chat-ui` | WP8 in progress: vendored marked + DOMPurify, chat routes for the composer (model, commands, attachments with server-assigned paths, drafts, PreStart), fake-agent shapes, chat fold/stream/Markdown/parts/outbox modules (92a21a4), and an **unfinished** chat view, composer and ask dialog (WIP commit 1d8383a). | Unfinished, untested. |

All five branches are 5 commits behind `rebuild`'s tip (the WP4 review fixes).

### Suggested integration order

1. Merge `wp3-fixes` into `rebuild`; run `go test -race ./internal/agent/... ./internal/agenthost/...`
   and the gated live tests (`SOCRATES_CLAUDE_LIVE=1`, see §5 for the account).
2. Merge `wp5-opencode` into `rebuild`. Expect conflicts with `wp3-fixes` and the WP4 fixes in the
   Claude/Codex adapters, because the unification touched both. Keep ONE rebuild mechanism and ONE
   palette source. Run the full race suite once, plus `SOCRATES_REAL_CODEX=1` and
   `SOCRATES_OPENCODE_REAL=1`. Then a **Fable review of WP5** (adapter + the unification).
3. Merge `wp1-backup` into `rebuild`, keeping its v11 migration after WP2's v10. Check that
   `paths.NotBackedUp` covers `run/` and that `chats/<id>/host.json` holds no credentials once WP7
   puts account env there (WP2 review note). Run the full race suite and the e2e scenarios
   `backup`, `restore`, plus `e2e/chats.mjs`. Then a **Fable review of WP1**.
4. Finish WP8 on `wp8-chat-ui` (merge `rebuild` into it first), then review and merge.
5. Then WP6, WP7, WP9, WP10, WP11 (§4), then the final test (§6).

## 3. Working method that was used (keep it)

- One orchestrator; each work package goes to one implementer in its own git worktree, then
  **one** Fable review (`model: fable`), then the implementer fixes once. No repeated reruns (the
  product owner asked for pace). A second review only for blockers.
- Implementer briefs state the scope by REBUILD/AGENT-HOST sections, the rules below, and ask for
  a report under 400 words. They commit early and often (sessions were cut off by authentication
  outages many times).
- Rules for everyone: never touch the running `socrates.service`, `/var/lib/socrates`, the user's
  tmux server, `~/.claude`, `~/.codex`, `~/.codex-multi` or the real OpenCode folders; tests in
  temp dirs; English in everything shipped; gofmt/vet/race-tests green; commit messages end with
  the Co-Authored-By line; **never push** from an implementer.
- `TestSavingPresetsWarnsAboutAMissingOne` (internal/server) writes into the real
  `/var/lib/socrates/workspaces`. Skip it (`-skip`) and fix it as part of WP10/WP11.
- e2e: `SOCRATES_E2E_CHROME=/opt/google/chrome/chrome node e2e/run.mjs <scenario>`. The old
  default browser path in `e2e/harness.mjs` no longer exists on this host. Wait for
  `domcontentloaded`, never `networkidle`.
- Known flaky under load: `offlinerestart`, `options`, `home-question`, `home-own-answer`,
  `TestTerminalSlowReaderIsGivenUpOn`, `TestGlobalHooksFireForEverySession`. All pass alone.
- The Windows build (`GOOS=windows go build ./...`) is broken in `internal/proc` and
  `internal/termux`, and was already broken before the rebuild.

## 4. What is left, package by package (REBUILD.md §10)

- **WP6 `ask_user`** (§5): one tool for all three agents. The schema is in §5.1/§5.2; the hooks
  are in place:
  - **Claude:** `Options.Servers` / `Answerer` and the in-process SDK MCP registration in
    `initialize`, plus `Options.AppendSystemPrompt` for the guidance. Cancel the per-`mcp_message`
    handler goroutines when the CLI exits (WP3 review).
  - **Codex:** `codex.RegisterTool` / `codex.HandleRequest` (process-global, so register before
    any `Start`), `Call.Ask` (blocking) and `Call.Defer` (code-mode models: answer as the next
    message, `via:"message"` / `agent.ErrAskDeferred`). Also convert `request_user_input`, async
    questions and elicitations.
  - **OpenCode:** the `MCPServers` and `QuestionHandler` hooks; the tool runs as
    `socrates mcp-ask --socket <host sock>`, a local MCP server with a 24 h timeout and progress
    notifications.
  - **Generic:** an open ask left by an interrupted turn keeps the host alive (WP2 review). Decide
    whether recovery closes it.
- **WP7 accounts** (§4):
  - **Model:** an account model replaces profiles. Sign-in flows: Claude via `claude auth login`
    in a Shell terminal with the account's `CLAUDE_CONFIG_DIR`; Codex by device code over
    app-server; OpenCode by provider OAuth/API key. API keys go in the vault.
  - **Switching:** a `switch_account` host op (reserved in WP2). It always interrupts gracefully
    first.
  - **Limits and recommender:** limits per account and the headroom recommender. Auto-switch on
    limit is a per-chat option, off by default.
  - **Helpers:** call `claude.PrepareConfigDir` (shares `projects/ tasks/ plans/ todos/`),
    `codex.PrepareHome` (shares `sessions/ archived_sessions/`) and
    `opencode Layout.PrepareAccount` (shared `OPENCODE_DB`, per-account `XDG_DATA_HOME`) before an
    account's first run.
  - **Environment:** build `StartSpec.Env` completely (PATH, HOME, the agent's dir; Codex refuses
    an empty Env). Use `SOCRATES_CLAUDE_API_KEY` for Claude API keys, `OPENCODE_AUTH_CONTENT` for
    OpenCode API keys only (never OAuth), and `OPENAI_API_KEY`/`auth.json` for Codex. Keep
    credentials out of backed-up `host.json`.
  - **Migration:** the step from REBUILD §8.1 (move `~/.claude`, `~/.claude.json`, `~/.codex` and
    the OpenCode folders into the data folder) belongs here. Plain WP1 did not do it.
- **WP8 chat UI** (§7.1, §5.4): finish on `wp8-chat-ui`. The brief is restated in §7 below.
- **WP9 adapt the rest:**
  - **Feeds:** activity from the journal (busy/waiting/idle), Home summaries, push wording and
    badge (open asks), titles, spoken Status, turn outcomes and the cost ledger all read the
    journal. Fix the per-turn cost (`wp3-fixes`) before the ledger relies on it.
  - **Session sheet:** agent → folder → model → account.
  - **Ultracode:** wire it (`SOCRATES_CLAUDE_ULTRACODE`; nothing sets it yet).
  - **Lost folders:** a chat whose whole folder is lost is not rebuilt yet; the server must pass
    `cli_session_id` (WP4 note).
- **WP10 cleanup:**
  - Delete the answer operator (`internal/server/operator.go`), question extraction in
    `digest.go`, the screen/title activity layers, Codex/OpenCode id discovery, theme forcing, the
    initial-prompt file, the agent key bar and profiles.
  - The terminal becomes Shell only.
  - Migration steps REBUILD §8.2–8.4. This is a clean cut: old agent sessions are stopped and
    their rows removed, and Shell sessions stay.
  - Fix `TestSavingPresetsWarnsAboutAMissingOne`.
- **WP11 e2e and docs:**
  - Switch the e2e harness from `faketui` to `fakeclaude` / `fakecodex` / `fakeopencode` for the
    agents.
  - Port the ~50 ADAPT scenarios and drop the operator/screen ones (inventory: research/02 §9.4).
  - Add the `design` assertions for the chat view.
  - Update README, DECISIONS and DESIGN, and retake the screenshots (`node e2e/shots.mjs`; the
    admin shot still shows the old Storage card).

## 5. Environment facts you will need

- **Real Claude accounts for tests:** `CLAUDE_CONFIG_DIR=/root/socrates-spike/claude-a` (Team plan,
  org Zesiger.net) and `/root/socrates-spike/claude-b` (Max plan, another org). Both are logged in,
  in the shared layout (`/root/socrates-spike/shared/{projects,tasks,plans}` via relative
  symlinks). Never log them out, never copy their credentials, keep spend tiny (haiku). Spike S1
  proved a conversation moves between them with full context.
- **Codex:** there is no working ChatGPT login on this host (both are expired). Tests use the real
  codex binary against the scripted model (`internal/agent/codex/fakemodel`).
- **OpenCode:** free models of the `opencode` provider only. Mind ~400 MB RAM per `serve`. The
  real `~/.config/opencode/opencode.jsonc` holds a plaintext Exa API key; do not copy it anywhere.
- **Repeated authentication outages:** the orchestrating Claude Code session kept getting logged
  out ("Not logged in", authentication_failed), which killed every subagent. The likely cause is
  that this session ran inside Socrates and shared `~/.claude/.credentials.json` with other
  Claude processes; this is not proven. Running the orchestrator with its own `CLAUDE_CONFIG_DIR`
  should avoid it.
- **Data already lost on this host:** the five profiles' folders ("Team Sascha", "Team André",
  "Team Karin", "Claude Campus", "Campus") vanished in the Docker → systemd move (d445348); only
  their DB rows survived. The migration turns them into accounts marked "sign in again".
- **Scratchpad lab scripts are not in the repo:** cc-lab, codex-lab, opencode-lab, s3-lab and
  s4-lab lived in a session scratchpad and may be gone. Their findings and key code paths are in
  `docs/design/research/`.

## 6. Definition of done for the whole rebuild

The product owner asked for everything finished and tested. Before merging into `main`:

1. `go build`, `go vet` and `go test -race ./...` pass.
2. The whole e2e suite passes in a real browser, desktop and phone viewport, including the new chat
   scenarios (stream, tools, offline send exactly once, server restart mid-turn, ask on phone and
   desktop, attachments, account switch, backup/restore, design).
3. Real runs, cheap:
   - **Claude:** a chat, a subagent, a workflow surviving `restart` of a test server instance, and
     an account switch A → B → A. Use the spike accounts.
   - **OpenCode:** a chat on a free model.
   - **Codex:** the real binary against the scripted model.
4. Migration rehearsed on a copy of `/var/lib/socrates` (never on the original): REBUILD §8, the
   clean cut, the first backup into the existing bucket under `backup/v1`.
5. Only then merge into `main`. The next nightly update deploys it, or the product owner runs it
   from Admin → Updates. Tell the product owner before that happens; the migration is one-way.

## 7. WP8 brief (restated, so it survives)

- **Chat view:** for session rows with `kind=chat`. It streams `/api/chats/{id}/stream` (hello
  since → replay → live) and folds the events like the projection in AGENT-HOST.md. DOM nodes of
  unchanged parts are never re-created.
- **Rendering:**
  - Markdown, via the vendored marked + DOMPurify.
  - Reasoning collapsed by default.
  - Tool rows that expand.
  - A unified-diff viewer.
  - Command output.
  - Todos pinned above the composer.
  - Nested groups by `parent_part_id` for subagents and workflows (phases plus each agent's
    status).
  - The plan card: Approve / Keep planning with a note.
  - Notices and dividers: resumed, account switched, compacted, conversation lost, limit reached
    with renews-in.
  - Usage and cost behind the info tip.
- **Composer:**
  - The draft autosaves.
  - Sending goes through the `net.js` Outbox with a `client_id`: offline sends queue visibly and
    are delivered exactly once.
  - Stop while busy; messages sent during a turn show as "queued".
  - Attachments get server-assigned paths.
  - Dictation fills the composer.
  - A slash palette shows the commands the adapter reports.
  - A model/effort picker, and an account picker placeholder for WP7.
- **Asks:** an inline card plus the full-screen dialog on the phone. It shows one question per step
  and supports every kind in REBUILD §5.1:
  - expandable option details and context;
  - "You decide", "Other", a note per answer, or a counter-question instead of an answer;
  - Read aloud and dictation;
  - "Answered on another device" when a second device answered first.
- **Session sheet:** for chats the steps are agent → folder → model. The PreStart draft becomes the
  first message.
- **Sidebar and Home:** activity and open asks for chats, with the push/badge contracts intact.
- **Phone:** works at 390×844 with a keyboard-safe composer and no horizontal scroll. Update the
  `sw.js` SHELL list (`embed_test` enforces it).
- **e2e scenarios:** `chat-stream`, `chat-tools`, `chat-offline`, `chat-ask`, `chat-attach`,
  `chat-phone`, and the `design` extension. All run against the `fake` adapter
  (`SOCRATES_FAKE_AGENT=1`).
