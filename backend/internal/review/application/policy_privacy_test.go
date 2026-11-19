package application

import "testing"

func TestPolicyIdentifierBoundaries(t *testing.T) {
	for _, tc := range []struct {
		text, name string
		fold, want bool
	}{
		{"orders_archive; identity", "orders", true, false},
		{"orders; id", "orders", true, true},
		{"OrderItems", "orderitems", true, true},
		{"OrderItems", "orderitems", false, false},
		{"\u8ba2\u5355.amount", "\u8ba2\u5355", true, true},
		{"audit&actor", "audit&actor", true, true},
	} {
		if got := policyIdentifierPresent(tc.text, tc.name, tc.fold); got != tc.want {
			t.Fatalf("boundary got %v want %v", got, tc.want)
		}
	}
}
