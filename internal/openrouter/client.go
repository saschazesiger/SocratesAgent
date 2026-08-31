// Package openrouter is a small client for the OpenRouter API: streaming chat
// completions with tool calling, the model catalogue, plus the audio helpers
// Socrates needs for voice mode.
package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
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
	Pricing       struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
}

// Models fetches the catalogue for the admin model pickers.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.send(ctx, http.MethodGet, "/models", nil, "", nil)
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
	// Some gateways nest input modalities under architecture.
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

// TranscribeChat converts speech to text by handing the audio to a multimodal
// chat model. OpenRouter has no dedicated transcription endpoint, but every
// audio capable model accepts an input_audio content part.
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
// /audio/transcriptions endpoint (Whisper and friends).
func (c *Client) TranscribeEndpoint(ctx context.Context, model string, audio []byte, filename string) (string, error) {
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
func (c *Client) Speech(ctx context.Context, model, voice, text string) ([]byte, string, error) {
	payload := map[string]any{
		"model":           model,
		"voice":           voice,
		"input":           text,
		"response_format": "mp3",
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
