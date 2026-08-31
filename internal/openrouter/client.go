// Package openrouter is a small client for the OpenRouter API: streaming chat
// completions with tool calling, the model catalogue, plus the audio helpers
// Socrates needs for voice mode.
package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client talks to an OpenAI compatible endpoint.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New builds a client. baseURL may be empty, in which case OpenRouter is used.
func New(baseURL, apiKey string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  strings.TrimSpace(apiKey),
		HTTP:    &http.Client{Timeout: 0},
	}
}

// Message is one chat message in OpenAI format. Content is either a plain
// string or a slice of content parts (used for audio input).
type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a function call requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall carries the name and the raw JSON arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool describes a callable function.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the schema of a tool.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ChatRequest is the payload of a chat completion.
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	ToolChoice  any       `json:"tool_choice,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	Usage       *struct {
		Include bool `json:"include"`
	} `json:"usage,omitempty"`
}

// Usage reports token consumption.
type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float64 `json:"cost"`
}

// Result is the outcome of a (streamed) completion.
type Result struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
	Model        string
}

// StreamHandler receives incremental output while a completion is running.
type StreamHandler struct {
	OnContent   func(delta string)
	OnReasoning func(delta string)
	OnToolCall  func(name string)
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("HTTP-Referer", "https://github.com/saschazesiger/SocratesAgent")
	req.Header.Set("X-Title", "SocratesAgent")
	return req, nil
}

// StatusError is a failure the provider reported with an HTTP status, as
// opposed to the connection never getting there at all. Keeping the status
// around is what lets the retry logic tell "rate limited, try again" from
// "your key is wrong, do not bother".
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string { return e.Message }

func apiError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	msg := strings.TrimSpace(string(body))
	var wrapped struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &wrapped) == nil && wrapped.Error.Message != "" {
		msg = wrapped.Error.Message
	}
	if msg == "" {
		msg = resp.Status
	}
	return &StatusError{Status: resp.StatusCode, Message: fmt.Sprintf("%s: %s", resp.Status, msg)}
}

// retryable reports whether another attempt has a real chance of working. A
// dropped connection or a mobile network that came back a second later is worth
// repeating; a rejected key is not.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var status *StatusError
	if errors.As(err, &status) {
		switch status.Status {
		case http.StatusRequestTimeout, http.StatusTooManyRequests,
			http.StatusInternalServerError, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	// Anything that is not a status is a transport problem: DNS, TCP, TLS, a
	// connection reset halfway through. Those are exactly the ones worth
	// repeating.
	return true
}

// maxAttempts is how often a transient failure is repeated before giving up.
const maxAttempts = 3

// backoff waits before attempt n, or gives up early if the caller does.
func backoff(ctx context.Context, attempt int) error {
	delay := time.Duration(1<<uint(attempt-1)) * 700 * time.Millisecond
	if delay > 5*time.Second {
		delay = 5 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// send performs a request, repeating it when the failure looks temporary. The
// body is passed as bytes rather than a reader precisely so that it can be
// replayed on the next attempt.
func (c *Client) send(ctx context.Context, method, path string, body []byte, contentType string, decorate func(*http.Request)) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if err := backoff(ctx, attempt-1); err != nil {
				return nil, lastErr
			}
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := c.newRequest(ctx, method, path, reader, contentType)
		if err != nil {
			return nil, err
		}
		if decorate != nil {
			decorate(req)
		}
		resp, err := c.HTTP.Do(req)
		if err == nil && resp.StatusCode < 300 {
			return resp, nil
		}
		if err == nil {
			err = apiError(resp)
			resp.Body.Close()
		}
		lastErr = err
		if ctx.Err() != nil || !retryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// Chat runs a completion. When h is non nil the request is streamed and the
// handler is called with deltas as they arrive.
func (c *Client) Chat(ctx context.Context, req ChatRequest, h *StreamHandler) (*Result, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("no API key configured - open /admin and add your OpenRouter key")
	}
	req.Stream = h != nil
	if req.Stream {
		req.Usage = &struct {
			Include bool `json:"include"`
		}{Include: true}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	decorate := func(r *http.Request) {
		if req.Stream {
			r.Header.Set("Accept", "text/event-stream")
		}
	}

	// A completion is retried while nothing has reached the screen yet. Once
	// the first token has been shown, a second attempt would append a whole
	// answer to half of one, so the failure is reported instead.
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if err := backoff(ctx, attempt-1); err != nil {
				return nil, lastErr
			}
		}
		res, emitted, err := c.chatOnce(ctx, body, req.Stream, h, decorate)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if ctx.Err() != nil || emitted || !retryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// chatOnce is one attempt at a completion. It reports whether the handler
// already produced visible output, because that is what makes an attempt
// impossible to repeat.
func (c *Client) chatOnce(ctx context.Context, body []byte, stream bool, h *StreamHandler, decorate func(*http.Request)) (*Result, bool, error) {
	var reader io.Reader = bytes.NewReader(body)
	httpReq, err := c.newRequest(ctx, http.MethodPost, "/chat/completions", reader, "application/json")
	if err != nil {
		return nil, false, err
	}
	decorate(httpReq)
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, false, apiError(resp)
	}
	if !stream {
		res, err := parseCompletion(resp.Body)
		return res, false, err
	}
	emitted := false
	guarded := &StreamHandler{}
	if h != nil {
		guarded.OnContent = func(delta string) {
			emitted = true
			if h.OnContent != nil {
				h.OnContent(delta)
			}
		}
		guarded.OnReasoning = func(delta string) {
			emitted = true
			if h.OnReasoning != nil {
				h.OnReasoning(delta)
			}
		}
		guarded.OnToolCall = func(name string) {
			emitted = true
			if h.OnToolCall != nil {
				h.OnToolCall(name)
			}
		}
	}
	res, err := parseStream(resp.Body, guarded)
	return res, emitted, err
}

type completionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string     `json:"content"`
			Reasoning string     `json:"reasoning"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func parseCompletion(r io.Reader) (*Result, error) {
	var cr completionResponse
	if err := json.NewDecoder(r).Decode(&cr); err != nil {
		return nil, fmt.Errorf("decode completion: %w", err)
	}
	if cr.Error != nil && cr.Error.Message != "" {
		return nil, fmt.Errorf("%s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("model returned no choices")
	}
	ch := cr.Choices[0]
	return &Result{
		Content:      ch.Message.Content,
		Reasoning:    ch.Message.Reasoning,
		ToolCalls:    ch.Message.ToolCalls,
		FinishReason: ch.FinishReason,
		Usage:        cr.Usage,
		Model:        cr.Model,
	}, nil
}

type streamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func parseStream(r io.Reader, h *StreamHandler) (*Result, error) {
	res := &Result{}
	type partialCall struct {
		id, name  string
		args      strings.Builder
		announced bool
	}
	calls := map[int]*partialCall{}
	order := []int{}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue // keepalive comment
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return nil, fmt.Errorf("%s", chunk.Error.Message)
		}
		if chunk.Model != "" {
			res.Model = chunk.Model
		}
		if chunk.Usage != nil {
			res.Usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.FinishReason != "" {
			res.FinishReason = ch.FinishReason
		}
		if ch.Delta.Content != "" {
			res.Content += ch.Delta.Content
			if h != nil && h.OnContent != nil {
				h.OnContent(ch.Delta.Content)
			}
		}
		if ch.Delta.Reasoning != "" {
			res.Reasoning += ch.Delta.Reasoning
			if h != nil && h.OnReasoning != nil {
				h.OnReasoning(ch.Delta.Reasoning)
			}
		}
		for _, tc := range ch.Delta.ToolCalls {
			pc, ok := calls[tc.Index]
			if !ok {
				pc = &partialCall{}
				calls[tc.Index] = pc
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				pc.id = tc.ID
			}
			if tc.Function.Name != "" {
				pc.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				pc.args.WriteString(tc.Function.Arguments)
			}
			if !pc.announced && pc.name != "" && h != nil && h.OnToolCall != nil {
				pc.announced = true
				h.OnToolCall(pc.name)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}
	for _, idx := range order {
		pc := calls[idx]
		if pc.name == "" {
			continue
		}
		id := pc.id
		if id == "" {
			id = fmt.Sprintf("call_%d_%d", idx, time.Now().UnixNano())
		}
		res.ToolCalls = append(res.ToolCalls, ToolCall{
			ID:       id,
			Type:     "function",
			Function: FunctionCall{Name: pc.name, Arguments: pc.args.String()},
		})
	}
	return res, nil
}

// Model is one entry of the OpenRouter catalogue.
type Model struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	ContextLength int      `json:"context_length"`
	Modalities    []string `json:"input_modalities"`
	Produces      []string `json:"output_modalities"`
	Pricing       struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
}

// UnmarshalJSON reads both shapes of the catalogue: OpenRouter nests the
// modalities under "architecture", other OpenAI compatible gateways put them at
// the top level. Reading only the top level leaves every entry with no modality
// at all, and a picker with nothing to filter on offers the whole catalogue -
// which is how a model that cannot listen ends up chosen for listening.
func (m *Model) UnmarshalJSON(data []byte) error {
	type plain Model
	var flat plain
	if err := json.Unmarshal(data, &flat); err != nil {
		return err
	}
	var nested struct {
		Architecture struct {
			Input  []string `json:"input_modalities"`
			Output []string `json:"output_modalities"`
		} `json:"architecture"`
	}
	_ = json.Unmarshal(data, &nested)
	*m = Model(flat)
	if len(m.Modalities) == 0 {
		m.Modalities = nested.Architecture.Input
	}
	if len(m.Produces) == 0 {
		m.Produces = nested.Architecture.Output
	}
	return nil
}

// Hears reports whether the model accepts audio at all.
func (m Model) Hears() bool { return hasModality(m.Modalities, "audio") }

// Transcribes reports whether this is a dedicated transcription model. Those
// answer on /audio/transcriptions and refuse /chat/completions outright, so
// telling the two apart is what decides where a recording is sent.
func (m Model) Transcribes() bool { return hasModality(m.Produces, "transcription") }

func hasModality(list []string, want string) bool {
	for _, item := range list {
		if strings.EqualFold(item, want) {
			return true
		}
	}
	return false
}

// Models fetches the catalogue for the admin model pickers.
//
// OpenRouter keeps the dedicated transcription models out of its main list, so
// both are fetched and merged: without the second one the transcription picker
// cannot offer whisper and friends at all, and a chat model is the only thing
// left to pick for a job it may not do.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	models, err := c.catalogue(ctx, "/models")
	if err != nil {
		return nil, err
	}
	// Best effort. A gateway that does not know the filter answers with its
	// whole list, which the merge drops as duplicates, or with an error, which
	// is no reason to leave the dashboard without any models at all.
	if extra, err := c.catalogue(ctx, "/models?output_modalities=transcription"); err == nil {
		seen := make(map[string]bool, len(models))
		for _, m := range models {
			seen[m.ID] = true
		}
		for _, m := range extra {
			if !seen[m.ID] {
				models = append(models, m)
			}
		}
	}
	c.rememberRoutes(models)
	return models, nil
}

