package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"time"

	"go.uber.org/zap"
)

type Server struct {
	server          *stdhttp.Server
	shutdownTimeout time.Duration
	log             *zap.Logger
}

func NewServer(cfg ServerConfig, handler stdhttp.Handler, log *zap.Logger) *Server {
	cfg = cfg.withDefaults()
	if log == nil {
		log = zap.NewNop()
	}

	return &Server{
		server: &stdhttp.Server{
			Addr:              cfg.Addr,
			Handler:           handler,
			ReadTimeout:       cfg.ReadTimeout,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
		},
		shutdownTimeout: cfg.ShutdownTimeout,
		log:             log,
	}
}

func (s *Server) ListenAndServe() error {
	if s == nil || s.server == nil {
		return errors.New("http server is not configured")
	}

	s.log.Info("http server listening", zap.String("addr", s.server.Addr))
	err := s.server.ListenAndServe()
	if errors.Is(err, stdhttp.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok && s.shutdownTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.shutdownTimeout)
		defer cancel()
	}

	s.log.Info("http server shutting down")
	return s.server.Shutdown(ctx)
}

func (s *Server) HTTPServer() *stdhttp.Server {
	if s == nil {
		return nil
	}
	return s.server
}
