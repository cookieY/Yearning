package application

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func TestQueryApplicationUnitHelpers(t *testing.T) {
	if _, err := NewServices(nil, nil, nil); err == nil {
		t.Fatal("expected error on nil NewServices dependencies")
	}

	if isSerializationFailure(nil) {
		t.Fatal("expected false on nil error")
	}
	if !isSerializationFailure(&pgconn.PgError{Code: "40001"}) {
		t.Fatal("expected true on 40001")
	}
	if !isSerializationFailure(&pgconn.PgError{Code: "40P01"}) {
		t.Fatal("expected true on 40P01")
	}

	if !isUniqueViolation(gorm.ErrDuplicatedKey) {
		t.Fatal("expected true on gorm.ErrDuplicatedKey")
	}
	if !isUniqueViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("expected true on 23505")
	}
	if isUniqueViolation(&pgconn.PgError{Code: "23503"}) {
		t.Fatal("expected false on 23503")
	}

	_ = newID()
	_ = idemRequestHash("a", "b", "c")
	_ = sha256Hex([]byte("select 1"))
	_ = lockUpdate()
	_ = dsKey([]string{"ds-1", "ds-2"})
	_ = auditActor(Actor{UserID: "u-1", Username: "alice"})
	_ = auditInput(Actor{UserID: "u-1", Username: "alice"}, "ev", "res", "id-1", "act", "succeeded")

	for _, trig := range []string{"user_deleted", "expired", "other"} {
		_ = sessionTerminalFor(trig)
	}

	// normalizeSensitiveColumns & sameStrings
	cols, err := normalizeSensitiveColumns([]string{"  Phone ", "EMAIL", "phone"})
	if err != nil || len(cols) != 2 {
		t.Fatalf("normalizeSensitiveColumns: cols=%v err=%v", cols, err)
	}
	if !sameStrings(cols, []string{"phone", "email"}) {
		t.Fatalf("sameStrings unexpected false: %v", cols)
	}
	if sameStrings([]string{"a"}, []string{"a", "b"}) || sameStrings([]string{"a"}, []string{"b"}) {
		t.Fatal("sameStrings unexpected true")
	}
	if _, err := normalizeSensitiveColumns([]string{""}); err == nil {
		t.Fatal("expected error on empty column name")
	}
	if _, err := normalizeSensitiveColumns([]string{strings.Repeat("a", 129)}); err == nil {
		t.Fatal("expected error on oversized column name")
	}
	tooMany := make([]string, 257)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("col_%d", i)
	}
	if _, err := normalizeSensitiveColumns(tooMany); err == nil {
		t.Fatal("expected error on >256 sensitive columns")
	}
}
