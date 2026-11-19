package fingerprint

import (
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// FuzzAnalyzeNeverPanicsAndNeverLeaks: random inputs must never panic;
// anonymized output on the success path must pass the leak assertion (built into the engine) and be deterministic.
func FuzzAnalyzeNeverPanicsAndNeverLeaks(f *testing.F) {
	seeds := []string{
		"SELECT 1",
		"UPDATE t SET a='x' WHERE id=1",
		"INSERT INTO t VALUES (1,'a'),(2,'b')",
		"DELETE FROM t WHERE k IN (1,2,3)",
		"SELECT /*+ MAX_EXECUTION_TIME(1000) */ * FROM t",
		"SELECT /*!40001 SQL_NO_CACHE */ * FROM t",
		"SELECT $a$ dlq $a$ FROM t",
		"SELECT E'x\\'y' FROM t",
		"WITH c AS (SELECT 1) SELECT * FROM c",
		"SELECT \"q\"\"x\" FROM \"T\"",
		"SELECT `b``c` FROM `T`",
		"CREATE TABLE t (id INT PRIMARY KEY, v VARCHAR(10) COMMENT 'z')",
		"ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES u(b)",
		"SELECT a::numeric(10,2)[3] FROM t",
		"SELECT data->>'k', j @> '{}' FROM t",
		"SELECT 1 -- tail comment",
		"SELECT 1 # hash tail",
		"TRUNCATE TABLE t",
		"MERGE INTO t USING s ON t.id=s.id WHEN MATCHED THEN UPDATE SET v=1",
		"SELECT a FROM t LIMIT 5, 10",
		"'unterminated",
		"/* unterminated",
		"$tag$unterminated",
		"SELECT \xff\xfe",
		";;;",
		"",
		"   ",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	profiles := []Profile{
		{Engine: "mysql", Mode: "mysql"},
		{Engine: "postgresql", Mode: "postgresql"},
		{Engine: "tidb", Mode: "mysql"},
	}
	e, err := NewEngine(testSigner(), Options{})
	if err != nil {
		f.Fatalf("engine: %v", err)
	}
	f.Fuzz(func(t *testing.T, input string) {
		for _, p := range profiles {
			res, err := e.Analyze(p, []byte(input), privacy.NewRunMapper())
			if err != nil {
				continue // fail-closed path
			}
			// Success path: defense-in-depth check that anonymized output contains no raw remnants.
			lower := strings.ToLower(res.AnonymizedSQL)
			for _, banned := range []string{"--", "/*", "'", "`"} {
				if strings.Contains(res.AnonymizedSQL, banned) {
					t.Fatalf("profile %s anonymized contains %q: %s", p, banned, res.AnonymizedSQL)
				}
			}
			if res.Quality != QualityExact && res.Quality != QualityLexer {
				t.Fatalf("invalid quality %s", res.Quality)
			}
			// Determinism: re-running on the same input must produce identical results.
			again, err2 := e.Analyze(p, []byte(input), privacy.NewRunMapper())
			if err2 != nil {
				t.Fatalf("deterministic rerun failed: %v", err2)
			}
			if again.CanonicalFingerprintHMAC != res.CanonicalFingerprintHMAC ||
				again.DialectFingerprintHMAC != res.DialectFingerprintHMAC ||
				again.AnonymizedSQL != res.AnonymizedSQL {
				t.Fatalf("nondeterministic for profile %s", p)
			}
			if lower == "" && len(input) > 0 && res.StatementKind != "other" {
				t.Fatalf("empty anonymized output for non-empty input")
			}
		}
	})
}
