package notification

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/platform/testkit"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/tasks"
)

func TestNotificationCoverageValidationAndHelpers(t *testing.T) {
	// validateWrite across all channel kinds and invalid branches
	writeCases := []struct {
		in      WriteInput
		wantErr bool
	}{
		{WriteInput{Name: "", Kind: "email"}, true},
		{WriteInput{Name: "em", Kind: "unknown", Configuration: json.RawMessage(`{}`)}, true},
		{WriteInput{Name: "em", Kind: "email", Configuration: json.RawMessage(`bad`)}, true},
		{WriteInput{Name: "em", Kind: "email", Configuration: json.RawMessage(`{"host":"","port":465,"tls_mode":"required"}`)}, true},
		{WriteInput{Name: "em", Kind: "email", Configuration: json.RawMessage(`{"host":"smtp.example.com","port":465,"tls_mode":"none"}`)}, true},
		{WriteInput{Name: "em", Kind: "email", Configuration: json.RawMessage(`{"host":"smtp.example.com","port":465,"tls_mode":"required","username":"` + strings.Repeat("u", 321) + `"}`)}, true},
		{WriteInput{Name: "em", Kind: "email", Configuration: json.RawMessage(`{"host":"smtp.example.com","port":587,"tls_mode":"starttls","username":"u","from_address":"u@example.com"}`)}, false},
		{WriteInput{Name: "dt", Kind: "dingtalk", Configuration: json.RawMessage(`bad`)}, true},
		{WriteInput{Name: "dt", Kind: "dingtalk", Configuration: json.RawMessage(`{"webhook_host":"evil.com"}`)}, true},
		{WriteInput{Name: "dt", Kind: "dingtalk", Configuration: json.RawMessage(`{"webhook_host":"oapi.dingtalk.com"}`)}, false},
		{WriteInput{Name: "fs", Kind: "feishu", Configuration: json.RawMessage(`bad`)}, true},
		{WriteInput{Name: "fs", Kind: "feishu", Configuration: json.RawMessage(`{}`)}, false},
		{WriteInput{Name: "sl", Kind: "slack", Configuration: json.RawMessage(`bad`)}, true},
		{WriteInput{Name: "sl", Kind: "slack", Configuration: json.RawMessage(`{}`)}, false},
	}
	for _, tc := range writeCases {
		_, err := validateWrite(&tc.in)
		if tc.wantErr && err == nil {
			t.Fatalf("expected validateWrite error for %+v", tc.in)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("unexpected validateWrite error for %+v: %v", tc.in, err)
		}
	}

	// validateSecretValue & sanitizeToken
	secretCases := []struct {
		kind    string
		val     string
		wantErr bool
	}{
		{"email", "", true},
		{"email", "secret-pass", false},
		{"feishu", "bad-json", true},
		{"feishu", `{"webhook_url":"https://evil.com/hook/123"}`, true},
		{"feishu", `{"webhook_url":"https://open.feishu.cn/open-apis/bot/v2/hook/bad token"}`, true},
		{"feishu", `{"webhook_url":"https://open.feishu.cn/open-apis/bot/v2/hook/abc-123_456"}`, false},
		{"slack", "https://evil.com/services/T/B/X", true},
		{"slack", "https://hooks.slack.com/services/bad token", true},
		{"slack", "https://hooks.slack.com/services/T000/B000/X000", false},
		{"dingtalk", "bad token!", true},
		{"dingtalk", "valid_token_123", false},
		{"dingtalk", `{"access_token":"tok123","sign_secret":"SEC123"}`, false},
		{"dingtalk", `{"access_token":"tok123","sign_secret":"bad secret!"}`, true},
	}
	for _, tc := range secretCases {
		err := validateSecretValue(tc.kind, tc.val)
		if tc.wantErr && err == nil {
			t.Fatalf("expected validateSecretValue error for %s %q", tc.kind, tc.val)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("unexpected validateSecretValue error for %s %q: %v", tc.kind, tc.val, err)
		}
	}
	if sanitizeToken("") != "" {
		t.Fatal("expected empty token")
	}

	// channelView across all 4 kinds
	svc := NewService(nil, nil)
	now := time.Now().UTC()
	for _, kind := range []string{"email", "dingtalk", "feishu", "slack", "other"} {
		v, err := svc.channelView(&NotificationChannelModel{
			ID: "ch-1", Kind: kind, Name: "n", Configuration: []byte(`{}`), CreatedAt: now, UpdatedAt: now,
		})
		if err != nil || v.ID != "ch-1" {
			t.Fatalf("unexpected channelView result for %s: %v", kind, err)
		}
	}
	if randomToken() == "" {
		t.Fatal("expected non-empty randomToken")
	}
	testKID, testAlg := "k", "aes-256-gcm"
	if _, err := openChannelSecret(svc.Crypter, &NotificationChannelModel{SecretCiphertext: []byte("ct"), SecretKeyID: &testKID, SecretAlgorithm: &testAlg}); err != ErrValidation {
		t.Fatalf("expected openChannelSecret ErrValidation on nil crypter, got %v", err)
	}
	if _, err := openChannelSecret(nil, &NotificationChannelModel{}); err != ErrValidation {
		t.Fatalf("expected openChannelSecret ErrValidation on empty ciphertext, got %v", err)
	}
	if _, err := openDeliveryAddress(svc.Crypter, &NotificationDeliveryModel{AddressCiphertext: []byte("ct"), AddressKeyID: "k"}); err != ErrValidation {
		t.Fatalf("expected openDeliveryAddress ErrValidation on nil crypter, got %v", err)
	}
	if _, err := openDeliveryAddress(nil, &NotificationDeliveryModel{}); err != ErrValidation {
		t.Fatalf("expected openDeliveryAddress ErrValidation on empty ciphertext, got %v", err)
	}

	// validateRuleWrite
	emptyStr := ""
	spaceTpl := "   "
	validTpl := "Order {{order_display_number}} state={{order_state}}"
	badTpl := "Order {{unknown_var}}"
	ruleCases := []struct {
		in      RuleWrite
		wantErr bool
	}{
		{RuleWrite{Name: ""}, true},
		{RuleWrite{Name: "r", EventType: "invalid.event", TargetKind: TargetSubmitter, ChannelID: "c1"}, true},
		{RuleWrite{Name: "r", EventType: "change_order.submitted", TargetKind: "bad_target", ChannelID: "c1"}, true},
		{RuleWrite{Name: "r", EventType: "change_order.submitted", TargetKind: TargetSubmitter, ChannelID: ""}, true},
		{RuleWrite{Name: "r", EventType: "change_order.submitted", TargetKind: TargetSubmitter, ChannelID: "c1", FlowID: &emptyStr, MessageTemplate: &spaceTpl}, false},
		{RuleWrite{Name: "r", EventType: "change_order.submitted", TargetKind: TargetSubmitter, ChannelID: "c1", MessageTemplate: &validTpl}, false},
		{RuleWrite{Name: "r", EventType: "change_order.submitted", TargetKind: TargetSubmitter, ChannelID: "c1", MessageTemplate: &badTpl}, true},
	}
	for _, tc := range ruleCases {
		cp := tc.in
		err := validateRuleWrite(&cp)
		if tc.wantErr && err == nil {
			t.Fatalf("expected validateRuleWrite error for %+v", tc.in)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("unexpected validateRuleWrite error for %+v: %v", tc.in, err)
		}
	}
	rv := ruleView(&NotificationRuleModel{ID: "r1", Name: "n", EventType: "change_order.submitted", TargetKind: TargetSubmitter, ChannelID: "c1", CreatedAt: now, UpdatedAt: now})
	if rv.ID != "r1" {
		t.Fatalf("unexpected ruleView: %+v", rv)
	}
}

