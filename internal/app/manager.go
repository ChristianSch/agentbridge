package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/ChristianSch/agentbridge/internal/config"
	"github.com/ChristianSch/agentbridge/internal/core"
)

const historyLimit = 20000

type Manager struct {
	cfg      config.Config
	adapters map[core.AgentKind]core.AgentAdapter
	mu       sync.RWMutex
	sessions map[string]*sessionRuntime
	subs     map[string]map[chan core.AgentEvent]struct{}
	history  map[string][]core.AgentEvent

	persistPath  string
	persistMu    sync.Mutex
	persistTimer *time.Timer
	activity     *activityNarrator
}

type sessionRuntime struct {
	core.Session
	adapter core.AgentAdapter
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	pty     *terminalRuntime

	cmdName                string
	args                   []string
	env                    []string
	dir                    string
	killed                 bool
	lastUse                time.Time
	clients                int
	resumeID               string
	remoteID               string
	startedAt              time.Time
	attachmentManifestPath string
	attachmentIndex        map[string]core.Attachment
	mu                     sync.Mutex
}

type terminalRuntime struct {
	file *os.File
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

type terminalConn struct {
	rt      *terminalRuntime
	output  chan []byte
	onClose func()
	once    sync.Once
}

type persistedState struct {
	Version  int                          `json:"version"`
	Sessions []persistedSession           `json:"sessions"`
	History  map[string][]core.AgentEvent `json:"history"`
}

type persistedSession struct {
	Session  core.Session `json:"session"`
	Dir      string       `json:"dir,omitempty"`
	Shell    string       `json:"shell,omitempty"`
	ResumeID string       `json:"resume_id,omitempty"`
	RemoteID string       `json:"remote_id,omitempty"`
}

func (c *terminalConn) Output() <-chan []byte          { return c.output }
func (c *terminalConn) Write(b []byte) (int, error)    { return c.rt.Write(b) }
func (c *terminalConn) Resize(cols, rows uint16) error { return c.rt.Resize(cols, rows) }
func (c *terminalConn) Close() error {
	c.once.Do(func() {
		c.rt.remove(c.output)
		if c.onClose != nil {
			c.onClose()
		}
	})
	return nil
}

func (t *terminalRuntime) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.file.Write(b)
}
func (t *terminalRuntime) Close() error { return t.file.Close() }
func (t *terminalRuntime) Resize(cols, rows uint16) error {
	return pty.Setsize(t.file, &pty.Winsize{Cols: cols, Rows: rows})
}
func (t *terminalRuntime) add() chan []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.subs == nil {
		t.subs = map[chan []byte]struct{}{}
	}
	ch := make(chan []byte, 256)
	t.subs[ch] = struct{}{}
	return ch
}
func (t *terminalRuntime) remove(ch chan []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.subs[ch]; ok {
		delete(t.subs, ch)
		close(ch)
	}
}
func (t *terminalRuntime) broadcast(b []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for ch := range t.subs {
		copyB := append([]byte(nil), b...)
		select {
		case ch <- copyB:
		default:
		}
	}
}

func NewManager(cfg config.Config, summarizer core.ActivitySummarizer, adapters ...core.AgentAdapter) *Manager {
	m := &Manager{cfg: cfg, sessions: map[string]*sessionRuntime{}, subs: map[string]map[chan core.AgentEvent]struct{}{}, history: map[string][]core.AgentEvent{}, adapters: map[core.AgentKind]core.AgentAdapter{}, persistPath: defaultPersistPath()}
	m.activity = newActivityNarrator(cfg.ActivitySummary, summarizer, m.publish)
	for _, a := range adapters {
		m.adapters[a.Kind()] = a
	}
	m.restorePersisted()
	go m.reaper()
	return m
}

func (m *Manager) CreateAgent(ctx context.Context, kind core.AgentKind, name, cwd, resumeID string) (*core.Session, error) {
	ad, ok := m.adapters[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported agent kind %q", kind)
	}
	id := newID(string(kind))
	if name == "" {
		name = id
	}
	dir := usableDir(cwd)
	if kind == core.AgentHermes {
		if m.cfg.Hermes.Cwd != "" && isHermesGatewayDir(m.cfg.Hermes.Cwd) {
			dir = m.cfg.Hermes.Cwd
		}
		if !isHermesGatewayDir(dir) {
			if detected := detectHermesGatewayDir(); detected != "" {
				log.Printf("session %s: auto-detected Hermes repo at %q", id, detected)
				dir = detected
			}
		}
		if !isHermesGatewayDir(dir) {
			return nil, fmt.Errorf("Hermes gateway not found. Select the Hermes repo root containing tui_gateway/entry.py, or set hermes.cwd to that path. Current selection: %s", dir)
		}
	}
	cmdName, args, env, err := ad.BuildCommand(core.AgentConfig{SessionName: name, Cwd: dir, PiBinary: m.cfg.Pi.Binary, PiResumeID: resumeID, HermesVenv: m.cfg.Hermes.Venv, HermesModule: m.cfg.Hermes.GatewayModule, HermesCwd: dir})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	p := &sessionRuntime{Session: core.Session{ID: id, Kind: kind, Name: name, Cwd: dir, State: core.StateStarting, CreatedAt: now, LastActive: now, OwnerID: core.OwnerID(ctx)}, adapter: ad, cmdName: cmdName, args: args, env: env, dir: dir, resumeID: resumeID, lastUse: now, attachmentManifestPath: attachmentManifestPath(id), attachmentIndex: map[string]core.Attachment{}}
	log.Printf("session %s: starting %s agent name=%q cwd=%q cmd=%q args=%q", id, kind, name, cwd, cmdName, args)
	if err := m.startAgent(p); err != nil {
		log.Printf("session %s: start failed: %v", id, err)
		return nil, err
	}
	m.mu.Lock()
	m.sessions[id] = p
	m.mu.Unlock()
	m.publish(core.AgentEvent{Event: "state_change", SessionID: id, State: core.StateStarting})
	return &p.Session, nil
}

