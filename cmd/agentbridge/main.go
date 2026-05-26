package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"strings"

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
	warnSecurity(cfg.Bind, cfg.Token)
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
	log.Printf("agentbridge listening on %s", cfg.Bind)
	log.Fatal(http.ListenAndServe(cfg.Bind, srv.Handler()))
}

func setenv(key, value string) error { return os.Setenv(key, value) }

func warnSecurity(bind, token string) {
	if token == "" {
		log.Printf("SECURITY WARNING: token auth is disabled; AgentBridge will stay locked unless passkeys are enabled or auth.allow_insecure_no_auth is explicitly set")
	} else if token == "change-me" || token == "dev" {
		log.Printf("SECURITY WARNING: token %q is for development only; use a high-entropy token", token)
	}
	host := bind
	if h, _, err := net.SplitHostPort(bind); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		log.Printf("SECURITY WARNING: bind address %q listens broadly; prefer 127.0.0.1 with Tailscale Serve in front", bind)
		return
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		log.Printf("SECURITY WARNING: bind address %q is not loopback; prefer 127.0.0.1 with Tailscale Serve in front", bind)
	}
}
