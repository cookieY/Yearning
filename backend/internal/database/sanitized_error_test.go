// sanitized_error_test.go SEC-0030: raw driver text must never leave through error strings.
package database

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizedErrorHidesDriverText(t *testing.T) {
	raw := errors.New(`ERROR: permission denied for table customers (SQLSTATE 42501) host=10.0.0.5 user=app_ro`)
	cls := ClassifyError("postgresql", raw)
	err := error(&SanitizedError{FailureName: "cursor_rejected_" + cls.FailureName, Raw: raw})
	text := err.Error()
	for _, forbidden := range []string{"customers", "10.0.0.5", "app_ro", "permission denied"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized error leaks %q: %s", forbidden, text)
		}
	}
	if !strings.HasPrefix(text, "database: cursor_rejected_") {
		t.Fatalf("unexpected sanitized text: %s", text)
	}
	// The errors chain retains the underlying error for internal classification (without participating in string formatting).
	if !errors.Is(err, raw) {
		t.Fatal("raw error must remain reachable via errors.Is")
	}
	var se *SanitizedError
	if !errors.As(err, &se) || se.FailureName == "" {
		t.Fatal("errors.As must recover SanitizedError with failure name")
	}
}
