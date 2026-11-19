//go:build integration

package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

func TestNotificationCoverageHTTPAndTemplateIntegration(t *testing.T) {
	db, svc, worker := newTestService(t)
	ctx := context.Background()
	adminID := "00000000-0000-0000-0000-000000000001"
	orderID := "11111111-1111-4111-8111-111111111199"
	seedUserAndOrder(t, db, orderID)
	now := time.Now().UTC().Truncate(time.Microsecond)

	gin.SetMode(gin.TestMode)
	memStore := session.NewMemoryStore()
	memStore.Put(&session.Session{
		ID: "s-admin",
		Identity: &session.Identity{
			UserID:         adminID,
			Username:       "admin_notif",
			Role:           "admin",
			IsBuiltinAdmin: true,
		},
	})
	router := gin.New()
	router.Use(session.Middleware(memStore, session.Options{CookieName: "sid", AnonymousAllowed: true}))
	RegisterRoutes(router, &Deps{Service: svc})

	doReq := func(method, path, body, ifMatch string) *httptest.ResponseRecorder {
		var r *bytes.Buffer
		if body != "" {
			r = bytes.NewBufferString(body)
		} else {
			r = bytes.NewBuffer(nil)
		}
		req := httptest.NewRequest(method, path, r)
		req.Header.Set("Content-Type", "application/json")
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		req.AddCookie(&http.Cookie{Name: "sid", Value: "s-admin"})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// 1. Create channel via HTTP (also triggers seedDefaultRules)
	w := doReq(http.MethodPost, "/admin/notification-channels", `{
		"kind":"dingtalk",
		"name":"ops-ding",
		"enabled":true,
		"configuration":{"webhook_host":"oapi.dingtalk.com"},
		"secret":{"value":"tok123456"}
	}`, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"err_code":0`) {
		t.Fatalf("create channel failed: %d %s", w.Code, w.Body.String())
	}
	var createChResp struct {
		Data ChannelView `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &createChResp)
	chID := createChResp.Data.ID

	// 2. List & Get channel
	if w := doReq(http.MethodGet, "/admin/notification-channels", "", ""); w.Code != http.StatusOK {
		t.Fatalf("list channels failed: %d", w.Code)
	}
	if w := doReq(http.MethodGet, "/admin/notification-channels/"+chID, "", ""); w.Code != http.StatusOK {
		t.Fatalf("get channel failed: %d", w.Code)
	}
	if w := doReq(http.MethodGet, "/admin/notification-channels/0198da00-9999-7000-8000-999999999999", "", ""); !strings.Contains(w.Body.String(), `"err_code":1002`) {
		t.Fatalf("expected 1002 on missing channel, got %s", w.Body.String())
	}

	// 3. Replace channel & Test Delivery
	w = doReq(http.MethodPut, "/admin/notification-channels/"+chID, `{
		"kind":"dingtalk",
		"name":"ops-ding-v2",
		"enabled":true,
		"configuration":{"webhook_host":"oapi.dingtalk.com"},
		"secret":{"value":"tok654321"}
	}`, `"1"`)
	if !strings.Contains(w.Body.String(), `"ops-ding-v2"`) {
		t.Fatalf("replace channel failed: %s", w.Body.String())
	}
	if w := doReq(http.MethodPost, "/admin/notification-channels/"+chID+"/test-deliveries", "", ""); !strings.Contains(w.Body.String(), `"task_id"`) {
		t.Fatalf("test delivery failed: %s", w.Body.String())
	}

	// 4. CountRulesForChannel & attempt DeleteChannel while rules reference it -> 1006
	cnt, err := svc.CountRulesForChannel(ctx, chID)
	if err != nil || cnt == 0 {
		t.Fatalf("expected seeded rules for channel, got %d (err=%v)", cnt, err)
	}
	if w := doReq(http.MethodDelete, "/admin/notification-channels/"+chID, "", `"2"`); !strings.Contains(w.Body.String(), `"err_code":1006`) {
		t.Fatalf("expected 1006 when deleting referenced channel, got %s", w.Body.String())
	}

	// 5. Rules CRUD + Preview via HTTP
	if w := doReq(http.MethodGet, "/admin/notification-rules", "", ""); w.Code != http.StatusOK {
		t.Fatalf("list rules failed: %d", w.Code)
	}
	w = doReq(http.MethodPost, "/admin/notification-rules", `{
		"name":"custom-rule-1",
		"enabled":true,
		"event_type":"ai.budget_warning",
		"target_kind":"builtin_admins",
		"channel_id":"`+chID+`",
		"message_template":"Budget alert at {{occurred_at}}"
	}`, "")
	var createRuleResp struct {
		Data RuleView `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &createRuleResp)
	ruleID := createRuleResp.Data.ID
	if ruleID == "" {
		t.Fatalf("create rule failed: %s", w.Body.String())
	}
	if w := doReq(http.MethodGet, "/admin/notification-rules/"+ruleID, "", ""); !strings.Contains(w.Body.String(), `"custom-rule-1"`) {
		t.Fatalf("get rule failed: %s", w.Body.String())
	}
	if w := doReq(http.MethodPut, "/admin/notification-rules/"+ruleID, `{
		"name":"custom-rule-1-v2",
		"enabled":true,
		"event_type":"ai.budget_warning",
		"target_kind":"builtin_admins",
		"channel_id":"`+chID+`"
	}`, `"1"`); !strings.Contains(w.Body.String(), `"custom-rule-1-v2"`) {
		t.Fatalf("replace rule failed: %s", w.Body.String())
	}
	if w := doReq(http.MethodPost, "/admin/notification-rules/preview", `{
		"sample_order_id":"`+orderID+`",
		"rule":{
			"name":"preview-r",
			"enabled":true,
			"event_type":"change_order.submitted",
			"target_kind":"order_submitter",
			"channel_id":"`+chID+`"
		}
	}`, ""); !strings.Contains(w.Body.String(), `"message_preview"`) {
		t.Fatalf("preview rule failed: %s", w.Body.String())
	}
	if w := doReq(http.MethodDelete, "/admin/notification-rules/"+ruleID, "", `"99"`); !strings.Contains(w.Body.String(), `"err_code":1004`) {
		t.Fatalf("expected 1004 on rule delete version mismatch, got %s", w.Body.String())
	}
	if w := doReq(http.MethodDelete, "/admin/notification-rules/"+ruleID, "", `"2"`); !strings.Contains(w.Body.String(), `"err_code":0`) {
		t.Fatalf("delete rule failed: %s", w.Body.String())
	}

	// 6. List deliveries via HTTP (with state and after cursor filters)
	evID := "0198da00-0099-7000-8000-000000000001"
	delivID := "0198da00-0099-7000-8000-000000000002"
	_ = db.Exec(`insert into outbox_events (id, aggregate_type, aggregate_id, event_type, subject, payload, occurred_at)
		values (?, 'ai', ?, 'ai.budget_warning', 'ai/budget', '{}', ?)`, evID, adminID, now).Error
	ct, nonce, kid, _ := worker.encryptAddress(delivID, "admin@example.com")
	_ = db.Create(&NotificationDeliveryModel{
		ID:                  delivID,
		DomainEventID:       evID,
		NotificationChannel: chID,
		RecipientUserID:     &adminID,
		RecipientKey:        recipientKeyForUser(adminID),
		AddressCiphertext:   ct,
		AddressNonce:        nonce,
		AddressKeyID:        kid,
		AddressAlgorithm:    "aes-256-gcm",
		State:               StateQueued,
		NextAttemptAt:       &now,
		CreatedAt:           now,
		UpdatedAt:           now,
	}).Error
	if w := doReq(http.MethodGet, "/admin/notification-deliveries?state=queued&after="+delivID, "", ""); !strings.Contains(w.Body.String(), `"err_code":0`) {
		t.Fatalf("list deliveries failed: %s", w.Body.String())
	}

	// 7. Delete second unreferenced channel via HTTP
	w2 := doReq(http.MethodPost, "/admin/notification-channels", `{
		"kind":"slack",
		"name":"ops-slack",
		"enabled":false,
		"configuration":{},
		"secret":{"value":"https://hooks.slack.com/services/T1/B1/X1"}
	}`, "")
	var createCh2 struct {
		Data ChannelView `json:"data"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &createCh2)
	if w := doReq(http.MethodDelete, "/admin/notification-channels/"+createCh2.Data.ID, "", `"1"`); !strings.Contains(w.Body.String(), `"err_code":0`) {
		t.Fatalf("delete unreferenced channel failed: %s", w.Body.String())
	}

	// 8. Exercise Worker template builders with DB handle
	_ = worker.EventBody(ctx, &OutboxEventModel{
		ID:         evID,
		EventType:  "query_grant.revoked",
		Subject:    "query-grants/" + adminID,
		Payload:    []byte(`{"grant_id":"` + adminID + `","revoked_by":"admin","reason_code":"EXPIRED"}`),
		OccurredAt: now,
	}, "EXPIRED")
}
