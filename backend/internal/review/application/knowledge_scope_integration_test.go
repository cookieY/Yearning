//go:build integration

package application

import (
	"context"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func TestKnowledgeScopeProviderUsesFrozenScopeAndQuotedIdentity(t *testing.T) {
	// Use table name scope_orders to avoid colliding with the fixture's built-in (shop, orders) guard entry —
	// sharing the same key between the fixture entry and the tested entry would misclassify a fixture leak as an expected injection.
	for _, tc := range []struct{ name, sql, table string }{
		{"unqualified default", "SELECT id FROM scope_orders;", "scope_orders"},
		{"explicit physical", "SELECT id FROM shop.scope_orders;", "scope_orders"},
		{"quoted case", "SELECT id FROM shop.`Scope_Orders`;", "Scope_Orders"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newB7Fixture(t)
			var ds string
			if err := b.db.Raw(`select datasource_id::text from flow_stages where flow_id = ?`, b.flowID).Scan(&ds).Error; err != nil {
				t.Fatal(err)
			}
			matched := b.insertEntry(t, "physical-guard", "table", ds, "shop", tc.table, []string{"critical"}, "kb.physical-guard")
			wrongSchema := b.insertEntry(t, "wrong-schema-guard", "table", ds, "audit", tc.table, []string{"critical"}, "kb.wrong-schema")
			wrongCase := ""
			if tc.table == "Scope_Orders" {
				wrongCase = b.insertEntry(t, "wrong-case-guard", "table", ds, "shop", "scope_orders", []string{"critical"}, "kb.wrong-case")
			}
			saw := false
			deps := b.fakeDial(func(req provider.Request) ([]byte, int, error) {
				if strings.Contains(req.SystemPrompt, `id="`+matched+`"`) {
					saw = true
				}
				for _, excluded := range []string{b.entryTable, wrongSchema, wrongCase} {
					if excluded != "" && strings.Contains(req.SystemPrompt, `id="`+excluded+`"`) {
						t.Fatal("different physical identity injected into actual Provider request")
					}
				}
				if len(req.NormalizedEvidence) == 0 {
					requests, err := scopedMetadataToolRequests(req, repeatedProviderToolID)
					if err != nil {
						t.Fatal(err)
					}
					return completeWithTools(req, requests)
				}
				return completeWithTools(req, nil)
			})
			runID := b.runMetadataContextReview(t, deps, tc.sql)
			if !saw {
				t.Fatal("frozen physical scope rule was not injected")
			}
			run, err := b.svc.GetRunOwned(context.Background(), b.owner, runID)
			if err != nil || run.State != "ready" {
				t.Fatalf("scoped valid SQL = %+v, %v", run, err)
			}
		})
	}
}
