//go:build integration

package application

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
)

func buildPerfOrdersSQLBatch(count int) []byte {
	padding := strings.Repeat("0123456789abcdef", 16) + "abcd" // 260 chars -> ~33.53 MB at 100k statements (just under 32 MiB cap)
	blockSize := count / 16
	if blockSize < 1 {
		blockSize = 1
	}
	var buf bytes.Buffer
	buf.Grow(count * 350)
	for i := 1; i <= count; i++ {
		variant := (i - 1) / blockSize
		if variant > 15 {
			variant = 15
		}
		id := i%10000 + 1
		amt := i%500 + 1
		switch variant {
		case 0:
			fmt.Fprintf(&buf, "UPDATE orders SET amount = %d WHERE id = %d /* perf_%s */;\n", amt, id, padding)
		case 1:
			fmt.Fprintf(&buf, "UPDATE orders SET amount = amount + %d WHERE id = %d /* perf_%s */;\n", amt, id, padding)
		case 2:
			fmt.Fprintf(&buf, "UPDATE orders SET amount = amount - %d WHERE id = %d AND amount > %d /* perf_%s */;\n", amt, id, i%10, padding)
		case 3:
			fmt.Fprintf(&buf, "UPDATE orders SET amount = %d WHERE id = %d AND amount >= %d /* perf_%s */;\n", amt, id, i%10, padding)
		case 4:
			fmt.Fprintf(&buf, "UPDATE orders SET amount = %d WHERE id = %d AND amount <= %d /* perf_%s */;\n", amt, id, amt+10, padding)
		case 5:
			fmt.Fprintf(&buf, "UPDATE orders SET amount = %d WHERE id = %d AND amount <> %d /* perf_%s */;\n", amt, id, 0, padding)
		case 6:
			fmt.Fprintf(&buf, "UPDATE orders SET amount = amount * %d WHERE id = %d AND amount > 0 /* perf_%s */;\n", 2, id, padding)
		case 7:
			fmt.Fprintf(&buf, "UPDATE orders SET amount = %d WHERE id > %d AND id <= %d ORDER BY id LIMIT 1 /* perf_%s */;\n", amt, id, id+1, padding)
		case 8:
			fmt.Fprintf(&buf, "UPDATE orders SET amount = %d WHERE id = %d OR (id = %d AND amount > %d) /* perf_%s */;\n", amt, id, id+1, 0, padding)
		case 9:
			fmt.Fprintf(&buf, "UPDATE orders SET amount = %d + id WHERE id = %d /* perf_%s */;\n", amt, id, padding)
		case 10:
			fmt.Fprintf(&buf, "INSERT INTO orders (id, amount) VALUES (%d, %d) /* perf_%s */;\n", id, amt, padding)
		case 11:
			fmt.Fprintf(&buf, "SELECT id, amount FROM orders WHERE id = %d /* perf_%s */;\n", id, padding)
		case 12:
			fmt.Fprintf(&buf, "SELECT id, amount FROM orders WHERE id = %d AND amount >= %d ORDER BY id DESC LIMIT 1 /* perf_%s */;\n", id, 0, padding)
		case 13:
			fmt.Fprintf(&buf, "SELECT COUNT(*) FROM orders WHERE id = %d AND amount = %d /* perf_%s */;\n", id, amt, padding)
		case 14:
			fmt.Fprintf(&buf, "DELETE FROM orders WHERE id = %d AND amount = %d /* perf_%s */;\n", id, amt, padding)
		default:
			fmt.Fprintf(&buf, "DELETE FROM orders WHERE id = %d AND amount < %d LIMIT 1 /* perf_%s */;\n", id, amt, padding)
		}
	}
	return buf.Bytes()
}

