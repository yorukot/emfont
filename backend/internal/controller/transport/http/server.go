package http

import (
	"context"
	"errors"
	"net"
	stdhttp "net/http"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

type Server struct {
	server          *stdhttp.Server
	shutdownTimeout time.Duration
	log             *zap.Logger
	serving         atomic.Bool
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
			MaxHeaderBytes:    cfg.MaxHeaderBytes,
		},
		shutdownTimeout: cfg.ShutdownTimeout,
		log:             log,
	}
}

func (s *Server) ListenAndServe() error {
	if s == nil || s.server == nil {
		return errors.New("http server is not configured")
	}

	listener, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return err
	}
	s.serving.Store(true)
	defer s.serving.Store(false)

	s.log.Info("http server listening", zap.String("addr", listener.Addr().String()))
	err = s.server.Serve(listener)
	if errors.Is(err, stdhttp.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) IsServing() bool {
	return s != nil && s.serving.Load()
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
	if err := s.server.Shutdown(ctx); err != nil {
		return errors.Join(err, s.server.Close())
	}
	return nil
}

func (s *Server) HTTPServer() *stdhttp.Server {
	if s == nil {
		return nil
	}
	return s.server
}
