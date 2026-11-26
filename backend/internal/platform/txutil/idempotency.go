package txutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// IdempotencyErrors maps idempotency protocol states to domain-specific error instances.
type IdempotencyErrors struct {
	Reused     func() error
	InProgress func() error
	Concurrent func() error
}

// IdempotencyStore persists in-progress claims and AEAD-sealed replay responses in idempotency_records.
type IdempotencyStore struct {
	DB      *gorm.DB
	Crypter *crypto.Crypter
	Errs    IdempotencyErrors
}

// NewIdempotencyStore constructs an IdempotencyStore bound to db, crypter, and domain error constructors.
func NewIdempotencyStore(db *gorm.DB, crypter *crypto.Crypter, errs IdempotencyErrors) *IdempotencyStore {
	return &IdempotencyStore{DB: db, Crypter: crypter, Errs: errs}
}

// PrimaryKey returns the stable AAD primary key binding a claim to its stored ciphertext envelope.
func PrimaryKey(userID, route, aggregateID, key string) string {
	return userID + "#" + route + "#" + aggregateID + "#" + key
}

// RequestHash computes the canonical SHA-256 request fingerprint over normalized request parts.
func RequestHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, ";")))
	return hex.EncodeToString(sum[:])
}

// Claim attempts to insert an in_progress idempotency claim.
// When claimed == false and err == nil, replay contains the decrypted prior JSON response.
func (s *IdempotencyStore) Claim(ctx context.Context, userID, route, aggregateID, key, reqHash string) (claimed bool, replay []byte, err error) {
	return s.claimAttempt(ctx, userID, route, aggregateID, key, reqHash, false)
}

// RunIdempotent executes fn under an idempotency claim, replaying the cached JSON response on duplicate keys
// and releasing the claim automatically whenever fn returns an error.
func RunIdempotent[T any](
	ctx context.Context,
	store *IdempotencyStore,
	userID, route, aggregateID, key, reqHash string,
	fn func() (T, error),
) (T, bool, error) {
	var zero T
	claimed, replay, err := store.Claim(ctx, userID, route, aggregateID, key, reqHash)
	if err != nil {
		return zero, false, err
	}
	if !claimed {
		var v T
		if err := json.Unmarshal(replay, &v); err != nil {
			return zero, false, err
		}
		return v, true, nil
	}
	res, err := fn()
	if err != nil {
		_ = store.Release(ctx, userID, route, aggregateID, key)
		return zero, false, err
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return zero, false, err
	}
	if err := store.Complete(ctx, userID, route, aggregateID, key, raw); err != nil {
		return zero, false, err
	}
	return res, false, nil
}

func (s *IdempotencyStore) claimAttempt(ctx context.Context, userID, route, aggregateID, key, reqHash string, retriedAfterExpiry bool) (bool, []byte, error) {
	if s == nil || s.DB == nil || s.Crypter == nil {
		return false, nil, errors.New("txutil: unconfigured idempotency store")
	}
	ins := s.DB.WithContext(ctx).Exec(`
		insert into idempotency_records (id, requesting_user_id, route_template, aggregate_id,
		    idempotency_key, request_hash, state, created_at, expires_at)
		values (?, ?, ?, ?, ?, ?, 'in_progress', now(), now() + interval '24 hours')`,
		uuid.NewString(), userID, route, aggregateID, key, reqHash)
	if ins.Error == nil {
		return true, nil, nil
	}
	if !IsUniqueViolation(ins.Error) {
		return false, nil, ins.Error
	}

	var row struct {
		State                  string
		RequestHash            string
		ResponseBodyCiphertext []byte
		ResponseBodyNonce      []byte
		ResponseBodyKeyID      string
		ResponseBodyAlgorithm  string
	}
	if err := s.DB.WithContext(ctx).Raw(`
		select state, request_hash, response_body_ciphertext, response_body_nonce,
		       response_key_id as response_body_key_id,
		       response_body_algorithm as response_body_algorithm
		from idempotency_records
		where requesting_user_id = ? and route_template = ? and aggregate_id = ? and idempotency_key = ?`,
		userID, route, aggregateID, key).Scan(&row).Error; err != nil {
		return false, nil, err
	}
	if row.RequestHash != reqHash {
		return false, nil, s.reusedErr()
	}
	if row.State != "completed" {
		var expired bool
		_ = s.DB.WithContext(ctx).Raw(`
			select expires_at < now() from idempotency_records
			where requesting_user_id = ? and route_template = ? and aggregate_id = ? and idempotency_key = ?`,
			userID, route, aggregateID, key).Scan(&expired).Error
		if expired && !retriedAfterExpiry {
			if err := s.releaseExpired(ctx, userID, route, aggregateID, key); err != nil {
				return false, nil, err
			}
			return s.claimAttempt(ctx, userID, route, aggregateID, key, reqHash, true)
		}
		return false, nil, s.inProgressErr()
	}

	env := crypto.Envelope{
		Ciphertext: row.ResponseBodyCiphertext,
		Nonce:      row.ResponseBodyNonce,
		KeyID:      row.ResponseBodyKeyID,
		Algorithm:  row.ResponseBodyAlgorithm,
	}
	plaintext, err := s.Crypter.OpenColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "idempotency_records",
		PrimaryKey: PrimaryKey(userID, route, aggregateID, key),
		Column:     "response_body_ciphertext",
	}, env)
	if err != nil {
		return false, nil, err
	}
	return false, plaintext, nil
}

