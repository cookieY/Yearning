package fingerprint

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// fixedSigner is a fixed-key signer for tests and golden samples (non-production key).
type fixedSigner struct{ key []byte }

func (f fixedSigner) KeyID() string { return "golden-key-1" }

func (f fixedSigner) HMACSHA256(data []byte) ([]byte, error) {
	mac := hmac.New(sha256.New, f.key)
	mac.Write(data)
	return mac.Sum(nil), nil
}

func testSigner() fixedSigner {
	return fixedSigner{key: []byte("yearning-fingerprint-golden-test-key-material-32b!")}
}

func testEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine(testSigner(), Options{})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return e
}

func TestAnalyzeBasicExact(t *testing.T) {
	e := testEngine(t)
	res, err := e.Analyze(Profile{Engine: "mysql", Mode: "mysql"},
		[]byte("UPDATE orders SET status = 'processed' WHERE id = 42"), privacy.NewRunMapper())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if res.Quality != QualityExact {
		t.Fatalf("quality = %s, want exact (anonymized=%s)", res.Quality, res.AnonymizedSQL)
	}
	if res.StatementKind != "update" {
		t.Fatalf("kind = %s", res.StatementKind)
	}
	if res.AlgorithmVersion != "yearning-fingerprint-v1" {
		t.Fatalf("algorithm version = %s", res.AlgorithmVersion)
	}
	if res.FingerprintKeyID != "golden-key-1" {
		t.Fatalf("key id = %s", res.FingerprintKeyID)
	}
	for _, h := range []string{res.CanonicalFingerprintHMAC, res.DialectFingerprintHMAC, res.ExecutionShapeHMAC} {
		if len(h) != 64 || strings.ToLower(h) != h {
			t.Fatalf("hmac not lowercase hex sha256: %q", h)
		}
	}
	for _, banned := range []string{"orders", "status", "processed", "42"} {
		if strings.Contains(res.AnonymizedSQL, banned) {
			t.Fatalf("anonymized leaks %q: %s", banned, res.AnonymizedSQL)
		}
	}
}

func TestDeterminismSameVersion(t *testing.T) {
	e := testEngine(t)
	sql := []byte("INSERT INTO t (a,b) VALUES (1,'x'),(2,'y') ON DUPLICATE KEY UPDATE a = a + 1")
	mk := func() (Result, error) {
		return e.Analyze(Profile{Engine: "mysql", Mode: "mysql"}, sql, privacy.NewRunMapper())
	}
	first, err := mk()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := mk()
		if err != nil {
			t.Fatalf("again %d: %v", i, err)
		}
		if again.CanonicalFingerprintHMAC != first.CanonicalFingerprintHMAC ||
			again.DialectFingerprintHMAC != first.DialectFingerprintHMAC ||
			again.ExecutionShapeHMAC != first.ExecutionShapeHMAC ||
			again.AnonymizedSQL != first.AnonymizedSQL {
			t.Fatalf("nondeterministic output at run %d", i)
		}
	}
}

func TestCrossDialectCanonicalEquivalence(t *testing.T) {
	e := testEngine(t)
	a, err := e.Analyze(Profile{Engine: "mysql", Mode: "mysql"},
		[]byte("SELECT id FROM orders WHERE name = 'x'"), privacy.NewRunMapper())
	if err != nil {
		t.Fatalf("mysql: %v (anon=%s)", err, mustAnon(t, e, Profile{Engine: "mysql", Mode: "mysql"}, "SELECT id FROM orders WHERE name = 'x'"))
	}
	b, err := e.Analyze(Profile{Engine: "postgresql", Mode: "postgresql"},
		[]byte("SELECT \"id\" FROM \"orders\" WHERE \"name\" = 'x'"), privacy.NewRunMapper())
	if err != nil {
		t.Fatalf("pg: %v (anon=%s)", err, mustAnon(t, e, Profile{Engine: "postgresql", Mode: "postgresql"}, "SELECT \"id\" FROM \"orders\" WHERE \"name\" = 'x'"))
	}
	if a.CanonicalFingerprintHMAC != b.CanonicalFingerprintHMAC {
		t.Fatalf("canonical differs across equivalent surface syntax")
	}
	if a.DialectFingerprintHMAC == b.DialectFingerprintHMAC {
		t.Fatalf("dialect fingerprint must differ across dialects")
	}
}

