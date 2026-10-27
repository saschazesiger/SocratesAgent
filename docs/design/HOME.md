# Home, questions and the answer operator — binding specification

> **Since the rebuild (REBUILD.md §5.4 and §7; WP6, WP9, WP10)** Home's questions are the chats':
> each is an agent's `ask_user` call (or a question of its own converted to one), read from the
> chat's journal, and the dialog is the chats' (`askdialog.js`, AGENT-HOST.md §7). The answer
> operator, the screen digest and the questions read in prose went with the agents' terminals.
> Home itself - the cards, the counts, the connection line (in the dialog too), the stream and the
> poll, the badge and the push - holds, read from the journal for a chat (AGENT-HOST.md §3).

Revision 3 (2026-10-20: the outcome of a turn, §5). Revision 2 (2026-10-19; revision 1 the same day, revision 2 after its review: the answer
policy and the key vocabulary, the run ending when the answer is taken, revs and activity seqs
from the clock, the signed-out close code, the per-login handshake ceiling). Extends DESIGN.md
and ACTIVITY.md; where they differ, this file wins for the features below. Asked for by the
owner on 2026-10-19:

- a **Home** button beside the sidebar's collapse button; Home is a page in the stage that shows,
  at a glance and always live, what is going on: how many sessions are running, asking a
  question, finished, waiting for a review, pending, prepared …
- the **Questions** card opens a large, touch-first dialog (a Tesla screen is the reference
  device): which session, what it is doing, the question, its options as the biggest buttons
  the screen allows, and a field for an answer of one's own;
- the answer reaches the session through an **AI operator** that drives the terminal (the owner
  chose "always the operator", not keystrokes computed in the browser);
- extras the owner chose: the question **read out loud** (and a microphone for the own answer),
  a **push** "needs an answer" that opens the question dialog itself, a **one-line AI summary
  per session** on Home, and **one question after the other** ("2 of 3") until none is left;
- Home is **always the landing page**.

This reverses one line of the 2026-09-04 decision ("the agent and the chat are gone") for one
narrow purpose: the operator only ever runs because a person tapped an answer to a question that
is on the screen, and its goal is that answer and nothing else. The chat does not come back.

Everything shipped stays English (DECISIONS.md). What a model writes for a person (summary,
context, the question) is in `settings.voice.language`, like the spoken status.

---

## 1. Server

### 1.1 Settings

`config.OpenRouterSettings` gains two fields, seeded in `Default()` and filled in `Normalize()`
like the others (new names on purpose: the old `agent_model` must not come back from an old
document):

| field | json | default | used for |
|---|---|---|---|
| `HomeModel` | `home_model` | `config.DefaultHomeModel = "google/gemini-3.5-flash"` | digests (§1.3) |
| `OperatorModel` | `operator_model` | `config.DefaultOperatorModel = "anthropic/claude-sonnet-5.5"` | the answer operator (§1.4) |
| `Outcomes` | `outcomes` | `false` | the outcome of a turn (§5) |

Admin → the "Status & naming" card (`#assistCard`) gets two more model pickers, `#orHome`
("Home summaries and questions") and `#orOperator` ("Answering questions"), added to
`MODEL_PICKERS` in admin.js exactly like `#orStatus`.

### 1.2 The event stream: `GET /api/events`