func TestNotificationCoverageTemplatesAndWorkerHelpers(t *testing.T) {
	// DefaultPlaceholderTemplate across all event types and locales
	for _, evType := range EventVocabulary {
		for _, loc := range []string{"zh-CN", "en-US", "fr-FR"} {
			if got := DefaultPlaceholderTemplate(evType, loc); got == "" {
				t.Fatalf("expected non-empty placeholder template for %s (%s)", evType, loc)
			}
		}
	}

	// RenderTemplateString edge cases
	if RenderTemplateString("", nil) != "" {
		t.Fatal("expected empty template result")
	}
	if got := RenderTemplateString("prefix {{order_id", map[string]string{"order_id": "1"}); got != "prefix {{order_id" {
		t.Fatalf("unexpected unclosed template output: %q", got)
	}
	if got := RenderTemplateString("{{order_id}} {{unknown_tok}} {{order_title}}", map[string]string{"order_id": ""}); got != "- - -" {
		t.Fatalf("unexpected placeholder fallback output: %q", got)
	}

	// Sanitizer and formatting helpers
	if got := boundedText("abc\x00def\x7f", 4); got != "abc" {
		t.Fatalf("unexpected boundedText: %q", got)
	}
	if got := sanitizeOrderTitle("   \n\t  "); got != "-" {
		t.Fatalf("expected '-' for blank title, got %q", got)
	}
	if got := sanitizeOrderTitle("valid\x00title"); got != "validtitle" {
		t.Fatalf("unexpected sanitizeOrderTitle: %q", got)
	}
	if got := boundedOperator(strings.Repeat("a!b_c.d-e", 10)); strings.Contains(got, "!") || len(got) > 64 {
		t.Fatalf("unexpected boundedOperator: %q", got)
	}
	if shortRef("") != "YR-PREVIEW" || shortRef("1234") != "YR-1234" || shortRef("1234567890") != "YR-12345678" {
		t.Fatal("unexpected shortRef outputs")
	}
	if joinBoundedActors([]string{"!@#"}, 5) != "-" {
		t.Fatal("expected '-' for empty cleaned actors")
	}
	if got := joinBoundedActors([]string{"u1", "u2", "u3", "u4"}, 2); !strings.Contains(got, "u1, u2") {
		t.Fatalf("unexpected joinBoundedActors truncation: %q", got)
	}
	if maskAddress("no-at-sign") != "***" || maskAddress("@domain.com") != "***" || maskAddress("a@d.com") != "a***@d.com" || maskAddress("alice@d.com") != "al***@d.com" {
		t.Fatal("unexpected maskAddress outputs")
	}
	if derefTemplate(nil) != "" {
		t.Fatal("expected empty derefTemplate")
	}
	if previewMessageWithTemplate("change_order.submitted", "{{invalid_token}}") == "" {
		t.Fatal("expected fallback to default template when custom template is invalid")
	}

	// Worker EventBody / EventBodyWithTemplate across event families (nil DB path)
	w := &Worker{locale: "en-US", publicBaseURL: "https://yearning.example.com/"}
	now := time.Now().UTC()
	for _, evType := range EventVocabulary {
		ev := &OutboxEventModel{
			ID:         "ev-1",
			EventType:  evType,
			Subject:    "change-orders/0198da00-0001-7000-8000-000000000001",
			Payload:    []byte(`{"aggregate_id":"0198da00-0001-7000-8000-000000000001","reason_code":"TIMEOUT","grant_id":"g-1","revoked_by":"admin"}`),
			OccurredAt: now,
		}
		body := w.EventBody(context.Background(), ev, "TIMEOUT")
		if body == "" {
			t.Fatalf("expected non-empty EventBody for %s", evType)
		}
	}
	if w.EventBody(context.Background(), &OutboxEventModel{EventType: "unknown"}, "") != "" {
		t.Fatal("expected empty EventBody for unknown event")
	}

	// eventOrderID, trimSubjectID, eventStepID, recipientKindOf, boundedFailure, reasonCode, isUniqueViolation
	if got := eventOrderID(&OutboxEventModel{Payload: []byte(`{"order_id":"ord-2"}`)}); got != "ord-2" {
		t.Fatalf("expected ord-2, got %s", got)
	}
	if got := eventOrderID(&OutboxEventModel{Subject: "change-orders/ord-3"}); got != "ord-3" {
		t.Fatalf("expected ord-3, got %s", got)
	}
	if trimSubjectID("short", "change-orders/") != "" {
		t.Fatal("expected empty trimSubjectID")
	}
	if eventStepID(&OutboxEventModel{EventType: "change_order.submitted"}) != "" {
		t.Fatal("expected empty eventStepID for non-step_activated")
	}
	if eventStepID(&OutboxEventModel{EventType: "change_order.step_activated", Payload: []byte(`{"step_id":"st-1"}`)}) != "st-1" {
		t.Fatal("expected st-1")
	}
	if recipientKindOf("other:1") != "unknown" {
		t.Fatal("expected unknown recipientKindOf")
	}
	for _, f := range []string{FailDial, FailTLS, FailAuth, FailMail, FailWebhook, FailWebhookNOK, FailFeishu, FailFeishuNOK, FailSlack, FailSlackNOK} {
		if boundedFailure("email", errors.New(f)) != f {
			t.Fatalf("expected %s", f)
		}
	}
	for _, k := range []string{"dingtalk", "feishu", "slack", "email"} {
		reason := boundedFailure(k, errors.New("raw error"))
		if reason == "" || strings.Contains(reason, "raw error") {
			t.Fatalf("boundedFailure(%s) returned unsafe or empty reason: %q", k, reason)
		}
	}
	evReason := &OutboxEventModel{Payload: []byte(`{"failure_name":"SMTP_TIMEOUT"}`)}
	if evReason.reasonCode() != "SMTP_TIMEOUT" {
		t.Fatalf("expected SMTP_TIMEOUT, got %s", evReason.reasonCode())
	}
	if (&OutboxEventModel{Payload: []byte(`{"reason_code":"bad lower"}`)}).reasonCode() != "" {
		t.Fatal("expected empty reasonCode for invalid format")
	}
	if !txutil.IsUniqueViolation(errors.New("ERROR: duplicate key value (SQLSTATE 23505)")) || txutil.IsUniqueViolation(nil) {
		t.Fatal("unexpected txutil.IsUniqueViolation result")
	}
	if w.loadRuleTemplate(context.Background(), nil) != "" {
		t.Fatal("expected empty template on nil ruleID")
	}
}

func TestNotificationCoverageHTTPHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := NewService(nil, nil)

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/admin/notification-channels"},
		{http.MethodPost, "/admin/notification-channels"},
		{http.MethodGet, "/admin/notification-channels/ch-1"},
		{http.MethodPut, "/admin/notification-channels/ch-1"},
		{http.MethodDelete, "/admin/notification-channels/ch-1"},
		{http.MethodPost, "/admin/notification-channels/ch-1/test-deliveries"},
		{http.MethodGet, "/admin/notification-deliveries"},
		{http.MethodGet, "/admin/notification-rules"},
		{http.MethodPost, "/admin/notification-rules"},
		{http.MethodPost, "/admin/notification-rules/preview"},
		{http.MethodGet, "/admin/notification-rules/r-1"},
		{http.MethodPut, "/admin/notification-rules/r-1"},
		{http.MethodDelete, "/admin/notification-rules/r-1"},
	}

	// 1. D14 fail-closed invariant: nil Guard must abort every route with HTTP 500 Problem Details.
	rNilGuard := gin.New()
	RegisterRoutes(rNilGuard, &Deps{Service: svc})
	for _, rt := range routes {
		req := httptest.NewRequest(rt.method, rt.path, bytes.NewBufferString(`{}`))
		w := httptest.NewRecorder()
		rNilGuard.ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("nil Guard %s %s expected 500 fail-closed, got %d body=%s", rt.method, rt.path, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Fatalf("nil Guard %s %s Content-Type = %q, want application/problem+json", rt.method, rt.path, ct)
		}
		if !strings.Contains(w.Body.String(), `"status":500`) {
			t.Fatalf("nil Guard %s %s expected RFC 9457 body, got %s", rt.method, rt.path, w.Body.String())
		}
	}

	// 2. Unauthenticated requests with explicit pass-through guard must return 403 application/problem+json.
	r := gin.New()
	RegisterRoutes(r, &Deps{
		Service: svc,
		Guard:   func(_ string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
	})
	for _, rt := range routes {
		req := httptest.NewRequest(rt.method, rt.path, bytes.NewBufferString(`{}`))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s expected 403 without admin session, got %d", rt.method, rt.path, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Fatalf("%s %s 403 Content-Type = %q, want application/problem+json", rt.method, rt.path, ct)
		}
		if !strings.Contains(w.Body.String(), `"status":403`) {
			t.Fatalf("%s %s expected RFC 9457 403 body, got %s", rt.method, rt.path, w.Body.String())
		}
	}

	// 3. Admin session with invalid JSON body on POST/PUT endpoints -> 200 with err_code=1001.
	memStore := session.NewMemoryStore()
	memStore.Put(&session.Session{
		ID: "s-admin",
		Identity: &session.Identity{
			UserID:         "0198da00-0000-7000-8000-000000000001",
			Role:           "admin",
			IsBuiltinAdmin: true,
		},
	})
	rAdmin := gin.New()
	rAdmin.Use(session.Middleware(memStore, session.Options{CookieName: "sid", AnonymousAllowed: true}))
	RegisterRoutes(rAdmin, &Deps{
		Service: svc,
		Guard:   func(_ string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
	})

	badJSONRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/admin/notification-channels"},
		{http.MethodPut, "/admin/notification-channels/ch-1"},
		{http.MethodPost, "/admin/notification-rules"},
		{http.MethodPut, "/admin/notification-rules/r-1"},
		{http.MethodPost, "/admin/notification-rules/preview"},
	}
	for _, rt := range badJSONRoutes {
		req := httptest.NewRequest(rt.method, rt.path, bytes.NewBufferString(`{bad json`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "sid", Value: "s-admin"})
		w := httptest.NewRecorder()
		rAdmin.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"err_code":1001`) {
			t.Fatalf("%s %s expected err_code 1001 on bad JSON, got %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
	}

	// 4. D14 strict If-Match validation: missing/unquoted/zero If-Match returns 1001 VALIDATION_FAILED.
	ifMatchRoutes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPut, "/admin/notification-channels/ch-1", `{"kind":"dingtalk","name":"ops","enabled":true,"configuration":{}}`},
		{http.MethodDelete, "/admin/notification-channels/ch-1", ``},
		{http.MethodPut, "/admin/notification-rules/r-1", `{"name":"r1","enabled":true,"event_type":"ai.budget_warning","target_kind":"builtin_admins","channel_id":"ch-1"}`},
		{http.MethodDelete, "/admin/notification-rules/r-1", ``},
	}
	for _, rt := range ifMatchRoutes {
		for _, hdr := range []string{"", "1", `"0"`, `"abc"`} {
			req := httptest.NewRequest(rt.method, rt.path, bytes.NewBufferString(rt.body))
			req.Header.Set("Content-Type", "application/json")
			if hdr != "" {
				req.Header.Set("If-Match", hdr)
			}
			req.AddCookie(&http.Cookie{Name: "sid", Value: "s-admin"})
			w := httptest.NewRecorder()
			rAdmin.ServeHTTP(w, req)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"err_code":1001`) {
				t.Fatalf("%s %s If-Match=%q expected 1001, got %d %s", rt.method, rt.path, hdr, w.Code, w.Body.String())
			}
		}
	}

	// 5. respondNotificationErr mapping coverage (replace vs delete profile + RFC 9457 500 Problem Details).
	errs := []error{nil, ErrNotFound, ErrConflict, ErrChannelReferenced, ErrValidation, tasks.ErrQueueFull, errors.New("boom")}
	for _, e := range errs {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		respondNotificationErr(c, e, false)
		wDel := httptest.NewRecorder()
		cDel, _ := gin.CreateTestContext(wDel)
		respondNotificationErr(cDel, e, true)
		if e != nil && errors.Is(e, ErrConflict) {
			if !strings.Contains(w.Body.String(), `"err_code":1003`) || !strings.Contains(wDel.Body.String(), `"err_code":1004`) {
				t.Fatalf("conflict mapping mismatch: replace=%s delete=%s", w.Body.String(), wDel.Body.String())
			}
		}
	}
	w500 := httptest.NewRecorder()
	c500, _ := gin.CreateTestContext(w500)
	c500.Set("request_id", "req-notif-500")
	respondNotificationErr(c500, errors.New("secret db error"), false)
	if w500.Code != http.StatusInternalServerError || !strings.HasPrefix(w500.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("unexpected 500 response: status=%d ct=%q body=%s", w500.Code, w500.Header().Get("Content-Type"), w500.Body.String())
	}
	if strings.Contains(w500.Body.String(), "secret db error") {
		t.Fatalf("500 Problem leaked raw error: %s", w500.Body.String())
	}

	// 6. Verify respondNotificationErr emits exact 1012 BusinessErrorResponse shape on tasks.ErrQueueFull.
	wCap := httptest.NewRecorder()
	cCap, _ := gin.CreateTestContext(wCap)
	cCap.Set("request_id", "req-cap-1012")
	respondNotificationErr(cCap, tasks.ErrQueueFull, false)
	if wCap.Code != http.StatusOK {
		t.Fatalf("respondNotificationErr(ErrQueueFull) status = %d, want 200", wCap.Code)
	}
	var capBody struct {
		ErrCode   int            `json:"err_code"`
		Message   string         `json:"message"`
		Data      any            `json:"data"`
		RequestID string         `json:"request_id"`
		Retryable bool           `json:"retryable"`
		Meta      map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(wCap.Body.Bytes(), &capBody); err != nil {
		t.Fatalf("unmarshal 1012 body: %v", err)
	}
	if capBody.ErrCode != 1012 || !capBody.Retryable || capBody.Data != nil || capBody.RequestID != "req-cap-1012" {
		t.Fatalf("unexpected 1012 body: %+v", capBody)
	}
	if capBody.Meta == nil || capBody.Meta["retry_after_ms"] != float64(1000) {
		t.Fatalf("expected meta.retry_after_ms=1000, got %+v", capBody.Meta)
	}
}

