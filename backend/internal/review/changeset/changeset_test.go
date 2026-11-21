package changeset

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

type fixedSigner struct{ key []byte }

func (f fixedSigner) KeyID() string { return "changeset-test-key" }

func (f fixedSigner) HMACSHA256(data []byte) ([]byte, error) {
	mac := hmac.New(sha256.New, f.key)
	mac.Write(data)
	return mac.Sum(nil), nil
}

func testEngine(t *testing.T) *fingerprint.Engine {
	t.Helper()
	e, err := fingerprint.NewEngine(fixedSigner{key: []byte("changeset-test-key-material-0123456789")}, fingerprint.Options{})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return e
}

func TestSplitBasic(t *testing.T) {
	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	input := "SELECT 1; UPDATE t SET a='x;y' WHERE id=1;; DELETE FROM t /* c; */ ;"
	s := NewSplitter(strings.NewReader(input), profile, 0)
	var got []string
	for {
		stmt, done, err := s.Next()
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if done {
			break
		}
		got = append(got, string(stmt.SQL))
	}
	want := []string{"SELECT 1", "UPDATE t SET a='x;y' WHERE id=1", "DELETE FROM t /* c; */"}
	if len(got) != len(want) {
		t.Fatalf("statements = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stmt[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Retaining Statement.SQL byte slices across Next() calls must not alias the internal sliding buffer when trailing newlines follow a semicolon.
	sRetain := NewSplitter(
		strings.NewReader("UPDATE e2e_rollback_orders SET status = 'PAID', amount = 1888 WHERE id = 101;\n"),
		profile,
		0,
	)
	var retained []Statement
	for {
		stmt, done, err := sRetain.Next()
		if err != nil {
			t.Fatalf("retain next: %v", err)
		}
		if done {
			break
		}
		retained = append(retained, stmt)
	}
	if len(retained) != 1 || string(retained[0].SQL) != "UPDATE e2e_rollback_orders SET status = 'PAID', amount = 1888 WHERE id = 101" {
		t.Fatalf("retained[0].SQL = %q", retained[0].SQL)
	}
}

func TestSplitStatementIndexAndOffsets(t *testing.T) {
	profile := fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}
	input := "SELECT 1;\nSELECT $a$ x $a$ WHERE b=$1"
	s := NewSplitter(strings.NewReader(input), profile, 0)
	stmt1, done, err := s.Next()
	if err != nil || done {
		t.Fatalf("first: %v %v", err, done)
	}
	if stmt1.Index != 1 || stmt1.Offset != 0 {
		t.Fatalf("stmt1 index=%d offset=%d", stmt1.Index, stmt1.Offset)
	}
	stmt2, done, err := s.Next()
	if err != nil || done {
		t.Fatalf("second: %v %v", err, done)
	}
	if stmt2.Index != 2 {
		t.Fatalf("stmt2 index=%d", stmt2.Index)
	}
	if !strings.HasPrefix(string(stmt2.SQL), "SELECT $a$ x $a$ WHERE b=$1") {
		t.Fatalf("stmt2 = %q", stmt2.SQL)
	}
	if stmt2.Offset != int64(len("SELECT 1;\n")) {
		t.Fatalf("stmt2 offset=%d", stmt2.Offset)
	}
	_, done, err = s.Next()
	if err != nil || !done {
		t.Fatalf("expected end: %v %v", err, done)
	}
}

func TestSplitFailClosedOnUnterminatedAtEOF(t *testing.T) {
	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	s := NewSplitter(strings.NewReader("SELECT 'unterminated"), profile, 0)
	if _, _, err := s.Next(); err == nil {
		t.Fatalf("unterminated string must fail closed")
	}
	s2 := NewSplitter(strings.NewReader("SELECT /* open"), profile, 0)
	if _, _, err := s2.Next(); err == nil {
		t.Fatalf("unterminated comment must fail closed")
	}
}

func TestSplitEnforcesStatementCap(t *testing.T) {
	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	big := "SELECT " + strings.Repeat("a,", 600000) + "a"
	s := NewSplitter(strings.NewReader(big), profile, 0)
	if _, _, err := s.Next(); err == nil {
		t.Fatalf("oversized statement must fail")
	}
}

func streamString(t *testing.T, sql string, limits Limits) (*Batch, error) {
	t.Helper()
	return Stream(context.Background(), strings.NewReader(sql),
		fingerprint.Profile{Engine: "mysql", Mode: "mysql"},
		testEngine(t), privacy.NewRunMapper(), limits)
}

func TestStreamGroupsBulkFixture(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 1000; i++ {
		if i == 543 {
			b.WriteString("UPDATE orders SET status = 'processed';\n")
			continue
		}
		fmt.Fprintf(&b, "UPDATE orders SET status = 'processed', updated_at = '2026-08-25 00:00:00' WHERE id = %d;\n", i)
	}
	batch, err := streamString(t, b.String(), DefaultLimits())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if batch.StatementCount != 1000 {
		t.Fatalf("statements = %d", batch.StatementCount)
	}
	if batch.UniqueFingerprintCount != 2 {
		t.Fatalf("unique fingerprints = %d, want 2", batch.UniqueFingerprintCount)
	}
	if batch.ComplexityWarning {
		t.Fatalf("no complexity warning expected below 200")
	}
	if len(batch.Exceptional) != 0 || batch.ExceptionalCount != 0 {
		t.Fatalf("unexpected exceptional statements: %d", batch.ExceptionalCount)
	}
	// readChunk is the number of bytes read per underlying read call.
	var anomaly *Group
	for _, g := range batch.Groups {
		if g.Count == 1 && g.FirstIndex == 543 {
			anomaly = g
		}
	}
	if anomaly == nil {
		t.Fatalf("anomaly statement group missing; groups=%d", len(batch.Groups))
	}
	// Splitter splits statements from an io.Reader in a streaming fashion.
	// Implemented as 'window + single lexing pass + token cursor': each fill lexes the window once, and statement cutting only advances byte/token cursors to avoid re-scanning per statement.
	for _, g := range batch.Groups {
		if len(g.Samples) > 3 {
			t.Fatalf("group samples = %d > 3", len(g.Samples))
		}
		if g.Count > 1 {
			if g.Samples[0].Index != g.FirstIndex || g.Samples[len(g.Samples)-1].Index != g.LastIndex {
				t.Fatalf("sample first/last policy violated")
			}
		}
	}
	// current window (including consumed prefix, compacted on fill)
	for _, g := range batch.Groups {
		for _, s := range g.Samples {
			if s.ByteLen == 0 {
				t.Fatalf("sample byte length must be recorded")
			}
		}
	}
}

func TestStreamLimitsBoundaries(t *testing.T) {
	if raceEnabled {
		t.Skip("32MiB/100k scale boundary skipped under race gate: unit gate runs in full; race detector targets concurrency bugs whereas this test validates single-goroutine streaming scale")
	}
	// absolute byte offset of buf[0]
	limits := DefaultLimits()
	fill := strings.Repeat("x", 500)
	stmt := "UPDATE t SET a='" + fill + "' WHERE id=%d;\n"
	build := func(exactBytes int) string {
		var b strings.Builder
		n := 0
		i := 0
		for n < exactBytes {
			line := fmt.Sprintf(stmt, i)
			if n+len(line) > exactBytes {
				b.WriteString(strings.Repeat(" ", exactBytes-n))
				break
			}
			b.WriteString(line)
			n += len(line)
			i++
		}
		return b.String()
	}
	ok := build(limits.MaxInputBytes)
	if len(ok) != limits.MaxInputBytes {
		t.Fatalf("builder produced %d bytes", len(ok))
	}
	if _, err := streamString(t, ok, limits); err != nil {
		t.Fatalf("exact 32MiB must pass: %v", err)
	}
	over := build(limits.MaxInputBytes) + " "
	if _, err := streamString(t, over, limits); err == nil {
		t.Fatalf("32MiB+1 must fail")
	} else {
		f, isF := err.(*fingerprint.Failure)
		if !isF || f.Code != fingerprint.ErrNameBatchTooLarge {
			t.Fatalf("unexpected error %v", err)
		}
	}

	// consumed bytes within buf (statement plus trailing semicolon)
	var b strings.Builder
	for i := 0; i < limits.MaxStatements; i++ {
		b.WriteString("SELECT 1;\n")
	}
	if _, err := streamString(t, b.String(), limits); err != nil {
		t.Fatalf("exact 100000 statements must pass: %v", err)
	}
	b.WriteString("SELECT 1;\n")
	if _, err := streamString(t, b.String(), limits); err == nil {
		t.Fatalf("100001 statements must fail")
	}

	// lexed tokens for the entire buf (rebuilt on fill)
	var c strings.Builder
	for i := 1; i <= limits.MaxUniqueFingerprints; i++ {
		c.WriteString("UPDATE t SET")
		for j := 1; j <= i; j++ {
			if j > 1 {
				c.WriteString(",")
			}
			fmt.Fprintf(&c, " c%d=%d", j, j)
		}
		c.WriteString(" WHERE id=1;\n")
	}
	warnBatch, err := streamString(t, c.String(), limits)
	if err != nil {
		t.Fatalf("1000 unique fingerprints must pass: %v", err)
	}
	if warnBatch.UniqueFingerprintCount != 1000 || !warnBatch.ComplexityWarning {
		t.Fatalf("complexity warning state wrong: %d %v", warnBatch.UniqueFingerprintCount, warnBatch.ComplexityWarning)
	}
	c.WriteString("UPDATE t SET c1=1 WHERE id=1 AND k=1;\n") // next statement ordinal (1-based)
	if _, err := streamString(t, c.String(), limits); err == nil {
		t.Fatalf("1001 unique fingerprints must fail")
	} else {
		f, _ := err.(*fingerprint.Failure)
		if f == nil || f.Code != fingerprint.ErrNameTooManyFprints {
			t.Fatalf("unexpected error %v", err)
		}
	}
}

func TestStreamComplexityWarningThreshold(t *testing.T) {
	limits := DefaultLimits()
	var b strings.Builder
	for i := 1; i <= limits.ComplexityWarning; i++ { // Statement represents a single statement and its position within the stream.
		b.WriteString("UPDATE t SET")
		for j := 1; j <= i; j++ {
			if j > 1 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, " c%d=%d", j, j)
		}
		b.WriteString(" WHERE id=1;\n")
	}
	batch, err := streamString(t, b.String(), limits)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if batch.ComplexityWarning {
		t.Fatalf("200 unique fingerprints must not warn")
	}
	b.WriteString("UPDATE t SET c1=1 WHERE id=1 AND k=1;\n") // single statement without trailing semicolon or leading/trailing whitespace
	batch, err = streamString(t, b.String(), limits)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !batch.ComplexityWarning {
		t.Fatalf("201 unique fingerprints must warn")
	}
}

