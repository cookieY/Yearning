// rulesets.go Rule Set admin surface: CRUD for pure Prompt Tool compositions,
// config_hash freezing, and replacement invalidation (R006: rules follow flows, and unsubmitted Reviews
// become outdated immediately after template/rule changes).
//
// Ruling (2026-08-27 revision 98a385c two-layer division of labor): business review rules are carried
// exclusively by Prompt Tools; the rules jsonb column is always an empty object — carrying any key is
// rejected (VALIDATION_FAILED, failing closed without silent downgrade). config_hash covers the enabled
// bit and the canonicalized member set; individual tool configurations are frozen via tool_config_hash.
package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
)

// RuleSetView is the admin read view (RuleSet schema).
type RuleSetView struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Enabled       bool            `json:"enabled"`
	Rules         json.RawMessage `json:"rules"`
	PromptToolIDs []string        `json:"prompt_tool_ids"`
	ConfigHash    string          `json:"config_hash"`
	Version       int64           `json:"version"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// ErrRuleSetReferenced 1006: referenced by a flow; physical deletion is forbidden.
var ErrRuleSetReferenced = errors.New("review: rule set referenced")

// ErrPromptToolNotFound 1001: a Prompt Tool referenced by the composition does not exist (distinguished
// from 1002 when the target rule set itself does not exist — R2-F13).
var ErrPromptToolNotFound = errors.New("review: prompt tool not found")

// ruleSetConfigHash computes the row-level config_hash: rules is always {}, combined with the enabled
// bit and the canonicalized set of member IDs. Member order carries no policy semantics; adding or
// removing members must invalidate old Runs, whereas pure reordering or saving unchanged must preserve the exact same Hash.
func ruleSetConfigHash(enabled bool, toolIDs []string) string {
	canonicalIDs := append([]string(nil), toolIDs...)
	sort.Strings(canonicalIDs)
	if canonicalIDs == nil {
		canonicalIDs = []string{}
	}
	payload, err := json.Marshal(struct {
		Enabled       bool            `json:"enabled"`
		Rules         json.RawMessage `json:"rules"`
		PromptToolIDs []string        `json:"prompt_tool_ids"`
	}{
		Enabled: enabled, Rules: json.RawMessage(`{}`), PromptToolIDs: canonicalIDs,
	})
	if err != nil {
		panic("review: rule set config hash payload must marshal")
	}
	return sha256Hex(payload)
}

// validateRuleSetWrite validates write payload constraints: rules must be an empty object.
func validateRuleSetWrite(name string, enabled bool, rules json.RawMessage, toolIDs []string) error {
	if name == "" || len(name) > 128 {
		return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	trimmed := bytes.TrimSpace(rules)
	if len(trimmed) == 0 || string(trimmed) != "{}" {
		return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	seen := map[string]bool{}
	for _, id := range toolIDs {
		if id == "" || seen[id] {
			return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
		}
		seen[id] = true
	}
	return nil
}

// replaceRuleSetMembership replaces all composition members in full (replace_membership_exact).
func replaceRuleSetMembership(ctx context.Context, tx *gorm.DB, ruleSetID string, toolIDs []string) error {
	if err := tx.WithContext(ctx).Exec(
		`delete from rule_set_prompt_tools where rule_set_id = ?`, ruleSetID).Error; err != nil {
		return err
	}
	for _, id := range toolIDs {
		if err := tx.WithContext(ctx).Exec(
			`insert into rule_set_prompt_tools (rule_set_id, prompt_tool_id) values (?, ?)`,
			ruleSetID, id).Error; err != nil {
			return err
		}
	}
	return nil
}

// lockPromptTools verifies that tools referenced by the composition exist and locks them (FOR UPDATE).
func lockPromptTools(ctx context.Context, tx *gorm.DB, toolIDs []string) error {
	for _, id := range toolIDs {
		var row struct{ ID string }
		if err := tx.WithContext(ctx).Raw(
			`select id::text as id from prompt_tools where id = ? for update`, id).Scan(&row).Error; err != nil {
			return err
		}
		if row.ID == "" {
			return ErrPromptToolNotFound
		}
	}
	return nil
}

// CreateRuleSet creates a rule set (v1; toolIDs may be omitted when the composition is empty).
func (s *Services) CreateRuleSet(ctx context.Context, actorUserID, name string, enabled bool,
	rules json.RawMessage, toolIDs []string) (*RuleSetView, error) {
	if err := validateRuleSetWrite(name, enabled, rules, toolIDs); err != nil {
		return nil, err
	}
	hash := ruleSetConfigHash(enabled, toolIDs)
	id := newID()
	now := nowUTC()
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockPromptTools(ctx, tx, toolIDs); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Exec(`
			insert into rule_sets (id, name, enabled, rules, config_hash, version, created_by_user_id, created_at, updated_at)
			values (?, ?, ?, '{}'::jsonb, ?, 1, ?, ?, ?)`,
			id, name, enabled, hash, actorUserID, now, now).Error; err != nil {
			return err
		}
		return replaceRuleSetMembership(ctx, tx, id, toolIDs)
	})
	if err != nil {
		return nil, mapRuleSetWriteErr(err)
	}
	tools := nonEmptyToolIDs(toolIDs)
	return &RuleSetView{ID: id, Name: name, Enabled: enabled, Rules: json.RawMessage(`{}`),
		PromptToolIDs: tools, ConfigHash: hash, Version: 1, CreatedAt: now, UpdatedAt: now}, nil
}

// ReplaceRuleSet performs optimistic-lock replacement; a config_hash change immediately invalidates
// unsubmitted Runs that froze the old Hash (R006).
func (s *Services) ReplaceRuleSet(ctx context.Context, id string, expectVersion int64,
	name string, enabled bool, rules json.RawMessage, toolIDs []string) (*RuleSetView, error) {
	if err := validateRuleSetWrite(name, enabled, rules, toolIDs); err != nil {
		return nil, err
	}
	newHash := ruleSetConfigHash(enabled, toolIDs)
	var out *RuleSetView
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID         string
			ConfigHash string
			Version    int64
			CreatedAt  time.Time
		}
		if err := tx.WithContext(ctx).Raw(
			`select id, config_hash, version, created_at from rule_sets where id = ? for update`, id).
			Scan(&row).Error; err != nil {
			return err
		}
		if row.ID == "" {
			return ErrNotFound
		}
		if row.Version != expectVersion {
			return ErrVersionConflict
		}
		if err := lockPromptTools(ctx, tx, toolIDs); err != nil {
			return err
		}
		now := nowUTC()
		res := tx.WithContext(ctx).Exec(`
			update rule_sets set name = ?, enabled = ?, rules = '{}'::jsonb, config_hash = ?,
				version = version + 1, updated_at = ?
			where id = ? and version = ?`,
			name, enabled, newHash, now, id, expectVersion)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrVersionConflict
		}
		if err := replaceRuleSetMembership(ctx, tx, id, toolIDs); err != nil {
			return err
		}
		if newHash != row.ConfigHash {
			// Frozen semantics changed: immediately invalidate unsubmitted Runs for this rule set (bound via flows) —
			// scoped by rule-set identity rather than matching globally by hash (since the hash consists of enabled
			// and members, global hash matching could falsely invalidate other rule sets sharing the same hash).
			if err := invalidateRunsForRuleSet(ctx, tx, id); err != nil {
				return err
			}
		}
		tools := nonEmptyToolIDs(toolIDs)
		out = &RuleSetView{ID: id, Name: name, Enabled: enabled, Rules: json.RawMessage(`{}`),
			PromptToolIDs: tools, ConfigHash: newHash, Version: expectVersion + 1,
			CreatedAt: row.CreatedAt, UpdatedAt: now}
		return nil
	})
	if err != nil {
		return nil, mapRuleSetWriteErr(err)
	}
	return out, nil
}

// DeleteRuleSet deletes a rule set: rejected if referenced by a flow or frozen by a non-outdated Run.
func (s *Services) DeleteRuleSet(ctx context.Context, id string, expectVersion int64) error {
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.WithContext(ctx).Raw(
			`select count(*) from flows where rule_set_id = ?`, id).Scan(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrRuleSetReferenced
		}
		if err := tx.WithContext(ctx).Raw(
			`select count(*) from review_runs r
			join change_drafts d on d.id = r.draft_id
			join flows f on f.id = d.flow_id
			where f.rule_set_id = ? and r.state <> 'outdated'
			  and d.state <> 'submitted'`, id).Scan(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return ErrRuleSetReferenced
		}
		res := tx.WithContext(ctx).Exec(
			`delete from rule_set_prompt_tools where rule_set_id = ?`, id)
		if res.Error != nil {
			return res.Error
		}
		res = tx.WithContext(ctx).Exec(
			`delete from rule_sets where id = ? and version = ?`, id, expectVersion)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			// C1004-only profile: stale If-Match surfaces as concurrent
			// modification, identical to the tx-level race.
			return ErrConcurrent
		}
		return nil
	})
	if err != nil {
		return mapRuleSetWriteErr(err)
	}
	return nil
}

// ruleSetRow is the read-model row.
type ruleSetRow struct {
	ID         string
	Name       string
	Enabled    bool
	Rules      []byte
	ConfigHash string
	Version    int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func ruleSetViewOf(ctx context.Context, db *gorm.DB, row ruleSetRow) (*RuleSetView, error) {
	var tools []string
	if err := db.WithContext(ctx).Raw(
		`select prompt_tool_id::text from rule_set_prompt_tools where rule_set_id = ? order by prompt_tool_id`,
		row.ID).Scan(&tools).Error; err != nil {
		return nil, err
	}
	if tools == nil {
		tools = []string{}
	}
	return &RuleSetView{ID: row.ID, Name: row.Name, Enabled: row.Enabled,
		Rules: json.RawMessage(row.Rules), PromptToolIDs: tools, ConfigHash: row.ConfigHash,
		Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

// ListRuleSets performs keyset pagination.
func (s *Services) ListRuleSets(ctx context.Context, limit int, after string, search string) ([]*RuleSetView, *string, error) {
	q := s.DB.WithContext(ctx).Table("rule_sets")
	if after != "" {
		if created, id, ok := splitCursor(after); ok {
			q = q.Where("(created_at, id) < (?, ?)", created, id)
		}
	}
	// section 18.73 admin server-side q: case-insensitive name substring (matching the q parameter
	// declaration on OpenAPI listRuleSets; narrows only the cursor walk set without relaxing authorization).
	if search != "" {
		q = q.Where("lower(name) like ?", "%"+strings.ToLower(search)+"%")
	}
	var rows []ruleSetRow
	if err := q.Select("id, name, enabled, rules, config_hash, version, created_at, updated_at").
		Order("created_at desc").Order("id desc").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		v := rows[len(rows)-1].CreatedAt.UTC().Format("2006-01-02T15:04:05.999999Z") + "|" + rows[len(rows)-1].ID
		next = &v
	}
	out := make([]*RuleSetView, 0, len(rows))
	for i := range rows {
		v, err := ruleSetViewOf(ctx, s.DB, rows[i])
		if err != nil {
			return nil, nil, err
		}
		out = append(out, v)
	}
	return out, next, nil
}

// GetRuleSet fetches a rule set by ID.
func (s *Services) GetRuleSet(ctx context.Context, id string) (*RuleSetView, error) {
	var row ruleSetRow
	err := s.DB.WithContext(ctx).Table("rule_sets").
		Select("id, name, enabled, rules, config_hash, version, created_at, updated_at").
		Where("id = ?", id).Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == "" {
		return nil, ErrNotFound
	}
	return ruleSetViewOf(ctx, s.DB, row)
}

// nonEmptyToolIDs ensures prompt_tool_ids serializes as [] rather than null
// (declared as a non-null array in the RuleSet schema).
func nonEmptyToolIDs(ids []string) []string {
	tools := make([]string, 0, len(ids))
	tools = append(tools, ids...)
	return tools
}

// mapRuleSetWriteErr maps unique name collision -> 1005; non-existent Prompt Tool referenced in
// composition -> 1001 (the exhaustive set of admin_create/admin_replace profiles excludes 1002);
// non-existent target rule set -> 1002 (admin_replace declares RESOURCE_NOT_FOUND).
func mapRuleSetWriteErr(err error) error {
	if isUniqueViolationErr(err) {
		return &domain.Error{Code: 1005, Name: "RESOURCE_ALREADY_EXISTS"}
	}
	if errors.Is(err, ErrPromptToolNotFound) {
		return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
	}
	return err
}

// ---- Unsubmitted Review invalidation (R006) ----

// invalidateRunsForRuleSet invalidates unsubmitted terminal Runs bound to this rule set (via flows)
// to outdated, transitioning ready drafts to outdated in lockstep (the sole inputs edge in the draft
// state machine). Active (queued/running) Runs are not eagerly invalidated here: the worker converges
// them idempotently on terminal write (via frozen Hash re-verification at reviewPhases entry), avoiding
// leaving reviewing drafts without a valid convergence path. submitted drafts are already frozen as
// order facts and are never invalidated.
func invalidateRunsForRuleSet(ctx context.Context, tx *gorm.DB, ruleSetID string) error {
	var rows []persistence.ReviewRunModel
	err := tx.WithContext(ctx).Raw(`
		select r.id, r.draft_id, r.draft_revision, r.state, r.version
		from review_runs r
		join change_drafts d on d.id = r.draft_id
		join flows f on f.id = d.flow_id
		where f.rule_set_id = ? and r.state in ('ready','blocked','partial','failed')
		  and d.state <> 'submitted'`, ruleSetID).Scan(&rows).Error
	if err != nil {
		return err
	}
	return invalidateRuns(ctx, tx, rows)
}

// InvalidateRunsForFlow invalidates Runs after a flow template replacement (called by the workflow domain).
// Semantics match rule replacement: all terminal unsubmitted Runs become outdated.
func InvalidateRunsForFlow(ctx context.Context, tx *gorm.DB, flowID string) error {
	var rows []persistence.ReviewRunModel
	err := tx.WithContext(ctx).Raw(`
		select r.id, r.draft_id, r.draft_revision, r.state, r.version
		from review_runs r
		join change_drafts d on d.id = r.draft_id
		where d.flow_id = ? and r.state in ('ready','blocked','partial','failed')
		  and d.state <> 'submitted'`, flowID).Scan(&rows).Error
	if err != nil {
		return err
	}
	return invalidateRuns(ctx, tx, rows)
}

// invalidateRuns invalidates each Run via version CAS and transitions ready drafts to outdated in lockstep.
// Lock order follows the domain convention (change_drafts before review_runs): lock all affected drafts
// in ID order first, then CAS the Run rows.
func invalidateRuns(ctx context.Context, tx *gorm.DB, rows []persistence.ReviewRunModel) error {
	draftIDs := map[string]bool{}
	for i := range rows {
		draftIDs[rows[i].DraftID] = true
	}
	ordered := make([]string, 0, len(draftIDs))
	for id := range draftIDs {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		if err := convergeDraftAfterRunInvalidation(ctx, tx, id); err != nil {
			return err
		}
	}
	for i := range rows {
		run := &rows[i]
		next, err := domain.RunTransition(domain.RunState(run.State), domain.RunEventInputsChanged)
		if err != nil {
			// An invalid terminal transition indicates persisted state is inconsistent with the state machine; fail closed.
			return domain.ErrIllegalTransition
		}
		sets := map[string]any{
			"state":       string(next),
			"version":     run.Version + 1,
			"finished_at": nowUTC(),
		}
		// review_run_failure_fields: outdated must clear failure fields.
		sets["business_err_code"] = nil
		sets["failure_name"] = nil
		res := tx.WithContext(ctx).Model(&persistence.ReviewRunModel{}).
			Where("id = ? and version = ?", run.ID, run.Version).
			Updates(sets)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrConcurrent
		}
	}
	return nil
}

// convergeDraftAfterRunInvalidation transitions ready -> outdated (idempotent: skips non-ready drafts).
func convergeDraftAfterRunInvalidation(ctx context.Context, tx *gorm.DB, draftID string) error {
	var draft struct {
		ID      string
		State   string
		Version int64
	}
	if err := tx.WithContext(ctx).Raw(
		`select id, state, version from change_drafts where id = ? for update`, draftID).
		Scan(&draft).Error; err != nil {
		return err
	}
	if draft.ID == "" || domain.DraftState(draft.State) != domain.DraftStateReady {
		return nil
	}
	next, err := domain.DraftTransition(domain.DraftStateReady, domain.DraftEventInputsChanged)
	if err != nil {
		return err
	}
	res := tx.WithContext(ctx).Exec(`
		update change_drafts set state = ?, version = version + 1, updated_at = ?
		where id = ? and version = ?`,
		string(next), nowUTC(), draft.ID, draft.Version)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrConcurrent
	}
	return nil
}
