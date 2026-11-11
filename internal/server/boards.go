package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/saschazesiger/SocratesAgent/internal/repos"
	"github.com/saschazesiger/SocratesAgent/internal/store"
)

// Boards: every project folder of the sidebar has notes of its own, a board
// of columns and cards (boards.js draws it). A card has a title, a subtitle
// and one of the folder's labels - a colour with a name, "Bug" or "Idea".
// The columns, the labels and the cards are all the folder's own.
//
// The board is one JSON document per folder, read and written whole with a
// revision (store/boards.go): a page saves the board it edited together
// with the revision it started from, and a save from an older one is
// refused with the current board, which the page then shows. Every save is a
// board frame on /api/events, so another page showing the folder's board
// reads it again.

// The colours a label can be. They are names, not colours: the page maps
// each to a token of the theme (--so-label-*), so a board reads the same in
// the dark and in the light.
var boardColors = map[string]bool{
	"blue": true, "orange": true, "teal": true, "purple": true,
	"red": true, "green": true, "yellow": true, "grey": true,
}

// How much a board may hold. Far above what anybody keeps as notes, and low
// enough that a board is always one small document.
const (
	maxBoardColumns  = 50
	maxBoardCards    = 2000
	maxBoardLabels   = 32
	maxBoardTitle    = 200
	maxBoardSubtitle = 4000
	maxBoardLabel    = 40
	maxBoardID       = 64
	maxBoardFolder   = 4096
	maxBoardBytes    = 2 << 20
)

type boardDoc struct {
	Labels  []boardLabel  `json:"labels"`
	Columns []boardColumn `json:"columns"`
}

type boardLabel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type boardColumn struct {
	ID    string      `json:"id"`
	Title string      `json:"title"`
	Cards []boardCard `json:"cards"`
}

type boardCard struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	// Label is the id of one of the board's labels, or empty.
	Label string `json:"label"`
}

// defaultBoard is what a folder without a board shows: three columns and
// the two labels the owner named, and one more, all of which can be renamed,
// recoloured or deleted.
func defaultBoard() boardDoc {
	return boardDoc{
		Labels: []boardLabel{
			{ID: "bug", Name: "Bug", Color: "red"},
			{ID: "idea", Name: "Idea", Color: "purple"},
			{ID: "feature", Name: "Feature", Color: "blue"},
		},
		Columns: []boardColumn{
			{ID: "todo", Title: "To do", Cards: []boardCard{}},
			{ID: "doing", Title: "In progress", Cards: []boardCard{}},
			{ID: "done", Title: "Done", Cards: []boardCard{}},
		},
	}
}

// boardFolder is the folder a request names, spelt as the sidebar spells it
// (folders.js dirOf): without trailing slashes, the root keeping its own.
// "More Chats" is not a project and has no board.
func boardFolder(raw string) (string, error) {
	folder := strings.TrimSpace(raw)
	if trimmed := strings.TrimRight(folder, "/"); trimmed != "" {
		folder = trimmed
	} else if folder != "" {
		folder = "/"
	}
	switch {
	case folder == "":
		return "", errors.New("name the folder whose board this is")
	case strings.HasPrefix(folder, "github:"):
		// A repository's board: github:<owner>/<name> (REPOS.md §5.3).
		if !repos.ValidName(strings.TrimPrefix(folder, "github:")) {
			return "", errors.New("a repository's board is named github:<owner>/<name>")
		}
		return folder, nil
	case strings.HasPrefix(folder, "#"):
		return "", errors.New("only a project folder has a board")
	case len(folder) > maxBoardFolder, !utf8.ValidString(folder), strings.ContainsRune(folder, 0):
		return "", errors.New("that is not a folder's path")
	}
	return folder, nil
}