func waitEphemeralTerminal(t *testing.T, svc *Services, runID, ownerUserID string) *EphemeralRun {
	t.Helper()
	deadline := time.Now().Add(180 * time.Second)
	for {
		er := svc.EphemeralGetForTest(runID, ownerUserID)
		if er != nil && er.TerminalState() {
			return er
		}
		if time.Now().After(deadline) {
			state := "missing"
			if er != nil {
				state = string(er.State())
			}
			t.Fatalf("ephemeral run %s did not reach terminal state (state=%s)", runID, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEphemeralAsyncSyntaxFailureOnLargeBatch(t *testing.T) {
	b := newB7Fixture(t)
	var sqlBuf bytes.Buffer
	sqlBuf.Write(buildPerfOrdersSQLBatch(1200))
	sqlBuf.WriteString("SELECT FROM WHERE syntax_error_near_tail;\n")
	rawSQL := sqlBuf.Bytes()
	if len(rawSQL) <= ephemeralSyncSyntaxCheckMaxBytes {
		t.Fatalf("expected payload > %d bytes, got %d", ephemeralSyncSyntaxCheckMaxBytes, len(rawSQL))
	}

	runner := NewAgentRunner(b.svc, b.fakeDial(cleanEvidenceDial))
	enqueueStart := time.Now()
	runID, err := b.svc.StartEphemeralRun(context.Background(), b.owner, runner, EphemeralStartInput{
		FlowID:         b.flowID,
		SQL:            rawSQL,
		IdempotencyKey: "async-syntax-fail-" + runKeySuffix(),
	})
	enqueueElapsed := time.Since(enqueueStart)
	if err != nil {
		t.Fatalf("StartEphemeralRun should enqueue without synchronous parse error on >256KiB SQL: %v", err)
	}
	if enqueueElapsed > time.Second {
		t.Fatalf("StartEphemeralRun took %s, expected fast enqueue <= 1s", enqueueElapsed)
	}

	er := waitEphemeralTerminal(t, b.svc, runID, b.owner.UserID)
	if er.State() != domain.RunStateFailed {
		t.Fatalf("state = %s, want failed", er.State())
	}
	if er.ExpectedStatementCount != 0 || er.syntaxValidated {
		t.Fatalf("expected deferred syntax validation on enqueue, got count=%d validated=%v", er.ExpectedStatementCount, er.syntaxValidated)
	}
	if er.Run.BusinessErrCode == nil || *er.Run.BusinessErrCode != domain.CodeSQLParseFailed {
		t.Fatalf("business error code = %v, want %d", er.Run.BusinessErrCode, domain.CodeSQLParseFailed)
	}
	er.mu.RLock()
	sqlCleared := er.SQLPlaintext == nil
	er.mu.RUnlock()
	if !sqlCleared {
		t.Fatal("expected SQLPlaintext to be cleared after failed grouping")
	}
	b.svc.Ephemeral.mu.Lock()
	activeBytes := b.svc.Ephemeral.activeSQLBytes
	b.svc.Ephemeral.mu.Unlock()
	if activeBytes != 0 {
		t.Fatalf("expected activeSQLBytes == 0 after completion, got %d", activeBytes)
	}
}

func TestEphemeralStreamingMaterializationOver500Statements(t *testing.T) {
	b := newB7Fixture(t)
	rawSQL := buildPerfOrdersSQLBatch(900)
	if len(rawSQL) <= ephemeralSyncSyntaxCheckMaxBytes {
		t.Fatalf("expected payload > %d bytes, got %d", ephemeralSyncSyntaxCheckMaxBytes, len(rawSQL))
	}
	draft := b.createDraftWithSQL(t, "SELECT id FROM orders WHERE id = 1;")

	runner := NewAgentRunner(b.svc, b.fakeDial(cleanEvidenceDial))
	runID, err := b.svc.StartEphemeralRun(context.Background(), b.owner, runner, EphemeralStartInput{
		FlowID:         b.flowID,
		DraftID:        draft.ID,
		SQL:            rawSQL,
		IdempotencyKey: "stream-mat-" + runKeySuffix(),
	})
	if err != nil {
		t.Fatalf("StartEphemeralRun: %v", err)
	}
	er := waitEphemeralTerminal(t, b.svc, runID, b.owner.UserID)
	if er.State() != domain.RunStateReady {
		t.Fatalf("state = %s, want ready (code=%s)", er.State(), ephemeralRunCodeString(er))
	}
	er.mu.RLock()
	inlineCount := len(er.Statements)
	sqlDropped := er.SQLPlaintext == nil
	stmtCount := er.Run.StatementCount
	groupCount := len(er.Groups)
	er.mu.RUnlock()
	if inlineCount != 0 {
		t.Fatalf("expected inline Statements dropped for 900-statement batch, got %d", inlineCount)
	}
	if !sqlDropped {
		t.Fatal("expected SQLPlaintext to be dropped after grouping")
	}
	if stmtCount != 900 {
		t.Fatalf("StatementCount = %d, want 900", stmtCount)
	}
	if groupCount != 16 {
		t.Fatalf("groupCount = %d, want 16", groupCount)
	}

	if err := b.db.Transaction(func(tx *gorm.DB) error {
		return b.svc.MaterializeEphemeralRunTxWithSQL(context.Background(), tx, er, draft.ID, draft.Revision, rawSQL)
	}); err != nil {
		t.Fatalf("MaterializeEphemeralRunTxWithSQL: %v", err)
	}

	if got := count(t, b.db, "select count(*) from sql_statements where review_run_id = ?", runID); got != 900 {
		t.Fatalf("materialized sql_statements count = %d, want 900", got)
	}
	if got := count(t, b.db, "select count(*) from sql_fingerprint_groups g join sql_statements s on s.id = g.representative_statement_id where g.review_run_id = ?", runID); got != int64(groupCount) {
		t.Fatalf("joined representative statement count = %d, want %d", got, groupCount)
	}
	if got := count(t, b.db, "select coalesce(sum(statement_count), 0) from sql_fingerprint_groups where review_run_id = ?", runID); got != 900 {
		t.Fatalf("group statement_count sum = %d, want 900", got)
	}
}
