//go:build integration

package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

const (
	metadataContextSchema  = "r012_private_schema"
	metadataContextTable   = "r012_private_table"
	metadataContextTable2  = "r012_private_table_two"
	metadataContextColumn  = "r012_private_column"
	metadataContextAlias   = "r012_private_alias"
	metadataContextLiteral = "r012_literal_marker"
	metadataContextComment = "r012_comment_marker"
)

// runMetadataContextReview drives an ephemeral precheck (RCP-20261101): StartEphemeralRun
// -> wait for terminal -> materialize; legacy DB row assertions (run state / stage reasons / findings) work as usual after materialization.
func (b *b7fx) runMetadataContextReview(
	t *testing.T, deps AgentDeps, sqlText string,
) string {
	t.Helper()
	draft := b.createDraftWithSQL(t, sqlText)
	er, runID := b.startEphemeralTerminal(t, b.owner, deps, EphemeralStartInput{
		FlowID:         b.flowID,
		DraftID:        draft.ID,
		SQL:            []byte(sqlText),
		IdempotencyKey: "r012-context-" + runKeySuffix(),
	})
	b.materializeEphemeral(t, er, draft.ID, draft.Revision)
	return runID
}

func emptyCompleteProviderBody(req provider.Request) ([]byte, int, error) {
	body := map[string]any{
		"schema_version": 1,
		"review_run_id":  req.ReviewRunID,
		"outcome":        "complete",
		"summary":        "Review complete.",
		"findings":       []any{},
		"tool_requests":  []any{},
	}
	raw, err := json.Marshal(body)
	return raw, 200, err
}

type metadataContextFactSource struct {
	*fakeFactSource
	fact       map[string]any
	err        error
	tableCalls int
	gotSchema  string
	gotTable   string
}

func (s *metadataContextFactSource) TableMetadata(
	_ context.Context, schema, table string,
) (map[string]any, error) {
	s.tableCalls++
	s.gotSchema = schema
	s.gotTable = table
	if s.err != nil {
		return nil, s.err
	}
	fact := make(map[string]any, len(s.fact)+2)
	for key, value := range s.fact {
		fact[key] = value
	}
	// Mirror the real FactSource: scoped metadata is self-describing so the
	// runner can bind an observed fact to the exact requested object.
	fact["schema"] = schema
	fact["table"] = table
	return fact, nil
}

func (b *b7fx) metadataContextDeps(
	source *metadataContextFactSource,
	respond func(provider.Request) ([]byte, int, error),
) AgentDeps {
	return AgentDeps{
		Dial: func(ep provider.Endpoint) (provider.Client, error) {
			return provider.NewLocalFakeAdapter(ep, respond), nil
		},
		FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
			return source, func() {}, nil
		},
	}
}

// configureMetadataContextScope points the fixture datasource default database to the private schema
// (RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: under unmapped semantics, the stage allowed scope = datasources.database_name).
func (b *b7fx) configureMetadataContextScope(t *testing.T) {
	t.Helper()
	if err := b.db.Exec(`update datasources set database_name = ? where id in
		(select datasource_id from flow_stages where flow_id = ?)`,
		metadataContextSchema, b.flowID).Error; err != nil {
		t.Fatalf("point datasource default database at the private schema: %v", err)
	}
}

type metadataContextGroup struct {
	RepresentativeAnonymizedSQL string                           `json:"representative_anonymized_sql"`
	StatementOrdinal            int                              `json:"statement_ordinal"`
	StatementOrdinalRanges      []provider.StatementOrdinalRange `json:"statement_ordinal_ranges"`
	Objects                     []map[string]any                 `json:"objects"`
}

