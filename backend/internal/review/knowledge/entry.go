// Package knowledge manages internal review experience entries (R010): scoped matching,
// deterministic prompt injection, config_hash computation, and reference-guarded updates.
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ScopeType identifies the matching granularity of an internal knowledge entry.
type ScopeType string

const (
	ScopeGlobal     ScopeType = "global"
	ScopeDatasource ScopeType = "datasource"
	ScopeTable      ScopeType = "table"
)

// State represents the lifecycle status of a knowledge entry.
type State string

const (
	StateDraft    State = "draft"
	StateEnabled  State = "enabled"
	StateDisabled State = "disabled"
)

// Provenance identifies whether an entry was authored manually or converted from a finding.
type Provenance string

const (
	ProvenanceManual            Provenance = "manual"
	ProvenanceFindingConversion Provenance = "finding_conversion"
)

// Definition holds the rule body, compatibility severity metadata, and revision integer.
type Definition struct {
	KnowledgeText string `json:"knowledge_text"`
	MaxSeverity   string `json:"max_severity"`
	Version       int    `json:"version"`
}

// Entry represents a persisted review knowledge entry and its matching scope.
type Entry struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Purpose         string     `json:"purpose"`
	State           State      `json:"state"`
	ScopeType       ScopeType  `json:"scope_type"`
	DatasourceID    string     `json:"datasource_id,omitempty"` // Required for datasource/table scopes.
	DatabaseName    string     `json:"database_name,omitempty"` // Required schema/database locator for scoped rules.
	TableName       string     `json:"table_name,omitempty"`    // Required only when ScopeType == ScopeTable.
	Definition      Definition `json:"definition"`
	ConfigHash      string     `json:"config_hash"`
	FindingKey      string     `json:"-"` // System-derived stable finding key prefix.
	Version         int64      `json:"version"`
	Provenance      Provenance `json:"provenance"`
	SourceFindingID string     `json:"source_finding_id,omitempty"`
	CreatedByUserID string     `json:"-"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// ErrValidation indicates an invalid Entry or Definition field value or scope/provenance conflict.
var ErrValidation = errors.New("knowledge: invalid entry")

// ErrReferenced indicates deletion is blocked because existing review tool calls reference this entry.
var ErrReferenced = errors.New("knowledge: entry referenced by reviews")

// Validate verifies required fields, scope locators, provenance fields, and Definition constraints.
func (e *Entry) Validate() error {
	if e.ID == "" || e.Name == "" || e.CreatedByUserID == "" {
		return fmt.Errorf("%w: identity fields required", ErrValidation)
	}
	switch e.State {
	case StateDraft, StateEnabled, StateDisabled:
	default:
		return fmt.Errorf("%w: state %q", ErrValidation, e.State)
	}
	switch e.ScopeType {
	case ScopeGlobal:
		if e.DatasourceID != "" || e.DatabaseName != "" || e.TableName != "" {
			return fmt.Errorf("%w: global scope must not carry locator", ErrValidation)
		}
	case ScopeDatasource:
		if e.DatasourceID == "" || e.DatabaseName == "" || e.TableName != "" {
			return fmt.Errorf("%w: datasource scope needs datasource+database", ErrValidation)
		}
	case ScopeTable:
		if e.DatasourceID == "" || e.DatabaseName == "" || e.TableName == "" {
			return fmt.Errorf("%w: table scope needs datasource+database+table", ErrValidation)
		}
	default:
		return fmt.Errorf("%w: scope %q", ErrValidation, e.ScopeType)
	}
	switch e.Provenance {
	case ProvenanceManual:
		if e.SourceFindingID != "" {
			return fmt.Errorf("%w: manual provenance must not cite finding", ErrValidation)
		}
	case ProvenanceFindingConversion:
		if e.SourceFindingID == "" {
			return fmt.Errorf("%w: finding_conversion provenance requires source finding", ErrValidation)
		}
	default:
		return fmt.Errorf("%w: provenance %q", ErrValidation, e.Provenance)
	}
	return e.Definition.Validate()
}

var controlledSeverities = map[string]bool{"info": true, "low": true, "medium": true, "high": true, "critical": true}

// Validate checks knowledge text length bounds, allowed severity, and version >= 1.
func (d Definition) Validate() error {
	if strings.TrimSpace(d.KnowledgeText) == "" {
		return fmt.Errorf("%w: knowledge_text required", ErrValidation)
	}
	if len(d.KnowledgeText) > 64*1024 {
		return fmt.Errorf("%w: knowledge_text too large", ErrValidation)
	}
	switch d.MaxSeverity {
	case "info", "low", "medium", "high", "critical":
	default:
		return fmt.Errorf("%w: max_severity invalid", ErrValidation)
	}
	if d.Version < 1 {
		return fmt.Errorf("%w: version must be >= 1", ErrValidation)
	}
	return nil
}

// ComputeConfigHash produces a deterministic SHA-256 hex digest of an entry's name, scope, and definition.
func ComputeConfigHash(e *Entry) (string, error) {
	payload := map[string]any{
		"name":       e.Name,
		"scope_type": string(e.ScopeType),
		"datasource": e.DatasourceID,
		"database":   e.DatabaseName,
		"table":      e.TableName,
		"definition": e.Definition,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// TouchedObjects describes the physical datasource, schemas, and tables referenced by a Review Stage.
type TouchedObjects struct {
	DatasourceID string
	DatabaseName string
	Tables       []string

	// PreciseScopes switches matching from the compatibility projection above
	// to proven physical schema/table identities. ScopeComplete is consumed by
	// the application gate; it does not suppress matches from a valid prefix.
	PreciseScopes bool
	ScopeComplete bool
	Schemas       []TouchedSchema
	Objects       []TouchedObject
}

// TouchedSchema is one physical schema visible in the current Stage.
type TouchedSchema struct {
	Name          string
	CaseSensitive bool
}

// TouchedObject is one physical table identity visible in the current Stage.
// CaseSensitive is derived from quoted SQL identity; unquoted identifiers use
// database-style case folding.
type TouchedObject struct {
	SchemaName          string
	TableName           string
	SchemaCaseSensitive bool
	TableCaseSensitive  bool
}

// Matched reports whether this entry's scope matches the stage's touched physical objects.
func (e *Entry) Matched(o TouchedObjects) bool {
	switch e.ScopeType {
	case ScopeGlobal:
		return true
	case ScopeDatasource:
		if o.PreciseScopes {
			if o.DatasourceID != e.DatasourceID {
				return false
			}
			for _, schema := range o.Schemas {
				if touchedIdentifierMatches(e.DatabaseName, schema.Name, schema.CaseSensitive) {
					return true
				}
			}
			return false
		}
		return o.DatasourceID == e.DatasourceID &&
			strings.EqualFold(o.DatabaseName, e.DatabaseName)
	case ScopeTable:
		if o.PreciseScopes {
			if o.DatasourceID != e.DatasourceID {
				return false
			}
			for _, object := range o.Objects {
				if touchedIdentifierMatches(e.DatabaseName, object.SchemaName, object.SchemaCaseSensitive) &&
					touchedIdentifierMatches(e.TableName, object.TableName, object.TableCaseSensitive) {
					return true
				}
			}
			return false
		}
		if o.DatasourceID != e.DatasourceID ||
			!strings.EqualFold(o.DatabaseName, e.DatabaseName) {
			return false
		}
		for _, t := range o.Tables {
			if strings.EqualFold(t, e.TableName) {
				return true
			}
		}
		return false
	}
	return false
}

func touchedIdentifierMatches(entry, touched string, caseSensitive bool) bool {
	if caseSensitive {
		return entry == touched
	}
	return strings.EqualFold(entry, touched)
}

// SortByDeterminism orders matched entries deterministically by scope breadth and entry ID.
func SortByDeterminism(entries []*Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].ScopeType != entries[j].ScopeType {
			return scopeRank(entries[i].ScopeType) < scopeRank(entries[j].ScopeType)
		}
		return entries[i].ID < entries[j].ID
	})
}

func scopeRank(s ScopeType) int {
	switch s {
	case ScopeGlobal:
		return 0
	case ScopeDatasource:
		return 1
	default:
		return 2
	}
}
