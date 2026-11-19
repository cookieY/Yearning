package realtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	initialListenBackoff = 500 * time.Millisecond
	maxListenBackoff     = 30 * time.Second
)

// StartPGListener maintains a dedicated PostgreSQL LISTEN connection for yearning_domain_events
// and publishes received BroadcastMessages into hub. It blocks on WaitForNotification without
// polling loops and reconnects with exponential backoff only when the connection drops.
func StartPGListener(ctx context.Context, dsn string, hub *Hub, logger *zap.Logger) {
	if hub == nil {
		return
	}
	defer hub.SetPGListenerActive(false)
	if dsn == "" {
		return
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	backoff := initialListenBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			hub.SetPGListenerActive(false)
			logger.Warn("pg listener connect failed; retrying",
				zap.Error(err),
				zap.Duration("backoff", backoff),
			)
			if !waitBackoff(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		if _, err := conn.Exec(ctx, "LISTEN "+PGChannel); err != nil {
			hub.SetPGListenerActive(false)
			_ = conn.Close(context.Background())
			logger.Warn("pg listener LISTEN failed; retrying",
				zap.Error(err),
				zap.Duration("backoff", backoff),
			)
			if !waitBackoff(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		backoff = initialListenBackoff
		hub.SetPGListenerActive(true)
		logger.Info("pg listener active", zap.String("channel", PGChannel))

		for {
			notif, werr := conn.WaitForNotification(ctx)
			if werr != nil {
				hub.SetPGListenerActive(false)
				_ = conn.Close(context.Background())
				if ctx.Err() != nil {
					return
				}
				logger.Warn("pg listener connection dropped; reconnecting", zap.Error(werr))
				break
			}
			var msg BroadcastMessage
			if uerr := json.Unmarshal([]byte(notif.Payload), &msg); uerr != nil {
				logger.Warn("pg listener ignored malformed notification", zap.Error(uerr))
				continue
			}
			hub.Publish(msg)
		}

		if !waitBackoff(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

// NotifyPostgresTx sends a transactional pg_notify on yearning_domain_events when tx uses postgres.
// Notifications emitted inside a transaction are delivered by PostgreSQL only after COMMIT succeeds.
func NotifyPostgresTx(ctx context.Context, tx *gorm.DB, msg BroadcastMessage) error {
	if tx == nil || tx.Dialector == nil || tx.Dialector.Name() != "postgres" {
		return nil
	}
	payload, err := compactBroadcastMessage(msg, MaxNotifyPayloadBytes)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec("SELECT pg_notify(?, ?)", PGChannel, string(payload)).Error
}

func waitBackoff(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > maxListenBackoff {
		return maxListenBackoff
	}
	return next
}
