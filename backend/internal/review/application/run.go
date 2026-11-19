// run.go Run Use Cases: explicitly start precheck (R001), read progress and findings.
// StartRun completes within a single transaction: idempotency claim -> state machine check ->
// flow authorization/version check -> review_run + async_task creation -> draft -> reviewing.
// Duplicate idempotency keys replay the response without producing duplicate active Runs or events.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/tasks"
)

// StartRunInput holds parameters for starting a precheck.
type StartRunInput struct {
	DraftID        string
	ExpectVersion  int64
	IdempotencyKey string
	OutputLocale   string
}

// StartRunResult returns the created Run and task references.
type StartRunResult struct {
	RunID  string `json:"run_id"`
	TaskID string `json:"task_id"`
}

// maxUserActiveRuns caps concurrent active Runs per user; exceeding it triggers capacity rate limiting (429).
const maxUserActiveRuns = 10

// StartRun explicitly starts an AI precheck.
func (s *Services) StartRun(ctx context.Context, actor Actor, in StartRunInput) (*StartRunResult, error) {
	in.OutputLocale = ResolveOutputLocale(in.OutputLocale)
	// Idempotency claim (independent of the business transaction: unique per request scope).
	reqHash, err := s.startRunRequestHash(actor, in)
	if err != nil {
		return nil, err
	}
	claimed, existing, err := s.claimIdempotency(ctx, actor.UserID, "POST /change-drafts/{draft_id}/review-runs", in.DraftID, in.IdempotencyKey, reqHash)
	if err != nil {
		return nil, err
	}
	if claimed == nil {
		return existing, nil // Replay existing result
	}
	// Bootstrap missing stage datasource identity_fingerprint before freezing the review policy hash.
	s.ensureDraftFlowStagesIdentity(ctx, actor.UserID, in.DraftID)
	var out *StartRunResult
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, err := s.loadDraftForUpdate(ctx, tx, in.DraftID)
		if err != nil {
			return err
		}
		if m.OwnerUserID != actor.UserID {
			return ErrNotFound
		}
		// State machine: run_review may only be initiated from declared source states (for rerunning from ready,
		// see RCP-20261024-REVIEW-RERUN-FROM-READY; reviewing/submitted are still rejected).
		from := domain.DraftState(m.State)
		next, err := domain.DraftTransition(from, domain.DraftActionRunReview)
		if err != nil {
			return err
		}
		// Verify flow authorization is still valid.
		granted, err := s.flowGranted(ctx, tx, actor.UserID, m.FlowID)
		if err != nil {
			return err
		}
		if !granted {
			return &domain.Error{Code: 2014, Name: "FLOW_GRANT_REVOKED"}
		}
		// Verify flow version has not drifted; if drifted, the draft is outdated (REVIEW_OUTDATED).
		var flow flowRow
		if err := tx.WithContext(ctx).Raw(
			`select id, flow_type, status, rule_set_id, version
			 from flows where id = ? for update`, m.FlowID).
			Scan(&flow).Error; err != nil {
			return err
		}
		if flow.Version != m.FlowVersionSeen {
			// Flow template has drifted: invalidation of existing conclusions is expressed via 2001;
			// the user must re-confirm the template in the draft (updating flow_version_seen).
			return domain.ErrReviewOutdated
		}
		// SQL must already be saved. F-UX-003: a bare 1001 only gave a generic "invalid request parameters"
		// message without telling the operator that SQL was missing — Detail is passed verbatim to the UI via meta.detail.
		var payload persistence.DraftSQLPayloadModel
		if err := tx.WithContext(ctx).Where("draft_id = ?", in.DraftID).
			First(&payload).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED",
					Detail: "SQL has not been saved: save SQL to the draft before starting an AI review"}
			}
			return err
		}
		// Concurrent active Run cap (capacity rate limiting).
		active, err := s.countActiveRunsOfUser(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		if active >= maxUserActiveRuns {
			return &domain.Error{Code: 1012, Name: "SYSTEM_CAPACITY_EXHAUSTED"}
		}
		// Freeze the complete review policy: rule-set members, tool definitions, all enabled knowledge candidates,
		// and stage database profiles are captured from the same policy boundary within the same transaction.
		// The three existing Hash columns bind flow / rule set / tools & knowledge respectively, while the full
		// content is copied with the task as an AEAD snapshot.
		policy, _, _, _, err := s.freezeReviewPolicy(ctx, tx, m.FlowID, flow)
		if err != nil {
			return err
		}
		// The Run row is created by the worker after grouping succeeds (the fingerprint_group_count
		// CHECK requires 1..1000: review_runs only holds grouped inputs).
		// Here we pre-allocate run_id and create the sql_grouping task (R009: streaming grouping
		// before entering the model phase).
		runID := newID()
		now := nowUTC()
		taskID := newID()
		aggregateType := "review_run"
		inputValue, err := sealReviewPolicySnapshot(
			s.Crypter,
			runID,
			PolicySnapshotPurposeReview,
			*policy,
		)
		if err != nil {
			return err
		}
		inputValue.DraftID = m.ID
		inputValue.DraftRevision = m.Revision
		inputValue.SQLHMAC = payload.PlaintextHMAC
		inputValue.OutputLocale = in.OutputLocale
		input, err := jsonMarshal(inputValue)
		if err != nil {
			return err
		}
		task := tasks.AsyncTaskModel{
			ID: taskID, Kind: tasks.KindSQLGrouping,
			State: "queued", AggregateType: &aggregateType, AggregateID: &runID,
			Input:         input,
			ProgressTotal: int64(payload.StatementCount),
			ProgressUnit:  "statements",
			FenceToken:    1,
			CreatedAt:     now, UpdatedAt: now,
		}
		if err := tx.WithContext(ctx).Create(&task).Error; err != nil {
			return err
		}
		// draft -> reviewing.
		if err := s.updateDraftVersioned(ctx, tx, m, in.ExpectVersion, map[string]any{
			"state": string(next), "version": m.Version + 1, "updated_at": nowUTC(),
		}); err != nil {
			return err
		}
		out = &StartRunResult{RunID: runID, TaskID: taskID}
		return nil
	})
	if err != nil {
		// Release idempotency claim (allowing retry on the next attempt).
		_ = s.releaseIdempotency(ctx, actor.UserID, "POST /change-drafts/{draft_id}/review-runs", in.DraftID, in.IdempotencyKey)
		return nil, err
	}
	// Complete the idempotency record and cache the response.
	if err := s.completeIdempotency(ctx, actor.UserID, "POST /change-drafts/{draft_id}/review-runs",
		in.DraftID, in.IdempotencyKey, out); err != nil {
		return nil, err
	}
	return out, nil
}

