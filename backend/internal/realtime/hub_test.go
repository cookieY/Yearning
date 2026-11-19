package realtime

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestHubAudienceIsolationAndDefaultDeny(t *testing.T) {
	hub := NewHub()

	aliceCh, unsubAlice := hub.Subscribe("user-alice", "user", false)
	defer unsubAlice()
	bobCh, unsubBob := hub.Subscribe("user-bob", "user", false)
	defer unsubBob()
	adminCh, unsubAdmin := hub.Subscribe("user-admin", "admin", true)
	defer unsubAdmin()
	roleAdminCh, unsubRoleAdmin := hub.Subscribe("user-ops", "admin", false)
	defer unsubRoleAdmin()

	// Empty TargetUserIDs and NotifyAdmins=false must default-deny everyone.
	hub.Publish(BroadcastMessage{
		Envelope: EventEnvelope{
			ID:         "evt-empty",
			Type:       "change_order.submitted",
			Subject:    "change-orders/order-1",
			Aggregate:  "order-1",
			State:      "submitted",
			OccurredAt: time.Now().UTC(),
		},
	})
	assertNoDelivery(t, aliceCh, "alice empty target")
	assertNoDelivery(t, bobCh, "bob empty target")
	assertNoDelivery(t, adminCh, "admin empty target")
	assertNoDelivery(t, roleAdminCh, "role admin empty target")

	// Targeted delivery must reach only alice (never bob or admins).
	hub.Publish(BroadcastMessage{
		Envelope: EventEnvelope{
			ID:         "evt-alice",
			Type:       "query_grant.revoked",
			Subject:    "query-grants/grant-1",
			Aggregate:  "grant-1",
			State:      "revoked",
			OccurredAt: time.Now().UTC(),
		},
		TargetUserIDs: []string{"user-alice"},
		NotifyAdmins:  false,
	})
	got := mustRecvDelivery(t, aliceCh, "alice targeted")
	if got.Resync || got.Envelope.ID != "evt-alice" || got.Envelope.Aggregate != "grant-1" {
		t.Fatalf("unexpected delivery for alice: %+v", got)
	}
	assertNoDelivery(t, bobCh, "bob must not receive alice event")
	assertNoDelivery(t, adminCh, "builtin admin must not receive business event")
	assertNoDelivery(t, roleAdminCh, "role admin must not receive business event")

	// NotifyAdmins=true delivers to both isBuiltinAdmin and role=admin, never to regular users.
	hub.Publish(BroadcastMessage{
		Envelope: EventEnvelope{
			ID:         "evt-admin",
			Type:       "flow.updated",
			Subject:    "flow:flow-1",
			Aggregate:  "flow-1",
			OccurredAt: time.Now().UTC(),
		},
		NotifyAdmins: true,
	})
	if d := mustRecvDelivery(t, adminCh, "builtin admin"); d.Envelope.ID != "evt-admin" {
		t.Fatalf("unexpected builtin admin delivery: %+v", d)
	}
	if d := mustRecvDelivery(t, roleAdminCh, "role admin"); d.Envelope.ID != "evt-admin" {
		t.Fatalf("unexpected role admin delivery: %+v", d)
	}
	assertNoDelivery(t, aliceCh, "alice must not receive admin-only event")
	assertNoDelivery(t, bobCh, "bob must not receive admin-only event")
}

func TestHubDedupBoundedRing(t *testing.T) {
	hub := NewHub()
	ch, unsub := hub.Subscribe("user-1", "user", false)
	defer unsub()

	now := time.Now().UTC()
	msg := BroadcastMessage{
		Envelope: EventEnvelope{
			ID:         "dup-1",
			Type:       "change_order.submitted",
			Subject:    "change-orders/o1",
			Aggregate:  "o1",
			OccurredAt: now,
		},
		TargetUserIDs: []string{"user-1"},
	}
	hub.Publish(msg)
	hub.Publish(msg) // Duplicate ID must be dropped.

	d := mustRecvDelivery(t, ch, "first dup-1")
	if d.Envelope.ID != "dup-1" {
		t.Fatalf("got %q, want dup-1", d.Envelope.ID)
	}
	assertNoDelivery(t, ch, "duplicate dup-1 must not be delivered")

	// Publish DedupRingSize unique events to evict "dup-1" from the bounded 2048-entry ring.
	for i := 0; i < DedupRingSize; i++ {
		hub.Publish(BroadcastMessage{
			Envelope: EventEnvelope{
				ID:         fmt.Sprintf("ring-%d", i),
				Type:       "change_order.submitted",
				Subject:    "change-orders/o1",
				Aggregate:  "o1",
				OccurredAt: now,
			},
			TargetUserIDs: []string{"user-1"},
		})
		<-ch
	}

	// Now "dup-1" has been evicted from the bounded ring and can be accepted again.
	hub.Publish(msg)
	d2 := mustRecvDelivery(t, ch, "evicted dup-1 re-accepted")
	if d2.Envelope.ID != "dup-1" {
		t.Fatalf("got %q, want dup-1 after ring eviction", d2.Envelope.ID)
	}
}

