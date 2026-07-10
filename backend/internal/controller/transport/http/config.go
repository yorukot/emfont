package http

import (
	"time"

	"github.com/emfont/emfont/backend/internal/controller/transport/http/middleware"
)

type Config struct {
	Version        string
	ServiceName    string
	RequestTimeout time.Duration
	Security       middleware.SecurityConfig
	OpenAPI        OpenAPIConfig
	Tracing        bool
}

type ServerConfig struct {
	Addr              string
	ReadTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

type OpenAPIConfig struct {
	Version        string
	BackendBaseURL string
}

func (cfg Config) withDefaults() Config {
	if cfg.Version == "" {
		cfg.Version = "v1"
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = "emfont-backend"
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 15 * time.Second
	}
	if cfg.OpenAPI.Version == "" {
		cfg.OpenAPI.Version = cfg.Version
	}
	return cfg
}

func (cfg ServerConfig) withDefaults() ServerConfig {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 10 * time.Second
	}
	if cfg.ReadHeaderTimeout == 0 {
		cfg.ReadHeaderTimeout = 5 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 30 * time.Second
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 60 * time.Second
	}
	if cfg.ShutdownTimeout == 0 {
		cfg.ShutdownTimeout = 10 * time.Second
	}
	return cfg
}