func (m *Manager) startAgent(p *sessionRuntime) error {
	cmd := exec.CommandContext(context.Background(), p.cmdName, p.args...)
	cmd.Dir = p.dir
	cmd.Env = append(cmd.Environ(), p.env...)
	if p.attachmentManifestPath != "" {
		cmd.Env = append(cmd.Env, "AGENTBRIDGE_ATTACHMENT_MANIFEST="+p.attachmentManifestPath)
		_ = p.writeAttachmentManifest()
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	log.Printf("session %s: process started pid=%d", p.ID, cmd.Process.Pid)
	p.mu.Lock()
	p.cmd = cmd
	p.stdin = stdin
	p.killed = false
	p.startedAt = time.Now()
	p.State = core.StateStarting
	p.mu.Unlock()
	go m.readLoop(p, stdout)
	go m.stderrLoop(p, stderr)
	go m.waitLoop(p)
	if msgs, err := p.adapter.InitialMessages(core.AgentConfig{PiResumeID: p.resumeID, HermesResumeID: p.resumeID}); err == nil {
		for _, b := range msgs {
			_, _ = stdin.Write(b)
		}
	}
	return nil
}

func (m *Manager) CreateTerminal(ctx context.Context, name, cwd, shell string) (*core.Session, error) {
	cwd = usableDir(cwd)
	if m.cfg.Terminal.MaxSessions > 0 && m.countTerminals() >= m.cfg.Terminal.MaxSessions {
		return nil, fmt.Errorf("terminal session limit reached (%d)", m.cfg.Terminal.MaxSessions)
	}
	if shell == "" {
		shell = m.cfg.Terminal.Shell
	}
	if shell == "" {
		shell = "/bin/bash"
	}
	id := newID("term")
	if name == "" {
		name = id
	}
	now := time.Now()
	p := &sessionRuntime{Session: core.Session{ID: id, Kind: core.AgentTerminal, Name: name, Cwd: cwd, State: core.StateStarting, CreatedAt: now, LastActive: now, OwnerID: core.OwnerID(ctx)}, dir: cwd, cmdName: shell, lastUse: now}
	log.Printf("session %s: starting terminal name=%q cwd=%q shell=%q", id, name, cwd, shell)
	if err := m.startTerminal(p, shell); err != nil {
		log.Printf("session %s: terminal start failed: %v", id, err)
		return nil, err
	}
	m.mu.Lock()
	m.sessions[id] = p
	m.mu.Unlock()
	m.publish(core.AgentEvent{Event: "state_change", SessionID: id, State: core.StateRunning})
	return &p.Session, nil
}

func (m *Manager) startTerminal(p *sessionRuntime, shell string) error {
	if shell == "" {
		shell = m.cfg.Terminal.Shell
	}
	if shell == "" {
		shell = "/bin/bash"
	}
	dir := usableDir(p.dir)
	cmd := exec.CommandContext(context.Background(), shell)
	cmd.Dir = dir
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 32})
	if err != nil {
		return err
	}
	log.Printf("session %s: terminal started pid=%d", p.ID, cmd.Process.Pid)
	tr := &terminalRuntime{file: f, subs: map[chan []byte]struct{}{}}
	p.mu.Lock()
	p.cmd = cmd
	p.pty = tr
	p.dir = dir
	p.cmdName = shell
	p.Cwd = dir
	p.State = core.StateRunning
	now := time.Now()
	p.lastUse = now
	p.LastActive = now
	p.mu.Unlock()
	go m.terminalReadLoop(p, tr)
	go m.waitLoop(p)
	return nil
}

func (m *Manager) countTerminals() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, s := range m.sessions {
		if s.Kind == core.AgentTerminal && s.State != core.StateExited {
			n++
		}
	}
	return n
}

func (m *Manager) List() []core.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]core.Session, 0, len(m.sessions))
	for _, p := range m.sessions {
		out = append(out, p.Session)
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := out[i].LastActive, out[j].LastActive
		if li.IsZero() {
			li = out[i].CreatedAt
		}
		if lj.IsZero() {
			lj = out[j].CreatedAt
		}
		return li.After(lj)
	})
	return out
}
func (m *Manager) Get(id string) (core.Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.sessions[id]
	if !ok {
		return core.Session{}, false
	}
	return p.Session, true
}

func (m *Manager) Rename(id, name string) (core.Session, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return core.Session{}, errors.New("name is required")
	}
	m.mu.Lock()
	p, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return core.Session{}, errors.New("session not found")
	}
	now := time.Now()
	p.Name = name
	p.lastUse = now
	p.LastActive = now
	sess := p.Session
	m.mu.Unlock()
	m.schedulePersist()
	m.publish(core.AgentEvent{Event: "session_renamed", SessionID: id, Content: name})
	return sess, nil
}

