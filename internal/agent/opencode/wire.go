//go:build !windows

package opencode

import (
	"encoding/json"
	"strings"
)

// The shapes of OpenCode 1.19.4's classic API, as far as the adapter reads
// them (research/05 §2, the OpenAPI spec served at /doc). Fields it does not
// use are left out.

// ocEvent is one frame of /event.
type ocEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

type ocTokens struct {
	Total     int64 `json:"total"`
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

// context is what the turn's prompt and answer take of the context window,
// the way OpenCode's own TUI counts it.
func (t ocTokens) context() int64 {
	return t.Input + t.Output + t.Reasoning + t.Cache.Read + t.Cache.Write
}

type ocModelRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// ocError is a NamedError: {name, data:{message, statusCode, …}}.
type ocError struct {
	Name string `json:"name"`
	Data struct {
		Message      string `json:"message"`
		StatusCode   int    `json:"statusCode"`
		ResponseBody string `json:"responseBody"`
		ProviderID   string `json:"providerID"`
	} `json:"data"`
}

func (e *ocError) text() string {
	if e == nil {
		return ""
	}
	if m := strings.TrimSpace(e.Data.Message); m != "" {
		return m
	}
	return e.Name
}

// Error names that matter.
const (
	errAborted      = "MessageAbortedError"
	errProviderAuth = "ProviderAuthError"
	errUnknown      = "UnknownError"
)

type ocMessage struct {
	ID         string      `json:"id"`
	SessionID  string      `json:"sessionID"`
	Role       string      `json:"role"`
	ParentID   string      `json:"parentID"`
	ModelID    string      `json:"modelID"`
	ProviderID string      `json:"providerID"`
	Model      *ocModelRef `json:"model"`
	Agent      string      `json:"agent"`
	Mode       string      `json:"mode"`
	Time       struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Error  *ocError `json:"error"`
	Cost   float64  `json:"cost"`
	Tokens ocTokens `json:"tokens"`
	Finish string   `json:"finish"`
	// Summary is a user message's {title, diffs}, a boolean on an
	// assistant message (summaryDiffs).
	Summary json.RawMessage `json:"summary"`
	// Path is an assistant message's folder and worktree.
	Path *ocPath `json:"path"`
}

// ocPath is where an assistant message worked: {cwd, root} (the worktree;
// verified against 1.19.5).
type ocPath struct {
	Cwd  string `json:"cwd"`
	Root string `json:"root"`
}

func (m *ocMessage) model() string {
	switch {
	case m.ProviderID != "" && m.ModelID != "":
		return m.ProviderID + "/" + m.ModelID
	case m.Model != nil && m.Model.ProviderID != "":
		return m.Model.ProviderID + "/" + m.Model.ModelID
	}
	return ""
}

type ocToolState struct {
	Status   string          `json:"status"`
	Input    json.RawMessage `json:"input"`
	Output   string          `json:"output"`
	Title    string          `json:"title"`
	Metadata json.RawMessage `json:"metadata"`
	Error    string          `json:"error"`
	Time     struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	} `json:"time"`
}

// Tool states.
const (
	toolPending   = "pending"
	toolRunning   = "running"
	toolCompleted = "completed"
	toolError     = "error"
)

type ocPart struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID"`
	Type      string `json:"type"`
	Text      string `json:"text"`
	Synthetic bool   `json:"synthetic"`
	Ignored   bool   `json:"ignored"`
	Time      *struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	} `json:"time"`
	Tool     string       `json:"tool"`
	CallID   string       `json:"callID"`
	State    *ocToolState `json:"state"`
	Mime     string       `json:"mime"`
	Filename string       `json:"filename"`
	URL      string       `json:"url"`
	Tokens   *ocTokens    `json:"tokens"`
	Cost     float64      `json:"cost"`
	Reason   string       `json:"reason"`
	// Files are a patch part's: the absolute paths a step changed.
	Files []string `json:"files"`
}

// Part types.
const (
	partText       = "text"
	partReasoning  = "reasoning"
	partTool       = "tool"
	partFile       = "file"
	partStepFinish = "step-finish"
	partPatch      = "patch"
)

type ocMessageWithParts struct {
	Info  ocMessage `json:"info"`
	Parts []ocPart  `json:"parts"`
}

type ocStatus struct {
	Type    string `json:"type"`
	Attempt int    `json:"attempt"`
	Message string `json:"message"`
	Next    int64  `json:"next"`
	Action  *struct {
		Reason   string `json:"reason"`
		Provider string `json:"provider"`
		Title    string `json:"title"`
		Message  string `json:"message"`
		Link     string `json:"link"`
	} `json:"action"`
}

// Session status types.
const (
	statusBusy  = "busy"
	statusIdle  = "idle"
	statusRetry = "retry"
)

type ocSession struct {
	ID        string `json:"id"`
	ParentID  string `json:"parentID"`
	Directory string `json:"directory"`
	Title     string `json:"title"`
}

// ocPermission is permission.asked's payload.
type ocPermission struct {
	ID         string   `json:"id"`
	SessionID  string   `json:"sessionID"`
	Permission string   `json:"permission"`
	Patterns   []string `json:"patterns"`
}

// Question is question.asked's payload: OpenCode's own question tool, which
// the config denies; one that still arrives is asked like ask_user.
type Question struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionID"`
	Questions json.RawMessage `json:"questions"`
	Tool      *struct {
		MessageID string `json:"messageID"`
		CallID    string `json:"callID"`
	} `json:"tool"`
}

// ocCommand is one of OpenCode's commands, as GET /command lists them.
type ocCommand struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Source      string   `json:"source,omitempty"`
	Hints       []string `json:"hints,omitempty"`
}

// ocAgent is one entry of GET /agent.
type ocAgent struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Mode is primary, subagent or all.
	Mode   string `json:"mode,omitempty"`
	Hidden bool   `json:"hidden,omitempty"`
}

// ocProviders is GET /config/providers, for the models' context windows.
type ocProviders struct {
	Providers []struct {
		ID     string `json:"id"`
		Models map[string]struct {
			Limit struct {
				Context int64 `json:"context"`
			} `json:"limit"`
		} `json:"models"`
	} `json:"providers"`
}

// ocFilePart is a file part of a prompt: an image as a data URL, any other
// file as a file:// URL, which OpenCode expands into a read.
type ocFilePart struct {
	Type     string `json:"type"`
	Mime     string `json:"mime"`
	Filename string `json:"filename,omitempty"`
	URL      string `json:"url"`
}

type ocTextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
