// Package artifacts is Socrates Artifacts, an MCP server built into Socrates
// (docs/design/ARTIFACTS.md): the tools a chat's agent makes pages with
// that show something better than chat text can, a diagram, a chart, a
// table, an explainer. The pages are the chat's (internal/artifacts) and
// its page shows them beside the chat, live.
//
// It needs no account and no configuration, and like every built-in server
// it is off in a chat until Admin or the chat switches it on.
package artifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/artifacts"
	"github.com/saschazesiger/SocratesAgent/internal/mcpserver"
)

// Name is the server's name in a chat.
const Name = "socrates-artifacts"

// The tools, bare.
const (
	List   = "artifacts_list"
	Create = "artifact_create"
	Update = "artifact_update"
	Delete = "artifact_delete"
	Read   = "artifact_read"
)

// Deps is what the server needs of the Socrates server.
type Deps struct {
	// Store keeps every chat's artifacts; nil while there is none (the
	// names of the built-in servers are read before anything runs).
	Store *artifacts.Store
}

// Server is Socrates Artifacts.
type Server struct{ deps Deps }

// New is the server.
func New(d Deps) *Server { return &Server{deps: d} }

func (s *Server) Name() string  { return Name }
func (s *Server) Title() string { return "Socrates Artifacts" }
func (s *Server) Description() string {
	return "Lets the agent make pages that explain with diagrams, charts and tables, shown beside the chat and updated live."
}

func (s *Server) Instructions() string {
	return "Socrates Artifacts: pages you make for the person, shown beside this chat and updated live. Make one when a " +
		"picture explains better than words: an architecture or flow as a diagram, numbers as a chart, a comparison as " +
		"a table, a timeline, a small interactive explainer. Not for a few lines of text, which belong in the chat.\n" +
		"- Make: " + Create + " with a title, a one-line description and the content. format markdown (the simplest): " +
		"headings, lists, tables, and ```mermaid code blocks drawn as diagrams (flowchart, sequenceDiagram, classDiagram, " +
		"stateDiagram-v2, erDiagram, gantt, pie, mindmap, timeline). format html (the default) for anything richer: write " +
		"a fragment (no <html>, <head> or <body>) and the page kit gives it Socrates' look, or a whole document to style " +
		"it all yourself. In a fragment: <pre class=\"mermaid\"> for diagrams, inline <svg> for drawings of your own, " +
		"<script> runs (Chart.js: <script src=\"https://cdn.jsdelivr.net/npm/chart.js\"></script>, other libraries from " +
		"cdn.jsdelivr.net too), and the kit's classes: grid (grid-2, grid-3) for cards side by side, card with " +
		"card-title, callout info|ok|warn|error, badge info|ok|warn|error, stat with stat-value and stat-label, steps on " +
		"an <ol>, muted, lead, small. Colours as CSS variables, also in SVG styles: --text --muted --line --surface --bg " +
		"--accent --ok --warn --error and the series --c1 to --c6. Pictures and data the page uses (a PNG you rendered, " +
		"a CSV) go in files: paths absolute or relative to the chat's folder, named relative in the page (<img " +
		"src=\"chart.png\">, fetch('data.json')). Plain file names only.\n" +
		"- Change: " + Update + " rather than a new artifact for the same subject. edits [{old, new}] replace exact text " +
		"of the latest version (each old found once, or all: true), content replaces the whole page; either makes a " +
		"new version, with note saying what changed in a few words. files attaches (a name there already is replaced), " +
		"remove_files takes files away. " + Delete + " deletes one that is no longer wanted.\n" +
		"- Read: " + List + " for every artifact, " + Read + " for one's content (to edit it exactly) and files.\n" +
		"- The page runs in a sandbox: it cannot reach Socrates, the chat or the person's files, only what it carries, " +
		"its files and the internet. Keep a page readable on a phone (it may be 360px wide). Say in the chat what you made, " +
		"by its title; the person may ask for changes there. Never put a secret into an artifact."
}

