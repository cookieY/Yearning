package application

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/persistence"
	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	assistanttool "github.com/cookieY/YearningX/backend/internal/assistant/tool"
	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	reviewdomain "github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

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

func TestProviderSynthesizerAndJSONHelpers(t *testing.T) {
	ctx := context.Background()

	// JSON helper functions
	if _, err := parseSynthesizeOutput("   ", domain.CapabilitySQLGenerate); err == nil {
		t.Fatal("expected error on empty content")
	}
	truncJSON := `{"out_of_scope": false, "needs_clarify": true, "session_title": "MyTitle", "assistant_reply": "Line1\nLine2\r\t\"\\\/\x", "anonymized_sql": "SELECT 1", "assumptions": ["a1", "a2"], "next_steps": ["n1"], "semantic_warnings": ["w1"], "cited_article_ids": ["c1"`
	parsedOut, err := parseSynthesizeOutput(truncJSON, domain.CapabilitySQLGenerate)
	if err != nil || !parsedOut.NeedsClarify || parsedOut.SessionTitle != "MyTitle" || len(parsedOut.Assumptions) != 2 {
		t.Fatalf("unexpected truncated JSON parse output: %+v err=%v", parsedOut, err)
	}
	mdOut, err := parseSynthesizeOutput("### Plain Markdown Reply", domain.CapabilityPlatformHelp)
	if err != nil || mdOut.AssistantReply != "### Plain Markdown Reply" {
		t.Fatalf("unexpected plain markdown parse output: %+v err=%v", mdOut, err)
	}

	if UnwrapAssistantReplyJSON("plain text") != "plain text" {
		t.Fatal("expected plain text unchanged")
	}
	if UnwrapAssistantReplyJSON(`{"assistant_reply": "unwrapped"}`) != "unwrapped" {
		t.Fatal("expected valid JSON unwrapped")
	}
	if UnwrapAssistantReplyJSON(`{"assistant_reply": "partial_unwrapped"`) != "partial_unwrapped" {
		t.Fatal("expected partial JSON unwrapped")
	}
	if UnwrapAssistantReplyJSON(`{"assistant_reply": ""}`) != `{"assistant_reply": ""}` {
		t.Fatal("expected empty assistant_reply to return raw")
	}
	if stripMarkdownSQLFence("```sql\nSELECT 1\n```") != "SELECT 1" {
		t.Fatal("expected SQL fence stripped")
	}

	// ProviderSynthesizer with DB endpoints and custom HTTP transport
	fx := newAssistantFixture(t)
	ps := NewProviderSynthesizer(fx.db, fx.crypter, nil)

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ps.Synthesize(cancelCtx, SynthesizeInput{}); err == nil {
		t.Fatal("expected error on canceled context")
	}

	if _, _, _, err := ps.Summarize(ctx, []AnonymizedTurn{{Sequence: 1, Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	var nilPS *ProviderSynthesizer
	if eps, err := nilPS.loadActiveEndpoints(ctx); err != nil || len(eps) != 0 {
		t.Fatalf("expected nil endpoints on nil ProviderSynthesizer, got %v %v", eps, err)
	}
	if _, err := ps.Synthesize(ctx, SynthesizeInput{Capability: domain.CapabilityPlatformHelp, AnonymizedPrompt: "how to submit order"}); !hasErrCode(err, domain.CodeAIProviderFailed) {
		t.Fatalf("expected 2011 AI_PROVIDER_FAILED when no active AI provider is configured and Fallback is nil, got %v", err)
	}

	// Insert two ai_providers: one with bad secret ciphertext (skipped), one with valid secret ciphertext
	badProvID := uuid.NewString()
	if err := fx.db.Exec(`INSERT INTO ai_providers (id, name, provider_kind, base_url, model_name, enabled, selection_priority, privacy_contract_hash, output_schema_hash, configuration, secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'bad-prov', 'openai_compatible', 'https://ai.example.internal/v1', 'gpt-4o', true, 1, repeat('a', 64), repeat('b', 64), '{}'::jsonb, '\xdeadbeef', '\x0102030405060708090a0b0c', 'pay-1', 'aes-256-gcm', 1, ?, now(), now())`,
		badProvID, fx.userA.UserID).Error; err != nil {
		t.Fatalf("insert bad-prov: %v", err)
	}

	goodProvID := uuid.NewString()
	secEnv, err := fx.crypter.Seal(crypto.PurposePayload, []byte("sk-test-key"), crypto.AAD{
		DeploymentID: fx.crypter.DeploymentID(),
		Table:        "ai_providers",
		PrimaryKey:   goodProvID,
		Column:       "secret_ciphertext",
		Purpose:      "provider_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Exec(`INSERT INTO ai_providers (id, name, provider_kind, base_url, model_name, enabled, selection_priority, privacy_contract_hash, output_schema_hash, configuration, secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm, version, created_by_user_id, created_at, updated_at)
		VALUES (?, 'good-prov', 'openai_compatible', 'https://ai.example.internal/v1', 'gpt-4o', true, 2, repeat('a', 64), repeat('b', 64), '{"max_tokens": 8192, "temperature": 0.1, "reasoning_effort": "low"}'::jsonb, ?, ?, ?, 'aes-256-gcm', 1, ?, now(), now())`,
		goodProvID, secEnv.Ciphertext, secEnv.Nonce, secEnv.KeyID, fx.userA.UserID).Error; err != nil {
		t.Fatalf("insert good-prov: %v", err)
	}

	// Case 1: Non-streaming JSON response
	ps.HTTPClient = &provider.HTTPClient{
		Inner: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body := `{"choices":[{"message":{"reasoning_content":"thinking...","content":"` +
					`{\"resolved_capability\":\"sql_generate\",\"session_title\":\"Order Query\",\"assistant_reply\":\"Here is the query for table_1\",\"anonymized_sql\":\"SELECT column_1 FROM table_1 LIMIT 10\"}"}}],` +
					`"usage":{"input_tokens":50,"output_tokens":25}}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			}),
		},
	}
	var gotEvents []string
	out1, err := ps.Synthesize(ctx, SynthesizeInput{
		Capability:       domain.CapabilitySQLGenerate,
		AnonymizedPrompt: "query table_1",
		OnDelta: func(kind, delta string) {
			gotEvents = append(gotEvents, kind+":"+delta)
		},
	})
	if err != nil || out1 == nil || out1.InputTokens != 50 || out1.OutputTokens != 25 || len(gotEvents) < 2 {
		t.Fatalf("unexpected non-streaming Synthesize result: %+v err=%v events=%v", out1, err, gotEvents)
	}

	// Case 2: Streaming SSE response
	ps.HTTPClient = &provider.HTTPClient{
		Inner: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				sse := strings.Join([]string{
					`event: message`,
					`data: {"choices":[{"delta":{"reasoning_content":"step 1"}}]}`,
					`data: {"choices":[{"delta":{"content":"{\"assistant_reply\":\"Hello "}}]}`,
					`data: {"choices":[{"delta":{"content":"world\",\"anonymized_sql\":\"SELECT 1 FROM table_1\"}"}}]}`,
					`data: {"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":20}}`,
					`data: {"choices":[],"usage":{"input_tokens":41,"output_tokens":21}}`,
					`data: invalid-json-ignored`,
					`data: [DONE]`,
					``,
				}, "\n")
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(sse)),
				}, nil
			}),
		},
	}
	var sseEvents []string
	out2, err := ps.Synthesize(ctx, SynthesizeInput{
		Capability:       domain.CapabilitySQLGenerate,
		AnonymizedPrompt: "query table_1",
		OnDelta: func(kind, delta string) {
			sseEvents = append(sseEvents, kind+":"+delta)
		},
	})
	if err != nil || out2 == nil || out2.AssistantReply != "Hello world" || len(sseEvents) == 0 {
		t.Fatalf("unexpected SSE Synthesize result: %+v err=%v events=%v", out2, err, sseEvents)
	}

	// Case 3: HTTP 500 error from provider
	ps.HTTPClient = &provider.HTTPClient{
		Inner: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Header:     http.Header{},
					Body:       io.NopCloser(strings.NewReader("upstream gateway timeout")),
				}, nil
			}),
		},
	}
	if _, err := ps.Synthesize(ctx, SynthesizeInput{Capability: domain.CapabilitySQLGenerate, AnonymizedPrompt: "query"}); err == nil {
		t.Fatal("expected error when provider returns 500")
	}

	// Case 4: Invalid base_url (http instead of https)
	if _, err := ps.callEndpoint(ctx, provider.Endpoint{BaseURL: "http://insecure.local"}, "sys", "usr", domain.CapabilityPlatformHelp, nil); err == nil {
		t.Fatal("expected error for non-https endpoint")
	}
}

func TestServiceOrderBridgeAndStreamingCoverage(t *testing.T) {
	fx := newAssistantFixture(t)
	ctx := context.Background()

	if _, err := NewServices(nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected error when db or crypter is nil")
	}

	// CreateSession with empty title (en-US and zh-CN) and > 120 rune title
	s1, err := fx.svc.CreateSession(ctx, fx.userA, CreateSessionInput{OutputLocale: "en-US"})
	if err != nil || s1.Title != "New Chat" {
		t.Fatalf("expected New Chat title, got %+v err=%v", s1, err)
	}
	longTitle := strings.Repeat("A", 130)
	s2, err := fx.svc.CreateSession(ctx, fx.userA, CreateSessionInput{Title: longTitle, OutputLocale: "zh-CN"})
	if err != nil || len([]rune(s2.Title)) != 120 {
		t.Fatalf("expected truncated 120-rune title, got len=%d err=%v", len([]rune(s2.Title)), err)
	}

	// ListSessions pagination and invalid cursor
	if _, _, err := fx.svc.ListSessions(ctx, fx.userA, 0, "invalid-cursor"); err == nil {
		t.Fatal("expected error on invalid pagination cursor")
	}
	page1, nextCur, err := fx.svc.ListSessions(ctx, fx.userA, 1, "")
	if err != nil || len(page1) != 1 || nextCur == nil {
		t.Fatalf("expected 1 session and next cursor, got len=%d next=%v err=%v", len(page1), nextCur, err)
	}
	page2, _, err := fx.svc.ListSessions(ctx, fx.userA, 10, *nextCur)
	if err != nil || len(page2) == 0 {
		t.Fatalf("expected second page of sessions, got len=%d err=%v", len(page2), err)
	}

	// DeleteSession version conflict
	if err := fx.svc.DeleteSession(ctx, fx.userA, s1.ID, 999); !hasErrCode(err, domain.CodeVersionConflict) {
		t.Fatalf("expected version conflict on DeleteSession, got %v", err)
	}

	// CancelMessageStream on non-existent message and on streaming message
	if _, err := fx.svc.CancelMessageStream(ctx, fx.userA, s1.ID, uuid.NewString()); !hasErrCode(err, domain.CodeResourceNotFound) {
		t.Fatalf("expected 1002 on unknown message, got %v", err)
	}
	streamMsgID := uuid.NewString()
	msgEnv, _ := fx.svc.sealMessagePayload(streamMsgID, persistedMessagePayload{Content: "partial"})
	_ = fx.db.Create(&persistence.AIAssistantMessageModel{
		ID:                streamMsgID,
		SessionID:         s1.ID,
		UserID:            fx.userA.UserID,
		Sequence:          1,
		Role:              string(domain.RoleAssistant),
		Capability:        string(domain.CapabilityPlatformHelp),
		State:             string(domain.MessageStreaming),
		ContentCiphertext: msgEnv.Ciphertext,
		ContentNonce:      msgEnv.Nonce,
		ContentKeyID:      msgEnv.KeyID,
		ContentAlgorithm:  msgEnv.Algorithm,
		Version:           1,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}).Error
	canceledView, err := fx.svc.CancelMessageStream(ctx, fx.userA, s1.ID, streamMsgID)
	if err != nil || canceledView.State != string(domain.MessageCancelled) {
		t.Fatalf("expected cancelled message state, got %+v err=%v", canceledView, err)
	}

	// SendMessage validation errors
	if _, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{SessionID: s2.ID, Content: "   "}); !hasErrCode(err, domain.CodeValidationFailed) {
		t.Fatalf("expected validation error on empty content, got %v", err)
	}
	if _, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{SessionID: s2.ID, Content: "hi", CapabilityHint: "bad_cap"}); !hasErrCode(err, domain.CodeValidationFailed) {
		t.Fatalf("expected validation error on invalid capability, got %v", err)
	}

	// SendMessage with no datasource selected in SQL generate mode (returns clarification message)
	var streamEvents []StreamEvent
	noDSRes, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s2.ID,
		Content:        "Please generate a SELECT query for warehouses",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		OutputLocale:   "en-US",
		OnStreamEvent: func(ev StreamEvent) {
			streamEvents = append(streamEvents, ev)
		},
	})
	if err != nil || noDSRes.SQLDraft != nil || len(streamEvents) == 0 {
		t.Fatalf("expected clarification reply when no datasource selected, got %+v err=%v", noDSRes, err)
	}

	// SendMessage matching table name from catalog automatically (without explicit TargetTable)
	autoTableRes, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s2.ID,
		Content:        "Please insert into warehouses with code='WH-99' and name='AutoHub'",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsChangeID,
		FlowID:         &fx.changeFlowID,
		OutputLocale:   "en-US",
	})
	if err != nil || autoTableRes.SQLDraft == nil {
		t.Fatalf("expected auto-matched table SQL draft, got %+v err=%v", autoTableRes, err)
	}

	// SendMessage in SQLOptimize mode reusing SelectedDraftID when InputSQL is omitted, plus ExecutionPlanText
	planText := "Index Scan using PRIMARY on warehouses"
	optRes, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:         s2.ID,
		Content:           "Please optimize this query on warehouses",
		CapabilityHint:    string(domain.CapabilitySQLOptimize),
		DatasourceID:      &fx.dsChangeID,
		FlowID:            &fx.changeFlowID,
		ExecutionPlanText: &planText,
		OutputLocale:      "en-US",
	})
	if err != nil || optRes == nil {
		t.Fatalf("expected optimization turn reusing selected draft to succeed, got %+v err=%v", optRes, err)
	}

	// Generate a read-only SELECT draft to test PrecheckDraft & SubmitDraftOrder read-only rejection
	roTable := "warehouses"
	roRes, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s2.ID,
		Content:        "Please query warehouses where code='WH-99'",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsChangeID,
		FlowID:         &fx.changeFlowID,
		TargetTable:    &roTable,
	})
	if err != nil || roRes.SQLDraft == nil {
		t.Fatalf("expected read-only SELECT draft, got %+v err=%v", roRes, err)
	}
	if _, err := fx.svc.PrecheckDraft(ctx, fx.userA, PrecheckDraftInput{
		SessionID: s2.ID,
		DraftID:   roRes.SQLDraft.ID,
		FlowID:    fx.changeFlowID,
		Revision:  roRes.SQLDraft.Revision,
		SQLHMAC:   roRes.SQLDraft.SQLHMAC,
	}); !hasErrCode(err, domain.CodeValidationFailed) {
		t.Fatalf("expected validation error prechecking read-only draft, got %v", err)
	}
	if _, err := fx.svc.SubmitDraftOrder(ctx, fx.userA, SubmitDraftOrderInput{
		SessionID:      s2.ID,
		DraftID:        roRes.SQLDraft.ID,
		FlowID:         fx.changeFlowID,
		RunID:          uuid.NewString(),
		Revision:       roRes.SQLDraft.Revision,
		SQLHMAC:        roRes.SQLDraft.SQLHMAC,
		Title:          "RO Order",
		IdempotencyKey: "idem-ro-1",
	}); !hasErrCode(err, domain.CodeValidationFailed) {
		t.Fatalf("expected validation error submitting read-only draft, got %v", err)
	}

	// SubmitDraftOrder validation errors
	if _, err := fx.svc.SubmitDraftOrder(ctx, fx.userA, SubmitDraftOrderInput{IdempotencyKey: "", Title: "T"}); !hasErrCode(err, domain.CodeValidationFailed) {
		t.Fatalf("expected validation error on empty IdempotencyKey, got %v", err)
	}
	if _, err := fx.svc.SubmitDraftOrder(ctx, fx.userA, SubmitDraftOrderInput{IdempotencyKey: "k", Title: ""}); !hasErrCode(err, domain.CodeValidationFailed) {
		t.Fatalf("expected validation error on empty Title, got %v", err)
	}

	// Test mapBridgeError and runFormalPrecheck / runFormalOrderSubmit unconfigured guards
	if mapBridgeError(nil) != nil {
		t.Fatal("expected nil from mapBridgeError(nil)")
	}
	domE := domain.ErrValidation("dom")
	if mapBridgeError(domE) != domE {
		t.Fatal("expected domain.Error passthrough")
	}
	revE := &reviewdomain.Error{Code: 2010, Name: "AI_BUDGET", Detail: "budget detail"}
	if mapped := mapBridgeError(revE); !hasErrCode(mapped, 2010) {
		t.Fatalf("expected mapped reviewdomain.Error 2010, got %v", mapped)
	}
	ordE := &orderdomain.Error{Code: 3001, Name: "SUBMISSION_BLOCKED:precheck_failed"}
	if mapped := mapBridgeError(ordE); !hasErrCode(mapped, 3001) {
		t.Fatalf("expected mapped orderdomain.Error 3001, got %v", mapped)
	}
	rawE := errors.New("raw error")
	if mapBridgeError(rawE) != rawE {
		t.Fatal("expected raw error passthrough")
	}

	fx.svc.PrecheckRunner = nil
	fx.svc.Review = nil
	if _, _, err := fx.svc.runFormalPrecheck(ctx, fx.userA, fx.changeFlowID, []byte("SELECT 1"), "", "idem"); !hasErrCode(err, domain.CodeAIProviderFailed) {
		t.Fatalf("expected 2011 when Review service is nil, got %v", err)
	}
	fx.svc.OrderSubmitter = nil
	fx.svc.Orders = nil
	if _, err := fx.svc.runFormalOrderSubmit(ctx, fx.userA, orderapp.SubmitOrderV2Input{}); !hasErrCode(err, domain.CodeInvalidStateTransition) {
		t.Fatalf("expected 1010 when Orders service is nil, got %v", err)
	}

	// Test summarizeLatestSessionTitle and makeSafeRestoringDeltaHandler edge cases
	if got := summarizeLatestSessionTitle(strings.Repeat("M", 45), "msg", "prev", "en-US"); !strings.HasSuffix(got, "\u2026") {
		t.Fatalf("expected truncated model title with ellipsis, got %q", got)
	}
	if got := summarizeLatestSessionTitle("", strings.Repeat("U", 32), "prev", "en-US"); !strings.HasSuffix(got, "\u2026") {
		t.Fatalf("expected truncated user message title with ellipsis, got %q", got)
	}
	if got := summarizeLatestSessionTitle("", "", "KeepPrev", "en-US"); got != "KeepPrev" {
		t.Fatalf("expected previous title preserved, got %q", got)
	}
	if got := summarizeLatestSessionTitle("", "", "", "en-US"); got != "New Chat" {
		t.Fatalf("expected New Chat fallback, got %q", got)
	}
	if got := summarizeLatestSessionTitle("", "", "", "zh-CN"); got == "" {
		t.Fatal("expected non-empty default fallback")
	}

	priv := privacy.NewTurnPrivacySession()
	defer priv.ZeroAndRelease()
	_, _ = priv.RegisterTable("core_db", "warehouses")
	var deltaOut []string
	onDelta, flush := makeSafeRestoringDeltaHandler(priv, func(ev string, data any) {
		deltaOut = append(deltaOut, ev)
	})
	onDelta("delta", "")
	onDelta("reasoning", "Checking table_1")
	onDelta("delta", "table_1") // All token-boundary characters -> buffered until non-boundary or flush
	onDelta("delta", " is ready. ")
	flush("warehouses is ready. Done.")
	if len(deltaOut) < 3 {
		t.Fatalf("expected reasoning + delta + flush events, got %v", deltaOut)
	}

	// Test reserveDailyBudget and releaseDailyBudget with Review service wired
	fx.svc.Review = &reviewapp.Services{DB: fx.db, Crypter: fx.crypter}
	_ = fx.db.Exec(`INSERT INTO system_settings (namespace, schema_version, settings, version, updated_by_user_id, created_at, updated_at)
		VALUES ('ai_budget', 1, '{"enforced": true, "daily_limit_minor": 100000, "currency": "USD", "input_price_per_million_minor": 500000}'::jsonb, 1, ?, now(), now())
		ON CONFLICT (namespace) DO UPDATE SET settings = EXCLUDED.settings`, fx.userA.UserID).Error
	t.Cleanup(func() {
		_ = fx.db.Exec(`DELETE FROM system_settings WHERE namespace = 'ai_budget'`).Error
	})
	amt, day, cur, err := fx.svc.reserveDailyBudget(ctx, 10000)
	if err != nil || amt <= 0 || day == "" || cur != "USD" {
		t.Fatalf("unexpected reserveDailyBudget result: amt=%d day=%q cur=%q err=%v", amt, day, cur, err)
	}
	fx.svc.releaseDailyBudget(ctx, day, cur, amt)

	// Unused helper coverage
	_ = hex.EncodeToString([]byte{1, 2})
	_ = database.MetadataColumn{}

	// Additional PrecheckDraft & SubmitDraftOrder branches
	dmlDraftID := autoTableRes.SQLDraft.ID
	dmlHMAC := autoTableRes.SQLDraft.SQLHMAC
	dmlRev := autoTableRes.SQLDraft.Revision

	// PrecheckRunner returning "blocked" and "queued" and error
	fx.svc.PrecheckRunner = func(ctx context.Context, actor domain.Actor, flowID string, sql []byte, locale, idemKey string) (string, string, error) {
		return uuid.NewString(), string(reviewdomain.RunStateBlocked), nil
	}
	resBlocked, err := fx.svc.PrecheckDraft(ctx, fx.userA, PrecheckDraftInput{
		SessionID: s2.ID,
		DraftID:   dmlDraftID,
		Revision:  dmlRev,
		SQLHMAC:   dmlHMAC,
	})
	if err != nil || resBlocked.Draft.SubmissionState != string(domain.SubmissionPrecheckBlocked) {
		t.Fatalf("expected precheck_blocked state, got %+v err=%v", resBlocked, err)
	}

	fx.svc.PrecheckRunner = func(ctx context.Context, actor domain.Actor, flowID string, sql []byte, locale, idemKey string) (string, string, error) {
		return uuid.NewString(), "queued", nil
	}
	resQueued, err := fx.svc.PrecheckDraft(ctx, fx.userA, PrecheckDraftInput{
		SessionID: s2.ID,
		DraftID:   dmlDraftID,
		Revision:  dmlRev,
		SQLHMAC:   dmlHMAC,
	})
	if err != nil || resQueued.Draft.SubmissionState != string(domain.SubmissionPrecheckRunning) {
		t.Fatalf("expected precheck_running state, got %+v err=%v", resQueued, err)
	}

	fx.svc.PrecheckRunner = func(ctx context.Context, actor domain.Actor, flowID string, sql []byte, locale, idemKey string) (string, string, error) {
		return "", "", errors.New("precheck engine down")
	}
	if _, err := fx.svc.PrecheckDraft(ctx, fx.userA, PrecheckDraftInput{
		SessionID: s2.ID,
		DraftID:   dmlDraftID,
		Revision:  dmlRev,
		SQLHMAC:   dmlHMAC,
	}); err == nil {
		t.Fatal("expected error when PrecheckRunner fails")
	}

	// SubmitDraftOrder stale, runID mismatch, flowID mismatch, OrderSubmitter error
	if _, err := fx.svc.SubmitDraftOrder(ctx, fx.userA, SubmitDraftOrderInput{
		SessionID:      s2.ID,
		DraftID:        dmlDraftID,
		FlowID:         fx.changeFlowID,
		RunID:          uuid.NewString(),
		Revision:       dmlRev,
		SQLHMAC:        dmlHMAC,
		Title:          "Order Title",
		IdempotencyKey: "idem-mismatch-run",
	}); !hasErrCode(err, domain.CodeAssistantDraftRevisionStale) {
		t.Fatalf("expected stale error on runID mismatch, got %v", err)
	}

	if _, err := fx.svc.SubmitDraftOrder(ctx, fx.userA, SubmitDraftOrderInput{
		SessionID:      s2.ID,
		DraftID:        dmlDraftID,
		FlowID:         uuid.NewString(),
		RunID:          resQueued.EphemeralRunID,
		Revision:       dmlRev,
		SQLHMAC:        dmlHMAC,
		Title:          "Order Title",
		IdempotencyKey: "idem-mismatch-flow",
	}); !hasErrCode(err, domain.CodeAssistantDraftRevisionStale) {
		t.Fatalf("expected stale error on flowID mismatch, got %v", err)
	}

	fx.svc.OrderSubmitter = func(ctx context.Context, actor domain.Actor, in orderapp.SubmitOrderV2Input) (*orderapp.OrderView, error) {
		return nil, errors.New("submit order failed")
	}
	if _, err := fx.svc.SubmitDraftOrder(ctx, fx.userA, SubmitDraftOrderInput{
		SessionID:      s2.ID,
		DraftID:        dmlDraftID,
		FlowID:         fx.changeFlowID,
		RunID:          resQueued.EphemeralRunID,
		Revision:       dmlRev,
		SQLHMAC:        dmlHMAC,
		Title:          "Order Title",
		IdempotencyKey: "idem-submit-err",
	}); err == nil {
		t.Fatal("expected error when OrderSubmitter fails")
	}

	// Mark draft as order_submitted and verify PrecheckDraft rejects it
	_ = fx.db.Model(&persistence.AIAssistantSQLDraftModel{}).Where("id = ?", dmlDraftID).Update("submission_state", string(domain.SubmissionOrderSubmitted)).Error
	if _, err := fx.svc.PrecheckDraft(ctx, fx.userA, PrecheckDraftInput{
		SessionID: s2.ID,
		DraftID:   dmlDraftID,
		Revision:  dmlRev,
		SQLHMAC:   dmlHMAC,
	}); !hasErrCode(err, domain.CodeInvalidStateTransition) {
		t.Fatalf("expected 1010 on already-submitted draft, got %v", err)
	}

	// Mark draft as stale and verify SubmitDraftOrder rejects it
	_ = fx.db.Model(&persistence.AIAssistantSQLDraftModel{}).Where("id = ?", dmlDraftID).Update("submission_state", string(domain.SubmissionStale)).Error
	if _, err := fx.svc.SubmitDraftOrder(ctx, fx.userA, SubmitDraftOrderInput{
		SessionID:      s2.ID,
		DraftID:        dmlDraftID,
		FlowID:         fx.changeFlowID,
		RunID:          resQueued.EphemeralRunID,
		Revision:       dmlRev,
		SQLHMAC:        dmlHMAC,
		Title:          "Order Title",
		IdempotencyKey: "idem-stale-state",
	}); !hasErrCode(err, domain.CodeAssistantDraftRevisionStale) {
		t.Fatalf("expected 6005 on stale draft state, got %v", err)
	}

	// SendMessage with custom Synthesizer error/OutOfScope/fallback branches
	origSynth := fx.svc.Synthesizer
	defer func() { fx.svc.Synthesizer = origSynth }()

	fx.svc.Synthesizer = &stubSynth{err: context.Canceled}
	if _, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s2.ID,
		Content:        "How does approval work?",
		CapabilityHint: string(domain.CapabilityPlatformHelp),
	}); !hasErrCode(err, domain.CodeInvalidStateTransition) {
		t.Fatalf("expected 1010 on canceled context, got %v", err)
	}
	if _, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s2.ID,
		Content:        "Select from warehouses",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsChangeID,
		TargetTable:    &roTable,
	}); !hasErrCode(err, domain.CodeInvalidStateTransition) {
		t.Fatalf("expected 1010 on canceled context in SQLGenerate, got %v", err)
	}

	fx.svc.Synthesizer = &stubSynth{out: &SynthesizeOutput{OutOfScope: true}}
	if _, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s2.ID,
		Content:        "How does approval work?",
		CapabilityHint: string(domain.CapabilityPlatformHelp),
	}); !hasErrCode(err, domain.CodeAssistantOutOfScope) {
		t.Fatalf("expected 6001 on out of scope platform help, got %v", err)
	}
	if _, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s2.ID,
		Content:        "Select from warehouses",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsChangeID,
		TargetTable:    &roTable,
	}); !hasErrCode(err, domain.CodeAssistantOutOfScope) {
		t.Fatalf("expected 6001 on out of scope SQLGenerate, got %v", err)
	}

	// DML returned on query-only datasource must be rejected with 6002
	fx.svc.Synthesizer = &stubSynth{out: &SynthesizeOutput{
		AssistantReply: "Drafted delete",
		AnonymizedSQL:  "DELETE FROM table_1 WHERE column_1 = 1",
	}}
	if _, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s2.ID,
		Content:        "Select from warehouses",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsQueryOnlyID,
		TargetTable:    &roTable,
	}); !hasErrCode(err, domain.CodeAssistantDatasourceForbidden) {
		t.Fatalf("expected 6002 when model returns DML on query-only datasource, got %v", err)
	}

	// SendMessage without datasource (s1 has no ActiveDatasourceID): test empty clarify reply (en-US and zh-CN), OutOfScope, and error
	fx.svc.Synthesizer = &stubSynth{out: &SynthesizeOutput{AssistantReply: ""}}
	if r, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s1.ID,
		Content:        "Generate SQL for warehouses",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		OutputLocale:   "en-US",
	}); err != nil || !strings.Contains(r.AssistantMessage.Content, "Please Select a Target Datasource") {
		t.Fatalf("expected English fallback clarify message, got %+v err=%v", r, err)
	}
	if r, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s1.ID,
		Content:        "Generate SQL for warehouses",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		OutputLocale:   "zh-CN",
	}); err != nil || r.AssistantMessage.Content == "" {
		t.Fatalf("expected default fallback clarify message, got %+v err=%v", r, err)
	}
	fx.svc.Synthesizer = &stubSynth{out: &SynthesizeOutput{OutOfScope: true}}
	if _, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s1.ID,
		Content:        "Generate SQL for warehouses",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
	}); !hasErrCode(err, domain.CodeAssistantOutOfScope) {
		t.Fatalf("expected 6001 on out of scope without datasource, got %v", err)
	}
	fx.svc.Synthesizer = &stubSynth{err: errors.New("provider failure")}
	if _, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s1.ID,
		Content:        "Generate SQL for warehouses",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
	}); !hasErrCode(err, domain.CodeAIProviderFailed) {
		t.Fatalf("expected 2011 on provider error without datasource, got %v", err)
	}

	// SendMessage with ProviderSynthesizer returning unmapped SQL token so fallback to DeterministicSynthesizer runs
	psFallback := NewProviderSynthesizer(nil, nil, &stubSynth{out: &SynthesizeOutput{
		AssistantReply: "Draft reply",
		AnonymizedSQL:  "SELECT unknown_ident FROM table_1",
	}})
	fx.svc.Synthesizer = psFallback
	if r, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      s2.ID,
		Content:        "Select from warehouses",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsChangeID,
		TargetTable:    &roTable,
	}); err != nil || r.SQLDraft == nil {
		t.Fatalf("expected DeterministicSynthesizer fallback to recover SQL draft, got %+v err=%v", r, err)
	}

	// Test openSessionTitle, openMessagePayload, openDraftPayload, openSummaryContent edge cases
	if _, err := fx.svc.openSessionTitle(&persistence.AIAssistantSessionModel{ID: "x", TitleCiphertext: []byte("bad")}); err == nil {
		t.Fatal("expected error on bad session title ciphertext")
	}
	if _, err := fx.svc.openMessagePayload(&persistence.AIAssistantMessageModel{ID: "x", ContentCiphertext: []byte("bad")}); err == nil {
		t.Fatal("expected error on bad message ciphertext")
	}
	if _, err := fx.svc.openDraftPayload(&persistence.AIAssistantSQLDraftModel{ID: "x", SQLCiphertext: []byte("bad")}); err == nil {
		t.Fatal("expected error on bad draft ciphertext")
	}
	if _, err := fx.svc.openSummaryContent(&persistence.AIAssistantContextSummaryModel{ID: "x", SummaryCiphertext: []byte("bad")}); err == nil {
		t.Fatal("expected error on bad summary ciphertext")
	}
	rawMsgID := uuid.NewString()
	rawMsgEnv, _ := fx.crypter.Seal(crypto.PurposePayload, []byte("plain non-json message"), crypto.AAD{
		DeploymentID: fx.crypter.DeploymentID(),
		Table:        "ai_assistant_messages",
		PrimaryKey:   rawMsgID,
		Column:       "content",
		Purpose:      crypto.PurposePayload,
	})
	if mp, err := fx.svc.openMessagePayload(&persistence.AIAssistantMessageModel{
		ID: rawMsgID, ContentCiphertext: rawMsgEnv.Ciphertext, ContentNonce: rawMsgEnv.Nonce, ContentKeyID: rawMsgEnv.KeyID, ContentAlgorithm: rawMsgEnv.Algorithm,
	}); err != nil || mp.Content != "plain non-json message" {
		t.Fatalf("expected non-json message fallback, got %+v err=%v", mp, err)
	}
	rawDraftID := uuid.NewString()
	rawDraftEnv, _ := fx.crypter.Seal(crypto.PurposePayload, []byte("SELECT 1"), crypto.AAD{
		DeploymentID: fx.crypter.DeploymentID(),
		Table:        "ai_assistant_sql_drafts",
		PrimaryKey:   rawDraftID,
		Column:       "sql",
		Purpose:      crypto.PurposePayload,
	})
	if dp, err := fx.svc.openDraftPayload(&persistence.AIAssistantSQLDraftModel{
		ID: rawDraftID, SQLCiphertext: rawDraftEnv.Ciphertext, SQLNonce: rawDraftEnv.Nonce, SQLKeyID: rawDraftEnv.KeyID, SQLAlgorithm: rawDraftEnv.Algorithm,
	}); err != nil || dp.SQLText != "SELECT 1" {
		t.Fatalf("expected non-json draft fallback, got %+v err=%v", dp, err)
	}
}

type stubSynth struct {
	out *SynthesizeOutput
	err error
}

func (s *stubSynth) Synthesize(ctx context.Context, in SynthesizeInput) (*SynthesizeOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.out, nil
}

func (s *stubSynth) Summarize(ctx context.Context, turns []AnonymizedTurn) (string, int64, int64, error) {
	return "summary", 10, 5, s.err
}
