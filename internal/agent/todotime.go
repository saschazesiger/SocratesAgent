package agent

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A task list says how long each task should take and how long it has taken
// (HARNESS-POLICY.md, "Task lists"). The agent's only part is a few
// characters: it ends a task's title with its estimate, "Run the tests
// (~3m)". The clock is the recorder's: each time a todo part is written it
// takes the estimate out of the title and stamps every task with when it was
// added, began and ended, carried over from the list as it last stood. The
// stamps are in the journal with the part, so a restart keeps them.
//
// The fields a task gains, next to its own:
//
//	estimate_s  the estimate, in seconds (0: none)
//	added_at    when the task first showed up, unix ms
//	started_at  when it went in progress (unix ms, 0: not yet)
//	done_at     when it was completed (unix ms, 0: not yet)
//
// A task completed without ever being in progress began when the last task
// before it was done (one of the list as it stood, or one above it in this
// list), or when it was added if that was later.

// TodoGuidance is the instruction every chat gets about its task list. tool is how the harness's task tool is named
// to the model, with any hint on calling it cheaply.
func TodoGuidance(tool string) string {
	return "Task list: for work of three or more steps, or that will take more than a few minutes, first write " +
		"an exact task list with " + tool + ": one entry per concrete step, in order. End every entry's title with " +
		"your estimate of how long that step takes: (~30s), (~4m) or (~1h15m), e.g. \"Run the test suite (~3m)\". " +
		"Keep it current while you work: mark an entry in progress when you begin it and completed as soon as it " +
		"is done, add steps you discover, and correct an estimate that turns out clearly wrong. Socrates times " +
		"every entry and shows the time taken against your estimate; never track time yourself. Keep it cheap: " +
		"no list for a question or a quick single change, short titles, and list updates sent in the same " +
		"message as the work they belong to (complete one entry, start the next and make that step's first " +
		"call together)."
}

// todoEstimate is an estimate at the end of a title: "(~5m)", "[~1h 30m]",
// "(est. 90s)", "~45 min".
var todoEstimate = regexp.MustCompile(`(?i)\s*(?:[(\[]\s*(?:~|≈|ca\.?|est\.?|estimate:?)?\s*((?:\d+(?:[.,]\d+)?\s*[a-z]+\s*)+)[)\]]|\s~\s*((?:\d+(?:[.,]\d+)?\s*[a-z]+\s*)+))\s*$`)

