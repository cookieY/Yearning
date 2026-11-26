package application

import (
	"errors"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
)

func TestSanitizeRestorationLogReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "plain_reason",
			err:  domain.ErrSQLRestorationInvalid("unparameterized_number_literal_forbidden"),
			want: "unparameterized_number_literal_forbidden",
		},
		{
			name: "strips_identifier_suffix",
			err:  domain.ErrSQLRestorationInvalid("unmapped_identifier_forbidden:secret_col_name"),
			want: "unmapped_identifier_forbidden",
		},
		{
			name: "strips_san_qualified_suffix",
			err:  domain.ErrSQLRestorationInvalid("unauthorized_column:prod_db.payroll.salary"),
			want: "unauthorized_column",
		},
		{
			name: "multi_statement_reason",
			err:  domain.ErrSQLRestorationInvalid("multi_statement_forbidden"),
			want: "multi_statement_forbidden",
		},
		{
			name: "empty_reason_or_non_domain_error",
			err:  errors.New("plain error"),
			want: "unknown",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeRestorationLogReason(tc.err); got != tc.want {
				t.Fatalf("sanitizeRestorationLogReason(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
