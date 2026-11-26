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
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

var updateSANWireGolden = flag.Bool(
	"update",
	false,
	"regenerate SAN wire contract golden fixtures",
)

const (
	sanGoldenRunID           = "00000000-0000-4000-8000-000000000001"
	sanGoldenDefaultDatabase = "shop"
	sanGoldenEngineProfile   = "mysql/mysql"
	sanGoldenGenerateCommand = "cd backend && go test -tags nomsgpack ./internal/review/application -run TestSANWireGolden -update"
)

type sanGoldenCase struct {
	name            string
	description     string
	sql             string
	withPrehydrated bool
	assertExtra     func(t *testing.T, wire sanWireGoldenPayload, groups []provider.AnonymizedGroup)
}

type sanWireGoldenMeta struct {
	ContractVersion string `json:"contract_version"`
	ContractDigest  string `json:"contract_digest"`
	CaseName        string `json:"case_name"`
	Description     string `json:"description"`
	DefaultDatabase string `json:"default_database"`
	EngineProfile   string `json:"engine_profile"`
	GenerateCommand string `json:"generate_command"`
}

type sanWireGoldenPayload struct {
	RequestSchemaVersion      int                              `json:"request_schema_version"`
	ReviewRunID               string                           `json:"review_run_id"`
	EngineProfile             string                           `json:"engine_profile"`
	AnonymizedStatementGroups []provider.AnonymizedGroup       `json:"anonymized_statement_groups"`
	NormalizedEvidence        []provider.NormalizedEvidenceRef `json:"normalized_evidence"`
	AllowedToolDescriptors    []provider.ToolDescriptor        `json:"allowed_tool_descriptors"`
}

func sanGoldenDir() string {
	return filepath.Join("..", "provider", "testdata", "san_wire_contract", "v2")
}

