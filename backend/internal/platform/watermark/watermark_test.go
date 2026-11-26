package watermark

import (
	"testing"
	"time"
)

func TestSQLViewerSnapshot(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	at := time.Date(2026, 11, 25, 22, 30, 45, 987654321, loc)
	got := SQLViewer("henry", at)
	const want = "Yearning SQL Viewer: henry @ 2026-11-25T14:30:45Z"
	if got != want {
		t.Fatalf("SQLViewer = %q, want %q", got, want)
	}
}
