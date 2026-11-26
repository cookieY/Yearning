// ephemeral_launch.go holds ephemeral domain helpers and operations.
package application

import (
	"context"
	"encoding/hex"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"gorm.io/gorm"
	"sort"
	"strings"
	"time"
)

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

func (er *EphemeralRun) trackedStatementCountLocked() int64 {
	if len(er.Statements) > 0 {
		return int64(len(er.Statements))
	}
	return int64(er.analyzedStatementCount)
}

// terminalExpiredAt atomically inspects both expiresAt and state under er.mu.RLock
// so concurrent ephemeralFinish updates never race with EphemeralStore.Get / Take / sweepNow.
func (er *EphemeralRun) terminalExpiredAt(now time.Time) bool {
	er.mu.RLock()
	defer er.mu.RUnlock()
	return er.expiresAt.Before(now) && domain.RunTerminal(er.state)
}

// NewEphemeralStore constructs the store; ttl is the retention window for terminal results.
func NewEphemeralStore(ttl time.Duration) *EphemeralStore {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &EphemeralStore{
		runs:              map[string]*EphemeralRun{},
		idem:              map[string]ephemeralIdemEntry{},
		maxActiveSQLBytes: ephemeralMaxActiveSQLBytes,
		maxTerminalRuns:   ephemeralMaxTerminalRuns,
		ttl:               ttl,
		stop:              make(chan struct{}),
	}
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
	st.sweepLocked(now)
}

func (st *EphemeralStore) sweepLocked(now time.Time) {
	for id, run := range st.runs {
		if run.terminalExpiredAt(now) {
			st.releaseSQLBytesLocked(run)
			delete(st.runs, id)
		}
	}
	for k, e := range st.idem {
		if now.Sub(e.CreatedAt) > ephemeralMemoryIdemTTL {
			delete(st.idem, k)
		}
	}
}

func (st *EphemeralStore) releaseSQLBytes(er *EphemeralRun) {
	if er == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.releaseSQLBytesLocked(er)
}

func (st *EphemeralStore) releaseSQLBytesLocked(er *EphemeralRun) {
	if er == nil || er.sqlReservedBytes <= 0 {
		return
	}
	st.activeSQLBytes -= er.sqlReservedBytes
	if st.activeSQLBytes < 0 {
		st.activeSQLBytes = 0
	}
	er.sqlReservedBytes = 0
}

func (st *EphemeralStore) enforceTerminalCap() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.enforceTerminalCapLocked(time.Now())
}

func (st *EphemeralStore) enforceTerminalCapLocked(now time.Time) {
	st.sweepLocked(now)
	capLimit := st.maxTerminalRuns
	if capLimit <= 0 {
		capLimit = ephemeralMaxTerminalRuns
	}
	type candidate struct {
		id         string
		ready      bool
		finishedAt time.Time
	}
	var term []candidate
	for id, r := range st.runs {
		r.mu.RLock()
		state := r.state
		fin := r.CreatedAt
		if r.FinishedAt != nil {
			fin = *r.FinishedAt
		}
		r.mu.RUnlock()
		if domain.RunTerminal(state) {
			term = append(term, candidate{
				id:         id,
				ready:      state == domain.RunStateReady,
				finishedAt: fin,
			})
		}
	}
	if len(term) <= capLimit {
		return
	}
	// Evict oldest non-ready terminal runs first, then oldest ready terminal runs.
	sort.Slice(term, func(i, j int) bool {
		if term[i].ready != term[j].ready {
			return !term[i].ready
		}
		return term[i].finishedAt.Before(term[j].finishedAt)
	})
	toEvict := len(term) - capLimit
	for i := 0; i < toEvict; i++ {
		id := term[i].id
		if r, ok := st.runs[id]; ok {
			st.releaseSQLBytesLocked(r)
			delete(st.runs, id)
		}
	}
}

func (st *EphemeralStore) admit(er *EphemeralRun, maxActivePerUser int) error {
	if er == nil {
		return nil
	}
	now := time.Now()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.runs == nil {
		st.runs = map[string]*EphemeralRun{}
	}
	st.sweepLocked(now)
	if st.activeOfUserLocked(er.OwnerUserID) >= maxActivePerUser {
		return &domain.Error{Code: 1012, Name: "SYSTEM_CAPACITY_EXHAUSTED"}
	}
	maxBytes := st.maxActiveSQLBytes
	if maxBytes <= 0 {
		maxBytes = ephemeralMaxActiveSQLBytes
	}
	need := int64(len(er.SQLPlaintext))
	if need > 0 && st.activeSQLBytes+need > maxBytes {
		return &domain.Error{Code: 1012, Name: "SYSTEM_CAPACITY_EXHAUSTED"}
	}
	er.sqlReservedBytes = need
	st.activeSQLBytes += need
	st.runs[er.ID] = er
	st.enforceTerminalCapLocked(now)
	return nil
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

func (er *EphemeralRun) readyAndUnexpiredAt(now time.Time) bool {
	er.mu.RLock()
	defer er.mu.RUnlock()
	return er.state == domain.RunStateReady && !er.expiresAt.Before(now)
}

// Take consumes a Run: retrieves and removes it (test/internal helper). Returns nil on ownership mismatch or expiry.
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
	st.releaseSQLBytesLocked(run)
	delete(st.runs, runID)
	return run
}