func sanGoldenCases() []sanGoldenCase {
	var bulkInsert strings.Builder
	for i := 1; i <= 4999; i++ {
		fmt.Fprintf(
			&bulkInsert,
			"INSERT INTO shop.order_items (order_id, sku, qty) VALUES (%d, 'sku_%d', %d), (%d, 'sku_%d_b', %d);\n",
			i, i, i%5+1, i+10000, i, (i+1)%5+1,
		)
	}
	bulkInsert.WriteString(
		"INSERT INTO shop.order_items (order_id, sku, qty) VALUES (5000, 'sku_5000', 1), (15000, 'sku_5000_b', 2), (25000, 'sku_5000_c', 3);\n",
	)

	var in3, in12, in25 strings.Builder
	writeIN := func(b *strings.Builder, n int) {
		b.WriteString("UPDATE shop.orders SET status = 'archived' WHERE id IN (")
		for i := 1; i <= n; i++ {
			if i > 1 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "%d", i)
		}
		b.WriteString(");\n")
	}
	writeIN(&in3, 3)
	writeIN(&in12, 12)
	writeIN(&in25, 25)

	oversizedLiteral := strings.Repeat("x", 300)

	return []sanGoldenCase{
		{
			name:        "01_update_where",
			description: "Single UPDATE statement with explicit WHERE clause",
			sql:         "UPDATE shop.orders SET status = 'paid', amount = 199 WHERE id = 1001;\n",
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				if len(wire.AnonymizedStatementGroups) != 1 {
					t.Fatalf("groups len = %d, want 1", len(wire.AnonymizedStatementGroups))
				}
				g := wire.AnonymizedStatementGroups[0]
				if g.RepresentativeShape != "kind=dml op=update where=true" {
					t.Fatalf("unexpected representative_shape: %q", g.RepresentativeShape)
				}
			},
		},
		{
			name:        "02_delete_no_where",
			description: "Single unbounded DELETE statement without WHERE clause",
			sql:         "DELETE FROM shop.temp_events;\n",
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				g := wire.AnonymizedStatementGroups[0]
				if g.RepresentativeShape != "kind=dml op=delete where=false" {
					t.Fatalf("unexpected representative_shape: %q", g.RepresentativeShape)
				}
				if g.Statistics["unbounded_mutation"] != true {
					t.Fatalf("expected unbounded_mutation=true, got %#v", g.Statistics["unbounded_mutation"])
				}
			},
		},
		{
			name:        "03_bulk_insert_5000",
			description: "5000 homomorphic multi-tuple INSERT statements folded into 1 group",
			sql:         bulkInsert.String(),
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				if len(wire.AnonymizedStatementGroups) != 1 {
					t.Fatalf("groups len = %d, want 1", len(wire.AnonymizedStatementGroups))
				}
				g := wire.AnonymizedStatementGroups[0]
				if g.StatementCount != 5000 {
					t.Fatalf("statement_count = %d, want 5000", g.StatementCount)
				}
				if g.Statistics["values_row_count_max"] != float64(3) {
					t.Fatalf("values_row_count_max = %#v, want 3", g.Statistics["values_row_count_max"])
				}
				if len(g.StatementOrdinalRanges) != 1 || g.StatementOrdinalRanges[0] != (provider.StatementOrdinalRange{Start: 1, End: 5000}) {
					t.Fatalf("statement_ordinal_ranges = %+v, want [{1 5000}]", g.StatementOrdinalRanges)
				}
			},
		},
		{
			name:        "04_update_large_in_list",
			description: "Three isomorphic UPDATE IN (...) statements verifying MAX in_value_count_max merge",
			sql:         in3.String() + in12.String() + in25.String(),
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				if len(wire.AnonymizedStatementGroups) != 1 {
					t.Fatalf("groups len = %d, want 1", len(wire.AnonymizedStatementGroups))
				}
				g := wire.AnonymizedStatementGroups[0]
				if g.StatementCount != 3 || g.Statistics["in_value_count_max"] != float64(25) {
					t.Fatalf("count=%d in_value_count_max=%#v, want 3 / 25", g.StatementCount, g.Statistics["in_value_count_max"])
				}
			},
		},
		{
			name:        "05_replace_into",
			description: "Single REPLACE INTO statement",
			sql:         "REPLACE INTO shop.inventory (sku, stock) VALUES ('sku_100', 50);\n",
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				g := wire.AnonymizedStatementGroups[0]
				if g.RepresentativeShape != "kind=dml op=replace where=true" {
					t.Fatalf("unexpected representative_shape: %q", g.RepresentativeShape)
				}
			},
		},
		{
			name:        "06_dml_interleaved_groups",
			description: "Interleaved DML groups preserving exact non-contiguous statement_ordinal_ranges",
			sql: strings.Join([]string{
				"UPDATE shop.orders SET status = 'paid' WHERE id = 1;",
				"INSERT INTO shop.audit_events (order_id, event_type) VALUES (1, 'paid');",
				"UPDATE shop.orders SET status = 'shipped' WHERE id = 2;",
				"",
			}, "\n"),
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				if len(wire.AnonymizedStatementGroups) != 2 {
					t.Fatalf("groups len = %d, want 2", len(wire.AnonymizedStatementGroups))
				}
				g1 := wire.AnonymizedStatementGroups[0]
				g2 := wire.AnonymizedStatementGroups[1]
				wantG1 := []provider.StatementOrdinalRange{{Start: 1, End: 1}, {Start: 3, End: 3}}
				wantG2 := []provider.StatementOrdinalRange{{Start: 2, End: 2}}
				if fmt.Sprint(g1.StatementOrdinalRanges) != fmt.Sprint(wantG1) || fmt.Sprint(g2.StatementOrdinalRanges) != fmt.Sprint(wantG2) {
					t.Fatalf("interleaved ranges mismatch: g1=%+v g2=%+v", g1.StatementOrdinalRanges, g2.StatementOrdinalRanges)
				}
			},
		},
		{
			name:        "07_create_table_full",
			description: "CREATE TABLE with AUTO_INCREMENT, PRIMARY KEY, indexes, charset, collation, and comments",
			sql: strings.Join([]string{
				"CREATE TABLE shop.payments (",
				"  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT 'primary id',",
				"  order_id BIGINT UNSIGNED NOT NULL COMMENT 'order reference',",
				"  currency CHAR(3) NOT NULL DEFAULT 'USD' COMMENT 'ISO currency',",
				"  reference_code VARCHAR(128) NOT NULL DEFAULT '' COMMENT 'external reference',",
				"  PRIMARY KEY (id),",
				"  UNIQUE KEY uniq_payments_order (order_id),",
				"  KEY idx_payments_order_currency (order_id, currency)",
				") ENGINE=InnoDB AUTO_INCREMENT=1 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='payment ledger';",
				"",
			}, "\n"),
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				g := wire.AnonymizedStatementGroups[0]
				if g.Statistics["ddl_has_auto_increment"] != true || g.Statistics["ddl_has_primary_key"] != true ||
					g.Statistics["ddl_charset"] != "utf8mb4" || g.Statistics["ddl_collation"] != "utf8mb4_0900_ai_ci" {
					t.Fatalf("unexpected create table stats: %+v", g.Statistics)
				}
			},
		},
		{
			name:        "08_create_table_unknown_charset",
			description: "CREATE TABLE triggering ddl_charset_status=unknown and metadata_context_complete=false",
			sql: strings.Join([]string{
				"CREATE TABLE shop.legacy_notes (",
				"  id BIGINT NOT NULL,",
				"  body VARCHAR(200) NOT NULL DEFAULT '',",
				"  PRIMARY KEY (id)",
				") ENGINE=InnoDB COMMENT='charset=custom_legacy_cs';",
				"",
			}, "\n"),
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				g := wire.AnonymizedStatementGroups[0]
				if g.Statistics["ddl_charset_status"] != "unknown" {
					t.Fatalf("expected ddl_charset_status=unknown, got %#v", g.Statistics["ddl_charset_status"])
				}
				if g.MetadataContextComplete {
					t.Fatal("expected metadata_context_complete=false when ddl_charset_status=unknown")
				}
			},
		},
		{
			name:        "09_alter_modify_after",
			description: "ALTER TABLE MODIFY COLUMN ... AFTER ...",
			sql:         "ALTER TABLE shop.orders MODIFY COLUMN status VARCHAR(64) NOT NULL DEFAULT 'pending' AFTER customer_id;\n",
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				g := wire.AnonymizedStatementGroups[0]
				if g.Statistics["ddl_alters_column_type"] != true || g.Statistics["ddl_changes_column_position"] != true {
					t.Fatalf("unexpected alter stats: %+v", g.Statistics)
				}
			},
		},
		{
			name:        "10_drop_table_and_index",
			description: "DROP TABLE and DROP INDEX statements",
			sql: strings.Join([]string{
				"DROP TABLE IF EXISTS shop.old_sessions;",
				"DROP INDEX idx_orders_status ON shop.orders;",
				"",
			}, "\n"),
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				if len(wire.AnonymizedStatementGroups) != 2 {
					t.Fatalf("groups len = %d, want 2", len(wire.AnonymizedStatementGroups))
				}
				if wire.AnonymizedStatementGroups[0].Statistics["ddl_drop_target"] != "table" ||
					wire.AnonymizedStatementGroups[1].Statistics["ddl_drop_target"] != "index" {
					t.Fatalf("unexpected drop targets: %+v / %+v",
						wire.AnonymizedStatementGroups[0].Statistics["ddl_drop_target"],
						wire.AnonymizedStatementGroups[1].Statistics["ddl_drop_target"])
				}
			},
		},
		{
			name:        "11_create_view_select_star",
			description: "CREATE OR REPLACE VIEW using SELECT *",
			sql:         "CREATE OR REPLACE VIEW shop.v_active_orders (id, status, amount) AS SELECT * FROM shop.orders WHERE status = 'active';\n",
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				g := wire.AnonymizedStatementGroups[0]
				if g.Statistics["ddl_is_create_view"] != true || g.Statistics["ddl_view_select_star"] != true {
					t.Fatalf("unexpected create view stats: %+v", g.Statistics)
				}
			},
		},
		{
			name:        "12_rename_table_cross_db",
			description: "Cross-database RENAME TABLE classified as DDL with AST objects and ddl_across_db_rename=true",
			sql:         "RENAME TABLE shop.orders_old TO archive.orders_old;\n",
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				g := wire.AnonymizedStatementGroups[0]
				if g.RepresentativeShape != "kind=ddl op=rename where=true" {
					t.Fatalf("unexpected representative_shape: %q", g.RepresentativeShape)
				}
				if g.Statistics["ddl_is_rename_table"] != true || g.Statistics["ddl_across_db_rename"] != true {
					t.Fatalf("unexpected rename stats: %+v", g.Statistics)
				}
				if !g.MetadataContextComplete {
					t.Fatal("expected metadata_context_complete=true for AST-parsed RENAME TABLE")
				}
				if len(g.Objects) != 4 {
					t.Fatalf("objects len = %d, want 4 (%+v)", len(g.Objects), g.Objects)
				}
			},
		},
		{
			name:        "13_truncate_table",
			description: "TRUNCATE TABLE statement",
			sql:         "TRUNCATE TABLE shop.staging_events;\n",
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				g := wire.AnonymizedStatementGroups[0]
				if g.RepresentativeShape != "kind=ddl op=truncate where=true" {
					t.Fatalf("unexpected representative_shape: %q", g.RepresentativeShape)
				}
			},
		},
		{
			name:        "14_ddl_multi_group",
			description: "Multi-group DDL batch (CREATE TABLE + ALTER TABLE + DROP TABLE)",
			sql: strings.Join([]string{
				"CREATE TABLE shop.shipments (",
				"  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,",
				"  order_id BIGINT UNSIGNED NOT NULL,",
				"  PRIMARY KEY (id)",
				") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;",
				"ALTER TABLE shop.orders ADD COLUMN tracking_no VARCHAR(64) NULL;",
				"DROP TABLE IF EXISTS shop.tmp_shipments;",
				"",
			}, "\n"),
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				if len(wire.AnonymizedStatementGroups) != 3 {
					t.Fatalf("groups len = %d, want 3", len(wire.AnonymizedStatementGroups))
				}
			},
		},
		{
			name:        "15_oversized_literal_truncation",
			description: "SQL single-quoted literal exceeding 256 runes truncated with preview and length marker",
			sql:         fmt.Sprintf("INSERT INTO shop.order_notes (order_id, note) VALUES (1001, '%s');\n", oversizedLiteral),
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				sqlText := wire.AnonymizedStatementGroups[0].RepresentativeAnonymizedSQL
				if !strings.Contains(sqlText, "...<truncated:300_chars>'") {
					t.Fatalf("expected truncated 300-char literal marker in %q", sqlText)
				}
			},
		},
		{
			name:            "16_prehydrated_normalized_evidence",
			description:     "DML statement with prehydrated schema_metadata and index_metadata normalized_evidence",
			sql:             "UPDATE shop.orders SET status = 'shipped' WHERE customer_id = 42;\n",
			withPrehydrated: true,
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				if len(wire.NormalizedEvidence) != 2 {
					t.Fatalf("normalized_evidence len = %d, want 2", len(wire.NormalizedEvidence))
				}
			},
		},
		{
			name:        "17_mixed_ddl_dml_precheck",
			description: "Mixed DDL and DML batch allowed during precheck review (execution preflight rejects as 3006)",
			sql: strings.Join([]string{
				"ALTER TABLE shop.orders ADD COLUMN remark VARCHAR(128) NULL;",
				"UPDATE shop.orders SET remark = 'backfilled' WHERE id = 1;",
				"",
			}, "\n"),
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				if len(wire.AnonymizedStatementGroups) != 2 {
					t.Fatalf("groups len = %d, want 2", len(wire.AnonymizedStatementGroups))
				}
				if !strings.HasPrefix(wire.AnonymizedStatementGroups[0].RepresentativeShape, "kind=ddl ") ||
					!strings.HasPrefix(wire.AnonymizedStatementGroups[1].RepresentativeShape, "kind=dml ") {
					t.Fatalf("expected ddl + dml shapes, got %q / %q",
						wire.AnonymizedStatementGroups[0].RepresentativeShape,
						wire.AnonymizedStatementGroups[1].RepresentativeShape)
				}
			},
		},
		{
			name:        "18_create_view_no_column_list",
			description: "Bare CREATE OR REPLACE VIEW without explicit column list using SELECT *",
			sql:         "CREATE OR REPLACE VIEW shop.v_active_orders AS SELECT * FROM shop.orders WHERE status = 'active';\n",
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				g := wire.AnonymizedStatementGroups[0]
				if g.RepresentativeShape != "kind=ddl op=create where=true" {
					t.Fatalf("unexpected representative_shape: %q", g.RepresentativeShape)
				}
				if g.Statistics["ddl_is_create_view"] != true || g.Statistics["ddl_view_select_star"] != true {
					t.Fatalf("unexpected create view stats: %+v", g.Statistics)
				}
				if g.MetadataContextComplete {
					t.Fatal("expected metadata_context_complete=false for CREATE VIEW")
				}
				if len(g.Objects) == 0 {
					t.Fatal("expected non-empty objects for AST-parsed bare CREATE VIEW")
				}
			},
		},
		{
			name:        "19a_rename_table_unqualified",
			description: "Same-database unqualified RENAME TABLE bound to stage default DB with ddl_across_db_rename=false",
			sql:         "RENAME TABLE orders_old TO orders_archive;\n",
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				g := wire.AnonymizedStatementGroups[0]
				if g.RepresentativeShape != "kind=ddl op=rename where=true" {
					t.Fatalf("unexpected representative_shape: %q", g.RepresentativeShape)
				}
				if g.Statistics["ddl_is_rename_table"] != true || g.Statistics["ddl_across_db_rename"] != false {
					t.Fatalf("unexpected rename stats: %+v", g.Statistics)
				}
				if !g.MetadataContextComplete {
					t.Fatal("expected metadata_context_complete=true for unqualified RENAME TABLE with default DB binding")
				}
				if len(g.Objects) < 2 {
					t.Fatalf("expected at least 2 objects for unqualified RENAME TABLE, got %+v", g.Objects)
				}
			},
		},
		{
			name:        "19b_rename_table_same_db_qualified",
			description: "Explicit same-database qualified RENAME TABLE with ddl_across_db_rename=false",
			sql:         "RENAME TABLE shop.orders_old TO shop.orders_archive;\n",
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				g := wire.AnonymizedStatementGroups[0]
				if g.RepresentativeShape != "kind=ddl op=rename where=true" {
					t.Fatalf("unexpected representative_shape: %q", g.RepresentativeShape)
				}
				if g.Statistics["ddl_is_rename_table"] != true || g.Statistics["ddl_across_db_rename"] != false {
					t.Fatalf("unexpected rename stats: %+v", g.Statistics)
				}
				if !g.MetadataContextComplete {
					t.Fatal("expected metadata_context_complete=true for explicit same-DB RENAME TABLE")
				}
				if len(g.Objects) < 3 {
					t.Fatalf("expected at least 3 objects for explicit same-DB RENAME TABLE, got %+v", g.Objects)
				}
			},
		},
		{
			name:        "20_create_table_as_select",
			description: "CREATE TABLE ... AS SELECT (CTAS) classified as DDL with AST objects",
			sql:         "CREATE TABLE shop.orders_bak AS SELECT * FROM shop.orders WHERE status = 'paid';\n",
			assertExtra: func(t *testing.T, wire sanWireGoldenPayload, _ []provider.AnonymizedGroup) {
				g := wire.AnonymizedStatementGroups[0]
				if g.RepresentativeShape != "kind=ddl op=create where=true" {
					t.Fatalf("unexpected representative_shape: %q", g.RepresentativeShape)
				}
				if !g.MetadataContextComplete {
					t.Fatal("expected metadata_context_complete=true for CTAS")
				}
				if g.Statistics["ddl_table_name_length"] != float64(len("orders_bak")) {
					t.Fatalf("unexpected ddl_table_name_length: %#v", g.Statistics["ddl_table_name_length"])
				}
				if len(g.Objects) < 3 {
					t.Fatalf("expected at least 3 objects for CTAS, got %+v", g.Objects)
				}
			},
		},
	}
}

