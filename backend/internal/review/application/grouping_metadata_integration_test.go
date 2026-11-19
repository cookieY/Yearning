//go:build integration

package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

type groupingStatementShape struct {
	AnonymizedSQL           string                        `json:"anonymized_sql"`
	Objects                 []fingerprint.ObjectReference `json:"objects"`
	StatementOrdinal        int32                         `json:"statement_ordinal"`
	MetadataContextComplete bool                          `json:"metadata_context_complete"`
}

type groupingGroupShape struct {
	RepresentativeAnonymizedSQL string                        `json:"representative_anonymized_sql"`
	Objects                     []fingerprint.ObjectReference `json:"objects"`
	StatementOrdinal            int32                         `json:"statement_ordinal"`
	StatementOrdinalRanges      []statementOrdinalRange       `json:"statement_ordinal_ranges"`
	MetadataContextComplete     bool                          `json:"metadata_context_complete"`
}

// runGroupingEphemeral drives ephemeral grouping (RCP-20261101): runs the review to a terminal state and
// materializes it — grouping artifacts (statements / fingerprint groups / identifier mappings) are
// isomorphic to the persisted rows of the legacy sql_grouping task phase. Uses an empty complete
// responder (the review itself converging to partial/2007 does not affect the grouping assertion surface).
func runGroupingEphemeral(t *testing.T, b *b7fx, sql, idempotencyKey string) string {
	t.Helper()
	draft := b.createDraftWithSQL(t, sql)
	er, runID := b.startEphemeralTerminal(t, b.owner, b.fakeDial(emptyCompleteProviderBody), EphemeralStartInput{
		FlowID:         b.flowID,
		DraftID:        draft.ID,
		SQL:            []byte(sql),
		IdempotencyKey: idempotencyKey,
	})
	b.materializeEphemeral(t, er, draft.ID, draft.Revision)
	return runID
}

