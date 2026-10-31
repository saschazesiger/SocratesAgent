package chattests

import (
	"path/filepath"
	"strings"
	"testing"
)

// wantAll fails unless text holds every part.
func wantAll(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(text, p) {
			t.Errorf("the answer lacks %q:\n%s", p, text)
		}
	}
}

func TestListText(t *testing.T) {
	s, _ := newStore(t)
	if text := ListText(nil); !strings.Contains(text, "no tests yet") || !strings.Contains(text, "test_create") {
		t.Fatalf("empty list: %s", text)
	}
	mustCreate(t, s, "c1", "Sign in with a wrong password", 4)
	mustStart(t, s, "c1", "T1")
	mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "pass"})
	mustJudge(t, s, "c1", "T1", Judgement{Step: 3, Verdict: "fail", Observed: "The page stays blank\nfor ever"})
	mustJudge(t, s, "c1", "T1", Judgement{Step: 4, Verdict: "fail", Observed: "Nothing"})
	s.Finish("c1", "T1", "Fails.", "")
	s.SetOverride("c1", "T1", "passed", "Known, fixed upstream")
	more := steps(5)
	s.Update("c1", "T1", Patch{Steps: &more}, ByPerson)

	mustCreate(t, s, "c1", "Checkout", 4)
	mustStart(t, s, "c1", "T2")
	mustJudge(t, s, "c1", "T2", Judgement{Step: 1, Verdict: "pass"})
	mustJudge(t, s, "c1", "T2", Judgement{Step: 2, Verdict: "pass"})

	mustCreate(t, s, "c1", "Export", 1)
	reason := "Needs a printer"
	s.Update("c1", "T3", Patch{Skip: &reason}, ByAgent)
	mustCreate(t, s, "c1", "Search", 1)

	tests, _ := s.List("c1")
	text := ListText(tests)
	wantAll(t, text,
		"4 tests: 1 running, 1 passed, 1 skipped, 1 draft.",
		`- T1 · Passed · Sign in with a wrong password — 5 steps · run 1 failed (finished 10:00; step 3 fail: "The page stays blank for ever" and 1 more) · changed since the last run · overridden by the person: Passed ("Known, fixed upstream")`,
		"- T2 · Running · Checkout — 4 steps · run 1 open, 2 of 4 judged",
		`- T3 · Skipped · Export — 1 step · never run · set aside by the agent: "Needs a printer"`,
		"- T4 · Draft · Search — 1 step · never run",
	)
}

func TestTestText(t *testing.T) {
	s, _ := newStore(t)
	src := t.TempDir()
	s.Create("c1", Fields{Title: "Sign in", Goal: "Check the **login**.", Environment: "iPhone 15", Tags: []string{"auth"},
		Steps: []Step{{"Open /login", "The form"}, {"Submit a wrong password", "An error under the field"}}}, ByAgent)
	mustStart(t, s, "c1", "T1")
	mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "pass", Observed: "The form shows",
		Files: []Attach{{Path: file(t, src, "login.png", "x"), Caption: "The login form", Source: "browserstack"}}})
	s.Finish("c1", "T1", "Step 2 not reached.", "- Nothing found")
	mustStart(t, s, "c1", "T1")
	mustJudge(t, s, "c1", "T1", Judgement{Step: 2, Verdict: "issue", Observed: "The error is grey on grey", Note: "contrast"})
	tt, _ := s.Get("c1", "T1")
	dir := s.FilesDir("c1", "T1")

	text, err := TestText(tt, 0, dir)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, text,
		"# T1 · Sign in", "Status: Running", "Environment: iPhone 15", "Tags: auth", "runs 1–2",
		"## Goal\n\nCheck the **login**.",
		"1. Open /login\n   Expect: The form",
		"## Run 2 · open, 1 of 2 judged",
		"2. issue · Submit a wrong password\n   Observed: The error is grey on grey\n   Note: contrast",
		"## Run 1 · blocked",
		"Summary: Step 2 not reached.",
		"   - "+filepath.Join(dir, "r1", "01-login.png")+" — The login form (image, 1 B, from browserstack)",
		"Observed: Not reached",
		"### Findings\n\n- Nothing found",
		"## Events", "the agent: created", "the agent: finished — Run 1: blocked (1 pass, 1 blocked)",
	)
	if strings.Index(text, "## Run 2") > strings.Index(text, "## Run 1") {
		t.Fatal("the runs are not newest first")
	}
	one, err := TestText(tt, 1, dir)
	if err != nil || strings.Contains(one, "## Run 2") || !strings.Contains(one, "## Run 1") {
		t.Fatalf("run 1 only: %v\n%s", err, one)
	}
	_, err = TestText(tt, 3, dir)
	wantErr(t, err, "run 3 of T1 does not exist: runs 1–2")
}

func TestStartJudgedFinishText(t *testing.T) {
	s, _ := newStore(t)
	src := t.TempDir()
	mustCreate(t, s, "c1", "Flow", 3)
	tt, r, _ := s.Start("c1", "T1", "Pixel 8", nil)
	wantAll(t, StartText(tt, r),
		`Run 1 of T1 "Flow" started · Pixel 8.`,
		"1. Do thing 1\n   Expect: Thing 1 shows\n2. Do thing 2",
		"test_step", `test_finish {"test": "T1"`)

	tt, _, added := mustJudge(t, s, "c1", "T1", Judgement{Step: 2, Verdict: "fail", Observed: "Error",
		Files: []Attach{{Path: file(t, src, "err.png", "x"), Caption: "The error"}}})
	wantAll(t, JudgedText(tt, 2, added, s.FilesDir("c1", "T1")),
		"Step 2 of run 1 of T1: fail, recorded.",
		filepath.Join(s.FilesDir("c1", "T1"), "r1", "01-err.png")+" — The error (image, 1 B)",
		"Steps 1, 3 not judged yet")
	tt, _, added = mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "pass"})
	wantAll(t, JudgedText(tt, 1, added, ""), "No file shows this step", "Step 3 not judged yet")

	tt, added, _ = s.Attach("c1", "T1", 0, []Attach{{Path: file(t, src, "net.har", "x")}})
	wantAll(t, AttachText(tt, 0, added, "/f"), "Attached to run 1 of T1:", "/f/r1/02-net.har (file, 1 B)")

	tt, r, _ = s.Finish("c1", "T1", "Fails at 2.", "")
	wantAll(t, FinishText(tt, r),
		"Run 1 of T1 finished: fail (1 pass, 1 fail, 1 blocked). T1 is now Failed.",
		"Not reached, so blocked: step 3.")
	wantAll(t, CreatedText(tt), `Created T1 "Flow" with 3 steps (draft)`)
}

func TestLongAnswersAreCut(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "c1", "Flow", 1)
	mustStart(t, s, "c1", "T1")
	for i := 0; i < 5; i++ {
		mustJudge(t, s, "c1", "T1", Judgement{Step: 1, Verdict: "pass", Observed: strings.Repeat("ä", MaxObserved/2)})
		s.Finish("c1", "T1", "ok", strings.Repeat("Finding line\n", 3000))
		mustStart(t, s, "c1", "T1")
	}
	tt, _ := s.Get("c1", "T1")
	text, _ := TestText(tt, 0, "/f")
	if len(text) > maxAnswer+300 || !strings.Contains(text, `Cut: the answer is longer than 40 KB; read one run with test_read {"test": "T1", "run": n}.`) {
		t.Fatalf("%d bytes, ending %q", len(text), text[len(text)-200:])
	}
}
