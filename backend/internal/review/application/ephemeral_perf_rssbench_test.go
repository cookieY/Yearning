//go:build integration && rssbench

package application

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
)

func TestEphemeralPerf32MiBRealRSS(t *testing.T) {
	if os.Getenv("YEARNING_RUN_32MIB_RSS_BENCH") != "1" {
		t.Skip("set YEARNING_RUN_32MIB_RSS_BENCH=1 to execute 32 MiB OS RSS measurement")
	}
	b := newB7Fixture(t)
	runner := NewAgentRunner(b.svc, b.fakeDial(cleanEvidenceDial))

	runtime.GC()
	debug.FreeOSMemory()
	time.Sleep(300 * time.Millisecond)

	var baseMS runtime.MemStats
	runtime.ReadMemStats(&baseMS)
	t.Logf("PERF_MARKER phase=baseline ts_ms=%d heap_alloc=%d heap_inuse=%d sys=%d",
		time.Now().UnixMilli(), baseMS.HeapAlloc, baseMS.HeapInuse, baseMS.Sys)

	for iter := 1; iter <= 3; iter++ {
		payload := buildPerfOrdersSQLBatch(100000)
		payloadBytes := len(payload)
		if payloadBytes < 31<<20 || payloadBytes > 32<<20 {
			t.Fatalf("expected 31..32 MiB payload, got %d bytes", payloadBytes)
		}

		enqStart := time.Now()
		runID, err := b.svc.StartEphemeralRun(context.Background(), b.owner, runner, EphemeralStartInput{
			FlowID:         b.flowID,
			SQL:            payload,
			IdempotencyKey: fmt.Sprintf("perf32m-%d-%s", iter, runKeySuffix()),
		})
		enqDur := time.Since(enqStart)
		// Drop caller reference immediately just like HandleStartEphemeralReview.
		payload = nil
		if err != nil {
			t.Fatalf("run %d StartEphemeralRun: %v", iter, err)
		}

		er := waitEphemeralTerminal(t, b.svc, runID, b.owner.UserID)
		totalDur := time.Since(enqStart)
		if er.State() != domain.RunStateReady {
			t.Fatalf("run %d state = %s, want ready (code=%s)", iter, er.State(), ephemeralRunCodeString(er))
		}
		if er.Run.StatementCount != 100000 {
			t.Fatalf("run %d StatementCount = %d, want 100000", iter, er.Run.StatementCount)
		}

		runtime.GC()
		debug.FreeOSMemory()
		time.Sleep(300 * time.Millisecond)

		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		t.Logf("PERF_MARKER phase=run run=%d start_ms=%d ts_ms=%d payload_bytes=%d enqueue_ms=%.2f total_ms=%.2f heap_alloc=%d heap_inuse=%d sys=%d groups=%d",
			iter, enqStart.UnixMilli(), time.Now().UnixMilli(), payloadBytes,
			float64(enqDur.Microseconds())/1000.0, float64(totalDur.Microseconds())/1000.0,
			ms.HeapAlloc, ms.HeapInuse, ms.Sys, len(er.Groups))
	}
}
