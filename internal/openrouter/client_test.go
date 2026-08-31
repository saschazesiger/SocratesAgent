package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatStreamingAccumulatesText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("missing auth header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ": OPENROUTER PROCESSING\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{"reasoning":"hmm "}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"Hello "}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"world"},"finish_reason":"stop"}],"usage":{"total_tokens":7}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(server.URL, "key")
	var streamed strings.Builder
	res, err := client.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		&StreamHandler{OnContent: func(d string) { streamed.WriteString(d) }})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "Hello world" || streamed.String() != "Hello world" {
		t.Fatalf("content = %q / streamed = %q", res.Content, streamed.String())
	}
	if res.Reasoning != "hmm " {
		t.Errorf("reasoning = %q", res.Reasoning)
	}
	if res.FinishReason != "stop" || res.Usage.TotalTokens != 7 {
		t.Errorf("finish/usage = %q %#v", res.FinishReason, res.Usage)
	}
}

func TestChatStreamingAssemblesSplitToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"delegate_to_agent","arguments":"{\"agent\":"}}]}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"codex\"}"}}]}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(server.URL, "key")
	var announced []string
	res, err := client.Chat(context.Background(), ChatRequest{Model: "m"},
		&StreamHandler{OnToolCall: func(name string) { announced = append(announced, name) }})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", res.ToolCalls)
	}
	call := res.ToolCalls[0]
	if call.ID != "call_a" || call.Function.Name != "delegate_to_agent" {
		t.Errorf("call = %#v", call)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not reassembled: %q", call.Function.Arguments)
	}
	if args["agent"] != "codex" {
		t.Errorf("args = %#v", args)
	}
	if len(announced) != 1 || announced[0] != "delegate_to_agent" {
		t.Errorf("announced = %#v", announced)
	}
}

func TestChatSurfacesAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		io.WriteString(w, `{"error":{"message":"not enough credits"}}`)
	}))
	defer server.Close()
	client := New(server.URL, "key")
	_, err := client.Chat(context.Background(), ChatRequest{Model: "m"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not enough credits") {
		t.Fatalf("err = %v", err)
	}
}

func TestChatWithoutKey(t *testing.T) {
	client := New("https://example.invalid", "")
	if _, err := client.Chat(context.Background(), ChatRequest{Model: "m"}, nil); err == nil {
		t.Fatal("expected an error without an API key")
	}
}

func TestTranscribeChatSendsAudioPart(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":" hello there "},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	client := New(server.URL, "key")
	text, err := client.TranscribeChat(context.Background(), "m", "transcribe", "QUJD", "wav")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if text != "hello there" {
		t.Fatalf("text = %q", text)
	}
	parts := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	audio := parts[1].(map[string]any)["input_audio"].(map[string]any)
	if audio["data"] != "QUJD" || audio["format"] != "wav" {
		t.Fatalf("audio part = %#v", audio)
	}
}

func TestSpeechReturnsAudio(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte{0xff, 0xfb, 0x00})
	}))
	defer server.Close()
	client := New(server.URL, "key")
	audio, contentType, err := client.Speech(context.Background(), "gpt-4o-mini-tts", "alloy", "hi", "Read it in German.")
	if err != nil {
		t.Fatalf("speech: %v", err)
	}
	if len(audio) != 3 || contentType != "audio/mpeg" {
		t.Fatalf("audio = %v %q", audio, contentType)
	}
	if body["instructions"] != "Read it in German." {
		t.Fatalf("instructions = %#v", body["instructions"])
	}
}

// The tts-1 family rejects instructions outright, and a rejected request is a
// silent answer - which is worse than an answer read with the wrong accent.
func TestSpeechKeepsInstructionsFromModelsThatRefuseThem(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte{0xff})
	}))
	defer server.Close()
	client := New(server.URL, "key")
	if _, _, err := client.Speech(context.Background(), "tts-1-hd", "alloy", "hi", "Read it in German."); err != nil {
		t.Fatalf("speech: %v", err)
	}
	if _, ok := body["instructions"]; ok {
		t.Fatalf("instructions were sent to tts-1-hd: %#v", body)
	}
}

// Naming the language up front is both faster and more accurate than letting
// the endpoint guess it from the first second of audio.
func TestTranscribeEndpointSendsTheLanguage(t *testing.T) {
	var fields map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse form: %v", err)
		}
		fields = map[string]string{}
		for key, values := range r.MultipartForm.Value {
			fields[key] = values[0]
		}
		io.WriteString(w, `{"text":" hallo "}`)
	}))
	defer server.Close()

	client := New(server.URL, "key")
	text, err := client.TranscribeEndpoint(context.Background(), "whisper-1", []byte("audio"), "audio.wav", "de")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if text != "hallo" {
		t.Fatalf("text = %q", text)
	}
	if fields["language"] != "de" || fields["model"] != "whisper-1" {
		t.Fatalf("fields = %#v", fields)
	}

	// Automatic sends no language at all, so the endpoint detects it itself.
	if _, err := client.TranscribeEndpoint(context.Background(), "whisper-1", []byte("audio"), "audio.wav", ""); err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if _, ok := fields["language"]; ok {
		t.Fatalf("language was sent for automatic: %#v", fields)
	}
}

// A dropped connection on the way to the model is not a failed answer, it is a
// failed attempt. As long as nothing has reached the screen, it is worth
// another try.
func TestChatRetriesATransientFailure(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"error":{"message":"upstream hiccup"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"eventually"},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := New(server.URL, "key")
	res, err := client.Chat(context.Background(),
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		&StreamHandler{OnContent: func(string) {}})
	if err != nil {
		t.Fatalf("chat should have survived two bad gateways: %v", err)
	}
	if res.Content != "eventually" {
		t.Fatalf("content = %q", res.Content)
	}
	if attempts != 3 {
		t.Fatalf("expected three attempts, got %d", attempts)
	}
}

// A rejected key is not going to start working, and repeating the request only
// makes the person wait longer to be told.
func TestChatDoesNotRetryARefusal(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer server.Close()

	client := New(server.URL, "key")
	_, err := client.Chat(context.Background(),
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err == nil {
		t.Fatal("expected the refusal to be reported")
	}
	if !strings.Contains(err.Error(), "bad key") {
		t.Errorf("the provider's reason should survive: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("a refusal must not be repeated, got %d attempts", attempts)
	}
}

// Once tokens have been shown, a second attempt would append a whole answer to
// half of one. The failure is reported instead.
func TestChatDoesNotRetryOnceTextHasBeenShown(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"half an "}}]}`+"\n\n")
		flusher.Flush()
		// Hang up without ever finishing the stream.
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("cannot simulate a dropped connection here")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer server.Close()

	client := New(server.URL, "key")
	var shown strings.Builder
	res, err := client.Chat(context.Background(),
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		&StreamHandler{OnContent: func(d string) { shown.WriteString(d) }})
	if attempts != 1 {
		t.Fatalf("expected a single attempt once text was shown, got %d", attempts)
	}
	// The stream ended early: either an error, or a result holding only the
	// half that arrived. Either way it must not have been said twice.
	if err == nil && res != nil && res.Content != "half an " {
		t.Fatalf("content = %q", res.Content)
	}
	if shown.String() != "half an " {
		t.Fatalf("the handler saw %q, so the answer was repeated", shown.String())
	}
}
