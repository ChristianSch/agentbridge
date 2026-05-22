package core

import (
	"context"
	"io"
)

// AgentAdapter is the protocol port implemented by Pi, Hermes, and future agents.
type AgentAdapter interface {
	Kind() AgentKind
	BuildCommand(cfg AgentConfig) (name string, args []string, env []string, err error)
	SendPrompt(payload PromptPayload) ([]byte, error)
	SendSteer(payload PromptPayload) ([]byte, error)
	SendAbort() ([]byte, error)
	SendFollowUp(payload PromptPayload) ([]byte, error)
	SendCompact() ([]byte, error)
	SendApproval(id string, approved bool) ([]byte, error)
	InitialMessages(cfg AgentConfig) ([][]byte, error)
	ParseEvent(line []byte) (*AgentEvent, error)
}

type AgentConfig struct {
	SessionName    string
	Cwd            string
	PiBinary       string
	PiResumeID     string
	HermesVenv     string
	HermesModule   string
	HermesCwd      string
	HermesResumeID string
}

type EventSink interface {
	Publish(event AgentEvent)
}

type AttachmentMeta struct {
	SessionID string
	FileName  string
	MimeType  string
	Size      int64
}

type AttachmentStore interface {
	Save(ctx context.Context, r io.Reader, meta AttachmentMeta) (Attachment, error)
	Get(ctx context.Context, id string) (Attachment, error)
	Open(ctx context.Context, id string) (io.ReadCloser, error)
	Delete(ctx context.Context, id string) error
}

type Transcript struct {
	Text     string  `json:"text"`
	Engine   string  `json:"engine"`
	Language string  `json:"language,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

type Transcriber interface {
	Transcribe(ctx context.Context, audio Attachment) (Transcript, error)
}

type ActivitySummarizer interface {
	SummarizeActivity(ctx context.Context, facts []ActivityFact) (string, error)
}

type TerminalIO interface {
	Output() <-chan []byte
	Write([]byte) (int, error)
	Resize(cols, rows uint16) error
	Close() error
}

type SessionStore interface {
	CreateAgent(ctx context.Context, kind AgentKind, name, cwd, resumeID string) (*Session, error)
	CreateTerminal(ctx context.Context, name, cwd, shell string) (*Session, error)
	List() []Session
	Get(id string) (Session, bool)
	Rename(id, name string) (Session, error)
	Kill(id string) error
	Send(cmd ClientCommand) error
	Subscribe(sessionID string) (<-chan AgentEvent, func(), error)
	Terminal(id string) (TerminalIO, func(), error)
}