type notifQueueStubFx struct {
	pendingCnt  int64
	execQueries []string
	execArgs    [][]driver.NamedValue
}

func (f *notifQueueStubFx) Connect(context.Context) (driver.Conn, error) { return f, nil }
func (f *notifQueueStubFx) Driver() driver.Driver                        { return notifQueueStubDriver{} }

type notifQueueStubDriver struct{}

func (notifQueueStubDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }
func (f *notifQueueStubFx) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unused")
}
func (f *notifQueueStubFx) Close() error              { return nil }
func (f *notifQueueStubFx) Begin() (driver.Tx, error) { return nil, errors.New("unused") }
func (f *notifQueueStubFx) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	f.execQueries = append(f.execQueries, query)
	f.execArgs = append(f.execArgs, args)
	return driver.RowsAffected(1), nil
}
func (f *notifQueueStubFx) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(strings.ToLower(query), "from async_tasks") {
		return &notifSingleRow{cols: []string{"count"}, vals: []driver.Value{f.pendingCnt}}, nil
	}
	now := time.Now().UTC()
	return &notifSingleRow{
		cols: []string{"id", "kind", "name", "enabled", "configuration", "version", "created_by_user_id", "created_at", "updated_at"},
		vals: []driver.Value{"0198da00-0000-7000-8000-000000000010", "email", "ops", true, []byte(`{}`), int64(1), "u-1", now, now},
	}, nil
}