func TestGroupingMetadataPersistsAnonymousSQLObjectsOrdinalsAndMapping(t *testing.T) {
	b := newB7Fixture(t)
	runID := runGroupingEphemeral(t, b,
		"select id from missing_alpha;\nselect id from missing_beta;\nselect id from missing_alpha;\n",
		"grouping-metadata-"+runKeySuffix())

	var statementRows []struct {
		Ordinal int32
		Typed   []byte
	}
	if err := b.db.Raw(`select ordinal, typed_shape::text as typed
		from sql_statements where review_run_id = ? order by ordinal`, runID).
		Scan(&statementRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(statementRows) != 3 {
		t.Fatalf("statement rows = %d, want 3", len(statementRows))
	}
	statementTables := map[int32]string{}
	for _, row := range statementRows {
		var shape groupingStatementShape
		if err := json.Unmarshal(row.Typed, &shape); err != nil {
			t.Fatalf("decode statement %d typed_shape: %v", row.Ordinal, err)
		}
		if shape.StatementOrdinal != row.Ordinal {
			t.Fatalf("statement ordinal = %d, want %d", shape.StatementOrdinal, row.Ordinal)
		}
		if !shape.MetadataContextComplete {
			t.Fatalf("statement %d metadata context unexpectedly incomplete", row.Ordinal)
		}
		if !strings.Contains(shape.AnonymizedSQL, "table_") {
			t.Fatalf("statement %d has no anonymous object SQL: %q", row.Ordinal, shape.AnonymizedSQL)
		}
		for _, object := range shape.Objects {
			if object.Kind == "table" && object.Relation == fingerprint.RelationRead {
				statementTables[row.Ordinal] = object.Token
			}
			if object.StatementOrdinal != row.Ordinal {
				t.Fatalf("statement %d object ordinal = %d: %+v",
					row.Ordinal, object.StatementOrdinal, object)
			}
		}
		raw := strings.ToLower(string(row.Typed))
		for _, original := range []string{"missing_alpha", "missing_beta"} {
			if strings.Contains(raw, original) {
				t.Fatalf("statement %d typed_shape leaked original %q: %s",
					row.Ordinal, original, row.Typed)
			}
		}
	}
	if statementTables[1] == "" || statementTables[2] == "" || statementTables[3] == "" ||
		statementTables[1] == statementTables[2] ||
		statementTables[1] != statementTables[3] {
		t.Fatalf("distinct referenced tables were not preserved: %+v", statementTables)
	}

	var group struct {
		Typed          []byte
		StatementCount int32
	}
	if err := b.db.Raw(`select typed_shape::text as typed, statement_count
		from sql_fingerprint_groups where review_run_id = ?`, runID).
		Scan(&group).Error; err != nil {
		t.Fatal(err)
	}
	if group.StatementCount != 3 {
		t.Fatalf("group statement count = %d, want 3", group.StatementCount)
	}
	var shape groupingGroupShape
	if err := json.Unmarshal(group.Typed, &shape); err != nil {
		t.Fatalf("decode group typed_shape: %v", err)
	}
	if shape.StatementOrdinal != 1 {
		t.Fatalf("representative ordinal = %d, want 1", shape.StatementOrdinal)
	}
	wantRanges := []statementOrdinalRange{{Start: 1, End: 3}}
	if !reflect.DeepEqual(shape.StatementOrdinalRanges, wantRanges) {
		t.Fatalf("statement ordinal ranges = %+v, want %+v", shape.StatementOrdinalRanges, wantRanges)
	}
	if !shape.MetadataContextComplete {
		t.Fatal("complete statement contexts must produce a complete group context")
	}
	if shape.RepresentativeAnonymizedSQL == "" ||
		!strings.Contains(shape.RepresentativeAnonymizedSQL, statementTables[1]) {
		t.Fatalf("representative SQL/token mismatch: sql=%q tokens=%v",
			shape.RepresentativeAnonymizedSQL, statementTables)
	}
	groupTables := map[string]fingerprint.ObjectReference{}
	for _, object := range shape.Objects {
		if object.Kind == "table" && object.Relation == fingerprint.RelationRead {
			groupTables[object.Token] = object
		}
	}
	alphaRef, alphaOK := groupTables[statementTables[1]]
	betaRef, betaOK := groupTables[statementTables[2]]
	wantAlphaRanges := []fingerprint.StatementOrdinalRange{{Start: 1, End: 1}, {Start: 3, End: 3}}
	if !alphaOK || alphaRef.StatementOrdinal != 1 ||
		!reflect.DeepEqual(alphaRef.StatementOrdinalRanges, wantAlphaRanges) {
		t.Fatalf("alpha object occurrences = %+v, want first ordinal 1 and ranges %+v",
			alphaRef, wantAlphaRanges)
	}
	wantBetaRanges := []fingerprint.StatementOrdinalRange{{Start: 2, End: 2}}
	if !betaOK || betaRef.StatementOrdinal != 2 ||
		!reflect.DeepEqual(betaRef.StatementOrdinalRanges, wantBetaRanges) {
		t.Fatalf("beta object occurrences = %+v, want first ordinal 2 and ranges %+v",
			betaRef, wantBetaRanges)
	}
	for _, original := range []string{"missing_alpha", "missing_beta"} {
		if strings.Contains(strings.ToLower(string(group.Typed)), original) {
			t.Fatalf("group typed_shape leaked original %q: %s", original, group.Typed)
		}
	}

	reborn := privacy.NewRunMapper()
	agent := NewAgentRunner(b.svc, AgentDeps{})
	if err := agent.rehydrateIdentifierMapping(context.Background(), b.db, runID, reborn); err != nil {
		t.Fatalf("rehydrate grouping mapping: %v", err)
	}
	for ordinal, token := range statementTables {
		kind, original, ok := reborn.Original(token)
		want := "missing_alpha"
		if ordinal == 2 {
			want = "missing_beta"
		}
		if !ok || kind != privacy.KindTable || original != want {
			t.Fatalf("rehydrated token %s = (%s,%s,%v), want table/%s/true",
				token, kind, original, ok, want)
		}
	}
}

func TestGroupingRepresentativeStatementMatchesCanonicalAndDialect(t *testing.T) {
	b := newB7Fixture(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	schema := fmt.Sprintf("m3_dialect_%d", suffix)
	firstTable := fmt.Sprintf("m3_first_%d", suffix)
	secondTable := fmt.Sprintf("m3_second_%d", suffix)

	if err := b.db.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = b.db.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error
	})
	for _, table := range []string{firstTable, secondTable} {
		if err := b.db.Exec(`CREATE TABLE "` + schema + `"."` + table + `"
			(id BIGINT PRIMARY KEY)`).Error; err != nil {
			t.Fatal(err)
		}
		if err := b.db.Exec(`INSERT INTO "` + schema + `"."` + table + `" (id) VALUES (1)`).Error; err != nil {
			t.Fatal(err)
		}
	}

	runID := runGroupingEphemeral(t, b,
		"select id from "+firstTable+";\n"+
			"select /*+ MAX_EXECUTION_TIME(1000) */ id from "+secondTable+";\n",
		"grouping-canonical-dialect-"+runKeySuffix())

	var statements []struct {
		ID                   string
		Ordinal              int32
		CanonicalFingerprint string
		DialectFingerprint   string
	}
	if err := b.db.Raw(`
		select id::text as id, ordinal,
			canonical_fingerprint_hmac as canonical_fingerprint,
			dialect_fingerprint_hmac as dialect_fingerprint
		from sql_statements
		where review_run_id = ?
		order by ordinal`, runID).Scan(&statements).Error; err != nil {
		t.Fatal(err)
	}
	if len(statements) != 2 {
		t.Fatalf("statement rows = %d, want 2", len(statements))
	}
	if statements[0].CanonicalFingerprint != statements[1].CanonicalFingerprint {
		t.Fatalf("fixture canonical differs: %q != %q",
			statements[0].CanonicalFingerprint, statements[1].CanonicalFingerprint)
	}
	if statements[0].DialectFingerprint == statements[1].DialectFingerprint {
		t.Fatalf("fixture dialect did not differ for canonical %q",
			statements[0].CanonicalFingerprint)
	}
	statementsByOrdinal := make(map[int32]string, len(statements))
	for _, statement := range statements {
		statementsByOrdinal[statement.Ordinal] = statement.ID
	}

	var groups []struct {
		ID                    string
		CanonicalFingerprint  string
		DialectFingerprint    string
		RepresentativeID      string
		RepresentativeOrdinal int32
		Typed                 []byte
	}
	if err := b.db.Raw(`
		select g.id::text as id,
			g.canonical_fingerprint_hmac as canonical_fingerprint,
			g.dialect_fingerprint_hmac as dialect_fingerprint,
			g.representative_statement_id::text as representative_id,
			ss.ordinal as representative_ordinal,
			g.typed_shape::text as typed
		from sql_fingerprint_groups g
		join sql_statements ss on ss.id = g.representative_statement_id
		where g.review_run_id = ?
		order by (g.typed_shape->>'statement_ordinal')::int`, runID).Scan(&groups).Error; err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("fingerprint groups = %d, want 2", len(groups))
	}
	seenRepresentatives := map[string]bool{}
	for i, group := range groups {
		var shape groupingGroupShape
		if err := json.Unmarshal(group.Typed, &shape); err != nil {
			t.Fatalf("decode group %d typed_shape: %v", i, err)
		}
		wantOrdinal := int32(i + 1)
		if shape.StatementOrdinal != wantOrdinal || group.RepresentativeOrdinal != wantOrdinal {
			t.Fatalf("group %d representatives: typed=%d joined=%d, want %d",
				i, shape.StatementOrdinal, group.RepresentativeOrdinal, wantOrdinal)
		}
		wantID := statementsByOrdinal[wantOrdinal]
		if group.RepresentativeID != wantID {
			t.Fatalf("group %d representative id = %s, want %s",
				i, group.RepresentativeID, wantID)
		}
		statement := statements[wantOrdinal-1]
		if group.CanonicalFingerprint != statement.CanonicalFingerprint ||
			group.DialectFingerprint != statement.DialectFingerprint {
			t.Fatalf("group %d fingerprint tuple does not match representative %d", i, wantOrdinal)
		}
		if seenRepresentatives[group.RepresentativeID] {
			t.Fatalf("different dialect groups share representative %s", group.RepresentativeID)
		}
		seenRepresentatives[group.RepresentativeID] = true
	}

	if err := b.db.Exec(`UPDATE datasources
		SET engine = 'postgresql', compatibility_mode = 'postgresql', version_constraint = '16.x'
		WHERE id IN (SELECT datasource_id FROM flow_stages WHERE flow_id = ?)`,
		b.flowID).Error; err != nil {
		t.Fatal(err)
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
	wantTables := []string{firstTable, secondTable}
	for i, group := range groups {
		statement, err := source.representativeStatementSQL(ctx, group.ID)
		if err != nil {
			t.Fatalf("resolve group %d representative: %v", i, err)
		}
		if !strings.Contains(strings.ToLower(statement), wantTables[i]) {
			t.Fatalf("group %d representative SQL = %q, want table %s",
				i, statement, wantTables[i])
		}
		fact, err := source.SafeExplain(ctx, schema, group.ID)
		if err != nil {
			t.Fatalf("safe_explain group %d: %v", i, err)
		}
		if fact["estimate"] != true || fact["access_method"] == "" {
			t.Fatalf("group %d real explain fact = %#v", i, fact)
		}
	}
}

