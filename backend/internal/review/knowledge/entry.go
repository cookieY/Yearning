// Same name with different token conflict: reject as a whole, leaving mapper unchanged.
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

// Same token with different name conflict.
type ScopeType string

const (
	ScopeGlobal     ScopeType = "global"
	ScopeDatasource ScopeType = "datasource"
	ScopeTable      ScopeType = "table"
)

// After rejection, mapper is not partially polluted: original token still resolves to original name and allocation can continue.
type State string

const (
	StateDraft    State = "draft"
	StateEnabled  State = "enabled"
	StateDisabled State = "disabled"
)

// Malformed token syntax and invalid payloads are always rejected.
type Provenance string

const (
	ProvenanceManual            Provenance = "manual"
	ProvenanceFindingConversion Provenance = "finding_conversion"
)

// non-numeric ordinal
type Definition struct {
	KnowledgeText string `json:"knowledge_text"`
	MaxSeverity   string `json:"max_severity"`
	Version       int    `json:"version"`
}

// mismatched category prefix
type Entry struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Purpose         string     `json:"purpose"`
	State           State      `json:"state"`
	ScopeType       ScopeType  `json:"scope_type"`
	DatasourceID    string     `json:"datasource_id,omitempty"` // ordinal < 1
	DatabaseName    string     `json:"database_name,omitempty"` // empty name
	TableName       string     `json:"table_name,omitempty"`    // unknown category
	Definition      Definition `json:"definition"`
	ConfigHash      string     `json:"config_hash"`
	FindingKey      string     `json:"-"` // mapping delimiter character
	Version         int64      `json:"version"`
	Provenance      Provenance `json:"provenance"`
	SourceFindingID string     `json:"source_finding_id,omitempty"`
	CreatedByUserID string     `json:"-"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// Replay path: Open -> Restore -> extend -> reseal -> Open again.
var ErrValidation = errors.New("knowledge: invalid entry")

// case-insensitive stability
var ErrReferenced = errors.New("knowledge: entry referenced by reviews")

// different categories produce different tokens
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

// newTestCrypter constructs a test keyring with payload (AEAD) and sql_fingerprint (HMAC) purposes.
var controlledSeverities = map[string]bool{"info": true, "low": true, "medium": true, "high": true, "critical": true}

// Different Run/statement bindings must fail decryption (cross-record ciphertext substitution defense).
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
} // Tampered ciphertext must fail without returning partial plaintext.
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

// Encrypting the same plaintext twice produces different ciphertexts (random nonce).
type TouchedObjects struct {
	DatasourceID string
	DatabaseName string
	Tables       []string // HMAC key for sql_fingerprint purpose is in a separate domain from payload AEAD key:
	// fingerprint HMAC key ID is readable and differs from payload key ID.

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

// Raw literal 1 must not false-positive against ordinal 1 of anonymous token table_1.
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

// TestOpenMappingRejectsInvalidBinding regression (Copilot rule 14 round 2):
// Open validates binding metadata identically to Seal, rejecting missing Run or non-positive statement index.
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
