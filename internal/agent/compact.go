package agent

// Compact is a run of a chat's events with what later events of the same run
// make moot left out, for a reader that folds them: a browser replaying the
// journal. Folded on top of whatever came before the run, it gives exactly
// the projection the whole run gives.
//
// A journal repeats itself where a part is replaced whole as it goes on - a
// workflow that runs for hours journals its whole state at every step, a
// subagent its own - and where a value is reported again and again (usage
// windows, background task counts, the agent's info). Replaying it means
// folding every one of those copies to keep the last; this keeps the last:
//
//   - the events of one part (its part events and deltas) become one part
//     event where the part first appears in the run, carrying what its last
//     part event said and, in UpdatedAt, when; the deltas after that last
//     part event stay, as they add to it. The part keeps its place - a part
//     event replaces a part where it is (Projection.part) - and nothing in
//     between reads a part's state but a turn that ends other than
//     completed, which interrupts whatever still runs: such an end splits
//     the run, and a part is compacted on each side of it on its own;
//   - an event whose only effect is a value a later event of the same kind
//     sets again (a rate_limit of the same window, tasks, usage with usage,
//     agent.info) goes, unless it carries a client_id, which the projection
//     remembers the first event of.
//
// Seqs keep their order, with gaps where events went.
func Compact(events []Event) []Event {
	if len(events) < 2 {
		return events
	}
	type key struct {
		part string
		span int
	}
	type run struct {
		first, last int // the part's first event in the span, and its last part event (-1: none)
	}
	runs := map[key]*run{}
	keys := make([]key, len(events))
	lastOf := map[string]int{}
	span := 0
	for i, ev := range events {
		switch ev.Type {
		case TypePart, TypePartDelta:
			if ev.PartID == "" {
				continue
			}
			k := key{ev.PartID, span}
			keys[i] = k
			r := runs[k]
			if r == nil {
				r = &run{first: i, last: -1}
				runs[k] = r
			}
			if ev.Type == TypePart {
				r.last = i
			}
		case TypeTurnEnded:
			if ev.Outcome != OutcomeCompleted {
				span++
			}
		}
		if s := stateKey(ev); s != "" {
			lastOf[s] = i
		}
	}
	out := make([]Event, 0, len(events))
	for i, ev := range events {
		if s := stateKey(ev); s != "" && lastOf[s] != i {
			continue
		}
		if (ev.Type != TypePart && ev.Type != TypePartDelta) || ev.PartID == "" {
			out = append(out, ev)
			continue
		}
		r := runs[keys[i]]
		switch {
		case r.last < 0, i > r.last && i != r.first:
			// Deltas with no part event after them: they are the text.
			out = append(out, ev)
		case i == r.first:
			if i == r.last {
				out = append(out, ev)
				continue
			}
			last := events[r.last]
			merged := ev
			if ev.Type == TypePartDelta {
				// A delta that comes first makes the part (Projection.delta):
				// a text part of its message, at the top of it.
				merged = Event{Seq: ev.Seq, At: ev.At, MessageID: ev.MessageID, PartID: ev.PartID}
			}
			merged.Type = TypePart
			merged.Kind, merged.State, merged.Text, merged.Content = last.Kind, last.State, last.Text, last.Content
			merged.Lite, merged.Held = last.Lite, max(merged.Held, last.Held)
			merged.UpdatedAt = last.At
			out = append(out, merged)
		}
	}
	return out
}

// stateKey names what an event sets that a later event of the same key sets
// again and nothing else reads in between, or "" for any other event.
func stateKey(ev Event) string {
	if ev.ClientID != "" {
		return ""
	}
	switch ev.Type {
	case TypeRateLimit:
		return TypeRateLimit + "\x00" + ev.Window
	case TypeTasks, TypeAgentInfo:
		return ev.Type
	case TypeUsage:
		if ev.Usage != nil {
			return ev.Type
		}
	}
	return ""
}
