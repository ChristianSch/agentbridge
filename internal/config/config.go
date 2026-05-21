package config

import (
	"errors"
	"flag"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type ActivitySummaryConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Provider string        `yaml:"provider"`
	Endpoint string        `yaml:"endpoint"`
	APIKey   string        `yaml:"api_key"`
	Model    string        `yaml:"model"`
	Debounce time.Duration `yaml:"debounce"`
}

type Config struct {
	Bind  string `yaml:"bind"`
	Token string `yaml:"token"`
	Pi    struct {
		Binary      string        `yaml:"binary"`
		IdleTimeout time.Duration `yaml:"idle_timeout"`
	} `yaml:"pi"`
	Hermes struct {
		Venv          string        `yaml:"venv"`
		GatewayModule string        `yaml:"gateway_module"`
		Cwd           string        `yaml:"cwd"`
		IdleTimeout   time.Duration `yaml:"idle_timeout"`
	} `yaml:"hermes"`
	Terminal struct {
		Shell       string        `yaml:"shell"`
		IdleTimeout time.Duration `yaml:"idle_timeout"`
		MaxSessions int           `yaml:"max_sessions"`
	} `yaml:"terminal"`
	Auth struct {
		Passkeys bool     `yaml:"passkeys"`
		RPID     string   `yaml:"rp_id"`
		Origins  []string `yaml:"origins"`
	} `yaml:"auth"`
	Projects      []Project `yaml:"projects"`
	Notifications struct {
		Backend    string   `yaml:"backend"`
		NtfyTopic  string   `yaml:"ntfy_topic"`
		NtfyServer string   `yaml:"ntfy_server"`
		NotifyOn   []string `yaml:"notify_on"`
	} `yaml:"notifications"`
	ActivitySummary ActivitySummaryConfig `yaml:"activity_summary"`
}

type Project struct {
	Name  string `json:"name" yaml:"name"`
	Path  string `json:"path" yaml:"path"`
	Agent string `json:"agent" yaml:"agent"`
}

func LoadFromFlags() (Config, error) {
	path := flag.String("config", defaultPath(), "config file")
	flag.Parse()
	return Load(*path)
}

func Load(path string) (Config, error) {
	cfg := Config{Bind: "127.0.0.1:7777"}
	cfg.Pi.Binary = "pi"
	cfg.Terminal.Shell = os.Getenv("SHELL")
	if cfg.Terminal.Shell == "" {
		cfg.Terminal.Shell = "/bin/bash"
	}
	cfg.Terminal.MaxSessions = 10
	cfg.ActivitySummary.Enabled = true
	cfg.ActivitySummary.Debounce = 900 * time.Millisecond

	if b, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return cfg, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	if tok := os.Getenv("AGENTBRIDGE_TOKEN"); tok != "" {
		cfg.Token = tok
	}
	if provider := os.Getenv("AGENTBRIDGE_ACTIVITY_PROVIDER"); provider != "" {
		cfg.ActivitySummary.Provider = provider
	}
	if endpoint := os.Getenv("AGENTBRIDGE_ACTIVITY_ENDPOINT"); endpoint != "" {
		cfg.ActivitySummary.Endpoint = endpoint
	}
	if model := os.Getenv("AGENTBRIDGE_ACTIVITY_MODEL"); model != "" {
		cfg.ActivitySummary.Model = model
	}
	if key := os.Getenv("AGENTBRIDGE_ACTIVITY_API_KEY"); key != "" {
		cfg.ActivitySummary.APIKey = key
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); cfg.ActivitySummary.APIKey == "" && key != "" {
		cfg.ActivitySummary.APIKey = key
	}
	if key := os.Getenv("OPENAI_API_KEY"); cfg.ActivitySummary.APIKey == "" && key != "" {
		cfg.ActivitySummary.APIKey = key
	}
	return cfg, nil
}

func defaultPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/.config/agentbridge/config.yaml"
	}
	return "agentbridge.yaml"
}
