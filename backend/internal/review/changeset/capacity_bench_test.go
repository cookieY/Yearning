//go:build capacitybench

// 201st unique structure (WHERE variant)
package changeset

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

const (
	benchWarmupRuns       = 5
	benchMeasuredRuns     = 30
	benchSLOp95ms         = 30000
	benchMaxRssDeltaBytes = 536870912 // Any single statement fingerprint failure in the batch fails the entire batch (1.0 coverage gate).
)

type benchSample struct {
	Run int   `json:"run"`
	Ms  int64 `json:"ms"`
}

type benchWorkloadResult struct {
	Workload           string        `json:"workload"`
	StatementCount     int           `json:"statement_count"`
	UniqueFingerprints int           `json:"unique_fingerprints"`
	FixtureSHA256      string        `json:"fixture_sha256"`
	InputBytes         int64         `json:"input_bytes"`
	RawSamples         []benchSample `json:"raw_samples"`
	P50Ms              int64         `json:"p50_ms"`
	P95Ms              int64         `json:"p95_ms"`
	P99Ms              int64         `json:"p99_ms"`
	SLOPass            bool          `json:"slo_pass"`
}

type benchReport struct {
	ContractVersion    int                   `json:"contract_version"`
	Workloads          []benchWorkloadResult `json:"workloads"`
	GoArch             string                `json:"go_arch"`
	GoOs               string                `json:"go_os"`
	GoVersion          string                `json:"go_version"`
	WarmupRuns         int                   `json:"warmup_runs"`
	MeasuredRuns       int                   `json:"measured_runs"`
	MaxRssBytes        int64                 `json:"max_rss_bytes"`
	PeakHeapDeltaBytes int64                 `json:"peak_heap_delta_bytes"`
	SLOp95ms           int                   `json:"slo_p95_ms"`
	SLOPass            bool                  `json:"slo_pass"`
	MemoryPass         bool                  `json:"memory_pass"`
	Notes              []string              `json:"notes,omitempty"`
}

// All fallback quality
type benchSource struct {
	name string
	mk   func() io.Reader
}

func runWorkload(t *testing.T, src benchSource, engine *fingerprint.Engine, profile fingerprint.Profile) benchWorkloadResult {
	t.Helper()
	// TestStreamExtremeSampleSlotUpdates regression (Copilot rule 14 round 3): when the first N statements share the same shape and a different parameter count appears later, the extreme slot must be captured (not triggered only on statement #2).
	h := sha256.New()
	tee := &hashTee{r: src.mk(), h: h}
	counter := &countingReader{r: tee}
	batch, err := Stream(context.Background(), counter, profile, engine, privacy.NewRunMapper(), DefaultLimits())
	if err != nil {
		t.Fatalf("%s hash pass: %v", src.name, err)
	}
	result := benchWorkloadResult{
		Workload:           src.name,
		StatementCount:     batch.StatementCount,
		UniqueFingerprints: batch.UniqueFingerprintCount,
		FixtureSHA256:      fmt.Sprintf("%x", h.Sum(nil)),
		InputBytes:         counter.total,
	}
	samples := make([]benchSample, 0, benchWarmupRuns+benchMeasuredRuns)
	for i := 0; i < benchWarmupRuns+benchMeasuredRuns; i++ {
		start := time.Now()
		b, err := Stream(context.Background(), src.mk(), profile, engine, privacy.NewRunMapper(), DefaultLimits())
		if err != nil {
			t.Fatalf("%s run %d: %v", src.name, i, err)
		}
		if b.StatementCount != batch.StatementCount {
			t.Fatalf("%s run %d: statement count drift", src.name, i)
		}
		samples = append(samples, benchSample{Run: i + 1, Ms: time.Since(start).Milliseconds()})
	}
	measured := samples[benchWarmupRuns:]
	result.RawSamples = measured
	result.P50Ms = nearestRank(measured, 50)
	result.P95Ms = nearestRank(measured, 95)
	result.P99Ms = nearestRank(measured, 99)
	result.SLOPass = result.P95Ms <= benchSLOp95ms
	return result
}

