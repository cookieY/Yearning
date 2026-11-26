// Package pagecursor provides canonical keyset pagination cursor encoding and decoding.
// Cursors use fixed UTC microsecond timestamps ("2006-01-02T15:04:05.999999Z|<id>") for
// lossless PostgreSQL timestamptz ordering while accepting legacy RFC3339Nano timestamps on decode.
package pagecursor

import (
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// Format is the canonical UTC microsecond timestamp layout used in encoded keyset cursors.
const Format = "2006-01-02T15:04:05.999999Z"

// ErrInvalidCursor is returned whenever a raw pagination cursor is malformed, unparseable, or empty.
var ErrInvalidCursor = errors.New("pagecursor: invalid pagination cursor")

// Encode formats a (timestamp, id) pair into a canonical UTC microsecond keyset cursor.
func Encode(t time.Time, id string) string {
	return t.UTC().Format(Format) + "|" + id
}

// Decode parses a keyset pagination cursor in either canonical UTC microsecond format or legacy RFC3339Nano.
// Returns ErrInvalidCursor for any missing delimiter, invalid timestamp, empty/whitespace/oversized ID,
// or non-canonical trailing garbage.
func Decode(raw string) (time.Time, string, error) {
	if raw == "" || len(raw) > 256 {
		return time.Time{}, "", ErrInvalidCursor
	}
	sep := strings.LastIndex(raw, "|")
	if sep <= 0 || sep >= len(raw)-1 {
		return time.Time{}, "", ErrInvalidCursor
	}
	tsRaw := raw[:sep]
	id := raw[sep+1:]
	if len(id) > 128 || strings.Contains(id, "|") {
		return time.Time{}, "", ErrInvalidCursor
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return time.Time{}, "", ErrInvalidCursor
		}
	}
	ts, err := time.Parse(Format, tsRaw)
	if err != nil {
		ts, err = time.Parse(time.RFC3339Nano, tsRaw)
		if err != nil {
			return time.Time{}, "", ErrInvalidCursor
		}
	}
	return ts.UTC(), id, nil
}

// DecodeUUID parses a keyset pagination cursor and additionally requires the ID component to be a valid UUID.
func DecodeUUID(raw string) (time.Time, string, error) {
	ts, id, err := Decode(raw)
	if err != nil {
		return time.Time{}, "", err
	}
	if _, err := uuid.Parse(id); err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	return ts, id, nil
}