A WebSocket (coder/websocket, already a dependency), subprotocol `socrates.events.v1`, under
`s.auth`, `OriginPatterns: [r.Host]`, read limit 64 KiB. An address the sign-in has locked out
(`s.throttle`) is refused as the terminal socket refuses it; the handshake ceiling is the
stream's own, **240 per minute per login** (`allowRate("events "+loginKey(token))`, the cookie's
hash, never the token), counted apart from the terminal's handshakes: a page opens a stream
whenever it is shown, so switching between tabs and apps is dozens a minute, and behind one home
router a phone, a laptop and a car are one address. It is the one live channel of a page that
has **no terminal attached** (Home, a prepared session's panel, the empty stage) and it is open
on every page of `index.html` while the page is visible (and for a grace after it is hidden,
§2.2), so the sidebar, Home and the question dialog are live everywhere.

New file `internal/server/events.go`: an `eventHub` (`s.events`) with subscribers; each has a
buffered channel of 64 frames plus 4 per session (a burst of every session committing in one tick,
an activity frame and a pending digest each, must fit); a subscriber whose channel is full is **dropped** (its socket is
closed with `StatusPolicyViolation` "too slow"), never waited for — `publish` is called from the
detector's tick and must never block. One writer goroutine per connection; write timeout
`s.writeTimeout`.

Server → client frames (JSON text, key `t`):

```json
{"t":"hello","sessions":[<view>…],"rev":12,"digests":{"<id>":<digest>},"answers":{"<id>":<run>},"now":1792390854374}
{"t":"sessions","sessions":[<view>…],"rev":13}
{"t":"activity","sessions":{"<id>":<activity>}}
{"t":"title","id":"…","phase":"start|done|error","title":"…","text":"…"}
{"t":"digest","id":"<sessionID>","digest":<digest>|null}
{"t":"answer","id":"<sessionID>","run":<run>|null}
{"t":"news","id":"<sessionID>","body":"…","kind":"finished"}
{"t":"tick","now":1792390854374}
{"t":"pong","id":7}
```

- `hello` is the full state at connect time; `<view>` is exactly `sessionView` as
  `GET /api/sessions` returns it. A client treats every `hello` as a full resync.
- `sessions` is the **whole** list again (it is tens of rows; diffing is a bug farm). It is sent
  whenever `s.store.Rev()` has moved: a watcher loop runs every 300 ms **only while there is at
  least one subscriber**, and every mutating handler may also call `s.events.poke()` to skip the
  wait. The list is coalesced: at most one `sessions` frame per 250 ms.
- `activity` and `title` are the same frames the terminal sockets get: `onSessionActivity` and
  `emitTitle` additionally `publish` them.
- `activity` objects carry `seq` (omitted when zero, so older clients and servers still
  agree): one counter for the whole detector, moved on every published picture — a new state,
  a note, the unread mark — and copied into the committed state that `ActivityOf`/`Activities`
  hand out, so a snapshot carries the seq of the last frame about it. It starts at the clock in
  microseconds when the process started (below 2^53), so a seq from before a restart is never
  above one after it. `since` only moves with the state and cannot order two pictures of one
  commit.
- `news` is something to notify about that is not an edge of the activity, in the words the push
  says it in: a chat stopped at its account's limit (AGENT-HOST.md §11 "At a limit"). The page's
  own notification and chime say it, as for a turn's end, where Web Push does not.
- `tick` every 15 s, so the client can tell a dead connection from a quiet one (§2.2).
- **Signed out.** The login the stream was opened with is checked again on every `tick`
  (`ValidLogin`), and signing out or a new password calls `s.events.revoke()`, which ends every
  stream whose login is no longer valid at once. Either way the socket is closed with the
  application code **4401** "signed out" (a stream dropped for being slow is
  `StatusPolicyViolation` "too slow"): the stream carries every session's title, summary and
  question, and a lost phone left on Home must stop hearing them when its owner signs everybody
  out.
- Client → server: `{"t":"ping","id":n}` → `{"t":"pong","id":n}`. Anything else is ignored.
- WebSocket-level pings as `termConn.pingLoop` does (`s.pingEvery`, `s.pingTimeout`).

`GET /api/home` (under `s.auth`) answers the same content as `hello` without `t` — the
fallback poll for a page whose socket is down.

### 1.3 Digests: what a session is doing, and what it is asking

New file `internal/server/digest.go`. A **digest** is one model call per session that reads the
screen and answers two things: a one-line summary for Home, and — when the session is asking
the person something — the question in a form a dialog can show.

```json
{
  "session_id": "…",
  "rev": 1792390854374001,       // per session, +1 on every change of this object; starts at
                                 // the clock in µs when the process started (below 2^53), so a
                                 // rev from before a restart never names a question after it
  "phase": "pending|ready|error",
  "error": "",                   // a sentence a person can act on, when phase is error
  "state": "busy|idle|waiting|tasks|unknown",  // the committed activity the digest was made for
  "made_at": 1792390854374,      // when the model answered (0 while pending the first time)
  "model": "google/gemini-3.5-flash",
  "summary": "Refactoring the S3 mirror; tests are running.",
  "context": "Claude Code in SocratesAgent is adding a home page and wants to know …",
  "question": null | {
    "source": "menu|text",       // menu: a waiting prompt on screen; text: the last answer asks in prose
    "kind": "choice|multi|confirm|plan|text",
    "detail": "",                // optional: e.g. the plan in two sentences
    "items": [{
      "header": "DB",
      "text": "Which database should we use?",
      "multi": false,
      "free_text": true,         // the prompt accepts an answer of one's own
      "options": [{"label": "Postgres", "description": "Relational, robust", "recommended": false}]
    }]
  }
}
```

**The model call.** `HomeModel`, temperature 0, `max_tokens` 1500 (a ceiling: reasoning models
spend thinking from it), non-streamed. Input: harness label, committed state and note
(`stateWords`), the session title and folder, `CapturePane(id, 150)`. For Claude Code, when the
session's transcript has a pending `AskUserQuestion` tool_use (the last assistant `tool_use` with
`name == "AskUserQuestion"` that has no matching `tool_result`), its `input` JSON is given too
as authoritative structure (a several-question prompt shows only one tab on the screen). The
transcript is found the way `harnesses` verifies a CLI session (project dir of the workdir under
the session's `CLAUDE_CONFIG_DIR`, `<cli session id>.jsonl`); any failure to find or parse it is
silently ignored. The model answers one JSON object: `{"summary","context","question"}` with
`question` shaped as above minus `source` (the server sets `source` from the state: `menu` when
waiting, `text` when idle). Parsing: strip a fence, `json.Unmarshal`; one retry with the parser
error; then phase `error`. Everything is sanitised: summary one line ≤ 160 runes, context ≤ 600,
question text ≤ 1000, ≤ 4 items, ≤ 12 options per item, labels ≤ 120, descriptions ≤ 300; empty
labels dropped; an item with no options and no free text dropped; a question with no items is
null. A question is only kept when the state is `waiting` (source menu) or `idle` (source text);
for busy/tasks it is forced to null.

**When a digest is made** (the `digester`, one goroutine per run, a global semaphore of 3
concurrent model calls, a per-session generation so a newer trigger cancels an older call):

1. A committed activity change **into `waiting`** → at once.
2. A committed change from busy/tasks **to `idle`** → at once (summary, and a question in prose
   if the last answer ends with one).
3. **Any committed change out of `waiting`** (except into `unknown`, a read that failed for a
   tick) → at once: a session that leaves a question straight to idle (declined with Esc, a
   permission refused) would otherwise keep a summary that says what it asked until its next
   turn.
4. While a session is `waiting`, every 4 s: the visible pane is hashed; when the hash differs
   from the digest's and is the same on two looks in a row, digest again (a several-step prompt
   moved on, or the question was answered in the terminal and a new one appeared).
   **The hash** (`screenHash`) is of the visible lines, trailing blanks and the blank lines
   around them dropped, with what ticks masked where it ticks and nowhere else: a line that
   reports progress (a spinner frame in front of a line that is going, `⎿ …` under a tool
   call, `+N more tool uses`, `N tool uses`, `esc to interrupt`) counts as one fixed line
   whatever it says; under the menu's last row (its hint, the bottom bar), and on a harness's
   bottom bar anywhere, durations, token counts, percentages, costs and clock times are
   masked. The menu's rows and every other line above them — what is asked, the command or
   the amount it asks about — are compared as they are: a prompt for `sleep 600s` after one
   for `sleep 5s` is a different question.
5. While at least one events subscriber exists: a `busy`/`tasks` session whose last **attempt**
   (whatever it ended in) is older than 60 s and whose screen hash changed is digested again
   (the summary stays current).
   And a session whose question in prose is dropped because a turn started (answered at the
   machine) is queued with the summaries at once, unless an answer run is typing into it.
6. On `hello`, any running coding session with no digest at all (or only the "no key" error,
   once there is a key) is queued.

Failed reads back off: after N failures in a row the looks wait 4 s doubled N times, at most
60 s, so a model that keeps failing is not asked on every look at a screen whose clock ticks. A
session an answer run is typing into is not looked at; the run's end reads it (§1.4).
`POST …/digest` while a read is on its way, or within 3 s of the last one asked for by a
person, answers that read instead of starting another paid call.

Never for tools (`harnesses.IsTool`: Shell, VS Code), prepared sessions or sessions that are not
running — their last digest is kept as it is (the last summary of a finished session is still
the best line to show).

**Staleness is the server's job.** On every committed activity change whose state differs from
`digest.state`, the digest's `question` is set to null (and `rev` bumped, frame published) before
any new call starts: a client never sees a question for a state the session has left. A pending
re-digest keeps the previous `summary`/`context` so Home does not blink.

**No key**: phase `error`, error `"no OpenRouter API key is configured - open /admin and add
your key"`, no call. A model failure keeps the last summary and sets phase `error` with
`modelProblem(err, model, "home")`. A failure keeps the question only when the screen it failed
on is the very screen the question was read from (same hash) in the same state; otherwise the
question goes (the read was made because the prompt may have moved on). A failure records the
state the read was made in, as a ready digest does: a waiting session whose read failed — also
its very first read — is answered in words of one's own (§1.4), not refused as one that stopped
asking.

