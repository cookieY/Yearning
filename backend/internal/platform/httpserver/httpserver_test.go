package httpserver

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/config"
)

func testServerConfig(graceSeconds int) config.ServerConfig {
	cfg := config.Defaults().Server
	cfg.ListenAddress = "127.0.0.1:0"
	cfg.ShutdownGraceSeconds = graceSeconds
	return cfg
}

func waitForAddr(t *testing.T, s *Server) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if addr := s.Addr(); addr != "127.0.0.1:0" {
			return addr
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("server did not report actual address")
	return ""
}

func TestGracefulShutdownWaitsForInFlightRequest(t *testing.T) {
	var handlerDone atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		handlerDone.Store(true)
		w.WriteHeader(http.StatusOK)
	})
	server := New(mux, testServerConfig(5))

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- server.Run(ctx) }()

	addr := waitForAddr(t, server)
	statusCh := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			statusCh <- 0
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		statusCh <- resp.StatusCode
	}()

	time.Sleep(100 * time.Millisecond) // Request has entered handler processing
	cancel()

	select {
	case status := <-statusCh:
		if status != http.StatusOK {
			t.Fatalf("in-flight request status = %d, want 200", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request did not complete during graceful shutdown")
	}
	if !handlerDone.Load() {
		t.Fatal("handler must finish before shutdown returns")
	}
	if err := <-runDone; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestShutdownRejectsNewRequests(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {})
	server := New(mux, testServerConfig(5))

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- server.Run(ctx) }()
	addr := waitForAddr(t, server)

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("new request after shutdown must be refused")
	}
}

func TestRunReturnsListenerError(t *testing.T) {
	blocker := New(http.NewServeMux(), testServerConfig(5))
	blockerCtx, blockerCancel := context.WithCancel(context.Background())
	go func() { _ = blocker.Run(blockerCtx) }()
	blockedAddr := waitForAddr(t, blocker)
	defer blockerCancel()

	dup := New(http.NewServeMux(), func() config.ServerConfig {
		cfg := testServerConfig(5)
		cfg.ListenAddress = blockedAddr
		return cfg
	}())
	err := dup.Run(context.Background())
	if err == nil {
		t.Fatal("listening on an occupied address must fail")
	}
}
