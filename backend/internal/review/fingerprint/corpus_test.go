package fingerprint

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

const corpusRelPath = "../../../../testdata/fingerprint"
const goldenHashRelPath = "../../../../testdata/fingerprint/golden-hashes.json"

type corpusCase struct {
	ID      string     `json:"id"`
	Profile string     `json:"profile"`
	SQL     string     `json:"sql"`
	SQLHex  string     `json:"sql_hex"`
	Expect  caseExpect `json:"expect"`
}

type caseExpect struct {
	Kind               string         `json:"kind"`
	Quality            string         `json:"quality"`
	LiteralTypes       map[string]int `json:"literal_types"`
	ParameterCount     int            `json:"parameter_count"`
	TupleCount         int            `json:"tuple_count"`
	InListSizes        []int          `json:"in_list_sizes"`
	FeaturesInclude    []string       `json:"features_include"`
	HintsInclude       []string       `json:"hints_include"`
	AnonymizedContains []string       `json:"anonymized_contains"`
	AnonymizedAbsent   []string       `json:"anonymized_absent"`
}

type corpusFile struct {
	ContractVersion   int                 `json:"contract_version"`
	AlgorithmVersion  string              `json:"algorithm_version"`
	TestKeyID         string              `json:"test_key_id"`
	EquivalenceGroups map[string][]string `json:"equivalence_groups"`
	DifferencePairs   [][2]string         `json:"difference_pairs"`
	Cases             []corpusCase        `json:"cases"`
	FailClosed        []struct {
		ID      string `json:"id"`
		Profile string `json:"profile"`
		SQL     string `json:"sql"`
		SQLHex  string `json:"sql_hex"`
		Error   string `json:"error"`
	} `json:"fail_closed"`
}

func loadCorpus(t *testing.T) corpusFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(corpusDir(t), "golden-corpus.json"))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c corpusFile
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("corpus json: %v", err)
	}
	if c.AlgorithmVersion != AlgorithmVersion {
		t.Fatalf("corpus algorithm %s != engine %s", c.AlgorithmVersion, AlgorithmVersion)
	}
	return c
}

// corpusDir returns the absolute path to repo-level testdata/fingerprint (four directories up from package dir).
func corpusDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(corpusRelPath)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return dir
}

// TestGoldenCorpus runs all golden samples: structural expectations, equivalence groups, distinct pairs,
// leak assertions (built into the engine), and frozen HMACs (golden-hashes.json, rewritten when GOLDEN_UPDATE=1).
func TestGoldenCorpus(t *testing.T) {
	corpus := loadCorpus(t)
	e := testEngine(t)
	results := map[string]Result{}

	for _, c := range corpus.Cases {
		sql := c.SQL
		if c.SQLHex != "" {
			raw, err := hex.DecodeString(c.SQLHex)
			if err != nil {
				t.Fatalf("%s: sql_hex: %v", c.ID, err)
			}
			sql = string(raw)
		}
		profile, err := ParseProfile(c.Profile)
		if err != nil {
			t.Fatalf("%s: profile: %v", c.ID, err)
		}
		res, err := e.Analyze(profile, []byte(sql), privacy.NewRunMapper())
		if err != nil {
			t.Fatalf("%s: analyze failed: %v", c.ID, err)
		}
		results[c.ID] = res
		assertCaseExpectation(t, c.ID, res, c.Expect)
	}

	for groupName, ids := range corpus.EquivalenceGroups {
		if len(ids) < 2 {
			continue
		}
		base := results[ids[0]]
		for _, id := range ids[1:] {
			if results[id].CanonicalFingerprintHMAC != base.CanonicalFingerprintHMAC {
				t.Fatalf("equivalence group %s: %s != %s canonical", groupName, id, ids[0])
			}
		}
	}
	for _, pair := range corpus.DifferencePairs {
		a, b := results[pair[0]], results[pair[1]]
		if a.DialectFingerprintHMAC == b.DialectFingerprintHMAC {
			t.Fatalf("difference pair %s/%s has identical dialect fingerprint", pair[0], pair[1])
		}
	}
	for _, fc := range corpus.FailClosed {
		sql := fc.SQL
		if fc.SQLHex != "" {
			raw, _ := hex.DecodeString(fc.SQLHex)
			sql = string(raw)
		}
		profile, err := ParseProfile(fc.Profile)
		if err != nil {
			t.Fatalf("%s: profile: %v", fc.ID, err)
		}
		_, err = e.Analyze(profile, []byte(sql), privacy.NewRunMapper())
		f, ok := err.(*Failure)
		if !ok {
			t.Fatalf("%s: expected failure, got %v", fc.ID, err)
		}
		if f.Code != fc.Error {
			t.Fatalf("%s: code %s want %s", fc.ID, f.Code, fc.Error)
		}
	}

	assertGoldenHashes(t, corpus, results)
}

