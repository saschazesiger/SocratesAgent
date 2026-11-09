# Notes per project folder: the board

Asked for by the owner on 2026-11-09: beside the **+** of every project folder in the sidebar, a
**notes** mark that opens a page with a Kanban board. Columns are free to add, name, move and
delete; a card has a **title**, a **subtitle** and a **colour**, and every colour has a **name**
("Bug", "Idea"). Columns, colours and cards belong to the **folder**. The board is kept on the
server and in the backup, so it is never lost.

## 1. Where it is

- The mark (`.so-folder__board`, DS glyph `fileText`) sits right after the folder's + in a project
  folder's head (session.js `boardButtonHtml`). "More Chats" (sessions in directories of
  Socrates' own) is no project and has none. It is pressed while its folder's board is on the
  stage.
- The board is a page on the stage like Home (`#board`, boards.js `mountBoard`): nothing is
  attached while it is shown. The address is `#board=<folder path, URI-encoded>`, so Back leaves it,
  a reload comes back to it and a link opens it. Home, a session or another folder's mark takes
  the stage back.
- The folder is the sidebar's folder key (folders.js `dirOf`): the working directory without
  trailing slashes. A folder whose sessions were all deleted keeps its board; its address still
  opens it.

## 2. What it holds

One JSON document per folder:

```json
{
  "labels":  [{ "id": "bug", "name": "Bug", "color": "red" }],
  "columns": [{ "id": "todo", "title": "To do",
                "cards": [{ "id": "card-…", "title": "…", "subtitle": "…", "label": "bug" }] }]
}
```

- Colours are names, not values: `blue teal green yellow orange red purple grey`. The theme maps
  each to a DS role or chart colour (`--so-label-*` in theme.css, `yellow` is DS's warning amber
  and called "Amber" on the page), so a board follows the light and the dark. A card shows its
  colour as an edge and as a tag with the colour's name: never colour alone.
- A folder without a board shows the default one at revision 0, written only when something is
  changed: To do, In progress, Done; Bug (red), Idea (purple), Feature (blue).
- Limits (server boards.go): 50 columns, 2000 cards, 32 colours, a title 200 characters, a
  subtitle 4000, a colour's name 40, ids `[A-Za-z0-9_-]{1,64}` and unique. A card needs a title. A
  card whose colour is deleted keeps no colour.

## 3. Server

- `GET /api/boards?folder=<path>` → `{folder, rev, updated_at, board}`.
- `PUT /api/boards` `{folder, base_rev, board}` → the same, with the board as stored (text
  trimmed). A `base_rev` that is not the current revision is a **409** with the current board and
  `"conflict": true`; a board the rules refuse is a 400 with a sentence.
- Stored in the database's `kv` table under `board:<folder>` (store/boards.go), the board with its
  revision and time: always read and written whole. Being in `socrates.db`, it is in every backup
  of the data directory to S3 and comes back with a restore.
- Every save publishes `{"t":"board","folder","rev"}` on `/api/events`; a page showing that folder's
  board, with nothing of its own unsaved, reads it again.

## 4. The page

- Every change is drawn at once and saved behind it, one save at a time; a change made during a
  save goes with the next. Until the server has a change it is also kept in `localStorage`
  (`socrates.board.<folder>`, with the revision it was made on): a tab closed offline sends it the
  next time the board is opened, if the server is still at that revision. A network failure
  retries with a growing pause (and when the browser is online again); the head says "Not saved
  yet" meanwhile.
- A save another device beat is not forced through: the page shows the server's board and says
  so in a snackbar.
- Cards are dragged with the pointer: a mouse once it moved 6 px, a finger after holding still
  for 350 ms (before that a swipe scrolls). The card stays in its list as the gap, a copy follows
  the pointer, the others glide (`DS.motion.flip`), and near the edges the row of columns and the
  column scroll. Escape cancels. The card's dialog moves it too (its Column field), which is the
  way for a keyboard.
- Dialogs: a card (title, subtitle, colour chips, column; Delete), a column (name, position;
  Delete, asked first when it holds cards), the colours (colour and name per row, add, delete),
  and a new column's name. On a phone the columns snap one by one and the page itself never
  scrolls sideways.
- Tested by `node e2e/run.mjs boards` and `go test ./internal/server -run TestBoards`.