Digests live in memory only (re-derivable). Deleting a session forgets its digest, cancels its
call and publishes `{"t":"digest","id":…,"digest":null}`; a read that fetched its row before the
delete never brings it back (the deleted ids are remembered for the life of the process).
An answer run that typed takes the question out of the digest at once (rev bumped) and reads
the screen again, so a second device still showing it is told the question has changed.

`POST /api/sessions/{id}/digest` → 202 `{digest}`: digest now (the dialog's "Read again").

### 1.4 The answer operator

New file `internal/server/operator.go`, recovered from `git show 0411ad8^:internal/server/assist.go`
(the `agentDriver`/`agentRun`/`drive`/`decide`/`planActions`/`agentKeyName`/`parseDecision`/
`stripFence`/`trimAgentHistory` code and its tests in `0411ad8^:internal/server/assist_test.go`),
renamed to `operator…`/`answerRun`, with these differences:

- It is started **only** by `POST /api/sessions/{id}/answer` and its goal is built by the server
  from the digest and the person's answer — never free-form instructions from the client.
- Request: `{"digest_rev": 3, "answers": [{"item": 0, "choices": ["Postgres"], "text": ""}], "text": ""}`.
  `choices` are option labels of `digest.question.items[item]` (validated: must exist; one
  choice unless `multi`); `text` per item is the answer of one's own for that item (requires
  `free_text`, or kind `text`), ≤ 2000 runes; the top-level `text` is used when there is no
  digest question at all ("answer what the terminal asks with this"); beside a question with
  items it is ignored and never typed. At least one answer. An answer of one's own is one line:
  line breaks and tabs become spaces before the goal is written (the goal says exactly what is
  typed), and any other control character is a 400.
- Refusals: 400 no key / not a coding harness / not running; 404 `"the session was deleted"` for
  an answer that reached the run's start after the session's delete began (the delete marks the
  id before it stops the run, so no run can start behind it; a delete that fails takes the mark
  back); 409 a run is live (`{error,run}`).
  409 `{"error",…,"digest":…}` (so the dialog redraws instead of answering a question that is
  gone) when: `digest_rev` is not **equal** to the current digest's (`"the question has
  changed"`; 0 when there is no digest); the digest is being read again (`"the question is being
  read again"`); its last read failed while it still holds a question (`"…read it again before
  answering"`); the session is not asking — the committed state is neither `waiting` nor (for a
  question asked in prose) `idle`, or is not the state the digest was made in (`"the session is
  no longer asking this question"`: a question-less digest's rev does not move with the state, so
  the state is compared); or, for a menu, the visible screen's hash is not the one the question
  was read from (a prompt that moved on in place, the state never leaving `waiting`) — the
  screen is read again and the 409 carries the new pending digest.