type sanGoldenRunOutput struct {
	analysis    *streamedRunAnalysis
	boundGroups []provider.AnonymizedGroup
	innerRaw    []byte
	prettyJSON  []byte
}

func runSANWirePipeline(t *testing.T, rawSQL string, withPrehydrated bool) sanGoldenRunOutput {
	t.Helper()

	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	engine := newPerfTestEngine(t)
	mapper := privacy.NewRunMapper()

	analysis, err := analyzeRunStatementsStreaming(streamingAnalysisOptions{
		runID:          sanGoldenRunID,
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
		ID:            sanGoldenRunID,
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
			ReviewRunID:              sanGoldenRunID,
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

	stageGroups := ephemeralStageGroups(er, "ds-san-golden")
	binding := resolveMySQLStageBinding(
		frozenFlowStageRow{
			Position:          1,
			CompatibilityMode: "mysql",
			DatabaseName:      ptr.To(sanGoldenDefaultDatabase),
		},
		"",
		stageGroups,
		mapper,
	)
	boundGroups, _, _, _ := bindGroupMetadataWithReason(stageGroups, binding, mapper)

	var sanEvidence []provider.NormalizedEvidenceRef
	if withPrehydrated {
		sanEvidence = buildDeterministicSANPrehydratedEvidence(t, boundGroups, mapper, binding)
	}

	stageCtx := withRepresentativeSQLResolver(context.Background(), func(runID, groupID string) (string, bool, error) {
		return ephemeralRepresentativeSQL(er, runID, groupID)
	})

	req, err := buildSANStageRequest(stageCtx, stagePromptRequest{
		runID: sanGoldenRunID,
		target: frozenDatasourceTarget{
			DatasourceID:      "ds-san-golden",
			Engine:            "mysql",
			CompatibilityMode: "mysql",
			ServerVersion:     "8.0.36",
		},
		groups:       boundGroups,
		sanEvidence:  sanEvidence,
		runMapper:    mapper,
		outputLocale: "en-US",
	})
	if err != nil {
		t.Fatalf("buildSANStageRequest failed: %v", err)
	}

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
		ProviderID:          "prov-san-golden",
		Name:                "san-local",
		ProviderKind:        "openai_compatible",
		BaseURL:             srv.URL,
		ModelName:           "san-4b",
		PrivacyContractHash: provider.PrivacyContractHashSAN,
		OutputSchemaHash:    provider.OutputSchemaHash,
		APIKey:              "test-san-key",
		Configuration: map[string]any{
			"system_prompt_profile":      promptprofile.ProfileSAN,
			provider.ConfigKeyOutputMode: provider.OutputModeJSONObject,
		},
	}, &provider.HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatalf("NewOpenAIAdapter failed: %v", err)
	}
	if _, err := adapter.ReviewStructured(context.Background(), req); err != nil {
		t.Fatalf("ReviewStructured failed: %v", err)
	}

	var envelope struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(capturedBody, &envelope); err != nil {
		t.Fatalf("unmarshal HTTP request body: %v", err)
	}
	if len(envelope.Messages) != 2 || envelope.Messages[1].Role != "user" {
		t.Fatalf("unexpected HTTP messages: %+v", envelope.Messages)
	}
	userContent := envelope.Messages[1].Content
	if !strings.HasPrefix(userContent, provider.SANWireUserMessagePrefix) {
		t.Fatalf("messages[1].content missing SAN prefix %q: %q", provider.SANWireUserMessagePrefix, userContent)
	}
	innerRaw := []byte(strings.TrimPrefix(userContent, provider.SANWireUserMessagePrefix))

	var wireMap map[string]json.RawMessage
	if err := json.Unmarshal(innerRaw, &wireMap); err != nil {
		t.Fatalf("unmarshal inner SAN wire JSON: %v", err)
	}
	if string(wireMap["allowed_tool_descriptors"]) != "[]" {
		t.Fatalf("expected allowed_tool_descriptors == [], got %s", wireMap["allowed_tool_descriptors"])
	}
	for _, elided := range []string{"system_prompt", "rule_snapshot", "output_schema"} {
		if _, present := wireMap[elided]; present {
			t.Fatalf("SAN wire JSON unexpectedly contained elided field %q", elided)
		}
	}

	var pretty bytes.Buffer
	if err := json.Indent(&pretty, innerRaw, "", "  "); err != nil {
		t.Fatalf("json.Indent inner SAN wire JSON: %v", err)
	}
	pretty.WriteByte('\n')

	var compacted bytes.Buffer
	if err := json.Compact(&compacted, pretty.Bytes()); err != nil {
		t.Fatalf("json.Compact pretty JSON: %v", err)
	}
	if !bytes.Equal(compacted.Bytes(), innerRaw) {
		t.Fatalf("pretty round-trip compact JSON mismatch")
	}

	return sanGoldenRunOutput{
		analysis:    analysis,
		boundGroups: boundGroups,
		innerRaw:    innerRaw,
		prettyJSON:  pretty.Bytes(),
	}
}