func assertCaseExpectation(t *testing.T, id string, res Result, exp caseExpect) {
	t.Helper()
	if exp.Kind != "" && res.StatementKind != exp.Kind {
		t.Fatalf("%s: kind = %s, want %s (anon=%s)", id, res.StatementKind, exp.Kind, res.AnonymizedSQL)
	}
	if exp.Quality != "" && res.Quality != exp.Quality {
		t.Fatalf("%s: quality = %s, want %s (anon=%s)", id, res.Quality, exp.Quality, res.AnonymizedSQL)
	}
	if exp.LiteralTypes != nil {
		if len(exp.LiteralTypes) != len(res.LiteralTypeCounts) {
			t.Fatalf("%s: literal types = %v, want %v", id, res.LiteralTypeCounts, exp.LiteralTypes)
		}
		for k, v := range exp.LiteralTypes {
			if res.LiteralTypeCounts[k] != v {
				t.Fatalf("%s: literal %s = %d, want %d (anon=%s)", id, k, res.LiteralTypeCounts[k], v, res.AnonymizedSQL)
			}
		}
	}
	if exp.ParameterCount > 0 && res.ParameterCount != exp.ParameterCount {
		t.Fatalf("%s: parameter_count = %d, want %d", id, res.ParameterCount, exp.ParameterCount)
	}
	if exp.TupleCount > 0 && res.TupleCount != exp.TupleCount {
		t.Fatalf("%s: tuple_count = %d, want %d", id, res.TupleCount, exp.TupleCount)
	}
	if exp.InListSizes != nil {
		if len(exp.InListSizes) != len(res.InListSizes) {
			t.Fatalf("%s: in sizes = %v, want %v", id, res.InListSizes, res.InListSizes)
		}
		for i, v := range exp.InListSizes {
			if res.InListSizes[i] != v {
				t.Fatalf("%s: in size[%d] = %d, want %d", id, i, res.InListSizes[i], v)
			}
		}
	}
	for _, f := range exp.FeaturesInclude {
		if !containsStr(res.DialectFeatures, f) {
			t.Fatalf("%s: features %v missing %s", id, res.DialectFeatures, f)
		}
	}
	for _, h := range exp.HintsInclude {
		if !containsStr(res.OptimizerHints, h) {
			t.Fatalf("%s: hints %v missing %s", id, res.OptimizerHints, h)
		}
	}
	lower := strings.ToLower(res.AnonymizedSQL)
	for _, want := range exp.AnonymizedContains {
		if !strings.Contains(lower, strings.ToLower(want)) {
			t.Fatalf("%s: anonymized %q missing %q", id, res.AnonymizedSQL, want)
		}
	}
	for _, banned := range exp.AnonymizedAbsent {
		if strings.Contains(lower, strings.ToLower(banned)) {
			t.Fatalf("%s: anonymized contains banned %q: %s", id, banned, res.AnonymizedSQL)
		}
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// assertGoldenHashes compares case-by-case against the frozen hash file (canonical/dialect/shape + anonymized).
func assertGoldenHashes(t *testing.T, corpus corpusFile, results map[string]Result) {
	t.Helper()
	path := filepath.Join(corpusDir(t), "golden-hashes.json")
	data, err := os.ReadFile(path)
	if os.Getenv("GOLDEN_UPDATE") == "1" {
		writeGoldenHashes(t, path, corpus, results)
		return
	}
	if err != nil {
		t.Fatalf("golden hashes missing (%v); run with GOLDEN_UPDATE=1 once after review", err)
	}
	var frozen map[string]map[string]string
	if err := json.Unmarshal(data, &frozen); err != nil {
		t.Fatalf("golden hashes json: %v", err)
	}
	if len(frozen) != len(corpus.Cases) {
		t.Fatalf("golden hash case count %d != corpus %d", len(frozen), len(corpus.Cases))
	}
	for id, res := range results {
		want, ok := frozen[id]
		if !ok {
			t.Fatalf("golden hashes missing case %s", id)
		}
		if want["canonical"] != res.CanonicalFingerprintHMAC {
			t.Fatalf("%s: canonical hash drift", id)
		}
		if want["dialect"] != res.DialectFingerprintHMAC {
			t.Fatalf("%s: dialect hash drift", id)
		}
		if want["shape"] != res.ExecutionShapeHMAC {
			t.Fatalf("%s: shape hash drift", id)
		}
		if want["anonymized"] != res.AnonymizedSQL {
			t.Fatalf("%s: anonymized drift: got %q want %q", id, res.AnonymizedSQL, want["anonymized"])
		}
	}
}

func writeGoldenHashes(t *testing.T, path string, corpus corpusFile, results map[string]Result) {
	t.Helper()
	out := map[string]map[string]string{}
	for _, c := range corpus.Cases {
		res := results[c.ID]
		out[c.ID] = map[string]string{
			"canonical":  res.CanonicalFingerprintHMAC,
			"dialect":    res.DialectFingerprintHMAC,
			"shape":      res.ExecutionShapeHMAC,
			"anonymized": res.AnonymizedSQL,
		}
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write golden: %v", err)
	}
	fmt.Printf("golden hashes written: %s (%d cases)\n", path, len(out))
}
