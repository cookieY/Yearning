package database

import (
	"errors"
	"strings"
	"testing"
)

func TestQuoteIdentifierMySQL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "items", want: "`items`"},
		{in: "select", want: "`select`"}, // Keywords are valid; quoting guarantees safety
		{in: "a`b", wantErr: true},       // Quoting metacharacters are rejected outright
		{in: `a"b`, wantErr: true},
		{in: "a b", wantErr: true},
		{in: "a;b", wantErr: true},
		{in: "'; DROP TABLE t; --", wantErr: true},
		{in: "", wantErr: true},
		{in: strings.Repeat("a", 65), wantErr: true},
		{in: strings.Repeat("a", 64), wantErr: true}, // PG NAMEDATALEN-1=63; overlong identifiers must be rejected (preventing silent truncation)
		{in: "1abc", wantErr: true},
		{in: "t1", want: "`t1`"},
	}
	for _, c := range cases {
		got, err := quoteMySQLOpen(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("mysql quote %q: expected error, got %q", c.in, got)
			}
			if !errors.Is(err, ErrIdentifierInvalid) {
				t.Fatalf("mysql quote %q: want ErrIdentifierInvalid, got %v", c.in, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("mysql quote %q: want %q err=%v got %q", c.in, c.want, err, got)
		}
	}
}

func TestQuoteIdentifierPostgres(t *testing.T) {
	if got, err := quotePostgresOpen("items"); err != nil || got != `"items"` {
		t.Fatalf("pg quote: got %q err %v", got, err)
	}
	if _, err := quotePostgresOpen(`a"b`); !errors.Is(err, ErrIdentifierInvalid) {
		t.Fatalf("pg quote embedded quote must be rejected, got %v", err)
	}
}
