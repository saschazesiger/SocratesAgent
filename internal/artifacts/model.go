// Package artifacts keeps every chat's artifacts (docs/design/ARTIFACTS.md):
// pages a chat's agent makes to show something better than chat text can, a
// diagram, a chart, a table, written as an HTML fragment or as Markdown. It
// is the model, the store per chat (artifacts.json, the content of every
// version and the files a page uses), the edits, the page a version is
// served as, and the Markdown answers the socrates-artifacts MCP server
// gives. The HTTP API and the server share every operation here.
package artifacts

import "time"

// The limits of a chat's artifacts.
const (
	MaxArtifacts   = 100
	MaxContent     = 2 << 20
	MaxVersions    = 20
	MaxFiles       = 30
	MaxFileSize    = 20 << 20
	MaxChatSize    = 200 << 20
	MaxTitle       = 200
	MaxDescription = 500
	MaxNote        = 300
	MaxFileName    = 100
	MaxEdits       = 100
)

// The formats of a version's content.
const (
	// HTML is an HTML fragment, wrapped in the page kit, or a whole
	// document, served as it is.
	HTML = "html"
	// Markdown is Markdown, rendered by the page kit, its mermaid code
	// blocks drawn as diagrams.
	Markdown = "markdown"
)

// What a change did, as OnChange is told.
const (
	Created = "created"
	Updated = "updated"
	Deleted = "deleted"
)

// Artifact is one artifact of a chat.
type Artifact struct {
	// ID is A1, A2 … per chat, never given twice.
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// Format is the latest version's.
	Format string `json:"format"`
	// Key is random, made once: the page's address carries it, and a file
	// of the page is served to whoever has it.
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Versions is the versions kept, oldest first; the last is the latest.
	Versions []Version `json:"versions"`
	// Files is the files the page uses, by name.
	Files []File `json:"files,omitempty"`
}

// Version is one version of an artifact's content.
type Version struct {
	N      int       `json:"n"`
	At     time.Time `json:"at"`
	Size   int64     `json:"size"`
	Format string    `json:"format"`
	// Note says what changed, in the agent's words.
	Note string `json:"note,omitempty"`
}

// File is a file a page uses, named relative in it.
type File struct {
	Name string    `json:"name"`
	Size int64     `json:"size"`
	At   time.Time `json:"at"`
}

// Latest is the artifact's latest version.
func (a Artifact) Latest() Version {
	if len(a.Versions) == 0 {
		return Version{}
	}
	return a.Versions[len(a.Versions)-1]
}

// Version is the version n of the artifact, false when it is not kept.
func (a Artifact) Version(n int) (Version, bool) {
	for _, v := range a.Versions {
		if v.N == n {
			return v, true
		}
	}
	return Version{}, false
}

// Edit is an exact replacement in the latest content: Old must be found
// once, or with All every time it is.
type Edit struct {
	Old string `json:"old"`
	New string `json:"new"`
	All bool   `json:"all,omitempty"`
}

// Fields are what an artifact is created with. Files are absolute paths of
// files to attach.
type Fields struct {
	Title       string
	Description string
	Format      string
	Content     string
	Files       []string
}

// Patch is a change of an artifact; a nil field stays. Content and Edits
// make a new version, with Note; Files attaches (replacing a file of the
// same name), RemoveFiles takes files away by name.
type Patch struct {
	Title       *string
	Description *string
	Format      *string
	Content     *string
	Edits       []Edit
	Note        string
	Files       []string
	RemoveFiles []string
}

// Change is what an update did, for its answer.
type Change struct {
	// Version is the new version's number, 0 for none.
	Version  int
	Attached []string
	Removed  []string
	// Fields is the fields changed besides the content: title,
	// description.
	Fields []string
}

// ViewArtifact is an artifact as the chat's page reads it.
type ViewArtifact struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Format      string    `json:"format"`
	Version     int       `json:"version"`
	Versions    []Version `json:"versions"`
	Files       []File    `json:"files"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	URL         string    `json:"url"`
}

// View is an artifact as the chat's page reads it, its page's address made
// by url.
func View(a Artifact, url string) ViewArtifact {
	versions := a.Versions
	if versions == nil {
		versions = []Version{}
	}
	files := a.Files
	if files == nil {
		files = []File{}
	}
	return ViewArtifact{ID: a.ID, Title: a.Title, Description: a.Description, Format: a.Format,
		Version: a.Latest().N, Versions: versions, Files: files, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, URL: url}
}
