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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte{0xff, 0xfb, 0x00})
	}))
	defer server.Close()
	client := New(server.URL, "key")
	audio, contentType, err := client.Speech(context.Background(), "tts", "alloy", "hi")
	if err != nil {
		t.Fatalf("speech: %v", err)
	}
	if len(audio) != 3 || contentType != "audio/mpeg" {
		t.Fatalf("audio = %v %q", audio, contentType)
	}
}
