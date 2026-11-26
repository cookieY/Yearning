package pagecursor

import (
	"errors"
	"testing"
	"time"
)

func TestEncodeDecodeMicrosecondRoundTrip(t *testing.T) {
	orig := time.Date(2026, 11, 25, 14, 32, 19, 123456000, time.FixedZone("CST", 8*3600))
	const id = "0198da00-0000-7000-8000-000000000042"

	encoded := Encode(orig, id)
	wantEncoded := "2026-11-25T06:32:19.123456Z|" + id
	if encoded != wantEncoded {
		t.Fatalf("Encode = %q, want %q", encoded, wantEncoded)
	}

	gotTime, gotID, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if !gotTime.Equal(orig.UTC()) || gotTime.Location() != time.UTC {
		t.Fatalf("Decode time = %v, want %v", gotTime, orig.UTC())
	}
	if gotID != id {
		t.Fatalf("Decode id = %q, want %q", gotID, id)
	}

	gotUUIDTime, gotUUID, err := DecodeUUID(encoded)
	if err != nil || !gotUUIDTime.Equal(orig.UTC()) || gotUUID != id {
		t.Fatalf("DecodeUUID = (%v, %q, %v), want (%v, %q, nil)", gotUUIDTime, gotUUID, err, orig.UTC(), id)
	}
}

func TestDecodeLegacyRFC3339NanoCompatibility(t *testing.T) {
	cases := []struct {
		raw    string
		wantTS time.Time
		wantID string
	}{
		{
			raw:    "2026-11-25T06:32:19.123456789Z|0198da00-0000-7000-8000-000000000001",
			wantTS: time.Date(2026, 11, 25, 6, 32, 19, 123456789, time.UTC),
			wantID: "0198da00-0000-7000-8000-000000000001",
		},
		{
			raw:    "2026-11-25T14:32:19+08:00|item-42",
			wantTS: time.Date(2026, 11, 25, 6, 32, 19, 0, time.UTC),
			wantID: "item-42",
		},
		{
			raw:    "2026-11-25T06:32:19Z|item-99",
			wantTS: time.Date(2026, 11, 25, 6, 32, 19, 0, time.UTC),
			wantID: "item-99",
		},
	}
	for _, tc := range cases {
		gotTS, gotID, err := Decode(tc.raw)
		if err != nil {
			t.Fatalf("Decode(%q) unexpected error: %v", tc.raw, err)
		}
		if !gotTS.Equal(tc.wantTS) || gotID != tc.wantID {
			t.Fatalf("Decode(%q) = (%v, %q), want (%v, %q)", tc.raw, gotTS, gotID, tc.wantTS, tc.wantID)
		}
	}
}

func TestDecodeRejectsCorruptCursors(t *testing.T) {
	bad := []string{
		"",
		"no-delimiter",
		"|only-id",
		"2026-11-25T06:32:19Z|",
		"not-a-timestamp|0198da00-0000-7000-8000-000000000001",
		"2026-11-25 06:32:19|0198da00-0000-7000-8000-000000000001",
		"2026-11-25T06:32:19Z|id with spaces",
		"2026-11-25T06:32:19Z|id\nnewline",
		"2026-11-25T06:32:19Z|a|b",
	}
	for _, raw := range bad {
		if _, _, err := Decode(raw); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("Decode(%q) error = %v, want ErrInvalidCursor", raw, err)
		}
	}
	if _, _, err := DecodeUUID("2026-11-25T06:32:19Z|not-a-uuid"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("DecodeUUID(non-uuid) error = %v, want ErrInvalidCursor", err)
	}
}