func buildDeterministicSANPrehydratedEvidence(
	t *testing.T,
	boundGroups []provider.AnonymizedGroup,
	mapper *privacy.RunMapper,
	binding *stageSchemaBinding,
) []provider.NormalizedEvidenceRef {
	t.Helper()

	var schemaTok, tableTok string
	for _, grp := range boundGroups {
		for _, obj := range grp.Objects {
			if obj.Kind == "table" && obj.SchemaToken != "" && obj.Token != "" {
				schemaTok = obj.SchemaToken
				tableTok = obj.Token
				break
			}
		}
		if schemaTok != "" && tableTok != "" {
			break
		}
	}
	if schemaTok == "" || tableTok == "" {
		t.Fatalf("missing mapped tokens for shop.orders in boundGroups: %+v", boundGroups)
	}

	reg := tool.NewRegistry()
	src := sanTestFactSource{}
	ctx := context.Background()
	tokenArgs := map[string]any{"schema": schemaTok, "table": tableTok}

	execOne := func(callID, toolName, evID string) provider.NormalizedEvidenceRef {
		res := reg.Execute(ctx, src, callID, toolName, map[string]any{"schema": "shop", "table": "orders"})
		if !res.Success {
			t.Fatalf("tool %s failed: error_code=%v", toolName, res.ErrorCode)
		}
		if err := projectToolFactSchema(res.NormalizedFact, tokenArgs, mapper, binding); err != nil {
			t.Fatalf("projectToolFactSchema(%s) failed: %v", toolName, err)
		}
		return provider.NormalizedEvidenceRef{
			EvidenceID: evID,
			FactKind:   res.ToolName,
			Fact:       copyNormalizedFactForSAN(res.NormalizedFact, res.FactSchemaVersion),
		}
	}

	return []provider.NormalizedEvidenceRef{
		execOne("call-schema-1", tool.NameSchemaMetadata, "20000000-0000-4000-8000-000000000001"),
		execOne("call-index-2", tool.NameIndexMetadata, "20000000-0000-4000-8000-000000000002"),
	}
}

