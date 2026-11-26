package application

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/systz"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

type recordedBudgetExec struct {
	query string
	args  []driver.Value
}

type budgetStubState struct {
	mu               sync.Mutex
	timezone         string
	budgetSettings   string
	sessionID        string
	userID           string
	failBudgetLoad   bool
	failLedgerInsert bool
	failBudgetUpdate bool
	txCommits        int
	txRollbacks      int
	execs            []recordedBudgetExec
}

var (
	budgetStubMu       sync.Mutex
	budgetStubRegistry = map[string]*budgetStubState{}
	budgetStubOnce     sync.Once
)

func openBudgetStubDB(t *testing.T, state *budgetStubState) (*gorm.DB, *crypto.Crypter) {
	t.Helper()
	budgetStubOnce.Do(func() {
		sql.Register("assistant_budget_stub", budgetStubDriver{})
	})
	dsn := "budget-" + uuid.NewString()
	budgetStubMu.Lock()
	budgetStubRegistry[dsn] = state
	budgetStubMu.Unlock()
	t.Cleanup(func() {
		budgetStubMu.Lock()
		delete(budgetStubRegistry, dsn)
		budgetStubMu.Unlock()
	})

	sqlDB, err := sql.Open("assistant_budget_stub", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	gdb, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               gormlogger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}

	makeKey := func(b byte) string {
		buf := make([]byte, 32)
		for i := range buf {
			buf[i] = b
		}
		return base64.StdEncoding.EncodeToString(buf)
	}
	keyPath := t.TempDir() + "/keyring.json"
	doc := fmt.Sprintf(`{"contract_version":1,"deployment_id":"budget-unit","keys":[
		{"key_id":"pay-1","purpose":"payload","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"id-1","purpose":"identifier_token","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"fp-1","purpose":"sql_fingerprint","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"},
		{"key_id":"cur-1","purpose":"cursor_affinity","status":"encrypt_decrypt","created_at":"2026-08-25T00:00:00Z","key_base64":"%s"}]}`,
		makeKey(1), makeKey(2), makeKey(3), makeKey(4))
	if err := os.WriteFile(keyPath, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	return gdb, crypter
}

type budgetStubDriver struct{}

func (budgetStubDriver) Open(dsn string) (driver.Conn, error) {
	budgetStubMu.Lock()
	st := budgetStubRegistry[dsn]
	budgetStubMu.Unlock()
	if st == nil {
		return nil, errors.New("unknown budget stub dsn")
	}
	return &budgetStubConn{state: st}, nil
}

type budgetStubConn struct{ state *budgetStubState }
type budgetStubTx struct{ state *budgetStubState }

func (c *budgetStubConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c *budgetStubConn) Close() error              { return nil }
func (c *budgetStubConn) Begin() (driver.Tx, error) { return budgetStubTx{state: c.state}, nil }
func (c *budgetStubConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return budgetStubTx{state: c.state}, nil
}
func (t budgetStubTx) Commit() error {
	t.state.mu.Lock()
	t.state.txCommits++
	t.state.mu.Unlock()
	return nil
}
func (t budgetStubTx) Rollback() error {
	t.state.mu.Lock()
	t.state.txRollbacks++
	t.state.mu.Unlock()
	return nil
}

func (c *budgetStubConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	vals := make([]driver.Value, len(args))
	for i, a := range args {
		vals[i] = a.Value
	}
	c.state.mu.Lock()
	c.state.execs = append(c.state.execs, recordedBudgetExec{query: query, args: vals})
	failLedger := c.state.failLedgerInsert
	failUpdate := c.state.failBudgetUpdate
	c.state.mu.Unlock()
	if failLedger && strings.Contains(query, "insert into ai_usage_ledger") {
		return nil, errors.New("simulated ledger insert failure")
	}
	if failUpdate && strings.Contains(query, "insert into ai_daily_budget_usage") {
		return nil, errors.New("simulated daily budget upsert failure")
	}
	return driver.RowsAffected(1), nil
}

func (c *budgetStubConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	tz := c.state.timezone
	cfg := c.state.budgetSettings
	sessID := c.state.sessionID
	userID := c.state.userID
	failLoad := c.state.failBudgetLoad
	c.state.mu.Unlock()

	switch {
	case strings.Contains(query, "system_timezone"):
		return &budgetStubRows{
			cols: []string{"tz"},
			rows: [][]driver.Value{{tz}},
		}, nil
	case strings.Contains(query, "namespace = 'ai_budget'"):
		if failLoad {
			return nil, errors.New("simulated ai_budget query failure")
		}
		return &budgetStubRows{
			cols: []string{"settings"},
			rows: [][]driver.Value{{cfg}},
		}, nil
	case strings.Contains(query, "ai_assistant_sessions"):
		return &budgetStubRows{
			cols: []string{
				"id", "user_id", "title_ciphertext", "title_nonce", "title_key_id",
				"title_algorithm", "state", "active_datasource_id", "active_flow_id",
				"active_schema_name", "selected_draft_id", "compacted_through_seq",
				"expires_at", "deleted_at", "version", "created_at", "updated_at",
			},
			rows: [][]driver.Value{{
				sessID, userID, []byte("ct"), []byte("nc"), "pay-1",
				"aes-256-gcm", string(domain.SessionStateActive), nil, nil,
				nil, nil, int64(0),
				time.Now().UTC().Add(time.Hour), nil, int64(1), time.Now().UTC(), time.Now().UTC(),
			}},
		}, nil
	case strings.Contains(query, "count(*)"):
		return &budgetStubRows{
			cols: []string{"count"},
			rows: [][]driver.Value{{int64(0)}},
		}, nil
	default:
		return &budgetStubRows{cols: []string{}}, nil
	}
}

type budgetStubRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *budgetStubRows) Columns() []string { return r.cols }
func (r *budgetStubRows) Close() error      { return nil }
func (r *budgetStubRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

type stubAssistantSynthesizer struct {
	out *SynthesizeOutput
	err error
}

func (s *stubAssistantSynthesizer) Synthesize(_ context.Context, _ SynthesizeInput) (*SynthesizeOutput, error) {
	return s.out, s.err
}

func (s *stubAssistantSynthesizer) Summarize(_ context.Context, _ []AnonymizedTurn) (string, int64, int64, error) {
	return "summary", 10, 5, nil
}

func TestBudgetDayUsesReviewSystemTimezone(t *testing.T) {
	state := &budgetStubState{
		timezone:       "America/Los_Angeles",
		budgetSettings: `{"enforced":true,"currency":"USD","daily_budget_minor":10000,"input_price_per_million_minor":300,"output_price_per_million_minor":600}`,
	}
	gdb, crypter := openBudgetStubDB(t, state)
	rec, err := auditlog.NewRecorder(gdb)
	if err != nil {
		t.Fatal(err)
	}
	reviewSvc, err := reviewapp.NewServices(gdb, crypter, rec, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 2026-03-10 04:00 UTC is 2026-03-09 21:00 PDT in America/Los_Angeles.
	instant := time.Date(2026, 3, 10, 4, 0, 0, 0, time.UTC)
	if got := reviewSvc.BudgetDay(instant); got != "2026-03-09" {
		t.Fatalf("BudgetDay(America/Los_Angeles) = %q, want 2026-03-09", got)
	}

	state.mu.Lock()
	state.timezone = "UTC"
	state.mu.Unlock()
	systz.InvalidateDB(gdb)
	if got := reviewSvc.BudgetDay(instant); got != "2026-03-10" {
		t.Fatalf("BudgetDay(UTC) = %q, want 2026-03-10", got)
	}

	// Verify reserveDailyBudget delegates to Review.BudgetDay rather than time.Now().UTC().
	svc, err := NewServices(gdb, crypter, reviewSvc, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	state.timezone = "America/Los_Angeles"
	state.execs = nil
	state.mu.Unlock()
	systz.InvalidateDB(gdb)

	expectedDay := reviewSvc.BudgetDay(time.Now())
	lease, err := svc.acquireDailyBudgetLease(context.Background(), 50000)
	if err != nil {
		t.Fatalf("acquireDailyBudgetLease: %v", err)
	}
	if lease.Day() != expectedDay {
		t.Fatalf("acquireDailyBudgetLease day = %q, want Review.BudgetDay %q", lease.Day(), expectedDay)
	}

	// D14 fail-closed: nil Review must return ErrBudgetServiceUnavailable instead of silently bypassing budget checks.
	svcNoReview := &Services{DB: gdb}
	if _, err := svcNoReview.acquireDailyBudgetLease(context.Background(), 50000); !errors.Is(err, reviewapp.ErrBudgetServiceUnavailable) {
		t.Fatalf("expected ErrBudgetServiceUnavailable when Review is nil, got %v", err)
	}
}

func TestSettleDailyBudgetTransfersAndReleasesExcess(t *testing.T) {
	ctx := context.Background()
	state := &budgetStubState{timezone: "UTC"}
	gdb, crypter := openBudgetStubDB(t, state)
	rec, err := auditlog.NewRecorder(gdb)
	if err != nil {
		t.Fatal(err)
	}
	reviewSvc, err := reviewapp.NewServices(gdb, crypter, rec, nil)
	if err != nil {
		t.Fatal(err)
	}

	settings := reviewapp.BudgetSettings{
		Enforced:                   true,
		Currency:                   "USD",
		DailyBudgetMinor:           10000,
		InputPricePerMillionMinor:  300,
		OutputPricePerMillionMinor: 600,
	}
	providerID := uuid.NewString()
	turnID := uuid.NewString()

	// Case 1: actual (300) < reserved (1000) -> settles 300 and releases only excess 700.
	state.execs = nil
	lease1 := reviewapp.NewBudgetLease(reviewSvc, gdb, "2026-11-21", "USD", 1000, settings)
	if err := lease1.Settle(ctx, turnID, providerID, "gpt-4o-mini", "assistant", 1_000_000, 0, "succeeded"); err != nil {
		t.Fatalf("BudgetLease.Settle under-budget: %v", err)
	}
	var settledAmt, releasedAmt int64
	for _, ex := range state.execs {
		if strings.Contains(ex.query, "insert into ai_usage_ledger") {
			settledAmt = ex.args[7].(int64)
		}
		if strings.Contains(ex.query, "update ai_daily_budget_usage set reserved_minor = greatest(reserved_minor -") {
			releasedAmt = ex.args[0].(int64)
		}
	}
	if settledAmt != 300 || releasedAmt != 700 {
		t.Fatalf("expected settled=300 released=700, got settled=%d released=%d", settledAmt, releasedAmt)
	}
	// Release after Settle must be a no-op (idempotent).
	beforeReleaseCount := len(state.execs)
	if err := lease1.Release(ctx); err != nil {
		t.Fatalf("Release after Settle: %v", err)
	}
	if len(state.execs) != beforeReleaseCount {
		t.Fatal("expected Release after Settle to be a no-op")
	}

	// Case 2: actual (1500) >= reserved (1000) -> settles 1500 and releases 0.
	state.execs = nil
	lease2 := reviewapp.NewBudgetLease(reviewSvc, gdb, "2026-11-21", "USD", 1000, settings)
	if err := lease2.Settle(ctx, turnID, providerID, "gpt-4o-mini", "assistant", 5_000_000, 0, "succeeded"); err != nil {
		t.Fatalf("BudgetLease.Settle over-reserve: %v", err)
	}
	for _, ex := range state.execs {
		if strings.Contains(ex.query, "update ai_daily_budget_usage set reserved_minor = greatest(reserved_minor -") {
			t.Fatalf("did not expect excess Release when actual >= reserved, got args=%v", ex.args)
		}
	}

	// Case 3: Settle failure releases full reservedMinor (1000) so reserved_minor never leaks.
	state.execs = nil
	state.failLedgerInsert = true
	lease3 := reviewapp.NewBudgetLease(reviewSvc, gdb, "2026-11-21", "USD", 1000, settings)
	if err := lease3.Settle(ctx, turnID, providerID, "gpt-4o-mini", "assistant", 1_000_000, 0, "succeeded"); err == nil {
		t.Fatal("expected error when ledger insert fails")
	}
	var recoveredRelease int64
	for _, ex := range state.execs {
		if strings.Contains(ex.query, "update ai_daily_budget_usage set reserved_minor = greatest(reserved_minor -") {
			recoveredRelease = ex.args[0].(int64)
		}
	}
	if recoveredRelease != 1000 {
		t.Fatalf("expected full reservation release 1000 on Settle failure, got %d", recoveredRelease)
	}

	// Case 4: Abort Release releases full reservedMinor once and is idempotent on repeat calls.
	state.execs = nil
	state.failLedgerInsert = false
	lease4 := reviewapp.NewBudgetLease(reviewSvc, gdb, "2026-11-21", "USD", 1000, settings)
	if err := lease4.Release(ctx); err != nil {
		t.Fatalf("Release on abort: %v", err)
	}
	if err := lease4.Release(ctx); err != nil {
		t.Fatalf("second Release on abort: %v", err)
	}
	releaseCalls := 0
	for _, ex := range state.execs {
		if strings.Contains(ex.query, "update ai_daily_budget_usage set reserved_minor = greatest(reserved_minor -") {
			releaseCalls++
			if ex.args[0].(int64) != 1000 {
				t.Fatalf("abort release amount = %v, want 1000", ex.args[0])
			}
		}
	}
	if releaseCalls != 1 {
		t.Fatalf("expected exactly 1 release SQL call across two Release() calls, got %d", releaseCalls)
	}
}

func TestSendMessageBranch2ReservesAndSettlesDailyBudget(t *testing.T) {
	sessID := uuid.NewString()
	userID := uuid.NewString()
	providerID := uuid.NewString()

	state := &budgetStubState{
		timezone:       "UTC",
		budgetSettings: `{"enforced":true,"currency":"USD","daily_budget_minor":10000,"input_price_per_million_minor":300,"output_price_per_million_minor":600}`,
		sessionID:      sessID,
		userID:         userID,
	}
	gdb, crypter := openBudgetStubDB(t, state)
	rec, err := auditlog.NewRecorder(gdb)
	if err != nil {
		t.Fatal(err)
	}
	reviewSvc, err := reviewapp.NewServices(gdb, crypter, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewServices(gdb, crypter, reviewSvc, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Subcase A: Branch 2 (no ActiveDatasourceID, sql_generate) reserves and settles daily budget on success.
	svc.Synthesizer = &stubAssistantSynthesizer{
		out: &SynthesizeOutput{
			ResolvedCapability: string(domain.CapabilitySQLGenerate),
			AssistantReply:     "Please select a datasource and schema first.",
			NeedsClarify:       true,
			ProviderID:         providerID,
			ModelName:          "gpt-4o-mini",
			InputTokens:        500_000, // 150 minor
			OutputTokens:       250_000, // 150 minor -> total 300 minor
		},
	}
	_, err = svc.SendMessage(context.Background(), domain.Actor{UserID: userID, Username: "alice"}, SendMessageInput{
		SessionID:      sessID,
		Content:        "Generate a select query for orders",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
	})
	if err != nil {
		t.Fatalf("SendMessage Branch 2 success: %v", err)
	}
	var sawReserve, sawSettle bool
	for _, ex := range state.execs {
		if strings.Contains(ex.query, "set reserved_minor = ai_daily_budget_usage.reserved_minor") {
			sawReserve = true
		}
		if strings.Contains(ex.query, "insert into ai_usage_ledger") && ex.args[7].(int64) == 300 {
			sawSettle = true
		}
	}
	if !sawReserve || !sawSettle {
		t.Fatalf("Branch 2 must reserve and settle daily budget: sawReserve=%v sawSettle=%v", sawReserve, sawSettle)
	}

	// Subcase B: Synthesizer failure releases the reservation without settling.
	state.execs = nil
	svc.Synthesizer = &stubAssistantSynthesizer{
		err: domain.ErrProviderFailed("upstream_timeout"),
	}
	_, err = svc.SendMessage(context.Background(), domain.Actor{UserID: userID, Username: "alice"}, SendMessageInput{
		SessionID:      sessID,
		Content:        "Generate a select query for orders",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
	})
	if err == nil {
		t.Fatal("expected error when synthesizer fails")
	}
	var sawRelease, sawUnexpectedSettle bool
	for _, ex := range state.execs {
		if strings.Contains(ex.query, "update ai_daily_budget_usage set reserved_minor = greatest(reserved_minor -") {
			sawRelease = true
		}
		if strings.Contains(ex.query, "insert into ai_usage_ledger") {
			sawUnexpectedSettle = true
		}
	}
	if !sawRelease || sawUnexpectedSettle {
		t.Fatalf("Branch 2 failure must release without settling: sawRelease=%v sawSettle=%v", sawRelease, sawUnexpectedSettle)
	}
}

type countingAssistantSynthesizer struct {
	calls int
	out   *SynthesizeOutput
}

func (s *countingAssistantSynthesizer) Synthesize(_ context.Context, _ SynthesizeInput) (*SynthesizeOutput, error) {
	s.calls++
	return s.out, nil
}

func (s *countingAssistantSynthesizer) Summarize(_ context.Context, _ []AnonymizedTurn) (string, int64, int64, error) {
	return "summary", 10, 5, nil
}

func TestAssistantBudgetFailClosedOnLoadErrorAndTransactionalSettle(t *testing.T) {
	sessID := uuid.NewString()
	userID := uuid.NewString()
	state := &budgetStubState{
		timezone:       "UTC",
		budgetSettings: `{"enforced":true,"currency":"USD","daily_budget_minor":10000,"input_price_per_million_minor":300,"output_price_per_million_minor":600}`,
		sessionID:      sessID,
		userID:         userID,
		failBudgetLoad: true,
	}
	gdb, crypter := openBudgetStubDB(t, state)
	rec, err := auditlog.NewRecorder(gdb)
	if err != nil {
		t.Fatal(err)
	}
	reviewSvc, err := reviewapp.NewServices(gdb, crypter, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewServices(gdb, crypter, reviewSvc, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	synth := &countingAssistantSynthesizer{
		out: &SynthesizeOutput{ResolvedCapability: string(domain.CapabilityPlatformHelp), AssistantReply: "help"},
	}
	svc.Synthesizer = synth

	// 1. LoadBudgetSettings DB error must fail closed without invoking Synthesizer.
	if _, err := svc.SendMessage(context.Background(), domain.Actor{UserID: userID, Username: "alice"}, SendMessageInput{
		SessionID: sessID, Content: "How do I submit an order?", CapabilityHint: string(domain.CapabilityPlatformHelp),
	}); err == nil {
		t.Fatal("expected error when LoadBudgetSettings fails")
	}
	if synth.calls != 0 {
		t.Fatalf("expected 0 synthesizer calls when LoadBudgetSettings fails, got %d", synth.calls)
	}

	// 2. Partial Settle failure (statement 2 fails after ai_usage_ledger insert) rolls back tx and releases reservedMinor outside tx.
	state.mu.Lock()
	state.failBudgetLoad = false
	state.failBudgetUpdate = true
	state.txRollbacks = 0
	state.execs = nil
	state.mu.Unlock()
	settings := reviewapp.BudgetSettings{
		Enforced:                   true,
		Currency:                   "USD",
		DailyBudgetMinor:           10000,
		InputPricePerMillionMinor:  300,
		OutputPricePerMillionMinor: 600,
	}
	leaseFail := reviewapp.NewBudgetLease(reviewSvc, gdb, "2026-11-21", "USD", 1000, settings)
	err = leaseFail.Settle(context.Background(), uuid.NewString(), "", "gpt-4o-mini", "assistant", 500_000, 250_000, "succeeded")
	if err == nil {
		t.Fatal("expected error when ai_daily_budget_usage upsert fails inside Settle tx")
	}
	state.mu.Lock()
	rollbacks := state.txRollbacks
	execs := append([]recordedBudgetExec(nil), state.execs...)
	state.mu.Unlock()
	if rollbacks != 1 {
		t.Fatalf("expected 1 transaction rollback on partial Settle failure, got %d", rollbacks)
	}
	var outerRelease int64
	for _, ex := range execs {
		if strings.Contains(ex.query, "update ai_daily_budget_usage set reserved_minor = greatest(reserved_minor -") && len(ex.args) > 0 {
			outerRelease = ex.args[0].(int64)
		}
	}
	if outerRelease != 1000 {
		t.Fatalf("expected full outer release 1000 after rolled-back Settle tx, got %d", outerRelease)
	}
}
