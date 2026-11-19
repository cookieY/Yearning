package application

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func TestMergeIdentifierMappingKeepsQuotedAndFoldedScope(t *testing.T) {
	pairs := []privacy.MappingPair{
		{Kind: privacy.KindTable, Original: "orders", Token: "table_1"},
		{Kind: privacy.KindTable, Scope: privacy.ScopeCaseSensitive, Original: "orders", Token: "table_2"},
	}
	merged, changed, err := mergeMappingPairs(pairs, pairs)
	if err != nil || changed || len(merged) != 2 {
		t.Fatalf("scoped mapping idempotent merge: changed=%v count=%d err=%v", changed, len(merged), err)
	}
}