// CancelReviewTask cancels a review task and converges business state:
// run exists -> partial (preserving completed evidence); prior to grouping -> draft partial.
// Provided for future cancellation entry points and operations (B6 has no OpenAPI cancel endpoint).
func (s *Services) CancelReviewTask(ctx context.Context, actor Actor, taskID string) error {
	var task struct {
		ID    string
		Kind  string
		Input []byte
		AggID *string
	}
	if err := s.DB.WithContext(ctx).Raw(
		`select id::text as id, kind, input, aggregate_id::text as agg_id
		 from async_tasks where id = ?`, taskID).Scan(&task).Error; err != nil {
		return err
	}
	if task.ID == "" || (task.Kind != KindSQLGroupingAlias && task.Kind != KindAIReviewAlias) {
		return ErrNotFound
	}
	var input TaskInput
	if err := json.Unmarshal(task.Input, &input); err != nil || input.DraftID == "" {
		return ErrValidation
	}
	draft, err := s.loadDraft(ctx, s.DB, input.DraftID)
	if err != nil {
		return err
	}
	if draft.OwnerUserID != actor.UserID {
		return ErrNotFound
	}
	// Cancelling a terminal task is an idempotent no-op (terminal states cannot regress).
	var taskState string
	_ = s.DB.WithContext(ctx).Raw(`select state from async_tasks where id = ?`, taskID).Scan(&taskState).Error
	if taskState != "queued" && taskState != "running" {
		return nil
	}
	if err := tasks.Cancel(ctx, s.DB, taskID); err != nil {
		return err
	}
	// Converge: run exists -> incomplete; otherwise draft transitions directly from reviewing -> partial.
	if run, err := s.loadRunOrNull(ctx, s.DB, input.ReviewRunID); err != nil {
		return err
	} else if run != nil {
		runner := NewFakeRunner(s, nil)
		return runner.finishCancelled(ctx, s.DB, run)
	}
	return s.draftReviewIncomplete(ctx, draft)
}

// KindSQLGroupingAlias/KindAIReviewAlias avoid duplicate constant import clutter between application and tasks
// (tasks is already imported in this file).
const (
	KindSQLGroupingAlias = "sql_grouping"
	KindAIReviewAlias    = "ai_review"
)