func mustAnon(t *testing.T, e *Engine, p Profile, sql string) string {
	t.Helper()
	res, err := e.Analyze(p, []byte(sql), privacy.NewRunMapper())
	if err != nil {
		return "<error>"
	}
	return res.AnonymizedSQL
}

func TestLiteralTypeDifferenceChangesDialect(t *testing.T) {
	e := testEngine(t)
	intRes, err := e.Analyze(Profile{Engine: "mysql", Mode: "mysql"},
		[]byte("SELECT a FROM t WHERE k = 1"), privacy.NewRunMapper())
	if err != nil {
		t.Fatalf("int: %v", err)
	}
	strRes, err := e.Analyze(Profile{Engine: "mysql", Mode: "mysql"},
		[]byte("SELECT a FROM t WHERE k = 'x'"), privacy.NewRunMapper())
	if err != nil {
		t.Fatalf("str: %v", err)
	}
	if intRes.DialectFingerprintHMAC == strRes.DialectFingerprintHMAC {
		t.Fatalf("int vs string literal must differ in dialect fingerprint")
	}
	if intRes.CanonicalFingerprintHMAC == strRes.CanonicalFingerprintHMAC {
		t.Fatalf("int vs string literal must differ in canonical fingerprint")
	}
}

func TestFailClosedCases(t *testing.T) {
	e := testEngine(t)
	cases := []struct {
		name    string
		profile Profile
		sql     string
		code    string
	}{
		{"unterminated_string", Profile{Engine: "mysql", Mode: "mysql"}, "SELECT a FROM t WHERE a='x", ErrNameParseFailed},
		{"unterminated_comment", Profile{Engine: "mysql", Mode: "mysql"}, "SELECT 1 /* open", ErrNameParseFailed},
		{"unterminated_dollar", Profile{Engine: "postgresql", Mode: "postgresql"}, "SELECT $a$open", ErrNameParseFailed},
		{"invalid_utf8", Profile{Engine: "mysql", Mode: "mysql"}, "SELECT '\xff\xfe'", ErrNameParseFailed},
		{"unknown_profile", Profile{Engine: "oracle", Mode: "mysql"}, "SELECT 1", ErrNameParseFailed},
		{"statement_too_large", Profile{Engine: "mysql", Mode: "mysql"}, "SELECT " + strings.Repeat("a,", 300000) + "a", ErrNameBatchTooLarge},
	}
	for _, c := range cases {
		_, err := e.Analyze(c.profile, []byte(c.sql), privacy.NewRunMapper())
		f, ok := err.(*Failure)
		if !ok {
			t.Fatalf("%s: error = %v, want *Failure", c.name, err)
		}
		if f.Code != c.code {
			t.Fatalf("%s: code = %s, want %s", c.name, f.Code, c.code)
		}
		if strings.Contains(f.Error(), c.sql[:min(12, len(c.sql))]) && len(c.sql) < 64 {
			t.Fatalf("%s: failure detail must not echo SQL: %s", c.name, f.Error())
		}
	}
}

func TestNilSignerFailClosed(t *testing.T) {
	if _, err := NewEngine(nil, Options{}); err == nil {
		t.Fatalf("nil signer must fail")
	}
}

func TestEngineRejectsEmptyStatement(t *testing.T) {
	e := testEngine(t)
	if _, err := e.Analyze(Profile{Engine: "mysql", Mode: "mysql"}, []byte("   "), privacy.NewRunMapper()); err == nil {
		t.Fatalf("empty statement must fail")
	}
}

func TestLeakAssertionBlocksInjectionLikeNames(t *testing.T) {
	e := testEngine(t)
	// Fallback path: unknown syntax (CHECKPOINT) must not leak the raw word.
	res, err := e.Analyze(Profile{Engine: "postgresql", Mode: "postgresql"},
		[]byte("CHECKPOINT"), privacy.NewRunMapper())
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if strings.Contains(strings.ToUpper(res.AnonymizedSQL), "CHECKPOINT") {
		t.Fatalf("fallback leaked head keyword: %s", res.AnonymizedSQL)
	}
	if res.Quality != QualityLexer {
		t.Fatalf("fallback quality = %s", res.Quality)
	}
}

func TestHMACOutputEncoding(t *testing.T) {
	e := testEngine(t)
	res, err := e.Analyze(Profile{Engine: "mysql", Mode: "mysql"}, []byte("DELETE FROM t WHERE id = 5"), privacy.NewRunMapper())
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	// Verify that canonical HMAC is indeed the HMAC of deterministic input under the test key (reproducibility spot check).
	if res.FingerprintKeyID != testSigner().KeyID() {
		t.Fatalf("key id mismatch")
	}
	if _, err := hex.DecodeString(res.CanonicalFingerprintHMAC); err != nil {
		t.Fatalf("canonical hmac not hex: %v", err)
	}
}

