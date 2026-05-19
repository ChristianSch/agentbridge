package main

import (
	"log"
	"net/http"

	"github.com/agentbridge/agentbridge/internal/adapters/agent"
	httpadapter "github.com/agentbridge/agentbridge/internal/adapters/http"
	"github.com/agentbridge/agentbridge/internal/app"
	"github.com/agentbridge/agentbridge/internal/config"
	"github.com/agentbridge/agentbridge/internal/static"
)

func main() {
	cfg, err := config.LoadFromFlags()
	if err != nil {
		log.Fatal(err)
	}
	mgr := app.NewManager(cfg, agent.NewPiAdapter(), agent.NewHermesAdapter())
	srv := httpadapter.New(cfg, mgr, static.FS())
	log.Printf("agentbridge listening on %s", cfg.Bind)
	log.Fatal(http.ListenAndServe(cfg.Bind, srv.Handler()))
}
