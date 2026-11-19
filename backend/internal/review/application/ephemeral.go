// ephemeral.go Ephemeral precheck Runs (RCP-20261101-EPHEMERAL-PRECHECK).
//
// Persistence iron rule from owner's five-round rulings on 2026-11-01: "Save and Return" = sole write entry
// for drafts/SQL, and "Submit" = sole persistence point for review artifacts; precheck performs zero DB
// persistence throughout. This file hosts in-process storage, launch entry points, and lifecycle management
// for ephemeral Runs; review execution lives in ephemeral_agent.go (reusing existing pure-compute paths while
// writing model/budget ledgers per ruling boundaries — pure counts without content), read views live in
// ephemeral_read.go, and submission-time materialization lives in ephemeral_materialize.go.
//
// Crash/TTL semantics are isomorphic to "unsaved means lost": process restart or result expiration invalidates
// the Run, presented by the client as "Result expired, please run precheck again". Multi-instance deployments
// require session affinity or a shared memory layer (documented in RCP security_impact).
package application

import (
	"context"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// ephemeralMaxPerUser caps concurrent ephemeral Runs per user (reusing maxUserActiveRuns).
const ephemeralMaxPerUser = maxUserActiveRuns

// ephemeralMemoryIdemTTL is the ephemeral idempotency memory window (prevents double-click double-runs only, without DB persistence).
const ephemeralMemoryIdemTTL = 10 * time.Minute

// EphemeralRun holds the complete in-memory state of an ephemeral precheck: rows that the existing pipeline
// would persist to the DB are kept here as constructed, unwritten model structs and batch-inserted under
// the original constraints by the materializer upon submission.
type EphemeralRun struct {
	ID          string
	OwnerUserID string
	FlowID      string
	// DraftID is nullable: empty string for draftless trial runs in create mode; when provided, used only
	// for UI anchoring without writing to the draft during precheck.
	DraftID      string
	OutputLocale string

	SQLPlaintext []byte
	SQLHMAC      string

	Policy                 *FrozenReviewPolicy
	FlowHash               string
	RuleHash               string
	ToolHash               string
	FlowVersion            int64
	FlowVersionSeen        int64
	Profile                fingerprint.Profile
	ExpectedStatementCount int

	mu      sync.RWMutex
	state   domain.RunState
	Run     persistence.ReviewRunModel // Populated after grouping completes (running), converged upon reaching terminal state
	ErrCode int32
	Failure string

	Statements []persistence.SQLStatementModel
	Groups     []persistence.SQLFingerprintGroupModel
	Snapshots  []persistence.StageReviewSnapshotModel
	Mapper     *privacy.RunMapper

	// Stage commits (produced by commitStageMem, column-for-column isomorphic to the DB path).
	Facts            []ephemeralFact
	ToolCalls        []persistence.ReviewToolCallModel
	Findings         []ephemeralFinding
	ContextEnvelopes map[string]crypto.Envelope // snapID -> terminal normalized context (including provenance)

	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	expiresAt  time.Time
}

type ephemeralFact struct {
	ID        string
	SnapID    string
	SourceRef string
	// Sealed normalized facts and raw payloads (same AAD convention as the DB path: AAD = factID).
	FactEnv   crypto.Envelope
	FactMAC   string
	RawEnv    crypto.Envelope
	RawHash   string
	CreatedAt time.Time
}

type ephemeralFinding struct {
	ID         string
	SnapID     string
	GroupID    string
	Category   string
	Severity   string
	Title      string
	Message    string
	Suggestion string
	Evidence   []string
	CreatedAt  time.Time
}

// State reads the current state snapshot.
func (er *EphemeralRun) State() domain.RunState {
	er.mu.RLock()
	defer er.mu.RUnlock()
	return er.state
}

// TerminalState reports whether the state is terminal.
func (er *EphemeralRun) TerminalState() bool {
	return domain.RunTerminal(er.State())
}

// terminalExpiredAt atomically inspects both expiresAt and state under er.mu.RLock
// so concurrent ephemeralFinish updates never race with EphemeralStore.Get / Take / sweepNow.
func (er *EphemeralRun) terminalExpiredAt(now time.Time) bool {
	er.mu.RLock()
	defer er.mu.RUnlock()
	return er.expiresAt.Before(now) && domain.RunTerminal(er.state)
}

// EphemeralStore is the in-process ephemeral Run store (single-instance; empty on restart = ruling semantics).
type EphemeralStore struct {
	mu   sync.Mutex
	runs map[string]*EphemeralRun
	idem map[string]ephemeralIdemEntry

	ttl       time.Duration
	sweepOnce sync.Once
	stop      chan struct{}
}

type ephemeralIdemEntry struct {
	RunID     string
	CreatedAt time.Time
}

// NewEphemeralStore constructs the store; ttl is the retention window for terminal results.
func NewEphemeralStore(ttl time.Duration) *EphemeralStore {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &EphemeralStore{runs: map[string]*EphemeralRun{}, idem: map[string]ephemeralIdemEntry{}, ttl: ttl, stop: make(chan struct{})}
}

// TTL returns the terminal retention window (shared by test seam and finish).
func (st *EphemeralStore) TTL() time.Duration { return st.ttl }

func (st *EphemeralStore) sweep() {
	st.sweepOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-st.stop:
					return
				case <-ticker.C:
					st.sweepNow(time.Now())
				}
			}
		}()
	})
}

