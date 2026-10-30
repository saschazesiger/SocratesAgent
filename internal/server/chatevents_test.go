//go:build linux

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/agent"
)

// getEvents is GET /api/chats/{id}/events as a browser polls it.
func (c *chatEnv) getEvents(t *testing.T, client *http.Client, id, query string) (int, chatEventsAnswer) {
	t.Helper()
	req, err := http.NewRequest("GET", c.server.URL+"/api/chats/"+id+"/events"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var answer chatEventsAnswer
	if res.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &answer); err != nil {
			t.Fatalf("events%s: %v: %s", query, err, raw)
		}
	}
	return res.StatusCode, answer
}

// The chat's events over a plain request - the stream's slower path - are
// what the stream replays: for the chat's owner only, from a seq on, a page
// at a time, and a page after the end is empty.
func TestAChatsEventsArePolledAPageAtATime(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	id := c.create(t)
	c.send(t, id, "m1", "first")
	c.send(t, id, "m2", "second")
	journal := c.waitJournal(t, id, 15*time.Second, "two answers", turnsEnded(2))
	last := journal[len(journal)-1].Seq

	if code, _ := c.getEvents(t, c.anon, id, "?since=0"); code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d, want 401", code)
	}
	if code, _ := c.getEvents(t, c.client, "0123456789abcdef0123456789abcdef", "?since=0"); code != http.StatusNotFound {
		t.Fatalf("a chat that does not exist: %d, want 404", code)
	}
	for _, bad := range []string{"?since=x", "?since=-1", "?limit=0"} {
		if code, _ := c.getEvents(t, c.client, id, bad); code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", bad, code)
		}
	}

	code, all := c.getEvents(t, c.client, id, "?since=0")
	if code != http.StatusOK || all.More || all.Seq != last || all.LastSeq < last || len(all.Events) == 0 {
		t.Fatalf("the whole chat: %d seq %d last %d more %v, %d events; journal ends at %d", code, all.Seq, all.LastSeq, all.More, len(all.Events), last)
	}
	said := func(events []agent.Event) string {
		p := agent.Fold(events)
		var words []string
		for _, m := range p.Messages {
			words = append(words, m.Role+":"+m.ClientID)
		}
		return strings.Join(words, " ")
	}
	if got := said(all.Events); !strings.Contains(got, "user:m1") || !strings.Contains(got, "user:m2") {
		t.Fatalf("the polled chat says %q", got)
	}

	// Light, as a light stream's replay: the same chat.
	if code, light := c.getEvents(t, c.client, id, "?since=0&lite=1"); code != http.StatusOK || said(light.Events) != said(all.Events) {
		t.Fatalf("light: %d, says %q", code, said(light.Events))
	}

	// From a seq on: only what came after it.
	mid := journal[len(journal)/2].Seq
	_, after := c.getEvents(t, c.client, id, "?since="+itoa(mid))
	for _, ev := range after.Events {
		if ev.Seq <= mid {
			t.Fatalf("an event at %d came after since=%d", ev.Seq, mid)
		}
	}
	if after.Seq != last || after.More {
		t.Fatalf("after %d: seq %d more %v", mid, after.Seq, after.More)
	}

	// A page at a time, each one on from the last, to the same chat.
	var paged []agent.Event
	since, pages := int64(0), 0
	for {
		code, page := c.getEvents(t, c.client, id, "?limit=3&since="+itoa(since))
		if code != http.StatusOK {
			t.Fatalf("page %d: %d", pages, code)
		}
		pages++
		if page.Seq <= since && page.More {
			t.Fatalf("page %d did not move on from %d", pages, since)
		}
		paged = append(paged, page.Events...)
		since = page.Seq
		if !page.More {
			break
		}
		if pages > len(journal) {
			t.Fatal("the pages never end")
		}
	}
	if pages < 2 || since != last {
		t.Fatalf("%d pages, ending at %d of %d", pages, since, last)
	}
	if a, b := said(paged), said(all.Events); a != b {
		t.Fatalf("paged the chat says %q, whole %q", a, b)
	}

	// At the end: nothing, and the same seq.
	_, end := c.getEvents(t, c.client, id, "?since="+itoa(last))
	if len(end.Events) != 0 || end.Seq != last || end.More {
		t.Fatalf("at the end: %d events, seq %d, more %v", len(end.Events), end.Seq, end.More)
	}
}

func itoa(n int64) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}
