// identifiermapping_test.go NEW-AI-01 regression: pure-function merge core for idempotent resealing
// (storage shape round-trip and envelope-level tests live in package privacy; this file covers
// the idempotency / extension / conflict checks of mergeMappingPairs).
package application

import (
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func storedPairs() []privacy.MappingPair {
	return []privacy.MappingPair{
		{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
		{Kind: privacy.KindSchema, Original: "shop", Token: "schema_1"},
	}
}

func currentPairs() []privacy.MappingPair {
	return []privacy.MappingPair{
		{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
		{Kind: privacy.KindSchema, Original: "shop", Token: "schema_1"},
		{Kind: privacy.KindColumn, Original: "status", Token: "column_1"},
	}
}

func TestMergeMappingPairsIdempotent(t *testing.T) {
	merged, changed, err := mergeMappingPairs(storedPairs(), storedPairs())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("identical snapshots must be a no-op (changed=false)")
	}
	if len(merged) != 2 {
		t.Fatalf("merged len = %d", len(merged))
	}
}

func TestMergeMappingPairsExtendsWithNewTokens(t *testing.T) {
	merged, changed, err := mergeMappingPairs(storedPairs(), currentPairs())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("snapshot superset must be changed=true")
	}
	if len(merged) != 3 || merged[2].Original != "status" {
		t.Fatalf("merged must keep stored order and append new entries: %+v", merged)
	}
}

func TestMergeMappingPairsRejectsConflicts(t *testing.T) {
	// Same name with different token (token drift).
	drifted := []privacy.MappingPair{
		{Kind: privacy.KindTable, Original: "orders", Token: "table_2"},
		{Kind: privacy.KindSchema, Original: "shop", Token: "schema_1"},
	}
	if _, _, err := mergeMappingPairs(storedPairs(), drifted); err == nil {
		t.Fatal("token drift for stored identifier must fail closed")
	} else if strings.Contains(err.Error(), "orders") {
		t.Fatalf("error must not echo identifier value: %v", err)
	}
	// Stored entry missing from current snapshot (would permanently lose restoration capability).
	missing := []privacy.MappingPair{
		{Kind: privacy.KindSchema, Original: "shop", Token: "schema_1"},
	}
	if _, _, err := mergeMappingPairs(storedPairs(), missing); err == nil {
		t.Fatal("stored entries missing from current snapshot must fail closed")
	}
}