// sweepNow runs a single sweep pass: removes terminal expired Runs and expired idempotency entries.
func (st *EphemeralStore) sweepNow(now time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for id, run := range st.runs {
		if run.terminalExpiredAt(now) {
			delete(st.runs, id)
		}
	}
	for k, e := range st.idem {
		if now.Sub(e.CreatedAt) > ephemeralMemoryIdemTTL {
			delete(st.idem, k)
		}
	}
}

// Get reads a Run after ownership verification; non-existent / expired / non-owner uniformly returns ErrNotFound
// (never leaking existence). Expired Runs are rejected immediately (without waiting for the sweep tick — TTL is a hard boundary, review agent M2).
func (st *EphemeralStore) Get(runID, ownerUserID string) *EphemeralRun {
	st.mu.Lock()
	defer st.mu.Unlock()
	run, ok := st.runs[runID]
	if !ok {
		return nil
	}
	if run.OwnerUserID != ownerUserID {
		return nil
	}
	if run.terminalExpiredAt(time.Now()) {
		return nil
	}
	return run
}

// Take consumes a Run for submission: retrieves and removes it (once materialized on submit, the in-memory
// copy is no longer readable — read surfaces switch to the materialized DB rows). Returns nil on ownership mismatch.
func (st *EphemeralStore) Take(runID, ownerUserID string) *EphemeralRun {
	st.mu.Lock()
	defer st.mu.Unlock()
	run, ok := st.runs[runID]
	if !ok || run.OwnerUserID != ownerUserID {
		return nil
	}
	if run.terminalExpiredAt(time.Now()) {
		return nil
	}
	delete(st.runs, runID)
	return run
}

func (st *EphemeralStore) put(run *EphemeralRun) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.runs[run.ID] = run
}

func (st *EphemeralStore) activeOfUser(ownerUserID string) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	n := 0
	for _, run := range st.runs {
		if run.OwnerUserID == ownerUserID && !domain.RunTerminal(run.State()) {
			n++
		}
	}
	return n
}

func (st *EphemeralStore) idemKey(ownerUserID, key string) string {
	return ownerUserID + "\x00" + key
}

