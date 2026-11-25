package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderFormatsAndPartialStream(t *testing.T) {
	tests := []struct {
		name, format, response, contentType, want string
		stream                                    bool
	}{
		{"chat", "openai-chat", `{"choices":[{"message":{"content":"chat ok"}}],"usage":{"prompt_tokens":12,"completion_tokens":4}}`, "application/json", "chat ok", false},
		{"responses", "openai-responses", `{"output":[{"type":"message","content":[{"type":"output_text","text":"responses ok"}]}],"usage":{"input_tokens":7,"output_tokens":3}}`, "application/json", "responses ok", false},
		{"anthropic", "anthropic", `{"content":[{"type":"text","text":"anthropic ok"}],"usage":{"input_tokens":9,"output_tokens":2}}`, "application/json", "anthropic ok", false},
		{"chat stream", "openai-chat", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", "text/event-stream", "partial", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.format == "anthropic" {
					if r.Header.Get("x-api-key") != "secret" {
						t.Error("missing x-api-key")
					}
				} else if r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("missing bearer")
				}
				expected := "/v1/messages"
				if tt.format == "openai-chat" {
					expected = "/v1/chat/completions"
				} else if tt.format == "openai-responses" {
					expected = "/v1/responses"
				}
				if r.URL.Path != expected {
					t.Errorf("path %s want %s", r.URL.Path, expected)
				}
				w.Header().Set("Content-Type", tt.contentType)
				fmt.Fprint(w, tt.response)
			}))
			defer server.Close()
			p := Profile{Format: tt.format, BaseURL: server.URL + "/v1", Model: "stub", MaxOutputTokens: 64, Stream: tt.stream}
			out, err := (HTTPProvider{}).Complete(context.Background(), p, "secret", []Message{{Role: "system", Content: "policy"}, {Role: "user", Content: "go"}})
			if tt.stream {
				var pe *ProviderError
				if !errors.As(err, &pe) || !pe.Partial || pe.Class != "stream_interrupted" {
					t.Fatalf("partial stream error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if out.Text != tt.want {
				t.Fatalf("text=%q", out.Text)
			}
			if !tt.stream && out.Usage.Estimated {
				t.Fatal("usage marked estimated")
			}
		})
	}
}

func TestProfileURLValidation(t *testing.T) {
	for _, raw := range []string{"https://user:pass@example.com/v1", "https://example.com/v1?token=x", "http://example.com/v1", "http://127.0.0.1:invalid/v1", "https://example.com/a/%2e%2e/b"} {
		if _, _, err := validateBaseURL(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if got, _, err := validateBaseURL("https://example.com/v1/"); err != nil || !strings.HasSuffix(got, "/v1") {
		t.Fatalf("valid URL: %q %v", got, err)
	}
}
