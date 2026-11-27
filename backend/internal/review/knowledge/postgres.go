// postgres.go implements GORM persistence for knowledge_entries using explicit column tags,
// TableName bindings, optimistic version checks, and row-level locking without AutoMigrate.
package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cookieY/YearningX/backend/internal/platform/pagecursor"
	"github.com/cookieY/YearningX/backend/internal/platform/ptr"
)

// Model maps directly to the knowledge_entries table.
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

// TableName binds Model to the knowledge_entries PostgreSQL table.
func (Model) TableName() string { return "knowledge_entries" }

// ErrNotFound indicates the requested knowledge entry does not exist.
var ErrNotFound = errors.New("knowledge: entry not found")

// ErrVersionConflict indicates an optimistic-locking mismatch during update.
var ErrVersionConflict = errors.New("knowledge: version conflict")

// ErrConcurrent indicates a concurrent mutation prevented safe deletion.
var ErrConcurrent = errors.New("knowledge: concurrent modification")

// Repository encapsulates GORM queries and mutations for knowledge entries.
type Repository struct{ DB *gorm.DB }

// toEntry converts a persistence Model into a domain Entry.
func toEntry(m *Model) *Entry {
	def := Definition{}
	_ = json.Unmarshal(m.Definition, &def)
	return &Entry{
		ID: m.ID, Name: m.Name, Purpose: m.Purpose,
		State: State(m.State), ScopeType: ScopeType(m.ScopeType),
		DatasourceID: ptr.Deref(m.DatasourceID), DatabaseName: ptr.Deref(m.DatabaseName),
		TableName: ptr.Deref(m.TableRef), Definition: def, FindingKey: m.FindingKey, ConfigHash: m.ConfigHash,
		Version: m.Version, Provenance: Provenance(m.Provenance),
		SourceFindingID: ptr.Deref(m.SourceFindingID), CreatedByUserID: m.CreatedByUserID,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// toModel serializes a domain Entry into a persistence Model.
func toModel(e *Entry) (*Model, error) {
	raw, err := json.Marshal(e.Definition)
	if err != nil {
		return nil, err
	}
	return &Model{
		ID: e.ID, Name: e.Name, Purpose: e.Purpose,
		State: string(e.State), ScopeType: string(e.ScopeType),
		DatasourceID: ptr.NonZero(e.DatasourceID), DatabaseName: ptr.NonZero(e.DatabaseName),
		TableRef: ptr.NonZero(e.TableName), Definition: raw, FindingKey: e.FindingKey, ConfigHash: e.ConfigHash,
		Version: e.Version, Provenance: string(e.Provenance),
		SourceFindingID: ptr.NonZero(e.SourceFindingID), CreatedByUserID: e.CreatedByUserID,
		CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}, nil
}

// List returns keyset-paginated knowledge entries sorted newest-first, optionally filtered by name search.
func (r *Repository) List(ctx context.Context, limit int, after, search string) ([]*Entry, error) {
	q := r.DB.WithContext(ctx).Model(&Model{})
	if after != "" {
		created, id, err := pagecursor.DecodeUUID(after)
		if err != nil {
			return nil, ErrValidation
		}
		q = q.Where("(created_at, id) < (?, ?)", created, id)
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

// Get loads a single knowledge entry by ID or returns ErrNotFound.
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

// Insert persists a new knowledge entry within tx and requires an exact 1-row insert.
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
		return fmt.Errorf("knowledge: insert affected %d rows", res.RowsAffected)
	}
	return nil
}

// UpdateVersioned applies partial column updates guarded by optimistic version matching.
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

// DeleteVersioned deletes a knowledge entry under optimistic version checking.
// Historical review_tool_calls and submitted order snapshots preserve immutable copies
// without foreign keys, so physical deletion is safe regardless of prior review references.
func (r *Repository) DeleteVersioned(ctx context.Context, tx *gorm.DB, id string, expectVersion int64) error {
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

// LoadForUpdate acquires a FOR UPDATE lock on a single knowledge entry inside tx.
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

// MatchedEnabledForUpdate locks enabled scoped entries and returns the deterministic subset matching touched objects.
func (r *Repository) MatchedEnabledForUpdate(ctx context.Context, db *gorm.DB, touched TouchedObjects) ([]*Entry, error) {
	var rows []Model
	q := db.WithContext(ctx).
		Where("state = ? and scope_type in ?", string(StateEnabled), []string{string(ScopeDatasource), string(ScopeTable)}).
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

// ListEnabledForScope performs a single indexed read of enabled datasource/table entries matching
// datasourceID, non-empty databaseName, and any table in tables (case-folded).
func (r *Repository) ListEnabledForScope(ctx context.Context, datasourceID, databaseName string, tables []string) ([]*Entry, error) {
	ds := strings.TrimSpace(datasourceID)
	dbName := strings.TrimSpace(databaseName)
	if r == nil || r.DB == nil || ds == "" || dbName == "" {
		return nil, nil
	}
	var rows []Model
	if err := r.DB.WithContext(ctx).
		Where("state = ? and datasource_id = ? and scope_type in ?",
			string(StateEnabled), ds, []string{string(ScopeDatasource), string(ScopeTable)}).
		Order("id asc").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	tableSet := make(map[string]bool, len(tables))
	for _, t := range tables {
		clean := strings.TrimSpace(t)
		if clean != "" {
			tableSet[strings.ToLower(clean)] = true
		}
	}
	out := make([]*Entry, 0, len(rows))
	for i := range rows {
		e := toEntry(&rows[i])
		if !strings.EqualFold(e.DatabaseName, dbName) {
			continue
		}
		if e.ScopeType == ScopeTable && !tableSet[strings.ToLower(strings.TrimSpace(e.TableName))] {
			continue
		}
		out = append(out, e)
	}
	SortByDeterminism(out)
	return out, nil
}

// CursorOf encodes an entry's keyset pagination cursor (`created_at|id`).
func CursorOf(e *Entry) string {
	return pagecursor.Encode(e.CreatedAt, e.ID)
}
