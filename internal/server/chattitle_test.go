//go:build linux

package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/store"
	"github.com/saschazesiger/SocratesAgent/internal/termux"
)

// withGateway points the chat server's OpenRouter client at a stand-in.
func (c *chatEnv) withGateway(t *testing.T) *scriptedGateway {
	t.Helper()
	gw := newGateway(t)
	c.configureOpenRouter(t, map[string]any{
		"base_url": gw.srv.URL, "api_key": "k", "status_model": "test/status-model",
		"title_model": "test/title-model", "home_model": "test/home-model",
	})
	return gw
}

// waitTitle waits for a chat's stored name and where it came from.
func (c *chatEnv) waitTitle(t *testing.T, id, want string) *store.Session {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		row, err := c.store.GetSession(id)
		if err != nil {
			t.Fatal(err)
		}
		if row.Title == want {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("the chat is called %q, want %q", row.Title, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A chat names itself once, from its first message, through the namer every
// session has; a later message does not rename it.
func TestAChatIsNamedFromItsFirstMessage(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	gw.always("Config loader rename")
	id := c.create(t)
	c.send(t, id, "m1", "Please rename the config loader")
	row := c.waitTitle(t, id, "Config loader rename")
	if row.TitleSource != store.TitleAuto {
		t.Fatalf("title source %q", row.TitleSource)
	}
	calls := gw.seen()
	if len(calls) != 1 || calls[0].Model != "test/title-model" ||
		!strings.Contains(calls[0].last(), "First message:\nPlease rename the config loader") ||
		!strings.Contains(calls[0].last(), "Scripted agent") {
		t.Fatalf("the namer was asked %+v", calls)
	}
	c.waitJournal(t, id, 10*time.Second, "the first turn", turnsEnded(1))
	gw.always("Something else")
	c.send(t, id, "m2", "Now the tests")
	c.waitJournal(t, id, 10*time.Second, "the second turn", turnsEnded(2))
	time.Sleep(300 * time.Millisecond)
	if row, _ := c.store.GetSession(id); row.Title != "Config loader rename" || gw.count() != 1 {
		t.Fatalf("renamed by a later message: %q after %d calls", row.Title, gw.count())
	}
}

// A name the person chose is never replaced by the namer.
func TestAChatNamedByThePersonKeepsItsName(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	gw.always("Not this")
	code, body := c.post(t, "/api/chats", map[string]any{"client_id": "named", "harness": "fake",
		"workdir_mode": "custom", "workdir": c.data, "title": "My own name"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["session"].(map[string]any)["id"].(string)
	c.send(t, id, "m1", "hello")
	c.waitJournal(t, id, 10*time.Second, "the turn", turnsEnded(1))
	time.Sleep(300 * time.Millisecond)
	if row, _ := c.store.GetSession(id); row.Title != "My own name" || gw.count() != 0 {
		t.Fatalf("%q after %d calls", row.Title, gw.count())
	}
}

// Naming a chat again on request reads its journal: the first message and
// where the conversation has got to, and never what its composer holds.
func TestAChatIsNamedAgainFromItsJournal(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	gw.always("First name")
	id := c.create(t)
	c.send(t, id, "m1", "Set up the database")
	c.waitTitle(t, id, "First name")
	c.waitJournal(t, id, 10*time.Second, "the first turn", turnsEnded(1))
	c.send(t, id, "m2", "Now write the migrations")
	c.waitJournal(t, id, 10*time.Second, "the second turn", turnsEnded(2))
	if res, _ := c.do(t, c.client, "PUT", "/api/sessions/"+id+"/draft", `{"text":"half typed","base_rev":0}`); res.StatusCode != http.StatusOK {
		t.Fatalf("draft: %d", res.StatusCode)
	}
	gw.always("Database migrations")
	res, body := c.do(t, c.client, "POST", "/api/sessions/"+id+"/title", `{}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("retitle: %d %v", res.StatusCode, body)
	}
	c.waitTitle(t, id, "Database migrations")
	calls := gw.seen()
	last := calls[len(calls)-1].last()
	if !strings.Contains(last, "First message:\nSet up the database") || !strings.Contains(last, "Now write the migrations") ||
		strings.Contains(last, "half typed") {
		t.Fatalf("the namer was asked:\n%s", last)
	}
}

// The rename dialog's suggestion is a name from the journal that is saved
// nowhere: the person decides in the field whether it is the name.
func TestARenameSuggestionSavesNothing(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	gw.always("First name")
	id := c.create(t)
	c.send(t, id, "m1", "Set up the database")
	c.waitTitle(t, id, "First name")
	c.waitJournal(t, id, 10*time.Second, "the first turn", turnsEnded(1))
	gw.always("Database setup")
	res, body := c.do(t, c.client, "POST", "/api/sessions/"+id+"/title", `{"suggest":true}`)
	if res.StatusCode != http.StatusOK || body["title"] != "Database setup" {
		t.Fatalf("suggest: %d %v", res.StatusCode, body)
	}
	if row, _ := c.store.GetSession(id); row.Title != "First name" {
		t.Fatalf("a suggestion renamed the chat to %q", row.Title)
	}
	if last := gw.seen()[len(gw.seen())-1].last(); !strings.Contains(last, "First message:\nSet up the database") {
		t.Fatalf("the namer was asked:\n%s", last)
	}
}

// With no key there is nothing to ask, and the placeholder name stays. It is
// silent on purpose: nobody pressed anything, and nothing is spent, so the
// chat may still be named once a key is set.
func TestAChatsTitleRunIsSilentWithoutAKey(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	c.configureOpenRouter(t, map[string]any{"api_key": ""})
	id := c.create(t)
	before, _ := c.store.GetSession(id)
	c.send(t, id, "m1", "hello")
	c.waitJournal(t, id, 10*time.Second, "the turn", turnsEnded(1))
	time.Sleep(300 * time.Millisecond)
	row, _ := c.store.GetSession(id)
	if gw.count() != 0 || row.Title != before.Title || row.TitleSource != "" {
		t.Fatalf("%d calls, the chat is %q (%q)", gw.count(), row.Title, row.TitleSource)
	}
}

// An agent busy before anybody has sent it anything is not taking up a first
// message: nothing is asked and nothing is spent, and the first message
// afterwards still names the chat.
func TestAChatsStartUpIsNoFirstMessage(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	gw.always("Too early")
	id := c.create(t)
	c.srv.titles.observe(id, termux.Activity{State: termux.StateBusy})
	c.srv.titles.observe(id, termux.Activity{State: termux.StateIdle})
	time.Sleep(300 * time.Millisecond)
	if row, _ := c.store.GetSession(id); gw.count() != 0 || row.TitleSource != "" {
		t.Fatalf("%d calls before a message, title source %q", gw.count(), row.TitleSource)
	}
	gw.always("Fix failing parser test")
	c.send(t, id, "m1", "The parser test fails")
	c.waitTitle(t, id, "Fix failing parser test")
}

// A gateway that refuses is not a silence: every browser hears why, the row
// stops pulsing, and the one go is spent; so is it on an answer with nothing
// usable in it.
func TestAChatsTitleRunReportsAFailureAndSpendsItsTurn(t *testing.T) {
	c := newChatEnv(t, time.Hour)
	gw := c.withGateway(t)
	gw.refuse(http.StatusUnauthorized, "User not found.")
	client := c.dialEvents(t)
	client.hello()
	id := c.create(t)
	before, _ := c.store.GetSession(id)
	c.send(t, id, "m1", "hello")
	client.await("a failed title frame", 10*time.Second, func() bool {
		frames := titleFrames(client, id)
		return len(frames) == 2 && frames[0]["phase"] == titlePhaseStart &&
			frames[1]["phase"] == titlePhaseError && strings.Contains(fmt.Sprint(frames[1]["text"]), "API key")
	})
	if row, _ := c.store.GetSession(id); row.TitleSource != store.TitleAuto || row.Title != before.Title {
		t.Fatalf("after a refusal the chat is %q (%q)", row.Title, row.TitleSource)
	}

	gw.refuse(0, "")
	gw.always("   \n  ")
	code, body := c.post(t, "/api/chats", map[string]any{"client_id": "create-other", "harness": "fake",
		"workdir_mode": "custom", "workdir": c.data})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}
	other := body["session"].(map[string]any)["id"].(string)
	before, _ = c.store.GetSession(other)
	c.send(t, other, "m1", "hello")
	deadline := time.Now().Add(10 * time.Second)
	for row, _ := c.store.GetSession(other); row.TitleSource != store.TitleAuto; row, _ = c.store.GetSession(other) {
		if time.Now().After(deadline) {
			t.Fatalf("the empty answer did not spend the turn: %q", row.TitleSource)
		}
		time.Sleep(20 * time.Millisecond)
	}
	calls := gw.count()
	c.waitJournal(t, other, 10*time.Second, "the first turn", turnsEnded(1))
	c.send(t, other, "m2", "again")
	c.waitJournal(t, other, 10*time.Second, "the second turn", turnsEnded(2))
	time.Sleep(300 * time.Millisecond)
	if row, _ := c.store.GetSession(other); row.Title != before.Title || gw.count() != calls {
		t.Fatalf("the chat is %q after %d calls, %d before the second message", row.Title, gw.count(), calls)
	}
}