func TestStreamFailClosedOnBadStatement(t *testing.T) {
	// absolute byte offset of the first byte of the statement
	_, err := streamString(t, "SELECT 1; SELECT 'unterminated", DefaultLimits())
	if err == nil {
		t.Fatalf("batch with bad statement must fail")
	}
}

func TestStreamCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("SELECT 1;\n")
	}
	_, err := Stream(ctx, strings.NewReader(b.String()),
		fingerprint.Profile{Engine: "mysql", Mode: "mysql"},
		testEngine(t), privacy.NewRunMapper(), DefaultLimits())
	if err == nil {
		t.Fatalf("cancelled context must fail")
	}
}

func TestStreamExceptionalBounded(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxExceptionalRefs = 5
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString("CHECKPOINT;\n") // NewSplitter constructs a statement splitter; maxStatementBytes<=0 uses the engine default.
	}
	b.WriteString("SELECT 1;\n")
	batch, err := streamString(t, b.String(), limits)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if batch.ExceptionalCount != 20 {
		t.Fatalf("exceptional count = %d", batch.ExceptionalCount)
	}
	if len(batch.Exceptional) != 5 {
		t.Fatalf("exceptional refs = %d, want bounded at 5", len(batch.Exceptional))
	}
}

// Next returns the next non-empty statement; returns done=true at end of stream.
func TestStreamExtremeSampleSlotUpdates(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		b.WriteString("UPDATE t SET a=1 WHERE id IN (1);\n")
	}
	// fill compacts the window, reads the next chunk, and rebuilds the lexed tokens.
	b.WriteString("UPDATE t SET a=1 WHERE id IN (1,2,3);\n")
	for i := 102; i <= 120; i++ {
		b.WriteString("UPDATE t SET a=1 WHERE id IN (1);\n")
	}
	batch, err := streamString(t, b.String(), DefaultLimits())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	// Unterminated statement in window exceeds the single-statement limit (no boundary found).
	if len(batch.Groups) != 1 {
		t.Fatalf("groups=%d want 1 (IN cardinality collapses into one canonical)", len(batch.Groups))
	}
	bulk := batch.Groups[0]
	if bulk.Count != 120 {
		t.Fatalf("bulk count=%d want 120", bulk.Count)
	}
	if len(bulk.Samples) != 3 {
		t.Fatalf("samples=%d want 3 (first+extreme+last)", len(bulk.Samples))
	}
	extreme := bulk.Samples[1]
	if extreme.Index != 101 {
		t.Fatalf("extreme sample index=%d want 101", extreme.Index)
	}
	if bulk.Samples[0].Index != bulk.FirstIndex || bulk.Samples[2].Index != bulk.LastIndex {
		t.Fatalf("first/last slot policy violated")
	}
	if bulk.MinParams == bulk.MaxParams {
		t.Fatalf("min/max must differ after extreme statement")
	}
}