- The goal text names the session, quotes the question items and their options as the digest has
  them (quoted, newlines and all, so a screen cannot write a line that reads as the server's),
  and states the person's answer per item exactly. The system prompt carries the harness key
  facts (below), the answer policy and the done rule: **done when the question is no longer on
  the screen and the program has taken the answer** (busy, a new turn, or a next prompt that is
  a *different* question); if the screen shows a different question than the one quoted, or none
  at all before any key was sent, stop at once with `done:true` and a summary saying so, and send
  nothing.
- **The answer policy** (`answerPolicy`, decided by the server, never by the model): the model
  reads a screen anybody can write on, and a line typed into a coding agent is a prompt it obeys
  or, after `!`, a shell command. So a `text` action may only be the person's own words from the
  goal, exactly and once each (on a prose question also each chosen label, and the whole answer
  line), or — on a menu — a row's number (1–99) or letter. Any other text refuses the whole
  step: nothing of it is typed and the run ends as `error`. Text with a control character is
  never typed (sent byte for byte it would be a key, past every guard); newlines in text become
  spaces, submitting is always an explicit Enter.
- **The key vocabulary** is fixed and the same for every harness: `Enter Escape Tab BSpace Space
  Up Down Left Right Home End PageUp PageDown` (spelling variants such as `esc`, `return`,
  `backspace`, `pgdn` are mapped). There is no `C-c`, `C-d`, `C-z` or `C-l`: an interrupt, an end
  of input or a suspend answers no question, and in a coding agent they stop its turn or end the
  program (in the first review a model's Ctrl-C killed the session it was answering).
- **The server decides when the question is gone**, not the model's `done` alone. There is no
  wait for the turn before the first step: the run reads the screen at once. Before the first key
  every step holds the screen to the question — the committed state left the one it was asked in,
  the digest's rev moved, or (a menu) the visible screen's hash changed → the run ends with
  nothing typed ("nothing was typed: …", phase `error`) and the digest is read again. Once a key
  is in, the committed state moving on from the asked state **is** the answer taken: the run
  ends `done` at once ("The answer was taken; the session is working on it."), it does not wait
  out the turn the answer started. A model's `done:true` with keys sent is held for 5 s to what
  the program shows (the state moved on, or the screen is no longer the one the answer was sent
  against), else it ends `error` "the keys were typed, but the program did not show that it took
  the answer"; `done:true` with no key sent is never "Answered" (the stop rule: phase `error`,
  "nothing was typed: …"). The step limit reached is `error` too.
  An answer with parts still to type is not "taken" by any of these: while the person's own
  words or the Enter that sends them are owed, only the session going to work ends the run,
  as `error` "the session went on before the answer's own words were typed and sent"; while a
  choice of theirs or the Submit is still to come, a changed screen that still shows the
  question (a resize, a line rewritten above it) is the same question, and the question
  leaving the screen, or the state moving on, with a choice of theirs not typed ends as
  `error` "the question went from the screen before the whole answer was typed".
- Harness key facts given to the model (verified on Claude Code 2.1.303 — see
  `docs/design/HOME.md` §4): AskUserQuestion: a digit chooses option N of a single-select tab
  and moves to the next tab; Up/Down + Enter works too; for an answer of one's own move to the
  "Type something." row (its digit moves the cursor there), type the text, Enter; multi-select:
  digits toggle `[ ]`/`[✔]`, the free-text row must be reached with Down, "Submit" is an
  unnumbered row below the options (Down … Enter); several questions are tabs (Tab/Right), and
  the last tab "Review your answers" takes `1` for "Submit answers"; Esc dismisses (never use it
  unless the answer is to decline). Plan approval: `1`/`2` approve, `3` "Tell Claude what to
  change" then text then Enter. Permission prompts: the digit, or Enter for the highlighted one.
  Codex: the digit or Enter, Escape declines. OpenCode shows its highlight in colour only, which a
  screen read as text does not carry: on a permission card Enter allows once, and Right and Left
  move the highlight one option along Allow once / Allow always / Reject. Where the highlight is
  is read off a capture with its colours (`StyledPane`, `litOption`), never followed from the
  first option: somebody at the machine may have moved it. Each step tells the model which option
  it is on, and an Enter on a screen with no cursor in its text is held to that option (refused
  when it cannot be told); its question tool (a card headed Question, the `question.asked`
  event, which the detector counts as `waiting`): the option's digit takes it at once, never
  Up/Down and Enter; the last row's digit, the text and Enter give an answer of one's own;
  Escape dismisses. A question asked in prose (source `text`):
  type the answer on the prompt and press Enter.
- Budget: 8 steps, 4 min wall, 8 actions per step, **45 s per step** (a gateway that took the
  request and never answers is not a dialog saying "Choosing Postgres…" for four minutes).
- `manager.NoteInput(id)` after each key, as before: a person asked for these keys.
- Run object, broadcast on the event stream (`{"t":"answer","id","run"}`) and on the session's own
  terminal sockets (`s.broadcast`, same frame), kept 2 minutes after it ended:

```json
{"run_id":"…","session_id":"…","phase":"waiting|thinking|acting|done|error",
 "step":2,"action":"pressed 2","summary":"","error":"","answer":"Postgres",
 "started":1792390854374,"ended":0}
```

  `answer` is the person's answer as one readable line (what the dialog shows while it works).
- `GET /api/sessions/{id}/answer` → `{run|null}`; `POST /api/sessions/{id}/answer/cancel` →
  `{ok, cancelled}`. Deleting, restarting or marking a session Done (by hand or by the sweep,
  which both park it) cancels its run **before** anything else happens to the pane, and waits up
  to 3 s for it to let go, so a key already on its way lands in the old program and never in the
  new one; a run stopped by Done ends `error` "the session was marked done, and its terminal was
  closed". An answer to a session marked done is refused with 409 and that same sentence, also
  one already past its checks when the Done came: the status is written before park stops the
  run, and `begin` reads it under the lock `stop` takes, so such an answer either is the run
  the Done stops or never starts (never a run that fails on "the terminal is gone"). The
  dialog shows that refusal with "This session was marked done." and no Try again.

### 1.5 Push opens the question

`pushNote` gains `kind`: `"question"` when the news is "Needs an answer", `"finished"`
otherwise (the scheduler's announcements: `"started"`). sw.js copies `kind` into
`data`; `notificationclick` posts `{t:'open', id, kind}` to an open tab, and a cold start opens
`/#q=<id>` for a question and `/#open=<id>` for anything else.

The payload is `{title, body, id, kind, badge, asking, at}`. `kind` is `question`, `finished`,
`started` (the scheduler) or `test`. `badge` and `asking` are the **asking set** when the message
went out: the sessions Home's Questions card counts (`askingSet` in asking.go, the same rule as
home.js `cardsOf`: not done, not a tool, not prepared, live, and `waiting` or idle with a digest
question of `source: "text"`; background sessions are counted). They are left out when the set
could not be counted, and a worker that finds them missing leaves the badge and the tray alone.
`at` is when they were counted (server milliseconds, strictly increasing): pushes reach a phone in
any order, so the worker applies a count only if it is not older than the last one it applied.
Digests live in memory; after a restart every coding session without one is read on its first
state (digester `start`), so an idle question in prose is in the set without a page connecting.

- **When a question is pushed** (push.go `asks`): the committed activity enters `waiting` from
  `busy`, `tasks` or `idle` with the unread mark set, for a coding session that is not
  background. Once per prompt: no second question push until the session has been seen busy,
  tasks or idle again (a waiting → unknown → waiting flicker is the same prompt). A pane that is
  gone or dead (unknown with source `pane`) drops the question: the next program's prompt is new.
- **Its words**: the push waits up to `pushQuestionWait` (8 s) for the digest of that very
  waiting commit. A menu question sends its first item's text (one line, ≤ 180 runes); a digest
  error, no key or the timeout sends "Needs your permission" (a permission prompt) or "Needs an
  answer". A session that leaves `waiting` first is sent nothing.
- **Topic**: one per session (a hash of its id), so a phone that was off gets the last word.
- **The worker** shows every push (iOS revokes a subscription whose pushes show nothing), then
  sets the app badge from `badge` and closes delivered `question` notifications whose id is not in
  `asking` (both skipped for a count older than the last applied, kept in the `socrates-push`
  cache), and posts `{t:'pushed'}` to open pages. A tap on a tab at another path focuses it and
  then navigates it (only one this worker controls); otherwise it opens a window.
  `pushsubscriptionchange` subscribes again and posts `{subscription, old_endpoint}`.
- **The page** sets the badge too, from `questionQueue`, whenever it holds a current state
  (notify.js `asking`), and closes answered questions in the tray the same way. After it comes
  back into view or is told `pushed`, it writes both again (forced) only from a state the server
  sent after that moment (a hello, a poll, or the pong to the ping it sends then).
- **`#questions`** (the manifest's Questions shortcut) is Home with the dialog at the oldest
  question, or Home alone when nothing is asking.

---

## 2. Browser

### 2.1 Modules and markup

- `js/events.js` — the event stream client (§2.2).
- `js/home.js` — the Home page: `mountHome(ctx)` → `{show, hide, shown, render}` (the
  `mountPrestart` pattern: `ctx` carries `sessions()`, `activityOf`, `statusOf`, `select(id)`,
  `newSession()`, `openQuestions(id?)`, `digestOf(id)`).
- `js/question.js` — the question dialog: `openQuestions(ctx, startId?)`.
- All three are imported statically from session.js (`from './home.js'`) and added to sw.js
  `SHELL` (embed_test enforces it). Styles go into app.css.
- index.html: `button#homeBtn.icon-btn.home-btn` (a house, `aria-pressed`) in `.brand-row`
  **before** `#sideCollapse`; it is shown in the phone drawer too (it must not carry
  `.side-toggle`, which phones hide) and stacks in the rail. `section#home.home[hidden]` is a
  third child of `#stage`.

### 2.2 Live, always

`events.js` opens `/api/events` while the page is visible; reconnects with backoff 0.5 s → 8 s
(reset after a `hello`), on `onWake`, `online` and when no frame (including `tick`) arrived for
35 s. While it is down the page polls `GET /api/home` every 5 s (Home or the dialog shown) or
15 s (otherwise). Every `hello`/`sessions` frame goes through the same path `refreshList` takes
(one function in session.js, `applyList(rows)`), every `activity` through `mergeActivity`, every
`title` through `titleFrame`. Home shows the connection plainly: "Live", "Reconnecting…" or
"Offline · updated 40 s ago" (seconds below 90 s, never "just now" once it is stale; the line is
said again every 2 s while not live), and its numbers are dimmed while not live. "Connecting…"
is only the first look before anything has failed: a page whose reads have all failed is
"Offline". A page shown again after its stream was closed for being hidden is "Reconnecting…"
(dimmed) until its first handshake or poll has succeeded or failed - it is not offline, only
old. When the stream
has been refused three handshakes in a row and the poll still answers (a proxy that does not pass
WebSockets), the line says "Updated every 5 s" (the poll's interval and the time its last read
took, which on a slow link makes it "every 12 s"; `/api/home` and `/api/sessions` are gzipped for
a client that accepts it) and the numbers are not dimmed - only once a poll
(or a list read) has got through since the stream was lost, and only for the three refusals
after it: refusals with no read since are a server that may be down, and say "Reconnecting…"
until the poll answers or fails. The first read that gets through after a loss throws the
refusals counted before it away (a server that restarted refused them) and tries the stream
again at once (events.js `noteRead`), so a server that is back is "Live" and never "Updated
every 5 s" over numbers the stream has not confirmed. Relative times
redraw every 15 s from the last data, never from a guess, and are measured on the server's clock:
`hello`, `tick` and `GET /api/home` carry `now`, and the page keeps the difference to its own
clock (events.js `serverNow()`), so a device whose clock is minutes off still says "just now" of a
question asked a second ago. The connection line's own ages ("updated 40 s ago") are the
device's: they count from when it last heard anything.

**Quiet.** The 35 s silence is a last resort, not the way a half-open socket is found: a live
stream that has heard nothing for 10 s sends a `ping` (the watch runs every 2 s); a ping
unanswered for 2 s makes the page say **"Checking…"** with the numbers dimmed, and one unanswered
for 5 s is a reconnect. A socket that dies quietly is "Checking…" within about 14 s and
reconnected within about 17 s, never "Live" over old numbers for half a minute. While it is
quiet the question dialog asks `GET …/answer` for its runs, as it does while the stream is down.

**The dialog says it too.** The question dialog is a modal over Home, so Home's line is behind
it. The dialog shows the same line by the same rule (home.js `connectionLine`) under its top bar
whenever it is "Connecting…", "Reconnecting…", "Checking…" or "Offline · updated 40 s ago" -
with "This question may already have been answered." while a question is offered - and dims the
question and its options, the line said again every 2 s. Send is not held: an answer sent while
offline is "Checking whether the answer went in…", and one that reaches the server for a
question that went is refused by its `digest_rev` (409). A question asked for by a link says
"Looking for this question…" / "No connection…" instead.

A handshake has 10 s from `new WebSocket` to its `hello`, CONNECTING included: a proxy that
accepts the upgrade and never answers is given up on, and `onWake`/the watch treat a socket stuck
in CONNECTING as down.

**Hidden.** A hidden page keeps its stream for 30 s, so a quick switch to another app or tab
comes back to a page that never stopped being live (no handshake, no "Reconnecting…"); past
that it closes it and polls nothing. A page shown again within the grace is only as fresh as
what it heard, and a phone freezes a hidden page: a stream that is live and has said nothing for
more than 20 s (a live one says something at least every `tick`) is opened again at once - its
`hello` is the whole state - rather than pinged while the page shows what it held.

A `hello` is also the end of every naming the page was told had started (`title` "start")
other than one it asked for itself: nothing in it carries naming, and a naming whose end was
lost (a restarted server, a frame sent while the stream was down) would otherwise pulse, with
its regenerate button disabled, until a reload. A naming whose end is not heard within 45 s
(the server's 30 s and a margin) is let go of the same way.

**Signed out.** A close with code 4401 is never reconnected at once (a handshake with a login
that is gone is refused, and refusals in a row read as a blocked stream). The page asks
`GET /api/home` first: a 401 takes it to `/login` the way every other request of the app does
for an expired login (net.js); an answer - the device that changed the password and was handed a
new login - is drawn as a `hello` and the stream is opened again from the start of the backoff;
no answer is the ordinary backoff, with the poll running. The same holds for a 4401 sent at a
`tick` for a login that simply expired.

**Order.** The stream's frames are in order on one socket, and a `hello` starts the order again.
A list read over HTTP (`refreshList`) while the stream is live is dropped when its `rev` is older
than the list on screen, and its embedded activity is only taken for sessions the page has no
activity for: the stream carries every committed change as an `activity` frame. An activity older
than the one held (`seq` where the server sends it, otherwise a strictly older `since`) is never
drawn over it. A digest or run that comes back on an HTTP answer (Read again, a 409, a poll) is
only taken when it is newer than the one held (a higher digest `rev`; a later phase or step of
the same run, or a run started later); a `hello` keeps a held run that started after its `now`.

### 2.3 Routing and landing

- `#home` (and an empty hash) is Home; `#<id>` is a session, as today.
- Boot: `#q=<id>` → Home and the question dialog for that session; `#open=<id>` → that session;
  **anything else → Home** (the owner's choice: Home is always the landing page). A signed-out
  tab sent to `/login` keeps the fragment, and the sign-in goes on to `/#q=<id>` or
  `/#open=<id>` rather than to `/`: a notification tapped after the login expired still opens
  what it was about.
- `showHome()`: `detach()`, the top bar reset the way `showEmpty` resets it, title "Home",
  `#termWrap`/`#prestart` hidden, `#home` shown, `location.hash = '#home'`, `state.wanted = ''`,
  `closeNav()`, `#homeBtn[aria-pressed=true]`, no sidebar row active. `attach()` hides Home.
  `keepTypingAlive` does nothing while Home is shown. Where the page used to fall back to
  `showEmpty()` (deleting the open session, a prepared session deleted elsewhere, boot) it now
  shows Home; Home's own empty state replaces "No session open".
- A notification click with `kind:'question'` opens the dialog; otherwise the session - except
  that a "Finished" whose session's digest now holds a question asked in prose (`source:
  "text"`) opens the dialog too: the digest that found the question came after the news.

### 2.4 The Home page

Counts are computed in the browser from the list and the activity map with the sidebar's own
predicates (`activityOf`, `statusOf`, `isPrepared`, `start_at`, live = running|starting|resuming),
so Home and the sidebar can never disagree. A service filed `background` finishes turns so as not to
interrupt anyone (no chime, no notification), so its unread turns are not Finished; a real prompt
from it is still a Question. Tools (Shell, VS Code) have no activity and appear
only in the session list and in "Not running". Done sessions count only in Done.

| card | rule (live coding sessions unless said) |
|---|---|
| **Questions** | `waiting`, or `idle` with `digest.question.source == "text"` |
| **Running** | `busy` |
| **Background tasks** | `tasks` |
| **Finished** | unread, without a text question: `idle`, `unknown`, and also a stopped session (it counts in Not running too); not for a session filed `background` |
| **Ready** | `idle`, read (or filed `background`), without a text question |
| **Review pending** | `status == review` (any state) |
| **Pending** | `status == pending` (any state) |
| **Not running** | `needs_resume`, `exited`, `failed` (not done) |
| **Done** | `status == done` |

Layout: the Questions card is the first and largest; when it is above zero it is amber and says
"Answer" — a tap opens the dialog at the oldest question. The other cards are a grid; a tap
filters the list below to that card (tap again: all). Cards with zero are drawn quiet, not
hidden, so the grid does not jump. Below: the sessions of the filter (default "Needs you":
questions, finished, review pending, of running sessions only - a stopped or ended one is under
"Not running" and "All"), each a large row: the harness mark, title, folder, the
state in words, the digest summary (or "…" while pending), how long ago; a tap opens the
session, a question row has its own "Answer" button. A row is two sibling buttons - its title (opens the
session) and Answer - never a button inside a `role=button`. The empty state offers New session.

### 2.5 The question dialog

A `dialog.sheet.question-sheet` opened with `showModal()` (keyboard.js needs a modal dialog),
full screen below 900 px and on `html.touch`, otherwise up to 760 px wide. A queue of the
question sessions, oldest first (the one asked for first), "2 of 3" with Previous/Next.

- Top: the session (mark, title, folder) and a large **Open session** button.
- `context` (the digest), then the question `text` in large type, `detail` when there is one.
- The options as full-width buttons, at least 64 px high, 20 px type, the description under the
  label; a recommended option is marked. They are toggle buttons (`aria-pressed`, no radio role).
  Single choice: a tap selects it (a second tap, or the big **Send** button, sends). Multi: taps
  toggle, **Send** sends. Several items: one item at a time with its own step ("Question 1 of 2"),
  the big button says **Continue** until the last item, and the answers are sent together at the
  end. (The arrows at the top, "Previous question"/"Next question", move between sessions.)
  Nothing is sent while the digest is `pending` (Send, a second tap and Enter alike).
  The dialog opens on the first tap of what asks for it, and a new question or step is drawn on
  the tap of the one before, so what is drawn under the finger then takes no tap for 0.6 s
  (question.js `TAP_ARM`: the options, Send/Continue, Back, Try again, Open session -
  `pointer-events: none` under `.q-arming`, and a capture that drops a pointer click): the second
  tap of a double tap on Home's Questions card never chooses the option drawn where the card
  was, and a third never sends it. A key (Enter, a digit) is never a stray tap and is taken at
  once. A redraw of the same question and step is not armed again.
- **Your own answer**: a textarea built with `dictated()` (so the microphone is beside it and
  touch mode handles it), shown when the item allows free text (or the kind is `text`); typing
  in it replaces the chosen option for that item. A digest with no question (also one whose
  reading failed) of a session that is `waiting` shows a field "Your answer" whose words Send
  sends. They go only onto a line prompt: on a screen that shows menu rows, which nobody read,
  their first digit or letter would take a row, so the run refuses them before any key ("nothing
  was typed: …") and the screen is read again. A session at its prompt with nothing asked gets no field (the server refuses it: the
  words would be a new task). A 409 that carries a digest is followed by the walk at once: a
  question answered at the machine while the answer was on its way is "answered elsewhere".
- **Redraws never move the person.** Another session starting to ask, a rename, a digest read
  again: only the part that changed is drawn again (the counter, the title), and the field of
  one's own is kept as the same element while it shows the same question, so its text, cursor,
  keyboard and a running dictation stay. After a redraw the focus goes back to what had it, or to
  the question heading - never to an option the person did not focus, where the next Space or
  Enter would choose and send it.
- **Read aloud**: one large button; `speak(context + question + detail + options)` (the detail -
  a plan approval's plan - is read with the first item, under which it is drawn), a second tap stops
  (the assist.js `say()` pattern). Never automatic. Its failure, and a dictation's, is said on the
  dialog's status line (kept for 10 s, or until the next Read aloud or another question) - never
  only as a toast, which is under the top layer. With no Text-to-Speech key (`/api/preferences`
  `tts`, voice.js `ttsKey`) its tip says "Needs a Text-to-Speech key (Admin, Voice)" and a tap
  opens the Close / Open Admin dialog dictation has without an OpenRouter key; nothing is sent.
- While the digest is `pending`: "Reading the question…" with the previous content dimmed;
  `error`: the sentence and **Try again** (`POST …/digest`) and Open session. An `error` that
  still holds its question is not answered (the server refuses it until it is read again): Send
  is disabled, and a 409 for it says to read it again instead of offering a Try again that sends.
  The no-key error offers no field and no Send (the server refuses every answer without a key):
  the sentence, **Add a key** (`/admin#voiceCard`) and Open session; a 400 for no key has no
  Try again.
- A send whose reply never came (no network, a reset, the 20 s timeout) is not "nothing was
  answered": the request may have got there. It says "Checking whether the answer went in…"
  and follows a run the stream shows, or asks `GET …/answer` (again every 1.5 s while the
  server cannot be reached): a run that is not the one held before the tap is this answer, and is
  followed and counted as answered here; no such run is "The answer did not reach Socrates.
  Nothing was answered." with Try again.
- After Send: the run's progress line ("Choosing Postgres…", the operator's `action`), then
  "Answered" with the summary; after 1.5 s the next question in the queue, or the dialog closes
  when none is left ("All questions answered" only when an answer went in from this dialog,
  "No question is waiting" otherwise). An error stays with **Try again** and **Open session** - also
  when the session then stops asking, which is said under the error and takes Try again away; a
  failed run is never relabelled "answered elsewhere". Cancel while it runs; a Cancel the server
  answers with `cancelled:false` reads the run and shows how it ended. Cancel appears where
  Send was, so it takes no tap for 0.7 s after it appears (the second tap of a double tap on
  Send is not a wish to stop); once the run has been seen typing (`acting`) it turns into
  **Stop** with a line saying the prompt may be left half answered, and only a second tap
  within 4 s stops it.
- A dialog opened while a run for that session is live shows the run (progress, Cancel), not
  the question to answer again - and so does one already open when another device starts one:
  the options lock and the line says "Another device is answering: Postgres" (with "already"
  when this dialog's own Send was refused for it). A run this page did not send (the page keeps
  the ids of the runs it started, across dialogs) ends as "Answered on another device" and does
  not count towards "All questions answered"; if it fails or is interrupted, the question is open
  here again with a note saying so. The ids are the browser's, not the tab's (localStorage, for
  10 minutes): a second tab of the same browser, or a tab reloaded while its answer goes in, is
  the same device. A reply that arrives after its dialog was closed still records its run. A send
  whose reply has not named its run yet is kept too (by session, with the run held before the
  tap, for 5 minutes): the stream shows the run to every tab before the sending tab has its
  reply. A run seen while that reply is still on its way (under 25 s, question.js `whose`) is
  followed but only provisionally its own ("Sending your answer…" still, and no Cancel, which
  could stop another device's answer): another device's answer that started first reaches
  the stream the same way, and the reply then refuses this Send for it (409 with its run), so it
  ends "Another device is already answering" and does not count; its end ("Answered", an
  error) is only said once the reply has said whose it is. A reply that names the run makes it
  this browser's. A reply that never came (`lost`, or not within 25 s: the sending tab closed)
  cannot be told apart any other way, so a run of that session that is not the one held before
  the tap is this browser's (the rule "Checking whether the answer went in…" follows). Every run the dialog sent is followed, not only the one on screen. A run whose end was not heard (the server restarted, the finished run expired while the
  tab slept: a `hello` or `GET …/answer` without it) says "Checking whether the answer went in…"
  until the session is read again: still asking the same question is "The answer was
  interrupted, and the question is still waiting." with Try again; anything else is "The question
  is no longer waiting.". A run with no word for 5 minutes (4 min wall and a margin) is treated
  the same way.
- Live: a session that stops waiting while it is shown (answered in the terminal or elsewhere)
  says "This question was answered elsewhere" and moves on; a new digest of the same session
  redraws (a 409 "the question has changed" too; a 409 for the same question keeps the choice).
  New questions join the end of the queue. A session that was deleted is never "answered
  elsewhere": an answer that failed keeps its error with "This session no longer exists." under
  it and neither Try again nor Open session, an answer refused because the session had just been
  deleted says the same, and a question or a lost run of a deleted session says "This session no
  longer exists." The body of a deleted session never says "Reading the question…".
- A session whose program stops (`needs_resume`, `exited`, `failed`: a crash, a kill, a quit
  at the machine) is never "answered elsewhere" either: "The session stopped before the
  question was answered." stays on screen with Open session until the person moves on (a lost
  run of one says "The session stopped while it was being answered."); started again and asking,
  it is a question again. A session marked Done (here or on another device) has its terminal
  closed too (`needs_resume`), but that is the person's doing, not a stop: it says "This session
  was marked done." and moves on like a question answered elsewhere, with no Open session (an
  answer that failed keeps its error with that line under it, and no Try again or Open session).
- `unknown` is not known yet. A server that has just restarted reads every pane again, and
  until its first commit every session is `unknown` in the `hello`; a read that found nothing
  says the same for a tick. For a running session the page keeps the last state it knew
  (session.js `heldOver`) until a known one replaces it, and lets go of it when the row stops
  running; so Home does not say "Live" with 0 questions across a restart. The dialog never calls
  a running session in `unknown` gone, never drops it from the walk, and keeps a link or a
  notification in "Looking for this question…" until its state is known. The choices made for a
  question survive a moment with no digest (a restarted server holds none until it reads again):
  only a different question clears them - and a different question always does, whatever went
  on meanwhile. Two questions are the same when what the screen fixes is the same: the kind, and
  per item its `screen_text` (the screen's or the program record's own wording; its `text` only
  when it has none, which is then all that tells it from another question), `multi`,
  `free_text` and the option labels in order (question.js `signatureOf`). What the model words
  itself - the header, the text in the person's language, the descriptions, `recommended` - is
  left out, so the same screen read again in other words (after a restart, a resize, Read again)
  keeps the typed answer and the choice, and a lost run is still "interrupted", not "no longer
  waiting". The question the choices belong to only moves while they can be made (asking, or
  an error): one that changed while an answer was going in, or after the question went, is found
  different once the dialog is back to answering. A failed answer that comes back to a different
  question (answered at the machine while the operator ran, then asked again) clears the choices
  and the error, and says "The question has changed. This is the new one." - the old answer is
  never shown pressed on the new question. An answer sent again from its error (Try again, a
  second tap on the chosen option, Enter) goes with the current `digest_rev` only while the
  question is, by everything the page sees, the one it was given to (same items and options,
  same `detail`: a restarted server reads every question again under a new rev, and Try again
  is still one tap); otherwise - a prompt that only looks the same, "Proceed? Yes / No" for the
  next command - with the rev it was first given to, so the server refuses it (409) and the
  dialog shows it ("send it again"), never answering it with the answer meant for the one before. The digests are held the same way: a running
  session the `hello` has no digest for (a restarted server has read nothing yet; a server
  only forgets the digest of a deleted session) keeps the one the page had, as `pending`, and
  a `pending` digest with nothing in it keeps the summary, context and question the page held,
  as the server does within its own life - so a question asked in prose stays counted. A question
  kept that way is `carried` (session.js `carriedOver`): the new server has not seen it, and it may
  have been answered at the machine while there was none. An idle session counted only by a
  carried question in prose says "Checking whether it still asks…" and has no Answer button
  until the read replaces it (home.js `unconfirmed`); a `waiting` one keeps its Answer, since the
  detector says it is asking.
  Running is what the `hello`'s own rows say, not the list held before it (across a reboot
  that list still says running where the hello says `needs_resume`). A session the hello lists
  as not running keeps its summary and context but not its question, and is not `pending`: a
  stopped program is never read again, so nothing would replace it, and once resumed its last
  life's question would be counted again and shown as being read for good.
