package application

import (
	"errors"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
)

func TestDDLNotSentFacts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		previous     domain.SendBoundary
		err          error
		wantTerminal domain.AttemptState
		wantBoundary domain.SendBoundary
		wantReason   int
		wantName     string
		wantOK       bool
	}{
		{
			name: "first statement path failure", previous: domain.BoundaryNotStarted,
			err:          &database.StatementNotSentError{FailureName: "schema_context_failed"},
			wantTerminal: domain.AttemptFailed, wantBoundary: domain.BoundaryNotStarted,
			wantReason: int(domain.CodePreflightFailed),
			wantName:   "schema_context_failed", wantOK: true,
		},
		{
			name: "later statement connection failure", previous: domain.BoundarySent,
			err:          &database.StatementNotSentError{FailureName: "connection_open_failed"},
			wantTerminal: domain.AttemptPartialFailed, wantBoundary: domain.BoundarySent,
			wantReason: int(domain.CodeStatementRejected),
			wantName:   "connection_open_failed", wantOK: true,
		},
		{
			name: "ordinary driver error remains ambiguous", previous: domain.BoundaryNotStarted,
			err: errors.New("driver result unknown"),
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotTerminal, gotBoundary, gotReason, gotName, gotOK := ddlNotSentFacts(
				tc.previous, tc.err,
			)
			if gotTerminal != tc.wantTerminal || gotBoundary != tc.wantBoundary ||
				gotReason != tc.wantReason || gotName != tc.wantName || gotOK != tc.wantOK {
				t.Fatalf(
					"ddlNotSentFacts() = (%q,%q,%d,%q,%v), want (%q,%q,%d,%q,%v)",
					gotTerminal, gotBoundary, gotReason, gotName, gotOK,
					tc.wantTerminal, tc.wantBoundary, tc.wantReason, tc.wantName, tc.wantOK,
				)
			}
		})
	}
}

func TestClassifyDMLFailureRollback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rbRes        *database.StatementResult
		rerr         error
		wantTerminal domain.AttemptState
		wantReason   int
		wantFailure  string
	}{
		{
			name:         "confirmed rollback maps to failed",
			rbRes:        &database.StatementResult{Outcome: database.OutcomeCancelled},
			wantTerminal: domain.AttemptFailed,
			wantReason:   int(domain.CodeStatementRejected),
			wantFailure:  "DML_STATEMENT_FAILED",
		},
		{
			name:         "rollback error fails closed as result_unknown",
			rbRes:        nil,
			rerr:         errors.New("rollback connection dropped"),
			wantTerminal: domain.AttemptResultUnknown,
			wantReason:   int(domain.CodeResultUnknown),
			wantFailure:  "ROLLBACK_AMBIGUOUS",
		},
		{
			name:         "nil rollback result fails closed as result_unknown",
			rbRes:        nil,
			wantTerminal: domain.AttemptResultUnknown,
			wantReason:   int(domain.CodeResultUnknown),
			wantFailure:  "ROLLBACK_AMBIGUOUS",
		},
		{
			name:         "ambiguous rollback outcome fails closed as result_unknown",
			rbRes:        &database.StatementResult{Outcome: database.OutcomeResultUnknown},
			wantTerminal: domain.AttemptResultUnknown,
			wantReason:   int(domain.CodeResultUnknown),
			wantFailure:  "ROLLBACK_AMBIGUOUS",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			terminal, code, name := classifyDMLFailureRollback(tc.rbRes, tc.rerr)
			if terminal != tc.wantTerminal || code != tc.wantReason || name != tc.wantFailure {
				t.Fatalf(
					"classifyDMLFailureRollback() = (%q,%d,%q), want (%q,%d,%q)",
					terminal, code, name,
					tc.wantTerminal, tc.wantReason, tc.wantFailure,
				)
			}
		})
	}
}

