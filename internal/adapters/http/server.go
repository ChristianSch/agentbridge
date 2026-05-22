package httpadapter

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"mime"
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
	cfg         config.Config
	store       core.SessionStore
	attachments core.AttachmentStore
	transcriber core.Transcriber
	frontend    fs.FS
	authn       *authManager
	upgrader    websocket.Upgrader
}

func New(cfg config.Config, store core.SessionStore, frontend fs.FS, attachments core.AttachmentStore, transcriber core.Transcriber) *Server {
	_ = mime.AddExtensionType(".css", "text/css; charset=utf-8")
	_ = mime.AddExtensionType(".js", "application/javascript; charset=utf-8")
	return &Server{cfg: cfg, store: store, attachments: attachments, transcriber: transcriber, frontend: frontend, authn: newAuthManager(cfg), upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}}
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
	mux.HandleFunc("/api/attachments", s.auth(s.uploadAttachment))
	mux.HandleFunc("/api/attachments/", s.auth(s.attachmentByID))
	mux.HandleFunc("/api/transcribe", s.auth(s.transcribe))
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
	return s.requestTokenOK(r) || s.cookieTokenOK(r)
}

func (s *Server) requestTokenOK(r *http.Request) bool {
	if s.cfg.Token == "" {
		return false
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.Token)) == 1
}

func (s *Server) cookieTokenOK(r *http.Request) bool {
	if s.cfg.Token == "" {
		return false
	}
	c, err := r.Cookie("ab_token")
	if err != nil || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.cfg.Token)) == 1
}

func (s *Server) persistTokenCookie(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Token == "" || r.URL.Query().Get("token") == "" || !s.requestTokenOK(r) {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "ab_token", Value: s.cfg.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), MaxAge: 86400})
}

func (s *Server) hasAuth(r *http.Request) bool {
	return s.tokenOK(r) || (s.authn != nil && s.authn.validSession(r))
}

func (s *Server) ownerID(r *http.Request) string {
	if s.authn != nil && (s.authn.validSession(r) || s.tokenOK(r)) {
		return s.authn.ownerID()
	}
	if s.tokenOK(r) {
		return "token"
	}
	return ""
}

func visibleToOwner(sess core.Session, ownerID string) bool {
	return sess.OwnerID == "" || ownerID == "" || sess.OwnerID == ownerID
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
		s.persistTokenCookie(w, r)
		log.Printf("http %s %s remote=%s", r.Method, r.URL.Path, r.RemoteAddr)
		next(w, r.WithContext(core.WithOwnerID(r.Context(), s.ownerID(r))))
	}
}

func (s *Server) uiAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/auth/") || isPublicAsset(r.URL.Path) {
			s.persistTokenCookie(w, r)
			next.ServeHTTP(w, r)
			return
		}
		if (s.cfg.Token != "" || s.authn != nil) && !s.hasAuth(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		s.persistTokenCookie(w, r)
		next.ServeHTTP(w, r.WithContext(core.WithOwnerID(r.Context(), s.ownerID(r))))
	})
}

func isPublicAsset(path string) bool {
	switch path {
	case "/app.js", "/app.css", "/favicon.ico":
		return true
	default:
		return false
	}
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
		ownerID := core.OwnerID(r.Context())
		list := []core.Session{}
		for _, sess := range s.store.List() {
			if visibleToOwner(sess, ownerID) {
				list = append(list, sess)
			}
		}
		writeJSON(w, list)
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
		if sess, ok := s.store.Get(id); !ok || !visibleToOwner(sess, core.OwnerID(r.Context())) {
			http.Error(w, "session not found", 404)
			return
		}
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
		if sess, ok := s.store.Get(id); !ok || !visibleToOwner(sess, core.OwnerID(r.Context())) {
			http.Error(w, "session not found", 404)
			return
		}
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

func (s *Server) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Attachments.Enabled || s.attachments == nil {
		http.Error(w, "attachments disabled", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if max := int64(s.cfg.Attachments.MaxSize); max > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, max+1024*1024)
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	att, err := s.attachments.Save(r.Context(), file, core.AttachmentMeta{SessionID: r.FormValue("session_id"), FileName: header.Filename, MimeType: header.Header.Get("Content-Type"), Size: header.Size})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	att.Path = ""
	writeJSON(w, att)
}

func (s *Server) attachmentByID(w http.ResponseWriter, r *http.Request) {
	if s.attachments == nil {
		http.Error(w, "attachments disabled", http.StatusNotFound)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/attachments/")
	switch r.Method {
	case http.MethodGet:
		att, err := s.attachments.Get(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		att.Path = ""
		writeJSON(w, att)
	case http.MethodDelete:
		_ = s.attachments.Delete(r.Context(), id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) transcribe(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Voice.Enabled || s.transcriber == nil || s.attachments == nil {
		http.Error(w, "voice transcription disabled", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if max := int64(s.cfg.Attachments.MaxSize); max > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, max+1024*1024)
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	att, err := s.attachments.Save(r.Context(), file, core.AttachmentMeta{SessionID: r.FormValue("session_id"), FileName: header.Filename, MimeType: header.Header.Get("Content-Type"), Size: header.Size})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tr, err := s.transcriber.Transcribe(r.Context(), att)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	att.ExtractedText = tr.Text
	att.Path = ""
	writeJSON(w, map[string]any{"text": tr.Text, "attachment": att, "transcript": tr})
}

func (s *Server) resolveAttachments(ctx context.Context, ids []string) ([]core.Attachment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if s.attachments == nil {
		return nil, nil
	}
	out := []core.Attachment{}
	for _, id := range ids {
		att, err := s.attachments.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if att.Kind == core.AttachmentImage {
			rc, err := s.attachments.Open(ctx, id)
			if err != nil {
				return nil, err
			}
			b, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil {
				return nil, err
			}
			att.Content = base64.StdEncoding.EncodeToString(b)
		}
		out = append(out, att)
	}
	return out, nil
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
			if sess, ok := s.store.Get(id); !ok || !visibleToOwner(sess, core.OwnerID(r.Context())) {
				write(map[string]any{"event": "error", "content": "session not found"})
				continue
			}
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
		if sess, ok := s.store.Get(cmd.SessionID); !ok || !visibleToOwner(sess, core.OwnerID(r.Context())) {
			write(map[string]any{"event": "error", "content": "session not found"})
			continue
		}
		if len(cmd.AttachmentIDs) > 0 {
			atts, err := s.resolveAttachments(r.Context(), cmd.AttachmentIDs)
			if err != nil {
				write(map[string]any{"event": "error", "content": err.Error()})
				continue
			}
			cmd.Attachments = atts
		}
		log.Printf("ws command action=%s session=%s text_len=%d attachments=%d", cmd.Action, cmd.SessionID, len(cmd.Text), len(cmd.Attachments))
		if err := s.store.Send(cmd); err != nil {
			write(map[string]any{"event": "error", "content": err.Error()})
		}
	}
}

func (s *Server) termWS(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/ws/term/")
	log.Printf("ws terminal connect session=%s remote=%s", id, r.RemoteAddr)
	if sess, ok := s.store.Get(id); !ok || !visibleToOwner(sess, core.OwnerID(r.Context())) {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
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
