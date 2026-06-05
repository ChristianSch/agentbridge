package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChristianSch/agentbridge/internal/config"
	"github.com/ChristianSch/agentbridge/internal/core"
)

const terminalUseOnceNotice = "Terminal sessions are use-once and are not restarted after AgentBridge restarts."
const nativeReadOnlyNotice = "Native session could not be resumed; restored AgentBridge history read-only."

func TestRestoreTerminalIsUseOnce(t *testing.T) {
	m := managerWithState(t, persistedState{Sessions: []persistedSession{{Session: testSession("term-1", core.AgentTerminal, core.StateRunning)}}})

	sess, ok := m.Get("term-1")
	if !ok {
		t.Fatal("restored terminal not found")
	}
	if sess.State != core.StateExited {
		t.Fatalf("state = %q, want %q", sess.State, core.StateExited)
	}
	if got := countNotice(m.history["term-1"], terminalUseOnceNotice); got != 1 {
		t.Fatalf("terminal notice count = %d, want 1", got)
	}
}

func TestRestoreExitedTerminalStillGetsNoticeOnce(t *testing.T) {
	m := managerWithState(t, persistedState{
		Sessions: []persistedSession{{Session: testSession("term-2", core.AgentTerminal, core.StateExited)}},
		History:  map[string][]core.AgentEvent{"term-2": {{Event: "history_source", SessionID: "term-2", Content: terminalUseOnceNotice}, {Event: "user_message", SessionID: "term-2", Content: "after notice"}}},
	})

	sess, ok := m.Get("term-2")
	if !ok {
		t.Fatal("restored terminal not found")
	}
	if sess.State != core.StateExited {
		t.Fatalf("state = %q, want %q", sess.State, core.StateExited)
	}
	if got := countNotice(m.history["term-2"], terminalUseOnceNotice); got != 1 {
		t.Fatalf("terminal notice count = %d, want 1", got)
	}
}

func TestRestoreAgentWithoutResumeIDReadOnly(t *testing.T) {
	ad := &fakeAdapter{kind: core.AgentPi}
	m := managerWithStateAndAdapters(t, persistedState{Sessions: []persistedSession{{Session: testSession("pi-1", core.AgentPi, core.StateRunning)}}}, ad)

	sess, ok := m.Get("pi-1")
	if !ok {
		t.Fatal("restored agent not found")
	}
	if sess.State != core.StateExited {
		t.Fatalf("state = %q, want %q", sess.State, core.StateExited)
	}
	if got := atomic.LoadInt32(&ad.builds); got != 0 {
		t.Fatalf("BuildCommand called %d time(s), want 0", got)
	}
	if got := countNotice(m.history["pi-1"], nativeReadOnlyNotice); got != 1 {
		t.Fatalf("read-only notice count = %d, want 1", got)
	}
}