// Release deletes an in_progress idempotency claim after a failed command so callers can safely retry.
func (s *IdempotencyStore) Release(ctx context.Context, userID, route, aggregateID, key string) error {
	if s == nil || s.DB == nil {
		return errors.New("txutil: unconfigured idempotency store")
	}
	return s.DB.WithContext(ctx).Exec(`
		delete from idempotency_records
		where requesting_user_id = ? and route_template = ? and aggregate_id = ?
		  and idempotency_key = ? and state = 'in_progress'`,
		userID, route, aggregateID, key).Error
}

func (s *IdempotencyStore) releaseExpired(ctx context.Context, userID, route, aggregateID, key string) error {
	return s.DB.WithContext(ctx).Exec(`
		delete from idempotency_records
		where requesting_user_id = ? and route_template = ? and aggregate_id = ?
		  and idempotency_key = ? and expires_at < now()`,
		userID, route, aggregateID, key).Error
}

// Complete seals response in an AEAD envelope and transitions an in_progress claim to completed.
func (s *IdempotencyStore) Complete(ctx context.Context, userID, route, aggregateID, key string, response []byte) error {
	if s == nil || s.DB == nil || s.Crypter == nil {
		return errors.New("txutil: unconfigured idempotency store")
	}
	env, err := s.Crypter.SealColumn(crypto.PurposePayload, crypto.ColumnRef{
		Table:      "idempotency_records",
		PrimaryKey: PrimaryKey(userID, route, aggregateID, key),
		Column:     "response_body_ciphertext",
	}, response)
	if err != nil {
		return err
	}
	res := s.DB.WithContext(ctx).Exec(`
		update idempotency_records set state='completed', response_http_status=200,
		    response_body_ciphertext=?, response_body_nonce=?, response_key_id=?,
		    response_body_algorithm='aes-256-gcm'
		where requesting_user_id = ? and route_template = ? and aggregate_id = ?
		  and idempotency_key = ? and state = 'in_progress'`,
		env.Ciphertext, env.Nonce, env.KeyID, userID, route, aggregateID, key)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return s.concurrentErr()
	}
	return nil
}

func (s *IdempotencyStore) reusedErr() error {
	if s != nil && s.Errs.Reused != nil {
		return s.Errs.Reused()
	}
	return errors.New("txutil: idempotency key reused with different payload")
}

func (s *IdempotencyStore) inProgressErr() error {
	if s != nil && s.Errs.InProgress != nil {
		return s.Errs.InProgress()
	}
	return errors.New("txutil: idempotency request in progress")
}

func (s *IdempotencyStore) concurrentErr() error {
	if s != nil && s.Errs.Concurrent != nil {
		return s.Errs.Concurrent()
	}
	return errors.New("txutil: concurrent idempotency completion")
}