func (m *Manager) ClaimSessions(ownerID string) error {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return errors.New("owner id is required")
	}
	m.mu.Lock()
	for _, p := range m.sessions {
		if p.OwnerID == "" || p.OwnerID == "token" || p.OwnerID == "bootstrap" {
			p.OwnerID = ownerID
		}
	}
	m.mu.Unlock()
	m.schedulePersist()
	return nil
}

func (m *Manager) Kill(id string) error {
	return m.stopSession(id, true)
}

func (m *Manager) stopSession(id string, remove bool) error {
	m.mu.Lock()
	p, ok := m.sessions[id]
	if ok && remove {
		delete(m.sessions, id)
	}
	if ok && !remove {
		now := time.Now()
		p.State = core.StateExited
		p.lastUse = now
		p.LastActive = now
	}
	m.mu.Unlock()
	if !ok {
		return errors.New("session not found")
	}
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	if p.pty != nil {
		_ = p.pty.Close()
	}
	m.schedulePersist()
	if !remove {
		m.publish(core.AgentEvent{Event: "state_change", SessionID: id, State: core.StateExited})
	}
	if p.cmd != nil && p.cmd.Process != nil {
		log.Printf("session %s: killing pid=%d", id, p.cmd.Process.Pid)
		return p.cmd.Process.Kill()
	}
	return nil
}

func (m *Manager) Send(c core.ClientCommand) error {
	m.mu.RLock()
	p, ok := m.sessions[c.SessionID]
	m.mu.RUnlock()
	if !ok {
		return errors.New("session not found")
	}
	if p.adapter == nil || p.stdin == nil {
		return errors.New("session does not accept agent commands")
	}
	if p.State == core.StateExited || p.State == core.StateError {
		return fmt.Errorf("session is %s", p.State)
	}
	if p.Kind == core.AgentHermes && c.Action != "abort" {
		p.mu.Lock()
		ready := p.remoteID != ""
		p.mu.Unlock()
		if !ready {
			return errors.New("Hermes session is not ready yet, no gateway session_id received")
		}
	}
	var b []byte
	var err error
	switch c.Action {
	case "prompt":
		b, err = p.adapter.SendPrompt(core.PromptPayload{Text: c.Text, Attachments: c.Attachments})
	case "steer":
		b, err = p.adapter.SendSteer(core.PromptPayload{Text: c.Text, Attachments: c.Attachments})
	case "abort":
		b, err = p.adapter.SendAbort()
	case "follow_up":
		b, err = p.adapter.SendFollowUp(core.PromptPayload{Text: c.Text, Attachments: c.Attachments})
	case "compact":
		b, err = p.adapter.SendCompact()
	case "approve":
		b, err = p.adapter.SendApproval(c.RequestID, c.Approved)
	default:
		return fmt.Errorf("unknown action %q", c.Action)
	}
	if err != nil {
		return err
	}
	if len(c.Attachments) > 0 {
		p.addAttachments(c.Attachments)
	}
	if c.Action == "prompt" {
		m.publish(core.AgentEvent{Event: "user_message", SessionID: p.ID, Content: c.Text, Attachments: c.Attachments})
	}
	if p.Kind == core.AgentHermes {
		b = m.addHermesSessionID(p, b)
		log.Printf("session %s hermes stdin: %.1000s", p.ID, string(b))
	}
	p.mu.Lock()
	now := time.Now()
	p.lastUse = now
	p.LastActive = now
	if p.Kind == core.AgentPi {
		log.Printf("session %s pi stdin: %.1000s", p.ID, string(b))
	}
	_, err = p.stdin.Write(b)
	p.mu.Unlock()
	return err
}

func (p *sessionRuntime) addAttachments(attachments []core.Attachment) {
	p.mu.Lock()
	if p.attachmentIndex == nil {
		p.attachmentIndex = map[string]core.Attachment{}
	}
	for _, att := range attachments {
		if att.ID != "" {
			p.attachmentIndex[att.ID] = att
		}
	}
	_ = p.writeAttachmentManifestLocked()
	p.mu.Unlock()
}

func (p *sessionRuntime) writeAttachmentManifest() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writeAttachmentManifestLocked()
}

func (p *sessionRuntime) writeAttachmentManifestLocked() error {
	if p.attachmentManifestPath == "" {
		return nil
	}
	items := []core.Attachment{}
	for _, att := range p.attachmentIndex {
		items = append(items, att)
	}
	b, err := json.MarshalIndent(map[string]any{"session_id": p.ID, "attachments": items}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.attachmentManifestPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p.attachmentManifestPath, b, 0o600)
}

func attachmentManifestPath(sessionID string) string {
	base := filepath.Dir(defaultPersistPath())
	return filepath.Join(base, "attachment-manifests", sessionID+".json")
}

func recoverSessionsFromHistory(history map[string][]core.AgentEvent) []persistedSession {
	out := []persistedSession{}
	for id, events := range history {
		kind := core.AgentKind("")
		switch {
		case strings.HasPrefix(id, string(core.AgentPi)+"-"):
			kind = core.AgentPi
		case strings.HasPrefix(id, string(core.AgentHermes)+"-"):
			kind = core.AgentHermes
		case strings.HasPrefix(id, "term-"):
			kind = core.AgentTerminal
		default:
			continue
		}
		now := time.Now()
		name := id
		created := now
		last := now
		for _, ev := range events {
			if ev.Event == "user_message" && strings.TrimSpace(ev.Content) != "" {
				name = strings.TrimSpace(ev.Content)
				if len(name) > 48 {
					name = name[:48] + "…"
				}
				break
			}
		}
		if kind == core.AgentTerminal {
			name = "recovered terminal"
		}
		resumeID, remoteID := inferResumeIDs(kind, events)
		out = append(out, persistedSession{Session: core.Session{ID: id, Kind: kind, Name: name, Cwd: usableDir(""), State: core.StateExited, CreatedAt: created, LastActive: last}, Dir: usableDir(""), ResumeID: resumeID, RemoteID: remoteID})
	}
	return out
}

