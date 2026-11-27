package application

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/platform/hashutil"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/query/domain"
	"github.com/cookieY/YearningX/backend/internal/query/persistence"
)

func TestQueryApplicationUnitHelpers(t *testing.T) {
	if _, err := NewServices(nil, nil, nil); err == nil {
		t.Fatal("expected error on nil NewServices dependencies")
	}

	if txutil.IsSerializationFailure(nil) {
		t.Fatal("expected false on nil error")
	}
	if !txutil.IsSerializationFailure(&pgconn.PgError{Code: "40001"}) {
		t.Fatal("expected true on 40001")
	}
	if !txutil.IsSerializationFailure(&pgconn.PgError{Code: "40P01"}) {
		t.Fatal("expected true on 40P01")
	}

	if !txutil.IsUniqueViolation(gorm.ErrDuplicatedKey) {
		t.Fatal("expected true on gorm.ErrDuplicatedKey")
	}
	if !txutil.IsUniqueViolation(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("expected true on 23505")
	}
	if txutil.IsUniqueViolation(&pgconn.PgError{Code: "23503"}) {
		t.Fatal("expected false on 23503")
	}

	if id1, id2 := newID(), newID(); id1 == "" || id2 == "" || id1 == id2 {
		t.Fatalf("newID returned non-unique or empty IDs: %q, %q", id1, id2)
	}
	if h := txutil.RequestHash("a", "b", "c"); h == "" || h != txutil.RequestHash("a", "b", "c") || h == txutil.RequestHash("c", "b", "a") {
		t.Fatalf("txutil.RequestHash determinism/order check failed: %q", h)
	}
	if h := hashutil.SHA256Hex([]byte("select 1")); len(h) != 64 {
		t.Fatalf("SHA256Hex expected 64-char hex, got %q", h)
	}
	if l := txutil.ForUpdate(); l.Strength != "UPDATE" {
		t.Fatalf("txutil.ForUpdate strength = %q, want UPDATE", l.Strength)
	}
	if k := dsKey([]string{"ds-2", "ds-1"}); k != "ds-2,ds-1," {
		t.Fatalf("dsKey = %q, want ds-2,ds-1,", k)
	}
	act := auditlog.UserActor("u-1", "alice")
	if act.Kind != "user" || act.UserID != "u-1" || act.Username != "alice" {
		t.Fatalf("auditlog.UserActor mismatch: %+v", act)
	}
	in := auditInput(Actor{UserID: "u-1", Username: "alice"}, "ev", "res", "id-1", "act", "succeeded")
	if in.EventType != "ev" || in.ResourceType != "res" || *in.ResourceID != "id-1" || in.Outcome != "succeeded" {
		t.Fatalf("auditInput mismatch: %+v", in)
	}

	if got := sessionTerminalFor("user_deleted"); got != "user_deleted" {
		t.Fatalf("sessionTerminalFor(user_deleted) = %q, want user_deleted", got)
	}
	if got := sessionTerminalFor("expired"); got != "expired" {
		t.Fatalf("sessionTerminalFor(expired) = %q, want expired", got)
	}
	if got := sessionTerminalFor("other"); got != "revoked" {
		t.Fatalf("sessionTerminalFor(other) = %q, want revoked", got)
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

func TestValidateRequestedUntil(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	if err := validateRequestedUntil(now, now, 43200); err == nil {
		t.Fatal("expected validation error for requested_until == now")
	} else {
		derr, ok := err.(*domain.Error)
		if !ok || derr.Code != domain.CodeValidationFailed || derr.BizMeta()["detail"] != "requested_until_in_past" {
			t.Fatalf("unexpected error for past requested_until: %#v", err)
		}
	}

	tooFar := now.Add(43201 * time.Minute)
	if err := validateRequestedUntil(now, tooFar, 43200); err == nil {
		t.Fatal("expected validation error when requested_until exceeds max duration")
	} else {
		derr, ok := err.(*domain.Error)
		if !ok || derr.Code != domain.CodeValidationFailed || derr.BizMeta()["detail"] != "requested_until_exceeds_max_minutes:43200" {
			t.Fatalf("unexpected error for exceeding max duration: %#v", err)
		}
	}

	within := now.Add(43200 * time.Minute)
	if err := validateRequestedUntil(now, within, 43200); err != nil {
		t.Fatalf("expected nil for valid requested_until, got %v", err)
	}
}

func TestSessionEligibleForGrantRebind(t *testing.T) {
	baseExp := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	laterExp := baseExp.Add(24 * time.Hour)
	earlierExp := baseExp.Add(-2 * time.Hour)
	covered := map[string]persistence.QueryAccessRequestDatasourceModel{
		"ds-1": {DatasourceID: "ds-1", CanQuery: true, CanExport: true},
		"ds-2": {DatasourceID: "ds-2", CanQuery: true, CanExport: false},
	}

	if !sessionEligibleForGrantRebind(baseExp, string(domain.GrantActive), laterExp, []string{"ds-1"}, covered) {
		t.Fatal("expected active session with covered datasources and later grant expiry to be eligible")
	}
	if sessionEligibleForGrantRebind(baseExp, string(domain.GrantActive), earlierExp, []string{"ds-1"}, covered) {
		t.Fatal("expected active grant with longer remaining expiry not to be shortened by earlier grant")
	}
	if !sessionEligibleForGrantRebind(baseExp, string(domain.GrantExpired), earlierExp, []string{"ds-1"}, covered) {
		t.Fatal("expected expired old grant session to be eligible when new grant covers its datasources")
	}
	if sessionEligibleForGrantRebind(baseExp, string(domain.GrantActive), laterExp, []string{"ds-1", "ds-3"}, covered) {
		t.Fatal("expected session with uncovered datasource not to be eligible")
	}
	noQuery := map[string]persistence.QueryAccessRequestDatasourceModel{
		"ds-1": {DatasourceID: "ds-1", CanQuery: false, CanExport: false},
	}
	if sessionEligibleForGrantRebind(baseExp, string(domain.GrantActive), laterExp, []string{"ds-1"}, noQuery) {
		t.Fatal("expected session whose datasource lacks CanQuery in new grant not to be eligible")
	}
	if sessionEligibleForGrantRebind(baseExp, string(domain.GrantActive), laterExp, nil, covered) {
		t.Fatal("expected empty session datasource list not to be eligible")
	}
}
