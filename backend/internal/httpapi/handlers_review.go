// handlers_review.go review domain HTTP handlers: drafts, Runs, findings, and evidence.
// Responses follow OpenAPI envelopes; SQL / Raw Evidence plaintext appears only in explicit Reveal responses
// with no-store; business actor relationship failures return 1002 (without leaking existence).
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/platform/pagecursor"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/tasks"
)

// B6Deps aggregates B6 domain dependencies.
type B6Deps struct {
	Review   *application.Services
	Sessions *identity.SessionStore
	DB       *gorm.DB

	agentOnce sync.Once
	agent     *application.AgentRunner
}

// AgentRunner singleton executor for ephemeral prechecks
// (RCP-20261101-EPHEMERAL-PRECHECK: precheck no longer uses the DB task chain).
func (d *B6Deps) AgentRunner() *application.AgentRunner {
	d.agentOnce.Do(func() {
		deps := application.AgentDeps{
			Dial: func(ep provider.Endpoint) (provider.Client, error) {
				return provider.NewOpenAIAdapter(ep, nil)
			},
			FactSourceFor: application.AdapterFactSourceFor(d.DB, d.Review.Crypter),
		}
		d.agent = application.NewAgentRunner(d.Review, deps)
	})
	return d.agent
}

// buildB6FromB3 constructs B6 from B3 platform dependencies (serve wiring path).
func buildB6FromB3(b3 *B3Deps) (*B6Deps, error) {
	recorder := b3.Recorder
	if recorder == nil {
		var err error
		recorder, err = auditlog.NewRecorder(b3.DB)
		if err != nil {
			return nil, err
		}
	}
	svc, err := application.NewServices(b3.DB, b3.Crypter, recorder, nil)
	if err != nil {
		return nil, err
	}
	return &B6Deps{Review: svc, Sessions: b3.Sessions, DB: b3.DB}, nil
}

// uuidParam validates that the path parameter is a UUID; writes 1001 and returns false if invalid.
func uuidParam(c *gin.Context, name string) (string, bool) {
	raw := c.Param(name)
	if _, err := uuid.Parse(raw); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid resource identifier"})
		return "", false
	}
	return raw, true
}

// b6Actor extracts the current user.
func b6Actor(c *gin.Context) application.Actor {
	id := session.FromContext(c.Request.Context())
	return application.Actor{UserID: id.UserID, Username: id.Username}
}

func b6Meta(c *gin.Context) application.RequestMeta {
	return application.RequestMeta{
		RequestID: RequestID(c), SourceIP: c.ClientIP(), UserAgent: c.GetHeader("User-Agent"),
	}
}

// ---- Drafts ----

type createDraftRequest struct {
	FlowID      string  `json:"flow_id" binding:"required,uuid"`
	Title       string  `json:"title" binding:"required,min=1,max=256"`
	Description *string `json:"description"`
	// S18.56 Option B: submitter requested execution schedule (RFC3339, optional, must be in the future).
	RequestedScheduledFor *string `json:"requested_scheduled_for"`
	RollbackEnabled       *bool   `json:"rollback_enabled"`
	TargetDatabase        *string `json:"target_database"`
}

func (d *B6Deps) HandleCreateChangeDraft(c *gin.Context) {
	var req createDraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	desc := ""
	if req.Description != nil {
		if len(*req.Description) > 4096 {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Description is too long"})
			return
		}
		desc = *req.Description
	}
	var requested *time.Time
	if req.RequestedScheduledFor != nil {
		var err error
		requested, err = application.ParseRequestedSchedule(*req.RequestedScheduledFor, time.Now().UTC())
		if err != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "requested_scheduled_for must be a future RFC3339 timestamp"})
			return
		}
	}
	view, err := d.Review.CreateDraft(c.Request.Context(), b6Actor(c), application.CreateDraftInput{
		FlowID: req.FlowID, Title: req.Title, Description: desc,
		RequestedScheduledFor: requested, RollbackEnabled: req.RollbackEnabled,
		TargetDatabase: req.TargetDatabase,
	})
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B6Deps) HandleListChangeDrafts(c *gin.Context) {
	var afterID, afterTS string
	if raw := c.Query("after"); raw != "" {
		ts, id, err := pagecursor.DecodeUUID(raw)
		if err != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid pagination cursor"})
			return
		}
		afterTS, afterID = ts.Format(pagecursor.Format), id
	}
	view, next, err := d.Review.ListDrafts(c.Request.Context(), b6Actor(c), intQuery(c, "limit", 50), afterID, afterTS)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, view, next)
}