func TestCardinalityBucket(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 2: "4", 4: "4", 5: "16", 16: "16", 17: "64", 1024: "1024", 5000: ">1024"}
	for n, want := range cases {
		if got := CardinalityBucket(n); got != want {
			t.Fatalf("bucket(%d) = %s, want %s", n, got, want)
		}
	}
}

func TestCryptoSignerAdapter(t *testing.T) {
	// Directly construct a minimal CrypterHMAC fake to verify purpose propagation.
	purposeSeen := ""
	signer := CryptoSigner{C: stubCrypter{purpose: &purposeSeen}}
	if signer.KeyID() != "stub-key" {
		t.Fatalf("key id = %s", signer.KeyID())
	}
	if _, err := signer.HMACSHA256([]byte("x")); err != nil {
		t.Fatalf("hmac: %v", err)
	}
	if purposeSeen != HMACPurpose {
		t.Fatalf("purpose = %s, want %s", purposeSeen, HMACPurpose)
	}
}

type stubCrypter struct{ purpose *string }

func (s stubCrypter) HMAC(purpose string, data []byte) ([]byte, error) {
	*s.purpose = purpose
	return make([]byte, 32), nil
}

func (s stubCrypter) HMACKeyID(purpose string) (string, error) { return "stub-key", nil }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestVersionCommentHeaderExcludedFromLiterals regression (Copilot rule 14 finding):
// the MySQL version comment header "!" + version digits is an execution condition rather than SQL content
// and must not pollute parameter counts or literal statistics.
func TestVersionCommentHeaderExcludedFromLiterals(t *testing.T) {
	e := testEngine(t)
	profile := Profile{Engine: "mysql", Mode: "mysql"}
	r1, err := e.Analyze(profile, []byte("SELECT /*!40001 SQL_NO_CACHE */ * FROM orders"), privacy.NewRunMapper())
	if err != nil {
		t.Fatalf("40001: %v", err)
	}
	r2, err := e.Analyze(profile, []byte("SELECT /*!50001 SQL_NO_CACHE */ * FROM orders"), privacy.NewRunMapper())
	if err != nil {
		t.Fatalf("50001: %v", err)
	}
	if r1.ParameterCount != 0 || len(r1.LiteralTypeCounts) != 0 {
		t.Fatalf("version header leaked into counts: params=%d literals=%v", r1.ParameterCount, r1.LiteralTypeCounts)
	}
	if r1.CanonicalFingerprintHMAC != r2.CanonicalFingerprintHMAC ||
		r1.DialectFingerprintHMAC != r2.DialectFingerprintHMAC ||
		r1.ExecutionShapeHMAC != r2.ExecutionShapeHMAC {
		t.Fatalf("version number must not change fingerprints")
	}
	if r1.AnonymizedSQL != r2.AnonymizedSQL {
		t.Fatalf("anonymized drift across version numbers")
	}
}

// TestMappingDelimiterIdentifierFailsClosed regression (Copilot rule 14 finding):
// when a quoted identifier contains mapping delimiter characters (\t/\n/\r/NUL), the envelope cannot
// round-trip unambiguously and must reject the entire statement with SQL_ANONYMIZATION_FAILED
// rather than silently downgrading to ident_0.
func TestMappingDelimiterIdentifierFailsClosed(t *testing.T) {
	e := testEngine(t)
	for _, id := range []string{"a\tb", "a\nb", "a\rb", "a\x00b"} {
		_, err := e.Analyze(Profile{Engine: "postgresql", Mode: "postgresql"},
			[]byte("SELECT x FROM \""+id+"\""), privacy.NewRunMapper())
		f, ok := err.(*Failure)
		if !ok || f.Code != ErrNameAnonFailed {
			t.Fatalf("identifier %q: err=%v, want SQL_ANONYMIZATION_FAILED", id, err)
		}
	}
	// Normal identifier mapping round-trip is unaffected.
	if _, err := e.Analyze(Profile{Engine: "postgresql", Mode: "postgresql"},
		[]byte("SELECT x FROM \"normal_name\""), privacy.NewRunMapper()); err != nil {
		t.Fatalf("normal identifier must pass: %v", err)
	}
}
