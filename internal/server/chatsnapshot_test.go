package server

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
	"github.com/saschazesiger/SocratesAgent/internal/agenthost"
)

// snapshotJournal writes events to a chat's journal and answers them as stamped.
func snapshotJournal(t *testing.T, dataDir, id string, events ...agent.Event) []agent.Event {
	t.Helper()
	j, _, err := agent.OpenJournal(agenthost.JournalPath(dataDir, id))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	out, err := j.Append(events...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// workflowSteps is a part replaced whole n times, as a workflow is.
func workflowSteps(from, n int) []agent.Event {
	var out []agent.Event
	for i := from; i < from+n; i++ {
		out = append(out,
			agent.Event{Type: agent.TypePart, MessageID: "a", PartID: "wf", Kind: agent.PartTool, State: agent.PartRunning,
				Content: json.RawMessage(fmt.Sprintf(`{"tool":"Workflow","step":%d}`, i))},
			agent.Event{Type: agent.TypeRateLimit, Window: "five_hour", Used: float64(i)})
	}
	return out
}

func folded(events []agent.Event) string {
	p := agent.Fold(events)
	raw, _ := json.Marshal(p)
	return string(raw)
}

// A history is read from its snapshot and the journal after it, and folds
// into what the whole journal folds into; a snapshot that is not the
// journal's any more is let go.
func TestAHistoryIsReadFromItsSnapshotAndTheJournalAfterIt(t *testing.T) {
	dir := t.TempDir()
	id := "c1"
	first := snapshotJournal(t, dir, id, append([]agent.Event{
		{Type: agent.TypeMessage, MessageID: "a", Role: agent.RoleAssistant, State: agent.MessageStreaming},
	}, workflowSteps(0, 40)...)...)
	h, err := readHistory(dir, id)
	if err != nil || h.base != 0 || len(h.events) != len(first) || h.tail != h.good || h.good == 0 {
		t.Fatalf("without a snapshot: %+v %v", h.base, err)
	}
	if err := writeSnapshot(dir, id, h.events, h.good); err != nil {
		t.Fatal(err)
	}
	more := snapshotJournal(t, dir, id, workflowSteps(40, 5)...)
	all := append(append([]agent.Event{}, first...), more...)

	h, err = readHistory(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if h.base != first[len(first)-1].Seq {
		t.Fatalf("base %d, want %d", h.base, first[len(first)-1].Seq)
	}
	if len(h.events) >= len(all)/2 || h.events[len(h.events)-1].Seq != all[len(all)-1].Seq {
		t.Fatalf("%d events read of %d, the last %d", len(h.events), len(all), h.events[len(h.events)-1].Seq)
	}
	if info, _ := os.Stat(agenthost.JournalPath(dir, id)); h.good != info.Size() || h.tail <= 0 || h.tail >= h.good/2 {
		t.Fatalf("good %d tail %d of %d", h.good, h.tail, info.Size())
	}
	if a, b := folded(h.events), folded(all); a != b {
		t.Fatalf("the history folds differently:\n%s\n%s", a, b)
	}

	// The journal restored from a backup of another day: the snapshot is
	// not its own.
	if err := os.Remove(agenthost.JournalPath(dir, id)); err != nil {
		t.Fatal(err)
	}
	other := snapshotJournal(t, dir, id, append([]agent.Event{{Type: agent.TypeNotice, Text: "another journal, longer at the start"}}, workflowSteps(0, 30)...)...)
	h, err = readHistory(dir, id)
	if err != nil || h.base != 0 || len(h.events) != len(other) {
		t.Fatalf("a snapshot of another journal was used: base %d, %d events of %d, %v", h.base, len(h.events), len(other), err)
	}
}
