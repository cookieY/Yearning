// Package realtime provides an in-process non-blocking SSE event broadcast hub
// and a PostgreSQL LISTEN/NOTIFY listener for multi-replica real-time domain updates.
//
// Security boundaries:
//   - Only minimal metadata (EventEnvelope) is ever written onto the SSE wire.
//   - TargetUserIDs and NotifyAdmins are internal-only routing metadata.
//   - Empty TargetUserIDs with NotifyAdmins=false defaults to deny (zero recipients).
package realtime

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// PGChannel is the PostgreSQL LISTEN/NOTIFY channel used for domain event broadcasts.
	PGChannel = "yearning_domain_events"
	// SubscriberBufferSize is the bounded per-connection delivery queue capacity.
	SubscriberBufferSize = 64
	// DedupRingSize is the bounded ring capacity for deduplicating event IDs.
	DedupRingSize = 2048
	// MaxNotifyPayloadBytes keeps pg_notify payloads safely below PostgreSQL's 8000-byte limit.
	MaxNotifyPayloadBytes = 7500
)

// EventEnvelope is the minimal metadata payload sent over SSE to authenticated clients.
// It never carries plaintext SQL, credentials, approval comments, or query results.
type EventEnvelope struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Subject    string    `json:"subject"`
	Aggregate  string    `json:"aggregate_id"`
	State      string    `json:"state,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

// BroadcastMessage carries an EventEnvelope plus internal-only routing metadata.
// Routing fields are stripped before sending frames to any browser SSE stream.
type BroadcastMessage struct {
	Envelope      EventEnvelope `json:"envelope"`
	TargetUserIDs []string      `json:"target_user_ids,omitempty"`
	NotifyAdmins  bool          `json:"notify_admins,omitempty"`
}

// Delivery represents an item delivered to a subscriber channel.
// When Resync is true, the client should resynchronize cached HTTP queries.
type Delivery struct {
	Envelope EventEnvelope
	Resync   bool
}

type subscriber struct {
	userID         string
	role           string
	isBuiltinAdmin bool
	ch             chan Delivery
	needsResync    bool
	closed         bool
}

// Hub fans out BroadcastMessages to matching authenticated SSE subscribers without blocking.
type Hub struct {
	mu        sync.Mutex
	subs      map[*subscriber]struct{}
	seen      map[string]struct{}
	ring      [DedupRingSize]string
	ringIdx   int
	ringCount int

	pgListenerActive atomic.Bool
}

// NewHub creates an initialized Hub.
func NewHub() *Hub {
	return &Hub{
		subs: make(map[*subscriber]struct{}),
		seen: make(map[string]struct{}, DedupRingSize),
	}
}

// SetPGListenerActive records whether the PostgreSQL LISTEN loop currently holds an active connection.
func (h *Hub) SetPGListenerActive(active bool) {
	if h == nil {
		return
	}
	h.pgListenerActive.Store(active)
}

// PGListenerActive reports whether the PostgreSQL LISTEN loop is currently active.
func (h *Hub) PGListenerActive() bool {
	if h == nil {
		return false
	}
	return h.pgListenerActive.Load()
}

// Subscribe registers a bounded subscriber channel (capacity 64) and returns an idempotent unsubscribe func.
func (h *Hub) Subscribe(userID, role string, isBuiltinAdmin bool) (<-chan Delivery, func()) {
	sub := &subscriber{
		userID:         strings.TrimSpace(userID),
		role:           strings.TrimSpace(role),
		isBuiltinAdmin: isBuiltinAdmin,
		ch:             make(chan Delivery, SubscriberBufferSize),
	}
	h.mu.Lock()
	h.subs[sub] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			h.mu.Lock()
			if !sub.closed {
				sub.closed = true
				delete(h.subs, sub)
				close(sub.ch)
			}
			h.mu.Unlock()
		})
	}
	return sub.ch, unsubscribe
}

// SubscriberCount returns the number of currently registered subscribers.
func (h *Hub) SubscriberCount() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	n := len(h.subs)
	h.mu.Unlock()
	return n
}

// Publish deduplicates by Envelope.ID and delivers strictly to authorized subscribers without blocking.
// Subscribers with empty targets (and NotifyAdmins=false) receive nothing (default deny).
func (h *Hub) Publish(msg BroadcastMessage) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	if msg.Envelope.ID != "" {
		if _, dup := h.seen[msg.Envelope.ID]; dup {
			return
		}
		h.recordSeenLocked(msg.Envelope.ID)
	}

	targets := make(map[string]struct{}, len(msg.TargetUserIDs))
	for _, uid := range msg.TargetUserIDs {
		uid = strings.TrimSpace(uid)
		if uid != "" {
			targets[uid] = struct{}{}
		}
	}
	if len(targets) == 0 && !msg.NotifyAdmins {
		return
	}

	for sub := range h.subs {
		if sub.closed {
			continue
		}
		if !subscriberMatches(sub, targets, msg.NotifyAdmins) {
			continue
		}
		deliverLocked(sub, msg.Envelope)
	}
}

func (h *Hub) recordSeenLocked(id string) {
	if h.ringCount == len(h.ring) {
		evict := h.ring[h.ringIdx]
		delete(h.seen, evict)
	} else {
		h.ringCount++
	}
	h.ring[h.ringIdx] = id
	h.seen[id] = struct{}{}
	h.ringIdx = (h.ringIdx + 1) % len(h.ring)
}

func subscriberMatches(sub *subscriber, targets map[string]struct{}, notifyAdmins bool) bool {
	if sub.userID != "" {
		if _, ok := targets[sub.userID]; ok {
			return true
		}
	}
	if notifyAdmins && (sub.isBuiltinAdmin || sub.role == "admin") {
		return true
	}
	return false
}

func deliverLocked(sub *subscriber, env EventEnvelope) {
	if sub.needsResync {
		select {
		case sub.ch <- Delivery{Resync: true}:
			sub.needsResync = false
		default:
			return
		}
	}
	select {
	case sub.ch <- Delivery{Envelope: env}:
	default:
		sub.needsResync = true
		// Evict one oldest queued item so a resync marker enters the bounded buffer
		// immediately without blocking Publish or spawning per-subscriber goroutines.
		select {
		case <-sub.ch:
		default:
		}
		select {
		case sub.ch <- Delivery{Resync: true}:
			sub.needsResync = false
		default:
		}
	}
}

// compactBroadcastMessage marshals msg within maxBytes, stripping excess target user IDs
// or falling back to a compact resync envelope when necessary for pg_notify's payload cap.
func compactBroadcastMessage(msg BroadcastMessage, maxBytes int) ([]byte, error) {
	dedup := make([]string, 0, len(msg.TargetUserIDs))
	seen := make(map[string]struct{}, len(msg.TargetUserIDs))
	for _, id := range msg.TargetUserIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		dedup = append(dedup, id)
	}
	msg.TargetUserIDs = dedup

	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	if len(raw) <= maxBytes {
		return raw, nil
	}

	// First compaction: clear optional state string.
	msg.Envelope.State = ""
	if raw, err = json.Marshal(msg); err != nil {
		return nil, err
	}
	if len(raw) <= maxBytes {
		return raw, nil
	}

	// Second compaction: binary-search maximum TargetUserIDs prefix fitting within maxBytes.
	lo, hi := 0, len(msg.TargetUserIDs)
	var best []byte
	for lo <= hi {
		mid := (lo + hi) / 2
		candidate := msg
		candidate.TargetUserIDs = msg.TargetUserIDs[:mid]
		b, merr := json.Marshal(candidate)
		if merr != nil {
			return nil, merr
		}
		if len(b) <= maxBytes {
			best = b
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if len(best) > 0 {
		return best, nil
	}

	// Final fallback: minimal resync envelope.
	fallback := BroadcastMessage{
		Envelope: EventEnvelope{
			ID:         msg.Envelope.ID,
			Type:       "resync",
			Subject:    "resync",
			OccurredAt: msg.Envelope.OccurredAt,
		},
		NotifyAdmins: msg.NotifyAdmins,
	}
	return json.Marshal(fallback)
}
