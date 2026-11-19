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
