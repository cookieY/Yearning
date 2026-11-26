package application

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	assistanttool "github.com/cookieY/YearningX/backend/internal/assistant/tool"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

func hasErrCode(err error, code int) bool {
	if err == nil {
		return false
	}
	var domErr *domain.Error
	if errors.As(err, &domErr) {
		return domErr.Code == code
	}
	return false
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDeterministicSynthesizerAllBranches(t *testing.T) {
	d := &DeterministicSynthesizer{}
	ctx := context.Background()

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.Synthesize(cancelCtx, SynthesizeInput{}); err == nil {
		t.Fatal("expected error on canceled context in Synthesize")
	}
	if _, _, _, err := d.Summarize(cancelCtx, nil); err == nil {
		t.Fatal("expected error on canceled context in Summarize")
	}

	// Out of scope in English and default locale
	for _, loc := range []string{"en-US", "zh-CN"} {
		out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:       domain.CapabilityPlatformHelp,
			OutputLocale:     loc,
			AnonymizedPrompt: "write a poem about cooking pasta",
		})
		if err != nil || !out.OutOfScope {
			t.Fatalf("expected out of scope for locale %s, got %+v err=%v", loc, out, err)
		}
	}

	// Platform help with OnDelta
	var deltas []string
	outHelp, err := d.Synthesize(ctx, SynthesizeInput{
		Capability:       domain.CapabilityPlatformHelp,
		OutputLocale:     "en-US",
		AnonymizedPrompt: "How does change order approval work?",
		OnDelta: func(kind, delta string) {
			deltas = append(deltas, kind+":"+delta)
		},
	})
	if err != nil || outHelp == nil || len(deltas) == 0 {
		t.Fatalf("unexpected platform help output: %+v err=%v deltas=%v", outHelp, err, deltas)
	}

	// Production ProviderSynthesizer (Fallback == nil) fails closed with 2011 AI_PROVIDER_FAILED when no active AI provider exists.
	prodPS := NewProviderSynthesizer(nil, nil, nil)
	if _, err := prodPS.Synthesize(ctx, SynthesizeInput{
		Capability:       domain.CapabilityPlatformHelp,
		AnonymizedPrompt: "How does change order approval work?",
	}); !hasErrCode(err, domain.CodeAIProviderFailed) {
		t.Fatalf("expected 2011 AI_PROVIDER_FAILED when no active AI provider is configured, got %v", err)
	}

	// SQL Generation without metadata (clarify)
	for _, loc := range []string{"en-US", "zh-CN"} {
		out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:       domain.CapabilitySQLGenerate,
			OutputLocale:     loc,
			AnonymizedPrompt: "select from table_1",
		})
		if err != nil || !out.NeedsClarify {
			t.Fatalf("expected NeedsClarify without metadata for %s, got %+v err=%v", loc, out, err)
		}
	}

	meta := []*assistanttool.AnonymizedTableDescription{
		{
			SchemaToken: "schema_1",
			TableToken:  "table_1",
			Columns: []assistanttool.AnonymizedColumnFact{
				{ColumnToken: "column_1", Ordinal: 1, DataType: "bigint", Nullable: false, DefaultBehavior: assistanttool.DefaultAutoIncrement},
				{ColumnToken: "column_2", Ordinal: 2, DataType: "varchar(64)", Nullable: false, DefaultBehavior: assistanttool.DefaultNone},
				{ColumnToken: "column_3", Ordinal: 3, DataType: "varchar(64)", Nullable: true, DefaultBehavior: assistanttool.DefaultServerDefault},
				{ColumnToken: "column_4", Ordinal: 4, DataType: "int", Nullable: true},
				{ColumnToken: "column_5", Ordinal: 5, DataType: "int", Nullable: true},
				{ColumnToken: "column_6", Ordinal: 6, DataType: "int", Nullable: true},
				{ColumnToken: "column_7", Ordinal: 7, DataType: "int", Nullable: true},
				{ColumnToken: "column_8", Ordinal: 8, DataType: "int", Nullable: true},
				{ColumnToken: "column_9", Ordinal: 9, DataType: "int", Nullable: true},
			},
			Indexes: []assistanttool.AnonymizedIndexFact{
				{IndexToken: "index_1", Primary: true, Unique: true, ColumnTokens: []string{"column_1"}},
			},
		},
	}

	for _, loc := range []string{"en-US", "zh-CN"} {
		// INSERT clarify (no bound placeholders for required column_2)
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:         domain.CapabilitySQLGenerate,
			OutputLocale:       loc,
			AnonymizedPrompt:   "insert into table_1",
			AnonymizedMetadata: meta,
		}); err != nil || !out.NeedsClarify {
			t.Fatalf("expected INSERT clarify for %s, got %+v err=%v", loc, out, err)
		}

		// INSERT valid
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:         domain.CapabilitySQLGenerate,
			OutputLocale:       loc,
			AnonymizedPrompt:   "insert into table_1",
			AnonymizedMetadata: meta,
			BoundPlaceholders:  []string{":val_str_1", ":val_str_2"},
		}); err != nil || !strings.HasPrefix(out.AnonymizedSQL, "INSERT INTO") {
			t.Fatalf("expected INSERT SQL for %s, got %+v err=%v", loc, out, err)
		}

		// UPDATE clarify (no bound placeholders)
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:         domain.CapabilitySQLGenerate,
			OutputLocale:       loc,
			AnonymizedPrompt:   "update table_1",
			AnonymizedMetadata: meta,
		}); err != nil || !out.NeedsClarify {
			t.Fatalf("expected UPDATE clarify for %s, got %+v err=%v", loc, out, err)
		}

		// UPDATE valid
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:         domain.CapabilitySQLGenerate,
			OutputLocale:       loc,
			AnonymizedPrompt:   "update table_1",
			AnonymizedMetadata: meta,
			BoundPlaceholders:  []string{":val_str_1", ":val_int_1"},
		}); err != nil || !strings.HasPrefix(out.AnonymizedSQL, "UPDATE") {
			t.Fatalf("expected UPDATE SQL for %s, got %+v err=%v", loc, out, err)
		}

		// DELETE clarify
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:         domain.CapabilitySQLGenerate,
			OutputLocale:       loc,
			AnonymizedPrompt:   "delete from table_1",
			AnonymizedMetadata: meta,
		}); err != nil || !out.NeedsClarify {
			t.Fatalf("expected DELETE clarify for %s, got %+v err=%v", loc, out, err)
		}

		// DELETE valid
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:         domain.CapabilitySQLGenerate,
			OutputLocale:       loc,
			AnonymizedPrompt:   "delete from table_1",
			AnonymizedMetadata: meta,
			BoundPlaceholders:  []string{":val_int_1"},
		}); err != nil || !strings.HasPrefix(out.AnonymizedSQL, "DELETE FROM") {
			t.Fatalf("expected DELETE SQL for %s, got %+v err=%v", loc, out, err)
		}

		// CREATE INDEX valid
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:         domain.CapabilitySQLGenerate,
			OutputLocale:       loc,
			AnonymizedPrompt:   "create index on table_1",
			AnonymizedMetadata: meta,
		}); err != nil || !strings.HasPrefix(out.AnonymizedSQL, "CREATE INDEX") {
			t.Fatalf("expected CREATE INDEX SQL for %s, got %+v err=%v", loc, out, err)
		}

		// ALTER TABLE valid
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:         domain.CapabilitySQLGenerate,
			OutputLocale:       loc,
			AnonymizedPrompt:   "alter table table_1",
			AnonymizedMetadata: meta,
		}); err != nil || !strings.HasPrefix(out.AnonymizedSQL, "ALTER TABLE") {
			t.Fatalf("expected ALTER TABLE SQL for %s, got %+v err=%v", loc, out, err)
		}

		// SELECT with bound placeholders and without bound placeholders
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:         domain.CapabilitySQLGenerate,
			OutputLocale:       loc,
			AnonymizedPrompt:   "query table_1",
			AnonymizedMetadata: meta,
			BoundPlaceholders:  []string{":val_str_1"},
		}); err != nil || !strings.Contains(out.AnonymizedSQL, "WHERE") {
			t.Fatalf("expected SELECT with WHERE for %s, got %+v err=%v", loc, out, err)
		}
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:         domain.CapabilitySQLGenerate,
			OutputLocale:       loc,
			AnonymizedPrompt:   "query table_1",
			AnonymizedMetadata: []*assistanttool.AnonymizedTableDescription{{TableToken: "table_1"}},
		}); err != nil || !strings.Contains(out.AnonymizedSQL, "SELECT 1 FROM table_1") {
			t.Fatalf("expected SELECT 1 fallback for %s, got %+v err=%v", loc, out, err)
		}

		// SQL Optimization: clarify when empty input SQL
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:       domain.CapabilitySQLOptimize,
			OutputLocale:     loc,
			AnonymizedPrompt: "optimize query",
		}); err != nil || !out.NeedsClarify {
			t.Fatalf("expected optimize clarify for %s, got %+v err=%v", loc, out, err)
		}

		// SQL Optimization: SELECT * without LIMIT + plan text
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:         domain.CapabilitySQLOptimize,
			OutputLocale:       loc,
			AnonymizedPrompt:   "optimize query",
			AnonymizedInputSQL: "SELECT * FROM table_1 WHERE column_2 = :val_str_1",
			AnonymizedPlanText: "Seq Scan on table_1",
			AnonymizedMetadata: meta,
		}); err != nil || !strings.Contains(out.AnonymizedSQL, "LIMIT 100") || strings.Contains(out.AnonymizedSQL, "SELECT *") {
			t.Fatalf("expected rewritten SELECT without * and with LIMIT 100 for %s, got %+v err=%v", loc, out, err)
		}

		// SQL Optimization: already has explicit columns and LIMIT (triggers len(reasons) == 0 branch)
		if out, err := d.Synthesize(ctx, SynthesizeInput{
			Capability:         domain.CapabilitySQLOptimize,
			OutputLocale:       loc,
			AnonymizedPrompt:   "optimize query",
			AnonymizedInputSQL: "SELECT column_1 FROM table_1 LIMIT 10",
			AnonymizedMetadata: meta,
		}); err != nil || len(out.Assumptions) == 0 {
			t.Fatalf("expected fallback optimization reason for %s, got %+v err=%v", loc, out, err)
		}
	}

	// Summarize with long turn content (> 120 chars)
	longContent := strings.Repeat("select column_1 from table_1 ", 10)
	sum, inTok, outTok, err := d.Summarize(ctx, []AnonymizedTurn{
		{Sequence: 1, Role: "user", Capability: "sql_generate", Content: longContent},
	})
	if err != nil || sum == "" || inTok == 0 || outTok == 0 || !strings.Contains(sum, "...") {
		t.Fatalf("unexpected Summarize output: sum=%q in=%d out=%d err=%v", sum, inTok, outTok, err)
	}

	// ExtractTablesFromSQL branches
	pg := fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}
	tbls := ExtractTablesFromSQL(`SELECT * FROM "schema_a"."tbl_a" JOIN tbl_b ON tbl_a.id = tbl_b.id`, pg)
	if len(tbls) != 2 {
		t.Fatalf("expected 2 tables from ExtractTablesFromSQL, got %v", tbls)
	}
}