var todoAmount = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*([a-z]+)`)

// splitEstimate takes the estimate off the end of a title: the title
// without it and the estimate in seconds, or the title as it is and 0.
func splitEstimate(title string) (string, int64) {
	m := todoEstimate.FindStringSubmatchIndex(title)
	if m == nil {
		return title, 0
	}
	amounts := ""
	for g := 2; g <= 4; g += 2 {
		if m[g] >= 0 {
			amounts = title[m[g]:m[g+1]]
		}
	}
	var total float64
	for _, a := range todoAmount.FindAllStringSubmatch(amounts, -1) {
		n, err := strconv.ParseFloat(strings.Replace(a[1], ",", ".", 1), 64)
		if err != nil {
			return title, 0
		}
		switch unit := strings.ToLower(a[2]); {
		case unit == "h" || strings.HasPrefix(unit, "hr") || strings.HasPrefix(unit, "hour"):
			total += n * 3600
		case unit == "m" || strings.HasPrefix(unit, "min"):
			total += n * 60
		case unit == "s" || strings.HasPrefix(unit, "sec"):
			total += n
		default:
			return title, 0
		}
	}
	if total <= 0 {
		return title, 0
	}
	return strings.TrimSpace(title[:m[0]]), int64(math.Round(total))
}

// The lists the adapters write (AGENT-HOST.md): Claude's items, OpenCode's
// todos, Codex's steps, each task's title under a name of its own.
var (
	todoLists  = []string{"items", "todos", "steps"}
	todoTitles = []string{"subject", "content", "step", "text"}
	todoActive = []string{"active_form", "activeForm"}
)

// todoTask is a task as the clock knows it.
type todoTask struct {
	key                         string
	estimate                    int64
	added, started, done        int64
	fields                      map[string]json.RawMessage
	title, titleField, statusOf string
}

// readTodos is the tasks of a todo part's content, and the content's other
// fields with the list's name; ok is false for content that holds no list.
func readTodos(content json.RawMessage) (top map[string]json.RawMessage, list string, tasks []*todoTask, ok bool) {
	if json.Unmarshal(content, &top) != nil || top == nil {
		return nil, "", nil, false
	}
	for _, name := range todoLists {
		raw, there := top[name]
		if !there {
			continue
		}
		var items []map[string]json.RawMessage
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&items) != nil {
			return nil, "", nil, false
		}
		for i, f := range items {
			if f == nil {
				f = map[string]json.RawMessage{}
			}
			t := &todoTask{fields: f}
			for _, k := range todoTitles {
				if s, ok := jsonString(f[k]); ok {
					t.title, t.titleField = s, k
					break
				}
			}
			t.statusOf, _ = jsonString(f["status"])
			t.estimate = jsonInt(f["estimate_s"])
			t.added, t.started, t.done = jsonInt(f["added_at"]), jsonInt(f["started_at"]), jsonInt(f["done_at"])
			if id, ok := jsonString(f["id"]); ok && id != "" {
				t.key = "id:" + id
			} else if t.title != "" {
				t.key = "title:" + t.title
			} else {
				t.key = "at:" + strconv.Itoa(i)
			}
			tasks = append(tasks, t)
		}
		return top, name, tasks, true
	}
	return nil, "", nil, false
}

func jsonString(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func jsonInt(raw json.RawMessage) int64 {
	var n float64
	if len(raw) == 0 || json.Unmarshal(raw, &n) != nil {
		return 0
	}
	return int64(n)
}

// todoStatus is a task's state in three words, whatever the harness calls it.
func todoStatus(s string) string {
	switch s {
	case "completed", "done":
		return "done"
	case "in_progress", "inProgress", "running":
		return "running"
	}
	return "pending"
}

// TimeTodos is content, a todo part's list as an adapter wrote it, with the
// estimates out of the titles and every task stamped, carried over from prev
// (the list as it last stood, already timed; nil for none) at now. Content
// that is not a list it knows comes back as it is.
func TimeTodos(content, prev json.RawMessage, now time.Time) json.RawMessage {
	top, list, tasks, ok := readTodos(content)
	if !ok {
		return content
	}
	at := now.UnixMilli()
	before := map[string]*todoTask{}
	var lastDone int64
	if len(prev) > 0 {
		if _, _, old, ok := readTodos(prev); ok {
			for _, t := range old {
				before[t.key] = t
				lastDone = max(lastDone, t.done)
			}
		}
	}
	items := make([]map[string]json.RawMessage, len(tasks))
	for i, t := range tasks {
		title, estimate := splitEstimate(t.title)
		if estimate > 0 {
			t.estimate = estimate
			t.fields[t.titleField], _ = json.Marshal(title)
			if t.key == "title:"+t.title {
				t.key = "title:" + title
			}
		}
		for _, k := range todoActive {
			if s, ok := jsonString(t.fields[k]); ok {
				if bare, n := splitEstimate(s); n > 0 {
					t.fields[k], _ = json.Marshal(bare)
				}
			}
		}
		if old := before[t.key]; old != nil {
			t.added = old.added
			t.started, t.done = old.started, old.done
			if t.estimate == 0 {
				t.estimate = old.estimate
			}
		}
		if t.added == 0 {
			t.added = at
		}
		switch todoStatus(t.statusOf) {
		case "running":
			if t.started == 0 {
				t.started = at
			}
			t.done = 0
		case "done":
			if t.done == 0 {
				t.done = at
			}
			if t.started == 0 {
				t.started = max(t.added, lastDone)
			}
		default:
			t.started, t.done = 0, 0
		}
		lastDone = max(lastDone, t.done)
		set := func(name string, v int64) {
			if v > 0 {
				t.fields[name], _ = json.Marshal(v)
			} else {
				delete(t.fields, name)
			}
		}
		set("estimate_s", t.estimate)
		set("added_at", t.added)
		set("started_at", t.started)
		set("done_at", t.done)
		items[i] = t.fields
	}
	top[list], _ = json.Marshal(items)
	out, err := json.Marshal(top)
	if err != nil {
		return content
	}
	return out
}