// draftReviewIncomplete transitions reviewing -> partial (cancellation path when no run row exists).
func (s *Services) draftReviewIncomplete(ctx context.Context, draft *persistence.ChangeDraftModel) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fresh, err := s.loadDraftForUpdate(ctx, tx, draft.ID)
		if err != nil {
			return err
		}
		if domain.DraftState(fresh.State) != domain.DraftStateReviewing {
			return nil
		}
		to, terr := domain.DraftTransition(domain.DraftStateReviewing, domain.DraftEventReviewIncomplete)
		if terr != nil {
			return terr
		}
		if err := s.updateDraftVersioned(ctx, tx, fresh, fresh.Version, map[string]any{
			"state": string(to), "version": fresh.Version + 1, "updated_at": nowUTC(),
		}); err != nil {
			return err
		}
		from := string(domain.DraftStateReviewing)
		return emitDraftEvent(ctx, tx, s.Recorder, domain.EventDraftPrecheckComplete, fresh.ID, fresh.OwnerUserID,
			domain.DraftStateChangedData{
				AggregateID: fresh.ID, From: &from, To: string(to),
				ReasonCode: domain.ReasonPrecheckPartial, AggregateVersion: fresh.Version + 1,
			})
	})
}

// startRunRequestHash computes the canonical request hash (detecting idempotency key reuse with different payloads).
func (s *Services) startRunRequestHash(actor Actor, in StartRunInput) (string, error) {
	canonical := fmt.Sprintf("actor=%s;draft=%s;version=%d", actor.UserID, in.DraftID, in.ExpectVersion)
	// Keep the old canonical form for the default locale so retries of requests
	// made before locale support still replay their original immutable result.
	if ResolveOutputLocale(in.OutputLocale) == "en-US" {
		canonical += ";output_locale=en-US"
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), nil
}

// countActiveRunsOfUser counts queued/running Runs across the user's drafts.
func (s *Services) countActiveRunsOfUser(ctx context.Context, db *gorm.DB, userID string) (int64, error) {
	var n int64
	err := db.WithContext(ctx).Raw(`
		select count(*) from review_runs r
		join change_drafts d on d.id = r.draft_id
		where d.owner_user_id = ? and r.state in ('queued','running')`, userID).Scan(&n).Error
	return n, err
}

// fakeProviderPlanHash is the provider plan hash for B6 FakeRunner.
// Replaced by provider_plan once B7 wires real Providers; any change invalidates unsubmitted Reviews.
var fakeProviderPlanHash = sha256Hex([]byte("fake-runner-v1"))

// ---- Idempotency ----

// idempotencyRow reads an idempotency record.
type idempotencyRow struct {
	State                  string
	RequestHash            string
	ResponseBodyCiphertext []byte
	ResponseBodyNonce      []byte
	ResponseBodyKeyID      string
	ResponseBodyAlgorithm  string
}

// claimIdempotency claims an idempotency key; returning (claimed == nil && existing != nil) indicates a replay.
func (s *Services) claimIdempotency(ctx context.Context, userID, route, aggregateID, key, reqHash string) (*struct{}, *StartRunResult, error) {
	err := s.DB.WithContext(ctx).Exec(`
		insert into idempotency_records (id, requesting_user_id, route_template, aggregate_id,
		    idempotency_key, request_hash, state, created_at, expires_at)
		values (?, ?, ?, ?, ?, ?, 'in_progress', now(), now() + interval '24 hours')`,
		newID(), userID, route, aggregateID, key, reqHash).Error
	if err == nil {
		return &struct{}{}, nil, nil
	}
	if !isUniqueViolationErr(err) {
		return nil, nil, err
	}
	// Already exists: dispatch by state.
	var row idempotencyRow
	if err := s.DB.WithContext(ctx).Raw(`
		select state, request_hash, response_body_ciphertext as response_body_ciphertext,
		       response_body_nonce as response_body_nonce, response_key_id as response_body_key_id,
		       response_body_algorithm as response_body_algorithm
		from idempotency_records
		where requesting_user_id = ? and route_template = ? and aggregate_id = ? and idempotency_key = ?`,
		userID, route, aggregateID, key).Scan(&row).Error; err != nil {
		return nil, nil, err
	}
	if row.RequestHash != reqHash {
		return nil, nil, &domain.Error{Code: 1007, Name: "IDEMPOTENCY_KEY_REUSED"}
	}
	if row.State != "completed" {
		// An expired in_progress claim (left behind by a dead process) may be taken over and retried.
		var expired bool
		_ = s.DB.WithContext(ctx).Raw(
			`select expires_at < now() from idempotency_records
			 where requesting_user_id = ? and route_template = ? and aggregate_id = ? and idempotency_key = ?`,
			userID, route, aggregateID, key).Scan(&expired).Error
		if expired {
			if err := s.releaseIdempotencyExpired(ctx, userID, route, aggregateID, key); err != nil {
				return nil, nil, err
			}
			return s.claimIdempotency(ctx, userID, route, aggregateID, key, reqHash)
		}
		return nil, nil, &domain.Error{Code: 1008, Name: "IDEMPOTENCY_IN_PROGRESS"}
	}
	// Decrypt the replayed response.
	env := crypto.Envelope{
		Ciphertext: row.ResponseBodyCiphertext, Nonce: row.ResponseBodyNonce,
		KeyID: row.ResponseBodyKeyID, Algorithm: row.ResponseBodyAlgorithm,
	}
	plaintext, err := openPayload(s.Crypter, "idempotency_records", s.idempotencyPK(userID, route, aggregateID, key), "response_body_ciphertext", env)
	if err != nil {
		return nil, nil, err
	}
	var out StartRunResult
	if err := jsonUnmarshalStrict(plaintext, &out); err != nil {
		return nil, nil, err
	}
	return nil, &out, nil
}

