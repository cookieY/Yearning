// draft.go Draft Use Cases: create / list / get / update / delete / SQL replacement / plaintext Reveal.
// Modifications follow the save_change_draft_revision command: row-lock draft, version CAS,
// SQL envelope and hash matching Revision, and change_draft.revised event in the same transaction.
package application

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/changeset"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
)

// DraftView is the draft read model (ChangeDraft schema allowlist).
type DraftView struct {
	ID          string `json:"id"`
	OwnerUserID string `json:"owner_user_id"`
	FlowID      string `json:"flow_id"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// Submitter requested execution time (section 18.56 Option B): RFC3339; nil = unset.
	RequestedScheduledFor    *string `json:"requested_scheduled_for,omitempty"`
	RollbackEnabled          bool    `json:"rollback_enabled"`
	TicketType               string  `json:"ticket_type"`
	SourceOrderID            *string `json:"source_order_id,omitempty"`
	SourceOrderDisplayNumber *string `json:"source_order_display_number,omitempty"`
	RollbackEvidenceID       *string `json:"rollback_evidence_id,omitempty"`
	Revision                 int64   `json:"revision"`
	State                    string  `json:"state"`
	HasSQL                   bool    `json:"has_sql"`
	SQLSizeBytes             int64   `json:"sql_size_bytes"`
	StatementCount           int64   `json:"statement_count"`
	ReviewRunID              *string `json:"review_run_id"`
	Version                  int64   `json:"version"`
	CreatedAt                string  `json:"created_at"`
	UpdatedAt                string  `json:"updated_at"`
}

// CreateDraftInput holds draft creation parameters.
type CreateDraftInput struct {
	FlowID                string
	Title                 string
	Description           string
	RequestedScheduledFor *time.Time
	RollbackEnabled       *bool
}

// ParseRequestedSchedule parses and validates the requested execution time (must be in the future).
// Purely advisory: does not check execution windows (windows are checked when the executor schedules
// and when execution time arrives) and does not enforce the 5-minute to 30-day range (approval duration
// is unpredictable; scheduling still validates per existing rule 3007).
func ParseRequestedSchedule(value string, now time.Time) (*time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil || !t.After(now) {
		return nil, ErrValidation
	}
	return &t, nil
}

// CreateDraft creates a draft: validates flow authorization and current version, then inserts the draft row.
func (s *Services) CreateDraft(ctx context.Context, actor Actor, in CreateDraftInput) (*DraftView, error) {
	var flow flowRow
	if err := s.DB.WithContext(ctx).Raw(
		`select id, flow_type, status, rule_set_id, version from flows where id = ?`, in.FlowID).
		Scan(&flow).Error; err != nil {
		return nil, err
	}
	if flow.ID == "" {
		return nil, ErrNotFound
	}
	if flow.Status != "enabled" || flow.FlowType != "change_review" {
		return nil, &domain.Error{Code: 2014, Name: "FLOW_GRANT_REVOKED"}
	}
	granted, err := s.flowGranted(ctx, s.DB, actor.UserID, in.FlowID)
	if err != nil {
		return nil, err
	}
	if !granted {
		return nil, &domain.Error{Code: 2014, Name: "FLOW_GRANT_REVOKED"}
	}
	now := nowUTC()
	rollbackEnabled := true
	if in.RollbackEnabled != nil {
		rollbackEnabled = *in.RollbackEnabled
	}
	row := persistence.ChangeDraftModel{
		ID: newID(), OwnerUserID: actor.UserID, FlowID: in.FlowID,
		FlowVersionSeen: flow.Version, Title: in.Title, Description: in.Description,
		RequestedScheduledFor: in.RequestedScheduledFor,
		RollbackEnabled:       rollbackEnabled,
		TicketType:            "normal",
		Revision:              1, State: string(domain.DraftStateDraft), Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.DB.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, err
	}
	v := draftViewOf(&row, nil)
	return &v, nil
}

// draftViewOf assembles the view (payload and latest Run are queried separately).
func draftViewOf(m *persistence.ChangeDraftModel, payload *persistence.DraftSQLPayloadModel) DraftView {
	ticketType := m.TicketType
	if ticketType == "" {
		ticketType = "normal"
	}
	v := DraftView{
		ID: m.ID, OwnerUserID: m.OwnerUserID, FlowID: m.FlowID,
		Title: m.Title, Description: m.Description,
		RollbackEnabled: m.RollbackEnabled, TicketType: ticketType,
		SourceOrderID: m.SourceOrderID, RollbackEvidenceID: m.RollbackEvidenceID,
		Revision: m.Revision, State: m.State, Version: m.Version,
		CreatedAt: m.CreatedAt.UTC().Format(timeFormat), UpdatedAt: m.UpdatedAt.UTC().Format(timeFormat),
	}
	if m.RequestedScheduledFor != nil {
		rsf := m.RequestedScheduledFor.UTC().Format(timeFormat)
		v.RequestedScheduledFor = &rsf
	}
	if payload != nil {
		v.HasSQL = true
		v.SQLSizeBytes = int64(payload.SizeBytes)
		v.StatementCount = int64(payload.StatementCount)
	}
	return v
}

const timeFormat = "2006-01-02T15:04:05.000Z07:00"

// ListDrafts lists drafts for the owner using keyset pagination (owner_updated_idx).
func (s *Services) ListDrafts(ctx context.Context, actor Actor, limit int, afterID string, afterUpdated string) ([]DraftView, *string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows := []persistence.ChangeDraftModel{}
	q := s.DB.WithContext(ctx).
		Where("owner_user_id = ?", actor.UserID)
	if afterID != "" && afterUpdated != "" {
		q = q.Where("(updated_at, id) < (?, ?)", afterUpdated, afterID)
	}
	if err := q.Order("updated_at desc, id desc").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	var next *string
	if len(rows) > limit {
		last := rows[limit-1]
		cursor := fmt.Sprintf("%s|%s", last.UpdatedAt.UTC().Format(timeFormat), last.ID)
		next = &cursor
		rows = rows[:limit]
	}
	out := make([]DraftView, 0, len(rows))
	for i := range rows {
		out = append(out, draftViewOf(&rows[i], nil))
	}
	return out, next, nil
}

// GetDraft returns the owner's draft view (including SQL metadata and latest Run).
func (s *Services) GetDraft(ctx context.Context, actor Actor, draftID string) (*DraftView, error) {
	m, err := s.loadDraft(ctx, s.DB, draftID)
	if err != nil {
		return nil, err
	}
	if m.OwnerUserID != actor.UserID {
		return nil, ErrNotFound
	}
	var payload persistence.DraftSQLPayloadModel
	hasPayload := true
	if err := s.DB.WithContext(ctx).Where("draft_id = ?", draftID).
		First(&payload).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		hasPayload = false
	}
	var pl *persistence.DraftSQLPayloadModel
	if hasPayload {
		pl = &payload
	}
	v := draftViewOf(m, pl)
	if m.SourceOrderID != nil && *m.SourceOrderID != "" {
		var disp string
		if err := s.DB.WithContext(ctx).Raw(
			`select display_number from change_orders where id = ?`, *m.SourceOrderID,
		).Scan(&disp).Error; err != nil {
			return nil, err
		}
		if disp != "" {
			v.SourceOrderDisplayNumber = &disp
		}
	}
	// Latest Run: take the most recent row ordered by creation time.
	var runID string
	if err := s.DB.WithContext(ctx).Raw(
		`select id::text from review_runs where draft_id = ? order by created_at desc, id desc limit 1`,
		draftID).Scan(&runID).Error; err != nil {
		return nil, err
	}
	if runID != "" {
		v.ReviewRunID = &runID
	}
	return &v, nil
}

// UpdateDraftInput holds metadata updates (any of title / description / flow_id / requested execution time).
// RequestedScheduledFor is tri-state: outer nil = not provided (unchanged); outer non-nil with inner nil =
// explicitly cleared; both non-nil = set to new value (section 18.56 Option B).
type UpdateDraftInput struct {
	Title                 *string
	Description           *string
	FlowID                *string
	RequestedScheduledFor **time.Time
	RollbackEnabled       *bool
	ExpectVersion         int64
}

// UpdateDraft updates metadata; a flow change is treated as an input change (ready -> outdated).
func (s *Services) UpdateDraft(ctx context.Context, actor Actor, draftID string, in UpdateDraftInput) (*DraftView, error) {
	var out *DraftView
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, err := s.loadDraftForUpdate(ctx, tx, draftID)
		if err != nil {
			return err
		}
		if m.OwnerUserID != actor.UserID {
			return ErrNotFound
		}
		if !domain.DraftEditable(domain.DraftState(m.State)) {
			return domain.ErrIllegalTransition
		}
		flowChanged := in.FlowID != nil && *in.FlowID != m.FlowID
		// Re-submitting the same flow is treated as "re-confirming the template": refreshes flow_version_seen
		// so a drifted draft can start a new precheck (Feature PRD section 3).
		flowReconfirmed := false
		if in.FlowID != nil && *in.FlowID == m.FlowID {
			var ver int64
			if err := tx.WithContext(ctx).Raw(
				`select version from flows where id = ?`, m.FlowID).Scan(&ver).Error; err != nil {
				return err
			}
			if ver > m.FlowVersionSeen {
				m.FlowVersionSeen = ver
				flowReconfirmed = true
			}
		}
		if flowChanged {
			granted, err := s.flowGranted(ctx, tx, actor.UserID, *in.FlowID)
			if err != nil {
				return err
			}
			if !granted {
				return &domain.Error{Code: 2014, Name: "FLOW_GRANT_REVOKED"}
			}
			var ver int64
			if err := tx.WithContext(ctx).Raw(
				`select version from flows where id = ?`, *in.FlowID).Scan(&ver).Error; err != nil {
				return err
			}
			if ver == 0 {
				return ErrNotFound
			}
			m.FlowID = *in.FlowID
			m.FlowVersionSeen = ver
		}
		sets := map[string]any{"version": m.Version + 1, "updated_at": nowUTC()}
		fromState := domain.DraftState(m.State)
		toState := fromState
		// Even when SQL is unchanged, a flow change invalidates existing conclusions (review inputs = SQL + flow).
		if flowChanged && fromState == domain.DraftStateReady {
			next, err := domain.DraftTransition(fromState, domain.DraftEventInputsChanged)
			if err != nil {
				return err
			}
			toState = next
			sets["state"] = string(next)
		}
		if flowChanged || flowReconfirmed {
			sets["flow_version_seen"] = m.FlowVersionSeen
		}
		if flowChanged {
			sets["flow_id"] = m.FlowID
		}
		if flowChanged || (flowReconfirmed && fromState == domain.DraftStateReady) {
			// Flow input change invalidates existing conclusions.
			if err := invalidateActiveRuns(ctx, tx, m.ID, m.Revision); err != nil {
				return err
			}
		}
		if flowReconfirmed && fromState == domain.DraftStateReady {
			next, err := domain.DraftTransition(fromState, domain.DraftEventInputsChanged)
			if err != nil {
				return err
			}
			toState = next
			sets["state"] = string(next)
		}
		if in.Title != nil {
			sets["title"] = *in.Title
		}
		if in.Description != nil {
			sets["description"] = *in.Description
		}
		if in.RequestedScheduledFor != nil {
			// Tri-state: explicit null clears (writes NULL); non-nil timestamp sets the value.
			if *in.RequestedScheduledFor == nil {
				sets["requested_scheduled_for"] = nil
				m.RequestedScheduledFor = nil
			} else {
				sets["requested_scheduled_for"] = **in.RequestedScheduledFor
				m.RequestedScheduledFor = *in.RequestedScheduledFor
			}
		}
		if in.RollbackEnabled != nil {
			if m.TicketType == "rollback" && *in.RollbackEnabled {
				return ErrValidation
			}
			sets["rollback_enabled"] = *in.RollbackEnabled
			m.RollbackEnabled = *in.RollbackEnabled
		}
		if err := s.updateDraftVersioned(ctx, tx, m, in.ExpectVersion, sets); err != nil {
			return err
		}
		m.Version++
		if flowChanged {
			from := string(fromState)
			data := domain.DraftStateChangedData{
				AggregateID: m.ID, From: &from, To: string(toState),
				ReasonCode: domain.ReasonDraftRevised, AggregateVersion: m.Version,
			}
			if err := emitDraftEvent(ctx, tx, s.Recorder, domain.EventDraftRevised, m.ID, actor.UserID, data); err != nil {
				return err
			}
		}
		out0 := draftViewOf(m, nil)
		out = &out0
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteDraft deletes an unsubmitted draft. The application role holds DELETE only on change_drafts/
// draft_sql_payloads/review_runs/stage_review_snapshots/async_tasks/
// idempotency_records (db/roles/grants.sql); cascading cleanup of insert_only
// child rows (sql_statements/groups/findings/evidence) relies on resolution RCP-20260826-01
// (see artifacts/development/BE-B6-REVIEW-DOMAIN/proposals/), which currently remain as orphan rows without blocking deletion.
func (s *Services) DeleteDraft(ctx context.Context, actor Actor, draftID string, expectVersion int64) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, err := s.loadDraftForUpdate(ctx, tx, draftID)
		if err != nil {
			return err
		}
		if m.OwnerUserID != actor.UserID {
			return ErrNotFound
		}
		if domain.DraftTerminal(domain.DraftState(m.State)) {
			return domain.ErrTerminalState
		}
		// Reverse-reference precheck (RCP-20260826-01): reject deletion if any order stage has ever bound
		// a review snapshot of this draft (already unreachable via state machine; defense in depth).
		var refs int64
		if err := tx.WithContext(ctx).Raw(`
			select count(*) from order_stages os
			where os.stage_review_snapshot_id in (
			    select id from stage_review_snapshots where review_run_id in
			        (select id from review_runs where draft_id = ?)
			)`, draftID).Scan(&refs).Error; err != nil {
			return err
		}
		if refs > 0 {
			return ErrReferenced
		}
		// Cancel all tasks for this draft: runs already in review_runs are cancelled by aggregate;
		// pre-allocated tasks prior to grouping completion are cancelled by input draft_id.
		if err := tx.WithContext(ctx).Exec(`
			update async_tasks set state='cancelled', business_err_code=2007,
			       failure_name='draft_deleted', lease_owner=null, lease_expires_at=null,
			       updated_at=now(), finished_at=now()
			where state in ('queued','running') and (
			    aggregate_id in (select id from review_runs where draft_id = ?)
			    or (aggregate_type='review_run' and input->>'draft_id' = ?)
			)`, draftID, draftID).Error; err != nil {
			return err
		}
		// Stage snapshots and Runs (review aggregates deletable by the application role).
		if err := tx.WithContext(ctx).Exec(`
			delete from stage_review_snapshots where review_run_id in
			    (select id from review_runs where draft_id = ?)`, draftID).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(
			`delete from review_runs where draft_id = ?`, draftID).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(
			`delete from draft_sql_payloads where draft_id = ?`, draftID).Error; err != nil {
			return err
		}
		res := tx.WithContext(ctx).Exec(
			`delete from change_drafts where id = ? and version = ?`, draftID, expectVersion)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			// draft_delete profile is C1004-only: stale If-Match surfaces
			// as CONCURRENT_MODIFICATION, same as the tx-level race.
			return ErrConcurrent
		}
		return nil
	})
}

// ReplaceSQL replaces SQL: increments Revision by 1, re-encrypts envelope, invalidates old Runs
// (ready -> outdated), and emits a change_draft.revised event.
func (s *Services) ReplaceSQL(ctx context.Context, actor Actor, draftID string, sql []byte, expectVersion int64) (*DraftView, error) {
	if len(sql) == 0 {
		return nil, fmt.Errorf("%w: empty sql", ErrValidation)
	}
	if len(sql) > 33554432 {
		return nil, domain.ErrBatchTooLarge
	}
	// Dialect profile is taken from the compatibility mode of the flow's first-stage datasource.
	profile, err := s.draftProfile(ctx, s.DB, draftID)
	if err != nil {
		return nil, err
	}
	count, err := s.countStatements(sql, profile)
	if err != nil {
		return nil, err
	}
	if count < 1 || count > 100000 {
		return nil, domain.ErrBatchTooLarge
	}
	var out *DraftView
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, err := s.loadDraftForUpdate(ctx, tx, draftID)
		if err != nil {
			return err
		}
		if m.OwnerUserID != actor.UserID {
			return ErrNotFound
		}
		if m.TicketType == "rollback" {
			return domain.ErrIllegalTransition
		}
		fromState := domain.DraftState(m.State)
		if !domain.DraftEditable(fromState) {
			return domain.ErrIllegalTransition
		}
		env, mac, err := sealPayload(s.Crypter, "draft_sql_payloads", draftID, "sql_ciphertext", sql)
		if err != nil {
			return err
		}
		newRevision := m.Revision + 1
		sets := map[string]any{
			"revision": newRevision, "version": m.Version + 1, "updated_at": nowUTC(),
		}
		toState := fromState
		if fromState == domain.DraftStateReady {
			next, terr := domain.DraftTransition(fromState, domain.DraftEventInputsChanged)
			if terr != nil {
				return terr
			}
			toState = next
			sets["state"] = string(next)
		}
		if err := s.updateDraftVersioned(ctx, tx, m, expectVersion, sets); err != nil {
			return err
		}
		payload := persistence.DraftSQLPayloadModel{
			DraftID:       draftID,
			SQLCiphertext: env.Ciphertext, SQLNonce: env.Nonce,
			SQLKeyID: env.KeyID, SQLAlgorithm: env.Algorithm,
			PlaintextHMAC: mac, SizeBytes: int32(len(sql)),
			StatementCount: int32(count), UpdatedAt: nowUTC(),
		}
		if err := tx.WithContext(ctx).Where("draft_id = ?", draftID).
			Delete(&persistence.DraftSQLPayloadModel{}).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(&payload).Error; err != nil {
			return err
		}
		if err := invalidateActiveRuns(ctx, tx, draftID, m.Revision); err != nil {
			return err
		}
		m.Revision = newRevision
		m.Version++
		m.State = string(toState)
		from := string(fromState)
		data := domain.DraftStateChangedData{
			AggregateID: m.ID, From: &from, To: string(toState),
			ReasonCode: domain.ReasonDraftRevised, AggregateVersion: m.Version,
		}
		if err := emitDraftEvent(ctx, tx, s.Recorder, domain.EventDraftRevised, m.ID, actor.UserID, data); err != nil {
			return err
		}
		v := draftViewOf(m, &payload)
		out = &v
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ErrValidation indicates parameter validation failed (maps to 1001).
var ErrValidation = errors.New("review: validation failed")

// countStatements counts statements by dialect profile using the B5 statement splitter.
func (s *Services) countStatements(sql []byte, profile fingerprint.Profile) (int, error) {
	split := changeset.NewSplitter(bytes.NewReader(sql), profile, 0)
	count := 0
	for {
		_, done, err := split.Next()
		if err != nil {
			base := domain.FailureFromFingerprintCode(fingerprintFailureName(err))
			if detail := formatSplitterFailureDetail(sql, profile, err); detail != "" {
				return 0, &domain.Error{Code: base.Code, Name: base.Name, Detail: detail}
			}
			return 0, base
		}
		if done {
			break
		}
		count++
		if count > 100000 {
			return 0, domain.ErrBatchTooLarge
		}
	}
	return count, nil
}

func formatSplitterFailureDetail(sql []byte, profile fingerprint.Profile, err error) string {
	var fpFail *fingerprint.Failure
	if !errors.As(err, &fpFail) || fpFail == nil {
		return ""
	}
	reason := strings.TrimSpace(fpFail.Detail)
	if fpFail.HasOffset {
		line, col := byteOffsetToLineCol(sql, fpFail.Offset)
		if idx := strings.Index(reason, " at offset "); idx >= 0 {
			reason = strings.TrimSpace(reason[:idx])
		} else if idx := strings.Index(reason, " (statement #"); idx >= 0 {
			reason = strings.TrimSpace(reason[:idx])
		}
		if reason == "" {
			reason = "lexical check failed"
		}
		stmtPart := ""
		if fpFail.Statement > 0 {
			stmtPart = fmt.Sprintf(", statement #%d", fpFail.Statement)
		}
		return fmt.Sprintf("line %d, col %d (offset %d%s, dialect %s): %s",
			line, col, fpFail.Offset, stmtPart, profile.String(), reason)
	}
	if reason == "" {
		return ""
	}
	return fmt.Sprintf("dialect %s: %s", profile.String(), reason)
}

func byteOffsetToLineCol(src []byte, offset int64) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(src)) {
		offset = int64(len(src))
	}
	prefix := src[:offset]
	line := bytes.Count(prefix, []byte{'\n'}) + 1
	lastNL := bytes.LastIndexByte(prefix, '\n')
	lineStart := 0
	if lastNL >= 0 {
		lineStart = lastNL + 1
	}
	col := utf8.RuneCount(prefix[lineStart:]) + 1
	return line, col
}

// SQLRevealResult holds the plaintext Reveal response material.
type SQLRevealResult struct {
	RevealID   string
	SQL        string
	Watermark  string
	ValidUntil string
}

// RevealDraftSQL returns SQL plaintext (Owner only) and audits the reveal event.
func (s *Services) RevealDraftSQL(ctx context.Context, actor Actor, meta RequestMeta, draftID, purpose string) (*SQLRevealResult, error) {
	m, err := s.loadDraft(ctx, s.DB, draftID)
	if err != nil {
		return nil, err
	}
	if m.OwnerUserID != actor.UserID {
		return nil, ErrNotFound
	}
	var payload persistence.DraftSQLPayloadModel
	if err := s.DB.WithContext(ctx).Where("draft_id = ?", draftID).
		First(&payload).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	env := crypto.Envelope{
		Ciphertext: payload.SQLCiphertext, Nonce: payload.SQLNonce,
		KeyID: payload.SQLKeyID, Algorithm: payload.SQLAlgorithm,
	}
	plaintext, err := openPayload(s.Crypter, "draft_sql_payloads", draftID, "sql_ciphertext", env)
	if err != nil {
		return nil, err
	}
	// Integrity check: fail closed on HMAC mismatch.
	mac, err := s.Crypter.HMAC(sqlHMACPurpose, plaintext)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(hex.EncodeToString(mac), payload.PlaintextHMAC) {
		return nil, &domain.Error{Code: domain.CodeReviewSchemaInvalid, Name: "REVIEW_SCHEMA_INVALID"}
	}
	revealID := newID()
	now := nowUTC()
	res := &SQLRevealResult{
		RevealID:   revealID,
		SQL:        string(plaintext),
		Watermark:  buildWatermark(actor.Username, now),
		ValidUntil: now.Add(5 * time.Minute).UTC().Format(timeFormat),
	}
	if err := s.Recorder.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "draft.sql_revealed",
		ActorKind: "user", ActorID: strPtrOrNil(actor.UserID), ActorUsername: strPtrOrNil(actor.Username),
		ResourceType: "change_draft", ResourceID: strPtrOrNil(draftID),
		Action: "reveal", Outcome: "succeeded",
		RequestID: strPtrOrNil(meta.RequestID), SourceIP: strPtrOrNil(meta.SourceIP),
		UserAgent: strPtrOrNil(meta.UserAgent),
		Metadata:  map[string]any{"reveal_id": revealID, "purpose": purpose, "revision": m.Revision},
	}); err != nil {
		return nil, err
	}
	return res, nil
}

// buildWatermark builds a dynamic watermark: viewer + server timestamp (without breaking copy-paste).
func buildWatermark(username string, at time.Time) string {
	return fmt.Sprintf("%s | %s", username, at.UTC().Format("2006-01-02T15:04:05Z"))
}

// jsonMarshal is a deterministic JSON encoding helper.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
