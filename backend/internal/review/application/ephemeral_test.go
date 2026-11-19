package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
)

// RCP-20261101-EPHEMERAL-PRECHECK: ownership, TTL, and idempotency semantics of the ephemeral Run store.
func TestEphemeralStoreOwnershipAndTTL(t *testing.T) {
	st := NewEphemeralStore(time.Minute)
	run := &EphemeralRun{ID: "r1", OwnerUserID: "u1", state: domain.RunStateReady,
		expiresAt: time.Now().Add(time.Minute)}
	st.put(run)
	if st.Get("r1", "u2") != nil {
		t.Fatal("non-owner read must be rejected without revealing existence")
	}
	if st.Get("r1", "u1") == nil {
		t.Fatal("owner read must succeed")
	}
	// TTL is a hard boundary (review agent M2): terminal expired Runs reject reads immediately without waiting for the sweep tick.
	run.mu.Lock()
	run.expiresAt = time.Now().Add(-time.Second)
	run.mu.Unlock()
	if st.Get("r1", "u1") != nil {
		t.Fatal("expired terminal Run must be immediately unreadable")
	}
	st.sweepNow(time.Now())
	st.mu.Lock()
	_, present := st.runs["r1"]
	st.mu.Unlock()
	if present {
		t.Fatal("sweep must remove expired terminal Run")
	}
}

func TestEphemeralStoreTakeIsDestructive(t *testing.T) {
	st := NewEphemeralStore(time.Minute)
	st.put(&EphemeralRun{ID: "r2", OwnerUserID: "u1", state: domain.RunStateReady,
		expiresAt: time.Now().Add(time.Minute)})
	if st.Take("r2", "u1") == nil {
		t.Fatal("owner Take must succeed")
	}
	if st.Take("r2", "u1") != nil || st.Get("r2", "u1") != nil {
		t.Fatal("Take has consumption semantics: in-memory copy must be unreadable after submission")
	}
}

func TestEphemeralStoreIdempotencyWindow(t *testing.T) {
	st := NewEphemeralStore(time.Minute)
	st.put(&EphemeralRun{ID: "r3", OwnerUserID: "u1", state: domain.RunStateRunning,
		expiresAt: time.Now().Add(time.Minute)})
	st.rememberIdem("u1", "k1", "r3")
	if id, ok := st.IdempotentRun("u1", "k1"); !ok || id != "r3" {
		t.Fatal("same key within idempotency window must reuse existing Run")
	}
	if _, ok := st.IdempotentRun("u2", "k1"); ok {
		t.Fatal("idempotency key must be isolated per user")
	}
	st.mu.Lock()
	e := st.idem[st.idemKey("u1", "k1")]
	e.CreatedAt = time.Now().Add(-ephemeralMemoryIdemTTL - time.Second)
	st.idem[st.idemKey("u1", "k1")] = e
	st.mu.Unlock()
	if _, ok := st.IdempotentRun("u1", "k1"); ok {
		t.Fatal("expired idempotency entry must not be reused")
	}
}

func TestEphemeralStoreActiveCap(t *testing.T) {
	st := NewEphemeralStore(time.Minute)
	for i := 0; i < ephemeralMaxPerUser; i++ {
		st.put(&EphemeralRun{ID: string(rune('a' + i)), OwnerUserID: "u1", state: domain.RunStateRunning,
			expiresAt: time.Now().Add(time.Minute)})
	}
	if st.activeOfUser("u1") != ephemeralMaxPerUser {
		t.Fatalf("active count=%d want %d", st.activeOfUser("u1"), ephemeralMaxPerUser)
	}
	st.put(&EphemeralRun{ID: "done", OwnerUserID: "u1", state: domain.RunStateFailed,
		expiresAt: time.Now().Add(time.Minute)})
	if st.activeOfUser("u1") != ephemeralMaxPerUser {
		t.Fatal("terminal Run must not count toward active cap")
	}
}

// TestEphemeralPutBackCompensatesFailedSubmit submission failure compensation (review agent
// H1 regression): PutBack after Take must make the Run readable and Take-able again — relied upon by
// serialization-conflict retries.
func TestEphemeralPutBackCompensatesFailedSubmit(t *testing.T) {
	st := NewEphemeralStore(time.Minute)
	svc := &Services{Ephemeral: st}
	er := &EphemeralRun{ID: "r9", OwnerUserID: "u1", state: domain.RunStateReady,
		expiresAt: time.Now().Add(time.Minute)}
	st.put(er)
	took := st.Take("r9", "u1")
	if took == nil {
		t.Fatal("Take must succeed")
	}
	if st.Get("r9", "u1") != nil {
		t.Fatal("Run must be unreadable after Take")
	}
	// Submission path ultimately fails -> compensating put-back (Services-layer entry, same path as submitV2Tx).
	svc.EphemeralPutBack(took)
	if st.Get("r9", "u1") == nil {
		t.Fatal("Run must be readable again after compensation (transient conflict must not destroy precheck result)")
	}
	if again := st.Take("r9", "u1"); again == nil || again.ID != "r9" {
		t.Fatal("must be able to Take again after compensation (retry semantics)")
	}
	// Idempotent: nil put-back is a no-op.
	svc.EphemeralPutBack(nil)
}

