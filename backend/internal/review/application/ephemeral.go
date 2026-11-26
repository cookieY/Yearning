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
	"bytes"
	"context"
	"encoding/hex"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"gorm.io/gorm"
	"strings"
	"sync"
	"time"
)

// maxUserActiveRuns caps concurrent active Runs per user; exceeding it triggers capacity rate limiting.
const maxUserActiveRuns = 10

// ephemeralMaxPerUser caps concurrent ephemeral Runs per user.
const ephemeralMaxPerUser = maxUserActiveRuns

// ephemeralMemoryIdemTTL is the ephemeral idempotency memory window (prevents double-click double-runs only, without DB persistence).
const ephemeralMemoryIdemTTL = 10 * time.Minute

const (
	// ephemeralSyncSyntaxCheckMaxBytes caps synchronous syntax validation in StartEphemeralRun;
	// larger batches validate asynchronously inside the single streaming grouping pass.
	ephemeralSyncSyntaxCheckMaxBytes = 256 * 1024

	// ephemeralMaxActiveSQLBytes caps total uncommitted raw SQL held across queued/grouping ephemeral Runs.
	ephemeralMaxActiveSQLBytes int64 = 256 << 20

	// ephemeralMaxTerminalRuns caps retained terminal ephemeral Runs per instance.
	ephemeralMaxTerminalRuns = 200

	// ephemeralInlineStatementLimit keeps per-statement DB models in memory only for small batches;
	// larger batches reconstruct rows incrementally at submit materialization time.
	ephemeralInlineStatementLimit = 500
)

// compactStmtValidityRef points to an interned StatementValidity and its group index in 8 pointer-free bytes.
type compactStmtValidityRef struct {
	validityIdx uint32
	groupIdx    uint16
}

type ephemeralGroupMeta struct {
	RepKind      string
	RepOperation string
	SizeMax      int64
	RepOrdinal   int32
}

// EphemeralRun holds the complete in-memory state of an ephemeral precheck: rows that the existing pipeline
// would persist to the DB are kept here as constructed, unwritten model structs and batch-inserted under
// the original constraints by the materializer upon submission.
type EphemeralRun struct {
	ID          string
	OwnerUserID string
	FlowID      string
	// DraftID is nullable: empty string for draftless trial runs in create mode; when provided, used only
	// for UI anchoring without writing to the draft during precheck.
	DraftID        string
	TargetDatabase string
	OutputLocale   string

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
	syntaxValidated        bool
	bulkSQL                bool
	skipStmtMaterialize    bool
	sqlReservedBytes       int64 // Guarded exclusively by EphemeralStore.mu

	mu                    sync.RWMutex
	state                 domain.RunState
	Run                   persistence.ReviewRunModel // Populated after grouping completes (running), converged upon reaching terminal state
	ErrCode               int32
	Failure               string
	stageIncompleteReason string

	analyzedStatementCount int32
	Statements             []persistence.SQLStatementModel
	Groups                 []persistence.SQLFingerprintGroupModel
	groupMeta              map[string]ephemeralGroupMeta
	repOrdinalToStmtID     map[int32]string
	repSQLByGroup          map[string]string
	validityTable          []fingerprint.StatementValidity
	validityRefs           []compactStmtValidityRef
	Snapshots              []persistence.StageReviewSnapshotModel
	Mapper                 *privacy.RunMapper

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

// EphemeralStore is the in-process ephemeral Run store (single-instance; empty on restart = ruling semantics).
type EphemeralStore struct {
	mu   sync.Mutex
	runs map[string]*EphemeralRun
	idem map[string]ephemeralIdemEntry

	activeSQLBytes    int64
	maxActiveSQLBytes int64
	maxTerminalRuns   int

	ttl       time.Duration
	sweepOnce sync.Once
	stop      chan struct{}
}

type ephemeralIdemEntry struct {
	RunID     string
	CreatedAt time.Time
}

func (st *EphemeralStore) activeOfUser(ownerUserID string) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.activeOfUserLocked(ownerUserID)
}

// EphemeralStartInput holds input parameters for starting an ephemeral precheck.
type EphemeralStartInput struct {
	FlowID            string
	DraftID           string // Optional; when provided, verifies ownership and flow consistency without writing to the draft
	SQL               []byte
	TargetDatabase    string
	TargetDatabaseSet bool // True when target_database was explicitly provided (including null to clear draft fallback)
	OutputLocale      string
	IdempotencyKey    string
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
	targetDB := strings.TrimSpace(in.TargetDatabase)
	if targetDB != "" {
		if err := ValidateTargetDatabase(targetDB); err != nil {
			return "", err
		}
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
						trimmedMAC, terr := s.Crypter.HMAC(sqlHMACPurpose, bytes.TrimSpace(in.SQL))
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
			targetDB, err = resolveEphemeralDraftTargetDatabase(targetDB, in.TargetDatabaseSet, draft.TargetDatabase)
			if err != nil {
				return err
			}
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
	var (
		statementCnt    int
		syntaxValidated bool
	)
	if len(in.SQL) <= ephemeralSyncSyntaxCheckMaxBytes {
		cnt, cntErr := s.countAndValidateStatements(in.SQL, profile)
		if cntErr != nil {
			return "", cntErr
		}
		if cnt < 1 || cnt > 100000 {
			return "", domain.ErrBatchTooLarge
		}
		statementCnt = cnt
		syntaxValidated = true
	} else if len(bytes.TrimSpace(in.SQL)) == 0 {
		return "", domain.ErrBatchTooLarge
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
		TargetDatabase: targetDB,
		OutputLocale:   ResolveOutputLocale(in.OutputLocale),
		SQLPlaintext:   in.SQL, SQLHMAC: sqlHMAC,
		Policy: policy, FlowHash: flowHash, RuleHash: ruleHash, ToolHash: toolHash,
		FlowVersion: flow.Version, FlowVersionSeen: flowVersionSeen,
		Profile: profile, ExpectedStatementCount: statementCnt,
		syntaxValidated: syntaxValidated,
		bulkSQL:         len(in.SQL) > 1<<20,
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
	if err := s.Ephemeral.admit(er, ephemeralMaxPerUser); err != nil {
		return "", err
	}
	s.Ephemeral.rememberIdem(actor.UserID, in.IdempotencyKey, er.ID)
	s.Ephemeral.sweep()
	go runner.ExecuteEphemeralRun(context.Background(), er)
	return runID, nil
}
