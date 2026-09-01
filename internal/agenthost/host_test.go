package agenthost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/harness"
)

// This is the property every guard in the engine assumes and that nothing else
// supplies: a subscriber that attaches while the pump is appending gets
// exactly the seqs it asked for, in order, with no gap and no repeat - and the
// status frame that closes its replay is snapshotted at exactly that point.
//
// Registering on accept would interleave live events with the replay and
// deliver everything appended before EOF twice. Registering after the replay
// would lose whatever was appended in between, and if that is the turn's
// turn_finished, a run waits forever.
func TestSubscribeDuringAppendLosesNothingAndRepeatsNothing(t *testing.T) {
	m := newManager(t)
	h := openTest(t, m, "chat_stream",
		step{Do: "text", Text: "chatter", Count: 150},
		step{Do: "sleep", MS: 150},
		step{Do: "text", Text: "chatter", Count: 150},
		step{Do: "sleep", MS: 150},
		step{Do: "text", Text: "chatter", Count: 150},
		step{Do: "end", Outcome: "ok"})

	floor, err := h.Send(t.Context(), "run_1", "talk a lot")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// Let the pump get well ahead, so the subscription below attaches to a
	// journal that is being written while it is read.
	time.Sleep(80 * time.Millisecond)

	frames, unsubscribe := h.Subscribe(floor)
	defer unsubscribe()

	var (
		seqs     []int64
		caughtUp *Status
		lastSeq  int64
		finished bool
	)
	deadline := time.After(30 * time.Second)
	for !finished || caughtUp == nil {
		select {
		case f, open := <-frames:
			if !open {
				t.Fatalf("the stream ended early: %d events, caught up %v, finished %v", len(seqs), caughtUp != nil, finished)
			}
			if f.CaughtUp != nil {
				if caughtUp == nil {
					caughtUp = f.CaughtUp
					// The status closing a replay is evaluated after every
					// event already sent on this connection and before every
					// event sent after it.
					if f.CaughtUp.Seq != lastSeq {
						t.Fatalf("the caught-up status says seq %d, but %d events had been delivered up to seq %d",
							f.CaughtUp.Seq, len(seqs), lastSeq)
					}
				}
				continue
			}
			seqs = append(seqs, f.Event.Seq)
			lastSeq = f.Event.Seq
			if f.Event.Kind == harness.KindTurnFinished {
				finished = true
			}
		case <-deadline:
			t.Fatalf("the turn did not end; %d events seen", len(seqs))
		}
	}

	if len(seqs) < 900 {
		t.Fatalf("expected the whole turn, got %d events", len(seqs))
	}
	for i, seq := range seqs {
		if want := floor + int64(i) + 1; seq != want {
			t.Fatalf("event %d has seq %d, want %d - the stream has a gap or a repeat", i, seq, want)
		}
	}
}

// A crashed CLI must not cost the user their chat: the next message restarts
// the adapter with the session id spec.json now carries, and says so in the
// transcript rather than pretending nothing happened.
func TestSendAfterACrashRestartsTheAdapter(t *testing.T) {
	m := newManager(t)
	h := openTest(t, m, "chat_crash", step{Do: "die"})

	if _, err := h.Send(t.Context(), "run_1", "first"); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool { return !h.Working() })

	floor, err := h.Send(t.Context(), "run_2", "second")
	if err != nil {
		t.Fatalf("the send that should have restarted the agent failed: %v", err)
	}
	frames, unsubscribe := h.Subscribe(floor)
	defer unsubscribe()
	events := collect(t, frames, 15*time.Second)

	sawNotice := false
	for _, ev := range events {
		if ev.Kind == harness.KindNotice && strings.Contains(ev.Error, "restarted") {
			sawNotice = true
			// The notice belongs inside the turn it explains, so it has to be
			// above the floor the engine filters on.
			if ev.Seq <= floor {
				t.Errorf("the restart notice has seq %d, at or below the floor %d - the engine would drop it", ev.Seq, floor)
			}
		}
	}
	if !sawNotice {
		t.Errorf("the restart was not announced in the transcript: %#v", events)
	}
	// The script runs from the start on every turn, so this one dies too - but
	// it got a turn_started and a turn_finished of its own, which is what
	// proves a second adapter was really started and really answered.
	kinds := map[string]int{}
	for _, ev := range events {
		kinds[ev.Kind]++
	}
	if kinds[harness.KindTurnStarted] != 1 || kinds[harness.KindTurnFinished] != 1 {
		t.Errorf("the restarted turn did not start and end exactly once: %#v", kinds)
	}
	// Whether the restarted adapter's session_id lands above or below this
	// turn's floor is not something the design fixes - it depends on when the
	// new pump is scheduled - so the assertion is on where the session id
	// actually lives, which is the host's Status.
	if st := h.Status(); st.SessionID == "" {
		t.Errorf("the restarted adapter did not report its resumed session: %+v", st)
	}
}