type notifSingleRow struct {
	cols []string
	vals []driver.Value
	done bool
}

func (r *notifSingleRow) Columns() []string { return r.cols }
func (r *notifSingleRow) Close() error      { return nil }
func (r *notifSingleRow) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.vals)
	return nil
}

func TestCreateTestDeliveryRejectsWhenQueueFull(t *testing.T) {
	defer tasks.SetPendingMaximumForTest(2)()
	fx := &notifQueueStubFx{pendingCnt: 2}
	sqlDB := sql.OpenDB(fx)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{Logger: gormlogger.Discard, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, nil)
	if _, err := svc.CreateTestDelivery(context.Background(), "0198da00-0000-7000-8000-000000000010"); !errors.Is(err, tasks.ErrQueueFull) {
		t.Fatalf("CreateTestDelivery err = %v, want tasks.ErrQueueFull", err)
	}
	if len(fx.execQueries) != 0 {
		t.Fatalf("expected zero inserts when queue full, got %v", fx.execQueries)
	}
}

func TestCreateTestDeliveryProgressAndFinishTask(t *testing.T) {
	defer tasks.SetPendingMaximumForTest(10)()
	fx := &notifQueueStubFx{pendingCnt: 0}
	sqlDB := sql.OpenDB(fx)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{Logger: gormlogger.Discard, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, nil)
	taskID, err := svc.CreateTestDelivery(context.Background(), "0198da00-0000-7000-8000-000000000010")
	if err != nil {
		t.Fatalf("CreateTestDelivery err = %v", err)
	}
	if len(fx.execQueries) != 1 {
		t.Fatalf("expected 1 insert query, got %d", len(fx.execQueries))
	}
	insertSQL := strings.ToLower(fx.execQueries[0])
	if !strings.Contains(insertSQL, "progress_total") || !strings.Contains(insertSQL, "0, 1, 'deliveries'") {
		t.Fatalf("expected CreateTestDelivery insert to initialize progress_completed=0, progress_total=1, got: %s", fx.execQueries[0])
	}

	worker := NewWorker(db, nil, 0)
	worker.finishTask(context.Background(), taskID, "succeeded", 0, "")
	worker.finishTask(context.Background(), taskID, "failed", 1001, FailWebhook)
	if len(fx.execQueries) != 3 {
		t.Fatalf("expected 3 total queries, got %d", len(fx.execQueries))
	}
	succeededSQL := strings.ToLower(fx.execQueries[1])
	for _, want := range []string{
		"state = 'succeeded'",
		"progress_total = greatest(progress_total, 1)",
		"progress_completed = 1",
		"lease_owner = null",
		"lease_expires_at = null",
		"updated_at = now()",
	} {
		if !strings.Contains(succeededSQL, want) {
			t.Fatalf("expected succeeded SQL to contain %q, got: %s", want, fx.execQueries[1])
		}
	}
	failedSQL := strings.ToLower(fx.execQueries[2])
	for _, want := range []string{
		"lease_owner = null",
		"lease_expires_at = null",
		"updated_at = now()",
	} {
		if !strings.Contains(failedSQL, want) {
			t.Fatalf("expected failed SQL to contain %q, got: %s", want, fx.execQueries[2])
		}
	}
}

