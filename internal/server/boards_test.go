package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func boardEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newEnv(t)
	if res, _ := e.do(t, e.client, "POST", "/api/setup", `{"password":"a-good-password"}`); res.StatusCode != http.StatusOK {
		t.Fatalf("setup: %d", res.StatusCode)
	}
	return e
}

func putBoard(t *testing.T, e *testEnv, folder string, baseRev int64, board any) (*http.Response, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"folder": folder, "base_rev": baseRev, "board": board})
	return e.do(t, e.client, "PUT", "/api/boards", string(body))
}

func boardOf(t *testing.T, view map[string]any) boardDoc {
	t.Helper()
	raw, _ := json.Marshal(view["board"])
	var doc boardDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// A folder nobody has written notes for shows the default board at revision
// zero; a save is kept per folder, spelt as the sidebar spells it, and a
// save from a revision another page has moved past is refused with what is
// there now.
func TestBoardsArePerFolderAndRevisioned(t *testing.T) {
	e := boardEnv(t)
	res, view := e.do(t, e.client, "GET", "/api/boards?folder="+url.QueryEscape("/root/one"), "")
	if res.StatusCode != http.StatusOK || view["rev"].(float64) != 0 {
		t.Fatalf("fresh board: %d %v", res.StatusCode, view)
	}
	fresh := boardOf(t, view)
	if len(fresh.Columns) != 3 || len(fresh.Labels) == 0 || fresh.Labels[0].Name != "Bug" {
		t.Fatalf("default board: %+v", fresh)
	}

	board := boardDoc{
		Labels: []boardLabel{{ID: "bug", Name: "  Bug ", Color: "red"}, {ID: "idea", Name: "Idee", Color: "yellow"}},
		Columns: []boardColumn{
			{ID: "a", Title: "Backlog", Cards: []boardCard{
				{ID: "c1", Title: " Fix login ", Subtitle: "on Safari\n", Label: "bug"},
				{ID: "c2", Title: "Dark mode", Label: "gone"},
			}},
			{ID: "b", Title: "Doing"},
		},
	}
	res, saved := putBoard(t, e, "/root/one/", 0, board)
	if res.StatusCode != http.StatusOK || saved["rev"].(float64) != 1 || saved["folder"] != "/root/one" {
		t.Fatalf("save: %d %v", res.StatusCode, saved)
	}
	got := boardOf(t, saved)
	if got.Labels[0].Name != "Bug" || got.Columns[0].Cards[0].Title != "Fix login" ||
		got.Columns[0].Cards[0].Subtitle != "on Safari" || got.Columns[0].Cards[1].Label != "" ||
		got.Columns[1].Cards == nil {
		t.Fatalf("stored board: %+v", got)
	}

	_, again := e.do(t, e.client, "GET", "/api/boards?folder="+url.QueryEscape("/root/one"), "")
	if again["rev"].(float64) != 1 || boardOf(t, again).Columns[0].Title != "Backlog" {
		t.Fatalf("read back: %v", again)
	}
	_, other := e.do(t, e.client, "GET", "/api/boards?folder="+url.QueryEscape("/root/two"), "")
	if other["rev"].(float64) != 0 {
		t.Fatalf("another folder has the first one's board: %v", other)
	}

	res, conflict := putBoard(t, e, "/root/one", 0, defaultBoard())
	if res.StatusCode != http.StatusConflict || conflict["conflict"] != true || conflict["rev"].(float64) != 1 ||
		boardOf(t, conflict).Columns[0].Title != "Backlog" {
		t.Fatalf("stale save: %d %v", res.StatusCode, conflict)
	}
	if res, next := putBoard(t, e, "/root/one", 1, defaultBoard()); res.StatusCode != http.StatusOK || next["rev"].(float64) != 2 {
		t.Fatalf("save on the current revision: %d %v", res.StatusCode, next)
	}
}

// What a board may not hold is refused with a sentence, and nothing is
// stored; boards are behind the sign-in.
func TestBoardsRefuseWhatTheyCannotHold(t *testing.T) {
	e := boardEnv(t)
	ok := func() boardDoc { return defaultBoard() }
	cases := map[string]struct {
		folder string
		board  boardDoc
	}{
		"no folder":    {"", ok()},
		"more chats":   {"#more", ok()},
		"bad colour":   {"/p", boardDoc{Labels: []boardLabel{{ID: "x", Name: "X", Color: "#ff0000"}}}},
		"bad id":       {"/p", boardDoc{Columns: []boardColumn{{ID: "a b", Title: "x"}}}},
		"same id":      {"/p", boardDoc{Columns: []boardColumn{{ID: "a"}, {ID: "a"}}}},
		"untitled":     {"/p", boardDoc{Columns: []boardColumn{{ID: "a", Cards: []boardCard{{ID: "c", Title: "  "}}}}}},
		"long title":   {"/p", boardDoc{Columns: []boardColumn{{ID: "a", Title: strings.Repeat("x", maxBoardTitle+1)}}}},
		"same card id": {"/p", boardDoc{Columns: []boardColumn{{ID: "a", Cards: []boardCard{{ID: "c", Title: "1"}}}, {ID: "b", Cards: []boardCard{{ID: "c", Title: "2"}}}}}},
	}
	for name, c := range cases {
		res, body := putBoard(t, e, c.folder, 0, c.board)
		if res.StatusCode != http.StatusBadRequest || body["error"] == "" {
			t.Errorf("%s: %d %v", name, res.StatusCode, body)
		}
	}
	if _, view := e.do(t, e.client, "GET", "/api/boards?folder=%2Fp", ""); view["rev"].(float64) != 0 {
		t.Fatalf("a refused board was stored: %v", view)
	}
	if res, _ := e.do(t, e.anon, "GET", "/api/boards?folder=%2Fp", ""); res.StatusCode == http.StatusOK {
		t.Fatal("a board was read without signing in")
	}
}
