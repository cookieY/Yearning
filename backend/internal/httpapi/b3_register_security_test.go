//go:build integration

// b3_register_security_test.go SEC-0061: registration rate limiting and uniform anti-enumeration message.
//
//	G-R1 Source-IP rate limiting before registration logic: 10 requests/min, 429 on exceed (same tier as login).
//	G-R2 Uniform rejection message on registration error surface: existing and fresh usernames receive
//	     identical failure messages without disclosing existence via 1005 or reserved-name semantics.
//
// Environment matches B3 integration tests (YEARNING_TEST_DATABASE_*); skipped when unconfigured.
package httpapi

import (
	"strings"
	"testing"
)

// registerSecurityEnableRegistration enables general.registration_enabled.
func registerSecurityEnableRegistration(t *testing.T, fx *b3Fixture) {
	t.Helper()
	fx.seedAdmin(t)
	if err := fx.db.Exec(`insert into system_settings (namespace, schema_version, settings, version, updated_by_user_id, created_at, updated_at)
		values ('general', 1, ?::jsonb, 1, ?, now(), now())
		on conflict (namespace) do update set settings = excluded.settings, updated_at = now()`,
		`{"registration_enabled":true}`, fx.mustAdminID(t)).Error; err != nil {
		t.Fatal(err)
	}
}

func registerSecurityPost(fx *b3Fixture, username, password string) (int, map[string]any, string) {
	return fx.admin.do("POST", "/api/v4/auth/register", map[string]any{
		"username":     username,
		"display_name": "Register Security Test",
		"password":     password,
	}, false)
}

const registerSecurityGenericMessage = registerRejectedMessage

// TestRegisterRateLimitedBeforeBusiness SEC-0061 G-R1: rate limiting precedes business checks,
// 10 requests/min (same tier as login). First 10 attempts (invalid payload, consuming rate-limit quota)
// return uniform 1001 message; 11th attempt returns 429 even with a fresh valid payload -- 429 carries
// no username existence signal.
func TestRegisterRateLimitedBeforeBusiness(t *testing.T) {
	fx := newB3Fixture(t)
	registerSecurityEnableRegistration(t, fx)

	for i := 0; i < 10; i++ {
		status, body, raw := registerSecurityPost(fx, "probe-user-"+strings.Repeat("x", i+1), "short")
		if status != 200 {
			t.Fatalf("attempt %d within budget: status = %d, want 200 envelope; body=%s", i+1, status, raw)
		}
		if code := errCode(body); code != 1001 {
			t.Fatalf("attempt %d within budget: err_code = %d, want 1001; body=%s", i+1, code, raw)
		}
	}
	// 11th attempt: fresh username + valid password must still return 429 (rate limiting precedes existence check and user creation).
	status, body, raw := registerSecurityPost(fx, "ratelimit-fresh-user", "longenoughpassword1")
	if status != 429 {
		t.Fatalf("11th attempt must be rate limited: status = %d, want 429; body=%s", status, raw)
	}
	if _, ok := body["err_code"]; ok {
		t.Fatalf("429 must be a transport Problem, not a business envelope: %s", raw)
	}
}

// TestRegisterAntiEnumerationUniformMessage SEC-0061 G-R2: existing username,
// fresh username (invalid payload), and reserved admin receive identical failure messages,
// without 1005 or "already exists" semantics in the response.
func TestRegisterAntiEnumerationUniformMessage(t *testing.T) {
	fx := newB3Fixture(t)
	registerSecurityEnableRegistration(t, fx)

	status, body, raw := registerSecurityPost(fx, "taken-user", "longenoughpassword1")
	if status != 200 || errCode(body) != 0 {
		t.Fatalf("fresh registration must succeed: status=%d body=%s", status, raw)
	}

	cases := []struct {
		name     string
		username string
		password string
	}{
		{"existing username", "taken-user", "longenoughpassword1"},
		{"fresh username, invalid payload", "fresh-user-abc", "short"},
		{"reserved admin", "admin", "longenoughpassword1"},
	}
	for _, tc := range cases {
		status, body, raw := registerSecurityPost(fx, tc.username, tc.password)
		if status != 200 {
			t.Fatalf("%s: status = %d, want 200 business envelope; body=%s", tc.name, status, raw)
		}
		if code := errCode(body); code != 1001 {
			t.Fatalf("%s: err_code = %d, want 1001; body=%s", tc.name, code, raw)
		}
		if msg, _ := body["message"].(string); msg != registerSecurityGenericMessage {
			t.Fatalf("%s: message = %q, want unified %q", tc.name, msg, registerSecurityGenericMessage)
		}
		if strings.Contains(raw, "1005") || strings.Contains(raw, "RESOURCE_ALREADY_EXISTS") ||
			strings.Contains(strings.ToLower(raw), "already exists") || strings.Contains(strings.ToLower(raw), "reserved") {
			t.Fatalf("%s: response must not leak existence semantics: %s", tc.name, raw)
		}
	}
}
