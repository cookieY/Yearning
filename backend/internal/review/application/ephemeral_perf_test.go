package application

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/platform/hashutil"
	"github.com/cookieY/YearningX/backend/internal/review/changeset"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

type perfTestSigner struct {
	key []byte
}

func (perfTestSigner) KeyID() string { return "perf-test-key" }

func (s perfTestSigner) HMACSHA256(data []byte) ([]byte, error) {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(data)
	return mac.Sum(nil), nil
}

func newPerfTestEngine(t *testing.T) *fingerprint.Engine {
	t.Helper()
	engine, err := fingerprint.NewEngine(perfTestSigner{key: []byte("0123456789abcdef0123456789abcdef")}, fingerprint.Options{})
	if err != nil {
		t.Fatalf("new perf test fingerprint engine: %v", err)
	}
	return engine
}

func TestSha256ValidityProgramMatchesJSONMarshal(t *testing.T) {
	repl := fingerprint.ValidityObject{
		Kind:        fingerprint.ValidityObjectTable,
		Token:       "table_2",
		SchemaToken: "schema_1",
	}
	repeatedFacts := fingerprint.StatementValidity{
		Complete:            true,
		DefinitionsComplete: true,
		Operation:           fingerprint.ValidityOperationUpdate,
		Requirements: []fingerprint.ValidityRequirement{
			{
				Object: fingerprint.ValidityObject{
					Kind:        fingerprint.ValidityObjectTable,
					Token:       "table_1",
					SchemaToken: "schema_1",
				},
				State:     fingerprint.ValidityRequirementPresent,
				Condition: fingerprint.ValidityConditionNone,
			},
			{
				Object: fingerprint.ValidityObject{
					Kind:        fingerprint.ValidityObjectColumn,
					Token:       "column_1",
					SchemaToken: "schema_1",
					ParentToken: "table_1",
				},
				State:     fingerprint.ValidityRequirementPresent,
				Condition: fingerprint.ValidityConditionNone,
			},
		},
	}
	renameFacts := fingerprint.StatementValidity{
		Complete:            true,
		DefinitionsComplete: false,
		Operation:           fingerprint.ValidityOperationAlter,
		Requirements: []fingerprint.ValidityRequirement{
			{
				Object: fingerprint.ValidityObject{
					Kind:        fingerprint.ValidityObjectTable,
					Token:       "table_1",
					SchemaToken: "schema_1",
				},
				State:     fingerprint.ValidityRequirementPresent,
				Condition: fingerprint.ValidityConditionIfExists,
			},
		},
		Effects: []fingerprint.ValidityEffect{
			{
				Action: fingerprint.ValidityEffectRename,
				Object: fingerprint.ValidityObject{
					Kind:        fingerprint.ValidityObjectTable,
					Token:       "table_1",
					SchemaToken: "schema_1",
				},
				Replacement: &repl,
				Condition:   fingerprint.ValidityConditionNone,
			},
		},
		Unsupported: "partial_ddl",
	}

	cases := []struct {
		name string
		prog ValidityProgram
	}{
		{
			name: "nil_statements",
			prog: ValidityProgram{Complete: false, Statements: nil},
		},
		{
			name: "empty_statements",
			prog: ValidityProgram{Complete: true, Statements: []StatementValidityAtOrdinal{}},
		},
		{
			name: "single_statement_with_replacement",
			prog: ValidityProgram{
				Complete: true,
				Statements: []StatementValidityAtOrdinal{
					{Ordinal: 1, Facts: renameFacts},
				},
			},
		},
		{
			name: "multi_statement_mixed_repeated_and_distinct",
			prog: ValidityProgram{
				Complete: true,
				Statements: []StatementValidityAtOrdinal{
					{Ordinal: 1, Facts: repeatedFacts},
					{Ordinal: 2, Facts: repeatedFacts},
					{Ordinal: 3, Facts: renameFacts},
					{Ordinal: 4, Facts: repeatedFacts},
					{Ordinal: 5, Facts: fingerprint.StatementValidity{Complete: false, Operation: fingerprint.ValidityOperationSelect}},
				},
			},
		},
		{
			name: "html_escaping_and_nil_vs_empty_slices",
			prog: ValidityProgram{
				Complete: true,
				Statements: []StatementValidityAtOrdinal{
					{
						Ordinal: 1,
						Facts: fingerprint.StatementValidity{
							Complete:            false,
							DefinitionsComplete: false,
							Operation:           fingerprint.ValidityOperationSelect,
							Unsupported:         "expr < 1 & x > 0",
						},
					},
					{
						Ordinal: 2,
						Facts: fingerprint.StatementValidity{
							Complete:            false,
							DefinitionsComplete: false,
							Operation:           fingerprint.ValidityOperationSelect,
							Requirements:        []fingerprint.ValidityRequirement{},
							Effects:             []fingerprint.ValidityEffect{},
							Unsupported:         "expr < 1 & x > 0",
						},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sha256ValidityProgram(tc.prog)
			if err != nil {
				t.Fatalf("sha256ValidityProgram: %v", err)
			}
			raw, err := json.Marshal(tc.prog)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			want := hashutil.SHA256Hex(raw)
			if got != want {
				t.Fatalf("digest mismatch: got %s, want %s", got, want)
			}
		})
	}
}

func TestEphemeralStoreMemoryBudgetAndTerminalEviction(t *testing.T) {
	st := NewEphemeralStore(time.Minute)
	st.maxActiveSQLBytes = 100
	st.maxTerminalRuns = 3

	r1 := &EphemeralRun{
		ID: "r1", OwnerUserID: "u1", state: domain.RunStateRunning,
		SQLPlaintext: make([]byte, 60), CreatedAt: time.Now(),
	}
	if err := st.admit(r1, ephemeralMaxPerUser); err != nil {
		t.Fatalf("admit r1 failed: %v", err)
	}
	if st.activeSQLBytes != 60 {
		t.Fatalf("activeSQLBytes = %d, want 60", st.activeSQLBytes)
	}

	r2 := &EphemeralRun{
		ID: "r2", OwnerUserID: "u2", state: domain.RunStateQueued,
		SQLPlaintext: make([]byte, 50), CreatedAt: time.Now(),
	}
	err := st.admit(r2, ephemeralMaxPerUser)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != 1012 {
		t.Fatalf("expected 1012 capacity error, got %v", err)
	}
	if st.activeSQLBytes != 60 {
		t.Fatalf("rejected admit altered activeSQLBytes: got %d, want 60", st.activeSQLBytes)
	}
	if st.Get("r2", "u2") != nil {
		t.Fatal("rejected run must not be stored")
	}

	st.releaseSQLBytes(r1)
	if st.activeSQLBytes != 0 {
		t.Fatalf("releaseSQLBytes left activeSQLBytes = %d, want 0", st.activeSQLBytes)
	}
	if err := st.admit(r2, ephemeralMaxPerUser); err != nil {
		t.Fatalf("admit r2 after release failed: %v", err)
	}
	if took := st.Take("r2", "u2"); took == nil || st.activeSQLBytes != 0 {
		t.Fatalf("Take did not restore activeSQLBytes: took=%v active=%d", took != nil, st.activeSQLBytes)
	}
	st.releaseSQLBytes(r1)
	_ = st.Take("r1", "u1")

	now := time.Now()
	exp := &EphemeralRun{
		ID: "expired", OwnerUserID: "u3", state: domain.RunStateReady,
		SQLPlaintext: make([]byte, 40), CreatedAt: now, expiresAt: now.Add(time.Minute),
	}
	if err := st.admit(exp, ephemeralMaxPerUser); err != nil {
		t.Fatalf("admit exp: %v", err)
	}
	exp.mu.Lock()
	exp.expiresAt = now.Add(-time.Second)
	exp.mu.Unlock()
	st.sweepNow(now)
	if st.activeSQLBytes != 0 {
		t.Fatalf("sweepNow must release expired sql bytes, got %d", st.activeSQLBytes)
	}

	// Terminal eviction order: active runs are immune, oldest non-ready terminal runs evict before oldest ready runs.
	t0 := now.Add(-10 * time.Minute)
	fin := func(offset time.Duration) *time.Time {
		v := t0.Add(offset)
		return &v
	}
	activeRun := &EphemeralRun{
		ID: "active", OwnerUserID: "u4", state: domain.RunStateRunning,
		CreatedAt: t0, expiresAt: now.Add(time.Hour),
	}
	failedOld := &EphemeralRun{
		ID: "failed-old", OwnerUserID: "u4", state: domain.RunStateFailed,
		CreatedAt: t0, FinishedAt: fin(1 * time.Minute), expiresAt: now.Add(time.Hour),
	}
	blockedNew := &EphemeralRun{
		ID: "blocked-new", OwnerUserID: "u4", state: domain.RunStateBlocked,
		CreatedAt: t0, FinishedAt: fin(4 * time.Minute), expiresAt: now.Add(time.Hour),
	}
	readyOld := &EphemeralRun{
		ID: "ready-old", OwnerUserID: "u4", state: domain.RunStateReady,
		CreatedAt: t0, FinishedAt: fin(2 * time.Minute), expiresAt: now.Add(time.Hour),
	}
	readyNew := &EphemeralRun{
		ID: "ready-new", OwnerUserID: "u4", state: domain.RunStateReady,
		CreatedAt: t0, FinishedAt: fin(3 * time.Minute), expiresAt: now.Add(time.Hour),
	}
	for _, r := range []*EphemeralRun{activeRun, failedOld, blockedNew, readyOld, readyNew} {
		st.put(r)
	}
	st.enforceTerminalCap()

	if st.Get("active", "u4") == nil {
		t.Fatal("active run must never be evicted by terminal cap")
	}
	if st.Get("failed-old", "u4") != nil {
		t.Fatal("oldest non-ready terminal run must be evicted first")
	}
	for _, keep := range []string{"blocked-new", "ready-old", "ready-new"} {
		if st.Get(keep, "u4") == nil {
			t.Fatalf("expected run %s to remain within terminal cap", keep)
		}
	}

	// Next, replace remaining non-ready run and force eviction among ready runs only.
	_ = st.Take("blocked-new", "u4")
	readyOlderStill := &EphemeralRun{
		ID: "ready-oldest", OwnerUserID: "u4", state: domain.RunStateReady,
		CreatedAt: t0, FinishedAt: fin(30 * time.Second), expiresAt: now.Add(time.Hour),
	}
	readyLatest := &EphemeralRun{
		ID: "ready-latest", OwnerUserID: "u4", state: domain.RunStateReady,
		CreatedAt: t0, FinishedAt: fin(5 * time.Minute), expiresAt: now.Add(time.Hour),
	}
	st.put(readyOlderStill)
	st.put(readyLatest)
	st.enforceTerminalCap()
	if st.Get("ready-oldest", "u4") != nil {
		t.Fatal("oldest ready run must be evicted when all non-ready runs are gone")
	}
	if st.Get("active", "u4") == nil || st.Get("ready-old", "u4") == nil || st.Get("ready-new", "u4") == nil || st.Get("ready-latest", "u4") == nil {
		t.Fatal("expected active and newest ready runs to survive terminal eviction")
	}
}

func TestAnalyzeRunStatementsStreaming_ParityAndScale(t *testing.T) {
	engine := newPerfTestEngine(t)
	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}

	t.Run("parity_inline_vs_streamed", func(t *testing.T) {
		var buf bytes.Buffer
		for i := 1; i <= 150; i++ {
			fmt.Fprintf(&buf, "UPDATE orders SET amount = %d WHERE id = %d;\n", i*10, i)
			fmt.Fprintf(&buf, "UPDATE orders SET amount = amount + %d WHERE id = %d AND amount > 0;\n", i, i)
			fmt.Fprintf(&buf, "INSERT INTO orders (id, amount) VALUES (%d, %d);\n", i, i*2)
			fmt.Fprintf(&buf, "SELECT id, amount FROM orders WHERE id = %d;\n", i)
		}
		rawSQL := buf.Bytes()

		mapperStream := privacy.NewRunMapper()
		streamed, err := analyzeRunStatementsStreaming(streamingAnalysisOptions{
			runID:           "run-parity",
			rawSQL:          rawSQL,
			profile:         profile,
			mapper:          mapperStream,
			engine:          engine,
			expectedCount:   0,
			validateSyntax:  true,
			inlineLimit:     ephemeralInlineStatementLimit,
			collectPostgres: false,
		})
		if err != nil {
			t.Fatalf("streaming analysis failed: %v", err)
		}
		if streamed.inlineStatements != nil {
			t.Fatalf("expected inlineStatements to be dropped past limit 500, got %d", len(streamed.inlineStatements))
		}

		mapperInline := privacy.NewRunMapper()
		inlined, err := analyzeRunStatementsStreaming(streamingAnalysisOptions{
			runID:           "run-parity",
			rawSQL:          rawSQL,
			profile:         profile,
			mapper:          mapperInline,
			engine:          engine,
			expectedCount:   600,
			validateSyntax:  true,
			inlineLimit:     1000,
			collectPostgres: false,
		})
		if err != nil {
			t.Fatalf("inline analysis failed: %v", err)
		}
		if len(inlined.inlineStatements) != 600 {
			t.Fatalf("expected 600 inline statements, got %d", len(inlined.inlineStatements))
		}
		if streamed.statementCount != 600 || inlined.statementCount != 600 {
			t.Fatalf("statement count mismatch: streamed=%d inline=%d", streamed.statementCount, inlined.statementCount)
		}
		if len(streamed.groups) != len(inlined.groups) || len(streamed.groups) != 4 {
			t.Fatalf("group count mismatch: streamed=%d inline=%d", len(streamed.groups), len(inlined.groups))
		}
		for i := range streamed.groups {
			sg, ig := streamed.groups[i], inlined.groups[i]
			if sg.canon != ig.canon || sg.dialect != ig.dialect || sg.count != ig.count ||
				sg.representativeOrdinal != ig.representativeOrdinal ||
				sg.representativeAnonymizedSQL != ig.representativeAnonymizedSQL ||
				sg.repKind != ig.repKind || sg.repOperation != ig.repOperation ||
				sg.repSQL != ig.repSQL || sg.sizeMax != ig.sizeMax ||
				sg.inMax != ig.inMax || sg.valMax != ig.valMax ||
				sg.unbounded != ig.unbounded ||
				sg.metadataContextComplete != ig.metadataContextComplete ||
				!reflect.DeepEqual(sg.statementOrdinalRanges, ig.statementOrdinalRanges) ||
				!reflect.DeepEqual(sg.objects, ig.objects) {
				t.Fatalf("group %d mismatch between streamed and inline paths", i)
			}
		}
		if len(streamed.validityRefs) != 600 || len(inlined.validityRefs) != 600 {
			t.Fatalf("validityRefs length mismatch: streamed=%d inline=%d", len(streamed.validityRefs), len(inlined.validityRefs))
		}
		for i := range streamed.validityRefs {
			sr, ir := streamed.validityRefs[i], inlined.validityRefs[i]
			if sr.groupIdx != ir.groupIdx {
				t.Fatalf("ordinal %d groupIdx mismatch: %d vs %d", i+1, sr.groupIdx, ir.groupIdx)
			}
			sv := streamed.validityTable[sr.validityIdx]
			iv := inlined.validityTable[ir.validityIdx]
			if !reflect.DeepEqual(sv, iv) {
				t.Fatalf("ordinal %d validity mismatch: %+v vs %+v", i+1, sv, iv)
			}
		}

		// Verify streaming materialization model builder matches inline statement rows column-for-column.
		split := changeset.NewSplitter(bytes.NewReader(rawSQL), profile, 0)
		mapperMat := privacy.NewRunMapper()
		var idx int
		for {
			st, done, serr := split.Next()
			if serr != nil {
				t.Fatalf("splitter next: %v", serr)
			}
			if done {
				break
			}
			ord := int32(idx + 1)
			res, aerr := engine.Analyze(profile, st.SQL, mapperMat)
			if aerr != nil {
				t.Fatalf("engine analyze ordinal %d: %v", ord, aerr)
			}
			op := strings.ToLower(operationOf(st.SQL))
			row, berr := buildSQLStatementModel(
				"run-parity", inlined.inlineStatements[idx].ID, ord, res, op,
				fingerprint.StatementClassOf(op), objectsAtStatementOrdinal(res.Objects, ord),
			)
			if berr != nil {
				t.Fatalf("buildSQLStatementModel ordinal %d: %v", ord, berr)
			}
			want := inlined.inlineStatements[idx]
			if !reflect.DeepEqual(row, want) {
				t.Fatalf("materialized statement model mismatch at ordinal %d:\ngot:  %+v\nwant: %+v", ord, row, want)
			}
			idx++
		}
		if idx != 600 {
			t.Fatalf("replayed statement count = %d, want 600", idx)
		}
	})

	t.Run("scale_100k_and_late_syntax_error", func(t *testing.T) {
		count := 100000
		if perfTestRaceEnabled {
			count = 5000
		}
		var buf bytes.Buffer
		buf.Grow(count * 350)
		for i := 1; i <= count; i++ {
			fmt.Fprintf(&buf,
				"UPDATE orders SET amount = %d WHERE id = %d AND amount >= %d AND (id + %d) > 0 /* batch_stmt_%06d_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef */;\n",
				i%1000+1, i, i%16, i%16+1, i,
			)
		}
		rawSQL := buf.Bytes()

		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)

		res, err := analyzeRunStatementsStreaming(streamingAnalysisOptions{
			runID:           "run-scale",
			rawSQL:          rawSQL,
			profile:         profile,
			mapper:          privacy.NewRunMapper(),
			engine:          engine,
			expectedCount:   0,
			validateSyntax:  true,
			inlineLimit:     ephemeralInlineStatementLimit,
			collectPostgres: false,
		})
		if err != nil {
			t.Fatalf("scale streaming analysis failed: %v", err)
		}
		if int(res.statementCount) != count {
			t.Fatalf("statementCount = %d, want %d", res.statementCount, count)
		}
		if res.inlineStatements != nil {
			t.Fatal("expected inlineStatements to be dropped on large batch")
		}
		if len(res.validityRefs) != count {
			t.Fatalf("len(validityRefs) = %d, want %d", len(res.validityRefs), count)
		}

		runtime.GC()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		retainedDelta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
		t.Logf("statements=%d raw_bytes=%d groups=%d validity_table=%d retained_heap_delta_bytes=%d",
			count, len(rawSQL), len(res.groups), len(res.validityTable), retainedDelta)
		if !perfTestRaceEnabled && retainedDelta > 256<<20 {
			t.Fatalf("retained heap delta too large: %d bytes", retainedDelta)
		}
		runtime.KeepAlive(res)

		// Large batch with a late syntax error (>256 KiB) must fail with 2003 SQL_PARSE_FAILED during streaming.
		var badBuf bytes.Buffer
		for i := 1; i <= 2500; i++ {
			fmt.Fprintf(&badBuf, "UPDATE orders SET amount = %d WHERE id = %d AND amount >= 0 /* filler_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef */;\n", i, i)
		}
		badBuf.WriteString("SELECT FROM WHERE bad syntax;\n")
		if badBuf.Len() <= ephemeralSyncSyntaxCheckMaxBytes {
			t.Fatalf("expected > 256 KiB payload for late syntax error check, got %d bytes", badBuf.Len())
		}
		_, serr := analyzeRunStatementsStreaming(streamingAnalysisOptions{
			runID:           "run-late-bad",
			rawSQL:          badBuf.Bytes(),
			profile:         profile,
			mapper:          privacy.NewRunMapper(),
			engine:          engine,
			expectedCount:   0,
			validateSyntax:  true,
			inlineLimit:     ephemeralInlineStatementLimit,
			collectPostgres: false,
		})
		var de *domain.Error
		if !errors.As(serr, &de) || de.Code != domain.CodeSQLParseFailed {
			t.Fatalf("expected SQL_PARSE_FAILED (2003), got %v", serr)
		}
	})
}