func TestCreateTestDeliveryEncryptsEmailRecipientAndRejectsInvalid(t *testing.T) {
	defer tasks.SetPendingMaximumForTest(10)()
	ring, err := crypto.LoadKeyring(testkit.WriteKeyring(t))
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	fx := &notifQueueStubFx{pendingCnt: 0}
	sqlDB := sql.OpenDB(fx)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{Logger: gormlogger.Discard, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(db, crypter)

	for _, bad := range []TestDeliveryOptions{
		{Recipient: "not-an-email"},
		{Recipient: "a@b.com\r\nBcc:evil@x.com"},
		{Recipient: "Alice <alice@example.com>"},
		{Locale: "fr-FR"},
	} {
		if _, err := svc.CreateTestDeliveryWithOptions(context.Background(), "0198da00-0000-7000-8000-000000000010", bad); !errors.Is(err, ErrValidation) {
			t.Fatalf("expected ErrValidation for %+v, got %v", bad, err)
		}
	}
	if len(fx.execQueries) != 0 {
		t.Fatalf("expected zero inserts on validation failure, got %d", len(fx.execQueries))
	}

	taskID, err := svc.CreateTestDeliveryWithOptions(context.Background(), "0198da00-0000-7000-8000-000000000010", TestDeliveryOptions{
		Recipient: "qa-inbox@example.com",
		Locale:    "zh-CN",
	})
	if err != nil {
		t.Fatalf("CreateTestDeliveryWithOptions err = %v", err)
	}
	if len(fx.execArgs) != 1 || len(fx.execArgs[0]) < 4 {
		t.Fatalf("expected captured insert args, got %+v", fx.execArgs)
	}
	rawInput, ok := fx.execArgs[0][3].Value.([]byte)
	if !ok {
		t.Fatalf("expected input arg bytes, got %T", fx.execArgs[0][3].Value)
	}
	if strings.Contains(string(rawInput), "qa-inbox@example.com") {
		t.Fatalf("plaintext test recipient leaked into async_tasks.input: %s", string(rawInput))
	}
	var decoded testDeliveryInput
	if err := json.Unmarshal(rawInput, &decoded); err != nil {
		t.Fatalf("unmarshal testDeliveryInput: %v", err)
	}
	if len(decoded.RecipientCipher) == 0 || decoded.Locale != "zh-CN" {
		t.Fatalf("expected encrypted recipient ciphertext and zh-CN locale, got %+v", decoded)
	}
	decrypted, err := openTestDeliveryRecipient(crypter, taskID, &decoded)
	if err != nil || decrypted != "qa-inbox@example.com" {
		t.Fatalf("openTestDeliveryRecipient = (%q, %v), want qa-inbox@example.com", decrypted, err)
	}
	if _, err := openTestDeliveryRecipient(crypter, "wrong-task-id", &decoded); err == nil {
		t.Fatal("expected decryption failure for mismatched taskID")
	}
	if _, err := openTestDeliveryRecipient(nil, taskID, &decoded); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation when crypter is nil, got %v", err)
	}
}
