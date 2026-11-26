//go:build integration

// fixture_test.go test environment helpers (env/keyring).
package notification

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
)

func writeTestKeyring(t *testing.T) string {
	t.Helper()
	return testkit.WriteKeyring(t)
}
