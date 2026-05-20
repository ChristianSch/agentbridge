package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/ChristianSch/agentbridge/internal/core"
)

type HermesAdapter struct{ seq atomic.Uint64 }

func NewHermesAdapter() *HermesAdapter { return &HermesAdapter{} }

func (*HermesAdapter) Kind() core.AgentKind { return core.AgentHermes }

func (*HermesAdapter) BuildCommand(cfg core.AgentConfig) (string, []string, []string, error) {
	python := "python3"
	venvCandidates := []string{
		cfg.HermesVenv,
		filepath.Join(cfg.Cwd, "venv"),
		filepath.Join(cfg.Cwd, ".venv"),
		filepath.Join(cfg.HermesCwd, "venv"),
		filepath.Join(cfg.HermesCwd, ".venv"),
	}
	for _, venv := range venvCandidates {
		if venv == "" {
			continue
		}
		candidate := filepath.Join(venv, "bin", "python")
		if _, err := os.Stat(candidate); err == nil {
			python = candidate
			break
		}
	}
	module := cfg.HermesModule
	if module == "" {
		module = "tui_gateway.entry"
	}
	env := []string{}
	for _, p := range []string{cfg.Cwd, cfg.HermesCwd} {
		if p == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(p, "tui_gateway")); err == nil && st.IsDir() {
			env = append(env, "PYTHONPATH="+p)
			break
		}
	}
	return python, []string{"-m", module}, env, nil
}

func (h *HermesAdapter) SendPrompt(text string) ([]byte, error) {
	return h.req("prompt.submit", map[string]any{"text": text})
}
func (h *HermesAdapter) SendSteer(text string) ([]byte, error) {
	return h.req("session.steer", map[string]any{"text": text})
}
func (h *HermesAdapter) SendAbort() ([]byte, error) {
	return h.req("session.interrupt", map[string]any{})
}
func (h *HermesAdapter) SendFollowUp(text string) ([]byte, error) { return h.SendPrompt(text) }
func (h *HermesAdapter) SendCompact() ([]byte, error) {
	return h.req("session.compress", map[string]any{})
}
func (h *HermesAdapter) SendApproval(id string, approved bool) ([]byte, error) {
	return h.req("approval.respond", map[string]any{"id": id, "approved": approved})
}

func (h *HermesAdapter) req(method string, params any) ([]byte, error) {
	id := fmt.Sprintf("%d", h.seq.Add(1))
	return line(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func (h *HermesAdapter) InitialMessages(cfg core.AgentConfig) ([][]byte, error) {
	method := "session.create"
	params := map[string]any{"cols": 120}
	if cfg.HermesResumeID != "" {
		method = "session.resume"
		params = map[string]any{"session_id": cfg.HermesResumeID}
	}
	b, err := h.req(method, params)
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

func (*HermesAdapter) ParseEvent(b []byte) (*core.AgentEvent, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m["method"] != "event" {
		if errObj, ok := m["error"].(map[string]any); ok {
			return &core.AgentEvent{Event: "error", Content: str(errObj["message"]), Raw: m}, nil
		}
		if _, ok := m["result"].(map[string]any); ok {
			return nil, nil
		}
		return &core.AgentEvent{Event: "rpc", Raw: m}, nil
	}
	params, _ := m["params"].(map[string]any)
	payload, _ := params["payload"].(map[string]any)
	switch str(params["type"]) {
	case "message.start":
		return &core.AgentEvent{Event: "state_change", State: core.StateRunning, Raw: m}, nil
	case "message.delta":
		return &core.AgentEvent{Event: "delta", Content: str(payload["text"]), Raw: m}, nil
	case "message.complete", "message.end":
		return &core.AgentEvent{Event: "state_change", State: core.StateIdle, Raw: m}, nil
	case "turn.end":
		return &core.AgentEvent{Event: "state_change", State: core.StateIdle, Raw: m}, nil
	case "thinking.delta", "reasoning.delta":
		if str(payload["text"]) == "" {
			return nil, nil
		}
		return &core.AgentEvent{Event: "thinking_delta", Content: str(payload["text"]), Raw: m}, nil
	case "tool.generating":
		return &core.AgentEvent{Event: "tool_delta", Tool: str(payload["name"]), Content: "generating tool call", Raw: m}, nil
	case "tool.progress":
		return &core.AgentEvent{Event: "tool_delta", Tool: str(payload["name"]), Output: str(payload["preview"]), Raw: m}, nil
	case "tool.start":
		return &core.AgentEvent{Event: "tool_start", Tool: str(payload["name"]), Args: payload, Raw: m}, nil
	case "tool.complete":
		return &core.AgentEvent{Event: "tool_end", Tool: str(payload["name"]), Output: str(payload["summary"]), Raw: m}, nil
	case "tool.end":
		return &core.AgentEvent{Event: "tool_end", Tool: str(payload["name"]), Output: str(payload["result"]), Raw: m}, nil
	case "state":
		return &core.AgentEvent{Event: "state_change", State: core.SessionState(str(params["state"])), Raw: m}, nil
	case "session.info":
		return &core.AgentEvent{Event: "state_change", State: core.StateIdle, Raw: m}, nil
	case "approval.request":
		prompt := str(params["prompt"])
		if prompt == "" {
			prompt = str(payload["description"])
		}
		if prompt == "" {
			prompt = str(payload["command"])
		}
		return &core.AgentEvent{Event: "approval_request", RequestID: str(params["id"]), Prompt: prompt, Command: str(payload["command"]), Description: str(payload["description"]), Args: payload, Raw: m}, nil
	default:
		return &core.AgentEvent{Event: "raw", Raw: m}, nil
	}
}