func TestRestoreAgentWithResumeIDFastExitNotStuckStarting(t *testing.T) {
	ad := &fakeAdapter{kind: core.AgentPi, helperExit: true}
	m := managerWithStateAndAdapters(t, persistedState{Sessions: []persistedSession{{Session: testSession("pi-2", core.AgentPi, core.StateExited), ResumeID: "native-1"}}}, ad)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sess, ok := m.Get("pi-2")
		if !ok {
			t.Fatal("restored agent not found")
		}
		if sess.State != core.StateStarting {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	sess, _ := m.Get("pi-2")
	t.Fatalf("state remained %q", sess.State)
}

func TestHermesNativeHistoryDoesNotReplaceExistingConversation(t *testing.T) {
	m := managerWithState(t, persistedState{History: map[string][]core.AgentEvent{
		"hermes-1": {
			{Event: "history_source", SessionID: "hermes-1", Content: "Loaded from cache"},
			{Event: "user_message", SessionID: "hermes-1", Content: "cached prompt"},
			{Event: "delta", SessionID: "hermes-1", Content: "cached response"},
		},
	}})

	ok := m.restoreHermesNativeHistory("hermes-1", []core.AgentEvent{{Event: "user_message", SessionID: "hermes-1", Content: "native prompt"}}, "Restored 1 messages from Hermes native history")

	if ok {
		t.Fatal("restoreHermesNativeHistory replaced existing conversation history")
	}
	if got := len(m.history["hermes-1"]); got != 3 {
		t.Fatalf("history len = %d, want 3", got)
	}
	if got := m.history["hermes-1"][1].Content; got != "cached prompt" {
		t.Fatalf("history was replaced, got %q", got)
	}
}

func TestHermesNativeHistoryRestoresWhenOnlyNoticesExist(t *testing.T) {
	m := managerWithState(t, persistedState{History: map[string][]core.AgentEvent{
		"hermes-2": {{Event: "history_source", SessionID: "hermes-2", Content: "Loaded from cache"}, {Event: "state_change", SessionID: "hermes-2", State: core.StateStarting}},
	}})

	ok := m.restoreHermesNativeHistory("hermes-2", []core.AgentEvent{{Event: "user_message", SessionID: "hermes-2", Content: "native prompt"}}, "Restored 1 messages from Hermes native history")

	if !ok {
		t.Fatal("restoreHermesNativeHistory did not restore empty conversation history")
	}
	if got := len(m.history["hermes-2"]); got != 2 {
		t.Fatalf("history len = %d, want 2", got)
	}
	if got := m.history["hermes-2"][1].Content; got != "native prompt" {
		t.Fatalf("native history not restored, got %q", got)
	}
}

func TestPersistBacksUpOncePerManager(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENTBRIDGE_STATE_DIR", dir)
	path := filepath.Join(dir, "sessions.json")
	old := persistedState{Sessions: []persistedSession{{Session: testSession("term-backup", core.AgentTerminal, core.StateExited)}}}
	writeState(t, path, old)

	m := NewManager(config.Config{}, nil)
	m.persistNow()
	time.Sleep(1100 * time.Millisecond)
	m.persistNow()

	matches, err := filepath.Glob(path + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("backup count = %d, want 1 (%v)", len(matches), matches)
	}
}

func managerWithState(t *testing.T, st persistedState) *Manager {
	t.Helper()
	return managerWithStateAndAdapters(t, st)
}

func managerWithStateAndAdapters(t *testing.T, st persistedState, adapters ...core.AgentAdapter) *Manager {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AGENTBRIDGE_STATE_DIR", dir)
	writeState(t, filepath.Join(dir, "sessions.json"), st)
	return NewManager(config.Config{}, nil, adapters...)
}

func writeState(t *testing.T, path string, st persistedState) {
	t.Helper()
	if st.History == nil {
		st.History = map[string][]core.AgentEvent{}
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testSession(id string, kind core.AgentKind, state core.SessionState) core.Session {
	now := time.Now().Add(-time.Minute).UTC()
	return core.Session{ID: id, Kind: kind, Name: id, Cwd: os.TempDir(), State: state, CreatedAt: now, LastActive: now}
}

func countNotice(events []core.AgentEvent, content string) int {
	count := 0
	for _, ev := range events {
		if ev.Event == "history_source" && ev.Content == content {
			count++
		}
	}
	return count
}

type fakeAdapter struct {
	kind       core.AgentKind
	helperExit bool
	builds     int32
}

func (a *fakeAdapter) Kind() core.AgentKind { return a.kind }
func (a *fakeAdapter) BuildCommand(cfg core.AgentConfig) (string, []string, []string, error) {
	atomic.AddInt32(&a.builds, 1)
	if a.helperExit {
		return os.Args[0], []string{"-test.run=TestHelperProcess", "--", "exit"}, []string{"GO_WANT_HELPER_PROCESS=1"}, nil
	}
	return os.Args[0], []string{"-test.run=TestHelperProcess", "--", "block"}, []string{"GO_WANT_HELPER_PROCESS=1"}, nil
}
func (a *fakeAdapter) SendPrompt(core.PromptPayload) ([]byte, error)      { return nil, nil }
func (a *fakeAdapter) SendSteer(core.PromptPayload) ([]byte, error)       { return nil, nil }
func (a *fakeAdapter) SendAbort() ([]byte, error)                         { return nil, nil }
func (a *fakeAdapter) SendFollowUp(core.PromptPayload) ([]byte, error)    { return nil, nil }
func (a *fakeAdapter) SendCompact() ([]byte, error)                       { return nil, nil }
func (a *fakeAdapter) SendApproval(string, bool) ([]byte, error)          { return nil, nil }
func (a *fakeAdapter) InitialMessages(core.AgentConfig) ([][]byte, error) { return nil, nil }
func (a *fakeAdapter) ParseEvent([]byte) (*core.AgentEvent, error)        { return nil, nil }

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	mode := "exit"
	for i, arg := range args {
		if arg == "--" && i+1 < len(args) {
			mode = args[i+1]
			break
		}
	}
	if strings.EqualFold(mode, "block") {
		<-context.Background().Done()
	}
	os.Exit(1)
}
