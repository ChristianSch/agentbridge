package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
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

type ByteSize int64

func (b *ByteSize) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		var n int64
		if err := value.Decode(&n); err != nil {
			return err
		}
		*b = ByteSize(n)
		return nil
	}
	n, err := parseByteSize(raw)
	if err != nil {
		return err
	}
	*b = ByteSize(n)
	return nil
}

func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, nil
	}
	units := []struct {
		suffix string
		mul    int64
	}{{"gib", 1024 * 1024 * 1024}, {"mib", 1024 * 1024}, {"kib", 1024}, {"gb", 1000 * 1000 * 1000}, {"mb", 1000 * 1000}, {"kb", 1000}, {"b", 1}}
	for _, unit := range units {
		if strings.HasSuffix(s, unit.suffix) {
			f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, unit.suffix)), 64)
			if err != nil {
				return 0, err
			}
			return int64(f * float64(unit.mul)), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte size %q", s)
	}
	return n, nil
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
	Attachments struct {
		Enabled          bool          `yaml:"enabled"`
		MaxSize          ByteSize      `yaml:"max_size"`
		AllowedMimeTypes []string      `yaml:"allowed_mime_types"`
		Retention        time.Duration `yaml:"retention"`
	} `yaml:"attachments"`
	Voice struct {
		Enabled  bool          `yaml:"enabled"`
		Backend  string        `yaml:"backend"`
		Binary   string        `yaml:"binary"`
		Model    string        `yaml:"model"`
		Language string        `yaml:"language"`
		Threads  int           `yaml:"threads"`
		Timeout  time.Duration `yaml:"timeout"`
	} `yaml:"voice"`
	Auth struct {
		Passkeys            bool     `yaml:"passkeys"`
		RPID                string   `yaml:"rp_id"`
		Origins             []string `yaml:"origins"`
		AllowInsecureNoAuth bool     `yaml:"allow_insecure_no_auth"`
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
	cfg.Attachments.Enabled = true
	cfg.Attachments.MaxSize = ByteSize(25 * 1000 * 1000)
	cfg.Attachments.AllowedMimeTypes = []string{"image/png", "image/jpeg", "image/webp", "image/gif", "text/plain", "text/markdown", "application/pdf", "audio/webm", "audio/wav", "audio/mpeg", "audio/mp4"}
	cfg.Attachments.Retention = 168 * time.Hour
	cfg.Voice.Enabled = true
	cfg.Voice.Backend = "whispercpp"
	cfg.Voice.Binary = "whisper-cli"
	cfg.Voice.Language = "auto"
	cfg.Voice.Timeout = 2 * time.Minute
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
