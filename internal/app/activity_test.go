package app

import (
	"testing"

	"github.com/ChristianSch/agentbridge/internal/core"
)

func TestDeterministicActivitySummary(t *testing.T) {
	tests := []struct {
		name string
		fact core.ActivityFact
		want string
	}{
		{"read", core.ActivityFact{Event: "tool_start", Tool: "read", Args: map[string]any{"path": "/tmp/main.go"}}, "Reading main.go."},
		{"edit", core.ActivityFact{Event: "tool_start", Tool: "edit", Args: map[string]any{"path": "/tmp/main.go"}}, "Editing main.go."},
		{"test", core.ActivityFact{Event: "tool_start", Tool: "bash", Args: map[string]any{"command": "go test ./..."}}, "Running tests."},
		{"thinking", core.ActivityFact{Event: "thinking"}, "Working through the next step."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deterministicActivitySummary(tt.fact); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
