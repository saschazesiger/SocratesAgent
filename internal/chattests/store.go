package chattests

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// fileVersion is the version of tests.json this build writes and reads.
const fileVersion = 1

// Store keeps every chat's tests under dir(chat)/tests/.
type Store struct {
	dir func(chat string) string

	mu       sync.Mutex
	locks    map[string]*sync.Mutex
	onChange func(chat string)

	// now is the clock, replaced by the tests.
	now func() time.Time
}

// New makes a store whose chats keep their tests in dir(chat)/tests/.
func New(dir func(chat string) string) *Store {
	return &Store{dir: dir, locks: map[string]*sync.Mutex{}, now: time.Now}
}

// SetOnChange sets what is called after every change that was saved, with
// the chat's id. It is called with no lock held, so it may read the store.
func (s *Store) SetOnChange(f func(chat string)) {
	s.mu.Lock()
	s.onChange = f
	s.mu.Unlock()
}

// doc is tests.json.
type doc struct {
	Version int    `json:"version"`
	Next    int    `json:"next"`
	Rev     int64  `json:"rev"`
	Tests   []Test `json:"tests"`
}

// root is the chat's tests folder.
func (s *Store) root(chat string) string {
	dir := filepath.Join(s.dir(chat), "tests")
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func (s *Store) file(chat string) string { return filepath.Join(s.root(chat), "tests.json") }

// FilesDir is the folder of a test's evidence; the Markdown answers name
// files by their absolute path in it, so that an agent can open them.
func (s *Store) FilesDir(chat, test string) string {
	return filepath.Join(s.root(chat), "files", test)
}

func (s *Store) lock(chat string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.locks[chat]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[chat] = l
	}
	return l
}

func (s *Store) changed(chat string) {
	s.mu.Lock()
	f := s.onChange
	s.mu.Unlock()
	if f != nil {
		f(chat)
	}
}

// load reads a chat's tests.json; a chat without one has no tests.
func (s *Store) load(chat string) (*doc, error) {
	if err := checkChat(chat); err != nil {
		return nil, err
	}
	file := s.file(chat)
	blob, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return &doc{Version: fileVersion, Next: 1}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the chat's tests cannot be read: %v", err)
	}
	d := &doc{}
	if err := json.Unmarshal(blob, d); err != nil {
		return nil, fmt.Errorf("the chat's tests are unreadable: %s is not valid JSON (%v); move it away to start over", file, err)
	}
	if d.Version != fileVersion {
		return nil, fmt.Errorf("the chat's tests are unreadable: %s has version %d, and this Socrates reads version %d", file, d.Version, fileVersion)
	}
	// next must stay above every id there is, even in a file put together
	// by hand, so that no id is given twice.
	for _, t := range d.Tests {
		if n := idNumber(t.ID); n >= d.Next {
			d.Next = n + 1
		}
	}
	if d.Next < 1 {
		d.Next = 1
	}
	return d, nil
}

