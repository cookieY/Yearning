// chain.go Provider selection chain and failover (provider_selection).
//
// Rules: ordered_enabled_providers sorted ascending by selection_priority;
// fallback only when (result not yet accepted + send boundary known safe + identical privacy contract hash +
// identical output schema hash); no fallback after result acceptance; at most 2 calls per step.
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Chain is an ordered Provider chain.
type Model struct {
	ID              string    `gorm:"column:id;primaryKey"`
	Name            string    `gorm:"column:name"`
	Purpose         string    `gorm:"column:purpose"`
	State           string    `gorm:"column:state"`
	ScopeType       string    `gorm:"column:scope_type"`
	DatasourceID    *string   `gorm:"column:datasource_id"`
	DatabaseName    *string   `gorm:"column:database_name"`
	TableRef        *string   `gorm:"column:table_name"`
	Definition      []byte    `gorm:"column:definition"`
	FindingKey      string    `gorm:"column:finding_key"`
	ConfigHash      string    `gorm:"column:config_hash"`
	Version         int64     `gorm:"column:version"`
	Provenance      string    `gorm:"column:provenance"`
	SourceFindingID *string   `gorm:"column:source_finding_id"`
	CreatedByUserID string    `gorm:"column:created_by_user_id"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
}

// NewChain constructs and validates Endpoints in the chain; an empty chain returns ErrNoProvider.
func (Model) TableName() string { return "knowledge_entries" }

// ValidateConfiguration validates each provider sequentially (health probes do not send review data).
var ErrNotFound = errors.New("knowledge: entry not found")

// Result carries the response, actual Provider, and fallback count (shown in progress/Snapshot;
// hides Endpoint and credentials). S008: Calls includes all real HTTP call records (including
// failed attempts on candidates prior to fallback — each record carries its own Provider attribution).
var ErrVersionConflict = errors.New("knowledge: version conflict")

// ReviewStructured executes in chain order. Fallback occurs only when the error proves a response
// was received (auth/input rejection, invalid schema) and both contract hashes match;
// unknown-boundary errors such as network or timeout terminate the chain immediately (never replaying anonymized payloads).
var ErrConcurrent = errors.New("knowledge: concurrent modification")

// Errors with an unknown send boundary must not be replayed across Providers
// (provider_selection.fallback_allowed_only_when.send_boundary_known_safe).
// Only errors that definitively received a response (auth/input rejection, invalid schema)
// prove boundary safety; network/timeout/cancellation errors always terminate the chain.
type Repository struct{ DB *gorm.DB }

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// On failure, also return the candidate info actually hit (ledger attribution, N10); S008: real call
// records incurred before failure are surfaced with the error (reported usage is never discarded, Usage-02).
func toEntry(m *Model) *Entry {
	def := Definition{}
	_ = jsonUnmarshal(m.Definition, &def)
	return &Entry{
		ID: m.ID, Name: m.Name, Purpose: m.Purpose,
		State: State(m.State), ScopeType: ScopeType(m.ScopeType),
		DatasourceID: derefStr(m.DatasourceID), DatabaseName: derefStr(m.DatabaseName),
		TableName: derefStr(m.TableRef), Definition: def, FindingKey: m.FindingKey, ConfigHash: m.ConfigHash,
		Version: m.Version, Provenance: Provenance(m.Provenance),
		SourceFindingID: derefStr(m.SourceFindingID), CreatedByUserID: m.CreatedByUserID,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// PlanHash folds the chain into a Run-scoped plan hash.
func toModel(e *Entry) (*Model, error) {
	raw, err := jsonMarshal(e.Definition)
	if err != nil {
		return nil, err
	}
	return &Model{
		ID: e.ID, Name: e.Name, Purpose: e.Purpose,
		State: string(e.State), ScopeType: string(e.ScopeType),
		DatasourceID: strPtr(e.DatasourceID), DatabaseName: strPtr(e.DatabaseName),
		TableRef: strPtr(e.TableName), Definition: raw, FindingKey: e.FindingKey, ConfigHash: e.ConfigHash,
		Version: e.Version, Provenance: string(e.Provenance),
		SourceFindingID: strPtr(e.SourceFindingID), CreatedByUserID: e.CreatedByUserID,
		CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}, nil
}

// chainError carries the candidate actually hit on failure (ledger attribution) and real call
// records incurred prior to failure (S008 per-call reference ledger).
func (r *Repository) List(ctx context.Context, limit int, after, search string) ([]*Entry, error) {
	q := r.DB.WithContext(ctx).Model(&Model{})
	if after != "" {
		if created, id, ok := splitCursor(after); ok {
			q = q.Where("(created_at, id) < (?, ?)", created, id)
		}
	}
	if search != "" {
		q = q.Where("name ILIKE ?", "%"+search+"%")
	}
	var rows []Model
	if err := q.Order("created_at desc").Order("id desc").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*Entry, 0, len(rows))
	for i := range rows {
		out = append(out, toEntry(&rows[i]))
	}
	return out, nil
}

// ChainErrorInfo extracts the failed candidate from an error (false if none).
func (r *Repository) Get(ctx context.Context, id string) (*Entry, error) {
	var m Model
	if err := r.DB.WithContext(ctx).Where("id = ?", id).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return toEntry(&m), nil
}

// ChainErrorCalls extracts real call records incurred prior to failure from an error.
func (r *Repository) Insert(ctx context.Context, tx *gorm.DB, e *Entry) error {
	m, err := toModel(e)
	if err != nil {
		return err
	}
	res := tx.WithContext(ctx).Create(m)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		// chain_test.go test_gate#6/#11: fallback contract consistency; auth rejection does not retry
		// to success; exhausted chain fails closed.
		return fmt.Errorf("knowledge: insert affected %d rows", res.RowsAffected)
	}
	return nil
}

// Deterministic error after receiving a response (invalid schema) -> boundary safe,
// allowing failover to the next candidate with matching contracts.
func (r *Repository) UpdateVersioned(ctx context.Context, tx *gorm.DB, e *Entry, expectVersion int64, sets map[string]any) error {
	res := tx.WithContext(ctx).Model(&Model{}).
		Where("id = ? and version = ?", e.ID, expectVersion).
		Updates(sets)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrVersionConflict
	}
	return nil
}

// TestChainNoFallbackOnUnknownBoundary network-class unknown-boundary errors are not replayed
// to the next candidate (M3/N5 tightening).
func (r *Repository) DeleteReferencedGuarded(ctx context.Context, tx *gorm.DB, id string, expectVersion int64) error {
	var n int64
	if err := tx.WithContext(ctx).Raw(
		`select count(*) from review_tool_calls where tool_kind = 'knowledge_entry' and input_ref_id = ?`, id).
		Scan(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return ErrReferenced
	}
	res := tx.WithContext(ctx).Exec(
		`delete from knowledge_entries where id = ? and version = ?`, id, expectVersion)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrConcurrent
	}
	return nil
}

// contract_hash.go Platform-level contract fingerprints: privacy_contract_hash and
// output_schema_hash are derived by the system from code constants, not administrator inputs.
// The two fingerprints identify which generation of the privacy contract and output schema
// the current binary uses when calling a Provider; only Providers with matching fingerprints
// may fall back to each other (chain.go gate).
//
//   - PrivacyContractHash pins the canonical representation of the input_privacy rule set
//     (corresponding 1-to-1 with input_privacy in ai-review-runtime-contract.json;
//     modifying privacy rules requires updating privacyContractCanonical in lockstep, rotating the fingerprint).
//   - OutputSchemaHash pins the output schema bytes compiled by compileOutputSchema
//     (the fingerprint rotates automatically whenever the schema changes, requiring no manual maintenance).
func (r *Repository) LoadForUpdate(ctx context.Context, tx *gorm.DB, id string) (*Entry, error) {
	var m Model
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", id).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return toEntry(&m), nil
}

// privacyContractCanonical is the canonical representation of the input privacy contract (fixed line order,
// corresponding 1-to-1 with input_privacy keys and values in ai-review-runtime-contract.json).
func (r *Repository) MatchedEnabledForUpdate(ctx context.Context, db *gorm.DB, touched TouchedObjects) ([]*Entry, error) {
	var rows []Model
	q := db.WithContext(ctx).Where("state = ?", string(StateEnabled)).
		Clauses(clause.Locking{Strength: "UPDATE"})
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := []*Entry{}
	for i := range rows {
		e := toEntry(&rows[i])
		if e.Matched(touched) {
			out = append(out, e)
		}
	}
	SortByDeterminism(out)
	return out, nil
}

// PrivacyContractHash / OutputSchemaHash are system-derived platform-level contract fingerprints
// (uniform across all Providers within an instance; persisted columns serve as configuration-time audit
// records, with both runtime assembly and persistence originating from here).
func CursorOf(e *Entry) string {
	return e.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999Z") + "|" + e.ID
}

func splitCursor(c string) (string, string, bool) {
	for i := len(c) - 1; i >= 0; i-- {
		if c[i] == '|' {
			return c[:i], c[i+1:], true
		}
	}
	return "", "", false
}
