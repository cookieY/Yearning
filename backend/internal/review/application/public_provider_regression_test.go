package application

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

var updatePublicProviderBaseline = flag.Bool(
	"update-public-baseline",
	false,
	"regenerate public provider regression baseline fixtures",
)

type publicProviderRegressionCase struct {
	name            string
	sql             string
	forbiddenTokens []string
}

func publicProviderRegressionCases() []publicProviderRegressionCase {
	var bulkInsert strings.Builder
	for i := 1; i <= 49; i++ {
		fmt.Fprintf(
			&bulkInsert,
			"INSERT INTO shop.order_items (order_id, sku, qty) VALUES (%d, 'sku_%d', %d), (%d, 'sku_%d_b', %d);\n",
			i, i, i%5+1, i+1000, i, (i+1)%5+1,
		)
	}
	bulkInsert.WriteString(
		"INSERT INTO shop.order_items (order_id, sku, qty) VALUES (50, 'sku_50', 1), (1050, 'sku_50_b', 2), (2050, 'sku_50_c', 3);\n",
	)

	return []publicProviderRegressionCase{
		{
			name:            "01_single_dml_update",
			sql:             "UPDATE shop.orders SET status = 'paid', amount = 199 WHERE id = 1001;\n",
			forbiddenTokens: []string{"shop", "orders", "paid", "1001"},
		},
		{
			name:            "02_bulk_homomorphic_insert",
			sql:             bulkInsert.String(),
			forbiddenTokens: []string{"shop", "order_items", "sku_1", "sku_50_c"},
		},
		{
			name: "03_ddl_multi_group",
			sql: strings.Join([]string{
				"CREATE TABLE shop.shipments (",
				"  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,",
				"  order_id BIGINT UNSIGNED NOT NULL,",
				"  carrier VARCHAR(32) NOT NULL DEFAULT 'standard',",
				"  PRIMARY KEY (id),",
				"  KEY idx_shipments_order (order_id)",
				") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='shipment records';",
				"ALTER TABLE shop.orders ADD COLUMN tracking_no VARCHAR(64) NULL;",
				"DROP TABLE IF EXISTS shop.tmp_shipments;",
				"",
			}, "\n"),
			forbiddenTokens: []string{"shop", "shipments", "shipment records", "tracking_no", "tmp_shipments"},
		},
		{
			name:            "04_rename_table_cross_db",
			sql:             "RENAME TABLE shop.orders_old TO archive.orders_old;\n",
			forbiddenTokens: []string{"shop", "orders_old", "archive"},
		},
		{
			name:            "05_create_view_no_column_list",
			sql:             "CREATE OR REPLACE VIEW shop.v_active_orders AS SELECT * FROM shop.orders WHERE status = 'active';\n",
			forbiddenTokens: []string{"shop", "v_active_orders", "orders", "status", "active"},
		},
		{
			name:            "06_ctas",
			sql:             "CREATE TABLE shop.orders_bak AS SELECT * FROM shop.orders WHERE status = 'paid';\n",
			forbiddenTokens: []string{"shop", "orders_bak", "orders", "status", "paid"},
		},
	}
}

