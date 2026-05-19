package agent

import (
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/ChristianSch/agentbridge/internal/core"
)

type PiAdapter struct{}

func NewPiAdapter() PiAdapter { return PiAdapter{} }

func (PiAdapter) Kind() core.AgentKind { return core.AgentPi }

func (PiAdapter) BuildCommand(cfg core.AgentConfig) (string, []string, []string, error) {
	bin := cfg.PiBinary
	if bin == "" {
		bin = "pi"
	}
	// Pi uses the process working directory. Current pi releases do not expose
	// a --cwd flag, so AgentBridge sets cmd.Dir in the process manager.
	args := []string{"--mode", "rpc"}
	if cfg.PiResumeID != "" {
		args = append(args, "--session", cfg.PiResumeID)
	}
	return bin, args, nil, nil
}

func (PiAdapter) SendPrompt(text string) ([]byte, error) {
	return line(map[string]any{"id": newReqID(), "type": "prompt", "message": text})
}
func (PiAdapter) SendSteer(text string) ([]byte, error) {
	return line(map[string]any{"id": newReqID(), "type": "steer", "message": text})
}
func (PiAdapter) SendAbort() ([]byte, error) {
	return line(map[string]any{"id": newReqID(), "type": "abort"})
}
func (PiAdapter) SendFollowUp(text string) ([]byte, error) {
	return line(map[string]any{"id": newReqID(), "type": "follow_up", "message": text})
}
func (PiAdapter) SendCompact() ([]byte, error) {
	return line(map[string]any{"id": newReqID(), "type": "compact"})
}
func (PiAdapter) SendApproval(id string, approved bool) ([]byte, error) {
	return line(map[string]any{"type": "extension_ui_response", "id": id, "confirmed": approved})
}

func (PiAdapter) InitialMessages(core.AgentConfig) ([][]byte, error) {
	b, err := line(map[string]any{"id": newReqID(), "type": "get_state"})
	if err != nil {
		return nil, err
	}
	return [][]byte{b}, nil
}

func (PiAdapter) ParseEvent(b []byte) (*core.AgentEvent, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	typ := str(m["type"])
	switch typ {
	case "response":
		if ok, _ := m["success"].(bool); !ok {
			return &core.AgentEvent{Event: "error", Content: str(m["error"]), Raw: m}, nil
		}
		if str(m["command"]) == "get_state" {
			data, _ := m["data"].(map[string]any)
			state := core.StateIdle
			if streaming, _ := data["isStreaming"].(bool); streaming {
				state = core.StateRunning
			}
			return &core.AgentEvent{Event: "state_change", State: state, Raw: m}, nil
		}
		return nil, nil
	case "agent_start", "turn_start":
		return &core.AgentEvent{Event: "state_change", State: core.StateRunning, Raw: m}, nil
	case "agent_end", "turn_end":
		return &core.AgentEvent{Event: "state_change", State: core.StateIdle, Raw: m}, nil
	case "message_update":
		ame, _ := m["assistantMessageEvent"].(map[string]any)
		switch str(ame["type"]) {
		case "text_delta":
			return &core.AgentEvent{Event: "delta", Content: str(ame["delta"]), Raw: m}, nil
		case "thinking_delta", "reasoning_delta":
			return &core.AgentEvent{Event: "thinking_delta", Content: str(ame["delta"]), Raw: m}, nil
		case "toolcall_delta":
			partial, _ := ame["partial"].(map[string]any)
			return &core.AgentEvent{Event: "tool_delta", Tool: toolNameFromPartial(partial), Content: str(ame["delta"]), Raw: m}, nil
		case "toolcall_end":
			call, _ := ame["toolCall"].(map[string]any)
			return &core.AgentEvent{Event: "tool_start", Tool: str(call["name"]), Args: mapFromAny(call["arguments"]), Raw: m}, nil
		case "tool_start":
			return &core.AgentEvent{Event: "tool_start", Tool: str(ame["name"]), Raw: m}, nil
		case "tool_end":
			return &core.AgentEvent{Event: "tool_end", Tool: str(ame["name"]), Output: stringify(ame), Raw: m}, nil
		default:
			return nil, nil
		}
	case "message_start":
		return nil, nil
	case "message_end":
		return nil, nil
	case "tool_call":
		return &core.AgentEvent{Event: "tool_start", Tool: str(m["name"]), Raw: m}, nil
	case "tool_result":
		return &core.AgentEvent{Event: "tool_end", Tool: str(m["name"]), Output: str(m["output"]), Raw: m}, nil
	case "tool_execution_start":
		return &core.AgentEvent{Event: "tool_start", Tool: str(m["toolName"]), Args: mapFromAny(m["args"]), Raw: m}, nil
	case "tool_execution_update":
		return &core.AgentEvent{Event: "tool_delta", Tool: str(m["toolName"]), Output: toolResultText(m["partialResult"]), Raw: m}, nil
	case "tool_execution_end":
		return &core.AgentEvent{Event: "tool_end", Tool: str(m["toolName"]), Output: toolResultText(m["result"]), Raw: m}, nil
	case "state":
		return &core.AgentEvent{Event: "state_change", State: core.SessionState(str(m["state"])), Raw: m}, nil
	case "extension_ui_request":
		return parseExtensionUI(m), nil
	default:
		return &core.AgentEvent{Event: typ, Raw: m}, nil
	}
}

func parseExtensionUI(m map[string]any) *core.AgentEvent {
	prompt := str(m["message"])
	if prompt == "" {
		prompt = str(m["title"])
	}
	return &core.AgentEvent{Event: "approval_request", RequestID: str(m["id"]), Prompt: prompt, Raw: m}
}

func line(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func stringify(v any) string { b, _ := json.Marshal(v); return string(b) }
func mapFromAny(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}
func toolNameFromPartial(partial map[string]any) string {
	content, _ := partial["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	first, _ := content[0].(map[string]any)
	return str(first["name"])
}
func toolResultText(v any) string {
	m, _ := v.(map[string]any)
	content, _ := m["content"].([]any)
	out := ""
	for _, item := range content {
		im, _ := item.(map[string]any)
		out += str(im["text"])
	}
	if out != "" {
		return out
	}
	return stringify(v)
}
func newReqID() string { return fmt.Sprintf("ab-%d", nextReqID()) }

var reqCounter atomic.Uint64

func nextReqID() uint64 { return reqCounter.Add(1) }
