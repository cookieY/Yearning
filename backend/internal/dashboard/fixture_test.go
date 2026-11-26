//go:build integration

// fixture_test.go test environment helpers.
package dashboard

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
)

func writeKeyring(t *testing.T) string {
	t.Helper()
	return testkit.WriteKeyring(t)
}