// save writes tests.json whole: a temporary file beside it, a sync and a
// rename, so that a crash leaves the old file or the new one, never half.
func (s *Store) save(chat string, d *doc) error {
	d.Version = fileVersion
	d.Rev++
	blob, err := json.MarshalIndent(d, "", " ")
	if err != nil {
		return err
	}
	root := s.root(chat)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("the chat's tests cannot be saved: %v", err)
	}
	tmp, err := os.CreateTemp(root, ".tests-*.json")
	if err != nil {
		return fmt.Errorf("the chat's tests cannot be saved: %v", err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(append(blob, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.file(chat))
	}
	if err != nil {
		return fmt.Errorf("the chat's tests cannot be saved: %v", err)
	}
	// The rename is only durable once the folder is synced; where a folder
	// cannot be opened for that (Windows), the rename has to do.
	if dir, err := os.Open(root); err == nil {
		dir.Sync()
		dir.Close()
	}
	return nil
}

// change runs f on the chat's tests under the chat's lock and saves them if
// f asks to; once they are saved and the lock is released, it calls
// OnChange. Removing the files of what is gone is the caller's, after a
// save that succeeded, so that a failed save never loses evidence.
func (s *Store) change(chat string, f func(d *doc) (save bool, err error)) error {
	l := s.lock(chat)
	l.Lock()
	d, err := s.load(chat)
	if err != nil {
		l.Unlock()
		return err
	}
	save, err := f(d)
	if err == nil && save {
		err = s.save(chat, d)
	}
	l.Unlock()
	if err == nil && save {
		s.changed(chat)
	}
	return err
}

// read loads the chat's tests under its lock, for a consistent view.
func (s *Store) read(chat string) (*doc, error) {
	l := s.lock(chat)
	l.Lock()
	defer l.Unlock()
	return s.load(chat)
}

// List is every test of the chat, in the order they were created.
func (s *Store) List(chat string) ([]Test, error) {
	d, err := s.read(chat)
	if err != nil {
		return nil, err
	}
	return d.Tests, nil
}

// Get is one test, named by its id ("T3", in any case) or its exact title.
func (s *Store) Get(chat, ref string) (Test, error) {
	d, err := s.read(chat)
	if err != nil {
		return Test{}, err
	}
	i, err := find(d, ref)
	if err != nil {
		return Test{}, err
	}
	return d.Tests[i], nil
}

// Rev is the chat's revision: it grows with every change, and is 0 for a
// chat that never had a test.
func (s *Store) Rev(chat string) (int64, error) {
	d, err := s.read(chat)
	if err != nil {
		return 0, err
	}
	return d.Rev, nil
}

// Fields are what a test is created with.
type Fields struct {
	Title       string
	Goal        string
	Environment string
	Tags        []string
	Steps       []Step
}

// Create adds a test, a draft until it first runs.
func (s *Store) Create(chat string, in Fields, by By) (Test, error) {
	f, err := cleanFields(in)
	if err != nil {
		return Test{}, err
	}
	var out Test
	err = s.change(chat, func(d *doc) (bool, error) {
		if len(d.Tests) >= MaxTests {
			return false, fmt.Errorf("the chat already has %d tests, the most it can have: delete one it no longer needs with test_delete first", MaxTests)
		}
		now := s.now()
		t := Test{
			ID: "T" + strconv.Itoa(d.Next), Title: f.Title, Goal: f.Goal, Environment: f.Environment,
			Tags: f.Tags, Steps: f.Steps, CreatedAt: now, UpdatedAt: now, StepsAt: now,
		}
		addEvent(&t, Event{At: now, By: by, What: EventCreated})
		d.Next++
		// A folder of this id can only be left over from a delete whose
		// clean-up failed; it is not this test's.
		os.RemoveAll(s.FilesDir(chat, t.ID))
		d.Tests = append(d.Tests, t)
		out = t
		return true, nil
	})
	return out, err
}

// Patch is an edit: every field that is not nil is set. Skip sets the test
// aside with that reason, or with "" brings it back.
type Patch struct {
	Title       *string
	Goal        *string
	Environment *string
	Tags        *[]string
	Steps       *[]Step
	Skip        *string
}

// Update edits a test. An edit that changes nothing is not saved.
func (s *Store) Update(chat, ref string, p Patch, by By) (Test, error) {
	if p.Title == nil && p.Goal == nil && p.Environment == nil && p.Tags == nil && p.Steps == nil && p.Skip == nil {
		return Test{}, errors.New("nothing to change: name at least one of title, goal, environment, tags, steps and skip")
	}
	var out Test
	err := s.change(chat, func(d *doc) (bool, error) {
		i, err := find(d, ref)
		if err != nil {
			return false, err
		}
		t := &d.Tests[i]
		cur := Fields{Title: t.Title, Goal: t.Goal, Environment: t.Environment, Tags: t.Tags, Steps: t.Steps}
		next := cur
		if p.Title != nil {
			next.Title = *p.Title
		}
		if p.Goal != nil {
			next.Goal = *p.Goal
		}
		if p.Environment != nil {
			next.Environment = *p.Environment
		}
		if p.Tags != nil {
			next.Tags = *p.Tags
		}
		if p.Steps != nil {
			next.Steps = *p.Steps
		}
		f, err := cleanFields(next)
		if err != nil {
			return false, err
		}
		now := s.now()
		var what []string
		if f.Title != t.Title {
			what = append(what, "title")
		}
		if f.Goal != t.Goal {
			what = append(what, "goal")
		}
		if f.Environment != t.Environment {
			what = append(what, "environment")
		}
		if !slices.Equal(f.Tags, t.Tags) {
			what = append(what, "tags")
		}
		if !slices.Equal(f.Steps, t.Steps) {
			what = append(what, "steps")
			t.StepsAt = now
		}
		saved := false
		if len(what) > 0 {
			t.Title, t.Goal, t.Environment, t.Tags, t.Steps = f.Title, f.Goal, f.Environment, f.Tags, f.Steps
			addEvent(t, Event{At: now, By: by, What: EventEdited, Detail: strings.Join(what, ", ")})
			saved = true
		}
		if p.Skip != nil {
			reason := strings.TrimSpace(*p.Skip)
			switch {
			case reason != "" && utf8.RuneCountInString(reason) > MaxCaption:
				return false, fmt.Errorf("the reason to set the test aside is %d characters long; keep it to %d", utf8.RuneCountInString(reason), MaxCaption)
			case reason != "" && (t.Skip == nil || t.Skip.Reason != reason):
				t.Skip = &Skip{Reason: reason, By: by, At: now}
				addEvent(t, Event{At: now, By: by, What: EventSkip, Detail: reason})
				saved = true
			case reason == "" && t.Skip != nil:
				t.Skip = nil
				addEvent(t, Event{At: now, By: by, What: EventUnskip})
				saved = true
			}
		}
		if saved {
			t.UpdatedAt = now
		}
		out = *t
		return saved, nil
	})
	return out, err
}

// Delete removes a test with its runs and files. Its id is not given again.
func (s *Store) Delete(chat, ref string) error {
	var gone string
	err := s.change(chat, func(d *doc) (bool, error) {
		i, err := find(d, ref)
		if err != nil {
			return false, err
		}
		gone = d.Tests[i].ID
		d.Tests = slices.Delete(d.Tests, i, i+1)
		return true, nil
	})
	if err == nil {
		os.RemoveAll(s.FilesDir(chat, gone))
	}
	return err
}

// find is the index of the test ref names: its id in any case, or its
// exact title.
func find(d *doc, ref string) (int, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return -1, errors.New("no test is named: give its id (T3) or its exact title")
	}
	if idNumber(ref) > 0 {
		id := strings.ToUpper(ref)
		for i, t := range d.Tests {
			if t.ID == id {
				return i, nil
			}
		}
		return -1, fmt.Errorf("test %s does not exist; %s", id, have(d))
	}
	var hits []int
	for i, t := range d.Tests {
		if t.Title == ref {
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return -1, fmt.Errorf("no test is titled %q; %s", ref, have(d))
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = d.Tests[h].ID
	}
	return -1, fmt.Errorf("%d tests are titled %q (%s): name one by its id", len(hits), ref, strings.Join(ids, ", "))
}

// have says which tests the chat has, for an error about one it has not.
func have(d *doc) string {
	if len(d.Tests) == 0 {
		return "the chat has no tests yet (test_create writes one)"
	}
	const most = 30
	ids := make([]string, 0, most)
	for i, t := range d.Tests {
		if i == most {
			break
		}
		ids = append(ids, t.ID)
	}
	more := ""
	if len(d.Tests) > most {
		more = fmt.Sprintf(" and %d more (tests_list names them all)", len(d.Tests)-most)
	}
	return "the chat has " + strings.Join(ids, ", ") + more
}

// checkChat refuses a chat id that is not one folder's name, before it can
// make a path that leads elsewhere.
func checkChat(chat string) error {
	if strings.TrimSpace(chat) == "" {
		return errors.New("no chat is named")
	}
	if chat == "." || chat == ".." || strings.ContainsAny(chat, "/\\\x00") {
		return fmt.Errorf("%q is not a chat id", chat)
	}
	return nil
}

// idNumber is the number of a test id ("T12", "t12": 12), or 0 for
// anything else.
func idNumber(id string) int {
	if len(id) < 2 || (id[0] != 'T' && id[0] != 't') || id[1] == '0' {
		return 0
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil || n <= 0 || strconv.Itoa(n) != id[1:] {
		return 0
	}
	return n
}

// cleanFields trims what a test is written with and checks it against the
// limits.
func cleanFields(in Fields) (Fields, error) {
	f := Fields{
		Title:       oneLine(in.Title),
		Goal:        strings.TrimSpace(in.Goal),
		Environment: oneLine(in.Environment),
	}
	switch n := utf8.RuneCountInString(f.Title); {
	case n == 0:
		return f, errors.New("the test needs a title: what flow it checks, in a few words")
	case n > MaxTitle:
		return f, fmt.Errorf("the title is %d characters long; keep it to %d and put the rest in the goal", n, MaxTitle)
	}
	if len(f.Goal) > MaxGoal {
		return f, fmt.Errorf("the goal is %s; keep it to %s", size(int64(len(f.Goal))), size(MaxGoal))
	}
	if n := utf8.RuneCountInString(f.Environment); n > MaxEnvironment {
		return f, fmt.Errorf("the environment is %d characters long; keep it to %d", n, MaxEnvironment)
	}
	for _, tag := range in.Tags {
		tag = oneLine(tag)
		if tag == "" || slices.Contains(f.Tags, tag) {
			continue
		}
		if n := utf8.RuneCountInString(tag); n > MaxTag {
			return f, fmt.Errorf("the tag %q is %d characters long; keep tags to %d", tag, n, MaxTag)
		}
		f.Tags = append(f.Tags, tag)
	}
	if len(f.Tags) > MaxTags {
		return f, fmt.Errorf("the test has %d tags; keep it to %d", len(f.Tags), MaxTags)
	}
	switch {
	case len(in.Steps) == 0:
		return f, errors.New("the test needs at least one step: an action and what must then be seen (expect)")
	case len(in.Steps) > MaxSteps:
		return f, fmt.Errorf("the test has %d steps; a test has at most %d: split the flow into several tests", len(in.Steps), MaxSteps)
	}
	f.Steps = make([]Step, len(in.Steps))
	for i, st := range in.Steps {
		st = Step{Action: strings.TrimSpace(st.Action), Expect: strings.TrimSpace(st.Expect)}
		switch {
		case st.Action == "":
			return f, fmt.Errorf("step %d has no action: say what a person does", i+1)
		case st.Expect == "":
			return f, fmt.Errorf("step %d has no expect: say what must then be seen", i+1)
		case utf8.RuneCountInString(st.Action) > MaxStepText:
			return f, fmt.Errorf("step %d's action is longer than %d characters; put the context in the goal", i+1, MaxStepText)
		case utf8.RuneCountInString(st.Expect) > MaxStepText:
			return f, fmt.Errorf("step %d's expect is longer than %d characters; split the step", i+1, MaxStepText)
		}
		f.Steps[i] = st
	}
	return f, nil
}

// oneLine trims s and joins its lines with spaces, for a title, an
// environment or a tag.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// addEvent records an event, dropping the oldest beyond what a test keeps.
func addEvent(t *Test, e Event) {
	t.Events = append(t.Events, e)
	if n := len(t.Events) - keepEvents; n > 0 {
		t.Events = slices.Delete(t.Events, 0, n)
	}
}

// size is a number of bytes as a person reads it: "820 B", "3.4 KB", "20 MB".
func size(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/(1<<10))) + " KB"
	case n < 1<<30:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/(1<<20))) + " MB"
	}
	return trimZero(fmt.Sprintf("%.1f", float64(n)/(1<<30))) + " GB"
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }
