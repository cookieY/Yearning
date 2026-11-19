package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/realtime"
)

type sseFrame struct {
	Comment string
	ID      string
	Event   string
	Data    string
}

func newEventsTestServer(t *testing.T, hub *realtime.Hub) (*httptest.Server, *session.MemoryStore) {
	t.Helper()
	store := session.NewMemoryStore()
	engine := NewServeRouter(ServeDeps{
		Logger:        zap.NewNop(),
		Readiness:     NewReadiness(zap.NewNop()),
		SessionStore:  store,
		SessionOption: session.Options{CookieName: sessionCookieName, AnonymousAllowed: true},
		RealtimeHub:   hub,
	})
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return srv, store
}

func putTestSession(store *session.MemoryStore, token, userID, username, role string, isBuiltinAdmin bool) {
	store.Put(&session.Session{
		ID: token,
		Identity: &session.Identity{
			UserID:         userID,
			Username:       username,
			Role:           role,
			IsBuiltinAdmin: isBuiltinAdmin,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	})
}

func openSSEStream(t *testing.T, ctx context.Context, baseURL, token string) (*http.Response, <-chan sseFrame) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v4/events/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE connect failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("SSE status = %d, want 200", resp.StatusCode)
	}

	frames := make(chan sseFrame, 128)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(resp.Body)
		var cur sseFrame
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if cur != (sseFrame{}) {
					frames <- cur
					cur = sseFrame{}
				}
				continue
			}
			switch {
			case strings.HasPrefix(line, ":"):
				cur.Comment = strings.TrimSpace(strings.TrimPrefix(line, ":"))
			case strings.HasPrefix(line, "id:"):
				cur.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			case strings.HasPrefix(line, "event:"):
				cur.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				cur.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
	}()
	return resp, frames
}

func readFrame(t *testing.T, frames <-chan sseFrame, label string) sseFrame {
	t.Helper()
	select {
	case f, ok := <-frames:
		if !ok {
			t.Fatalf("%s: SSE stream closed unexpectedly", label)
		}
		return f
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: timed out waiting for SSE frame", label)
		return sseFrame{}
	}
}

func readInitialSSEFrames(t *testing.T, frames <-chan sseFrame, label string) {
	t.Helper()
	f1 := readFrame(t, frames, label+" comment")
	if f1.Comment != "connected" {
		t.Fatalf("%s: expected ': connected' comment, got %+v", label, f1)
	}
	f2 := readFrame(t, frames, label+" ready")
	if f2.Event != "ready" {
		t.Fatalf("%s: expected event=ready, got %+v", label, f2)
	}
	var handshake map[string]string
	if err := json.Unmarshal([]byte(f2.Data), &handshake); err != nil {
		t.Fatalf("%s: invalid ready JSON: %v", label, err)
	}
	if handshake["status"] != "ready" || handshake["transport"] != "sse" {
		t.Fatalf("%s: unexpected ready payload: %v", label, handshake)
	}
}

func TestEventsStreamUnauthenticatedAndJSONHandshake(t *testing.T) {
	hub := realtime.NewHub()
	srv, store := newEventsTestServer(t, hub)

	// 1. Unauthenticated caller must receive 401 Problem Details.
	unauthReq, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v4/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	unauthReq.Header.Set("Accept", "text/event-stream")
	unauthResp, err := http.DefaultClient.Do(unauthReq)
	if err != nil {
		t.Fatal(err)
	}
	unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", unauthResp.StatusCode)
	}
	if ct := unauthResp.Header.Get("Content-Type"); !strings.Contains(ct, "application/problem+json") {
		t.Fatalf("unauthenticated Content-Type = %q, want application/problem+json", ct)
	}

	// 2. Authenticated non-SSE caller receives EventStreamHandshake JSON envelope.
	putTestSession(store, "tok-handshake", "u-1", "alice", "user", false)
	jsonReq, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v4/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	jsonReq.Header.Set("Accept", "application/json")
	jsonReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "tok-handshake"})
	jsonResp, err := http.DefaultClient.Do(jsonReq)
	if err != nil {
		t.Fatal(err)
	}
	defer jsonResp.Body.Close()
	if jsonResp.StatusCode != http.StatusOK {
		t.Fatalf("JSON handshake status = %d, want 200", jsonResp.StatusCode)
	}
	var env map[string]any
	if err := json.NewDecoder(jsonResp.Body).Decode(&env); err != nil {
		t.Fatalf("decode JSON handshake: %v", err)
	}
	if code, ok := env["err_code"].(float64); !ok || code != 0 {
		t.Fatalf("JSON handshake err_code = %v, want 0", env["err_code"])
	}
	data, _ := env["data"].(map[string]any)
	if data["status"] != "ready" || data["transport"] != "sse" {
		t.Fatalf("JSON handshake data = %v, want ready/sse", data)
	}
}

func TestEventsStreamSSEHeadersAudienceIsolationAndDedup(t *testing.T) {
	hub := realtime.NewHub()
	srv, store := newEventsTestServer(t, hub)

	putTestSession(store, "tok-alice", "user-alice", "alice", "user", false)
	putTestSession(store, "tok-bob", "user-bob", "bob", "user", false)
	putTestSession(store, "tok-admin", "user-admin", "admin", "admin", true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	aliceResp, aliceFrames := openSSEStream(t, ctx, srv.URL, "tok-alice")
	defer aliceResp.Body.Close()
	if ct := aliceResp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := aliceResp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	if xab := aliceResp.Header.Get("X-Accel-Buffering"); xab != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", xab)
	}

	bobResp, bobFrames := openSSEStream(t, ctx, srv.URL, "tok-bob")
	defer bobResp.Body.Close()
	adminResp, adminFrames := openSSEStream(t, ctx, srv.URL, "tok-admin")
	defer adminResp.Body.Close()

	readInitialSSEFrames(t, aliceFrames, "alice")
	readInitialSSEFrames(t, bobFrames, "bob")
	readInitialSSEFrames(t, adminFrames, "admin")

	// Publish business event targeted strictly to alice (plus a duplicate ID).
	msg := realtime.BroadcastMessage{
		Envelope: realtime.EventEnvelope{
			ID:         "evt-sse-1",
			Type:       "query_grant.revoked",
			Subject:    "query-grants/grant-123",
			Aggregate:  "grant-123",
			State:      "revoked",
			OccurredAt: time.Now().UTC(),
		},
		TargetUserIDs: []string{"user-alice"},
		NotifyAdmins:  false,
	}
	hub.Publish(msg)
	hub.Publish(msg) // Duplicate must be suppressed by Hub.

	f := readFrame(t, aliceFrames, "alice business event")
	if f.ID != "evt-sse-1" || f.Event != "query_grant.revoked" {
		t.Fatalf("unexpected alice SSE frame: %+v", f)
	}
	if strings.Contains(f.Data, "target_user_ids") || strings.Contains(f.Data, "notify_admins") {
		t.Fatalf("internal routing metadata leaked on SSE wire: %s", f.Data)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(f.Data), &decoded); err != nil {
		t.Fatalf("invalid SSE JSON data: %v", err)
	}
	if decoded["aggregate_id"] != "grant-123" || decoded["state"] != "revoked" {
		t.Fatalf("unexpected envelope payload: %v", decoded)
	}

	// Verify dedup: alice must not receive a second copy.
	select {
	case extra := <-aliceFrames:
		t.Fatalf("alice received unexpected duplicate frame: %+v", extra)
	case <-time.After(60 * time.Millisecond):
	}
	// Verify audience isolation: bob and admin must receive zero frames.
	select {
	case extra := <-bobFrames:
		t.Fatalf("bob received unauthorized frame: %+v", extra)
	case <-time.After(60 * time.Millisecond):
	}
	select {
	case extra := <-adminFrames:
		t.Fatalf("admin received unauthorized business frame: %+v", extra)
	case <-time.After(60 * time.Millisecond):
	}

	// Admin-scoped event must reach admin, not alice or bob.
	hub.Publish(realtime.BroadcastMessage{
		Envelope: realtime.EventEnvelope{
			ID:         "evt-sse-admin",
			Type:       "flow.updated",
			Subject:    "flow:flow-9",
			Aggregate:  "flow-9",
			OccurredAt: time.Now().UTC(),
		},
		NotifyAdmins: true,
	})
	af := readFrame(t, adminFrames, "admin governance event")
	if af.ID != "evt-sse-admin" || af.Event != "flow.updated" {
		t.Fatalf("unexpected admin frame: %+v", af)
	}
	select {
	case extra := <-aliceFrames:
		t.Fatalf("alice received admin-only frame: %+v", extra)
	case <-time.After(60 * time.Millisecond):
	}
}

