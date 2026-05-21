package httpadapter

import (
	"crypto/subtle"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/ChristianSch/agentbridge/internal/config"
	"github.com/ChristianSch/agentbridge/internal/core"
)

type Server struct {
	cfg      config.Config
	store    core.SessionStore
	frontend fs.FS
	authn    *authManager
	upgrader websocket.Upgrader
}

func New(cfg config.Config, store core.SessionStore, frontend fs.FS) *Server {
	return &Server{cfg: cfg, store: store, frontend: frontend, authn: newAuthManager(cfg), upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", s.loginPage)
	mux.HandleFunc("/auth/status", s.authStatus)
	mux.HandleFunc("/auth/passkey/register/begin", s.passkeyRegisterBegin)
	mux.HandleFunc("/auth/passkey/register/finish", s.passkeyRegisterFinish)
	mux.HandleFunc("/auth/passkey/login/begin", s.passkeyLoginBegin)
	mux.HandleFunc("/auth/passkey/login/finish", s.passkeyLoginFinish)
	mux.HandleFunc("/api/health", s.auth(s.health))
	mux.HandleFunc("/api/projects", s.auth(s.projects))
	mux.HandleFunc("/api/browse", s.auth(s.browse))
	mux.HandleFunc("/api/detect", s.auth(s.detect))
	mux.HandleFunc("/api/sessions", s.auth(s.sessions))
	mux.HandleFunc("/api/sessions/", s.auth(s.sessionByID))
	mux.HandleFunc("/ws", s.auth(s.ws))
	mux.HandleFunc("/ws/term/", s.auth(s.termWS))
	mux.Handle("/", s.noCache(s.uiAuth(http.FileServer(http.FS(s.frontend)))))
	return mux
}

func (s *Server) noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) tokenOK(r *http.Request) bool {
	if s.cfg.Token == "" {
		return false
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.Token)) == 1
}

func (s *Server) hasAuth(r *http.Request) bool {
	return s.tokenOK(r) || (s.authn != nil && s.authn.validSession(r))
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" || s.authn != nil {
			if !s.hasAuth(r) {
				log.Printf("http unauthorized %s %s remote=%s", r.Method, r.URL.Path, r.RemoteAddr)
				http.Error(w, "AgentBridge locked: authenticate with a passkey at /login or provide a valid Bearer token / ?token=...", http.StatusUnauthorized)
				return
			}
		}
		log.Printf("http %s %s remote=%s", r.Method, r.URL.Path, r.RemoteAddr)
		next(w, r)
	}
}

func (s *Server) uiAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		if (s.cfg.Token != "" || s.authn != nil) && !s.hasAuth(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true})
}
func (s *Server) projects(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.existingProjects()) }

func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	requested := r.URL.Query().Get("path")
	path := usableBrowsePath(requested)
	if requested != "" && requested != path {
		log.Printf("browse fallback requested=%q using=%q", requested, path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		log.Printf("browse failed requested=%q path=%q: %v", requested, path, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dirs := []map[string]string{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dirs = append(dirs, map[string]string{"name": e.Name(), "path": filepath.Join(path, e.Name())})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i]["name"] < dirs[j]["name"] })
	writeJSON(w, map[string]any{"path": path, "parent": filepath.Dir(path), "dirs": dirs})
}

func usableBrowsePath(path string) string {
	candidates := []string{path}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, home)
	}
	candidates = append(candidates, ".")
	for _, p := range candidates {
		if p == "" {
			continue
		}
		p = filepath.Clean(p)
		if st, err := os.Stat(p); err == nil {
			if st.IsDir() {
				return p
			}
			if parent := filepath.Dir(p); parent != "" {
				return parent
			}
		}
	}
	return "."
}

func (s *Server) detect(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	hermes := []map[string]string{}
	candidates := []string{s.cfg.Hermes.Cwd, cwd, filepath.Join(home, ".hermes", "hermes-agent"), filepath.Join(home, "hermes-agent"), filepath.Join(home, "dev/hermes-agent"), filepath.Join(home, ".hermes")}
	for _, base := range []string{filepath.Join(home, "dev"), filepath.Join(home, "Developer"), filepath.Join(home, "src")} {
		if entries, err := os.ReadDir(base); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					candidates = append(candidates, filepath.Join(base, e.Name()))
				}
			}
		}
	}
	seen := map[string]bool{}
	for _, p := range candidates {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if isHermesRepo(p) {
			hermes = append(hermes, map[string]string{"name": filepath.Base(p), "path": p})
		}
	}
	writeJSON(w, map[string]any{"home": home, "cwd": cwd, "projects": s.existingProjects(), "hermes_profiles": hermes})
}

