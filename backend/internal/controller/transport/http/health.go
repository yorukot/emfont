package http

import (
	"context"
	stdhttp "net/http"
	"time"

	"github.com/emfont/emfont/backend/internal/controller/transport/http/httpx"
)

type HealthConfig struct {
	ServiceName    string
	Version        string
	ReadinessCheck func(context.Context) error
}

type HealthResponse struct {
	Status  string    `json:"status"`
	Service string    `json:"service"`
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
}

func HealthHandler(cfg HealthConfig) stdhttp.HandlerFunc {
	if cfg.ServiceName == "" {
		cfg.ServiceName = "emfont-controller"
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}

	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if cfg.ReadinessCheck != nil {
			if err := cfg.ReadinessCheck(r.Context()); err != nil {
				_ = httpx.WriteError(w, r, stdhttp.StatusServiceUnavailable, "dependency not ready")
				return
			}
		}

		_ = httpx.WriteJSON(w, stdhttp.StatusOK, HealthResponse{
			Status:  "ready",
			Service: cfg.ServiceName,
			Version: cfg.Version,
			Time:    time.Now().UTC(),
		})
	}
}