func obj(props map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

var (
	artifactArg = str("The artifact's id (A3) or its exact title.")
	formatArg   = map[string]any{"type": "string", "enum": []string{artifacts.HTML, artifacts.Markdown},
		"description": "html: a fragment in the page kit, or a whole document. markdown: Markdown with ```mermaid diagrams."}
	filesArg = strs("Files the page uses: paths absolute or relative to the chat's folder, each with a plain name (chart.png) the page names it by.")
)

func (s *Server) Tools() []mcpserver.Tool {
	return []mcpserver.Tool{
		{Name: List, Title: "List the artifacts", Description: "Every artifact of this chat: id, title, description, " +
			"format, latest version and its files.", Schema: obj(map[string]any{})},
		{Name: Create, Title: "Make an artifact", Description: "Makes a page shown beside the chat, and answers its id.",
			Schema: obj(map[string]any{
				"title":       str("What it shows, in a few words."),
				"description": str("One line on what it is for."),
				"format":      formatArg,
				"content":     str("The page: an HTML fragment or document, or Markdown."),
				"files":       filesArg,
			}, "title", "content")},
		{Name: Update, Title: "Change an artifact", Description: "Changes the page (edits or a whole new content, a new " +
			"version), its title or description, or its files. The person's view follows at once.",
			Schema: obj(map[string]any{
				"artifact": artifactArg,
				"content":  str("The whole new page, replacing the latest version."),
				"edits": map[string]any{"type": "array", "maxItems": artifacts.MaxEdits,
					"description": "Exact replacements in the latest version, in order.",
					"items": obj(map[string]any{
						"old": str("Text of the page as it is, found exactly once (unless all)."),
						"new": str("What it becomes."),
						"all": map[string]any{"type": "boolean", "description": "Replace every time old is found."},
					}, "old", "new")},
				"format":       formatArg,
				"note":         str("What changed, in a few words; shown with the version."),
				"title":        str("A new title."),
				"description":  str("A new description."),
				"files":        filesArg,
				"remove_files": strs("Names of files to take away."),
			}, "artifact")},
		{Name: Delete, Title: "Delete an artifact", Description: "Deletes the artifact with every version and file of it.",
			Schema: obj(map[string]any{"artifact": artifactArg}, "artifact")},
		{Name: Read, Title: "Read an artifact", Description: "An artifact's content (the latest version, or the one " +
			"named), its versions and its files with absolute paths.",
			Schema: obj(map[string]any{"artifact": artifactArg,
				"version": map[string]any{"type": "integer", "minimum": 1, "description": "This version; the latest when left out."}}, "artifact")},
	}
}

func (s *Server) Call(ctx context.Context, sess mcpserver.Session, tool string, args json.RawMessage) mcpserver.Result {
	if s.deps.Store == nil {
		return mcpserver.Error("Socrates Artifacts is not running.")
	}
	if sess.ID == "" {
		return mcpserver.Error("Socrates Artifacts works for a chat.")
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	text, err := s.do(sess, tool, args)
	if err != nil {
		return mcpserver.Error(err.Error())
	}
	return mcpserver.Text(text)
}

func (s *Server) do(sess mcpserver.Session, tool string, args json.RawMessage) (string, error) {
	st := s.deps.Store
	chat := sess.ID
	switch tool {
	case List:
		if err := decode(args, &struct{}{}); err != nil {
			return "", err
		}
		list, err := st.List(chat)
		if err != nil {
			return "", err
		}
		return artifacts.ListText(list), nil
	case Create:
		var a struct {
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Format      string   `json:"format"`
			Content     string   `json:"content"`
			Files       []string `json:"files"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		files, err := paths(sess, a.Files)
		if err != nil {
			return "", err
		}
		art, err := st.Create(chat, artifacts.Fields{Title: a.Title, Description: a.Description, Format: a.Format,
			Content: a.Content, Files: files})
		if err != nil {
			return "", err
		}
		return artifacts.CreatedText(art), nil
	case Update:
		var a struct {
			Artifact    string           `json:"artifact"`
			Content     *string          `json:"content"`
			Edits       []artifacts.Edit `json:"edits"`
			Format      *string          `json:"format"`
			Note        string           `json:"note"`
			Title       *string          `json:"title"`
			Description *string          `json:"description"`
			Files       []string         `json:"files"`
			RemoveFiles []string         `json:"remove_files"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		files, err := paths(sess, a.Files)
		if err != nil {
			return "", err
		}
		art, ch, err := st.Update(chat, a.Artifact, artifacts.Patch{Title: a.Title, Description: a.Description,
			Format: a.Format, Content: a.Content, Edits: a.Edits, Note: a.Note, Files: files, RemoveFiles: a.RemoveFiles})
		if err != nil {
			return "", err
		}
		return artifacts.UpdatedText(art, ch), nil
	case Delete:
		var a struct {
			Artifact string `json:"artifact"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		gone, err := st.Delete(chat, a.Artifact)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Deleted %s “%s” with its versions and files.", gone.ID, gone.Title), nil
	case Read:
		var a struct {
			Artifact string `json:"artifact"`
			Version  int    `json:"version"`
		}
		if err := decode(args, &a); err != nil {
			return "", err
		}
		art, v, content, err := st.Content(chat, a.Artifact, a.Version)
		if err != nil {
			return "", err
		}
		return artifacts.ReadText(art, v, content, st.FilesDir(chat, art.ID)), nil
	}
	return "", fmt.Errorf("Socrates Artifacts has no tool %s", tool)
}

// paths is the files an agent named, absolute: relative ones are the chat
// folder's.
func paths(sess mcpserver.Session, in []string) ([]string, error) {
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			if sess.Workdir == "" {
				return nil, fmt.Errorf("%s: give an absolute path, the chat has no folder", p)
			}
			p = filepath.Join(sess.Workdir, p)
		}
		out = append(out, filepath.Clean(p))
	}
	return out, nil
}

// decode reads a tool's arguments, refusing a field the tool does not have.
func decode(args json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("the arguments could not be read: %s", strings.TrimPrefix(err.Error(), "json: "))
	}
	return nil
}