func inferResumeIDs(kind core.AgentKind, events []core.AgentEvent) (resumeID, remoteID string) {
	for _, ev := range events {
		if ev.Raw != nil {
			resumeID, remoteID = collectResumeIDs(kind, ev.Raw, resumeID, remoteID)
		}
	}
	if resumeID == "" {
		resumeID = remoteID
	}
	return resumeID, remoteID
}

func collectResumeIDs(kind core.AgentKind, v any, resumeID, remoteID string) (string, string) {
	switch x := v.(type) {
	case map[string]any:
		if kind == core.AgentHermes {
			if s := stringValue(x["session_key"]); s != "" {
				resumeID = s
			}
			if s := stringValue(x["resumed"]); s != "" {
				resumeID = s
			}
			if s := stringValue(x["session_id"]); s != "" {
				remoteID = s
			}
		}
		if kind == core.AgentPi {
			if s := stringValue(x["sessionId"]); s != "" {
				remoteID = s
			}
			if s := stringValue(x["sessionFile"]); s != "" {
				remoteID = s
			}
		}
		for _, child := range x {
			resumeID, remoteID = collectResumeIDs(kind, child, resumeID, remoteID)
		}
	case []any:
		for _, child := range x {
			resumeID, remoteID = collectResumeIDs(kind, child, resumeID, remoteID)
		}
	}
	return resumeID, remoteID
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func attachmentsFromHistory(events []core.AgentEvent) map[string]core.Attachment {
	out := map[string]core.Attachment{}
	for _, ev := range events {
		for _, att := range ev.Attachments {
			if att.ID != "" {
				out[att.ID] = att
			}
		}
	}
	return out
}

func (m *Manager) addHermesSessionID(p *sessionRuntime, b []byte) []byte {
	p.mu.Lock()
	remoteID := p.remoteID
	p.mu.Unlock()
	if remoteID == "" {
		return b
	}
	var msg map[string]any
	if err := json.Unmarshal(b, &msg); err != nil {
		return b
	}
	params, _ := msg["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}
	if _, ok := params["session_id"]; !ok {
		params["session_id"] = remoteID
	}
	msg["params"] = params
	out, err := json.Marshal(msg)
	if err != nil {
		return b
	}
	return append(out, '\n')
}

func (m *Manager) Subscribe(sessionID string) (<-chan core.AgentEvent, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.sessions[sessionID]
	if !ok {
		return nil, nil, errors.New("session not found")
	}
	ch := make(chan core.AgentEvent, 128)
	if m.subs[sessionID] == nil {
		m.subs[sessionID] = map[chan core.AgentEvent]struct{}{}
	}
	m.subs[sessionID][ch] = struct{}{}
	p.clients++
	p.lastUse = time.Now()
	if n := len(m.history[sessionID]); n > 0 {
		select {
		case ch <- core.AgentEvent{Event: "history_source", SessionID: sessionID, Content: fmt.Sprintf("Loaded %d events from AgentBridge cache", n)}:
		default:
		}
	}
	for _, ev := range m.history[sessionID] {
		select {
		case ch <- ev:
		default:
		}
	}
	cancel := func() {
		m.mu.Lock()
		if _, ok := m.subs[sessionID][ch]; ok {
			delete(m.subs[sessionID], ch)
			p.clients--
			p.lastUse = time.Now()
			close(ch)
		}
		m.mu.Unlock()
	}
	return ch, cancel, nil
}

func (m *Manager) Terminal(id string) (core.TerminalIO, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.sessions[id]
	if !ok {
		return nil, nil, errors.New("session not found")
	}
	if p.pty == nil {
		return nil, nil, errors.New("session is not a terminal")
	}
	p.clients++
	p.lastUse = time.Now()
	ch := p.pty.add()
	detach := func() { m.mu.Lock(); p.clients--; p.lastUse = time.Now(); m.mu.Unlock() }
	conn := &terminalConn{rt: p.pty, output: ch, onClose: detach}
	return conn, func() { _ = conn.Close() }, nil
}

func (m *Manager) terminalReadLoop(p *sessionRuntime, tr *terminalRuntime) {
	buf := make([]byte, 32*1024)
	for {
		n, err := tr.file.Read(buf)
		if n > 0 {
			tr.broadcast(buf[:n])
		}
		if err != nil {
			log.Printf("session %s: terminal read ended: %v", p.ID, err)
			return
		}
	}
}

func (m *Manager) readLoop(p *sessionRuntime, r io.Reader) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for s.Scan() {
		line := append([]byte(nil), s.Bytes()...)
		if p.Kind == core.AgentPi {
			log.Printf("session %s pi stdout: %.1000s", p.ID, string(line))
		}
		if p.Kind == core.AgentHermes {
			log.Printf("session %s hermes stdout: %.1000s", p.ID, string(line))
		}
		if p.Kind == core.AgentHermes {
			m.captureHermesRemoteID(p, line)
		}
		if p.Kind == core.AgentPi {
			m.capturePiRemoteID(p, line)
		}
		ev, err := p.adapter.ParseEvent(line)
		if err != nil {
			ev = &core.AgentEvent{Event: "error", Content: err.Error()}
		}
		if ev == nil {
			continue
		}
		ev.SessionID = p.ID
		if p.Kind == core.AgentHermes && ev.Event == "error" && strings.Contains(strings.ToLower(ev.Content), "session not found") {
			m.markReadOnly(p, "Hermes session was not found; keeping AgentBridge history read-only.")
		}
		if ev.Event == "state_change" {
			log.Printf("session %s event state=%s", p.ID, ev.State)
			p.mu.Lock()
			p.State = ev.State
			now := time.Now()
			p.lastUse = now
			p.LastActive = now
			p.mu.Unlock()
			m.setState(p.ID, ev.State)
		} else if ev.Event != "delta" {
			log.Printf("session %s event=%s", p.ID, ev.Event)
		}
		m.publish(*ev)
	}
	if err := s.Err(); err != nil {
		m.publish(core.AgentEvent{Event: "error", SessionID: p.ID, Content: err.Error()})
	}
}

func (m *Manager) markReadOnly(p *sessionRuntime, reason string) {
	p.mu.Lock()
	p.stdin = nil
	p.killed = true
	p.State = core.StateExited
	now := time.Now()
	p.lastUse = now
	p.LastActive = now
	cmd := p.cmd
	pty := p.pty
	p.mu.Unlock()
	log.Printf("session %s: marking read-only: %s", p.ID, reason)
	if pty != nil {
		_ = pty.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	m.publish(core.AgentEvent{Event: "error", SessionID: p.ID, Content: reason})
	m.setState(p.ID, core.StateExited)
	m.publish(core.AgentEvent{Event: "state_change", SessionID: p.ID, State: core.StateExited})
}

func (m *Manager) captureHermesRemoteID(p *sessionRuntime, line []byte) {
	var msg struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      any            `json:"id"`
		Result  map[string]any `json:"result"`
		Error   map[string]any `json:"error"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.Result == nil {
		return
	}
	if key, _ := msg.Result["session_key"].(string); key != "" {
		p.mu.Lock()
		changed := p.resumeID != key
		p.resumeID = key
		p.mu.Unlock()
		if changed {
			log.Printf("session %s: hermes persistent session_key=%s", p.ID, key)
			m.schedulePersist()
		}
	}
	if sid, _ := msg.Result["session_id"].(string); sid != "" {
		resumed, _ := msg.Result["resumed"].(string)
		p.mu.Lock()
		p.remoteID = sid
		if resumed != "" {
			p.resumeID = resumed
		}
		needSessionKey := p.resumeID == ""
		p.State = core.StateIdle
		p.mu.Unlock()
		log.Printf("session %s: hermes remote session_id=%s", p.ID, sid)
		if resumed != "" {
			log.Printf("session %s: hermes resumed persistent session_key=%s", p.ID, resumed)
		}
		if messages, ok := msg.Result["messages"].([]any); ok && len(messages) > 0 {
			m.replaceHistory(p.ID, hermesMessagesToEvents(p.ID, messages), fmt.Sprintf("Restored %d messages from Hermes native history", len(messages)))
			log.Printf("session %s: restored %d Hermes history message(s)", p.ID, len(messages))
		}
		if needSessionKey {
			m.queryHermesSessionKey(p, sid)
		}
		m.setState(p.ID, core.StateIdle)
		m.publish(core.AgentEvent{Event: "state_change", SessionID: p.ID, State: core.StateIdle})
	}
}

func (m *Manager) queryHermesSessionKey(p *sessionRuntime, remoteID string) {
	p.mu.Lock()
	stdin := p.stdin
	p.mu.Unlock()
	if stdin == nil || remoteID == "" {
		return
	}
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "ab-session-title-" + remoteID,
		"method":  "session.title",
		"params":  map[string]any{"session_id": remoteID},
	})
	if err != nil {
		return
	}
	b = append(b, '\n')
	log.Printf("session %s hermes stdin: %.1000s", p.ID, string(b))
	_, _ = stdin.Write(b)
}

func (m *Manager) capturePiRemoteID(p *sessionRuntime, line []byte) {
	var msg struct {
		Type    string         `json:"type"`
		Command string         `json:"command"`
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(line, &msg); err != nil || msg.Type != "response" || msg.Command != "get_state" || !msg.Success || msg.Data == nil {
		return
	}
	sid, _ := msg.Data["sessionId"].(string)
	if sid == "" {
		sid, _ = msg.Data["sessionFile"].(string)
	}
	if sid == "" {
		return
	}
	p.mu.Lock()
	changed := p.remoteID != sid
	p.remoteID = sid
	p.mu.Unlock()
	if changed {
		log.Printf("session %s: pi remote session_id=%s", p.ID, sid)
		m.schedulePersist()
	}
}

func (m *Manager) stderrLoop(p *sessionRuntime, r io.Reader) {
	s := bufio.NewScanner(r)
	for s.Scan() {
		line := s.Text()
		log.Printf("session %s stderr: %s", p.ID, line)
		m.publish(core.AgentEvent{Event: "stderr", SessionID: p.ID, Content: line})
	}
}

func (m *Manager) waitLoop(p *sessionRuntime) {
	err := p.cmd.Wait()
	if err != nil {
		log.Printf("session %s: process exited: %v", p.ID, err)
	} else {
		log.Printf("session %s: process exited cleanly", p.ID)
	}
	p.mu.Lock()
	killed := p.killed
	isAgent := p.adapter != nil
	p.mu.Unlock()
	if killed {
		m.setState(p.ID, core.StateExited)
		m.publish(core.AgentEvent{Event: "state_change", SessionID: p.ID, State: core.StateExited})
		return
	}
	if isAgent {
		p.mu.Lock()
		uptime := time.Since(p.startedAt)
		resumeID := p.resumeID
		p.mu.Unlock()
		if uptime < 3*time.Second {
			if resumeID != "" {
				m.markReadOnly(p, "Native session could not be resumed; keeping AgentBridge history read-only.")
				log.Printf("session %s: startup exit after %s while resuming %q; restored read-only", p.ID, uptime, resumeID)
				return
			}
			m.setState(p.ID, core.StateError)
			m.publish(core.AgentEvent{Event: "error", SessionID: p.ID, Content: "process exited during startup; not restarting"})
			m.publish(core.AgentEvent{Event: "state_change", SessionID: p.ID, State: core.StateError})
			log.Printf("session %s: startup exit after %s, not restarting", p.ID, uptime)
			return
		}
		m.publish(core.AgentEvent{Event: "error", SessionID: p.ID, Content: "process exited, restarting"})
		log.Printf("session %s: restarting after unexpected exit", p.ID)
		go m.restart(p)
		return
	}
	m.setState(p.ID, core.StateExited)
	m.publish(core.AgentEvent{Event: "state_change", SessionID: p.ID, State: core.StateExited})
}

func (m *Manager) restart(p *sessionRuntime) {
	for i := 0; i < 5; i++ {
		time.Sleep(time.Duration(1<<i) * time.Second)
		p.mu.Lock()
		killed := p.killed
		p.mu.Unlock()
		if killed {
			return
		}
		if err := m.startAgent(p); err != nil {
			log.Printf("session %s: restart attempt %d failed: %v", p.ID, i+1, err)
			m.publish(core.AgentEvent{Event: "error", SessionID: p.ID, Content: err.Error()})
			continue
		}
		m.publish(core.AgentEvent{Event: "state_change", SessionID: p.ID, State: core.StateStarting})
		return
	}
	m.setState(p.ID, core.StateError)
	m.publish(core.AgentEvent{Event: "state_change", SessionID: p.ID, State: core.StateError})
}

func (m *Manager) setState(id string, state core.SessionState) {
	m.mu.Lock()
	if p, ok := m.sessions[id]; ok {
		p.State = state
		now := time.Now()
		p.lastUse = now
		p.LastActive = now
	}
	m.mu.Unlock()
	m.schedulePersist()
}

func (m *Manager) publish(ev core.AgentEvent) {
	if ev.Event != "activity_summary" {
		m.activity.Observe(ev)
	}
	m.mu.Lock()
	if ev.SessionID != "" {
		h := m.history[ev.SessionID]
		if shouldCoalesceHistory(ev) && len(h) > 0 {
			last := &h[len(h)-1]
			if last.Event == ev.Event && last.Tool == ev.Tool {
				last.Content += ev.Content
				last.Output += ev.Output
				last.Raw = ev.Raw
			} else {
				h = append(h, ev)
			}
		} else {
			h = append(h, ev)
		}
		if len(h) > historyLimit {
			h = h[len(h)-historyLimit:]
		}
		m.history[ev.SessionID] = h
	}
	subCount := len(m.subs[ev.SessionID])
	chs := make([]chan core.AgentEvent, 0, subCount)
	for ch := range m.subs[ev.SessionID] {
		chs = append(chs, ch)
	}
	m.mu.Unlock()
	m.schedulePersist()
	for _, ch := range chs {
		select {
		case ch <- ev:
		default:
		}
	}
	if subCount == 0 && m.shouldNotify(ev) {
		go m.notify(ev)
	}
}

func (m *Manager) replaceHistory(sessionID string, events []core.AgentEvent, source string) {
	if source != "" {
		events = append([]core.AgentEvent{{Event: "history_source", SessionID: sessionID, Content: source}}, events...)
	}
	if len(events) > historyLimit {
		events = events[len(events)-historyLimit:]
	}
	m.mu.Lock()
	m.history[sessionID] = events
	subCount := len(m.subs[sessionID])
	chs := make([]chan core.AgentEvent, 0, subCount)
	for ch := range m.subs[sessionID] {
		chs = append(chs, ch)
	}
	m.mu.Unlock()
	m.schedulePersist()
	for _, ev := range events {
		for _, ch := range chs {
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

func hermesMessagesToEvents(sessionID string, messages []any) []core.AgentEvent {
	events := make([]core.AgentEvent, 0, len(messages))
	for _, item := range messages {
		m, _ := item.(map[string]any)
		role, _ := m["role"].(string)
		text, _ := m["text"].(string)
		if text == "" {
			text, _ = m["content"].(string)
		}
		switch role {
		case "user":
			events = append(events, core.AgentEvent{Event: "user_message", SessionID: sessionID, Content: text})
		case "assistant":
			events = append(events, core.AgentEvent{Event: "delta", SessionID: sessionID, Content: text})
		case "tool":
			name, _ := m["name"].(string)
			if text == "" {
				text, _ = m["context"].(string)
			}
			events = append(events, core.AgentEvent{Event: "tool_end", SessionID: sessionID, Tool: name, Output: text})
		}
	}
	return events
}

func shouldCoalesceHistory(ev core.AgentEvent) bool {
	return ev.Event == "delta" || ev.Event == "thinking_delta" || ev.Event == "tool_delta"
}

func (m *Manager) reaper() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		var kill []string
		m.mu.RLock()
		for id, p := range m.sessions {
			if p.clients > 0 || p.State == core.StateExited {
				continue
			}
			timeout := m.idleTimeout(p.Kind)
			if timeout > 0 && now.Sub(p.lastUse) > timeout {
				kill = append(kill, id)
			}
		}
		m.mu.RUnlock()
		for _, id := range kill {
			log.Printf("session %s: idle timeout reached, stopping", id)
			_ = m.stopSession(id, false)
		}
	}
}
func (m *Manager) idleTimeout(k core.AgentKind) time.Duration {
	switch k {
	case core.AgentPi:
		return m.cfg.Pi.IdleTimeout
	case core.AgentHermes:
		return m.cfg.Hermes.IdleTimeout
	case core.AgentTerminal:
		return m.cfg.Terminal.IdleTimeout
	default:
		return 0
	}
}

func (m *Manager) shouldNotify(ev core.AgentEvent) bool {
	name := ev.Event
	if ev.Event == "state_change" && ev.State != "" {
		name = string(ev.State)
	}
	if len(m.cfg.Notifications.NotifyOn) == 0 {
		return name == "approval_request" || name == string(core.StateWaitingForInput)
	}
	for _, n := range m.cfg.Notifications.NotifyOn {
		if n == name {
			return true
		}
	}
	return false
}

func (m *Manager) notify(ev core.AgentEvent) {
	if m.cfg.Notifications.Backend != "ntfy" || m.cfg.Notifications.NtfyTopic == "" {
		return
	}
	server := m.cfg.Notifications.NtfyServer
	if server == "" {
		server = "https://ntfy.sh"
	}
	body := ev.Prompt
	if body == "" {
		body = fmt.Sprintf("%s needs attention", ev.SessionID)
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(server, "/")+"/"+m.cfg.Notifications.NtfyTopic, bytes.NewBufferString(body))
	if err != nil {
		return
	}
	req.Header.Set("Title", "AgentBridge")
	resp, err := http.DefaultClient.Do(req)
	if err == nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func (m *Manager) restorePersisted() {
	st, ok := readPersistedStateWithLog(m.persistPath)
	if !ok {
		return
	}
	if st.History != nil {
		m.history = st.History
	}
	if len(st.Sessions) == 0 && len(st.History) > 0 {
		st.Sessions = recoverSessionsFromHistory(st.History)
		if len(st.Sessions) > 0 {
			log.Printf("session persistence: recovered %d history-only session(s) from cached events", len(st.Sessions))
		}
	}
	for _, ps := range st.Sessions {
		sess := ps.Session
		if sess.ID == "" || sess.Kind == "" {
			continue
		}
		if sess.CreatedAt.IsZero() {
			sess.CreatedAt = time.Now()
		}
		if sess.LastActive.IsZero() {
			sess.LastActive = sess.CreatedAt
		}
		persistedState := sess.State
		if persistedState == "" {
			persistedState = core.StateStarting
		}
		resumeID, remoteID := ps.ResumeID, ps.RemoteID
		inferredResumeID, inferredRemoteID := inferResumeIDs(sess.Kind, st.History[sess.ID])
		if resumeID == "" {
			resumeID = inferredResumeID
		}
		if remoteID == "" {
			remoteID = inferredRemoteID
		}
		if resumeID == "" {
			resumeID = remoteID
		}
		p := &sessionRuntime{Session: sess, dir: ps.Dir, cmdName: ps.Shell, resumeID: resumeID, remoteID: remoteID, lastUse: time.Now(), attachmentManifestPath: attachmentManifestPath(sess.ID), attachmentIndex: attachmentsFromHistory(st.History[sess.ID])}
		if p.dir == "" {
			p.dir = sess.Cwd
		}
		if persistedState == core.StateExited {
			p.State = core.StateExited
			m.sessions[sess.ID] = p
			continue
		}
		p.State = core.StateStarting
		switch sess.Kind {
		case core.AgentTerminal:
			if err := m.startTerminal(p, ps.Shell); err != nil {
				log.Printf("session %s: restore terminal failed: %v", sess.ID, err)
				p.State = core.StateError
			}
		case core.AgentPi, core.AgentHermes:
			ad, ok := m.adapters[sess.Kind]
			if !ok {
				p.State = core.StateError
				break
			}
			p.adapter = ad
			if p.resumeID == "" {
				p.State = core.StateExited
				m.sessions[sess.ID] = p
				log.Printf("session %s: no native resume id available; restored as read-only history", sess.ID)
				continue
			}
			if sess.Kind == core.AgentHermes && !isHermesGatewayDir(p.dir) {
				p.dir = detectHermesGatewayDir()
			}
			p.dir = usableDir(p.dir)
			p.Cwd = p.dir
			cmdName, args, env, err := ad.BuildCommand(core.AgentConfig{SessionName: sess.Name, Cwd: p.dir, PiBinary: m.cfg.Pi.Binary, PiResumeID: p.resumeID, HermesVenv: m.cfg.Hermes.Venv, HermesModule: m.cfg.Hermes.GatewayModule, HermesCwd: p.dir})
			if err != nil {
				log.Printf("session %s: restore command failed: %v", sess.ID, err)
				p.State = core.StateError
				break
			}
			p.cmdName, p.args, p.env = cmdName, args, env
			if err := m.startAgent(p); err != nil {
				log.Printf("session %s: restore agent failed: %v", sess.ID, err)
				p.State = core.StateError
			}
		default:
			continue
		}
		m.sessions[sess.ID] = p
	}
	if len(st.Sessions) > 0 {
		log.Printf("session persistence: restored %d session(s) from %s", len(m.sessions), m.persistPath)
	}
}

func (m *Manager) schedulePersist() {
	if m.persistPath == "" {
		return
	}
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	if m.persistTimer != nil {
		m.persistTimer.Reset(500 * time.Millisecond)
		return
	}
	m.persistTimer = time.AfterFunc(500*time.Millisecond, func() {
		m.persistMu.Lock()
		m.persistTimer = nil
		m.persistMu.Unlock()
		m.persistNow()
	})
}

func (m *Manager) persistNow() {
	m.mu.RLock()
	st := persistedState{Version: 1, Sessions: []persistedSession{}, History: map[string][]core.AgentEvent{}}
	for id, h := range m.history {
		st.History[id] = append([]core.AgentEvent(nil), h...)
	}
	for _, p := range m.sessions {
		ps := persistedSession{Session: p.Session, Dir: p.dir, Shell: p.cmdName, ResumeID: p.resumeID, RemoteID: p.remoteID}
		st.Sessions = append(st.Sessions, ps)
	}
	m.mu.RUnlock()
	if len(st.Sessions) == 0 && len(st.History) > 0 {
		if prev, ok := readPersistedState(m.persistPath); ok && len(prev.Sessions) > 0 {
			log.Printf("session persistence: refusing to overwrite %d persisted session(s) with empty session list; preserving previous metadata", len(prev.Sessions))
			st.Sessions = prev.Sessions
		}
	}
	sort.Slice(st.Sessions, func(i, j int) bool {
		li, lj := st.Sessions[i].Session.LastActive, st.Sessions[j].Session.LastActive
		if li.IsZero() {
			li = st.Sessions[i].Session.CreatedAt
		}
		if lj.IsZero() {
			lj = st.Sessions[j].Session.CreatedAt
		}
		return li.After(lj)
	})
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		log.Printf("session persistence: marshal failed: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.persistPath), 0o700); err != nil {
		log.Printf("session persistence: mkdir failed: %v", err)
		return
	}
	tmp := m.persistPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("session persistence: write failed: %v", err)
		return
	}
	backupPersistedState(m.persistPath)
	if err := os.Rename(tmp, m.persistPath); err != nil {
		log.Printf("session persistence: rename failed: %v", err)
	}
}

func readPersistedStateWithLog(path string) (persistedState, bool) {
	st, ok, err := readPersistedStateRaw(path)
	if errors.Is(err, os.ErrNotExist) {
		return persistedState{}, false
	}
	if err != nil {
		log.Printf("session persistence: read/parse %s failed: %v", path, err)
		return persistedState{}, false
	}
	return st, ok
}

func readPersistedState(path string) (persistedState, bool) {
	st, ok, err := readPersistedStateRaw(path)
	return st, ok && err == nil
}

func readPersistedStateRaw(path string) (persistedState, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return persistedState{}, false, err
	}
	var st persistedState
	if err := json.Unmarshal(b, &st); err != nil {
		return persistedState{}, false, err
	}
	if st.Sessions == nil {
		st.Sessions = []persistedSession{}
	}
	if st.History == nil {
		st.History = map[string][]core.AgentEvent{}
	}
	return st, true, nil
}

func backupPersistedState(path string) {
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return
	}
	backup := fmt.Sprintf("%s.bak-%s", path, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.WriteFile(backup, b, 0o600); err != nil {
		log.Printf("session persistence: backup failed: %v", err)
	}
	prunePersistedBackups(path, 10)
}

func prunePersistedBackups(path string, keep int) {
	matches, err := filepath.Glob(path + ".bak-*")
	if err != nil || len(matches) <= keep {
		return
	}
	sort.Strings(matches)
	for _, old := range matches[:len(matches)-keep] {
		_ = os.Remove(old)
	}
}

func defaultPersistPath() string {
	if dir := os.Getenv("AGENTBRIDGE_STATE_DIR"); dir != "" {
		return filepath.Join(dir, "sessions.json")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "agentbridge", "sessions.json")
	}
	return filepath.Join(".", ".agentbridge-sessions.json")
}

func detectHermesGatewayDir() string {
	home, _ := os.UserHomeDir()
	candidates := []string{filepath.Join(home, ".hermes", "hermes-agent"), filepath.Join(home, "hermes-agent"), filepath.Join(home, "dev", "hermes-agent")}
	for _, c := range candidates {
		if isHermesGatewayDir(c) {
			return c
		}
	}
	return ""
}

func isHermesGatewayDir(dir string) bool {
	if dir == "" {
		return false
	}
	if st, err := os.Stat(filepath.Join(dir, "tui_gateway")); err == nil && st.IsDir() {
		return true
	}
	if st, err := os.Stat(filepath.Join(dir, "tui_gateway", "entry.py")); err == nil && !st.IsDir() {
		return true
	}
	return false
}

func usableDir(dir string) string {
	if dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
		log.Printf("cwd %q unavailable, falling back", dir)
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "."
}

func newID(prefix string) string { return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()) }