type blockingSSEWriter struct {
	mu          sync.Mutex
	header      http.Header
	buf         bytes.Buffer
	status      int
	flushCount  int
	readyCh     chan struct{}
	flushing2Ch chan struct{}
	gateCh      chan struct{}
	unblockOnce sync.Once
}

func newBlockingSSEWriter() *blockingSSEWriter {
	return &blockingSSEWriter{
		header:      make(http.Header),
		readyCh:     make(chan struct{}),
		flushing2Ch: make(chan struct{}),
		gateCh:      make(chan struct{}),
	}
}

func (w *blockingSSEWriter) Header() http.Header { return w.header }
func (w *blockingSSEWriter) WriteHeader(code int) {
	w.mu.Lock()
	w.status = code
	w.mu.Unlock()
}
func (w *blockingSSEWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
func (w *blockingSSEWriter) Flush() {
	w.mu.Lock()
	w.flushCount++
	cnt := w.flushCount
	w.mu.Unlock()
	if cnt == 1 {
		close(w.readyCh)
		return
	}
	if cnt == 2 {
		close(w.flushing2Ch)
		<-w.gateCh
	}
}
func (w *blockingSSEWriter) Unblock() {
	w.unblockOnce.Do(func() { close(w.gateCh) })
}
func (w *blockingSSEWriter) BodyString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestEventsStreamSlowConsumerResync(t *testing.T) {
	hub := realtime.NewHub()
	store := session.NewMemoryStore()
	engine := NewServeRouter(ServeDeps{
		Logger:        zap.NewNop(),
		Readiness:     NewReadiness(zap.NewNop()),
		SessionStore:  store,
		SessionOption: session.Options{CookieName: sessionCookieName, AnonymousAllowed: true},
		RealtimeHub:   hub,
	})
	putTestSession(store, "tok-slow", "user-slow", "slow", "user", false)

	bw := newBlockingSSEWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/v4/events/stream", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "tok-slow"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		engine.ServeHTTP(bw, req)
	}()

	// Wait for initial ready flush.
	<-bw.readyCh

	// First event enters Flush() #2 and pauses the handler goroutine before it can drain further.
	hub.Publish(realtime.BroadcastMessage{
		Envelope: realtime.EventEnvelope{
			ID:         "burst-sse-head",
			Type:       "change_order.approval_decided",
			Subject:    "change-orders/order-1",
			Aggregate:  "order-1",
			State:      "stage_approval_active",
			OccurredAt: time.Now().UTC(),
		},
		TargetUserIDs: []string{"user-slow"},
	})
	<-bw.flushing2Ch

	// Fill past the 64-slot bounded channel while the handler is paused in Flush() #2.
	for i := 0; i < realtime.SubscriberBufferSize+10; i++ {
		hub.Publish(realtime.BroadcastMessage{
			Envelope: realtime.EventEnvelope{
				ID:         fmt.Sprintf("burst-sse-%d", i),
				Type:       "change_order.approval_decided",
				Subject:    "change-orders/order-1",
				Aggregate:  "order-1",
				State:      "stage_approval_active",
				OccurredAt: time.Now().UTC(),
			},
			TargetUserIDs: []string{"user-slow"},
		})
	}

	bw.Unblock()

	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(bw.BodyString(), "event: resync\ndata: {\"reason\":\"buffer_overflow\"}\n\n") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("expected event: resync frame on SSE wire, got:\n%s", bw.BodyString())
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	<-done
}
