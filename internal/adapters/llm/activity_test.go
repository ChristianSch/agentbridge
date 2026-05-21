package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ChristianSch/agentbridge/internal/config"
	"github.com/ChristianSch/agentbridge/internal/core"
)

func TestSummarizeOpenAICompatible(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization") == "Bearer key"
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "cheap" {
			t.Fatalf("model = %v", body["model"])
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Reading config.go."}}]}`))
	}))
	defer srv.Close()
	s := NewActivitySummarizer(config.ActivitySummaryConfig{Enabled: true, Provider: "openai", Endpoint: srv.URL, APIKey: "key", Model: "cheap"})
	got, err := s.SummarizeActivity(context.Background(), []core.ActivityFact{{Event: "tool_start", Tool: "read"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Reading config.go." {
		t.Fatalf("got %q", got)
	}
	if !sawAuth {
		t.Fatal("missing auth header")
	}
}

func TestSummarizeAnthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "key" {
			t.Fatal("missing anthropic key")
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"Running tests."}]}`))
	}))
	defer srv.Close()
	s := NewActivitySummarizer(config.ActivitySummaryConfig{Enabled: true, Provider: "anthropic", Endpoint: srv.URL, APIKey: "key", Model: "claude-3-5-haiku-latest"})
	got, err := s.SummarizeActivity(context.Background(), []core.ActivityFact{{Event: "tool_start", Tool: "bash"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Running tests." {
		t.Fatalf("got %q", got)
	}
}