func TestHubSlowConsumerResyncWithoutBlocking(t *testing.T) {
	hub := NewHub()
	slowCh, unsubSlow := hub.Subscribe("slow-user", "user", false)
	defer unsubSlow()
	fastCh, unsubFast := hub.Subscribe("fast-user", "user", false)
	defer unsubFast()

	if cap(slowCh) != SubscriberBufferSize {
		t.Fatalf("subscriber buffer cap = %d, want %d", cap(slowCh), SubscriberBufferSize)
	}

	// Fill beyond capacity without draining slowCh; Publish must never block and fastCh must receive all events.
	errCh := make(chan error, 1)
	go func() {
		for i := 0; i < SubscriberBufferSize+10; i++ {
			hub.Publish(BroadcastMessage{
				Envelope: EventEnvelope{
					ID:         fmt.Sprintf("burst-%d", i),
					Type:       "execution.started",
					Subject:    "change-orders/o-burst",
					Aggregate:  "o-burst",
					State:      "running",
					OccurredAt: time.Now().UTC(),
				},
				TargetUserIDs: []string{"slow-user", "fast-user"},
			})
			select {
			case d, ok := <-fastCh:
				if !ok {
					errCh <- fmt.Errorf("fast consumer channel closed at %d", i)
					return
				}
				if d.Resync {
					errCh <- fmt.Errorf("fast consumer unexpectedly got resync at %d", i)
					return
				}
			case <-time.After(time.Second):
				errCh <- fmt.Errorf("fast consumer timed out at %d", i)
				return
			}
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on slow consumer")
	}

	var sawResync bool
	for len(slowCh) > 0 {
		d := <-slowCh
		if d.Resync {
			sawResync = true
		}
	}
	if !sawResync {
		t.Fatal("expected slow consumer to receive Delivery{Resync: true}")
	}
}

func TestHubUnsubscribeAndConcurrency(t *testing.T) {
	hub := NewHub()
	ch, unsub := hub.Subscribe("u1", "user", false)
	if hub.SubscriberCount() != 1 {
		t.Fatalf("subscriber count = %d, want 1", hub.SubscriberCount())
	}
	unsub()
	unsub() // Idempotent.
	if hub.SubscriberCount() != 0 {
		t.Fatalf("subscriber count after unsub = %d, want 0", hub.SubscriberCount())
	}
	if _, ok := <-ch; ok {
		t.Fatal("expected closed subscriber channel after unsubscribe")
	}

	hub.SetPGListenerActive(true)
	if !hub.PGListenerActive() {
		t.Fatal("expected PGListenerActive true")
	}
	hub.SetPGListenerActive(false)
	if hub.PGListenerActive() {
		t.Fatal("expected PGListenerActive false")
	}

	// Concurrent Subscribe / Publish / Unsubscribe under -race.
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c, u := hub.Subscribe(fmt.Sprintf("u-%d", idx%3), "user", false)
			for i := 0; i < 20; i++ {
				hub.Publish(BroadcastMessage{
					Envelope: EventEnvelope{
						ID:         fmt.Sprintf("c-%d-%d", idx, i),
						Type:       "query_session.opened",
						Subject:    "query-sessions/s1",
						Aggregate:  "s1",
						OccurredAt: time.Now().UTC(),
					},
					TargetUserIDs: []string{"u-0", "u-1", "u-2"},
				})
				select {
				case <-c:
				default:
				}
			}
			u()
		}(g)
	}
	wg.Wait()
}