- A question asked for (a link, a notification tap, a card) or one that starts asking while the
  last word is on screen opens the walk again rather than being closed with the dialog.
- A question answered here leaves the walk with the rev of its digest and the commit (`since`)
  of the activity it was asked in. The same session joins again once its digest has a newer
  question, or at once when it has started asking again since (a later `since`: it took the
  answer, went on and asks a follow-up Home already counts) - shown as "Reading the question…",
  with Send off, until its digest moves past the one that was answered. That is so whether the
  follow-up comes while "Answered" is shown or on the last word.
- `#q=<id>` and a "needs an answer" tap are only judged against a state newer than the moment
  the session was asked for: a `hello` since, a poll sent since, or a `pong` to a ping sent
  since (every frame before it has arrived). A stream that only calls itself live is not
  enough - a half-open socket is live and carries nothing until it misses a tick - so asking
  for a session pings the stream at once (or reconnects and polls one that is down): until
  then the dialog says
  "Looking for this question…", or "No connection…" when the list cannot be read, and stays
  open. A session that is not asking says "No question is waiting here any more."; an unknown one
  "This session no longer exists.". The page's own hash normalisation (boot, `#q=`, `#open=`, a
  session that went away) uses `history.replaceState`, so Back never reopens a link.
- On a short screen (`max-height: 500px`, a phone on its side) the bars shrink, the session is
  one line and the context is two lines that open with a tap, so the first option is on screen.
