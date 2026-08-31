package openrouter

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
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
	audio, contentType, err := client.Speech(context.Background(), "deepgram/aura-2", "aura-2-thalia-en", "hi", "Read it in German.")
	if err != nil {
		t.Fatalf("speech: %v", err)
	}
	if len(audio) != 3 || contentType != "audio/mpeg" {
		t.Fatalf("audio = %v %q", audio, contentType)
	}
	if body["instructions"] != "Read it in German." {
		t.Fatalf("instructions = %#v", body["instructions"])
	}
	if body["voice"] != "aura-2-thalia-en" || body["response_format"] != "mp3" {
		t.Fatalf("request = %#v", body)
	}
}

// Without a model there is nothing to ask, and saying so here costs nothing.
func TestSpeechNeedsAModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the provider was called with no model at all")
	}))
	defer server.Close()
	client := New(server.URL, "key")
	if _, _, err := client.Speech(context.Background(), "", "alloy", "hi", ""); err == nil {
		t.Fatal("a missing model was accepted")
	}
}

// Whether a voice is needed is the model's business. The fish-audio family
// reads without one, and refusing on its behalf is how a model that works ends
// up never being asked - so the field is left out and the model decides. An
// empty string is not the same thing: every model rejects that outright.
func TestSpeechLeavesAnEmptyVoiceOutOfTheRequest(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte{0xff, 0xfb, 0x00})
	}))
	defer server.Close()
	client := New(server.URL, "key")
	if _, _, err := client.Speech(context.Background(), "fish-audio/s1", "  ", "hi", ""); err != nil {
		t.Fatalf("a model that needs no voice was refused: %v", err)
	}
	if _, sent := body["voice"]; sent {
		t.Fatalf("an empty voice was sent anyway: %#v", body)
	}
	if body["model"] != "fish-audio/s1" {
		t.Fatalf("request = %#v", body)
	}
}

// A model that does want a voice says so itself, and that sentence is what the
// dashboard needs to show. It must arrive whole rather than as a generic
// failure, because it is the only thing that says what to fix.
func TestSpeechPassesOnTheProvidersOwnComplaint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"An explicit voice is required for this TTS provider."}}`)
	}))
	defer server.Close()
	client := New(server.URL, "key")
	_, _, err := client.Speech(context.Background(), "hexgrad/kokoro-82m", "", "hi", "")
	if err == nil {
		t.Fatal("the refusal was swallowed")
	}
	if !strings.Contains(err.Error(), "explicit voice is required") {
		t.Fatalf("error = %q", err)
	}
}

// Gemini's TTS renders PCM and nothing else, and says so instead of rendering
// an mp3. The refusal is worth exactly one more attempt - and the samples that
// come back are worth the forty four bytes that let a browser play them.
func TestSpeechFallsBackToPCMAndWrapsItInWAV(t *testing.T) {
	formats := []string{}
	samples := []byte{0x01, 0x00, 0xff, 0x7f}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		format, _ := body["response_format"].(string)
		formats = append(formats, format)
		if format != "pcm" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Gemini TTS only supports response_format=\"pcm\". Got \"mp3\"."}}`)
			return
		}
		w.Header().Set("Content-Type", "audio/pcm;rate=24000;channels=1")
		_, _ = w.Write(samples)
	}))
	defer server.Close()
	client := New(server.URL, "key")

	audio, contentType, err := client.Speech(context.Background(), "google/gemini-3.1-flash-tts-preview", "Zephyr", "hi", "")
	if err != nil {
		t.Fatalf("speech: %v", err)
	}
	if contentType != "audio/wav" {
		t.Fatalf("content type = %q", contentType)
	}
	if len(audio) != 44+len(samples) || string(audio[:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		t.Fatalf("not a wav: %v", audio)
	}
	if !bytes.Equal(audio[44:], samples) {
		t.Fatalf("samples were changed: %v", audio[44:])
	}
	if got := binary.LittleEndian.Uint32(audio[24:28]); got != 24000 {
		t.Fatalf("sample rate = %d", got)
	}
	if got := binary.LittleEndian.Uint16(audio[22:24]); got != 1 {
		t.Fatalf("channels = %d", got)
	}
	if want := []string{"mp3", "pcm"}; !slices.Equal(formats, want) {
		t.Fatalf("formats = %v", formats)
	}

	// The refusal is remembered, so the second answer is rendered once.
	formats = formats[:0]
	if _, _, err := client.Speech(context.Background(), "google/gemini-3.1-flash-tts-preview", "Zephyr", "again", ""); err != nil {
		t.Fatalf("speech: %v", err)
	}
	if want := []string{"pcm"}; !slices.Equal(formats, want) {
		t.Fatalf("the model was asked for mp3 again: %v", formats)
	}
}

