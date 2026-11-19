package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestEffectiveToolConfigHashInvalidatesPreSnapshotPolicy(t *testing.T) {
	// A review with no knowledge entries must also be invalidated when the
	// immutable StartRun policy boundary is introduced.
	const beforeSnapshot = "7f1b38689d4e91bd7dabc996acb170b3453dd8369b47c8904db58997156c1297"
	if EffectiveToolConfigHash(nil) == beforeSnapshot {
		t.Fatal("review from before immutable policy freezing is still reusable")
	}
}

func TestEffectiveToolConfigHashIncludesPolicyRevision(t *testing.T) {
	if PolicyRevision == "" {
		t.Fatal("policy revision must not be empty")
	}

	legacyNone := sha256.Sum256([]byte("none"))
	got := EffectiveToolConfigHash(nil)
	if got == hex.EncodeToString(legacyNone[:]) {
		t.Fatal("no-tool policy hash must change when severity governance changes")
	}
	const want = "1ef7bcafa0f024203670385f0a5bce190e0955c97a00e41ffbb8435c031f8bf3"
	if got != want {
		t.Fatalf("no-tool effective policy hash = %s, want %s", got, want)
	}
}

func TestEffectiveToolConfigHashIsDeterministicAndOrderIndependent(t *testing.T) {
	a := EffectiveToolConfigHash([]string{"tool-b", "tool-a", "tool-a"})
	b := EffectiveToolConfigHash([]string{"tool-a", "tool-a", "tool-b"})
	if a != b {
		t.Fatalf("equivalent effective policy hashes differ: %s != %s", a, b)
	}
	const want = "8d34b92492dedaff2241c62c0fbc6c4424b2eb88b01dc01420a1d7e7b3c0c479"
	if a != want {
		t.Fatalf("effective tool-policy hash = %s, want %s", a, want)
	}
	if got := EffectiveToolConfigHash([]string{"tool-a"}); got == a {
		t.Fatal("tool set change must alter effective policy hash")
	}
	if len(a) != sha256.Size*2 {
		t.Fatalf("effective policy hash length = %d, want %d", len(a), sha256.Size*2)
	}
}

func TestEffectivePolicyInvalidatesPreCatalogPrivacyReview(t *testing.T) {
	const beforeCatalog = "a987f55bec0699d9233c58545eae429853d1feeca12720b4efabe94a3561e712"
	if EffectiveToolConfigHash(nil) == beforeCatalog {
		t.Fatal("pre-catalog-privacy review must be invalidated")
	}
}