- Errors are shown inside the dialog (toasts are under the top layer).

---

## 3. Tests

- Go: the event hub (hello content, a `sessions` frame after create/rename/delete, activity and
  title forwarded, a slow subscriber dropped without blocking `publish`, auth and origin
  refused), digests (each trigger; staleness clearing; no key; model failure keeps the summary;
  cancel on delete; the semaphore; sanitising; the Claude transcript lookup), the operator (the
  ported old tests, the goal text, `digest_rev` 409, validation of choices), push `kind`,
  config defaults.
- faketui: a question mode driven like the real menus (§4) for e2e.
- e2e: landing on Home; live counts with no session attached; the whole question flow with a
  stubbed gateway (digest JSON, operator actions) reaching the pane; an own answer; two
  questions in a row; answered elsewhere; `#q=` deep link; server restart → reconnect and
  resync; a Tesla user agent (button sizes, the keyboard button in the dialog); every kind of
  question (multi-select, tabs and review, plan feedback and approval, prose); the operator
  held to the answer (a `done` that was not, a prompt asked again, a row not chosen, letters
  after a row); a restart under an open dialog (counts, walk, choices, ages, prose); two
  devices on one question; a lost reply, a dead program, an outage that is never "polled";
  the operator on the program's prompt (an Up that would send an old prompt, an arrow on a menu
  nobody read, the words edited once typed, a part of a prose answer, the Enter after a digit
  on a prompt asked again); OpenCode's colour-only cards (Allow always, Reject, an own answer);
  a prose question whose read failed or had no key, read on the open Home; a question that moved
  on under a failed or running answer; tabs that share labels read in German; ages on a skewed
  clock, the dialog offline, a quiet stream noticed in seconds, a gzipped poll.