// IdempotentRun deduplicates double clicks: returns the existing RunID if the same key's Run is still within the window.
func (st *EphemeralStore) IdempotentRun(ownerUserID, key string) (string, bool) {
	if key == "" {
		return "", false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	e, ok := st.idem[st.idemKey(ownerUserID, key)]
	if !ok || time.Since(e.CreatedAt) > ephemeralMemoryIdemTTL {
		return "", false
	}
	if _, alive := st.runs[e.RunID]; !alive {
		return "", false
	}
	return e.RunID, true
}

func (st *EphemeralStore) rememberIdem(ownerUserID, key, runID string) {
	if key == "" {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.idem[st.idemKey(ownerUserID, key)] = ephemeralIdemEntry{RunID: runID, CreatedAt: time.Now()}
}

// EphemeralStartInput holds input parameters for starting an ephemeral precheck.
type EphemeralStartInput struct {
	FlowID         string
	DraftID        string // Optional; when provided, verifies ownership and flow consistency without writing to the draft
	SQL            []byte
	OutputLocale   string
	IdempotencyKey string
}

// StartEphemeralRun starts an ephemeral precheck: validation and policy freezing read the database read-only,
// then review execution runs in a goroutine (runner is provided by the wiring layer — sharing the same
// dependency set as AgentRunner in serve). Returns RunID for polling.
func (s *Services) StartEphemeralRun(ctx context.Context, actor Actor, runner *AgentRunner, in EphemeralStartInput) (string, error) {
	if runner == nil {
		return "", &domain.Error{Code: 2011, Name: "AI_PROVIDER_FAILED"}
	}
	if in.FlowID == "" {
		return "", &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "flow_id is required"}
	}
	if len(in.SQL) == 0 {
		return "", &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "SQL must not be empty"}
	}
	if len(in.SQL) > 33554432 {
		return "", domain.ErrBatchTooLarge
	}
	if runID, ok := s.Ephemeral.IdempotentRun(actor.UserID, in.IdempotencyKey); ok {
		return runID, nil
	}
	// Bootstrap missing stage datasource identity_fingerprint before freezing the review policy hash.
	s.ensureFlowStagesIdentity(ctx, actor.UserID, in.FlowID)
	var (
		profile         fingerprint.Profile
		policy          *FrozenReviewPolicy
		flowHash        string
		ruleHash        string
		toolHash        string
		flow            flowRow
		flowVersionSeen int64
		sqlHMAC         string
	)
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		granted, err := s.flowGranted(ctx, tx, actor.UserID, in.FlowID)
		if err != nil {
			return err
		}
		if !granted {
			return &domain.Error{Code: 2014, Name: "FLOW_GRANT_REVOKED"}
		}
		if err := tx.WithContext(ctx).Raw(
			`select id, flow_type, status, rule_set_id, version
			 from flows where id = ? for update`, in.FlowID).Scan(&flow).Error; err != nil {
			return err
		}
		if flow.ID == "" {
			return ErrNotFound
		}
		if in.DraftID != "" {
			draft, err := s.loadDraft(ctx, tx, in.DraftID)
			if err != nil {
				return err
			}
			if draft.OwnerUserID != actor.UserID {
				return ErrNotFound
			}
			if draft.FlowID != in.FlowID {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "Draft does not match the specified workflow"}
			}
			if domain.DraftState(draft.State) != domain.DraftStateDraft {
				return domain.ErrIllegalTransition
			}
			if flow.Version != draft.FlowVersionSeen {
				return domain.ErrReviewOutdated
			}
			if draft.TicketType == "rollback" {
				var expectedHMAC string
				if err := tx.WithContext(ctx).Raw(
					`select plaintext_hmac from draft_sql_payloads where draft_id = ?`, draft.ID,
				).Scan(&expectedHMAC).Error; err != nil {
					return err
				}
				if expectedHMAC != "" {
					rawMAC, merr := s.Crypter.HMAC(sqlHMACPurpose, in.SQL)
					if merr != nil {
						return merr
					}
					matched := strings.EqualFold(expectedHMAC, hex.EncodeToString(rawMAC))
					if !matched {
						trimmedMAC, terr := s.Crypter.HMAC(sqlHMACPurpose, []byte(strings.TrimSpace(string(in.SQL))))
						if terr != nil {
							return terr
						}
						matched = strings.EqualFold(expectedHMAC, hex.EncodeToString(trimmedMAC))
					}
					if !matched {
						return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED", Detail: "Rollback draft SQL cannot be modified"}
					}
				}
			}
			flowVersionSeen = draft.FlowVersionSeen
		}
		// Policy freeze (read-only): rule set members, tool definitions, knowledge candidates, and stage profiles.
		policy, flowHash, ruleHash, toolHash, err = s.freezeReviewPolicy(ctx, tx, in.FlowID, flow)
		if err != nil {
			return err
		}
		// Dialect profile is taken from the flow's first-stage datasource (same source as the draft path).
		profile, err = s.flowProfile(ctx, tx, in.FlowID)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	statementCnt, cntErr := s.countAndValidateStatements(in.SQL, profile)
	if cntErr != nil {
		return "", cntErr
	}
	if statementCnt < 1 || statementCnt > 100000 {
		return "", domain.ErrBatchTooLarge
	}
	if s.Ephemeral.activeOfUser(actor.UserID) >= ephemeralMaxPerUser {
		return "", &domain.Error{Code: 1012, Name: "SYSTEM_CAPACITY_EXHAUSTED"}
	}
	sqlHMACBytes, hmacErr := s.Crypter.HMAC(sqlHMACPurpose, in.SQL)
	if hmacErr != nil {
		return "", hmacErr
	}
	sqlHMAC = hex.EncodeToString(sqlHMACBytes)
	now := nowUTC()
	runID := newID()
	er := &EphemeralRun{
		ID: runID, OwnerUserID: actor.UserID, FlowID: in.FlowID, DraftID: in.DraftID,
		OutputLocale: ResolveOutputLocale(in.OutputLocale),
		SQLPlaintext: in.SQL, SQLHMAC: sqlHMAC,
		Policy: policy, FlowHash: flowHash, RuleHash: ruleHash, ToolHash: toolHash,
		FlowVersion: flow.Version, FlowVersionSeen: flowVersionSeen,
		Profile: profile, ExpectedStatementCount: statementCnt,
		// The queued state already carries a minimal Run model: the very first poll tick before grouping completes
		// can still read a real id/created_at rather than zero values (overwritten as a whole when grouping completes).
		Run: persistence.ReviewRunModel{
			ID: runID, DraftID: in.DraftID, DraftRevision: 0,
			State: string(domain.RunStateQueued), Version: 1, CreatedAt: now,
		},
		state:  domain.RunStateQueued,
		Mapper: privacy.NewRunMapper(),
		Facts:  []ephemeralFact{}, ToolCalls: []persistence.ReviewToolCallModel{},
		Findings:         []ephemeralFinding{},
		ContextEnvelopes: map[string]crypto.Envelope{},
		CreatedAt:        now,
	}
	s.Ephemeral.put(er)
	s.Ephemeral.rememberIdem(actor.UserID, in.IdempotencyKey, er.ID)
	s.Ephemeral.sweep()
	go runner.ExecuteEphemeralRun(context.Background(), er)
	return runID, nil
}

