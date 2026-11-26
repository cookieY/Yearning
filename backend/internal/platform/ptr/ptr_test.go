package ptr

import "testing"

func TestTo(t *testing.T) {
	empty := To("")
	if empty == nil || *empty != "" {
		t.Fatalf("To(\"\") = %v, want non-nil pointer to empty string", empty)
	}
	s := To("hello")
	if s == nil || *s != "hello" {
		t.Fatalf("To(\"hello\") = %v", s)
	}
	n := To(0)
	if n == nil || *n != 0 {
		t.Fatalf("To(0) = %v, want non-nil pointer to 0", n)
	}
}

func TestNonZero(t *testing.T) {
	if got := NonZero(""); got != nil {
		t.Fatalf("NonZero(\"\") = %v, want nil", got)
	}
	if got := NonZero(0); got != nil {
		t.Fatalf("NonZero(0) = %v, want nil", got)
	}
	if got := NonZero(false); got != nil {
		t.Fatalf("NonZero(false) = %v, want nil", got)
	}
	s := NonZero("abc")
	if s == nil || *s != "abc" {
		t.Fatalf("NonZero(\"abc\") = %v", s)
	}
	n := NonZero(42)
	if n == nil || *n != 42 {
		t.Fatalf("NonZero(42) = %v", n)
	}
}

func TestDeref(t *testing.T) {
	if got := Deref[string](nil); got != "" {
		t.Fatalf("Deref[string](nil) = %q, want empty", got)
	}
	if got := Deref[int](nil); got != 0 {
		t.Fatalf("Deref[int](nil) = %d, want 0", got)
	}
	if got := Deref(To("ok")); got != "ok" {
		t.Fatalf("Deref(To(\"ok\")) = %q, want ok", got)
	}
}
