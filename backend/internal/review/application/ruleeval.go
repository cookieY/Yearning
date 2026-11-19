// ruleeval.go B8 frozen input re-verification and coverage validation (shared by both Runners).
//
// Ruling (2026-08-27 revision 98a385c two-layer division of labor): business review rules are carried
// exclusively by Prompt Tools, so this file performs no business rule evaluation; lexical facts
// (B5 fingerprint group statistics) are injected into model context by stageGroups, and skills produce
// structured Findings from those facts. Non-business technical admission enforced in code: SQL Hash
// binding re-verification (complete_ai_precheck precondition review_bound_to_exact_draft_revision_and_sql_hash),
// rule-set Hash drift re-verification, and Fingerprint coverage (group coverage side).
package application

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
)

// errInputsDrift indicates frozen inputs have drifted (converging run -> outdated).
var errInputsDrift = errors.New("review: frozen inputs drifted")

// verifyFrozenInputs re-verifies that Run-bound inputs have not drifted before terminal convergence:
// SQL payload HMAC and rule-set row config_hash (row-level frozen surface such as the enabled bit;
// composition semantic drift is carried by tool_config_hash and the injection reference invalidation chain).
// Legacy rows (without a binding row) fall back to Revision binding.
func (s *Services) verifyFrozenInputs(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel) error {
	// Frozen SQL Hash inside the Run row (000008 table; GORM would take the scalar scan path on a
	// single-field struct, so database/sql row scanning is used directly here).
	var frozen sql.NullString
	if err := db.WithContext(ctx).Raw(
		`select sql_payload_hmac from review_run_sql_bindings where review_run_id = ?`, run.ID).
		Row().Scan(&frozen); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if frozen.Valid && frozen.String != "" {
		var payloadRow struct{ HMAC string }
		if err := db.WithContext(ctx).Raw(
			`select plaintext_hmac as hmac from draft_sql_payloads where draft_id = ?`, run.DraftID).
			Scan(&payloadRow).Error; err != nil {
			return err
		}
		if payloadRow.HMAC == "" {
			return ErrNotFound
		}
		if payloadRow.HMAC != frozen.String {
			return errInputsDrift
		}
	}
	return s.verifyRuleSetHash(ctx, db, run)
}

// verifyRuleSetHash re-verifies that the current rule-set row config_hash of the flow equals the Run's
// frozen value; any drift (enabled toggle or row replacement) triggers inputs_changed.
func (s *Services) verifyRuleSetHash(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel) error {
	var flowRow struct {
		RuleSetID *string
	}
	if err := db.WithContext(ctx).Raw(`
		select f.rule_set_id::text as rule_set_id
		from change_drafts d join flows f on f.id = d.flow_id
		where d.id = ?`, run.DraftID).Scan(&flowRow).Error; err != nil {
		return err
	}
	if flowRow.RuleSetID == nil || *flowRow.RuleSetID == "" {
		if run.RuleConfigHash != sha256Hex([]byte("none")) {
			return errInputsDrift
		}
		return nil
	}
	var rsRow struct {
		ConfigHash string
	}
	if err := db.WithContext(ctx).Raw(
		`select config_hash from rule_sets where id = ?`, *flowRow.RuleSetID).Scan(&rsRow).Error; err != nil {
		return err
	}
	if rsRow.ConfigHash == "" {
		return errInputsDrift
	}
	if rsRow.ConfigHash != run.RuleConfigHash {
		return errInputsDrift
	}
	return nil
}

// verifyRunCoverage checks Fingerprint coverage (the group coverage side of complete_ai_precheck
// precondition evidence_complete_or_fail_closed): both the statement row count and the sum of statement
// counts across groups must equal the Run's frozen statement_count.
func (s *Services) verifyRunCoverage(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel) error {
	var stmtCount, groupSum int64
	if err := db.WithContext(ctx).Raw(
		`select count(*) from sql_statements where review_run_id = ?`, run.ID).Scan(&stmtCount).Error; err != nil {
		return err
	}
	if err := db.WithContext(ctx).Raw(
		`select coalesce(sum(statement_count), 0) from sql_fingerprint_groups where review_run_id = ?`, run.ID).
		Scan(&groupSum).Error; err != nil {
		return err
	}
	if stmtCount != int64(run.StatementCount) || groupSum != int64(run.StatementCount) {
		return &domain.Error{Code: domain.CodeReviewCoverageIncomp, Name: "REVIEW_COVERAGE_INCOMPLETE"}
	}
	return nil
}