// flowProfile resolves the dialect profile of the flow's first-stage datasource (ephemeral path: no draft row).
func (s *Services) flowProfile(ctx context.Context, db *gorm.DB, flowID string) (fingerprint.Profile, error) {
	var name string
	err := db.WithContext(ctx).Raw(`
		select d.engine || '/' || d.compatibility_mode as name
		from flow_stages fs
		join datasources d on d.id = fs.datasource_id
		where fs.flow_id = ?
		order by fs.position limit 1`, flowID).Scan(&name).Error
	if err != nil {
		return fingerprint.Profile{}, err
	}
	if name == "" {
		return fingerprint.Profile{}, ErrNotFound
	}
	return fingerprint.ParseProfile(name)
}

// FindFinding locates an ephemeral Run and finding by finding ID (with ownership verification; returns nil if absent).
func (st *EphemeralStore) FindFinding(findingID, ownerUserID string) (*EphemeralRun, *ephemeralFinding) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, run := range st.runs {
		if run.OwnerUserID != ownerUserID {
			continue
		}
		run.mu.RLock()
		for i := range run.Findings {
			if run.Findings[i].ID == findingID {
				f := run.Findings[i]
				run.mu.RUnlock()
				return run, &f
			}
		}
		run.mu.RUnlock()
	}
	return nil, nil
}