func (d *B6Deps) HandleGetChangeDraft(c *gin.Context) {
	draftID, ok := uuidParam(c, "draft_id")
	if !ok {
		return
	}
	view, err := d.Review.GetDraft(c.Request.Context(), b6Actor(c), draftID)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

type updateDraftRequest struct {
	Title       *string `json:"title" binding:"omitempty,min=1,max=256"`
	Description *string `json:"description" binding:"omitempty,max=4096"`
	FlowID      *string `json:"flow_id" binding:"omitempty,uuid"`
	// S18.56 Option B tri-state: non-pointer RawMessage distinguishes absent (len==0, unchanged) /
	// null (clear) / value (set) -- a pointer would become nil on null and be indistinguishable from absent.
	RequestedScheduledForRaw json.RawMessage `json:"requested_scheduled_for" binding:"omitempty"`
	RollbackEnabled          *bool           `json:"rollback_enabled" binding:"omitempty"`
	TargetDatabaseRaw        json.RawMessage `json:"target_database" binding:"omitempty"`
}

func (d *B6Deps) HandleUpdateChangeDraft(c *gin.Context) {
	draftID, ok := uuidParam(c, "draft_id")
	if !ok {
		return
	}
	var req updateDraftRequest
	if err := c.ShouldBindJSON(&req); err != nil ||
		(req.Title == nil && req.Description == nil && req.FlowID == nil &&
			len(req.RequestedScheduledForRaw) == 0 && req.RollbackEnabled == nil && len(req.TargetDatabaseRaw) == 0) {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	var requested **time.Time
	if len(req.RequestedScheduledForRaw) > 0 {
		var raw any
		if err := json.Unmarshal(req.RequestedScheduledForRaw, &raw); err != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
			return
		}
		var inner *time.Time
		if raw != nil {
			text, isString := raw.(string)
			if !isString {
				WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "requested_scheduled_for must be an RFC3339 string or null"})
				return
			}
			parsed, err := application.ParseRequestedSchedule(text, time.Now().UTC())
			if err != nil {
				WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "requested_scheduled_for must be a future RFC3339 timestamp"})
				return
			}
			inner = parsed
		}
		requested = &inner
	}
	targetDBSet, innerTargetDB, ok := parseOptionalTargetDatabaseRaw(c, req.TargetDatabaseRaw)
	if !ok {
		return
	}
	var targetDB **string
	if targetDBSet {
		targetDB = &innerTargetDB
	}
	view, err := d.Review.UpdateDraft(c.Request.Context(), b6Actor(c), draftID, application.UpdateDraftInput{
		Title: req.Title, Description: req.Description, FlowID: req.FlowID,
		RequestedScheduledFor: requested, RollbackEnabled: req.RollbackEnabled,
		TargetDatabase: targetDB,
		ExpectVersion:  version,
	})
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B6Deps) HandleDeleteChangeDraft(c *gin.Context) {
	draftID, ok := uuidParam(c, "draft_id")
	if !ok {
		return
	}
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	if err := d.Review.DeleteDraft(c.Request.Context(), b6Actor(c), draftID, version); err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

type replaceSqlRequest struct {
	SQL string `json:"sql" binding:"required,max=33554432"`
}

func (d *B6Deps) HandleReplaceDraftSql(c *gin.Context) {
	draftID, ok := uuidParam(c, "draft_id")
	if !ok {
		return
	}
	var req replaceSqlRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	view, err := d.Review.ReplaceSQL(c.Request.Context(), b6Actor(c), draftID, []byte(req.SQL), version)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

type revealRequest struct {
	Purpose string `json:"purpose" binding:"required,min=1,max=256"`
}

func (d *B6Deps) HandleRevealDraftSql(c *gin.Context) {
	draftID, ok := uuidParam(c, "draft_id")
	if !ok {
		return
	}
	var req revealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	res, err := d.Review.RevealDraftSQL(c.Request.Context(), b6Actor(c), b6Meta(c), draftID, req.Purpose)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, gin.H{
		"reveal_id": res.RevealID, "sql": res.SQL,
		"watermark": res.Watermark, "valid_until": res.ValidUntil,
	})
}

// ---- Run ----

// HandleStartEphemeralReview POST /review-runs: ephemeral precheck (RCP-20261101).
// body={flow_id, draft_id?, sql, output_locale?}; precheck writes zero DB rows and
// returns run_id for polling via GET /review-runs/{id}. Idempotency-Key is optional (in-memory
// deduplication to prevent duplicate runs on double click).
func parseOptionalTargetDatabaseRaw(c *gin.Context, raw json.RawMessage) (present bool, value *string, ok bool) {
	if len(raw) == 0 {
		return false, nil, true
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters",
		})
		return false, nil, false
	}
	if parsed == nil {
		return true, nil, true
	}
	text, isString := parsed.(string)
	if !isString {
		WriteBusinessError(c, &BusinessError{
			Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "target_database must be a string or null",
		})
		return false, nil, false
	}
	if err := application.ValidateTargetDatabase(text); err != nil {
		writeCodedError(c, err)
		return false, nil, false
	}
	trimmed := strings.TrimSpace(text)
	return true, &trimmed, true
}

