package pagination

import "testing"

func TestClampLimit(t *testing.T) {
	cases := []struct {
		v, def, max, want int
	}{
		{0, 50, 200, 50},
		{-10, 50, 200, 50},
		{1, 50, 200, 1},
		{25, 50, 200, 25},
		{200, 50, 200, 200},
		{201, 50, 200, 200},
		{500, 20, 100, 100},
		{0, 0, 200, 50},
		{0, 300, 100, 100},
		{75, 50, 0, 75},
	}
	for _, tc := range cases {
		if got := ClampLimit(tc.v, tc.def, tc.max); got != tc.want {
			t.Fatalf("ClampLimit(%d, %d, %d) = %d, want %d", tc.v, tc.def, tc.max, got, tc.want)
		}
	}
}
