package txutil

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func TestIsSerializationFailure(t *testing.T) {
	if IsSerializationFailure(nil) {
		t.Fatal("nil must not be a serialization failure")
	}
	if !IsSerializationFailure(&pgconn.PgError{Code: "40001"}) {
		t.Fatal("SQLSTATE 40001 must be classified as serialization failure")
	}
	if !IsSerializationFailure(&pgconn.PgError{Code: "40P01"}) {
		t.Fatal("SQLSTATE 40P01 must be classified as serialization failure")
	}
	if IsSerializationFailure(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("SQLSTATE 23505 must not be classified as serialization failure")
	}
}

func TestIsUniqueViolation(t *testing.T) {
	if IsUniqueViolation(nil) {
		t.Fatal("nil must not be a unique violation")
	}
	if !IsUniqueViolation(gorm.ErrDuplicatedKey) {
		t.Fatal("gorm.ErrDuplicatedKey must be classified as unique violation")
	}
	if !IsUniqueViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("SQLSTATE 23505 must be classified as unique violation")
	}
	if !IsUniqueViolation(errors.New("ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)")) {
		t.Fatal("driver error string containing 23505 must be classified as unique violation")
	}
	if IsUniqueViolation(&pgconn.PgError{Code: "23503"}) {
		t.Fatal("foreign key 23503 must not match unique violation")
	}
}

func TestIdempotencyHelpersAndValidation(t *testing.T) {
	if pk := PrimaryKey("u1", "POST:/x", "agg1", "k1"); pk != "u1#POST:/x#agg1#k1" {
		t.Fatalf("PrimaryKey = %q, want u1#POST:/x#agg1#k1", pk)
	}
	if h1, h2 := RequestHash("a", "b"), RequestHash("a", "b"); len(h1) != 64 || h1 != h2 {
		t.Fatalf("RequestHash unexpected: %q vs %q", h1, h2)
	}
	if RequestHash("a", "b") == RequestHash("ab") {
		t.Fatal("RequestHash separator should distinguish parts")
	}
	var store *IdempotencyStore
	if _, _, err := store.Claim(context.Background(), "u", "r", "a", "k", "h"); err == nil {
		t.Fatal("nil IdempotencyStore Claim must return error")
	}
	if err := store.Release(context.Background(), "u", "r", "a", "k"); err == nil {
		t.Fatal("nil IdempotencyStore Release must return error")
	}
	if err := store.Complete(context.Background(), "u", "r", "a", "k", nil); err == nil {
		t.Fatal("nil IdempotencyStore Complete must return error")
	}
}
