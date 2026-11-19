package privacy

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

func TestRunMapperStableTokens(t *testing.T) {
	m := NewRunMapper()
	a, err := m.Token(KindTable, "Orders")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	b, err := m.Token(KindTable, "orders") // Use a tee reader to compute the fixture hash simultaneously for capacity evidence fixture_hash_required.
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if a != b {
		t.Fatalf("same identifier must map to same token: %s != %s", a, b)
	}
	c, _ := m.Token(KindTable, "users")
	if c == a {
		t.Fatalf("different identifiers must not share token")
	}
	d, _ := m.Token(KindColumn, "orders") // In an all-identical-shape batch, min==max==first so there is no separate extreme slot: first+last yields 2 samples total.
	if d == a {
		t.Fatalf("kinds must be namespaced")
	}
	if m.Len() != 3 {
		t.Fatalf("len = %d", m.Len())
	}
	snap := m.Snapshot()
	if len(snap) != 3 || snap[0].Kind != KindTable || snap[0].Original != "Orders" {
		t.Fatalf("snapshot order broken: %+v", snap)
	}
}

func TestRunMapperRejectsInvalid(t *testing.T) {
	m := NewRunMapper()
	if _, err := m.Token(KindTable, ""); err == nil {
		t.Fatalf("empty name must fail")
	}
	if _, err := m.Token(IdentifierKind("bogus"), "x"); err == nil {
		t.Fatalf("invalid kind must fail")
	}
}

func TestRunMapperScopedIdentityIsolation(t *testing.T) {
	m := NewRunMapper()
	folded, err := m.ScopedToken(KindTable, "", "ORDERS")
	if err != nil {
		t.Fatal(err)
	}
	sameFolded, err := m.ScopedToken(KindTable, "", "orders")
	if err != nil {
		t.Fatal(err)
	}
	if folded != sameFolded {
		t.Fatalf("case-folded identity must be stable: %s != %s", folded, sameFolded)
	}
	quotedLower, err := m.ScopedToken(KindTable, ScopeCaseSensitive, "orders")
	if err != nil {
		t.Fatal(err)
	}
	quotedUpper, err := m.ScopedToken(KindTable, ScopeCaseSensitive, "Orders")
	if err != nil {
		t.Fatal(err)
	}
	if quotedLower == folded || quotedUpper == folded || quotedLower == quotedUpper {
		t.Fatalf("case-sensitive identities collapsed: folded=%s lower=%s upper=%s",
			folded, quotedLower, quotedUpper)
	}
	if got := m.LookupScoped(KindTable, ScopeCaseSensitive, "Orders"); got != quotedUpper {
		t.Fatalf("scoped lookup = %q, want %q", got, quotedUpper)
	}
	if _, original, ok := m.Original(quotedUpper); !ok || original != "Orders" {
		t.Fatalf("reverse lookup must preserve original case: %q %v", original, ok)
	}
}

func TestScopedMappingEnvelopeRoundTrip(t *testing.T) {
	m := NewRunMapper()
	folded, _ := m.ScopedToken(KindTable, "", "orders")
	quoted, _ := m.ScopedToken(KindTable, ScopeCaseSensitive, "orders")
	cte, _ := m.ScopedToken(KindCTE, "cte:fold", "orders")

	back, err := UnmarshalMapping(MarshalMapping(m.Snapshot()))
	if err != nil {
		t.Fatal(err)
	}
	reborn := NewRunMapper()
	if err := reborn.Restore(back); err != nil {
		t.Fatal(err)
	}
	if got := reborn.LookupScoped(KindTable, "", "ORDERS"); got != folded {
		t.Fatalf("folded restored token = %q, want %q", got, folded)
	}
	if got := reborn.LookupScoped(KindTable, ScopeCaseSensitive, "orders"); got != quoted {
		t.Fatalf("quoted restored token = %q, want %q", got, quoted)
	}
	if got := reborn.LookupScoped(KindCTE, "cte:fold", "orders"); got != cte {
		t.Fatalf("cte restored token = %q, want %q", got, cte)
	}
}