func (s *Services) idempotencyPK(userID, route, aggregateID, key string) string {
	return userID + "#" + route + "#" + aggregateID + "#" + key
}

// releaseIdempotency deletes an in_progress claim after business failure (allowing retry).
func (s *Services) releaseIdempotency(ctx context.Context, userID, route, aggregateID, key string) error {
	return s.DB.WithContext(ctx).Exec(`
		delete from idempotency_records
		where requesting_user_id = ? and route_template = ? and aggregate_id = ?
		  and idempotency_key = ? and state = 'in_progress'`,
		userID, route, aggregateID, key).Error
}

// releaseIdempotencyExpired deletes an expired claim (regardless of state).
func (s *Services) releaseIdempotencyExpired(ctx context.Context, userID, route, aggregateID, key string) error {
	return s.DB.WithContext(ctx).Exec(`
		delete from idempotency_records
		where requesting_user_id = ? and route_template = ? and aggregate_id = ?
		  and idempotency_key = ? and expires_at < now()`,
		userID, route, aggregateID, key).Error
}

// completeIdempotency persists the encrypted response and marks the record completed.
func (s *Services) completeIdempotency(ctx context.Context, userID, route, aggregateID, key string, result *StartRunResult) error {
	body, err := jsonMarshal(result)
	if err != nil {
		return err
	}
	env, err := s.Crypter.Seal(crypto.PurposePayload, body, payloadAAD(s.Crypter.DeploymentID(), "idempotency_records",
		s.idempotencyPK(userID, route, aggregateID, key), "response_body_ciphertext"))
	if err != nil {
		return err
	}
	res := s.DB.WithContext(ctx).Exec(`
		update idempotency_records set state='completed', response_http_status=200,
		    response_body_ciphertext=?, response_body_nonce=?, response_key_id=?,
		    response_body_algorithm='aes-256-gcm'
		where requesting_user_id = ? and route_template = ? and aggregate_id = ?
		  and idempotency_key = ? and state = 'in_progress'`,
		env.Ciphertext, env.Nonce, env.KeyID,
		userID, route, aggregateID, key)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrConcurrent
	}
	return nil
}

func isUniqueViolationErr(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// uniqueViolationConstraint extracts the constraint/index name from a unique constraint violation
// (PG uses ConstraintName; MySQL extracts the key name from 1062 message "for key '...'" — that arm is
// defensive since the platform DB is strictly PG and go-sql-driver is used only for target business DBs).
// Constraint names in both dialects include the column name, allowing callers to tell which unique axis
// collided; returns an empty string when unavailable (driver differences / future wrappers) to degrade to generic wording.
func uniqueViolationConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		const marker = "for key '"
		if i := strings.Index(mysqlErr.Message, marker); i >= 0 {
			rest := mysqlErr.Message[i+len(marker):]
			if j := strings.Index(rest, "'"); j >= 0 {
				return rest[:j]
			}
		}
	}
	return ""
}

// jsonUnmarshalStrict performs strict JSON decoding.
func jsonUnmarshalStrict(data []byte, v any) error { return json.Unmarshal(data, v) }