// TestGroupingMapperAndRowsRollBackWithRunTransaction RCP-20261101 equivalent migration:
// the legacy version injected a chained ai_review task insertion failure inside the sql_grouping task
// transaction and asserted zero residual grouping rows; on the ephemeral path grouping completes in
// memory and the sole persistence point is the materialization transaction — so this test now injects
// a fingerprint group insertion failure inside the materialization transaction and asserts that Run /
// mapping / statements / groups / snapshots all roll back (submit = all-or-nothing). The chained task
// insertion vector was retired along with the task chain.
func TestGroupingMapperAndRowsRollBackWithRunTransaction(t *testing.T) {
	b := newB7Fixture(t)
	sqlText := "select id from missing_alpha;\n"
	draft := b.createDraftWithSQL(t, sqlText)
	er, runID := b.startEphemeralTerminal(t, b.owner, b.fakeDial(emptyCompleteProviderBody), EphemeralStartInput{
		FlowID:         b.flowID,
		DraftID:        draft.ID,
		SQL:            []byte(sqlText),
		IdempotencyKey: "grouping-rollback-" + runKeySuffix(),
	})

	callbackName := "test:fail_materialize_group_insert"
	if err := b.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "sql_fingerprint_groups" {
			tx.AddError(errors.New("injected materialize group insert failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = b.db.Callback().Create().Remove(callbackName)
	})

	err := b.db.Transaction(func(tx *gorm.DB) error {
		return b.svc.MaterializeEphemeralRunTx(context.Background(), tx, er, draft.ID, draft.Revision)
	})
	if err == nil {
		t.Fatal("materialize must fail when the group insert fails")
	}
	for label, query := range map[string]string{
		"run":        `select count(*) from review_runs where id = ?`,
		"mapping":    `select count(*) from review_identifier_mappings where review_run_id = ?`,
		"statements": `select count(*) from sql_statements where review_run_id = ?`,
		"groups":     `select count(*) from sql_fingerprint_groups where review_run_id = ?`,
		"snapshots":  `select count(*) from stage_review_snapshots where review_run_id = ?`,
	} {
		if n := count(t, b.db, query, runID); n != 0 {
			t.Fatalf("%s rows remained after transaction rollback: %d", label, n)
		}
	}
}