type oneStmtPerReadReader struct {
	chunks [][]byte
	idx    int
}

func (r *oneStmtPerReadReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.idx])
	r.idx++
	return n, nil
}

func TestSplitterReusesReadBufferAndMatchesShortReads(t *testing.T) {
	const count = 1000
	var full bytes.Buffer
	chunks := make([][]byte, 0, count)
	for i := 1; i <= count; i++ {
		stmt := fmt.Sprintf("UPDATE orders SET status = 'PAID' WHERE id = %d;\n", i)
		full.WriteString(stmt)
		chunks = append(chunks, []byte(stmt))
	}
	raw := full.Bytes()
	prof := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}

	collect := func(r io.Reader) ([]string, []int64) {
		s := NewSplitter(r, prof, 0)
		sqls := make([]string, 0, count)
		offs := make([]int64, 0, count)
		for {
			st, done, err := s.Next()
			if err != nil {
				t.Fatalf("splitter Next failed: %v", err)
			}
			if done {
				break
			}
			sqls = append(sqls, string(st.SQL))
			offs = append(offs, st.Offset)
			if st.Index != len(sqls) {
				t.Fatalf("ordinal mismatch: got %d want %d", st.Index, len(sqls))
			}
		}
		return sqls, offs
	}

	chunkedSQLs, chunkedOffs := collect(bytes.NewReader(raw))
	shortSQLs, shortOffs := collect(&oneStmtPerReadReader{chunks: chunks})
	if len(chunkedSQLs) != count || len(shortSQLs) != count {
		t.Fatalf("counts mismatch: chunked=%d short=%d want %d", len(chunkedSQLs), len(shortSQLs), count)
	}
	for i := 0; i < count; i++ {
		if chunkedSQLs[i] != shortSQLs[i] || chunkedOffs[i] != shortOffs[i] {
			t.Fatalf("mismatch at %d: (%q,%d) vs (%q,%d)", i, chunkedSQLs[i], chunkedOffs[i], shortSQLs[i], shortOffs[i])
		}
	}

	if !raceEnabled {
		// Reusing Splitter.readBuf avoids allocating a fresh 64 KiB read slice on each of the 1,000 short fills
		// (~64 MiB previously). Compare total bytes allocated via Benchmark.
		br := testing.Benchmark(func(b *testing.B) {
			reader := oneStmtPerReadReader{chunks: chunks}
			for i := 0; i < b.N; i++ {
				reader.idx = 0
				s := NewSplitter(&reader, prof, 0)
				for {
					_, done, err := s.Next()
					if err != nil {
						b.Fatalf("splitter Next failed: %v", err)
					}
					if done {
						break
					}
				}
			}
		})
		allocBytes := br.AllocedBytesPerOp()
		// 1,000 short reads previously allocated >64 MiB just for chunk buffers; with reuse total is only a few MiB.
		if allocBytes > 8*1024*1024 {
			t.Fatalf("expected < 8 MiB allocated for 1000 short reads with readBuf reuse, got %d bytes", allocBytes)
		}
	}
}
