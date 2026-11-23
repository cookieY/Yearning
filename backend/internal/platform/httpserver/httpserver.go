// Package httpserver manages the HTTP server lifecycle: listening, timeouts, and graceful shutdown.
// Legacy Yearning called http.ListenAndServe directly without signal handling; v4 stops accepting
// new requests on SIGTERM and waits within the grace period for in-flight requests to complete before releasing resources.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/cookieY/YearningX/backend/internal/config"
)

// Server wraps http.Server with context-driven graceful shutdown.
type Server struct {
	srv      *http.Server
	grace    time.Duration
	actual   atomic.Value
	OnListen func(net.Addr)
}

// Addr returns the bound listener address; when configured with port :0, returns the kernel-assigned port after Run starts.
func (s *Server) Addr() string {
	if v := s.actual.Load(); v != nil {
		return v.(string)
	}
	return s.srv.Addr
}

// New constructs a Server from configuration with all timeouts explicitly set to prevent unbounded connections.
func New(handler http.Handler, cfg config.ServerConfig) *Server {
	return &Server{
		srv: &http.Server{
			Addr:              cfg.ListenAddress,
			Handler:           handler,
			ReadHeaderTimeout: time.Duration(cfg.ReadHeaderTimeoutSec) * time.Second,
			ReadTimeout:       time.Duration(cfg.ReadTimeoutSeconds) * time.Second,
			WriteTimeout:      time.Duration(cfg.WriteTimeoutSeconds) * time.Second,
			IdleTimeout:       time.Duration(cfg.IdleTimeoutSeconds) * time.Second,
		},
		grace: cfg.ShutdownGrace(),
	}
}

// Run serves HTTP requests until ctx is cancelled or the listener fails.
// After ctx cancellation, waits within the grace window for in-flight requests to drain before forcing close on timeout.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.srv.Addr, err)
	}
	s.actual.Store(listener.Addr().String())
	if s.OnListen != nil {
		s.OnListen(listener.Addr())
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- s.srv.Serve(listener) }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.grace)
		defer cancel()
		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			_ = s.srv.Close()
			return fmt.Errorf("graceful shutdown within %s: %w", s.grace, err)
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