// A refusal that is not about the format is the answer, not a reason to pay
// for a second rendering of the same sentence.
func TestSpeechReportsARealRefusalOnce(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"No endpoints found for vendor/nope."}}`)
	}))
	defer server.Close()
	client := New(server.URL, "key")
	if _, _, err := client.Speech(context.Background(), "vendor/nope", "alloy", "hi", ""); err == nil {
		t.Fatal("the refusal was swallowed")
	}
	if calls != 1 {
		t.Fatalf("the request was sent %d times", calls)
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

// fakeGateway stands in for OpenRouter, which serves the two kinds of
// transcription model on two different endpoints and refuses each at the
// other's - the refusal this test is really about.
func fakeGateway(t *testing.T, transcription string, chat string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/audio/transcriptions":
			if transcription == "" {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":{"message":"Model m does not exist"}}`)
				return
			}
			io.WriteString(w, `{"text":"`+transcription+`"}`)
		case "/chat/completions":
			if chat == "" {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":{"message":"m is a transcription model and cannot be used with the chat/completions endpoint. Use the /api/v1/audio/transcriptions endpoint instead."}}`)
				return
			}
			io.WriteString(w, `{"choices":[{"message":{"content":"`+chat+`"},"finish_reason":"stop"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

// A transcription model refuses /chat/completions outright. Sending a
// recording there was the whole of the "502" the microphone used to answer
// with, so the refusal has to be read and the other endpoint used.
func TestTranscribeFallsBackToTheTranscriptionEndpoint(t *testing.T) {
	server, seen := fakeGateway(t, "hallo zusammen", "")
	client := New(server.URL, "key")

	text, err := client.Transcribe(context.Background(), "vendor/listen-1", "verbatim", []byte("audio"), "wav", "de")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if text != "hallo zusammen" {
		t.Fatalf("text = %q", text)
	}
	if len(*seen) != 2 || (*seen)[0] != "/chat/completions" || (*seen)[1] != "/audio/transcriptions" {
		t.Fatalf("requests = %v", *seen)
	}

	// And what worked is remembered, so the next recording is uploaded once.
	if _, err := client.Transcribe(context.Background(), "vendor/listen-1", "verbatim", []byte("audio"), "wav", "de"); err != nil {
		t.Fatalf("second transcribe: %v", err)
	}
	if len(*seen) != 3 || (*seen)[2] != "/audio/transcriptions" {
		t.Fatalf("requests = %v", *seen)
	}
}

// The same the other way round: a name that reads like a transcriber but is an
// audio capable chat model still gets its recording transcribed.
func TestTranscribeFallsBackToTheChatEndpoint(t *testing.T) {
	server, seen := fakeGateway(t, "", "guten Morgen")
	client := New(server.URL, "key")

	text, err := client.Transcribe(context.Background(), "vendor/whisper-chat", "verbatim", []byte("audio"), "wav", "de")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if text != "guten Morgen" {
		t.Fatalf("text = %q", text)
	}
	if len(*seen) != 2 || (*seen)[0] != "/audio/transcriptions" || (*seen)[1] != "/chat/completions" {
		t.Fatalf("requests = %v", *seen)
	}
}

// A refusal that is not about the endpoint is reported as it is, rather than
// costing a second upload of the same recording.
func TestTranscribeDoesNotRetryARealRefusal(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"No auth credentials found"}}`)
	}))
	defer server.Close()

	client := New(server.URL, "key")
	_, err := client.Transcribe(context.Background(), "vendor/model", "verbatim", []byte("audio"), "wav", "de")
	if err == nil || !strings.Contains(err.Error(), "No auth credentials") {
		t.Fatalf("err = %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("requests = %v", seen)
	}
}

// The catalogue is what the dashboard filters on. OpenRouter nests the
// modalities under "architecture" and keeps the transcription models out of
// the main list, so both have to be read - otherwise every picker offers every
// model and a model that cannot listen ends up transcribing.
// The models that only listen and the models that only speak are kept out of
// OpenRouter's main list, so a catalogue that is not merged leaves both the
// transcription and the voice picker with nothing to offer.
func TestModelsMergeTheListeningAndSpeakingCatalogues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("output_modalities") {
		case "transcription":
			io.WriteString(w, `{"data":[{"id":"vendor/listen-1","name":"Listen",
				"architecture":{"input_modalities":["audio"],"output_modalities":["transcription"]}}]}`)
		case "speech":
			io.WriteString(w, `{"data":[
				{"id":"vendor/speak-1","name":"Speak","supported_voices":["lara-de","thalia-en"],
				 "architecture":{"input_modalities":["text"],"output_modalities":["speech"]}},
				{"id":"vendor/chat-1","name":"Chat","architecture":{"input_modalities":["text"],"output_modalities":["text"]}}]}`)
		default:
			io.WriteString(w, `{"data":[
				{"id":"vendor/chat-1","name":"Chat","architecture":{"input_modalities":["text","audio"],"output_modalities":["text"]}},
				{"id":"vendor/text-1","name":"Text","architecture":{"input_modalities":["text"],"output_modalities":["text"]}}]}`)
		}
	}))
	defer server.Close()

	client := New(server.URL, "key")
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	// Four entries, not five: chat-1 comes back from two of the three lists
	// and must not be offered twice.
	if len(models) != 4 {
		t.Fatalf("models = %#v", models)
	}
	byID := map[string]Model{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if !byID["vendor/listen-1"].Hears() || !byID["vendor/listen-1"].Transcribes() {
		t.Errorf("listen-1 = %#v", byID["vendor/listen-1"])
	}
	if !byID["vendor/chat-1"].Hears() || byID["vendor/chat-1"].Transcribes() || byID["vendor/chat-1"].Speaks() {
		t.Errorf("chat-1 = %#v", byID["vendor/chat-1"])
	}
	if byID["vendor/text-1"].Hears() {
		t.Errorf("text-1 = %#v", byID["vendor/text-1"])
	}
	speaker := byID["vendor/speak-1"]
	if !speaker.Speaks() || speaker.Hears() {
		t.Errorf("speak-1 = %#v", speaker)
	}
	// The voices travel with the model: they are what the dashboard offers
	// next, and no other request lists them.
	if !slices.Equal(speaker.Voices, []string{"lara-de", "thalia-en"}) {
		t.Errorf("voices = %#v", speaker.Voices)
	}
}