func TestSANWireGolden(t *testing.T) {
	t.Parallel()

	dir := sanGoldenDir()
	digest := provider.SANContractDigest()

	for _, tc := range sanGoldenCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sqlPath := filepath.Join(dir, tc.name+".sql")
			reqPath := filepath.Join(dir, tc.name+".request.json")
			metaPath := filepath.Join(dir, tc.name+".meta.json")

			inputSQL := tc.sql
			if !*updateSANWireGolden {
				rawFromDisk, err := os.ReadFile(sqlPath)
				if err != nil {
					t.Fatalf("read golden SQL %s: %v (run with -update to create)", sqlPath, err)
				}
				if string(rawFromDisk) != tc.sql {
					t.Fatalf("golden SQL fixture %s drifted from sanGoldenCases(); regenerate with %q", sqlPath, sanGoldenGenerateCommand)
				}
				inputSQL = string(rawFromDisk)
			}

			out := runSANWirePipeline(t, inputSQL, tc.withPrehydrated)

			var wire sanWireGoldenPayload
			if err := json.Unmarshal(out.prettyJSON, &wire); err != nil {
				t.Fatalf("unmarshal generated SAN wire payload: %v", err)
			}
			if tc.assertExtra != nil {
				tc.assertExtra(t, wire, out.boundGroups)
			}

			meta := sanWireGoldenMeta{
				ContractVersion: provider.SANWireContractVersion,
				ContractDigest:  digest,
				CaseName:        tc.name,
				Description:     tc.description,
				DefaultDatabase: sanGoldenDefaultDatabase,
				EngineProfile:   sanGoldenEngineProfile,
				GenerateCommand: sanGoldenGenerateCommand,
			}
			metaBytes, err := json.MarshalIndent(meta, "", "  ")
			if err != nil {
				t.Fatalf("marshal meta: %v", err)
			}
			metaBytes = append(metaBytes, '\n')

			if *updateSANWireGolden {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", dir, err)
				}
				if err := os.WriteFile(sqlPath, []byte(inputSQL), 0o644); err != nil {
					t.Fatalf("write %s: %v", sqlPath, err)
				}
				if err := os.WriteFile(reqPath, out.prettyJSON, 0o644); err != nil {
					t.Fatalf("write %s: %v", reqPath, err)
				}
				if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
					t.Fatalf("write %s: %v", metaPath, err)
				}
				return
			}

			wantReq, err := os.ReadFile(reqPath)
			if err != nil {
				t.Fatalf("read golden request %s: %v (run with -update to create)", reqPath, err)
			}
			if !bytes.Equal(out.prettyJSON, wantReq) {
				t.Fatalf("SAN wire golden drift detected for %s; if intentional, follow docs/contracts/san-wire-contract.md and regenerate with %q",
					tc.name, sanGoldenGenerateCommand)
			}

			wantMeta, err := os.ReadFile(metaPath)
			if err != nil {
				t.Fatalf("read golden meta %s: %v", metaPath, err)
			}
			if !bytes.Equal(metaBytes, wantMeta) {
				t.Fatalf("SAN golden meta drift detected for %s:\ngot:\n%s\nwant:\n%s", tc.name, metaBytes, wantMeta)
			}
		})
	}
}

