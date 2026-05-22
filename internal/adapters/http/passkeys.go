package httpadapter

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/ChristianSch/agentbridge/internal/config"
)

type authManager struct {
	mu       sync.Mutex
	path     string
	wa       *webauthn.WebAuthn
	user     *passkeyUser
	ceremony map[string]webauthn.SessionData
	sessions map[string]time.Time
}

type passkeyUser struct {
	ID          []byte                `json:"id"`
	Name        string                `json:"name"`
	DisplayName string                `json:"display_name"`
	Credentials []webauthn.Credential `json:"credentials"`
}

func (u *passkeyUser) WebAuthnID() []byte                         { return u.ID }
func (u *passkeyUser) WebAuthnName() string                       { return u.Name }
func (u *passkeyUser) WebAuthnDisplayName() string                { return u.DisplayName }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

func newAuthManager(cfg config.Config) *authManager {
	if !cfg.Auth.Passkeys {
		return nil
	}
	rpID := cfg.Auth.RPID
	if rpID == "" {
		log.Printf("passkeys disabled: auth.rp_id is required")
		return nil
	}
	origins := cfg.Auth.Origins
	if len(origins) == 0 {
		origins = []string{"https://" + rpID}
	}
	wa, err := webauthn.New(&webauthn.Config{RPID: rpID, RPDisplayName: "AgentBridge", RPOrigins: origins, AuthenticatorSelection: protocol.AuthenticatorSelection{UserVerification: protocol.VerificationRequired}})
	if err != nil {
		log.Printf("passkeys disabled: %v", err)
		return nil
	}
	am := &authManager{path: authStatePath(), wa: wa, ceremony: map[string]webauthn.SessionData{}, sessions: map[string]time.Time{}}
	if err := am.load(); err != nil {
		log.Printf("passkey state load failed: %v", err)
	}
	return am
}

func authStatePath() string {
	if dir := os.Getenv("AGENTBRIDGE_STATE_DIR"); dir != "" {
		return filepath.Join(dir, "auth.json")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "agentbridge", "auth.json")
	}
	return ".agentbridge-auth.json"
}

func (a *authManager) load() error {
	b, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		id := make([]byte, 32)
		_, _ = rand.Read(id)
		a.user = &passkeyUser{ID: id, Name: "agentbridge", DisplayName: "AgentBridge"}
		return nil
	}
	if err != nil {
		return err
	}
	var u passkeyUser
	if err := json.Unmarshal(b, &u); err != nil {
		return err
	}
	a.user = &u
	return nil
}

func (a *authManager) save() error {
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a.user, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.path, b, 0o600)
}

func (a *authManager) hasCredential() bool {
	return a != nil && a.user != nil && len(a.user.Credentials) > 0
}

func (a *authManager) ownerID() string {
	if a == nil || a.user == nil || len(a.user.ID) == 0 {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(a.user.ID)
}

func (a *authManager) validSession(r *http.Request) bool {
	if a == nil {
		return false
	}
	c, err := r.Cookie("ab_session")
	if err != nil || c.Value == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[c.Value]
	if !ok || time.Now().After(exp) {
		delete(a.sessions, c.Value)
		return false
	}
	return true
}

func (a *authManager) setSession(w http.ResponseWriter, r *http.Request) {
	id := randString(32)
	a.mu.Lock()
	a.sessions[id] = time.Now().Add(24 * time.Hour)
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "ab_session", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), MaxAge: 86400})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func randString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"passkeys": s.authn != nil, "registered": s.authn != nil && s.authn.hasCredential(), "authenticated": s.hasAuth(r)})
}

func (s *Server) tokenLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Token != "" {
		r.Header.Set("Authorization", "Bearer "+req.Token)
	}
	if !s.requestTokenOK(r) {
		http.Error(w, "invalid AgentBridge token", http.StatusUnauthorized)
		return
	}
	if s.authn == nil {
		http.SetCookie(w, &http.Cookie{Name: "ab_token", Value: s.cfg.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), MaxAge: 86400})
		writeJSON(w, map[string]any{"ok": true, "passkeys": false, "registered": false, "authenticated": true})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "ab_token", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), MaxAge: -1})
	writeJSON(w, map[string]any{"ok": true, "passkeys": true, "registered": s.authn.hasCredential(), "authenticated": false})
}

func (s *Server) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if s.authn == nil {
		http.Error(w, "passkeys are not enabled", 404)
		return
	}
	if !s.tokenOK(r) && !s.authn.validSession(r) {
		http.Error(w, "setup requires the AgentBridge token", http.StatusUnauthorized)
		return
	}
	a := s.authn
	a.mu.Lock()
	creation, session, err := a.wa.BeginRegistration(a.user)
	if err == nil {
		a.ceremony["register"] = *session
	}
	a.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, creation)
}

func (s *Server) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if s.authn == nil {
		http.Error(w, "passkeys are not enabled", 404)
		return
	}
	if !s.tokenOK(r) && !s.authn.validSession(r) {
		http.Error(w, "setup requires the AgentBridge token", http.StatusUnauthorized)
		return
	}
	a := s.authn
	a.mu.Lock()
	session, ok := a.ceremony["register"]
	delete(a.ceremony, "register")
	a.mu.Unlock()
	if !ok {
		http.Error(w, "registration challenge expired", 400)
		return
	}
	cred, err := a.wa.FinishRegistration(a.user, session, r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	a.mu.Lock()
	a.user.Credentials = append(a.user.Credentials, *cred)
	err = a.save()
	ownerID := a.ownerID()
	a.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := s.store.ClaimSessions(ownerID); err != nil {
		log.Printf("claim sessions after passkey registration failed: %v", err)
	}
	a.setSession(w, r)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if s.authn == nil || !s.authn.hasCredential() {
		http.Error(w, "no passkey registered", 404)
		return
	}
	a := s.authn
	a.mu.Lock()
	assertion, session, err := a.wa.BeginLogin(a.user)
	if err == nil {
		a.ceremony["login"] = *session
	}
	a.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, assertion)
}

func (s *Server) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if s.authn == nil || !s.authn.hasCredential() {
		http.Error(w, "no passkey registered", 404)
		return
	}
	a := s.authn
	a.mu.Lock()
	session, ok := a.ceremony["login"]
	delete(a.ceremony, "login")
	a.mu.Unlock()
	if !ok {
		http.Error(w, "login challenge expired", 400)
		return
	}
	cred, err := a.wa.FinishLogin(a.user, session, r)
	if err != nil {
		http.Error(w, err.Error(), 401)
		return
	}
	a.mu.Lock()
	for i := range a.user.Credentials {
		if string(a.user.Credentials[i].ID) == string(cred.ID) {
			a.user.Credentials[i] = *cred
		}
	}
	_ = a.save()
	a.mu.Unlock()
	a.setSession(w, r)
	writeJSON(w, map[string]any{"ok": true})
}
