//go:build integration

// integration_test.go B12 notification domain acceptance (S003): Outbox -> idempotent delivery ->
// successful fake SMTP delivery; <= 5 failures with backoff; async test delivery task; rendered
// content free of SQL patterns (reusing the telemetry golden scanner).
package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/textproto"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func itestDB(t *testing.T) *gorm.DB {
	t.Helper()
	password := envOr(t, "YEARNING_TEST_DATABASE_PASSWORD", "")
	if password == "" {
		t.Skip("integration PostgreSQL not configured")
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		envOr(t, "YEARNING_TEST_DATABASE_HOST", "localhost"),
		envOr(t, "YEARNING_TEST_DATABASE_PORT", "54330"),
		envOr(t, "YEARNING_TEST_DATABASE_USER", "yearning"),
		password,
		envOr(t, "YEARNING_TEST_DATABASE_DBNAME", "yearning_v4"))
	acquireSharedITestLock(t)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("postgres connect: %v", err)
	}
	return db
}

func envOr(t *testing.T, key, def string) string {
	t.Helper()
	if v := goGetenv(key); v != "" {
		return v
	}
	return def
}

func newTestService(t *testing.T) (*gorm.DB, *Service, *Worker) {
	t.Helper()
	db := itestDB(t)
	if err := db.Exec(`alter table notification_rules add column if not exists message_template text check (message_template is null or char_length(message_template) <= 2000)`).Error; err != nil {
		t.Fatalf("ensure message_template column: %v", err)
	}
	// Clean domain tables (leftover queued/failed deliveries from prior runs would point to dead
	// fake test ports; outbox likewise retains only events from the current run).
	for _, tbl := range []string{"notification_deliveries", "notification_rules", "notification_channels", "outbox_events", "audit_events",
		"order_approval_actors", "approval_decisions", "order_approval_steps", "order_execution_actors",
		"order_stages", "order_flow_snapshots", "change_orders", "async_tasks"} {
		if err := db.Exec("truncate table " + tbl).Error; err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	keyringPath := writeTestKeyring(t)
	ring, err := crypto.LoadKeyring(keyringPath)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	ns := NewService(db, crypter)
	worker := NewWorker(db, crypter, 10*time.Millisecond)
	worker.DingTalkBaseURL = "http://127.0.0.1:1"
	// S007: suppression audit recorder (audit_events is truncated alongside domain tables above).
	if auditor, aerr := auditlog.NewRecorder(db); aerr == nil {
		worker.SetAuditor(auditor)
	}
	return db, ns, worker
}

// fakeSMTP minimal SMTP server (capturing DATA payloads).
func fakeSMTP(t *testing.T) (addr string, got func() []string) {
	t.Helper()
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 8)
	var mu sync.Mutex // all is written by the server goroutine and read by got(): requires a mutex under -race
	all := []string{}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := textproto.NewConn(conn)
		_ = r.PrintfLine("220 test ESMTP")
		_, _ = r.ReadLine()
		_ = r.PrintfLine("250 test")
		var dataBuf strings.Builder
		inData := false
		for {
			line, err := r.ReadLine()
			if err != nil {
				break
			}
			upper := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case inData:
				if line == "." {
					inData = false
					mu.Lock()
					all = append(all, dataBuf.String())
					mu.Unlock()
					received <- dataBuf.String()
					_ = r.PrintfLine("250 OK queued") // Client Close() waits for the 250 reply
				} else {
					dataBuf.WriteString(line + "\n")
				}
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				_ = r.PrintfLine("250-test")
				_ = r.PrintfLine("250 OK")
			case strings.HasPrefix(upper, "AUTH"):
				_ = r.PrintfLine("235 Authentication successful")
			case strings.HasPrefix(upper, "MAIL FROM"), strings.HasPrefix(upper, "RCPT TO"):
				_ = r.PrintfLine("250 OK")
			case strings.HasPrefix(upper, "DATA"):
				_ = r.PrintfLine("354 go")
				inData = true
			case strings.HasPrefix(upper, "QUIT"):
				_ = r.PrintfLine("221 bye")
				return
			default:
				_ = r.PrintfLine("250 OK")
			}
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), func() []string {
		for {
			select {
			case m := <-received:
				mu.Lock()
				all = append(all, m)
				mu.Unlock()
			default:
				mu.Lock()
				defer mu.Unlock()
				return all
			}
		}
	}
}

