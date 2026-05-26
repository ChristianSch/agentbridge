package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ChristianSch/agentbridge/internal/adapters/agent"
	httpadapter "github.com/ChristianSch/agentbridge/internal/adapters/http"
	"github.com/ChristianSch/agentbridge/internal/adapters/llm"
	"github.com/ChristianSch/agentbridge/internal/adapters/storage"
	"github.com/ChristianSch/agentbridge/internal/adapters/transcribe"
	"github.com/ChristianSch/agentbridge/internal/app"
	"github.com/ChristianSch/agentbridge/internal/config"
	"github.com/ChristianSch/agentbridge/internal/static"
)

func main() {
	cfg, err := config.LoadFromFlags()
	if err != nil {
		log.Fatal(err)
	}
	warnSecurity(cfg)
	if ext, err := agent.EnsurePiAttachmentExtension(""); err == nil {
		log.Printf("pi attachment extension: %s", ext)
		_ = setenv(agent.PiAttachmentExtensionEnv, ext)
	} else {
		log.Printf("pi attachment extension disabled: %v", err)
	}
	mgr := app.NewManager(cfg, llm.NewActivitySummarizer(cfg.ActivitySummary), agent.NewPiAdapter(), agent.NewHermesAdapter())
	attachments, err := storage.NewLocalAttachmentStore("", int64(cfg.Attachments.MaxSize), cfg.Attachments.AllowedMimeTypes)
	if err != nil {
		log.Fatal(err)
	}
	var transcriber = transcribe.WhisperCPP{Binary: cfg.Voice.Binary, Model: cfg.Voice.Model, Language: cfg.Voice.Language, Threads: cfg.Voice.Threads, Timeout: cfg.Voice.Timeout}
	srv := httpadapter.New(cfg, mgr, static.FS(), attachments, transcriber)
	server := &http.Server{
		Addr:    cfg.Bind,
		Handler: srv.Handler(),
		// WebSocket connections are hijacked after the HTTP upgrade; these
		// timeouts protect normal HTTP requests and the upgrade handshake.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	log.Printf("agentbridge listening on %s", cfg.Bind)
	log.Fatal(server.ListenAndServe())
}

func setenv(key, value string) error { return os.Setenv(key, value) }

func warnSecurity(cfg config.Config) {
	if cfg.Token == "" {
		if cfg.Auth.AllowInsecureNoAuth {
			log.Printf("SECURITY WARNING: token auth is disabled and auth.allow_insecure_no_auth is enabled")
		} else {
			log.Printf("SECURITY WARNING: token auth is disabled; AgentBridge will stay locked unless passkeys are enabled or auth.allow_insecure_no_auth is explicitly set")
		}
	} else if cfg.Token == "change-me" || cfg.Token == "dev" {
		log.Printf("SECURITY WARNING: token %q is for development only; use a high-entropy token", cfg.Token)
	}
	host := cfg.Bind
	if h, _, err := net.SplitHostPort(cfg.Bind); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		log.Printf("SECURITY WARNING: bind address %q listens broadly; prefer 127.0.0.1 with Tailscale Serve in front", cfg.Bind)
		return
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		log.Printf("SECURITY WARNING: bind address %q is not loopback; prefer 127.0.0.1 with Tailscale Serve in front", cfg.Bind)
	}
}