func TestStatementSyntaxValidator_DialectAwareShapesAndUniqueCap(t *testing.T) {
	svc := &Services{}
	mysqlProfile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	pgProfile := fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}

	cases := []struct {
		name    string
		profile fingerprint.Profile
		prefix  string
		badTail string
	}{
		{
			name:    "pg_backslash_in_single_quote",
			profile: pgProfile,
			prefix:  `INSERT INTO t(a) VALUES ('\');` + "\n",
			badTail: `INSERT INTO t(a) VALUES ('\') BROKEN GARBAGE SYNTAX;` + "\n",
		},
		{
			name:    "mysql_escaped_double_quote",
			profile: mysqlProfile,
			prefix:  `INSERT INTO t(a) VALUES ("\"");` + "\n",
			badTail: `INSERT INTO t(a) VALUES ("\"") BROKEN GARBAGE SYNTAX;` + "\n",
		},
		{
			name:    "mysql_comment_with_odd_quote",
			profile: mysqlProfile,
			prefix:  `SELECT /* ' */ 1;` + "\n",
			badTail: `SELECT /* ' */ BROKEN GARBAGE SYNTAX;` + "\n",
		},
		{
			name:    "pg_comment_with_odd_quote",
			profile: pgProfile,
			prefix:  `SELECT /* ' */ 1;` + "\n",
			badTail: `SELECT /* ' */ BROKEN GARBAGE SYNTAX;` + "\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			for i := 0; i < syntaxCheckDirectLimit; i++ {
				b.WriteString(tc.prefix)
			}
			b.WriteString(tc.badTail)
			_, err := svc.countAndValidateStatements([]byte(b.String()), tc.profile)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeSQLParseFailed {
				t.Fatalf("expected 2003 SQL_PARSE_FAILED for %s, got %v", tc.name, err)
			}
		})
	}

	t.Run("initial_200_do_not_consume_500_unique_shape_budget", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < syntaxCheckDirectLimit; i++ {
			b.WriteString("SELECT 1;\n")
		}
		for i := 1; i <= 350; i++ {
			fmt.Fprintf(&b, "SELECT col_%d FROM t;\n", i)
		}
		b.WriteString("SELECT FROM WHERE broken_after_350;\n")
		_, err := svc.countAndValidateStatements([]byte(b.String()), mysqlProfile)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeSQLParseFailed {
			t.Fatalf("expected 2003 SQL_PARSE_FAILED after 350 post-limit shapes, got %v", err)
		}
	})
}

