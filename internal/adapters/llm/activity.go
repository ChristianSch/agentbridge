package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ChristianSch/agentbridge/internal/config"
	"github.com/ChristianSch/agentbridge/internal/core"
)

const activityPrompt = "You narrate visible coding-agent activity. Summarize only observable tool/state events, never hidden reasoning. Return one short present-tense sentence, max 14 words. No markdown. No first-person thoughts; say what is happening."

type ActivitySummarizer struct {
	cfg    config.ActivitySummaryConfig
	client *http.Client
}

func NewActivitySummarizer(cfg config.ActivitySummaryConfig) core.ActivitySummarizer {
	if !cfg.Enabled || cfg.Model == "" {
		return nil
	}
	s := &ActivitySummarizer{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
	if !s.configured() {
		return nil
	}
	return s
}

func (s *ActivitySummarizer) SummarizeActivity(ctx context.Context, facts []core.ActivityFact) (string, error) {
	if s.provider() == "anthropic" {
		return s.summarizeAnthropic(ctx, facts)
	}
	return s.summarizeOpenAI(ctx, facts)
}

func (s *ActivitySummarizer) configured() bool {
	return s.endpoint() != "" && (s.provider() != "anthropic" || s.cfg.APIKey != "")
}

func (s *ActivitySummarizer) provider() string {
	if s.cfg.Provider != "" {
		return strings.ToLower(s.cfg.Provider)
	}
	if strings.Contains(strings.ToLower(s.cfg.Model), "claude") || strings.Contains(strings.ToLower(s.cfg.Endpoint), "anthropic") {
		return "anthropic"
	}
	return "openai"
}

func (s *ActivitySummarizer) endpoint() string {
	endpoint := strings.TrimRight(s.cfg.Endpoint, "/")
	if endpoint != "" {
		return endpoint
	}
	if s.provider() == "anthropic" {
		return "https://api.anthropic.com/v1/messages"
	}
	return "https://api.openai.com/v1"
}

func (s *ActivitySummarizer) summarizeOpenAI(ctx context.Context, facts []core.ActivityFact) (string, error) {
	endpoint := s.endpoint()
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint = strings.TrimRight(endpoint, "/") + "/chat/completions"
	}
	b, _ := json.Marshal(facts)
	body, _ := json.Marshal(map[string]any{
		"model": s.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": activityPrompt},
			{"role": "user", "content": string(b)},
		},
		"temperature": 0.2,
		"max_tokens":  40,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return "", fmt.Errorf("activity summary model returned %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("no choices")
	}
	return out.Choices[0].Message.Content, nil
}

func (s *ActivitySummarizer) summarizeAnthropic(ctx context.Context, facts []core.ActivityFact) (string, error) {
	b, _ := json.Marshal(facts)
	body, _ := json.Marshal(map[string]any{
		"model":      s.cfg.Model,
		"system":     activityPrompt,
		"max_tokens": 40,
		"messages":   []map[string]string{{"role": "user", "content": string(b)}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", s.cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	res, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return "", fmt.Errorf("activity summary model returned %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", err
	}
	for _, c := range out.Content {
		if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
			return c.Text, nil
		}
	}
	return "", fmt.Errorf("no text content")
}