func TestCompactBroadcastMessageAndNotifyPostgresTx(t *testing.T) {
	// Oversized target list (>7500 bytes) must compact cleanly below MaxNotifyPayloadBytes.
	targets := make([]string, 300)
	for i := range targets {
		targets[i] = fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
	}
	msg := BroadcastMessage{
		Envelope: EventEnvelope{
			ID:         "00000000-0000-0000-0000-000000000001",
			Type:       "change_order.submitted",
			Subject:    "change-orders/00000000-0000-0000-0000-000000000002",
			Aggregate:  "00000000-0000-0000-0000-000000000002",
			State:      "submitted",
			OccurredAt: time.Now().UTC(),
		},
		TargetUserIDs: targets,
	}
	raw, err := compactBroadcastMessage(msg, MaxNotifyPayloadBytes)
	if err != nil {
		t.Fatalf("compactBroadcastMessage failed: %v", err)
	}
	if len(raw) > MaxNotifyPayloadBytes {
		t.Fatalf("compacted payload length = %d, want <= %d", len(raw), MaxNotifyPayloadBytes)
	}
	var decoded BroadcastMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("compacted JSON invalid: %v", err)
	}

	// Pathological huge envelope falls back to resync.
	huge := BroadcastMessage{
		Envelope: EventEnvelope{
			ID:         "huge-1",
			Type:       "change_order.submitted",
			Subject:    strings.Repeat("x", MaxNotifyPayloadBytes+500),
			Aggregate:  "agg-1",
			OccurredAt: time.Now().UTC(),
		},
	}
	fallbackRaw, err := compactBroadcastMessage(huge, MaxNotifyPayloadBytes)
	if err != nil {
		t.Fatalf("fallback compact failed: %v", err)
	}
	if len(fallbackRaw) > MaxNotifyPayloadBytes {
		t.Fatalf("fallback payload length = %d, want <= %d", len(fallbackRaw), MaxNotifyPayloadBytes)
	}
	var fallback BroadcastMessage
	if err := json.Unmarshal(fallbackRaw, &fallback); err != nil {
		t.Fatalf("fallback JSON invalid: %v", err)
	}
	if fallback.Envelope.Type != "resync" {
		t.Fatalf("fallback type = %q, want resync", fallback.Envelope.Type)
	}

	// nil tx is a no-op.
	if err := NotifyPostgresTx(context.Background(), nil, msg); err != nil {
		t.Fatalf("nil tx NotifyPostgresTx returned error: %v", err)
	}

	// Stub postgres driver captures SELECT pg_notify('yearning_domain_events', ?).
	rec := &notifyCaptureDriver{}
	sqlDB := sql.OpenDB(rec)
	defer sqlDB.Close()
	gdb, err := gorm.Open(postgres.New(postgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("gorm open failed: %v", err)
	}

	if err := NotifyPostgresTx(context.Background(), gdb, BroadcastMessage{
		Envelope: EventEnvelope{
			ID:         "evt-pg-1",
			Type:       "query_grant.activated",
			Subject:    "query-grants/g1",
			Aggregate:  "g1",
			State:      "active",
			OccurredAt: time.Now().UTC(),
		},
		TargetUserIDs: []string{"u-1"},
	}); err != nil {
		t.Fatalf("NotifyPostgresTx failed: %v", err)
	}
	if len(rec.queries) != 1 || !strings.Contains(rec.queries[0], "pg_notify") || len(rec.args) != 1 {
		t.Fatalf("unexpected pg_notify exec capture: queries=%v args=%v", rec.queries, rec.args)
	}
	if rec.args[0][0] != PGChannel {
		t.Fatalf("channel arg = %v, want %s", rec.args[0][0], PGChannel)
	}
}

func mustRecvDelivery(t *testing.T, ch <-chan Delivery, label string) Delivery {
	t.Helper()
	select {
	case d, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed unexpectedly", label)
		}
		return d
	case <-time.After(time.Second):
		t.Fatalf("%s: timed out waiting for delivery", label)
		return Delivery{}
	}
}

func assertNoDelivery(t *testing.T, ch <-chan Delivery, label string) {
	t.Helper()
	select {
	case d := <-ch:
		t.Fatalf("%s: unexpected delivery %+v", label, d)
	case <-time.After(25 * time.Millisecond):
	}
}

type notifyCaptureDriver struct {
	mu      sync.Mutex
	queries []string
	args    [][]any
}

func (d *notifyCaptureDriver) Connect(context.Context) (driver.Conn, error) {
	return &notifyCaptureConn{parent: d}, nil
}

func (d *notifyCaptureDriver) Driver() driver.Driver { return d }

func (d *notifyCaptureDriver) Open(string) (driver.Conn, error) {
	return &notifyCaptureConn{parent: d}, nil
}

type notifyCaptureConn struct {
	parent *notifyCaptureDriver
}

func (c *notifyCaptureConn) Prepare(string) (driver.Stmt, error) { return nil, io.EOF }
func (c *notifyCaptureConn) Close() error                        { return nil }
func (c *notifyCaptureConn) Begin() (driver.Tx, error)           { return nil, nil }
func (c *notifyCaptureConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.parent.mu.Lock()
	c.parent.queries = append(c.parent.queries, query)
	vals := make([]any, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	c.parent.args = append(c.parent.args, vals)
	c.parent.mu.Unlock()
	return driver.RowsAffected(1), nil
}