func TestEphemeralRepresentativeSQLResolution(t *testing.T) {
	prof, err := fingerprint.ParseProfile("mysql/mysql")
	if err != nil {
		t.Fatal(err)
	}
	er := &EphemeralRun{
		ID:           "run-ephem-1",
		Profile:      prof,
		SQLPlaintext: []byte("UPDATE orders SET status = 'PAID' WHERE id = 101;"),
		Statements: []persistence.SQLStatementModel{
			{ID: "stmt-1", ReviewRunID: "run-ephem-1", Ordinal: 1},
		},
		Groups: []persistence.SQLFingerprintGroupModel{
			{ID: "grp-1", ReviewRunID: "run-ephem-1", RepresentativeStatementID: "stmt-1"},
		},
	}
	ctx := withRepresentativeSQLResolver(context.Background(), func(runID, groupID string) (string, bool, error) {
		return ephemeralRepresentativeSQL(er, runID, groupID)
	})
	fs := &factSource{runID: "run-ephem-1"}
	sqlText, err := fs.representativeStatementSQL(ctx, "grp-1")
	if err != nil {
		t.Fatalf("expected ephemeral representative SQL to resolve, got err: %v", err)
	}
	if sqlText != "UPDATE orders SET status = 'PAID' WHERE id = 101" &&
		sqlText != "UPDATE orders SET status = 'PAID' WHERE id = 101;" {
		t.Fatalf("unexpected resolved SQL: %q", sqlText)
	}
	if _, err := fs.representativeStatementSQL(ctx, "unknown-grp"); err != ErrFactSourceUnavailable {
		t.Fatalf("expected ErrFactSourceUnavailable for missing group, got %v", err)
	}
}

func TestEphemeralStoreConcurrentGetTakeAndFinishNoRace(t *testing.T) {
	st := NewEphemeralStore(time.Minute)
	run := &EphemeralRun{
		ID:          "r-race",
		OwnerUserID: "u1",
		state:       domain.RunStateRunning,
		expiresAt:   time.Now().Add(time.Minute),
	}
	st.put(run)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			now := time.Now()
			run.mu.Lock()
			run.state = domain.RunStateReady
			run.expiresAt = now.Add(time.Minute)
			run.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = st.Get("r-race", "u1")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			st.sweepNow(time.Now())
		}
	}()
	wg.Wait()
}

func TestCountStatementsSQLParseFailureLocationAndReason(t *testing.T) {
	svc := &Services{}
	pgProfile := fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}

	// Backtick in PostgreSQL mode on line 2, statement #2 -> exact line/col/offset/statement/dialect and zero raw identifiers.
	sql := []byte("SELECT 1;\nSELECT `secret_col` FROM secret_tbl;")
	_, err := svc.countStatements(sql, pgProfile)
	if err == nil {
		t.Fatal("expected SQL_PARSE_FAILED error")
	}
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeSQLParseFailed {
		t.Fatalf("expected 2003 SQL_PARSE_FAILED, got %v", err)
	}
	for _, want := range []string{"line 2, col 8", "offset 17", "statement #2", "dialect postgresql/postgresql", "unclassifiable byte 0x60"} {
		if !strings.Contains(de.Detail, want) {
			t.Fatalf("detail %q missing %q", de.Detail, want)
		}
	}
	if strings.Contains(de.Detail, "secret_col") || strings.Contains(de.Detail, "secret_tbl") {
		t.Fatalf("detail leaked raw identifier: %q", de.Detail)
	}

	// Unterminated string literal on line 2, statement #2 at EOF.
	_, err2 := svc.countStatements([]byte("SELECT 1;\nINSERT INTO t VALUES ('open"), pgProfile)
	var de2 *domain.Error
	if !errors.As(err2, &de2) || de2.Code != domain.CodeSQLParseFailed {
		t.Fatalf("expected 2003 SQL_PARSE_FAILED on unterminated literal, got %v", err2)
	}
	for _, want := range []string{"line 2, col 1", "statement #2", "unterminated"} {
		if !strings.Contains(de2.Detail, want) {
			t.Fatalf("unterminated detail %q missing %q", de2.Detail, want)
		}
	}
}
