//go:build integration

package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/platform/testenv"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

type quotedFixtureSigner struct{}

func (quotedFixtureSigner) KeyID() string { return "quoted-fixture" }

func (quotedFixtureSigner) HMACSHA256(data []byte) ([]byte, error) {
	sum := sha256.Sum256(data)
	return sum[:], nil
}

// TestQuotedPostgreSQLFactFingerprintTokensReachProviderRequest uses the
// isolated target PostgreSQL container, not the application metadata DB. It
// proves case-sensitive catalog facts reuse the same RunMapper tokens as the
// quoted SQL object graph and that the complete Provider request leaks no real
// identifier.
func TestQuotedPostgreSQLFactFingerprintTokensReachProviderRequest(t *testing.T) {
	cfg := testenv.RequireTargetPG(t)
	port := cfg.Port
	host := cfg.Host
	user := cfg.User
	password := cfg.Password
	dbname := cfg.DBName
	admin, err := sql.Open("pgx", cfg.LibpqDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx := context.Background()
	schema := fmt.Sprintf("R012_Quoted_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	for _, statement := range []string{
		`CREATE TYPE "` + schema + `"."CaseState" AS ENUM ('open', 'closed')`,
		`CREATE TABLE "` + schema + `"."Foo" ("BarCol" BIGINT, "barcol" TEXT, "state" "` + schema + `"."CaseState", id BIGINT)`,
		`CREATE INDEX "IDX_Foo" ON "` + schema + `"."Foo" ("BarCol")`,
	} {
		if _, err := admin.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	adapter := database.NewPostgreSQLAdapter(database.Endpoint{
		Host: host, Port: port, DatabaseName: dbname, CompatibilityMod: "postgresql",
	}, database.AdapterOptions{SchemaContext: schema})
	t.Cleanup(func() { _ = adapter.Close() })
	pool, err := adapter.OpenPurposePool(ctx, database.PurposeReview, database.Credential{
		Username: user, Password: password,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	columns, err := adapter.ListColumns(ctx, pool, schema, "Foo")
	if err != nil {
		t.Fatal(err)
	}
	indexes, err := adapter.ListIndexes(ctx, pool, schema, "Foo")
	if err != nil {
		t.Fatal(err)
	}

	mapper := privacy.NewRunMapper()
	engine, err := fingerprint.NewEngine(quotedFixtureSigner{}, fingerprint.Options{})
	if err != nil {
		t.Fatal(err)
	}
	query := `UPDATE "` + schema + `"."Foo" SET "BarCol" = "BarCol", "barcol" = "barcol" WHERE id = "id"`
	analyzed, err := engine.Analyze(
		fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"},
		[]byte(query),
		mapper,
	)
	if err != nil {
		t.Fatal(err)
	}
	if analyzed.Quality != fingerprint.QualityExact {
		t.Fatalf("quoted fixture query quality = %s, want exact", analyzed.Quality)
	}
	schemaToken := mapper.LookupScoped(privacy.KindSchema, privacy.ScopeCaseSensitive, schema)
	tableToken := mapper.LookupScoped(privacy.KindTable, privacy.ScopeCaseSensitive, "Foo")
	if schemaToken == "" || tableToken == "" {
		t.Fatalf("quoted query did not create schema/table bindings: schema=%q table=%q",
			schemaToken, tableToken)
	}
	columnFolded := mapper.Lookup(privacy.KindColumn, "id")
	columnExact := mapper.LookupScoped(privacy.KindColumn, privacy.ScopeCaseSensitive, "id")
	if columnFolded == "" || columnExact == "" || columnFolded == columnExact {
		t.Fatalf("unquoted/quoted id must have distinct Run tokens: folded=%q exact=%q",
			columnFolded, columnExact)
	}
	if _, err := mapper.ScopedToken(privacy.KindColumn, privacy.ScopeCaseSensitive, "state"); err != nil {
		t.Fatal(err)
	}
	projectedColumns, _, metadataComplete := metadataColumnFacts(columns)
	if metadataComplete {
		t.Fatal("custom PostgreSQL enum must not be presented as a fully known type profile")
	}
	for _, column := range columns {
		if _, err := mapper.ScopedToken(privacy.KindColumn, privacy.ScopeCaseSensitive, column.ColumnName); err != nil {
			t.Fatal(err)
		}
	}
	for _, index := range indexes {
		if _, err := mapper.ScopedToken(privacy.KindIndex, privacy.ScopeCaseSensitive, index.IndexName); err != nil {
			t.Fatal(err)
		}
	}
	columnFacts := make([]any, 0, len(projectedColumns))
	for _, column := range projectedColumns {
		columnFacts = append(columnFacts, column)
	}
	fact := map[string]any{
		"schema": schema, "table": "Foo", "metadata_complete": metadataComplete,
		"columns": columnFacts,
	}
	indexFacts := make([]any, 0, len(indexes))
	for _, index := range indexes {
		indexColumns := make([]any, 0, len(index.ColumnNames))
		for _, column := range index.ColumnNames {
			indexColumns = append(indexColumns, column)
		}
		indexFacts = append(indexFacts, map[string]any{
			"index_name": index.IndexName,
			"primary":    index.Primary,
			"columns":    indexColumns,
		})
	}
	fact["indexes"] = indexFacts
	if err := tokenizeToolFactWithBindings(fact, mapper, map[string]any{
		"schema": schemaToken, "table": tableToken,
	}); err != nil {
		t.Fatal(err)
	}
	if fact["schema"] != schemaToken || fact["table"] != tableToken {
		t.Fatalf("quoted object tokens were not reused: %#v", fact)
	}
	if got := fact["columns"].([]any)[0].(map[string]any)["column_name"]; got != "column_1" {
		t.Fatalf("quoted column token = %v, want column_1", got)
	}
	if got := fact["columns"].([]any)[1].(map[string]any)["column_name"]; got != "column_2" {
		t.Fatalf("lowercase column token = %v, want column_2", got)
	}
	custom := projectedColumns[2]
	if custom["type_class"] != "other" || custom["type_status"] != "unknown" {
		t.Fatalf("custom type must remain unknown without leaking its name: %#v", custom)
	}
	if got := fact["columns"].([]any)[3].(map[string]any)["column_name"]; got != columnExact {
		t.Fatalf("id primary column token = %v, want exact token %s", got, columnExact)
	}
	assertFactTokenList(t,
		fact["columns"].([]any)[3].(map[string]any)["sql_reference_tokens"],
		[]string{columnFolded, columnExact},
	)
	if got := fact["indexes"].([]any)[0].(map[string]any)["index_name"]; got != "index_1" {
		t.Fatalf("quoted index token = %v, want index_1", got)
	}
	assertFactTokenList(t,
		fact["indexes"].([]any)[0].(map[string]any)["sql_reference_tokens"],
		[]string{"index_1"},
	)

	req := agent.BuildRequest("00000000-0000-4000-8000-000000000812", agent.ContextInput{
		EngineProfile: "postgresql/postgresql",
		OutputLocale:  "en-US",
		Groups: []provider.AnonymizedGroup{{
			GroupID:                     "00000000-0000-4000-8000-000000000813",
			RepresentativeShape:         "kind=update op=update where=true",
			RepresentativeAnonymizedSQL: "update " + schemaToken + "." + tableToken + ` set "` + fact["columns"].([]any)[0].(map[string]any)["column_name"].(string) + `" = 1`,
			Objects: []provider.AnonymizedObjectReference{
				{Token: schemaToken, Kind: "schema", Relation: "reference", StatementOrdinal: 1},
				{Token: tableToken, Kind: "table", Relation: "target", SchemaToken: schemaToken, StatementOrdinal: 1},
			},
			MetadataContextComplete: true,
			StatementCount:          1,
			StatementOrdinal:        1,
		}},
		Evidence: []provider.NormalizedEvidenceRef{{
			EvidenceID: "00000000-0000-4000-8000-000000000814",
			FactKind:   tool.NameSchemaMetadata,
			Fact:       fact,
		}},
	}, tool.NewRegistry())
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, original := range []string{schema, "Foo", "BarCol", "barcol", "IDX_Foo"} {
		if strings.Contains(body, original) {
			t.Fatalf("provider request leaked quoted identifier %q", original)
		}
	}
	for _, token := range []string{schemaToken, tableToken, "column_1", "column_2",
		columnFolded, columnExact, "index_1"} {
		if !strings.Contains(body, token) {
			t.Fatalf("provider request missing bound token %q", token)
		}
	}
}