func metadataContextFirstGroup(req provider.Request) (metadataContextGroup, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return metadataContextGroup{}, err
	}
	var envelope struct {
		Groups []metadataContextGroup `json:"anonymized_statement_groups"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return metadataContextGroup{}, err
	}
	if len(envelope.Groups) == 0 {
		return metadataContextGroup{}, errors.New("provider request has no statement group")
	}
	return envelope.Groups[0], nil
}

func metadataContextObjectsOfKind(group metadataContextGroup, kind string) []map[string]any {
	var out []map[string]any
	for _, object := range group.Objects {
		got, _ := object["kind"].(string)
		if strings.EqualFold(got, kind) {
			out = append(out, object)
		}
	}
	return out
}

func metadataContextSchemaToken(group metadataContextGroup, table map[string]any) string {
	if token, _ := table["schema_token"].(string); token != "" {
		return token
	}
	for _, object := range metadataContextObjectsOfKind(group, "schema") {
		if token, _ := object["token"].(string); token != "" {
			return token
		}
	}
	return ""
}

func metadataContextScopedToolTurn(
	req provider.Request, tableIndex int,
) ([]byte, int, error) {
	group, err := metadataContextFirstGroup(req)
	if err != nil {
		return nil, 500, err
	}
	tables := metadataContextObjectsOfKind(group, "table")
	if tableIndex < 0 || tableIndex >= len(tables) {
		return nil, 500, fmt.Errorf("table object %d missing (objects=%d)", tableIndex, len(tables))
	}
	tableToken, _ := tables[tableIndex]["token"].(string)
	schemaToken := metadataContextSchemaToken(group, tables[tableIndex])
	if schemaToken == "" || tableToken == "" {
		return nil, 500, errors.New("scoped metadata tool tokens missing")
	}
	body := map[string]any{
		"schema_version": 1,
		"review_run_id":  req.ReviewRunID,
		"outcome":        "complete",
		"summary":        "Continue reading required database context.",
		"findings":       []any{},
		"tool_requests": []map[string]any{{
			"tool_call_id": "9f0b1e2c-0000-4000-8000-000000000121",
			"tool_name":    "schema_metadata",
			"arguments": map[string]any{
				"schema": schemaToken,
				"table":  tableToken,
			},
		}},
	}
	raw, err := json.Marshal(body)
	return raw, 200, err
}

func assertNoMetadataContextLeak(t *testing.T, bodies [][]byte) {
	t.Helper()
	for index, body := range bodies {
		for _, forbidden := range []string{
			metadataContextSchema,
			metadataContextTable,
			metadataContextTable2,
			metadataContextColumn,
			metadataContextAlias,
			metadataContextLiteral,
			metadataContextComment,
		} {
			if strings.Contains(strings.ToLower(string(body)), forbidden) {
				t.Fatalf("provider request %d leaked raw marker %q: %s", index+1, forbidden, body)
			}
		}
	}
}

func metadataContextRunState(t *testing.T, b *b7fx, runID string) (string, *int32) {
	t.Helper()
	var state string
	var code *int32
	if err := b.db.Raw(`select state, business_err_code from review_runs where id = ?`, runID).
		Row().Scan(&state, &code); err != nil {
		t.Fatalf("read run state: %v", err)
	}
	return state, code
}

func metadataContextStageReasons(t *testing.T, b *b7fx, runID string) (string, string) {
	t.Helper()
	var state, reasons string
	if err := b.db.Raw(`select state, gate_reason_codes::text
		from stage_review_snapshots where review_run_id = ? limit 1`, runID).
		Row().Scan(&state, &reasons); err != nil {
		t.Fatalf("read stage state and reasons: %v", err)
	}
	return state, reasons
}

func expectMetadataContextPartial(t *testing.T, b *b7fx, runID string) {
	t.Helper()
	state, code := metadataContextRunState(t, b, runID)
	if state != string(domain.RunStatePartial) || code == nil || *code != 2007 {
		gotCode := "nil"
		if code != nil {
			gotCode = fmt.Sprint(*code)
		}
		t.Fatalf("incomplete metadata context = %s/%s, want partial/2007", state, gotCode)
	}
	stageState, reasons := metadataContextStageReasons(t, b, runID)
	if stageState != string(domain.StageStatePartial) {
		t.Fatalf("incomplete metadata stage state = %s, want partial", stageState)
	}
	if !strings.Contains(reasons, "evidence_incomplete") {
		t.Fatalf("evidence_incomplete reason missing: %s", reasons)
	}
}

func TestR012TableMetadataBaselineCannotBeSkippedByEmptyComplete(t *testing.T) {
	b := newB7Fixture(t)
	deps := b.fakeDial(emptyCompleteProviderBody)
	runID := b.runMetadataContextReview(
		t, deps, "update orders set amount = amount + 1 where id = 1;\n")

	expectMetadataContextPartial(t, b, runID)
}

func TestR012PureLexicalNoTableContextDoesNotRequireDatabase(t *testing.T) {
	b := newB7Fixture(t)
	deps := b.fakeDial(emptyCompleteProviderBody)
	runID := b.runMetadataContextReview(t, deps, "select 1;\n")

	state, code := metadataContextRunState(t, b, runID)
	if state != string(domain.RunStateReady) || code != nil {
		t.Fatalf("no-object lexical review = %s/%v, want ready without error", state, code)
	}
}

func TestR012ProviderContextIsAnonymousAndObjectBound(t *testing.T) {
	b := newB7Fixture(t)
	b.configureMetadataContextScope(t)
	var providerBodies [][]byte
	deps := b.metadataContextDeps(
		&metadataContextFactSource{fakeFactSource: &fakeFactSource{}},
		func(req provider.Request) ([]byte, int, error) {
			raw, err := json.Marshal(req)
			if err != nil {
				return nil, 500, err
			}
			providerBodies = append(providerBodies, raw)
			return emptyCompleteProviderBody(req)
		},
	)
	runID := b.runMetadataContextReview(t, deps, fmt.Sprintf(
		"update %s.%s %s set %s = '%s' /* %s */ where %s.id = 42;\n",
		metadataContextSchema, metadataContextTable, metadataContextAlias,
		metadataContextColumn, metadataContextLiteral, metadataContextComment,
		metadataContextAlias,
	))
	expectMetadataContextPartial(t, b, runID)
	if len(providerBodies) == 0 {
		t.Fatal("provider was not called")
	}
	assertNoMetadataContextLeak(t, providerBodies)

	var parsed struct {
		Groups []metadataContextGroup `json:"anonymized_statement_groups"`
	}
	if err := json.Unmarshal(providerBodies[0], &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Groups) != 1 {
		t.Fatalf("provider groups = %d, want 1", len(parsed.Groups))
	}
	first := parsed.Groups[0]
	if first.RepresentativeAnonymizedSQL == "" {
		t.Fatal("representative_anonymized_sql is missing")
	}
	if !strings.Contains(first.RepresentativeAnonymizedSQL, "schema_") ||
		!strings.Contains(first.RepresentativeAnonymizedSQL, "table_") {
		t.Fatalf("anonymous SQL is not object-bound: %s", first.RepresentativeAnonymizedSQL)
	}
	if first.StatementOrdinal != 1 {
		t.Fatalf("statement_ordinal = %d, want 1", first.StatementOrdinal)
	}
	if len(first.StatementOrdinalRanges) != 1 ||
		first.StatementOrdinalRanges[0].Start != 1 ||
		first.StatementOrdinalRanges[0].End != 1 {
		t.Fatalf("statement_ordinal_ranges = %+v, want 1..1",
			first.StatementOrdinalRanges)
	}
	for _, kind := range []string{"schema", "table", "column", "alias"} {
		if len(metadataContextObjectsOfKind(first, kind)) == 0 {
			t.Fatalf("%s object relation missing: %#v", kind, first.Objects)
		}
	}
}

func TestR012ScopedMetadataCompletenessControlsRunOutcome(t *testing.T) {
	tests := []struct {
		name      string
		fact      map[string]any
		factErr   error
		wantState string
	}{
		{
			name: "observed present",
			fact: map[string]any{
				"existence": "present", "metadata_complete": true, "truncated": false,
				"columns": []any{
					map[string]any{"column_name": metadataContextColumn, "type_class": "integer"},
					map[string]any{"column_name": "id", "type_class": "integer"},
				},
			},
			wantState: "ready",
		},
		{
			name: "observed absent is complete",
			fact: map[string]any{
				"existence": "absent", "metadata_complete": true, "truncated": false,
				"columns": []any{},
			},
			wantState: "blocked",
		},
		{
			name:    "tool failure",
			factErr: errors.New("metadata read failed"),
		},
		{
			name: "unknown",
			fact: map[string]any{
				"existence": "unknown", "metadata_complete": true, "truncated": false,
			},
		},
		{
			name: "truncated",
			fact: map[string]any{
				"existence": "present", "metadata_complete": false, "truncated": true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newB7Fixture(t)
			b.configureMetadataContextScope(t)
			source := &metadataContextFactSource{
				fakeFactSource: &fakeFactSource{},
				fact:           tt.fact,
				err:            tt.factErr,
			}
			var providerBodies [][]byte
			turn := 0
			deps := b.metadataContextDeps(source, func(req provider.Request) ([]byte, int, error) {
				raw, err := json.Marshal(req)
				if err != nil {
					return nil, 500, err
				}
				providerBodies = append(providerBodies, raw)
				turn++
				if turn == 1 {
					return metadataContextScopedToolTurn(req, 0)
				}
				return emptyCompleteProviderBody(req)
			})
			runID := b.runMetadataContextReview(t, deps, fmt.Sprintf(
				"update %s.%s set %s = 1 where id = 1;\n",
				metadataContextSchema, metadataContextTable, metadataContextColumn))
			if source.tableCalls != 1 {
				t.Fatalf("scoped metadata calls = %d, want 1", source.tableCalls)
			}
			if source.gotSchema != metadataContextSchema || source.gotTable != metadataContextTable {
				t.Fatalf("scoped metadata target = %s.%s", source.gotSchema, source.gotTable)
			}
			assertNoMetadataContextLeak(t, providerBodies)
			if tt.wantState != "" {
				state, code := metadataContextRunState(t, b, runID)
				if state != tt.wantState || (tt.wantState == "ready" && code != nil) ||
					(tt.wantState == "blocked" && (code == nil || *code != 2008)) {
					t.Fatalf("complete metadata review = %s/%v, want %s", state, code, tt.wantState)
				}
				return
			}
			expectMetadataContextPartial(t, b, runID)
		})
	}
}

func TestR012EveryReferencedTableNeedsAnObservedBaseline(t *testing.T) {
	b := newB7Fixture(t)
	b.configureMetadataContextScope(t)
	source := &metadataContextFactSource{
		fakeFactSource: &fakeFactSource{},
		fact: map[string]any{
			"existence": "present", "metadata_complete": true, "truncated": false,
			"columns": []any{map[string]any{
				"column_name": metadataContextColumn, "type_class": "integer",
			}},
		},
	}
	turn := 0
	deps := b.metadataContextDeps(source, func(req provider.Request) ([]byte, int, error) {
		turn++
		if turn == 1 {
			group, err := metadataContextFirstGroup(req)
			if err != nil {
				return nil, 500, err
			}
			if tables := metadataContextObjectsOfKind(group, "table"); len(tables) < 2 {
				return nil, 500, fmt.Errorf("provider context has %d table objects, want 2", len(tables))
			}
			return metadataContextScopedToolTurn(req, 0)
		}
		return emptyCompleteProviderBody(req)
	})
	runID := b.runMetadataContextReview(t, deps, fmt.Sprintf(
		"select one.id from %s.%s one join %s.%s two on one.id = two.id where two.%s > 0;\n",
		metadataContextSchema, metadataContextTable,
		metadataContextSchema, metadataContextTable2,
		metadataContextColumn,
	))
	if source.tableCalls != 1 {
		t.Fatalf("scoped metadata calls = %d, want only the first of 2 tables", source.tableCalls)
	}
	expectMetadataContextPartial(t, b, runID)
}

func TestR012CoverageIncompleteTakesPrecedenceOverHighFinding(t *testing.T) {
	b := newB7Fixture(t)
	deps := b.metadataContextDeps(
		&metadataContextFactSource{fakeFactSource: &fakeFactSource{}},
		func(req provider.Request) ([]byte, int, error) {
			return b.validProviderBody(req.AnonymizedStatementGroups[0].GroupID, false)(req)
		},
	)
	runID := b.runMetadataContextReview(
		t, deps, "update orders set amount = amount + 1 where id = 1;\n")

	// The model returned a high finding without observing the required table
	// baseline. The result must preserve the finding but classify the run as
	// coverage-incomplete, not as a fully evaluated risk result.
	expectMetadataContextPartial(t, b, runID)
	var highFindingCount int
	if err := b.db.Raw(`select count(*) from review_findings
		where stage_review_id in (
			select id from stage_review_snapshots where review_run_id = ?
		) and severity in ('high', 'critical')`, runID).Scan(&highFindingCount).Error; err != nil {
		t.Fatal(err)
	}
	if highFindingCount == 0 {
		t.Fatal("incomplete review must retain the observed high finding")
	}
}