// Statement #101: longer IN list — canonical folding keeps it in the same group while parameter count 3>2 becomes the new global max.
func TestFingerprintCapacityRun(t *testing.T) {
	engine := testEngine(t)
	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	workloads := []benchSource{
		{name: "bulk_update_high_similarity_fixture", mk: func() io.Reader { return &bulkFixtureReader{} }},
		{name: "bulk_update_high_similarity_32mib", mk: func() io.Reader { return newPaddedBulkFixtureReader(250) }},
	}

	var memBefore runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)
	peakHeap := memBefore.HeapAlloc

	report := benchReport{
		ContractVersion: 1,
		GoArch:          runtime.GOARCH,
		GoOs:            runtime.GOOS,
		GoVersion:       runtime.Version(),
		WarmupRuns:      benchWarmupRuns,
		MeasuredRuns:    benchMeasuredRuns,
		SLOp95ms:        benchSLOp95ms,
	}
	for _, src := range workloads {
		res := runWorkload(t, src, engine, profile)
		if !res.SLOPass {
			t.Fatalf("%s P95 %dms exceeds SLO %dms", res.Workload, res.P95Ms, benchSLOp95ms)
		}
		report.Workloads = append(report.Workloads, res)
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		if mem.HeapAlloc > peakHeap {
			peakHeap = mem.HeapAlloc
		}
	}
	report.SLOPass = true

	cpuPath := os.Getenv("FINGERPRINT_CAPACITY_CPU_PROFILE")
	if cpuPath != "" {
		if f, ferr := os.Create(cpuPath); ferr == nil {
			if ferr := pprof.StartCPUProfile(f); ferr == nil {
				_, _ = Stream(context.Background(), &bulkFixtureReader{}, profile, engine, privacy.NewRunMapper(), DefaultLimits())
				pprof.StopCPUProfile()
			}
			f.Close()
		}
	}
	heapPath := os.Getenv("FINGERPRINT_CAPACITY_HEAP_PROFILE")
	if heapPath != "" {
		if f, ferr := os.Create(heapPath); ferr == nil {
			runtime.GC()
			_ = pprof.WriteHeapProfile(f)
			f.Close()
		}
	}

	report.MaxRssBytes = maxRssBytes()
	report.PeakHeapDeltaBytes = int64(peakHeap) - int64(memBefore.HeapAlloc)
	report.MemoryPass = report.PeakHeapDeltaBytes <= benchMaxRssDeltaBytes
	if !report.MemoryPass {
		t.Fatalf("heap delta %d bytes exceeds invariant %d", report.PeakHeapDeltaBytes, benchMaxRssDeltaBytes)
	}

	out, _ := json.MarshalIndent(report, "", "  ")
	if path := os.Getenv("FINGERPRINT_CAPACITY_OUT"); path != "" {
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
	} else {
		fmt.Println(string(out))
	}
}

// IN cardinality folding: all statements share the same canonical fingerprint and group.
func nearestRank(samples []benchSample, pct int) int64 {
	vals := make([]int64, 0, len(samples))
	for _, s := range samples {
		vals = append(vals, s.Ms)
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	rank := (pct*len(vals) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return vals[rank-1]
}

// raceEnabled is false in non-race builds: scale tests run in full.
func maxRssBytes() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return ru.Maxrss // raceEnabled is true in race builds: heavyweight scale tests (100k/32MiB boundaries) are skipped under the race gate — the race detector targets concurrency bugs whereas these tests validate single-goroutine streaming scale; scale semantics are covered by full non-race unit gate execution and capacitybench evidence, while small-scale race tests cover the same code paths.
	}
	return ru.Maxrss * 1024 // Package changeset implements streaming SQL statement splitting, fingerprint grouping, and capacity limits for large batches.
	// Resource invariants (capacity-slo-contract resource_invariants):
	//   - Input is read as a stream, retaining only the current unterminated statement window at any time without copying the full SQL batch;
	//   - Each group retains <=3 representative samples and bounded exceptional statement references;
	//   - Per-statement goroutines are forbidden; processing runs serially;
	//   - Statement splitting boundaries share the exact same rules as the lexer: semicolons inside strings, comments, dollar-quotes, and hints are not statement boundaries; fail closed whenever a boundary cannot be proven.
	//
	// The Statement.SQL slice is valid only until the next Next() call (the underlying window buffer is reused).
}
