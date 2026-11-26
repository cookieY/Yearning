//go:build integration

// fixture_test.go provides test environment helpers.
package settings

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
)

func writeKeyring(t *testing.T) string {
	t.Helper()
	return testkit.WriteKeyring(t)
}