## 4. Facts the operator prompt relies on

Captured on Claude Code 2.1.303 (single question):

```
 ☐ DB

Which database should we use?

❯ 1. Postgres
     Relational, robust
  2. SQLite
     Embedded file
  3. Type something.
──────────────────────────
  4. Chat about this

Enter to select · ↑/↓ to navigate · Esc to cancel
```

Several questions add a tab strip `←  ☐ Color  ☐ Features  ✔ Submit  →`, the hint
`Enter to select · Tab/Arrow keys to navigate · Esc to cancel`, and a last tab "Review your
answers … Ready to submit your answers? ❯ 1. Submit answers  2. Cancel". The session file says
`"status":"waiting","waitingFor":"input needed"` for a question and `"permission prompt"` for a
plan approval ("Would you like to proceed? ❯ 1. Yes, and use auto mode 2. Yes, manually approve
edits 3. Tell Claude what to change").

---

## 5. The outcome of a turn

Asked for by the owner on 2026-10-20 (DECISIONS.md, Revision 8): an optional check, like the
naming, that runs whenever a session comes to rest after a turn and says how the turn left the
work. Code: `internal/server/outcome.go`, `js/outcome.js`.

### 5.1 When

`settings.openrouter.outcomes` (Admin → Status & naming, "Check each turn's outcome", **off** by
default, global) and an OpenRouter key. A committed change into `idle` from `busy`, `tasks` or
`waiting` (`endsTurn`), in a session somebody typed at (`Activity.Asked`), of a running coding
harness, with no answer run live. Never from `unknown` (start, restart, relaunch, resume, a read
that failed for a tick). `onSessionActivity` calls `digests.outcomeEdge` **before** the activity
frame goes out, so every page hears that a verdict is coming before it hears the turn ended.

### 5.2 One read

Not a call of its own: the digest the same change starts (§1.3 triggers 2 and 3) is the read.
While an outcome is pending (or failed) for the idle the read is made in, `digestSystemPrompt`
gets `outcomeSystemPrompt()` and the call gets `max_tokens` 2000; for Claude the user prompt is
headed by the turn from its transcript (`readClaudeTurn`: the last prompt the person typed, ≤2000
runes; the answer after it, its last 3000 runes; the task list replayed from `TaskCreate` and
`TaskUpdate`, ≤20). With the setting off a digest is asked byte for byte what it always was.
The answer gains `"outcome":{"kind","reason","todos"}`: `complete | partial | needs_input |
blocked`, a reason of ≤160 runes, ≤5 todos (partial only), **always English**. A question the
same read found makes it `needs_input` (`sanitizeOutcome`). A read that fails, a missing key or
a missing or unknown kind is phase `error` with the reason. "Read again" by hand reads the
verdict again; the look loop's retries of a failed read refill a failed verdict.

### 5.3 The object

```json
{"session_id":"…","rev":1792…,"since":1792…,"phase":"pending|ready|error",
 "kind":"partial","reason":"…","todos":["…"],"error":"","due":1792…,"made_at":1792…,"model":"…"}
```

`since` is the Since of the idle it judges; a page shows it only while the session's activity
has the same `since` and is `idle` (`outcomeCurrent`). `due` is when a waiting notification
gives up (start + 45 s). Frames `{"t":"outcome","id","outcome"|null}` go to every terminal
socket and every event stream; the hello and `GET /api/home` carry `outcomes`. It is dropped on
`busy`, `tasks` or `waiting`, kept through `unknown`, and carried onto the same prompt in a new
commit after a tick of unknown. Kept in kv `outcomes` (finished ones only, never mirrored to the
bucket): after a restart one is shown again only while its session is at that idle. Switching
the setting off drops every verdict (`clearOutcomes`); deleting a session drops its own.

### 5.4 Notifications

`push.observe`: a "Finished" whose outcome is pending is held (`holdFinished`) for at most
`outcomeWait` = 45 s, then sent saying the verdict (`outcomeBody`): "Complete · reason",
"Partly done · N open: first", "Blocked · reason", "Waiting on you · reason", and a turn that
ends asking as its question (kind `question`). Timeout, error or the setting switched off:
"Finished". Dropped, unsent, when the session leaves the idle or somebody reads it first. The
page's chime, notification (`notify.js` `hold`/`judged`) and screen-reader line wait the same
way, until `due`. A verdict that comes after the wait sends nothing more. `waiting → idle`
sets no unread mark and so is not pushed, as before; it is judged and shown.

### 5.5 Browser

- Sidebar: the hourglass (`.row-mark.checking`) while pending, then `.row-outcome[data-kind]` on
  the same corner — two ticks (complete, green), a list (partial, blue), a speech bubble
  (needs_input, "Waiting on you", amber), a triangle (blocked, red), an exclamation mark (error,
  faint). The mark's title is the word and the reason; Info adds the line and "Open: …". A closed
  folder counts a pending one like `tasks`.
- Header: `#sessionOutcome`, a tag beside `#sessionStatus`, "Checking…" or the word; a tap shows
  the reason and what is open.
- Home: a pending session is under **Background tasks**, then Finished/Ready; rows carry the
  hourglass or the verdict's mark (`.home-badge`), "Finished · Partly done: 2 open" in the meta
  line, and the reason and todos under the summary. `needs_input` and `blocked` are in
  **Needs you** too.
- The Status button is told the verdict (`statusPrompt`).
- The filing status is never changed by a verdict (DEVIATIONS.md, "a turn's end files nothing").