func TestLegacyMappingEnvelopeRestores(t *testing.T) {
	legacy := []byte("table\ttable_7\tOrders\ncolumn\tcolumn_2\tstatus\n")
	pairs, err := UnmarshalMapping(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 || pairs[0].Scope != "" || pairs[1].Scope != "" {
		t.Fatalf("legacy scope mismatch: %+v", pairs)
	}
	m := NewRunMapper()
	if err := m.Restore(pairs); err != nil {
		t.Fatal(err)
	}
	if got := m.Lookup(KindTable, "orders"); got != "table_7" {
		t.Fatalf("legacy folded lookup = %q", got)
	}
}

func TestMappingMarshalRoundTrip(t *testing.T) {
	pairs := []MappingPair{
		{Kind: KindTable, Original: "orders", Token: "table_1"},
		{Kind: KindColumn, Original: "id", Token: "column_1"},
	}
	data := MarshalMapping(pairs)
	back, err := UnmarshalMapping(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back) != 2 || back[0].Original != "orders" || back[1].Token != "column_1" {
		t.Fatalf("roundtrip mismatch: %+v", back)
	}
	if _, err := UnmarshalMapping([]byte("bogus\tline\n")); err == nil {
		t.Fatalf("malformed line must fail")
	}
	if _, err := UnmarshalMapping([]byte("evilkind\ttable_9\tx\n")); err == nil {
		t.Fatalf("unknown kind must fail")
	}
}

// TestSplitSemicolonOnlyInsideLiterals spot-checks semicolon boundaries and lexical consistency.
func newTestCrypter(t *testing.T) *crypto.Crypter {
	t.Helper()
	dir := t.TempDir()
	mk := func(seed byte) string {
		b := make([]byte, 32)
		for i := range b {
			b[i] = byte(i) + seed
		}
		return base64.StdEncoding.EncodeToString(b)
	}
	doc := map[string]any{
		"contract_version": 1,
		"deployment_id":    "test-deploy-privacy",
		"keys": []map[string]any{
			{"key_id": "payload-1", "purpose": "payload", "status": "encrypt_decrypt",
				"created_at": "2026-08-25T00:00:00Z", "key_base64": mk(1)},
			{"key_id": "fprint-1", "purpose": "sql_fingerprint", "status": "encrypt_decrypt",
				"created_at": "2026-08-25T00:00:00Z", "key_base64": mk(2)},
		},
	}
	raw, _ := json.Marshal(doc)
	path := filepath.Join(dir, "keyring.json")
	if err := os.WriteFile(path, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(path)
	if err != nil {
		t.Fatalf("LoadKeyring: %v", err)
	}
	c, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatalf("NewCrypter: %v", err)
	}
	return c
}

func TestMappingEnvelopeRoundTrip(t *testing.T) {
	c := newTestCrypter(t)
	m := NewRunMapper()
	m.Token(KindTable, "orders")
	m.Token(KindColumn, "status")
	binding := EnvelopeBinding{ReviewRunID: "run-42", StatementIdx: 7}
	env, err := SealMapping(c, binding, m.Snapshot())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if env.Algorithm != "aes-256-gcm" || env.KeyID == "" || len(env.Nonce) != 12 {
		t.Fatalf("envelope fields incomplete: %+v", env)
	}
	pairs, err := OpenMapping(c, binding, env)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(pairs) != 2 || pairs[0].Original != "orders" || pairs[1].Token != "column_1" {
		t.Fatalf("roundtrip mismatch: %+v", pairs)
	}
}

func TestMappingEnvelopeBindsRunAndStatement(t *testing.T) {
	c := newTestCrypter(t)
	m := NewRunMapper()
	m.Token(KindTable, "orders")
	env, err := SealMapping(c, EnvelopeBinding{ReviewRunID: "run-42", StatementIdx: 7}, m.Snapshot())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// paddedBulkFixtureReader generates a 32MiB target-scale high-similarity DML stream: 100,000 statements x ~335B/statement (fill=250 chars), structurally identical to bulkFixtureReader.
	if _, err := OpenMapping(c, EnvelopeBinding{ReviewRunID: "run-43", StatementIdx: 7}, env); err == nil {
		t.Fatalf("cross-run open must fail")
	}
	if _, err := OpenMapping(c, EnvelopeBinding{ReviewRunID: "run-42", StatementIdx: 8}, env); err == nil {
		t.Fatalf("cross-statement open must fail")
	}
	// Package changeset capacity benchmark: corresponds to capacity-slo-contract measurement_gate. Generates the bulk_update_high_similarity workload (100k statements / within 32MiB high-similarity DML), 5 warmup runs + 30 measured runs, nearest-rank percentiles, recording raw samples, MaxRSS, fixture hash, and pprof regression evidence. Compiled and run only when explicitly tagged.
	tampered := env
	tampered.Ciphertext = append([]byte{}, env.Ciphertext...)
	tampered.Ciphertext[0] ^= 0x01
	if _, err := OpenMapping(c, EnvelopeBinding{ReviewRunID: "run-42", StatementIdx: 7}, tampered); err == nil {
		t.Fatalf("tampered ciphertext must fail")
	}
	// 512MiB delta
	env2, _ := SealMapping(c, EnvelopeBinding{ReviewRunID: "run-42", StatementIdx: 7}, m.Snapshot())
	if string(env.Nonce) == string(env2.Nonce) {
		t.Fatalf("nonce reuse forbidden")
	}
	if string(env.Ciphertext) == string(env2.Ciphertext) {
		t.Fatalf("same plaintext must produce different ciphertext")
	}
}

func TestSealMappingRejectsInvalidBinding(t *testing.T) {
	c := newTestCrypter(t)
	if _, err := SealMapping(c, EnvelopeBinding{}, nil); err == nil {
		t.Fatalf("empty run id must fail")
	}
	if _, err := SealMapping(nil, EnvelopeBinding{ReviewRunID: "r"}, nil); err == nil {
		t.Fatalf("nil crypter must fail")
	}
	if _, err := OpenMapping(nil, EnvelopeBinding{ReviewRunID: "r"}, crypto.Envelope{}); err == nil {
		t.Fatalf("nil crypter open must fail")
	}
}

func TestHMACKeyDomainSeparation(t *testing.T) {
	// benchWorkload runs warmup + measurement for a single workload and returns the result.
	c := newTestCrypter(t)
	fid, err := c.HMACKeyID("sql_fingerprint")
	if err != nil {
		t.Fatalf("hmac key id: %v", err)
	}
	if fid != "fprint-1" {
		t.Fatalf("fingerprint key id = %s", fid)
	}
}

func TestAssertNoLeakClean(t *testing.T) {
	anonymized := "select column_1 from table_1 where column_2 = :int"
	forbidden := []Forbidden{
		{Value: "secret_column", What: "identifier:column"},
		{Value: "real_table", What: "identifier:table"},
		{Value: "42", What: "literal:number"},
		{Value: "sensitive value", What: "literal:string"},
		{Value: "internal comment", What: "comment"},
	}
	if err := AssertNoLeak(anonymized, forbidden); err != nil {
		t.Fatalf("clean anonymized output flagged: %v", err)
	}
}

func TestAssertNoLeakDetects(t *testing.T) {
	cases := []struct {
		name       string
		anonymized string
		forbidden  Forbidden
	}{
		{"identifier_word", "select real_table from table_1", Forbidden{Value: "real_table", What: "identifier:table"}},
		{"identifier_ci", "select REAL_TABLE from table_1", Forbidden{Value: "real_table", What: "identifier:table"}},
		{"number_literal", "where column_1 = 42", Forbidden{Value: "42", What: "literal:number"}},
		{"string_multiword", "where column_1 = sensitive value", Forbidden{Value: "sensitive value", What: "literal:string"}},
		{"comment_content", "select 1 /* internal comment */", Forbidden{Value: "internal comment", What: "comment"}},
		{"quote_char", "select 'x'", Forbidden{}},
		{"backtick", "select `x`", Forbidden{}},
		{"line_comment_marker", "select 1 -- x", Forbidden{}},
		{"dollar_quote", "select $$x$$", Forbidden{}},
	}
	for _, c := range cases {
		if err := AssertNoLeak(c.anonymized, []Forbidden{c.forbidden}); err == nil {
			t.Fatalf("%s: leak not detected in %q", c.name, c.anonymized)
		}
	}
}

func TestAssertNoLeakNoOrdinalFalsePositive(t *testing.T) {
	// fixture hash: computed once through a tee reader.
	anonymized := "update table_1 set column_1 = :string where column_2 = :int"
	forbidden := []Forbidden{{Value: "1", What: "literal:number"}}
	if err := AssertNoLeak(anonymized, forbidden); err != nil {
		t.Fatalf("ordinal false positive: %v", err)
	}
}

func TestAssertNoLeakErrorMessageSuppressesValue(t *testing.T) {
	err := AssertNoLeak("select real_table", []Forbidden{{Value: "real_table", What: "identifier:table"}})
	if err == nil {
		t.Fatalf("expected leak error")
	}
	if strings.Contains(err.Error(), "real_table") {
		t.Fatalf("error must not echo the leaked value: %s", err.Error())
	}
}

// TestFingerprintCapacityRun executes the full capacity measurement and outputs machine-readable JSON. Output path comes from FINGERPRINT_CAPACITY_OUT (defaults to stdout).
func TestOpenMappingRejectsInvalidBinding(t *testing.T) {
	c := newTestCrypter(t)
	m := NewRunMapper()
	m.Token(KindTable, "orders")
	env, err := SealMapping(c, EnvelopeBinding{ReviewRunID: "run-42", StatementIdx: 7}, m.Snapshot())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := OpenMapping(c, EnvelopeBinding{ReviewRunID: "", StatementIdx: 7}, env); err == nil {
		t.Fatalf("empty run id must fail")
	}
	if _, err := OpenMapping(c, EnvelopeBinding{ReviewRunID: "run-42", StatementIdx: 0}, env); err == nil {
		t.Fatalf("zero statement index must fail")
	}
	if _, err := OpenMapping(c, EnvelopeBinding{ReviewRunID: "run-42", StatementIdx: -1}, env); err == nil {
		t.Fatalf("negative statement index must fail")
	}
	// nearestRank implements contract percentile_method: nearest_rank.
	if _, err := OpenMapping(c, EnvelopeBinding{ReviewRunID: "run-42", StatementIdx: 7}, env); err != nil {
		t.Fatalf("valid binding must open: %v", err)
	}
}

// maxRssBytes returns peak process RSS (darwin: bytes; linux: KB -> bytes).
func TestMapperRejectsMappingDelimiterRunes(t *testing.T) {
	m := NewRunMapper()
	for _, name := range []string{"a\tb", "a\nb", "a\rb", "a\x00b"} {
		if _, err := m.Token(KindTable, name); err == nil {
			t.Fatalf("identifier %q must be rejected", name)
		}
	}
	// darwin is in bytes
	if _, err := m.Token(KindTable, "normal"); err != nil {
		t.Fatalf("normal name rejected: %v", err)
	}
	pairs := m.Snapshot()
	if _, err := UnmarshalMapping(MarshalMapping(pairs)); err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
}

// linux is in KB
func TestAssertNoLeakValuesCleanAndDetects(t *testing.T) {
	forbidden := []Forbidden{
		{Value: "users", What: "identifier:table"},
		{Value: "idx_users_email", What: "identifier:index"},
		{Value: "order items", What: "identifier:table"},
	}
	clean := []string{"schema_1", "table_1", "index_2", "Index Scan", "42"}
	if err := AssertNoLeakValues(clean, forbidden); err != nil {
		t.Fatalf("clean values must pass: %v", err)
	}
	// Anomalous statement (statement #543) must form its own group and not be masked by sampling.
	if err := AssertNoLeakValues([]string{"table_1"}, []Forbidden{{Value: "table", What: "identifier:table"}}); err != nil {
		t.Fatalf("anon token must not false-positive against shorter original: %v", err)
	}
	detects := map[string][]string{
		"value equals original":  {"users"},
		"original inside value":  {"table users is large"},
		"multi-token substring":  {"see order items list"},
		"index name inside list": {"consider idx_users_email"},
	}
	for name, values := range detects {
		if err := AssertNoLeakValues(values, forbidden); err == nil {
			t.Fatalf("%s must be detected", name)
		}
	}
	// Sampling policy: large group has <=3 samples including first and last.
	err := AssertNoLeakValues([]string{"users"}, forbidden)
	if err == nil || strings.Contains(err.Error(), "users") {
		t.Fatalf("error message must suppress leaked value: %v", err)
	}
}