// TakeReady atomically verifies ownership, ready state, and TTL before removing a Run for submission.
// Non-ready runs stay untouched in st.runs with their activeSQLBytes reservation intact.
func (st *EphemeralStore) TakeReady(runID, ownerUserID string) *EphemeralRun {
	st.mu.Lock()
	defer st.mu.Unlock()
	run, ok := st.runs[runID]
	if !ok || run.OwnerUserID != ownerUserID {
		return nil
	}
	if !run.readyAndUnexpiredAt(time.Now()) {
		return nil
	}
	st.releaseSQLBytesLocked(run)
	delete(st.runs, runID)
	return run
}

func (st *EphemeralStore) put(run *EphemeralRun) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.runs == nil {
		st.runs = map[string]*EphemeralRun{}
	}
	st.runs[run.ID] = run
}

// putBack restores a Run after a failed submission transaction, re-accounting any unreserved SQL bytes
// and enforcing the terminal run cap under strict lock ordering (st.mu -> run.mu).
func (st *EphemeralStore) putBack(run *EphemeralRun) {
	if run == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.runs == nil {
		st.runs = map[string]*EphemeralRun{}
	}
	run.mu.RLock()
	sqlLen := int64(len(run.SQLPlaintext))
	run.mu.RUnlock()
	if run.sqlReservedBytes == 0 && sqlLen > 0 {
		run.sqlReservedBytes = sqlLen
		st.activeSQLBytes += sqlLen
	}
	st.runs[run.ID] = run
	st.enforceTerminalCapLocked(time.Now())
}

func (st *EphemeralStore) activeOfUserLocked(ownerUserID string) int {
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

func resolveEphemeralDraftTargetDatabase(targetDB string, targetDBSet bool, draftTargetDB *string) (string, error) {
	if !targetDBSet && targetDB == "" && draftTargetDB != nil {
		targetDB = strings.TrimSpace(*draftTargetDB)
		if targetDB != "" {
			if err := ValidateTargetDatabase(targetDB); err != nil {
				return "", err
			}
		}
	}
	return targetDB, nil
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
	s.Ephemeral.putBack(er)
}

// EphemeralTTL returns the terminal retention window (used at finish).
func (s *Services) EphemeralTTL() time.Duration { return s.Ephemeral.ttl }

// EphemeralTake consumes a ready ephemeral Run for submission (changeorder submit v2 entry; ownership and ready-state checks built in).
func (s *Services) EphemeralTake(runID, ownerUserID string) *EphemeralRun {
	return s.Ephemeral.TakeReady(runID, ownerUserID)
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
		skipStmtMaterialize: true,
		state:               state, Mapper: privacy.NewRunMapper(),
		Facts: []ephemeralFact{}, ToolCalls: []persistence.ReviewToolCallModel{},
		Findings: []ephemeralFinding{}, ContextEnvelopes: map[string]crypto.Envelope{},
		Run: persistence.ReviewRunModel{
			ID: runID, State: string(state), BudgetDay: now.Format("2006-01-02"), Version: 1, CreatedAt: now,
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

// SeedSubmitReadyEphemeralRunForTest directly inserts a ready ephemeral Run populated with
// frozen policy hashes and stage snapshots for changeorder submit v2 integration tests.
func (s *Services) SeedSubmitReadyEphemeralRunForTest(
	runID, ownerUserID, flowID string, flowVersion int64,
	flowHash, ruleHash, toolHash, providerPlanHash string,
	sqlText []byte, snapshots []persistence.StageReviewSnapshotModel,
) (*EphemeralRun, error) {
	er, err := s.SeedEphemeralRunForTest(runID, ownerUserID, flowID, domain.RunStateReady, sqlText)
	if err != nil {
		return nil, err
	}
	er.mu.Lock()
	defer er.mu.Unlock()
	er.FlowVersion = flowVersion
	er.Run.FlowSnapshotHash = flowHash
	er.Run.RuleConfigHash = ruleHash
	er.Run.ToolConfigHash = toolHash
	er.Run.ProviderPlanHash = providerPlanHash
	er.Run.FingerprintAlgorithmVersion = "v1"
	er.Run.AnonymizationAlgorithmVersion = "v1"
	er.Snapshots = snapshots
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

// PrepareStreamingSubmitForTest configures a ready EphemeralRun as a streaming (>500 statement)
// batch with dropped SQLPlaintext/Statements and representative statement ID mappings for submit tests.
func (er *EphemeralRun) PrepareStreamingSubmitForTest(
	profile fingerprint.Profile,
	stmtCount int32,
	groups []persistence.SQLFingerprintGroupModel,
	repOrdinalToStmtID map[int32]string,
) {
	er.mu.Lock()
	defer er.mu.Unlock()
	er.Profile = profile
	er.skipStmtMaterialize = false
	er.SQLPlaintext = nil
	er.Statements = nil
	er.Run.StatementCount = stmtCount
	er.Run.FingerprintGroupCount = int32(len(groups))
	er.Groups = groups
	er.repOrdinalToStmtID = repOrdinalToStmtID
}