func (d *B6Deps) HandleStartEphemeralReview(c *gin.Context) {
	var body struct {
		FlowID            string          `json:"flow_id"`
		DraftID           string          `json:"draft_id"`
		SQL               string          `json:"sql"`
		TargetDatabaseRaw json.RawMessage `json:"target_database"`
		OutputLocale      string          `json:"output_locale"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request body"})
		return
	}
	targetDBSet, targetDBPtr, ok := parseOptionalTargetDatabaseRaw(c, body.TargetDatabaseRaw)
	if !ok {
		return
	}
	var targetDB string
	if targetDBPtr != nil {
		targetDB = *targetDBPtr
	}
	outputLocale := strings.TrimSpace(body.OutputLocale)
	if outputLocale == "" {
		outputLocale = application.ResolveOutputLocale(c.GetHeader("Accept-Language"))
	}
	sqlBytes := []byte(body.SQL)
	body.SQL = "" // Release up to 32 MiB request string immediately for GC.
	runID, err := d.Review.StartEphemeralRun(c.Request.Context(), b6Actor(c), d.AgentRunner(),
		application.EphemeralStartInput{
			FlowID:            body.FlowID,
			DraftID:           body.DraftID,
			SQL:               sqlBytes,
			TargetDatabase:    targetDB,
			TargetDatabaseSet: targetDBSet,
			OutputLocale:      outputLocale,
			IdempotencyKey:    strings.TrimSpace(c.GetHeader("Idempotency-Key")),
		})
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, gin.H{"run_id": runID, "result_ref": "/review-runs/" + runID})
}

func (d *B6Deps) HandleGetReviewRun(c *gin.Context) {
	runID, ok := uuidParam(c, "review_run_id")
	if !ok {
		return
	}
	actor := b6Actor(c)
	if d.Review.LookupEphemeralRun(runID, actor.UserID) != nil {
		run, merr := d.Review.GetEphemeralRunOwned(c.Request.Context(), actor, runID, b6Meta(c))
		if merr != nil {
			if !writeCodedError(c, merr) {
				WriteProblem(c, http.StatusInternalServerError, "internal error")
			}
			return
		}
		WriteSuccess(c, run)
		return
	}
	run, err := d.Review.GetRunOwned(c.Request.Context(), actor, runID, b6Meta(c))
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, run)
}

func (d *B6Deps) HandleListReviewRunFindings(c *gin.Context) {
	runID, ok := uuidParam(c, "review_run_id")
	if !ok {
		return
	}
	actor := b6Actor(c)
	if d.Review.LookupEphemeralRun(runID, actor.UserID) != nil {
		items, next, merr := d.Review.ListEphemeralRunFindings(c.Request.Context(), actor,
			runID, intQuery(c, "limit", 50), c.Query("after"), b6Meta(c))
		if merr != nil {
			if !writeCodedError(c, merr) {
				WriteProblem(c, http.StatusInternalServerError, "internal error")
			}
			return
		}
		writePage(c, items, next)
		return
	}
	items, next, err := d.Review.ListRunFindings(c.Request.Context(), actor,
		runID, intQuery(c, "limit", 50), c.Query("after"), b6Meta(c))
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

// HandleGetTask implements the task-reader rule: task kind and aggregate reference determine
// actor relationship (RCP-20261021-GETTASK-EXECUTOR-READ / SEC-0059).
// Management tasks (e.g. B3 connection test) are readable only by builtin_admin; business tasks
// (ai_review/sql_grouping) are readable only by the owner of the draft associated with the Run;
// stage_execution tasks are readable only by the executor of the Attempt (initiated_by snapshot or
// stage frozen executor snapshot) -- builtin admin does not imply business relationship and is rejected
// just like regular users on non-management tasks; unrelated tasks return 403 (non-existent resources
// do not disclose existence to non-admins).
func (d *B6Deps) HandleGetTask(c *gin.Context) {
	taskID, ok := uuidParam(c, "task_id")
	if !ok {
		return
	}
	id := session.FromContext(c.Request.Context())
	var task tasks.AsyncTaskModel
	if err := d.DB.WithContext(c.Request.Context()).
		Where("id = ?", taskID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if id.HasAdminAccess() {
				WriteBusinessError(c, &BusinessError{Code: bizerr.CodeResourceNotFound, Name: "RESOURCE_NOT_FOUND", Message: "Resource not found"})
			} else {
				WriteProblem(c, http.StatusForbidden, "permission denied")
			}
			return
		}
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	switch task.Kind {
	case tasks.KindAIReview, tasks.KindSQLGrouping:
		// Relationship resolution: if run row exists -> draft owner; before grouping completes -> draft_id
		// carried in task input (so owner can always read progress throughout the two-step chain).
		if task.AggregateID == nil || !d.runOrTaskOwnedByUser(c, *task.AggregateID, task.Input, id.UserID) {
			WriteProblem(c, http.StatusForbidden, "permission denied")
			return
		}
	case execapp.KindStageExecution:
		// Execution progress relationship: task aggregate must point to execution_attempt;
		// executor (initiated_by snapshot or frozen executor snapshot) can read task metadata for their own Attempt;
		// submitter/reviewer receive 403 just like unrelated users (their progress visibility uses the existing
		// execution-attempts read surface). Admin falls into relationship check and misses, consistent with non-management task rejection.
		if task.AggregateType == nil || *task.AggregateType != execapp.AggregateTypeExecutionAttempt || task.AggregateID == nil {
			WriteProblem(c, http.StatusForbidden, "permission denied")
			return
		}
		allowed, err := execapp.CanReadAttemptAsExecutor(c.Request.Context(), d.DB, *task.AggregateID, id.UserID)
		if err != nil {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
			return
		}
		if !allowed {
			WriteProblem(c, http.StatusForbidden, "permission denied")
			return
		}
	default:
		// Management tasks (e.g. B3 connection tests) remain admin-exclusive.
		if !id.HasAdminAccess() {
			WriteProblem(c, http.StatusForbidden, "permission denied")
			return
		}
	}
	view := gin.H{
		"id": task.ID, "kind": task.Kind, "state": task.State,
		"progress": gin.H{
			"completed": task.ProgressCompleted, "total": task.ProgressTotal, "unit": task.ProgressUnit,
		},
		"created_at": task.CreatedAt, "updated_at": task.UpdatedAt,
	}
	if task.ResultRef != nil {
		view["result_ref"] = *task.ResultRef
	} else {
		view["result_ref"] = nil
	}
	if task.FailureName != nil && task.State != "succeeded" {
		view["error"] = gin.H{"title": *task.FailureName}
	} else {
		view["error"] = nil
	}
	WriteSuccess(c, view)
}

// runOrTaskOwnedByUser resolves ownership via run row first, falling back to draft_id in task
// input when run row does not exist (before grouping or on failure).
func (d *B6Deps) runOrTaskOwnedByUser(c *gin.Context, runID string, input []byte, userID string) bool {
	var owner string
	err := d.DB.WithContext(c.Request.Context()).Raw(`
		select d.owner_user_id::text from review_runs r
		join change_drafts d on d.id = r.draft_id
		where r.id = ?`, runID).Scan(&owner).Error
	if err == nil && owner != "" {
		return owner == userID
	}
	var parsed struct {
		DraftID string `json:"draft_id"`
	}
	if json.Unmarshal(input, &parsed) != nil || parsed.DraftID == "" {
		return false
	}
	owner = ""
	err = d.DB.WithContext(c.Request.Context()).Raw(
		`select owner_user_id::text from change_drafts where id = ?`, parsed.DraftID).Scan(&owner).Error
	return err == nil && owner == userID
}

// ---- Evidence ----

func (d *B6Deps) HandleListReviewFindingEvidence(c *gin.Context) {
	findingID, ok := uuidParam(c, "finding_id")
	if !ok {
		return
	}
	// RCP-20261101 ephemeral Run: evidence for pre-submission findings reads from in-memory copy (post-submission reads materialized rows).
	if er, _ := d.Review.EphemeralFindFinding(findingID, b6Actor(c).UserID); er != nil {
		items, merr := d.Review.ListEphemeralFindingEvidence(c.Request.Context(), b6Actor(c), findingID)
		if merr != nil {
			if !writeCodedError(c, merr) {
				WriteProblem(c, http.StatusInternalServerError, "internal error")
			}
			return
		}
		WriteSuccess(c, items)
		return
	}
	items, err := d.Review.ListFindingEvidence(c.Request.Context(), b6Actor(c), findingID)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, items)
}

func (d *B6Deps) HandleRevealRawReviewEvidence(c *gin.Context) {
	evidenceID, ok := uuidParam(c, "evidence_id")
	if !ok {
		return
	}
	var req revealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	res, err := d.Review.RevealRawEvidence(c.Request.Context(), b6Actor(c), b6Meta(c), evidenceID, req.Purpose)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		// F12 #012: writeReviewErr already answered mapped domain errors —
		// falling through dereferenced the nil result and panicked.
		return
	}
	c.Header("Cache-Control", "no-store")
	WriteSuccess(c, gin.H{
		"reveal_id": res.RevealID, "content_type": res.ContentType,
		"raw_payload": string(res.RawPayload), "watermark": res.Watermark,
		"valid_until": res.ValidUntil.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	})
}

type copyAuditRequest struct {
	SourceRevealID string `json:"source_reveal_id" binding:"required,uuid"`
}

func (d *B6Deps) HandleRecordRawReviewEvidenceCopy(c *gin.Context) {
	evidenceID, ok := uuidParam(c, "evidence_id")
	if !ok {
		return
	}
	var req copyAuditRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if err := d.Review.RecordEvidenceCopy(c.Request.Context(), b6Actor(c), b6Meta(c),
		evidenceID, req.SourceRevealID); err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

// identifierMappingRevealRequest is the mapping reveal request: mutually exclusive tokens vs all.
type identifierMappingRevealRequest struct {
	Tokens []string `json:"tokens"`
	All    bool     `json:"all"`
}

// HandleRevealIdentifierMapping RCP-20261022: reveals anonymized identifier mappings for a Run
// (draft owner only, audited per call, no-store response, plaintext never logged).
func (d *B6Deps) HandleRevealIdentifierMapping(c *gin.Context) {
	runID, ok := uuidParam(c, "review_run_id")
	if !ok {
		return
	}
	var req identifierMappingRevealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if req.All && len(req.Tokens) > 0 || !req.All && len(req.Tokens) == 0 {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Exactly one of tokens or all must be specified"})
		return
	}
	res, err := d.Review.RevealIdentifierMapping(c.Request.Context(), b6Actor(c), b6Meta(c), runID, req.Tokens, req.All)
	if err != nil {
		if !writeCodedError(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	mappings := make([]gin.H, 0, len(res.Mappings))
	for _, m := range res.Mappings {
		mappings = append(mappings, gin.H{"token": m.Token, "kind": m.Kind, "identifier": m.Identifier})
	}
	WriteSuccess(c, gin.H{"review_run_id": res.RunID, "mappings": mappings, "revealed_at": res.RevealedAt})
}
