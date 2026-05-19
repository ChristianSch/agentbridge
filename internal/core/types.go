package core

import "time"

type AgentKind string

const (
	AgentPi       AgentKind = "pi"
	AgentHermes   AgentKind = "hermes"
	AgentTerminal AgentKind = "terminal"
)

type SessionState string

const (
	StateStarting        SessionState = "starting"
	StateIdle            SessionState = "idle"
	StateRunning         SessionState = "running"
	StateWaitingForInput SessionState = "waiting_for_input"
	StateExited          SessionState = "exited"
	StateError           SessionState = "error"
)

type Session struct {
	ID        string       `json:"id"`
	Kind      AgentKind    `json:"kind"`
	Name      string       `json:"name"`
	Cwd       string       `json:"cwd"`
	State     SessionState `json:"state"`
	CreatedAt time.Time    `json:"created_at"`
}

type AgentEvent struct {
	Event     string         `json:"event"`
	SessionID string         `json:"session_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	Output    string         `json:"output,omitempty"`
	State     SessionState   `json:"state,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
	Prompt    string         `json:"prompt,omitempty"`
	Raw       map[string]any `json:"raw,omitempty"`
}

type ClientCommand struct {
	Action    string `json:"action"`
	SessionID string `json:"session_id"`
	Text      string `json:"text,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Approved  bool   `json:"approved,omitempty"`
}