// validID is an id the page made up for a column, a card or a label.
func validID(id string) bool {
	if id == "" || len(id) > maxBoardID {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// cleanText is a title or a name with its outer space taken off, refused when
// it is longer than max characters.
func cleanText(what, s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("the %s is not valid text", what)
	}
	if utf8.RuneCountInString(s) > max {
		return "", fmt.Errorf("a %s can be at most %d characters", what, max)
	}
	return s, nil
}

// checkBoard checks a board a page sent and returns it in the form it is
// stored in: text trimmed, no nil lists, and a card's label emptied when the
// label is not on the board (deleting a label leaves its cards plain).
func checkBoard(doc boardDoc) (boardDoc, error) {
	out := boardDoc{Labels: []boardLabel{}, Columns: []boardColumn{}}
	if len(doc.Labels) > maxBoardLabels {
		return out, fmt.Errorf("a board can have at most %d colours", maxBoardLabels)
	}
	if len(doc.Columns) > maxBoardColumns {
		return out, fmt.Errorf("a board can have at most %d columns", maxBoardColumns)
	}
	labels := map[string]bool{}
	for _, l := range doc.Labels {
		if !validID(l.ID) || labels[l.ID] {
			return out, errors.New("every colour needs an id of its own")
		}
		if !boardColors[l.Color] {
			return out, fmt.Errorf("%q is not one of the board's colours", l.Color)
		}
		name, err := cleanText("colour's name", l.Name, maxBoardLabel)
		if err != nil {
			return out, err
		}
		labels[l.ID] = true
		out.Labels = append(out.Labels, boardLabel{ID: l.ID, Name: name, Color: l.Color})
	}
	ids := map[string]bool{}
	cards := 0
	for _, c := range doc.Columns {
		if !validID(c.ID) || ids["col:"+c.ID] {
			return out, errors.New("every column needs an id of its own")
		}
		ids["col:"+c.ID] = true
		title, err := cleanText("column's name", c.Title, maxBoardTitle)
		if err != nil {
			return out, err
		}
		col := boardColumn{ID: c.ID, Title: title, Cards: []boardCard{}}
		for _, card := range c.Cards {
			cards++
			if cards > maxBoardCards {
				return out, fmt.Errorf("a board can have at most %d cards", maxBoardCards)
			}
			if !validID(card.ID) || ids["card:"+card.ID] {
				return out, errors.New("every card needs an id of its own")
			}
			ids["card:"+card.ID] = true
			ct, err := cleanText("card's title", card.Title, maxBoardTitle)
			if err != nil {
				return out, err
			}
			if ct == "" {
				return out, errors.New("a card needs a title")
			}
			sub := strings.TrimRight(card.Subtitle, " \t\r\n")
			sub = strings.TrimLeft(sub, "\r\n")
			if !utf8.ValidString(sub) || utf8.RuneCountInString(sub) > maxBoardSubtitle {
				return out, fmt.Errorf("a card's subtitle can be at most %d characters", maxBoardSubtitle)
			}
			label := card.Label
			if !labels[label] {
				label = ""
			}
			col.Cards = append(col.Cards, boardCard{ID: card.ID, Title: ct, Subtitle: sub, Label: label})
		}
		out.Columns = append(out.Columns, col)
	}
	return out, nil
}

// boardView is a board as the API answers it: the folder's, or the default
// one at revision zero when the folder has none yet.
func boardView(b store.Board) map[string]any {
	var doc any = defaultBoard()
	if len(b.Doc) > 0 {
		doc = b.Doc
	}
	return map[string]any{"folder": b.Folder, "rev": b.Rev, "updated_at": b.UpdatedAt, "board": doc}
}

// handleGetBoard answers a folder's board:
//
//	GET /api/boards?folder=/root/project  {"folder", "rev", "updated_at", "board"}
func (s *Server) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	folder, err := boardFolder(r.URL.Query().Get("folder"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := s.store.GetBoard(folder)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, boardView(b))
}

// handlePutBoard saves a folder's board:
//
//	PUT /api/boards  {"folder", "base_rev", "board"}  ->  {"folder", "rev", "updated_at", "board"}
//
// A save based on a revision that is no longer the current one is a 409
// with the current board and "conflict": true.
func (s *Server) handlePutBoard(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBoardBytes)
	var body struct {
		Folder  string   `json:"folder"`
		BaseRev int64    `json:"base_rev"`
		Board   boardDoc `json:"board"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	folder, err := boardFolder(body.Folder)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	doc, err := checkBoard(body.Board)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	saved, err := s.store.SaveBoard(folder, raw, body.BaseRev)
	if errors.Is(err, store.ErrConflict) {
		view := boardView(saved)
		view["conflict"] = true
		writeJSON(w, http.StatusConflict, view)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.events.publish(map[string]any{"t": "board", "folder": folder, "rev": saved.Rev})
	writeJSON(w, http.StatusOK, boardView(saved))
}
