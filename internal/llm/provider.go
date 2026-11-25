package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	Estimated        bool  `json:"estimated"`
}
type Completion struct {
	Text       string        `json:"text"`
	Usage      Usage         `json:"usage"`
	FirstToken time.Duration `json:"-"`
	ToolState  bool          `json:"-"`
}
type ProviderError struct {
	Class      string
	Status     int
	RetryAfter time.Duration
	Partial    bool
	Cause      error
}

func (e *ProviderError) Error() string { return e.Class }
func (e *ProviderError) Unwrap() error { return e.Cause }

type HTTPProvider struct{ Client *http.Client }

func (p HTTPProvider) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (p HTTPProvider) Complete(ctx context.Context, profile Profile, credential string, messages []Message) (Completion, error) {
	if len(messages) == 0 {
		return Completion{}, ErrInvalid
	}
	endpoint, body, err := providerRequest(profile, messages)
	if err != nil {
		return Completion{}, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Completion{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return Completion{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream, application/json")
	if profile.Format == "anthropic" {
		request.Header.Set("x-api-key", credential)
		request.Header.Set("anthropic-version", "2023-06-01")
	} else {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	if profile.SessionHeaderName != "" {
		request.Header.Set(profile.SessionHeaderName, "tianfa")
	}
	started := time.Now()
	response, err := p.client().Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return Completion{}, &ProviderError{Class: "cancelled", Cause: err}
		}
		return Completion{}, &ProviderError{Class: "transport", Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return Completion{}, &ProviderError{Class: classifyStatus(response.StatusCode), Status: response.StatusCode, RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	}
	reader := io.LimitReader(response.Body, 8<<20)
	if profile.Stream && strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		out, err := readSSE(reader, profile.Format, started)
		if err != nil {
			return out, &ProviderError{Class: "stream_interrupted", Partial: out.Text != "" || out.ToolState, Cause: err}
		}
		return out, nil
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return Completion{}, &ProviderError{Class: "transport", Cause: err}
	}
	return parseResponse(profile.Format, raw)
}

func providerRequest(profile Profile, messages []Message) (string, map[string]any, error) {
	base := strings.TrimSuffix(profile.BaseURL, "/")
	if _, _, err := validateBaseURL(base); err != nil {
		return "", nil, err
	}
	switch profile.Format {
	case "openai-chat":
		body := map[string]any{"model": profile.Model, "messages": messages, "max_completion_tokens": profile.MaxOutputTokens, "stream": profile.Stream}
		if profile.Stream {
			body["stream_options"] = map[string]any{"include_usage": true}
		}
		if profile.ReasoningEffort != "" {
			body["reasoning_effort"] = profile.ReasoningEffort
		}
		return base + "/chat/completions", body, nil
	case "openai-responses":
		body := map[string]any{"model": profile.Model, "input": messages, "max_output_tokens": profile.MaxOutputTokens, "stream": profile.Stream}
		if profile.ReasoningEffort != "" {
			body["reasoning"] = map[string]any{"effort": profile.ReasoningEffort}
		}
		return base + "/responses", body, nil
	case "anthropic":
		var system []string
		var turns []Message
		for _, m := range messages {
			if m.Role == "system" {
				system = append(system, m.Content)
			} else {
				turns = append(turns, m)
			}
		}
		if len(turns) == 0 {
			return "", nil, ErrInvalid
		}
		body := map[string]any{"model": profile.Model, "system": strings.Join(system, "\n\n"), "messages": turns, "max_tokens": profile.MaxOutputTokens, "stream": profile.Stream}
		return base + "/messages", body, nil
	}
	return "", nil, ErrInvalid
}

func classifyStatus(status int) string {
	switch status {
	case 400, 413, 422:
		return "invalid_request"
	case 401, 403:
		return "auth"
	case 402:
		return "quota"
	case 404:
		return "model_not_found"
	case 408, 429:
		return "rate_limit"
	}
	if status >= 500 {
		return "provider_unavailable"
	}
	return "provider_error"
}
func retryAfter(value string) time.Duration {
	if n, err := time.ParseDuration(value + "s"); err == nil && n > 0 && n <= 24*time.Hour {
		return n
	}
	if date, err := http.ParseTime(value); err == nil {
		d := time.Until(date)
		if d > 0 && d <= 24*time.Hour {
			return d
		}
	}
	return 0
}

func parseResponse(format string, raw []byte) (Completion, error) {
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil {
		return Completion{}, &ProviderError{Class: "invalid_response"}
	}
	var out Completion
	out.Usage.Estimated = true
	switch format {
	case "openai-chat":
		var choices []struct {
			Message struct {
				Content   string          `json:"content"`
				ToolCalls json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		}
		_ = json.Unmarshal(data["choices"], &choices)
		if len(choices) > 0 {
			out.Text = choices[0].Message.Content
			out.ToolState = len(choices[0].Message.ToolCalls) > 2
		}
	case "openai-responses":
		_ = json.Unmarshal(data["output_text"], &out.Text)
		if out.Text == "" {
			var output []struct {
				Type    string `json:"type"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			_ = json.Unmarshal(data["output"], &output)
			for _, item := range output {
				if strings.Contains(item.Type, "call") {
					out.ToolState = true
				}
				for _, c := range item.Content {
					out.Text += c.Text
				}
			}
		}
	case "anthropic":
		var content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(data["content"], &content)
		for _, part := range content {
			if part.Type == "tool_use" {
				out.ToolState = true
			}
			out.Text += part.Text
		}
	}
	out.Usage = parseUsage(format, data["usage"])
	if out.Text == "" && !out.ToolState {
		return out, &ProviderError{Class: "invalid_response"}
	}
	return out, nil
}
func parseUsage(format string, raw json.RawMessage) Usage {
	var values map[string]json.RawMessage
	_ = json.Unmarshal(raw, &values)
	read := func(key string) int64 { var n int64; _ = json.Unmarshal(values[key], &n); return n }
	var u Usage
	if format == "anthropic" {
		u.InputTokens = read("input_tokens")
		u.OutputTokens = read("output_tokens")
		u.CacheReadTokens = read("cache_read_input_tokens")
		u.CacheWriteTokens = read("cache_creation_input_tokens")
	} else {
		u.InputTokens = read("prompt_tokens")
		u.OutputTokens = read("completion_tokens")
		if format == "openai-responses" {
			u.InputTokens = read("input_tokens")
			u.OutputTokens = read("output_tokens")
		}
		var detail map[string]json.RawMessage
		_ = json.Unmarshal(values["prompt_tokens_details"], &detail)
		_ = json.Unmarshal(detail["cached_tokens"], &u.CacheReadTokens)
		_ = json.Unmarshal(values["input_tokens_details"], &detail)
		_ = json.Unmarshal(detail["cached_tokens"], &u.CacheReadTokens)
		_ = json.Unmarshal(values["output_tokens_details"], &detail)
		_ = json.Unmarshal(detail["reasoning_tokens"], &u.ReasoningTokens)
	}
	u.Estimated = len(raw) == 0 || string(raw) == "null"
	return u
}

func readSSE(reader io.Reader, format string, started time.Time) (Completion, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	var out Completion
	out.Usage.Estimated = true
	var dataLines []string
	done := false
	consume := func() {
		if len(dataLines) == 0 {
			return
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = nil
		if payload == "[DONE]" {
			done = true
			return
		}
		var event map[string]json.RawMessage
		if json.Unmarshal([]byte(payload), &event) != nil {
			return
		}
		var kind string
		_ = json.Unmarshal(event["type"], &kind)
		var delta string
		switch format {
		case "openai-chat":
			var choices []struct {
				Delta struct {
					Content   string          `json:"content"`
					ToolCalls json.RawMessage `json:"tool_calls"`
				} `json:"delta"`
			}
			_ = json.Unmarshal(event["choices"], &choices)
			if len(choices) > 0 {
				delta = choices[0].Delta.Content
				if len(choices[0].Delta.ToolCalls) > 2 {
					out.ToolState = true
				}
			}
			if len(event["usage"]) > 2 {
				out.Usage = parseUsage(format, event["usage"])
			}
		case "openai-responses":
			if kind == "response.output_text.delta" {
				_ = json.Unmarshal(event["delta"], &delta)
			}
			if kind == "response.output_item.added" {
				out.ToolState = out.ToolState || bytes.Contains(event["item"], []byte("call"))
			}
			if kind == "response.completed" {
				var response map[string]json.RawMessage
				_ = json.Unmarshal(event["response"], &response)
				out.Usage = parseUsage(format, response["usage"])
				done = true
			}
		case "anthropic":
			if kind == "message_start" {
				var message map[string]json.RawMessage
				_ = json.Unmarshal(event["message"], &message)
				out.Usage = parseUsage(format, message["usage"])
			}
			if kind == "content_block_delta" {
				var block map[string]json.RawMessage
				_ = json.Unmarshal(event["delta"], &block)
				_ = json.Unmarshal(block["text"], &delta)
			}
			if kind == "content_block_start" {
				out.ToolState = out.ToolState || bytes.Contains(event["content_block"], []byte("tool_use"))
			}
			if kind == "message_delta" {
				update := parseUsage(format, event["usage"])
				if update.OutputTokens > 0 {
					out.Usage.OutputTokens = update.OutputTokens
				}
				out.Usage.Estimated = false
			}
			if kind == "message_stop" {
				done = true
			}
		}
		if delta != "" {
			if out.FirstToken == 0 {
				out.FirstToken = time.Since(started)
			}
			out.Text += delta
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			consume()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	consume()
	if err := scanner.Err(); err != nil {
		return out, err
	}
	if !done {
		return out, fmt.Errorf("stream ended before completion")
	}
	if out.Text == "" && !out.ToolState {
		return out, fmt.Errorf("stream produced no content")
	}
	return out, nil
}