func capturePublicProviderHTTPBody(t *testing.T, rawSQL string) []byte {
	t.Helper()

	const runID = "00000000-0000-4000-8000-000000000001"
	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	engine := newPerfTestEngine(t)
	mapper := privacy.NewRunMapper()

	analysis, err := analyzeRunStatementsStreaming(streamingAnalysisOptions{
		runID:          runID,
		rawSQL:         []byte(rawSQL),
		profile:        profile,
		mapper:         mapper,
		engine:         engine,
		validateSyntax: true,
		inlineLimit:    ephemeralInlineStatementLimit,
	})
	if err != nil {
		t.Fatalf("analyzeRunStatementsStreaming failed: %v", err)
	}

	er := &EphemeralRun{
		ID:            runID,
		Profile:       profile,
		Mapper:        mapper,
		Groups:        make([]persistence.SQLFingerprintGroupModel, 0, len(analysis.groups)),
		groupMeta:     make(map[string]ephemeralGroupMeta, len(analysis.groups)),
		repSQLByGroup: make(map[string]string, len(analysis.groups)),
	}

	for i := range analysis.groups {
		g := analysis.groups[i]
		typed, merr := buildGroupTypedShape(g)
		if merr != nil {
			t.Fatalf("buildGroupTypedShape failed: %v", merr)
		}
		gid := fmt.Sprintf("10000000-0000-4000-8000-%012d", i+1)
		er.Groups = append(er.Groups, persistence.SQLFingerprintGroupModel{
			ID:                       gid,
			ReviewRunID:              runID,
			CanonicalFingerprintHMAC: g.canon,
			DialectFingerprintHMAC:   g.dialect,
			AlgorithmVersion:         fingerprint.AlgorithmVersion,
			FingerprintKeyID:         "perf-test-key",
			TypedShape:               typed,
			InValueCountMax:          int32(g.inMax),
			ValuesRowCountMax:        int32(g.valMax),
			StatementCount:           int32(g.count),
		})
		er.groupMeta[gid] = ephemeralGroupMeta{
			RepKind:      g.repKind,
			RepOperation: g.repOperation,
			SizeMax:      g.sizeMax,
			RepOrdinal:   g.representativeOrdinal,
		}
		er.repSQLByGroup[gid] = g.repSQL
	}

	stageGroups := ephemeralStageGroups(er, "ds-baseline")
	binding := resolveMySQLStageBinding(
		frozenFlowStageRow{
			Position:          1,
			CompatibilityMode: "mysql",
			DatabaseName:      ptr.To("shop"),
		},
		"",
		stageGroups,
		mapper,
	)
	boundGroups, _, _, _ := bindGroupMetadataWithReason(stageGroups, binding, mapper)
	legend := buildTokenLegend(mapper, binding, nil)
	legend = metadataTokenLegend(legend, boundGroups)

	req := agent.BuildRequest(runID, agent.ContextInput{
		EngineProfile: "mysql/mysql",
		OutputLocale:  "en-US",
		RuleSnapshot:  []byte("baseline-rule-snapshot-v1"),
		Groups:        boundGroups,
		Evidence:      []provider.NormalizedEvidenceRef{},
		TokenLegend:   legend,
	}, tool.NewRegistry())

	var capturedBody []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, rerr := io.ReadAll(r.Body)
		if rerr != nil {
			t.Fatalf("read request body: %v", rerr)
		}
		capturedBody = raw
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"schema_version\":1,\"review_run_id\":\"00000000-0000-4000-8000-000000000001\",\"outcome\":\"complete\",\"summary\":\"ok\",\"findings\":[],\"tool_requests\":[]}"}}]}`)
	}))
	defer srv.Close()

	adapter, err := provider.NewOpenAIAdapter(provider.Endpoint{
		ProviderID:          "prov-public-baseline",
		Name:                "public-openai",
		ProviderKind:        "openai_compatible",
		BaseURL:             srv.URL,
		ModelName:           "gpt-4o-mini",
		PrivacyContractHash: provider.PrivacyContractHashDefault,
		OutputSchemaHash:    provider.OutputSchemaHash,
		APIKey:              "test-key",
		Configuration:       map[string]any{provider.ConfigKeyOutputMode: provider.OutputModeJSONSchema},
	}, &provider.HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatalf("NewOpenAIAdapter failed: %v", err)
	}
	if _, err := adapter.ReviewStructured(context.Background(), req); err != nil {
		t.Fatalf("ReviewStructured failed: %v", err)
	}
	if len(capturedBody) == 0 {
		t.Fatal("captured public provider HTTP body is empty")
	}
	return capturedBody
}

func TestPublicProviderZeroImpactRegression(t *testing.T) {
	t.Parallel()

	baselineDir := filepath.Join("testdata", "public_provider_baseline")
	for _, tc := range publicProviderRegressionCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := capturePublicProviderHTTPBody(t, tc.sql)

			var envelope map[string]any
			if err := json.Unmarshal(got, &envelope); err != nil {
				t.Fatalf("captured HTTP body is invalid JSON: %v", err)
			}
			msgs, ok := envelope["messages"].([]any)
			if !ok || len(msgs) != 2 {
				t.Fatalf("unexpected messages count: %T len=%d", envelope["messages"], len(msgs))
			}
			userMsg, _ := msgs[1].(map[string]any)
			userContent, _ := userMsg["content"].(string)
			_, payloadJSON, cutOK := strings.Cut(userContent, "\n")
			if !cutOK {
				t.Fatalf("missing user payload JSON separator in %q", userContent)
			}
			var wire map[string]any
			if err := json.Unmarshal([]byte(payloadJSON), &wire); err != nil {
				t.Fatalf("invalid user payload JSON: %v", err)
			}
			tools, ok := wire["allowed_tool_descriptors"].([]any)
			if !ok || len(tools) == 0 {
				t.Fatalf("public provider request must include non-empty allowed_tool_descriptors, got %#v", wire["allowed_tool_descriptors"])
			}

			bodyText := string(got)
			for _, tok := range tc.forbiddenTokens {
				if strings.Contains(bodyText, tok) {
					t.Fatalf("public provider request leaked raw token %q", tok)
				}
			}

			baselinePath := filepath.Join(baselineDir, tc.name+".http_request.json")
			if *updatePublicProviderBaseline {
				if err := os.MkdirAll(baselineDir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", baselineDir, err)
				}
				if err := os.WriteFile(baselinePath, got, 0o644); err != nil {
					t.Fatalf("write baseline %s: %v", baselinePath, err)
				}
				return
			}

			want, err := os.ReadFile(baselinePath)
			if err != nil {
				t.Fatalf("read baseline %s: %v (run with -update-public-baseline to create)", baselinePath, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("public provider HTTP request drifted for %s (got %d bytes, want %d bytes)", tc.name, len(got), len(want))
			}
		})
	}
}
