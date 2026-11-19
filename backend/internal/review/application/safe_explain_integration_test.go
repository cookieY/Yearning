//go:build integration

package application

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// TestRepresentativeStatementSQLFeedsRealReadOnlyExplain proves that grouping
// ordinals resolve the real pending statement and that safe_explain only plans
// it. The fixture mutation is setup; the reviewed UPDATE is never executed.
func TestRepresentativeStatementSQLFeedsRealReadOnlyExplain(t *testing.T) {
	b := newB7Fixture(t)
	ctx := context.Background()

	schema := fmt.Sprintf("r012_explain_%d", time.Now().UnixNano())
	const table = "r012_explain_items"
	if err := b.db.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = b.db.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error
	})
	if err := b.db.Exec(`CREATE TABLE "` + schema + `"."` + table + `"
		(id BIGINT PRIMARY KEY, amount BIGINT NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`INSERT INTO "` + schema + `"."` + table + `" (id, amount)
		VALUES (1, 7)`).Error; err != nil {
		t.Fatal(err)
	}

	if err := b.db.Exec(`UPDATE datasources
		SET engine = 'postgresql', compatibility_mode = 'postgresql'
		WHERE id IN (SELECT datasource_id FROM flow_stages WHERE flow_id = ?)`,
		b.flowID).Error; err != nil {
		t.Fatal(err)
	}
	// RCP-20261101: ephemeral grouping driver (empty complete responder -> converges to partial; grouping
	// artifacts are isomorphic to the persisted rows of the legacy sql_grouping task).
	runID := runGroupingEphemeral(t, b,
		"SELECT 1;\nUPDATE "+table+" SET amount = amount + 1 WHERE id = 1;\n",
		"r012-real-explain-"+runKeySuffix())

	var groupID string
	if err := b.db.Raw(`SELECT g.id::text
		FROM sql_fingerprint_groups g
		JOIN sql_statements ss ON ss.id = g.representative_statement_id
		WHERE g.review_run_id = ? AND ss.ordinal = 2`, runID).
		Scan(&groupID).Error; err != nil {
		t.Fatal(err)
	}
	if groupID == "" {
		t.Fatal("representative UPDATE group not found")
	}

	port, err := strconv.Atoi(itestEnv("YEARNING_TEST_DATABASE_PORT", "54330"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := database.NewPostgreSQLAdapter(database.Endpoint{
		Host:             itestEnv("YEARNING_TEST_DATABASE_HOST", "localhost"),
		Port:             port,
		DatabaseName:     itestEnv("YEARNING_TEST_DATABASE_DBNAME", "yearning_v4"),
		CompatibilityMod: "postgresql",
	}, database.AdapterOptions{})
	t.Cleanup(func() { _ = adapter.Close() })
	reviewPool, err := adapter.OpenPurposePool(ctx, database.PurposeReview, database.Credential{
		Username: itestEnv("YEARNING_TEST_DATABASE_USER", "yearning"),
		Password: os.Getenv("YEARNING_TEST_DATABASE_PASSWORD"),
	}, nil)
	if err != nil {
		t.Fatalf("open real review pool: %v", err)
	}
	source := &factSource{
		db:      b.db,
		crypter: b.svc.Crypter,
		runID:   runID,
		adapter: adapter,
		pool:    reviewPool,
	}

	statement, err := source.representativeStatementSQL(ctx, groupID)
	if err != nil {
		t.Fatalf("resolve representative statement: %v", err)
	}
	if statement != "UPDATE r012_explain_items SET amount = amount + 1 WHERE id = 1" {
		t.Fatalf("representative statement = %q", statement)
	}

	fact, err := source.SafeExplain(ctx, schema, groupID)
	if err != nil {
		t.Fatalf("real safe_explain: %v", err)
	}
	if fact["estimate"] != true || fact["access_method"] == "" {
		t.Fatalf("real explain fact = %#v", fact)
	}

	var amount int64
	if err := b.db.Raw(`SELECT amount FROM "` + schema + `"."` + table + `" WHERE id = 1`).
		Scan(&amount).Error; err != nil {
		t.Fatal(err)
	}
	if amount != 7 {
		t.Fatalf("safe_explain mutated pending SQL: amount = %d, want 7", amount)
	}
}

// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: the logical-to-physical mapping channel has been removed;
// evidence tools read directly against the allowed physical schema, and SQL qualifiers are never rewritten.
func TestPhysicalSchemaAcrossRealPostgresFacts(t *testing.T) {
	b := newB7Fixture(t)
	ctx := context.Background()

	const (
		physicalSchema = "r012_physical_items"
		table          = "orders"
	)
	if err := b.db.Exec(`DROP SCHEMA IF EXISTS "` + physicalSchema + `" CASCADE`).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`CREATE SCHEMA "` + physicalSchema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = b.db.Exec(`DROP SCHEMA IF EXISTS "` + physicalSchema + `" CASCADE`).Error
	})
	if err := b.db.Exec(`CREATE TABLE "` + physicalSchema + `"."` + table + `"
		(id BIGINT PRIMARY KEY, amount BIGINT NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`CREATE INDEX orders_amount_idx ON "` + physicalSchema + `"."` + table + `" (amount)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`INSERT INTO "` + physicalSchema + `"."` + table + `" (id, amount)
		VALUES (1, 7)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := b.db.Exec(`UPDATE datasources
		SET engine = 'postgresql', compatibility_mode = 'postgresql'
		WHERE id IN (SELECT datasource_id FROM flow_stages WHERE flow_id = ?)`,
		b.flowID).Error; err != nil {
		t.Fatal(err)
	}
	runID := runGroupingEphemeral(t, b,
		"UPDATE "+physicalSchema+"."+table+" SET amount = amount + 1 WHERE id = 1;\n",
		"r012-physical-"+runKeySuffix())
	var groupID string
	if err := b.db.Raw(`SELECT id::text FROM sql_fingerprint_groups
		WHERE review_run_id = ?`, runID).Scan(&groupID).Error; err != nil || groupID == "" {
		t.Fatalf("load group: %v", err)
	}

	port, err := strconv.Atoi(itestEnv("YEARNING_TEST_DATABASE_PORT", "54330"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := database.NewPostgreSQLAdapter(database.Endpoint{
		Host:             itestEnv("YEARNING_TEST_DATABASE_HOST", "localhost"),
		Port:             port,
		DatabaseName:     itestEnv("YEARNING_TEST_DATABASE_DBNAME", "yearning_v4"),
		CompatibilityMod: "postgresql",
	}, database.AdapterOptions{})
	t.Cleanup(func() { _ = adapter.Close() })
	reviewPool, err := adapter.OpenPurposePool(ctx, database.PurposeReview, database.Credential{
		Username: itestEnv("YEARNING_TEST_DATABASE_USER", "yearning"),
		Password: os.Getenv("YEARNING_TEST_DATABASE_PASSWORD"),
	}, nil)
	if err != nil {
		t.Fatalf("open review pool: %v", err)
	}
	source := &factSource{
		db: b.db, crypter: b.svc.Crypter, runID: runID,
		adapter: adapter, pool: reviewPool,
	}
	mapper := privacy.NewRunMapper()
	schemaToken, _ := mapper.Token(privacy.KindSchema, physicalSchema)
	tableToken, _ := mapper.Token(privacy.KindTable, table)
	binding := bindingOf(physicalSchema)
	args := map[string]any{"schema": schemaToken, "table": tableToken}

	tableFact, err := source.TableMetadata(ctx, physicalSchema, table)
	if err != nil {
		t.Fatalf("physical table metadata: %v", err)
	}
	indexFact, err := source.IndexMetadata(ctx, physicalSchema, table)
	if err != nil {
		t.Fatalf("physical index metadata: %v", err)
	}
	for name, fact := range map[string]map[string]any{
		"table": tableFact, "index": indexFact,
	} {
		if err := projectToolFactSchema(fact, args, mapper, binding); err != nil {
			t.Fatalf("project %s fact: %v", name, err)
		}
		if fact["schema"] != physicalSchema {
			t.Fatalf("%s fact schema = %v, want physical %s", name, fact["schema"], physicalSchema)
		}
		if err := tokenizeToolFactWithBindings(fact, mapper, args); err != nil {
			t.Fatalf("tokenize %s fact: %v", name, err)
		}
		if fact["schema"] != schemaToken || fact["table"] != tableToken {
			t.Fatalf("%s fact logical tokens = %v", name, fact)
		}
	}

	fact, err := source.SafeExplain(ctx, physicalSchema, groupID)
	if err != nil {
		t.Fatalf("real safe_explain: %v", err)
	}
	if fact["estimate"] != true || fact["access_method"] == "" {
		t.Fatalf("real explain fact = %#v", fact)
	}
	var amount int64
	if err := b.db.Raw(`SELECT amount FROM "` + physicalSchema + `"."` + table + `" WHERE id = 1`).
		Scan(&amount).Error; err != nil {
		t.Fatal(err)
	}
	if amount != 7 {
		t.Fatalf("safe_explain mutated pending SQL: amount = %d, want 7", amount)
	}
}
