// Package watermark formats server-issued audit watermarks for plaintext SQL and raw evidence reveals.
package watermark

import "time"

// SQLViewer returns the canonical dynamic reveal watermark (`Yearning SQL Viewer: <username> @ <UTC RFC3339>`).
// Frontend `formatWatermark` parses the embedded ISO-8601 UTC timestamp and projects it into `general.system_timezone`.
func SQLViewer(username string, at time.Time) string {
	return "Yearning SQL Viewer: " + username + " @ " + at.UTC().Format("2006-01-02T15:04:05Z")
}
