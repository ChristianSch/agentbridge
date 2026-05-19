package core

import "context"

// AgentAdapter is the protocol port implemented by Pi, Hermes, and future agents.
type AgentAdapter interface {
	Kind() AgentKind
	BuildCommand(cfg AgentConfig) (name string, args []string, env []string, err error)
	SendPrompt(text string) ([]byte, error)
	SendSteer(text string) ([]byte, error)
	SendAbort() ([]byte, error)
	SendFollowUp(text string) ([]byte, error)
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