func TestEphemeralTakeReadyAndPutBackBudgetAccounting(t *testing.T) {
	st := NewEphemeralStore(time.Minute)
	st.maxActiveSQLBytes = 200
	st.maxTerminalRuns = 2
	svc := &Services{Ephemeral: st}

	future := time.Now().Add(time.Minute)
	queued := &EphemeralRun{
		ID: "q1", OwnerUserID: "u1", state: domain.RunStateQueued,
		SQLPlaintext: make([]byte, 80), CreatedAt: time.Now(), expiresAt: future,
	}
	if err := st.admit(queued, ephemeralMaxPerUser); err != nil {
		t.Fatalf("admit queued: %v", err)
	}
	if got := svc.EphemeralTake("q1", "u1"); got != nil {
		t.Fatal("EphemeralTake must reject non-ready queued run")
	}
	if st.Get("q1", "u1") == nil {
		t.Fatal("rejected EphemeralTake must leave queued run in store")
	}
	if st.activeSQLBytes != 80 {
		t.Fatalf("rejected EphemeralTake altered activeSQLBytes: got %d, want 80", st.activeSQLBytes)
	}

	// Simulate ready run with residual SQL bytes (e.g. test fixture) -> TakeReady releases and PutBack re-accounts.
	fin := time.Now()
	ready := &EphemeralRun{
		ID: "ready-1", OwnerUserID: "u1", state: domain.RunStateReady,
		SQLPlaintext: make([]byte, 50), CreatedAt: fin.Add(-time.Second), FinishedAt: &fin, expiresAt: future,
	}
	if err := st.admit(ready, ephemeralMaxPerUser); err != nil {
		t.Fatalf("admit ready: %v", err)
	}
	if st.activeSQLBytes != 130 {
		t.Fatalf("activeSQLBytes = %d, want 130", st.activeSQLBytes)
	}
	took := svc.EphemeralTake("ready-1", "u1")
	if took == nil {
		t.Fatal("expected EphemeralTake to return ready run")
	}
	if st.activeSQLBytes != 80 {
		t.Fatalf("activeSQLBytes after TakeReady = %d, want 80", st.activeSQLBytes)
	}

	// PutBack restores the ready run, re-reserves its 50 SQL bytes, and enforces maxTerminalRuns (=2).
	svc.EphemeralPutBack(took)
	if st.Get("ready-1", "u1") == nil {
		t.Fatal("expected ready-1 in store after PutBack")
	}
	if st.activeSQLBytes != 130 || took.sqlReservedBytes != 50 {
		t.Fatalf("expected activeSQLBytes=130 and reserved=50 after PutBack, got active=%d reserved=%d",
			st.activeSQLBytes, took.sqlReservedBytes)
	}

	f1 := &EphemeralRun{
		ID: "f1", OwnerUserID: "u2", state: domain.RunStateFailed,
		CreatedAt: fin.Add(-3 * time.Second), FinishedAt: ptrTime(fin.Add(-3 * time.Second)), expiresAt: future,
	}
	f2 := &EphemeralRun{
		ID: "f2", OwnerUserID: "u2", state: domain.RunStateFailed,
		CreatedAt: fin.Add(-2 * time.Second), FinishedAt: ptrTime(fin.Add(-2 * time.Second)), expiresAt: future,
	}
	st.put(f1)
	st.put(f2)
	// Ready-1 PutBack when already 2 terminal runs exist must trigger cap eviction (evicts oldest non-ready f1).
	readyAgain := svc.EphemeralTake("ready-1", "u1")
	if readyAgain == nil {
		t.Fatal("expected TakeReady to succeed")
	}
	svc.EphemeralPutBack(readyAgain)
	if st.Get("f1", "u2") != nil {
		t.Fatal("expected PutBack to enforce maxTerminalRuns by evicting oldest non-ready run")
	}
	if st.Get("ready-1", "u1") == nil || st.Get("f2", "u2") == nil {
		t.Fatal("expected newer ready-1 and f2 to survive")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