func isHermesRepo(path string) bool {
	st, err := os.Stat(filepath.Join(path, "tui_gateway"))
	if err == nil && st.IsDir() {
		return true
	}
	if _, err := os.Stat(filepath.Join(path, "tui_gateway", "entry.py")); err == nil {
		return true
	}
	return false
}

func (s *Server) existingProjects() []config.Project {
	out := []config.Project{}
	for _, p := range s.cfg.Projects {
		if st, err := os.Stat(p.Path); err == nil && st.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.store.List())
	case http.MethodPost:
		var req struct {
			Kind                       core.AgentKind `json:"kind"`
			Name, Cwd, Shell, ResumeID string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var sess *core.Session
		var err error
		if req.Kind == core.AgentTerminal {
			sess, err = s.store.CreateTerminal(r.Context(), req.Name, req.Cwd, req.Shell)
		} else {
			sess, err = s.store.CreateAgent(r.Context(), req.Kind, req.Name, req.Cwd, req.ResumeID)
		}
		if err != nil {
			log.Printf("create session failed kind=%q name=%q cwd=%q: %v", req.Kind, req.Name, req.Cwd, err)
			http.Error(w, err.Error(), 500)
			return
		}
		log.Printf("created session id=%s kind=%s name=%q cwd=%q", sess.ID, sess.Kind, sess.Name, sess.Cwd)
		writeJSON(w, sess)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) sessionByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	switch r.Method {
	case http.MethodPatch:
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		sess, err := s.store.Rename(id, req.Name)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		writeJSON(w, sess)
	case http.MethodDelete:
		log.Printf("delete session id=%s", id)
		if err := s.store.Kill(id); err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	log.Printf("ws agent connect remote=%s", r.RemoteAddr)
	c, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	cancels := map[string]func(){}
	var writeMu sync.Mutex
	write := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = c.WriteJSON(v)
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()

	for {
		var msg map[string]json.RawMessage
		if err := c.ReadJSON(&msg); err != nil {
			return
		}
		if raw, ok := msg["subscribe"]; ok {
			var id string
			_ = json.Unmarshal(raw, &id)
			log.Printf("ws subscribe session=%s", id)
			events, cancel, err := s.store.Subscribe(id)
			if err != nil {
				write(map[string]any{"event": "error", "content": err.Error()})
				continue
			}
			cancels[id] = cancel
			go func() {
				for ev := range events {
					write(ev)
				}
			}()
			continue
		}
		if raw, ok := msg["unsubscribe"]; ok {
			var id string
			_ = json.Unmarshal(raw, &id)
			if cancel := cancels[id]; cancel != nil {
				cancel()
				delete(cancels, id)
			}
			continue
		}
		var cmd core.ClientCommand
		b, _ := json.Marshal(msg)
		if err := json.Unmarshal(b, &cmd); err != nil {
			write(map[string]any{"event": "error", "content": err.Error()})
			continue
		}
		log.Printf("ws command action=%s session=%s text_len=%d", cmd.Action, cmd.SessionID, len(cmd.Text))
		if err := s.store.Send(cmd); err != nil {
			write(map[string]any{"event": "error", "content": err.Error()})
		}
	}
}

func (s *Server) termWS(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/ws/term/")
	log.Printf("ws terminal connect session=%s remote=%s", id, r.RemoteAddr)
	term, detach, err := s.store.Terminal(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	c, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	defer detach()

	done := make(chan struct{})
	var writeMu sync.Mutex
	go func() {
		defer close(done)
		for b := range term.Output() {
			writeMu.Lock()
			_ = c.WriteMessage(websocket.BinaryMessage, b)
			writeMu.Unlock()
		}
	}()

	for {
		select {
		case <-done:
			return
		default:
		}
		mt, payload, err := c.ReadMessage()
		if err != nil {
			return
		}
		if mt == websocket.TextMessage {
			var ctrl struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(payload, &ctrl) == nil && ctrl.Type == "resize" {
				_ = term.Resize(ctrl.Cols, ctrl.Rows)
				continue
			}
		}
		if _, err := term.Write(payload); err != nil {
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
