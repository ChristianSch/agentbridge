package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ChristianSch/agentbridge/internal/config"
	"github.com/ChristianSch/agentbridge/internal/core"
)

type activityNarrator struct {
	cfg        config.ActivitySummaryConfig
	summarizer core.ActivitySummarizer
	publish    func(core.AgentEvent)
	mu         sync.Mutex
	pending    map[string][]core.ActivityFact
	timers     map[string]*time.Timer
	lastThink  map[string]time.Time
	lastText   map[string]string
}

func newActivityNarrator(cfg config.ActivitySummaryConfig, summarizer core.ActivitySummarizer, publish func(core.AgentEvent)) *activityNarrator {
	if !cfg.Enabled {
		return nil
	}
	return &activityNarrator{cfg: cfg, summarizer: summarizer, publish: publish, pending: map[string][]core.ActivityFact{}, timers: map[string]*time.Timer{}, lastThink: map[string]time.Time{}, lastText: map[string]string{}}
}

func (n *activityNarrator) Observe(ev core.AgentEvent) {
	if n == nil || ev.SessionID == "" || ev.Event == "activity_summary" {
		return
	}
	fact, ok := activityFactFromEvent(ev)
	if !ok {
		return
	}
	if fact.Event == "thinking" && n.tooSoonForThinking(ev.SessionID) {
		return
	}
	if n.summarizer == nil {
		n.emit(ev.SessionID, deterministicActivitySummary(fact))
		return
	}
	n.mu.Lock()
	n.pending[ev.SessionID] = append(n.pending[ev.SessionID], fact)
	if len(n.pending[ev.SessionID]) > 12 {
		n.pending[ev.SessionID] = n.pending[ev.SessionID][len(n.pending[ev.SessionID])-12:]
	}
	if t := n.timers[ev.SessionID]; t != nil {
		t.Stop()
	}
	n.timers[ev.SessionID] = time.AfterFunc(n.delay(), func() { n.flush(ev.SessionID) })
	n.mu.Unlock()
}

func (n *activityNarrator) tooSoonForThinking(sessionID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	last := n.lastThink[sessionID]
	if time.Since(last) < 2500*time.Millisecond {
		return true
	}
	n.lastThink[sessionID] = time.Now()
	return false
}

func (n *activityNarrator) delay() time.Duration {
	if n.cfg.Debounce > 0 {
		return n.cfg.Debounce
	}
	return 900 * time.Millisecond
}

func (n *activityNarrator) flush(sessionID string) {
	n.mu.Lock()
	facts := append([]core.ActivityFact(nil), n.pending[sessionID]...)
	n.pending[sessionID] = nil
	n.mu.Unlock()
	if len(facts) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	text, err := n.summarizer.SummarizeActivity(ctx, facts)
	if err != nil || strings.TrimSpace(text) == "" {
		text = deterministicActivitySummary(facts[len(facts)-1])
	}
	n.emit(sessionID, text)
}

func (n *activityNarrator) emit(sessionID, text string) {
	text = cleanSummary(text)
	if text == "" {
		return
	}
	n.mu.Lock()
	if n.lastText[sessionID] == text {
		n.mu.Unlock()
		return
	}
	n.lastText[sessionID] = text
	n.mu.Unlock()
	n.publish(core.AgentEvent{Event: "activity_summary", SessionID: sessionID, Content: text})
}

func activityFactFromEvent(ev core.AgentEvent) (core.ActivityFact, bool) {
	switch ev.Event {
	case "thinking_delta":
		return core.ActivityFact{Event: "thinking", Text: "agent is reasoning internally"}, true
	case "tool_start":
		return core.ActivityFact{Event: "tool_start", Tool: ev.Tool, Args: safeActivityArgs(ev.Args)}, true
	case "tool_delta":
		return core.ActivityFact{Event: "tool_progress", Tool: ev.Tool, Text: truncateActivity(ev.Output + ev.Content)}, true
	case "tool_end":
		return core.ActivityFact{Event: "tool_end", Tool: ev.Tool, Text: truncateActivity(ev.Output)}, true
	case "approval_request":
		return core.ActivityFact{Event: "approval_request", Text: truncateActivity(ev.Prompt), Tool: ev.Tool}, true
	}
	return core.ActivityFact{}, false
}

func deterministicActivitySummary(f core.ActivityFact) string {
	tool := strings.TrimSpace(f.Tool)
	switch f.Event {
	case "thinking":
		return "Working through the next step."
	case "approval_request":
		return "Waiting for approval."
	case "tool_start":
		if s := toolStartSummary(tool, f.Args); s != "" {
			return s
		}
		return "Using " + displayTool(tool) + "."
	case "tool_progress":
		return "Getting results from " + displayTool(tool) + "."
	case "tool_end":
		return "Finished using " + displayTool(tool) + "."
	default:
		return "Working on it."
	}
}

func toolStartSummary(tool string, args map[string]any) string {
	name := strings.ToLower(tool)
	path := firstString(args, "path", "file", "filename")
	cmd := firstString(args, "command", "cmd")
	oldText := firstString(args, "oldText", "old_text")
	newText := firstString(args, "newText", "new_text")
	if strings.Contains(name, "read") && path != "" {
		return "Reading " + filepath.Base(path) + "."
	}
	if strings.Contains(name, "edit") && path != "" {
		return "Editing " + filepath.Base(path) + "."
	}
	if strings.Contains(name, "write") && path != "" {
		return "Writing " + filepath.Base(path) + "."
	}
	if strings.Contains(name, "bash") || strings.Contains(name, "shell") {
		if looksLikeTest(cmd) {
			return "Running tests."
		}
		if cmd != "" {
			return "Running a shell command."
		}
	}
	if oldText != "" || newText != "" {
		return "Applying a code change."
	}
	return ""
}

func displayTool(tool string) string {
	if strings.TrimSpace(tool) == "" {
		return "a tool"
	}
	return tool
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func looksLikeTest(cmd string) bool {
	cmd = strings.ToLower(cmd)
	return strings.Contains(cmd, "go test") || strings.Contains(cmd, "npm test") || strings.Contains(cmd, "pytest") || strings.Contains(cmd, "cargo test") || strings.Contains(cmd, "pnpm test") || strings.Contains(cmd, "yarn test")
}

func safeActivityArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"path", "file", "filename", "command", "cmd", "description"} {
		if s, ok := args[key].(string); ok && s != "" {
			out[key] = truncateActivity(s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cleanSummary(s string) string {
	s = strings.TrimSpace(strings.Trim(s, "`\"' \n\t"))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) > 140 {
		s = strings.TrimSpace(s[:140])
	}
	return s
}

func truncateActivity(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return s[:500]
	}
	return s
}
