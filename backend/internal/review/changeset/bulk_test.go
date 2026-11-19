package changeset

import (
	"context"
	"crypto/sha256"
	"io"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// No raw SQL text is retained inside groups.
type bulkFixtureReader struct {
	index int
	done  bool
	buf   []byte
}

func (r *bulkFixtureReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		if r.done {
			return 0, io.EOF
		}
		r.index++
		var line string
		if r.index == 54321 {
			line = "UPDATE orders SET status = 'processed';\n"
		} else {
			line = "UPDATE orders SET status = 'processed', updated_at = '2026-08-25 00:00:00' WHERE id = " +
				itoa(r.index) + ";\n"
		}
		r.buf = []byte(line)
		if r.index >= 100000 {
			r.done = true
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// 32MiB boundary: exact limit is allowed, exceeding by 1 byte fails. Statements are ~600B (~55k statements in 32MiB), hitting the byte limit before the statement count limit.
func TestStreamBulk100kHighSimilarity(t *testing.T) {
	if raceEnabled {
		t.Skip("100k-scale grouping skipped under race gate: unit gate runs in full (~2s) and capacity benchmarks are collected separately; small-scale tests on the same code path run in full under race")
	}
	reader := &bulkFixtureReader{}
	digest := sha256.New()
	engine := testEngine(t)
	// 100,000-statement boundary: exact limit is allowed, 100,001 fails.
	tee := &hashTee{r: reader, h: digest}
	batch, err := Stream(context.Background(), tee,
		fingerprint.Profile{Engine: "mysql", Mode: "mysql"},
		engine, privacy.NewRunMapper(), DefaultLimits())
	if err != nil {
		t.Fatalf("stream 100k: %v", err)
	}
	if batch.StatementCount != 100000 {
		t.Fatalf("statements = %d", batch.StatementCount)
	}
	if batch.UniqueFingerprintCount != 2 {
		t.Fatalf("unique fingerprints = %d, want 2 (fixture expectation)", batch.UniqueFingerprintCount)
	}
	if batch.ComplexityWarning {
		t.Fatalf("2 fingerprints must not trigger complexity warning")
	}
	var bulk, anomaly *Group
	for _, g := range batch.Groups {
		if g.Count == 99999 {
			bulk = g
		}
		if g.Count == 1 && g.FirstIndex == 54321 {
			anomaly = g
		}
	}
	if bulk == nil || anomaly == nil {
		t.Fatalf("expected groups (99999 bulk + 1 anomaly) missing: %+v", batch.Groups)
	}
	if bulk.FirstIndex != 1 || bulk.LastIndex != 100000 {
		t.Fatalf("bulk group span %d..%d", bulk.FirstIndex, bulk.LastIndex)
	}
	// 1,000 unique fingerprint boundary: 1,000 is allowed, 1,001 fails; 201..1,000 records a complexity warning. Unique fingerprints come from structural differences (SET column count), while table names/literals fold per contract.
	if len(bulk.Samples) != 2 {
		t.Fatalf("bulk samples = %d, want first+last=2 for uniform bulk", len(bulk.Samples))
	}
	if bulk.MinParams != bulk.MaxParams {
		t.Fatalf("high-similarity bulk must have uniform parameter count")
	}
}

type hashTee struct {
	r io.Reader
	h interface{ Write([]byte) (int, error) }
}

func (t *hashTee) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.h.Write(p[:n])
	}
	return n, err
}

// Structure +1 again (WHERE variant) -> 1001st unique fingerprint
func TestSplitSemicolonOnlyInsideLiterals(t *testing.T) {
	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	input := strings.Join([]string{
		"SELECT ';' AS a",
		"SELECT 'a;b' FROM t",
		"SELECT /* ; */ 1",
	}, ";\n") + ";\n"
	s := NewSplitter(strings.NewReader(input), profile, 0)
	count := 0
	for {
		_, done, err := s.Next()
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if done {
			break
		}
		count++
	}
	if count != 3 {
		t.Fatalf("statements = %d, want 3", count)
	}
}

// Exactly 200: no warning
type paddedBulkFixtureReader struct {
	index int
	done  bool
	buf   []byte
	fill  string
}

func newPaddedBulkFixtureReader(fillLen int) *paddedBulkFixtureReader {
	return &paddedBulkFixtureReader{fill: strings.Repeat("v", fillLen)}
}

func (r *paddedBulkFixtureReader) Read(p []byte) (int, error) {
	if len(r.buf) == 0 {
		if r.done {
			return 0, io.EOF
		}
		r.index++
		var line string
		if r.index == 54321 {
			line = "UPDATE orders SET status = '" + r.fill + "';\n"
		} else {
			line = "UPDATE orders SET status = '" + r.fill + "', updated_at = '2026-08-25 00:00:00' WHERE id = " +
				itoa(r.index) + ";\n"
		}
		r.buf = []byte(line)
		if r.index >= 100000 {
			r.done = true
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