// A crash loop has to end somewhere, and a chat that says what happened is
// better than one that quietly stalls.
func TestTheRestartBudgetIsSpentAndThenRefused(t *testing.T) {
	m := newManager(t)
	h := openTest(t, m, "chat_loop", step{Do: "die"})

	var lastErr error
	for i := 0; i < maxRestarts+2; i++ {
		if _, err := h.Send(t.Context(), "run", "again"); err != nil {
			lastErr = err
			break
		}
		waitFor(t, 10*time.Second, func() bool { return !h.Working() })
	}
	if lastErr == nil {
		t.Fatal("the restart budget was never spent")
	}
	if !strings.Contains(lastErr.Error(), "crashed") {
		t.Errorf("the refusal should say what happened, got: %v", lastErr)
	}
}

// The journal recovers its sequence number from what is on disk, so a host
// restarted by hand does not hand out seqs that are already taken.
func TestJournalRecoversItsSequence(t *testing.T) {
	dir := t.TempDir()
	j, err := openJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := j.append(harness.Event{Kind: harness.KindNotice, Error: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if j.seq != 5 {
		t.Fatalf("seq = %d", j.seq)
	}
	if err := j.close(); err != nil {
		t.Fatal(err)
	}

	// A killed host leaves a half written last line; it is not a reason to
	// refuse the whole transcript.
	f, err := os.OpenFile(journalPath(dir), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"kind":"noti`)
	f.Close()

	again, err := openJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.close()
	if again.seq != 5 {
		t.Fatalf("recovered seq = %d, want 5", again.seq)
	}
	var seen []int64
	if err := again.replay(2, func(ev harness.Event) bool { seen = append(seen, ev.Seq); return true }); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || seen[0] != 3 || seen[2] != 5 {
		t.Fatalf("replay from 2 = %#v", seen)
	}
}

// A subscribe below the point rotation trimmed to cannot be answered honestly,
// so it is refused rather than silently answered with half a turn.
func TestReplayBelowTheTrimPointIsRefused(t *testing.T) {
	dir := t.TempDir()
	j, err := openJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	if _, err := j.append(harness.Event{Kind: harness.KindNotice, Error: "x"}); err != nil {
		t.Fatal(err)
	}
	j.trimmedTo = 40
	if err := j.replay(10, func(harness.Event) bool { return true }); err != ErrReplayWindow {
		t.Fatalf("replay error = %v, want %v", err, ErrReplayWindow)
	}
	// A subscribe from zero is a chat's first turn on this host, and it is
	// refused too: handing it the surviving tail as if it were the whole
	// journal is exactly the silent loss the check exists to prevent.
	if err := j.replay(0, func(harness.Event) bool { return true }); err != ErrReplayWindow {
		t.Fatalf("a subscription from the start of a rotated journal = %v", err)
	}
	// The trim point is the last seq that is gone, so a subscribe from exactly
	// that number asks only for events that are still here.
	if err := j.replay(40, func(harness.Event) bool { return true }); err != nil {
		t.Fatalf("a subscription from the trim point was refused: %v", err)
	}
}

// The sweep is the only place the previous version's wire format is still
// spoken, and it exists so an upgrade does not leave detached terminal hosts
// running forever.
func TestSweepLegacyTerminalsRemovesTheDirectory(t *testing.T) {
	data := t.TempDir()
	dir := filepath.Join(data, "terminals", "term_abc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), []byte(`{"id":"term_abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	SweepLegacyTerminals(data)
	if _, err := os.Stat(filepath.Join(data, "terminals")); !os.IsNotExist(err) {
		t.Fatalf("the legacy directory survived the sweep: %v", err)
	}
	// It is safe to run on an installation that never had one.
	SweepLegacyTerminals(t.TempDir())
}

// Closing a session that is mid-turn ends the host rather than leaving it
// lingering, because whoever asked has already seen the answer.
func TestCloseEndsAHangingTurn(t *testing.T) {
	m := newManager(t)
	h := openTest(t, m, "chat_close", step{Do: "hang"})
	if _, err := h.Send(t.Context(), "run_1", "wait forever"); err != nil {
		t.Fatalf("send: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := m.Close(ctx, h.ID(), 2*time.Second); err != nil {
		t.Fatalf("close: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool { return !h.Alive() })
}

// A host restarted after its journal was rotated has to remember where the
// file was cut, or it would serve the surviving tail as if it were the whole
// transcript and a run would be replayed from the middle without anyone
// noticing.
func TestARotatedJournalIsStillRotatedAfterAReopen(t *testing.T) {
	dir := t.TempDir()
	// A journal whose first line is seq 1000: everything below it is gone.
	var lines []byte
	for seq := int64(1000); seq < 1005; seq++ {
		lines = append(lines, []byte(fmt.Sprintf(
			`{"kind":"notice","seq":%d,"ts":1,"error":"x"}`+"\n", seq))...)
	}
	if err := os.WriteFile(journalPath(dir), lines, 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	if j.seq != 1004 {
		t.Fatalf("recovered seq = %d", j.seq)
	}
	if j.trimmedTo != 999 {
		t.Fatalf("trim point = %d, want 999", j.trimmedTo)
	}
	if err := j.replay(5, func(harness.Event) bool { return true }); err != ErrReplayWindow {
		t.Fatalf("a replay from before the cut = %v, want %v", err, ErrReplayWindow)
	}
	if err := j.replay(999, func(harness.Event) bool { return true }); err != nil {
		t.Fatalf("a replay from the cut itself was refused: %v", err)
	}
	// A journal that starts where journals start has nothing trimmed.
	fresh, err := openJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.close()
	if _, err := fresh.append(harness.Event{Kind: harness.KindNotice}); err != nil {
		t.Fatal(err)
	}
	if fresh.trimmedTo != 0 {
		t.Fatalf("a fresh journal reports a trim point of %d", fresh.trimmedTo)
	}
}

// A connection stays a broadcast recipient after the turn that subscribed has
// ended - there is no unsubscribe op - and the next turn subscribes on that
// same connection. Its stream has to begin at its own floor: the frames the
// host pushed in between belong to the connection's past, and letting them
// through puts an arbitrary prefix of another turn in front of this one's
// replay, with a gap at the front where the previous subscriber was being torn
// down. That gap is a journal event the new subscriber never sees at all,
// because its replay is already behind it on the wire.
func TestASecondSubscriptionStartsAtItsOwnFloor(t *testing.T) {
	m := newManager(t)
	h := openTest(t, m, "chat_resub",
		step{Do: "text", Text: "one"},
		step{Do: "end", Outcome: "ok"})

	// A first turn, subscribed to and read to the end.
	floor, err := h.Send(t.Context(), "run_1", "first")
	if err != nil {
		t.Fatal(err)
	}
	frames, unsubscribe := h.Subscribe(floor)
	collect(t, frames, 15*time.Second)
	unsubscribe()

	// A second turn whose events are all pushed before anyone subscribes: the
	// connection is still registered, so this is the window that matters.
	floor2, err := h.Send(t.Context(), "run_2", "second")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, func() bool {
		st, err := h.Refresh(t.Context())
		return err == nil && !st.Busy && st.Seq > floor2+2
	})

	frames2, unsubscribe2 := h.Subscribe(floor2)
	defer unsubscribe2()
	var seqs []int64
	for _, ev := range collect(t, frames2, 15*time.Second) {
		seqs = append(seqs, ev.Seq)
	}
	if len(seqs) == 0 {
		t.Fatal("the second subscription received nothing")
	}
	for i, seq := range seqs {
		if want := floor2 + int64(i) + 1; seq != want {
			t.Fatalf("event %d of the second subscription has seq %d, want %d - the stream has a gap or a repeat: %v",
				i, seq, want, seqs)
		}
	}
}
