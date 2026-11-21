package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
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
	if _, err := svc.openSecret(context.Background(), &NotificationChannelModel{}); err == nil {
		t.Fatal("expected openSecret error on nil ciphertext")
	}
	if _, err := svc.openDeliveryAddress(context.Background(), &NotificationDeliveryModel{}); err == nil {
		t.Fatal("expected openDeliveryAddress error on nil crypter")
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
		_ = boundedFailure(k, errors.New("raw error"))
	}
	evReason := &OutboxEventModel{Payload: []byte(`{"failure_name":"SMTP_TIMEOUT"}`)}
	if evReason.reasonCode() != "SMTP_TIMEOUT" {
		t.Fatalf("expected SMTP_TIMEOUT, got %s", evReason.reasonCode())
	}
	if (&OutboxEventModel{Payload: []byte(`{"reason_code":"bad lower"}`)}).reasonCode() != "" {
		t.Fatal("expected empty reasonCode for invalid format")
	}
	if !isUniqueViolation(errors.New("ERROR: duplicate key value (SQLSTATE 23505)")) || isUniqueViolation(nil) {
		t.Fatal("unexpected isUniqueViolation result")
	}
	if w.localeOf(context.Background()) != "zh-CN" {
		t.Fatal("expected zh-CN")
	}
	if w.loadRuleTemplate(context.Background(), nil) != "" {
		t.Fatal("expected empty template on nil ruleID")
	}
}

func TestNotificationCoverageHTTPHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := NewService(nil, nil)
	r := gin.New()
	RegisterRoutes(r, &Deps{Service: svc})

	// 1. Unauthenticated requests to all 13 routes must return 403 Forbidden
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
	for _, rt := range routes {
		req := httptest.NewRequest(rt.method, rt.path, bytes.NewBufferString(`{}`))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s expected 403 without admin session, got %d", rt.method, rt.path, w.Code)
		}
	}

	// 2. Admin session with invalid JSON body on POST/PUT endpoints -> 200 with err_code=1001
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

	// 3. writeErr / writeDeleteErr error mapping coverage
	errs := []error{nil, ErrNotFound, ErrConflict, ErrChannelReferenced, ErrValidation, errors.New("boom")}
	for _, e := range errs {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		writeErr(c, e)
		wDel := httptest.NewRecorder()
		cDel, _ := gin.CreateTestContext(wDel)
		writeDeleteErr(cDel, e)
	}
	if parseVersion(`"42"`) != 42 {
		t.Fatal("expected parseVersion 42")
	}
}
