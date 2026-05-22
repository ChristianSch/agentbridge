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
	ID         string       `json:"id"`
	Kind       AgentKind    `json:"kind"`
	Name       string       `json:"name"`
	Cwd        string       `json:"cwd"`
	State      SessionState `json:"state"`
	CreatedAt  time.Time    `json:"created_at"`
	LastActive time.Time    `json:"last_active"`
	OwnerID    string       `json:"owner_id,omitempty"`
}

type ActivityFact struct {
	Event string         `json:"event"`
	Tool  string         `json:"tool,omitempty"`
	Args  map[string]any `json:"args,omitempty"`
	Text  string         `json:"text,omitempty"`
}

type AttachmentKind string

const (
	AttachmentImage AttachmentKind = "image"
	AttachmentAudio AttachmentKind = "audio"
	AttachmentFile  AttachmentKind = "file"
	AttachmentText  AttachmentKind = "text"
)

type Attachment struct {
	ID            string         `json:"id"`
	Kind          AttachmentKind `json:"kind"`
	FileName      string         `json:"file_name"`
	MimeType      string         `json:"mime_type"`
	Size          int64          `json:"size"`
	Path          string         `json:"path,omitempty"`
	Content       string         `json:"content,omitempty"`
	ExtractedText string         `json:"extracted_text,omitempty"`
	Preview       string         `json:"preview,omitempty"`
}

type PromptPayload struct {
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type AgentEvent struct {
	Event       string         `json:"event"`
	SessionID   string         `json:"session_id,omitempty"`
	Content     string         `json:"content,omitempty"`
	Attachments []Attachment   `json:"attachments,omitempty"`
	Tool        string         `json:"tool,omitempty"`
	Args        map[string]any `json:"args,omitempty"`
	Output      string         `json:"output,omitempty"`
	State       SessionState   `json:"state,omitempty"`
	RequestID   string         `json:"request_id,omitempty"`
	Prompt      string         `json:"prompt,omitempty"`
	Command     string         `json:"command,omitempty"`
	Description string         `json:"description,omitempty"`
	Raw         map[string]any `json:"raw,omitempty"`
}

type ClientCommand struct {
	Action        string       `json:"action"`
	SessionID     string       `json:"session_id"`
	Text          string       `json:"text,omitempty"`
	AttachmentIDs []string     `json:"attachment_ids,omitempty"`
	Attachments   []Attachment `json:"attachments,omitempty"`
	RequestID     string       `json:"request_id,omitempty"`
	Approved      bool         `json:"approved,omitempty"`
}