func TestClassifyDMLSiblingRollbackPathsAndDDLProgress(t *testing.T) {
	t.Parallel()

	okRB := &database.StatementResult{Outcome: database.OutcomeCancelled}
	unknownRB := &database.StatementResult{Outcome: database.OutcomeResultUnknown}
	rbErr := errors.New("rollback connection dropped")

	t.Run("search_path failure rollback outcomes", func(t *testing.T) {
		term, bnd, code, name := classifyDMLSearchPathFailureRollback(okRB, nil, 0)
		if term != domain.AttemptFailed || bnd != domain.BoundaryNotStarted ||
			code != int(domain.CodePreflightFailed) || name != "postgres_search_path_changed" {
			t.Fatalf("priorFacts=0 okRB got (%q,%q,%d,%q)", term, bnd, code, name)
		}
		term, bnd, code, name = classifyDMLSearchPathFailureRollback(okRB, nil, 1)
		if term != domain.AttemptFailed || bnd != domain.BoundarySent ||
			code != int(domain.CodeStatementRejected) || name != "postgres_search_path_changed" {
			t.Fatalf("priorFacts=1 okRB got (%q,%q,%d,%q)", term, bnd, code, name)
		}
		for _, bad := range []struct {
			res *database.StatementResult
			err error
		}{
			{unknownRB, nil},
			{nil, nil},
			{nil, rbErr},
		} {
			term, bnd, code, name = classifyDMLSearchPathFailureRollback(bad.res, bad.err, 1)
			if term != domain.AttemptResultUnknown || bnd != domain.BoundarySent ||
				code != int(domain.CodeResultUnknown) || name != "ROLLBACK_AMBIGUOUS" {
				t.Fatalf("ambiguous search_path rollback got (%q,%q,%d,%q)", term, bnd, code, name)
			}
		}
	})

	t.Run("rollback capability/conflict outcomes", func(t *testing.T) {
		term, bnd, code, name := classifyDMLRollbackCapabilityOrConflict(okRB, nil, int(domain.CodeStatementRejected), failureRollbackUnsupported)
		if term != domain.AttemptFailed || bnd != domain.BoundarySent ||
			code != int(domain.CodeStatementRejected) || name != failureRollbackUnsupported {
			t.Fatalf("confirmed capability rollback got (%q,%q,%d,%q)", term, bnd, code, name)
		}
		term, bnd, code, name = classifyDMLRollbackCapabilityOrConflict(unknownRB, nil, int(domain.CodeStatementRejected), failureRollbackConflict)
		if term != domain.AttemptResultUnknown || bnd != domain.BoundarySent ||
			code != int(domain.CodeResultUnknown) || name != "ROLLBACK_AMBIGUOUS" {
			t.Fatalf("OutcomeResultUnknown conflict rollback got (%q,%q,%d,%q)", term, bnd, code, name)
		}
	})

	t.Run("pre-commit abort rollback outcomes", func(t *testing.T) {
		svc := &Services{}
		okFacts := buildDMLPreCommitAbortFacts(svc, okRB, nil, 2, "seal failed", nil)
		if okFacts.Terminal != domain.AttemptFailed || okFacts.FailureName != failureRollbackUnsupported {
			t.Fatalf("confirmed pre-commit abort got %+v", okFacts)
		}
		badFacts := buildDMLPreCommitAbortFacts(svc, unknownRB, nil, 2, "seal failed", nil)
		if badFacts.Terminal != domain.AttemptResultUnknown || badFacts.ReasonCode != int(domain.CodeResultUnknown) || badFacts.FailureName != "ROLLBACK_AMBIGUOUS" {
			t.Fatalf("ambiguous pre-commit abort must fail closed, got %+v", badFacts)
		}
	})

	t.Run("cancel rollback helper outcomes", func(t *testing.T) {
		term, code, name := classifyDMLCancelRollback(okRB, nil)
		if term != domain.AttemptCancelled || code != int(domain.CodeIllegalTransition) || name != "EXECUTION_CANCELLED" {
			t.Fatalf("confirmed cancel got (%q,%d,%q)", term, code, name)
		}
		term, code, name = classifyDMLCancelRollback(unknownRB, nil)
		if term != domain.AttemptResultUnknown || code != int(domain.CodeResultUnknown) || name != "CANCEL_NOT_CONFIRMED" {
			t.Fatalf("ambiguous cancel got (%q,%d,%q)", term, code, name)
		}
	})

	t.Run("ddl progress accounting on fresh and resumed runs", func(t *testing.T) {
		if got := ddlBeatCompleted(0, 3); got != 3 {
			t.Fatalf("fresh run 3rd statement progress = %d, want 3", got)
		}
		if got := ddlBeatCompleted(1, 2); got != 3 {
			t.Fatalf("resumed run after 1 prior success + 2 new facts = %d, want 3", got)
		}
	})
}