// FindFact probes ephemeral Runs by evidence (fact) ID (in-memory branch entry for the raw-reveal endpoint;
// owner filtering prevents leaking existence).
func (st *EphemeralStore) FindFact(factID, ownerUserID string) (*EphemeralRun, *ephemeralFact) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, run := range st.runs {
		if run.OwnerUserID != ownerUserID {
			continue
		}
		run.mu.RLock()
		for i := range run.Facts {
			if run.Facts[i].ID == factID {
				f := run.Facts[i]
				run.mu.RUnlock()
				return run, &f
			}
		}
		run.mu.RUnlock()
	}
	return nil, nil
}

// EphemeralFindFact probes ephemeral facts by evidence ID (Services-layer entry).
func (s *Services) EphemeralFindFact(factID, ownerUserID string) (*EphemeralRun, *ephemeralFact) {
	return s.Ephemeral.FindFact(factID, ownerUserID)
}

// EphemeralFindFinding probes ephemeral Runs by finding ID (in-memory branch entry for the evidence endpoint).
func (s *Services) EphemeralFindFinding(findingID, ownerUserID string) (*EphemeralRun, *ephemeralFinding) {
	return s.Ephemeral.FindFinding(findingID, ownerUserID)
}

// EphemeralPutBack performs compensating put-back after a submission transaction fails (including serialization
// retry exhaustion): returns the ephemeral Run to the store so the user can read and resubmit it (review agent H1).
func (s *Services) EphemeralPutBack(er *EphemeralRun) {
	if er == nil {
		return
	}
	s.Ephemeral.put(er)
}

// EphemeralTTL returns the terminal retention window (used at finish).
func (s *Services) EphemeralTTL() time.Duration { return s.Ephemeral.ttl }

// EphemeralTake consumes an ephemeral Run for submission (changeorder submit v2 entry; ownership check built in).
func (s *Services) EphemeralTake(runID, ownerUserID string) *EphemeralRun {
	return s.Ephemeral.Take(runID, ownerUserID)
}

// StatementCount returns the number of submitted statements (used by submit v2 when writing the payload).
func (er *EphemeralRun) StatementCount() int32 {
	er.mu.RLock()
	defer er.mu.RUnlock()
	return er.Run.StatementCount
}

// ---- Cross-package test seams (named ForTest, forbidden in production) ----

// SeedEphemeralRunForTest directly inserts an ephemeral Run (bypassing the executor; HMAC is computed
// using the real formula). Used for changeorder submit v2 integration regression tests (review agent H1).
func (s *Services) SeedEphemeralRunForTest(runID, ownerUserID, flowID string, state domain.RunState, sqlText []byte) (*EphemeralRun, error) {
	mac, err := s.Crypter.HMAC(sqlHMACPurpose, sqlText)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	er := &EphemeralRun{
		ID: runID, OwnerUserID: ownerUserID, FlowID: flowID,
		SQLPlaintext: sqlText, SQLHMAC: hex.EncodeToString(mac),
		state: state, Mapper: privacy.NewRunMapper(),
		Facts: []ephemeralFact{}, ToolCalls: []persistence.ReviewToolCallModel{},
		Findings: []ephemeralFinding{}, ContextEnvelopes: map[string]crypto.Envelope{},
		Run: persistence.ReviewRunModel{
			ID: runID, State: string(state), Version: 1, CreatedAt: now,
			StatementCount: 1, FingerprintGroupCount: 1,
		},
		CreatedAt: now, expiresAt: now.Add(s.Ephemeral.TTL()),
	}
	if state != domain.RunStateQueued && state != domain.RunStateRunning {
		er.Run.FinishedAt = &now
		er.FinishedAt = &now
	}
	s.Ephemeral.put(er)
	return er, nil
}

// EphemeralGetForTest is a test read seam.
func (s *Services) EphemeralGetForTest(runID, ownerUserID string) *EphemeralRun {
	return s.Ephemeral.Get(runID, ownerUserID)
}

// IDForTest is a Run ID read seam.
func (er *EphemeralRun) IDForTest() string { return er.ID }

// TerminalForTest is a terminal-state read seam.
func (er *EphemeralRun) TerminalForTest() bool { return er.TerminalState() }