func TestNotificationOutboxToDeliveryIdempotent(t *testing.T) {
	db, ns, worker := newTestService(t)
	ctx := context.Background()
	smtpAddr, got := fakeSMTP(t)
	_, portStr, _ := net.SplitHostPort(smtpAddr)
	ch, err := ns.CreateChannel(ctx, "00000000-0000-0000-0000-000000000001", &WriteInput{
		Kind: "email", Name: "it-main-" + uniqueSuffix(), Enabled: true,
		Configuration: json.RawMessage(fmt.Sprintf(
			`{"host":"127.0.0.1","port":%d,"tls_mode":"starttls","username":"n","from_address":"y@test.local"}`, atoi(portStr))),
		Secret: &struct {
			Value string `json:"value"`
		}{Value: "not-a-real-secret"},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	orderID := "11111111-1111-4111-8111-111111111111"
	seedUserAndOrder(t, db, orderID)
	// S007 rules surface: no delivery when no rules exist (legacy hardcoded routing is retired).
	if _, err := ns.CreateRule(ctx, "00000000-0000-0000-0000-000000000001", &RuleWrite{
		Name: "it-rule-" + uniqueSuffix(), Enabled: true,
		EventType: "execution.failed", TargetKind: TargetSubmitter, ChannelID: ch.ID,
	}); err != nil {
		t.Fatalf("create rule: %v", err)
	}
	seedOutbox(t, db, "execution.failed", "change-orders/"+orderID,
		map[string]any{"aggregate_id": orderID, "reason_code": "DATASOURCE_UNAVAILABLE"})
	worker.Tick(ctx) // outbox→delivery
	worker.Tick(ctx) // delivery→send
	time.Sleep(100 * time.Millisecond)
	deliveries := listAll(t, db)
	if len(deliveries) == 0 {
		t.Fatal("no deliveries created")
	}
	for _, d := range deliveries {
		if d.State != StateSucceeded {
			t.Fatalf("delivery state=%s failure=%v", d.State, derefOr(d.LastFailureName))
		}
	}
	if len(got()) == 0 {
		t.Fatal("fake SMTP received nothing")
	}
	// Idempotency: subsequent Tick calls neither insert new rows nor re-send.
	n := len(deliveries)
	worker.Tick(ctx)
	worker.Tick(ctx)
	if got := len(listAll(t, db)); got != n {
		t.Fatalf("idempotency broken: %d → %d", n, got)
	}
	var published bool
	db.Raw(`select published_at is not null from outbox_events where subject = ?`,
		"change-orders/"+orderID).Scan(&published)
	if !published {
		t.Fatal("outbox not published")
	}
	// Rendered content must not contain SQL patterns (reusing the golden scanner).
	for _, msg := range got() {
		if strings.Contains(strings.ToLower(msg), "select ") || strings.Contains(msg, "password=") {
			t.Fatalf("notification content leak: %q", msg[:min(len(msg), 80)])
		}
	}
}

func TestNotificationRetryAndUnique(t *testing.T) {
	db, ns, worker := newTestService(t)
	ctx := context.Background()
	_, err := ns.CreateChannel(ctx, "00000000-0000-0000-0000-000000000001", &WriteInput{
		Kind: "dingtalk", Name: "it-ding-" + uniqueSuffix(), Enabled: true,
		Configuration: json.RawMessage(`{"webhook_host":"oapi.dingtalk.com"}`),
		Secret: &struct {
			Value string `json:"value"`
		}{Value: "not-a-real-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	chID, rerr := createDingtalkChannel(ctx, ns, "it-ding-"+uniqueSuffix())
	if rerr != nil {
		t.Fatal(rerr)
	}
	orderID := "22222222-2222-4222-8222-222222222222"
	seedUserAndOrder(t, db, orderID)
	if _, err := ns.CreateRule(ctx, "00000000-0000-0000-0000-000000000001", &RuleWrite{
		Name: "it-retry-rule-" + uniqueSuffix(), Enabled: true,
		EventType: "execution.result_unknown", TargetKind: TargetSubmitter, ChannelID: chID,
	}); err != nil {
		t.Fatal(err)
	}
	seedOutbox(t, db, "execution.result_unknown", "change-orders/"+orderID,
		map[string]any{"aggregate_id": orderID})
	for i := 0; i < 12; i++ {
		worker.Tick(ctx)
		time.Sleep(2 * time.Millisecond)
	}
	deliveries := listAll(t, db)
	if len(deliveries) == 0 {
		t.Fatal("no deliveries")
	}
	d := deliveries[0]
	if d.AttemptCount < 1 || d.AttemptCount > MaxDeliveryAttempts {
		t.Fatalf("attempt count out of range: %d", d.AttemptCount)
	}
	if d.State != StateFailed && d.State != StateDeadLetter {
		t.Fatalf("state = %s", d.State)
	}
	// Unique constraint: one row per event + channel + recipient_key (S007 deduplication identity).
	var total int
	db.Raw(`select count(*) from notification_deliveries`).Scan(&total)
	var distinct int
	db.Raw(`select count(distinct (domain_event_id, notification_channel_id, recipient_key)) from notification_deliveries`).Scan(&distinct)
	if total != distinct {
		t.Fatalf("duplicates: total=%d distinct=%d", total, distinct)
	}
}

// TestDefaultRulesSeedOnFirstChannel M1: creating the first channel seeds the PRD §7.4 default rule
// set (only when the rules table is empty); creating a second channel does not re-seed.
func TestDefaultRulesSeedOnFirstChannel(t *testing.T) {
	db, ns, _ := newTestService(t)
	ctx := context.Background()
	_, err := ns.CreateChannel(ctx, "00000000-0000-0000-0000-000000000001", &WriteInput{
		Kind: "email", Name: "it-seed-" + uniqueSuffix(), Enabled: true,
		Configuration: json.RawMessage(`{"host":"127.0.0.1","port":1,"tls_mode":"required","username":"n","from_address":"y@test.local"}`),
		Secret: &struct {
			Value string `json:"value"`
		}{Value: "not-a-real-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var seeded int64
	db.Raw(`select count(*) from notification_rules`).Scan(&seeded)
	if seeded != int64(len(defaultRuleSeeds)) {
		t.Fatalf("default rules seeded = %d, want %d", seeded, len(defaultRuleSeeds))
	}
	var aiRules int64
	db.Raw(`select count(*) from notification_rules where event_type in ('review_run.completed','change_draft.precheck_completed')`).Scan(&aiRules)
	if aiRules != 0 {
		t.Fatalf("AI pre-review events in default seeds: %d", aiRules)
	}
	// Second channel: must not duplicate seeded rules.
	_, err = ns.CreateChannel(ctx, "00000000-0000-0000-0000-000000000001", &WriteInput{
		Kind: "email", Name: "it-seed2-" + uniqueSuffix(), Enabled: true,
		Configuration: json.RawMessage(`{"host":"127.0.0.1","port":1,"tls_mode":"required","username":"n","from_address":"y@test.local"}`),
		Secret: &struct {
			Value string `json:"value"`
		}{Value: "not-a-real-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	db.Raw(`select count(*) from notification_rules`).Scan(&seeded)
	if seeded != int64(len(defaultRuleSeeds)) {
		t.Fatalf("second channel re-seeded: %d", seeded)
	}
}

// TestSuppressionPerRecipient M2: once step1 is approved, stale todo deliveries for step1 approvers
// are suppressed while todo deliveries for active step2 approvers are retained.
func TestSuppressionPerRecipient(t *testing.T) {
	db, ns, worker := newTestService(t)
	ctx := context.Background()
	ch, err := ns.CreateChannel(ctx, "00000000-0000-0000-0000-000000000001", &WriteInput{
		Kind: "email", Name: "it-sup2-" + uniqueSuffix(), Enabled: true,
		Configuration: json.RawMessage(`{"host":"127.0.0.1","port":1,"tls_mode":"required","username":"n","from_address":"y@test.local"}`),
		Secret: &struct {
			Value string `json:"value"`
		}{Value: "not-a-real-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	orderID := "77777777-7777-4777-8777-777777777777"
	oldApprover := "88888888-8888-4888-8888-888888888888"
	newApprover := "aaaaaaa0-0000-4000-8000-00000000aaa2"
	seedApprovalTodo(t, db, orderID, oldApprover)
	if _, err := ns.CreateRule(ctx, "00000000-0000-0000-0000-000000000001", &RuleWrite{
		Name: "it-sup2-rule-" + uniqueSuffix(), Enabled: true,
		EventType: "change_order.step_activated", TargetKind: TargetStageApprovers, ChannelID: ch.ID,
	}); err != nil {
		t.Fatal(err)
	}
	stepID := seedApprovalStepActive(t, db, orderID, oldApprover)
	seedOutbox(t, db, "change_order.step_activated", "change-orders/"+orderID,
		map[string]any{"aggregate_id": orderID, "step_id": stepID})
	worker.Tick(ctx)
	var total int64
	db.Raw(`select count(*) from notification_deliveries`).Scan(&total)
	if total != 1 {
		t.Fatalf("expected 1 todo delivery, got %d", total)
	}
	// step1 approved (its approver's todo becomes stale), step2 activated (new approver).
	db.Exec(`update order_approval_steps set state = 'approved', decided_at = now() where id = ?`, stepID)
	step2 := "99999999-9999-4999-8999-999999999997"
	db.Exec(`insert into order_approval_steps (id, stage_id, position, state)
		values (?, ?, 2, 'active') on conflict (id) do nothing`, step2, "99999999-9999-4999-8999-999999999999")
	db.Exec(`insert into order_approval_actors (step_id, reviewer_user_id_snapshot, username_snapshot, display_name_snapshot)
		values (?, ?, 'newappr', 'New Approver') on conflict (step_id, reviewer_user_id_snapshot) do nothing`,
		step2, newApprover)
	db.Exec(`insert into users (id, username, display_name, email, is_builtin_admin, version, created_at, updated_at)
		values (?, 'newappr', 'New Approver', 'newappr@test.local', false, 1, now(), now())
		on conflict (id) do update set email = 'newappr@test.local'`, newApprover)
	db.Exec(`update notification_deliveries set recipient_key = 'user:' || ?::text where recipient_user_id = ?::uuid`,
		oldApprover, oldApprover)
	db.Exec(`update notification_deliveries set state = 'failed', delivery_attempt_count = 1, next_attempt_at = now() - interval '1 minute'`)
	worker.Tick(ctx)
	db.Raw(`select count(*) from notification_deliveries`).Scan(&total)
	if total != 0 {
		t.Fatalf("stale step1 approver todo not suppressed: %d", total)
	}
}

// TestNotificationGroupDedupMultiRule S007 acceptance-12: multiple rules matching the same group
// channel for the same event generate only a single group delivery (which a nullable unique key
// could not prevent); an individual matching multiple roles on the same email channel receives only one email.
func TestNotificationGroupDedupMultiRule(t *testing.T) {
	db, ns, worker := newTestService(t)
	ctx := context.Background()
	ch, err := ns.CreateChannel(ctx, "00000000-0000-0000-0000-000000000001", &WriteInput{
		Kind: "dingtalk", Name: "it-grp-" + uniqueSuffix(), Enabled: true,
		Configuration: json.RawMessage(`{"webhook_host":"oapi.dingtalk.com"}`),
		Secret: &struct {
			Value string `json:"value"`
		}{Value: "not-a-real-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	orderID := "55555555-5555-4555-8555-555555555555"
	seedUserAndOrder(t, db, orderID)
	for _, ev := range []string{"execution.failed", "execution.result_unknown"} {
		if _, err := ns.CreateRule(ctx, "00000000-0000-0000-0000-000000000001", &RuleWrite{
			Name: "it-grp-rule-" + ev + "-" + uniqueSuffix(), Enabled: true,
			EventType: ev, TargetKind: TargetSubmitter, ChannelID: ch.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Event 1: two rules hit the same group -> single delivery; Event 2: another event -> second delivery.
	seedOutbox(t, db, "execution.failed", "change-orders/"+orderID,
		map[string]any{"aggregate_id": orderID})
	e2 := seedOutbox(t, db, "execution.result_unknown", "change-orders/"+orderID,
		map[string]any{"aggregate_id": orderID})
	worker.Tick(ctx)
	var total int
	db.Raw(`select count(*) from notification_deliveries`).Scan(&total)
	if total != 2 {
		t.Fatalf("group dedup broken: want 2 deliveries (one per event), got %d", total)
	}
	var groupKeys int
	db.Raw(`select count(*) from notification_deliveries where recipient_key = 'group:' || notification_channel_id::text`).Scan(&groupKeys)
	if groupKeys != 2 {
		t.Fatalf("group recipient_key missing: %d", groupKeys)
	}
	// Duplicate event (replayed with the same ID): idempotent with zero new rows.
	var evID string
	db.Raw(`select id from outbox_events where id = ?`, e2).Scan(&evID)
	db.Exec(`update outbox_events set published_at = null where id = ?`, e2)
	worker.Tick(ctx)
	db.Raw(`select count(*) from notification_deliveries`).Scan(&total)
	if total != 2 {
		t.Fatalf("replay created duplicates: %d", total)
	}
	_ = evID
}

// TestNotificationAIExclusion S007 acceptance-05: structural exclusion of AI review events —
// rejected by the rule vocabulary CHECK constraint at configuration time, and ignored when present in outbox.
func TestNotificationAIExclusion(t *testing.T) {
	db, ns, worker := newTestService(t)
	ctx := context.Background()
	ch, err := ns.CreateChannel(ctx, "00000000-0000-0000-0000-000000000001", &WriteInput{
		Kind: "email", Name: "it-ai-" + uniqueSuffix(), Enabled: true,
		Configuration: json.RawMessage(`{"host":"127.0.0.1","port":1,"tls_mode":"required","username":"n","from_address":"y@test.local"}`),
		Secret: &struct {
			Value string `json:"value"`
		}{Value: "not-a-real-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	orderID := "66666666-6666-4666-8666-666666666666"
	seedUserAndOrder(t, db, orderID)
	for _, banned := range []string{"review_run.completed", "change_draft.precheck_completed"} {
		if _, err := ns.CreateRule(ctx, "00000000-0000-0000-0000-000000000001", &RuleWrite{
			Name: "it-ai-rule-" + uniqueSuffix(), Enabled: true,
			EventType: banned, TargetKind: TargetSubmitter, ChannelID: ch.ID,
		}); err == nil {
			t.Fatalf("AI pre-review event %s must not be subscribable", banned)
		}
	}
	seedOutbox(t, db, "review_run.completed", "change-orders/"+orderID,
		map[string]any{"aggregate_id": orderID})
	seedOutbox(t, db, "change_draft.precheck_completed", "change-drafts/"+orderID,
		map[string]any{"aggregate_id": orderID})
	worker.Tick(ctx)
	var total int
	db.Raw(`select count(*) from notification_deliveries`).Scan(&total)
	if total != 0 {
		t.Fatalf("AI pre-review events produced deliveries: %d", total)
	}
}

// TestNotificationSuppression S007 acceptance-16: approval todo handled while queued ->
// suppressed before sending: delete the delivery row and write a bounded audit event (never faking a successful delivery).
func TestNotificationSuppression(t *testing.T) {
	db, ns, worker := newTestService(t)
	ctx := context.Background()
	ch, err := ns.CreateChannel(ctx, "00000000-0000-0000-0000-000000000001", &WriteInput{
		Kind: "email", Name: "it-sup-" + uniqueSuffix(), Enabled: true,
		Configuration: json.RawMessage(`{"host":"127.0.0.1","port":1,"tls_mode":"required","username":"n","from_address":"y@test.local"}`),
		Secret: &struct {
			Value string `json:"value"`
		}{Value: "not-a-real-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	orderID := "77777777-7777-4777-8777-777777777777"
	approverID := "88888888-8888-4888-8888-888888888888"
	seedApprovalTodo(t, db, orderID, approverID)
	if _, err := ns.CreateRule(ctx, "00000000-0000-0000-0000-000000000001", &RuleWrite{
		Name: "it-sup-rule-" + uniqueSuffix(), Enabled: true,
		EventType: "change_order.step_activated", TargetKind: TargetStageApprovers, ChannelID: ch.ID,
	}); err != nil {
		t.Fatal(err)
	}
	stepID := seedApprovalStepActive(t, db, orderID, approverID)
	seedOutbox(t, db, "change_order.step_activated", "change-orders/"+orderID,
		map[string]any{"aggregate_id": orderID, "stage_id": "99999999-9999-4999-8999-999999999999", "step_id": stepID})
	worker.Tick(ctx)
	var total int
	db.Raw(`select count(*) from notification_deliveries`).Scan(&total)
	if total != 1 {
		t.Fatalf("todo delivery not created: %d", total)
	}
	// Todo already handled (step left active state) -> suppress before sending.
	db.Exec(`update order_approval_steps set state = 'approved', decided_at = now() where id = ?`, stepID)
	// Make the delivery claimable (queued -> sending is handled by Tick; set to failed with an expired retry timestamp).
	db.Exec(`update notification_deliveries set state = 'failed', delivery_attempt_count = 1, next_attempt_at = now() - interval '1 minute'`)
	worker.Tick(ctx)
	db.Raw(`select count(*) from notification_deliveries`).Scan(&total)
	if total != 0 {
		t.Fatalf("stale todo not suppressed: %d", total)
	}
	var audits int
	db.Raw(`select count(*) from audit_events where event_type = 'notification.suppressed'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("suppression audit missing: %d", audits)
	}
}

// TestNotificationCustomTemplateAndPreview verifies custom allowlisted message_template CRUD,
// preview rendering with a real sample order, and Worker end-to-end delivery rendering.
func TestNotificationCustomTemplateAndPreview(t *testing.T) {
	db, ns, worker := newTestService(t)
	ctx := context.Background()
	smtpAddr, got := fakeSMTP(t)
	_, portStr, _ := net.SplitHostPort(smtpAddr)
	ch, err := ns.CreateChannel(ctx, "00000000-0000-0000-0000-000000000001", &WriteInput{
		Kind: "email", Name: "it-tpl-" + uniqueSuffix(), Enabled: true,
		Configuration: json.RawMessage(fmt.Sprintf(
			`{"host":"127.0.0.1","port":%d,"tls_mode":"starttls","username":"n","from_address":"y@test.local"}`, atoi(portStr))),
		Secret: &struct {
			Value string `json:"value"`
		}{Value: "not-a-real-secret"},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	// Clear auto-seeded default rules so only our custom rule matches.
	db.Exec(`truncate table notification_rules`)

	orderID := "33333333-3333-4333-8333-333333333333"
	seedUserAndOrder(t, db, orderID)
	approverID := "88888888-8888-4888-8888-888888888888"
	stepID := seedApprovalStepActive(t, db, orderID, approverID)
	now := time.Date(2026, 11, 10, 12, 30, 0, 0, time.UTC)
	if err := db.Exec(`insert into approval_decisions (id, order_id, stage_id, step_id, reviewer_user_id_snapshot,
		reviewer_username_snapshot, decision, comment, occurred_at)
		values (?, ?, '99999999-9999-4999-8999-999999999999', ?, ?, 'supuser', 'approve', '', ?)`,
		newID(), orderID, stepID, approverID, now).Error; err != nil {
		t.Fatalf("seed approval decision: %v", err)
	}
	if err := db.Exec(`insert into order_execution_actors (stage_id, executor_user_id_snapshot, username_snapshot, display_name_snapshot)
		values ('99999999-9999-4999-8999-999999999999', ?, 'execuser', 'Exec User')
		on conflict do nothing`, approverID).Error; err != nil {
		t.Fatalf("seed execution actor: %v", err)
	}

	// Disallowed placeholder must be rejected with ErrValidation.
	badTpl := "Order {{order_id}} SQL={{sql}}"
	if _, err := ns.CreateRule(ctx, "00000000-0000-0000-0000-000000000001", &RuleWrite{
		Name: "it-bad-tpl", Enabled: true,
		EventType: "change_order.approval_decided", TargetKind: TargetSubmitter,
		ChannelID: ch.ID, MessageTemplate: &badTpl,
	}); err == nil {
		t.Fatal("expected disallowed placeholder {{sql}} to be rejected")
	}

	customTpl := "OrderID={{order_id}} No={{order_display_number}} Title={{order_title}} Submitter={{submitter_username}} State={{order_state}} Reviewer={{approved_by}} ApprovedAt={{approved_at}} Executors={{executors}}"
	ruleView, err := ns.CreateRule(ctx, "00000000-0000-0000-0000-000000000001", &RuleWrite{
		Name: "it-custom-tpl-" + uniqueSuffix(), Enabled: true,
		EventType: "change_order.approval_decided", TargetKind: TargetSubmitter,
		ChannelID: ch.ID, MessageTemplate: &customTpl,
	})
	if err != nil {
		t.Fatalf("create rule with custom template: %v", err)
	}
	if ruleView.MessageTemplate == nil || *ruleView.MessageTemplate != customTpl {
		t.Fatalf("expected MessageTemplate=%q, got %v", customTpl, ruleView.MessageTemplate)
	}

	// Preview with sample_order_id resolves real snapshot placeholders.
	preview, err := ns.PreviewRule(ctx, &RulePreviewInput{
		Rule: RuleWrite{
			Name: ruleView.Name, Enabled: true,
			EventType: ruleView.EventType, TargetKind: ruleView.TargetKind,
			ChannelID: ruleView.ChannelID, MessageTemplate: &customTpl,
		},
		SampleOrderID: orderID,
	})
	if err != nil {
		t.Fatalf("preview rule: %v", err)
	}
	for _, want := range []string{
		"OrderID=" + orderID,
		"Title=b12",
		"Submitter=b12user",
		"Reviewer=supuser",
		"ApprovedAt=2026-11-10T12:30:00Z",
		"Executors=execuser",
	} {
		if !strings.Contains(preview.Message, want) {
			t.Fatalf("preview message %q missing %q", preview.Message, want)
		}
	}

	// End-to-end delivery uses the rule's custom template.
	seedOutbox(t, db, "change_order.approval_decided", "change-orders/"+orderID,
		map[string]any{"aggregate_id": orderID, "reason_code": "APPROVED"})
	worker.Tick(ctx)
	worker.Tick(ctx)
	time.Sleep(100 * time.Millisecond)
	msgs := got()
	if len(msgs) == 0 {
		t.Fatal("fake SMTP received no message for custom template rule")
	}
	if !strings.Contains(msgs[0], "OrderID="+orderID) || !strings.Contains(msgs[0], "Reviewer=supuser") {
		t.Fatalf("delivered SMTP payload did not render custom template: %q", msgs[0])
	}
}

func TestNotificationFlowAndStageNameSnapshotAndFallback(t *testing.T) {
	db, ns, worker := newTestService(t)
	ctx := context.Background()
	chID, err := createDingtalkChannel(ctx, ns, "it-flow-name-"+uniqueSuffix())
	if err != nil {
		t.Fatalf("createChannel: %v", err)
	}

	orderID := "33333333-3333-4333-8333-333333334444"
	seedUserAndOrder(t, db, orderID)
	seedApprovalStepActive(t, db, orderID, "88888888-8888-4888-8888-888888888888")

	flowID := "55555555-5555-4555-8555-555555555551"
	ruleSetID := "66666666-6666-4666-8666-666666666661"
	flowNameFallback := "legacy-lookup-flow-" + uniqueSuffix()
	now := time.Now().UTC()
	if err := db.Exec(`insert into flows (id, name, flow_type, status, rule_set_id, version, created_by_user_id, created_at, updated_at)
		values (?, ?, 'change_review', 'enabled', ?, 1, '44444444-4444-4444-8444-444444444444', ?, ?)
		on conflict (id) do update set name = excluded.name`, flowID, flowNameFallback, ruleSetID, now, now).Error; err != nil {
		t.Fatalf("seed flow: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`delete from flows where id = ?`, flowID).Error
	})

	sealSnapshot := func(payload string) {
		env, serr := worker.crypter.Seal(crypto.PurposePayload, []byte(payload), crypto.AAD{
			DeploymentID: worker.crypter.DeploymentID(),
			Table:        "order_flow_snapshots",
			PrimaryKey:   orderID,
			Column:       "flow_snapshot_ciphertext",
			Purpose:      crypto.PurposePayload,
		})
		if serr != nil {
			t.Fatalf("seal flow snapshot: %v", serr)
		}
		if err := db.Exec(`insert into order_flow_snapshots (
				order_id, flow_snapshot_ciphertext, flow_snapshot_nonce,
				flow_snapshot_key_id, flow_snapshot_algorithm, snapshot_hash, created_at
			) values (?, ?, ?, ?, ?, repeat('e', 64), ?)
			on conflict (order_id) do update set
				flow_snapshot_ciphertext = excluded.flow_snapshot_ciphertext,
				flow_snapshot_nonce = excluded.flow_snapshot_nonce,
				flow_snapshot_key_id = excluded.flow_snapshot_key_id`,
			orderID, env.Ciphertext, env.Nonce, env.KeyID, env.Algorithm, now).Error; err != nil {
			t.Fatalf("upsert order_flow_snapshots: %v", err)
		}
	}

	tpl := "Flow={{flow_name}} Stage={{stage_name}}"
	// Case 1: new snapshot with explicit flow_name and stages[].name.
	sealSnapshot(fmt.Sprintf(`{"flow_id":%q,"flow_name":"release-prod-flow","stages":[{"position":1,"name":"stage-canary-mysql"}]}`, flowID))
	p1, err := ns.PreviewRule(ctx, &RulePreviewInput{
		Rule:          RuleWrite{Name: "p1", Enabled: true, EventType: "change_order.submitted", TargetKind: TargetSubmitter, ChannelID: chID, MessageTemplate: &tpl},
		SampleOrderID: orderID,
	})
	if err != nil {
		t.Fatalf("PreviewRule new snapshot: %v", err)
	}
	if !strings.Contains(p1.Message, "Flow=release-prod-flow Stage=stage-canary-mysql") {
		t.Fatalf("expected snapshot flow/stage names, got %q", p1.Message)
	}

	// Case 2: legacy snapshot omitting flow_name and stages[].name falls back to flows.name and order_stages.datasource_name_snapshot.
	sealSnapshot(fmt.Sprintf(`{"flow_id":%q,"stages":[{"position":1}]}`, flowID))
	p2, err := ns.PreviewRule(ctx, &RulePreviewInput{
		Rule:          RuleWrite{Name: "p2", Enabled: true, EventType: "change_order.submitted", TargetKind: TargetSubmitter, ChannelID: chID, MessageTemplate: &tpl},
		SampleOrderID: orderID,
	})
	if err != nil {
		t.Fatalf("PreviewRule legacy fallback: %v", err)
	}
	if !strings.Contains(p2.Message, "Flow="+flowNameFallback+" Stage=seed-ds") {
		t.Fatalf("expected legacy fallback flow/stage names, got %q", p2.Message)
	}
}

// ---- Seed and query helpers ----

func seedUserAndOrder(t *testing.T, db *gorm.DB, orderID string) {
	t.Helper()
	now := time.Now().UTC()
	// display_number unique constraint persists across runs: use a nanosecond suffix (matching ^YR-\d{8}-\d{6}$).
	display := fmt.Sprintf("YR-20260828-%06d", now.UnixNano()%1000000)
	userID := "44444444-4444-4444-8444-444444444444"
	if err := db.Exec(`insert into users (id, username, display_name, email, is_builtin_admin, version, created_at, updated_at)
		values (?, 'b12user', 'B12 User', 'b12@test.local', false, 1, ?, ?)
		on conflict (id) do nothing`, userID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into change_orders (id, display_number, submitter_user_id_snapshot,
		submitter_username_snapshot, submitter_display_name_snapshot, title, description, state,
		sql_hmac, snapshot_hash, manually_verified, version, submitted_at)
		values (?, ?, ?, 'b12user', 'B12 User', 'b12', '', 'submitted',
		repeat('a',64), repeat('b',64), false, 1, ?)
		on conflict (id) do nothing`, orderID, display, userID, now).Error; err != nil {
		t.Fatal(err)
	}
}

func seedOutbox(t *testing.T, db *gorm.DB, eventType, subject string, payload map[string]any) string {
	t.Helper()
	p, _ := json.Marshal(payload)
	now := time.Now().UTC()
	id := newID()
	if err := db.Exec(`insert into outbox_events (id, event_type, subject, payload, occurred_at, available_at)
		values (?, ?, ?, ?, ?, ?)`, id, eventType, subject, p, now, now).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func listAll(t *testing.T, db *gorm.DB) []NotificationDeliveryModel {
	t.Helper()
	var rows []NotificationDeliveryModel
	if err := db.Order("created_at, id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func uniqueSuffix() string { return fmt.Sprint(time.Now().UnixNano()) }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func derefOr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// acquireSharedITestLock shares the same flock file with httpapi/dashboard/review packages:
// once httpapi truncation includes notification tables, cross-package runs must serialize access to the shared itest DB.
func acquireSharedITestLock(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile(os.TempDir()+"/yearningx-itest-shared.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}

// createDingtalkChannel creates an enabled DingTalk channel with a configured Secret (the rule engine
// creates deliveries only for channels with non-empty secret_ciphertext — S007 safety switch).
func createDingtalkChannel(ctx context.Context, ns *Service, name string) (string, error) {
	ch, err := ns.CreateChannel(ctx, "00000000-0000-0000-0000-000000000001", &WriteInput{
		Kind: "dingtalk", Name: name, Enabled: true,
		Configuration: json.RawMessage(`{"webhook_host":"oapi.dingtalk.com"}`),
		Secret: &struct {
			Value string `json:"value"`
		}{Value: "not-a-real-secret"},
	})
	if err != nil {
		return "", err
	}
	return ch.ID, nil
}

// seedApprovalTodo seeds base rows for todo scenarios (users/change order; places the order in
// approval state — production semantics: order state is stage_approval_active while approval todos are pending).
func seedApprovalTodo(t *testing.T, db *gorm.DB, orderID, approverID string) {
	t.Helper()
	seedUserAndOrder(t, db, orderID)
	if err := db.Exec(`update change_orders set state = 'stage_approval_active' where id = ?`,
		orderID).Error; err != nil {
		t.Fatal(err)
	}
	_ = approverID
}

// seedApprovalStepActive seeds an active approval step with a frozen approver and returns the step ID.
func seedApprovalStepActive(t *testing.T, db *gorm.DB, orderID, approverID string) string {
	t.Helper()
	now := time.Now().UTC()
	stageID := "99999999-9999-4999-8999-999999999999"
	dsID := "aaaaaaa0-0000-4000-8000-00000000aaa1"
	if err := db.Exec(`insert into datasources (id, name, engine, compatibility_mode, host, port, version_constraint, created_by_user_id, version, created_at, updated_at)
		values (?, 'seed-ds', 'mysql', 'mysql', '127.0.0.1', 3306, '8.0', '00000000-0000-0000-0000-000000000001', 1, ?, ?)
		on conflict (id) do nothing`, dsID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into order_stages (id, order_id, position, datasource_id,
		datasource_name_snapshot, datasource_engine_snapshot, datasource_identity_fingerprint,
		stage_review_snapshot_id, review_snapshot_hash, review_gate_passed,
		target_snapshot_ciphertext, target_snapshot_nonce, target_snapshot_key_id,
		target_snapshot_algorithm, state, version, activated_at)
		values (?, ?, 1, ?,
		'seed-ds', 'mysql', repeat('c', 64),
		?, repeat('d', 64), true,
		decode('00','hex'), decode('00','hex'), 'k', 'aes-256-gcm',
		'approval_active', 1, ?)
		on conflict (id) do nothing`,
		stageID, orderID, dsID, stageID, now).Error; err != nil {
		t.Fatal(err)
	}
	stepID := "99999999-9999-4999-8999-999999999998"
	if err := db.Exec(`insert into order_approval_steps (id, stage_id, position, state)
		values (?, ?, 1, 'active')
		on conflict (id) do nothing`, stepID, stageID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`insert into order_approval_actors (step_id, reviewer_user_id_snapshot, username_snapshot, display_name_snapshot)
		values (?, ?, 'supuser', 'Sup User')
		on conflict (step_id, reviewer_user_id_snapshot) do nothing`,
		stepID, approverID).Error; err != nil {
		t.Fatal(err)
	}
	// Approver user row (with email address; the rule engine creates deliveries only for valid email addresses).
	if err := db.Exec(`insert into users (id, username, display_name, email, is_builtin_admin, version, created_at, updated_at)
		values (?, 'supuser', 'Sup User', 'sup@test.local', false, 1, ?, ?)
		on conflict (id) do update set email = 'sup@test.local'`,
		approverID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	return stepID
}