func (c *Client) catalogue(ctx context.Context, path string) ([]Model, error) {
	resp, err := c.send(ctx, http.MethodGet, path, nil, "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []Model `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// KeyInfo is the response of the /key endpoint, used by the "test" button.
type KeyInfo struct {
	Label      string   `json:"label"`
	Usage      float64  `json:"usage"`
	Limit      *float64 `json:"limit"`
	IsFreeTier bool     `json:"is_free_tier"`
}

// CheckKey verifies the configured API key.
func (c *Client) CheckKey(ctx context.Context) (*KeyInfo, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("no API key configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := c.send(ctx, http.MethodGet, "/key", nil, "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data KeyInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// Two kinds of model turn a recording into words, and each is served by its
// own endpoint: a dedicated transcription model (audio -> transcription)
// answers on /audio/transcriptions, an audio capable chat model on
// /chat/completions. Sending one to the other's endpoint is a flat refusal -
// "openai/gpt-transcribe is a transcription model and cannot be used with the
// chat/completions endpoint" - and no id says reliably which kind it is:
// deepgram/nova-3 and google/chirp-3 transcribe, google/gemini-2.5-flash
// chats. So the route is learned: from the catalogue when the dashboard has
// fetched it, from the refusal itself otherwise, and remembered either way, so
// a recording is uploaded twice at most once per model.
var transcribeRoutes sync.Map // baseURL + "\n" + model -> bool (dedicated endpoint)

func (c *Client) routeKey(model string) string { return c.BaseURL + "\n" + model }

// rememberRoutes records what the catalogue says, so the first recording after
// a visit to the dashboard already goes to the right place.
func (c *Client) rememberRoutes(models []Model) {
	for _, m := range models {
		if m.Hears() {
			transcribeRoutes.Store(c.routeKey(m.ID), m.Transcribes())
		}
	}
}

// route is the endpoint to try first for a model.
func (c *Client) route(model string) bool {
	if known, ok := transcribeRoutes.Load(c.routeKey(model)); ok {
		return known.(bool)
	}
	return looksLikeTranscriber(model)
}

// looksLikeTranscriber is the guess for a model nobody has asked about yet. It
// only has to be right often enough to save an upload: being wrong costs the
// second attempt that Transcribe makes anyway.
func looksLikeTranscriber(model string) bool {
	id := strings.ToLower(model)
	for _, hint := range []string{"transcribe", "whisper", "-asr", "-stt", "speech-to-text"} {
		if strings.Contains(id, hint) {
			return true
		}
	}
	return false
}

// wrongEndpoint reports whether a refusal was about where the request went
// rather than about the request itself. OpenRouter says so in words, both ways
// round: a transcription model on /chat/completions is "a transcription model
// and cannot be used with the chat/completions endpoint", a chat model on
// /audio/transcriptions "does not exist". Neither sentence is a contract, but
// reading one wrong only ever costs one more attempt.
func wrongEndpoint(err error) bool {
	var status *StatusError
	if !errors.As(err, &status) {
		return false
	}
	if status.Status != http.StatusBadRequest && status.Status != http.StatusNotFound {
		return false
	}
	msg := strings.ToLower(status.Message)
	for _, hint := range []string{
		"transcription model", "does not exist", "no endpoints found",
		"not a valid model", "audio/transcriptions", "chat/completions",
	} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

// Transcribe turns a recording into text with whichever kind of model is
// configured. prompt steers an audio capable chat model; a dedicated
// transcription model is given the language instead, which is what it
// understands.
func (c *Client) Transcribe(ctx context.Context, model, prompt string, audio []byte, format, language string) (string, error) {
	if strings.TrimSpace(model) == "" {
		return "", fmt.Errorf("no transcription model configured - open /admin and pick one")
	}
	dedicated := c.route(model)
	text, err := c.transcribeVia(ctx, dedicated, model, prompt, audio, format, language)
	if err == nil {
		transcribeRoutes.Store(c.routeKey(model), dedicated)
		return text, nil
	}
	if !wrongEndpoint(err) {
		return "", err
	}
	// The gateway says the model lives at the other endpoint. Believe it, and
	// remember it, so the next recording goes straight there.
	text, retry := c.transcribeVia(ctx, !dedicated, model, prompt, audio, format, language)
	if retry != nil {
		return "", retry
	}
	transcribeRoutes.Store(c.routeKey(model), !dedicated)
	return text, nil
}

func (c *Client) transcribeVia(ctx context.Context, dedicated bool, model, prompt string, audio []byte, format, language string) (string, error) {
	if dedicated {
		return c.TranscribeEndpoint(ctx, model, audio, "audio."+format, language)
	}
	return c.TranscribeChat(ctx, model, prompt, base64.StdEncoding.EncodeToString(audio), format)
}

// TranscribeChat converts speech to text by handing the audio to a multimodal
// chat model, which accepts it as an input_audio content part.
func (c *Client) TranscribeChat(ctx context.Context, model, prompt, audioB64, format string) (string, error) {
	msg := Message{
		Role: "user",
		Content: []any{
			map[string]any{"type": "text", "text": prompt},
			map[string]any{"type": "input_audio", "input_audio": map[string]string{
				"data": audioB64, "format": format,
			}},
		},
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res, err := c.Chat(ctx, ChatRequest{Model: model, Messages: []Message{msg}}, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Content), nil
}

// TranscribeEndpoint posts the audio to an OpenAI compatible
// /audio/transcriptions endpoint (Whisper and friends). language is an
// ISO 639-1 code, or empty to let the endpoint detect it - naming it up front
// is both faster and more accurate, because the model no longer has to guess
// from the first second of audio.
func (c *Client) TranscribeEndpoint(ctx context.Context, model string, audio []byte, filename, language string) (string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(audio); err != nil {
		return "", err
	}
	if model != "" {
		_ = mw.WriteField("model", model)
	}
	if language = strings.TrimSpace(language); language != "" {
		_ = mw.WriteField("language", language)
	}
	_ = mw.WriteField("response_format", "json")
	if err := mw.Close(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	resp, err := c.send(ctx, http.MethodPost, "/audio/transcriptions", buf.Bytes(), mw.FormDataContentType(), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.Text), nil
}

// Speech calls an OpenAI compatible /audio/speech endpoint and returns the
// audio bytes together with their content type.
//
// instructions steer the delivery - which language to read in, and with which
// accent. They are only sent when the model understands them: the original
// tts-1 family rejects the field outright, and a rejected request is a silent
// answer, which is worse than an answer read with the wrong accent.
func (c *Client) Speech(ctx context.Context, model, voice, text, instructions string) ([]byte, string, error) {
	payload := map[string]any{
		"model":           model,
		"voice":           voice,
		"input":           text,
		"response_format": "mp3",
	}
	if instructions = strings.TrimSpace(instructions); instructions != "" && !strings.HasPrefix(model, "tts-1") {
		payload["instructions"] = instructions
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	resp, err := c.send(ctx, http.MethodPost, "/audio/speech", body, "application/json", nil)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	audio, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, "", err
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "audio/mpeg"
	}
	return audio, ct, nil
}