func sanWireBranchFor(class, op, rawSQL string) string {
	if class == "dml" {
		switch op {
		case "update", "delete":
			return provider.SANStatBranchDMLUpdateDelete
		case "insert":
			return provider.SANStatBranchDMLInsert
		case "replace":
			return provider.SANStatBranchDMLReplace
		default:
			return provider.SANStatBranchDMLOther
		}
	}
	switch {
	case reCreateView.MatchString(rawSQL):
		return provider.SANStatBranchDDLCreateView
	case op == "drop":
		return provider.SANStatBranchDDLDrop
	case op == "rename":
		if isRenameTable, _ := parseRenameTableFacts([]byte(rawSQL)); isRenameTable {
			return provider.SANStatBranchDDLRenameTable
		}
		return provider.SANStatBranchDDLTruncateOther
	case op == "alter":
		return provider.SANStatBranchDDLAlter
	case op == "create":
		return provider.SANStatBranchDDLCreateOther
	default:
		return provider.SANStatBranchDDLTruncateOther
	}
}

func TestSANStatisticsWhitelist(t *testing.T) {
	t.Parallel()

	dir := sanGoldenDir()
	for _, tc := range sanGoldenCases() {
		tc := tc
		t.Run("golden/"+tc.name, func(t *testing.T) {
			t.Parallel()

			sqlBytes, err := os.ReadFile(filepath.Join(dir, tc.name+".sql"))
			if err != nil {
				t.Fatalf("read fixture SQL: %v", err)
			}
			out := runSANWirePipeline(t, string(sqlBytes), tc.withPrehydrated)
			if len(out.boundGroups) != len(out.analysis.groups) {
				t.Fatalf("boundGroups len = %d, want %d", len(out.boundGroups), len(out.analysis.groups))
			}

			reqBytes, err := os.ReadFile(filepath.Join(dir, tc.name+".request.json"))
			if err != nil {
				t.Fatalf("read fixture request JSON: %v", err)
			}
			var diskWire sanWireGoldenPayload
			if err := json.Unmarshal(reqBytes, &diskWire); err != nil {
				t.Fatalf("unmarshal fixture request JSON: %v", err)
			}
			if len(diskWire.AnonymizedStatementGroups) != len(out.analysis.groups) {
				t.Fatalf("disk groups len = %d, want %d", len(diskWire.AnonymizedStatementGroups), len(out.analysis.groups))
			}

			for i, agg := range out.analysis.groups {
				branch := sanWireBranchFor(agg.repKind, agg.repOperation, agg.repSQL)
				if err := provider.ValidateSANGroupStatistics(branch, out.boundGroups[i].Statistics); err != nil {
					t.Fatalf("live bound group %d (%s): %v", i, branch, err)
				}
				if err := provider.ValidateSANGroupStatistics(branch, diskWire.AnonymizedStatementGroups[i].Statistics); err != nil {
					t.Fatalf("disk fixture group %d (%s): %v", i, branch, err)
				}
			}
		})
	}

	corpus := []struct {
		name string
		sql  string
	}{
		{"stmtshape_comment_charset", "CREATE TABLE orders (id int) COMMENT='charset=private_comment_secret';"},
		{"stmtshape_comment_collate", "CREATE TABLE orders (id int) -- COLLATE private_comment_name\n;"},
		{"stmtshape_default_collate", "CREATE TABLE orders (id int DEFAULT 'COLLATE private_default_secret');"},
		{"stmtshape_unlisted_collate", "CREATE TABLE orders (id int) COLLATE private_structured_name;"},
		{"stmtshape_known_charset", "CREATE TABLE orders (id int) CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;"},
		{"stmtshape_update_limit", "UPDATE orders SET amount = 2 WHERE id = 2 AND status = NULL ORDER BY id LIMIT 10;"},
		{"stmtshape_update_subselect", "UPDATE orders SET amount = 3 WHERE id IN (SELECT id FROM other);"},
		{"stmtshape_delete_limit", "DELETE FROM orders WHERE id = 2 ORDER BY id LIMIT 5;"},
		{"stmtshape_insert_cols_null", "INSERT INTO orders (id, note) VALUES (2, NULL);"},
		{"stmtshape_insert_select", "INSERT INTO orders (id, note) SELECT id, note FROM archive;"},
		{"stmtshape_replace_select", "REPLACE INTO orders SELECT id, note FROM archive;"},
		{"stmtshape_select", "SELECT id FROM orders WHERE id = 1;"},
		{"stmtshape_create_view", "CREATE OR REPLACE VIEW v1 (id) AS SELECT * FROM t1;"},
		{"stmtshape_drop_schema", "DROP SCHEMA s1;"},
		{"stmtshape_drop_table", "DROP TABLE t1;"},
		{"stmtshape_drop_event", "DROP EVENT e1;"},
		{"stmtshape_rename_table", "RENAME TABLE db1.t1 TO db2.t2;"},
		{"stmtshape_rename_user", "RENAME USER 'old'@'localhost' TO 'new'@'localhost';"},
		{"stmtshape_alter_rename_to", "ALTER TABLE shop.orders RENAME TO shop.orders_v2;"},
		{"stmtshape_alter_modify_after", "ALTER TABLE t1 MODIFY COLUMN c1 INT AFTER c0;"},
		{"stmtshape_truncate", "TRUNCATE TABLE t1;"},
		{"stmtshape_grant", "GRANT SELECT ON t1 TO u1;"},
		{"stmtshape_create_full", "CREATE TABLE db1.`order` (id BIGINT UNSIGNED AUTO_INCREMENT, code CHAR(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci, name VARCHAR(128) NOT NULL DEFAULT 'a' COMMENT 'n', PRIMARY KEY (id), INDEX (code, (LOWER(name))), INDEX bad_prefix (name)) AUTO_INCREMENT = 1 COMMENT = 'tbl';"},
	}

	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	engine := newPerfTestEngine(t)
	for _, sample := range corpus {
		sample := sample
		t.Run("corpus/"+sample.name, func(t *testing.T) {
			t.Parallel()

			mapper := privacy.NewRunMapper()
			analysis, err := analyzeRunStatementsStreaming(streamingAnalysisOptions{
				runID:          sanGoldenRunID,
				rawSQL:         []byte(sample.sql),
				profile:        profile,
				mapper:         mapper,
				engine:         engine,
				validateSyntax: false,
				inlineLimit:    ephemeralInlineStatementLimit,
			})
			if err != nil {
				t.Fatalf("analyzeRunStatementsStreaming(%s) failed: %v", sample.name, err)
			}
			er := &EphemeralRun{
				ID:        sanGoldenRunID,
				groupMeta: map[string]ephemeralGroupMeta{},
			}
			for i, g := range analysis.groups {
				typed, merr := buildGroupTypedShape(g)
				if merr != nil {
					t.Fatal(merr)
				}
				gid := fmt.Sprintf("g-%d", i+1)
				er.Groups = append(er.Groups, persistence.SQLFingerprintGroupModel{
					ID:             gid,
					ReviewRunID:    er.ID,
					TypedShape:     typed,
					StatementCount: int32(g.count),
				})
				er.groupMeta[gid] = ephemeralGroupMeta{
					RepKind:      g.repKind,
					RepOperation: g.repOperation,
					SizeMax:      g.sizeMax,
					RepOrdinal:   g.representativeOrdinal,
				}
			}
			groups := ephemeralStageGroups(er, "ds-corpus")
			for i, g := range analysis.groups {
				branch := sanWireBranchFor(g.repKind, g.repOperation, g.repSQL)
				if err := provider.ValidateSANGroupStatistics(branch, groups[i].Statistics); err != nil {
					t.Fatalf("corpus %s (%s): %v", sample.name, branch, err)
				}
			}
		})
	}

	assertDirectFacts := func(name, branch, op, class, sqlText string) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			facts := extractStmtFacts([]byte(sqlText), op, class)
			typed, err := buildGroupTypedShape(aggregatedGroup{
				representativeAnonymizedSQL: "direct_fact_corpus",
				representativeOrdinal:       1,
				repKind:                     class,
				repOperation:                op,
				repSQL:                      sqlText,
				sizeMax:                     int64(len(sqlText)),
				statementOrdinalRanges:      []statementOrdinalRange{{Start: 1, End: 1}},
				objects:                     []fingerprint.ObjectReference{},
				count:                       1,
				facts:                       facts,
			})
			if err != nil {
				t.Fatal(err)
			}
			er := &EphemeralRun{
				ID: "r-" + name,
				Groups: []persistence.SQLFingerprintGroupModel{{
					ID:             "g-1",
					ReviewRunID:    "r-" + name,
					TypedShape:     typed,
					StatementCount: 1,
				}},
				groupMeta: map[string]ephemeralGroupMeta{
					"g-1": {RepKind: class, RepOperation: op, SizeMax: int64(len(sqlText)), RepOrdinal: 1},
				},
			}
			groups := ephemeralStageGroups(er, "ds-"+name)
			if err := provider.ValidateSANGroupStatistics(branch, groups[0].Statistics); err != nil {
				t.Fatalf("%s (%s) validation failed: %v", name, branch, err)
			}
		})
	}
	assertDirectFacts("ddl_rename_table_schema", provider.SANStatBranchDDLRenameTable, "rename", "ddl", "RENAME TABLE shop.orders_old TO archive.orders_old;")
	assertDirectFacts("ddl_create_view_bare_schema", provider.SANStatBranchDDLCreateView, "create", "ddl", "CREATE OR REPLACE VIEW v1 AS SELECT * FROM t1;")
}
