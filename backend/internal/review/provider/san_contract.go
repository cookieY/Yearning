package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
)

const (
	// SANWireContractVersion is the semantic version of the Head 1 (ai-review) SAN user wire
	// contract. It exists only in code, fixtures, and documentation and is never placed on the wire.
	SANWireContractVersion = "san-wire-v2"

	// SANWireRequestSchemaVersion is the shared wire request_schema_version frozen for san-wire-v1 and san-wire-v2.
	SANWireRequestSchemaVersion = 1

	// SANWireUserMessagePrefix is the verbatim text prefix prepended to the serialized sanWireRequest JSON
	// in messages[1].content on OpenAI-compatible SAN endpoints.
	SANWireUserMessagePrefix = "Review request JSON payload follows. Respond ONLY with a single json object matching the required JSON Schema.\n"

	// SANRepresentativeShapeTemplate is the fmt.Sprintf format string used to construct
	// AnonymizedGroup.RepresentativeShape from (repKind, repOp, !unbounded_mutation).
	SANRepresentativeShapeTemplate = "kind=%v op=%v where=%v"
)

// SANStatisticsValueType identifies the allowed JSON value type for a statistics entry.
type SANStatisticsValueType string

const (
	SANStatTypeString SANStatisticsValueType = "string"
	SANStatTypeBool   SANStatisticsValueType = "bool"
	SANStatTypeInt64  SANStatisticsValueType = "int64"
)

// Canonical statistics branch identifiers corresponding to ephemeral_agent.go and stmtshape.go.
const (
	SANStatBranchDMLUpdateDelete  = "dml_update_delete"
	SANStatBranchDMLInsert        = "dml_insert"
	SANStatBranchDMLReplace       = "dml_replace"
	SANStatBranchDMLOther         = "dml_other"
	SANStatBranchDDLCreateView    = "ddl_create_view"
	SANStatBranchDDLDrop          = "ddl_drop"
	SANStatBranchDDLRenameTable   = "ddl_rename_table"
	SANStatBranchDDLAlter         = "ddl_alter"
	SANStatBranchDDLTruncateOther = "ddl_truncate_or_other"
	SANStatBranchDDLCreateOther   = "ddl_create_other"
)

// SANStatisticsKeySpec defines one allowed statistics map key, its runtime/wire type,
// whether it is unconditionally emitted within its scope, and any fixed enum allowlist.
type SANStatisticsKeySpec struct {
	Name          string                 `json:"name"`
	Type          SANStatisticsValueType `json:"type"`
	Required      bool                   `json:"required"`
	AllowedValues []string               `json:"allowed_values,omitempty"`
	Condition     string                 `json:"condition,omitempty"`
}

// SANStatisticsBranchSchema defines the branch-specific statistics keys permitted for a
// statement class and operation family.
type SANStatisticsBranchSchema struct {
	Branch         string                 `json:"branch"`
	StatementClass string                 `json:"statement_class"`
	Operations     []string               `json:"operations"`
	Notes          string                 `json:"notes,omitempty"`
	Keys           []SANStatisticsKeySpec `json:"keys"`
}

// SANStatisticsSchema declares the authoritative allowlist and forbid-list for
// AnonymizedGroup.Statistics keys emitted by ephemeralStageGroups and stmtshape.go.
type SANStatisticsSchema struct {
	UniversalKeys []SANStatisticsKeySpec      `json:"universal_keys"`
	Branches      []SANStatisticsBranchSchema `json:"branches"`
	ForbiddenKeys []string                    `json:"forbidden_keys"`
}

// SANStatisticsKeySchema is the frozen san-wire-v2 whitelist verified line-by-line against
// ephemeral_agent.go:646-665/707-744, statement_analysis.go:657-664, and stmtshape.go:77-328.
var SANStatisticsKeySchema = SANStatisticsSchema{
	UniversalKeys: []SANStatisticsKeySpec{
		{
			Name:          "algorithm_version",
			Type:          SANStatTypeString,
			Required:      true,
			AllowedValues: []string{fingerprint.AlgorithmVersion},
		},
		{Name: "in_value_count_max", Type: SANStatTypeInt64, Required: true},
		{Name: "max_statement_bytes", Type: SANStatTypeInt64, Required: true},
		{Name: "unbounded_mutation", Type: SANStatTypeBool, Required: true},
		{Name: "values_row_count_max", Type: SANStatTypeInt64, Required: true},
	},
	Branches: []SANStatisticsBranchSchema{
		{
			Branch:         SANStatBranchDMLUpdateDelete,
			StatementClass: "dml",
			Operations:     []string{"update", "delete"},
			Keys: []SANStatisticsKeySpec{
				{Name: "dml_has_limit", Type: SANStatTypeBool, Required: true},
				{Name: "dml_has_order_by", Type: SANStatTypeBool, Required: true},
				{Name: "dml_has_subselect", Type: SANStatTypeBool, Required: true},
				{Name: "where_literal_null", Type: SANStatTypeBool, Required: true},
			},
		},
		{
			Branch:         SANStatBranchDMLInsert,
			StatementClass: "dml",
			Operations:     []string{"insert"},
			Keys: []SANStatisticsKeySpec{
				{Name: "insert_has_columns", Type: SANStatTypeBool, Required: true},
				{Name: "insert_has_null_literal", Type: SANStatTypeBool, Required: true},
				{Name: "dml_has_subselect", Type: SANStatTypeBool, Required: true},
			},
		},
		{
			Branch:         SANStatBranchDMLReplace,
			StatementClass: "dml",
			Operations:     []string{"replace"},
			Keys: []SANStatisticsKeySpec{
				{Name: "dml_has_subselect", Type: SANStatTypeBool, Required: true},
			},
		},
		{
			Branch:         SANStatBranchDMLOther,
			StatementClass: "dml",
			Operations:     []string{"select", "merge", "*"},
			Notes:          "Applies to SELECT, MERGE, and any non-UPDATE/DELETE/INSERT/REPLACE DML statement.",
			Keys:           []SANStatisticsKeySpec{},
		},
		{
			Branch:         SANStatBranchDDLCreateView,
			StatementClass: "ddl",
			Operations:     []string{"create"},
			Notes:          "Applies when reCreateView matches the CREATE VIEW statement.",
			Keys: []SANStatisticsKeySpec{
				{Name: "ddl_is_create_view", Type: SANStatTypeBool, Required: true},
				{Name: "ddl_view_select_star", Type: SANStatTypeBool, Required: true},
			},
		},
		{
			Branch:         SANStatBranchDDLDrop,
			StatementClass: "ddl",
			Operations:     []string{"drop"},
			Keys: []SANStatisticsKeySpec{
				{
					Name:          "ddl_drop_target",
					Type:          SANStatTypeString,
					Required:      true,
					AllowedValues: []string{"database", "function", "index", "other", "procedure", "table", "trigger", "view"},
				},
			},
		},
		{
			Branch:         SANStatBranchDDLRenameTable,
			StatementClass: "ddl",
			Operations:     []string{"rename"},
			Notes:          "Applies to RENAME TABLE statements (class=ddl, op=rename). ddl_across_db_rename is true when any old TO new pair specifies two distinct explicit schemas; an unqualified side is treated as matching the other side because stmtshape runs before stage default-database binding.",
			Keys: []SANStatisticsKeySpec{
				{Name: "ddl_is_rename_table", Type: SANStatTypeBool, Required: true},
				{Name: "ddl_across_db_rename", Type: SANStatTypeBool, Required: true},
			},
		},
		{
			Branch:         SANStatBranchDDLAlter,
			StatementClass: "ddl",
			Operations:     []string{"alter"},
			Keys: []SANStatisticsKeySpec{
				{Name: "ddl_alters_column_type", Type: SANStatTypeBool, Required: true},
				{Name: "ddl_changes_column_position", Type: SANStatTypeBool, Required: true},
			},
		},
		{
			Branch:         SANStatBranchDDLTruncateOther,
			StatementClass: "ddl",
			Operations:     []string{"truncate", "*"},
			Notes:          "Applies to TRUNCATE and any other DDL statement without a specialized branch (including non-TABLE RENAME such as RENAME USER).",
			Keys:           []SANStatisticsKeySpec{},
		},
		{
			Branch:         SANStatBranchDDLCreateOther,
			StatementClass: "ddl",
			Operations:     []string{"create"},
			Notes:          "Applies to all CREATE statements that do not match reCreateView.",
			Keys: []SANStatisticsKeySpec{
				{Name: "ddl_has_primary_key", Type: SANStatTypeBool, Required: true},
				{Name: "ddl_has_foreign_key", Type: SANStatTypeBool, Required: true},
				{Name: "ddl_has_partition", Type: SANStatTypeBool, Required: true},
				{Name: "ddl_table_comment_present", Type: SANStatTypeBool, Required: true},
				{Name: "ddl_has_float_double", Type: SANStatTypeBool, Required: true},
				{Name: "ddl_has_special_type", Type: SANStatTypeBool, Required: true},
				{Name: "ddl_column_comment_count", Type: SANStatTypeInt64, Required: true},
				{Name: "ddl_column_count", Type: SANStatTypeInt64, Required: true},
				{Name: "ddl_not_null_count", Type: SANStatTypeInt64, Required: true},
				{Name: "ddl_default_count", Type: SANStatTypeInt64, Required: true},
				{Name: "ddl_index_count", Type: SANStatTypeInt64, Required: true},
				{Name: "ddl_index_max_key_parts", Type: SANStatTypeInt64, Required: true},
				{Name: "ddl_index_unnamed_count", Type: SANStatTypeInt64, Required: true},
				{Name: "ddl_index_prefix_conformant", Type: SANStatTypeBool, Required: true},
				{Name: "ddl_max_char_length", Type: SANStatTypeInt64, Required: true},
				{Name: "ddl_max_varchar_length", Type: SANStatTypeInt64, Required: true},
				{
					Name:      "ddl_has_auto_increment",
					Type:      SANStatTypeBool,
					Required:  false,
					Condition: "present when AUTO_INCREMENT appears in CREATE statement",
				},
				{
					Name:      "ddl_autoincrement_unsigned",
					Type:      SANStatTypeBool,
					Required:  false,
					Condition: "present when AUTO_INCREMENT appears in CREATE statement",
				},
				{
					Name:      "ddl_autoincrement_init_one",
					Type:      SANStatTypeBool,
					Required:  false,
					Condition: "present when AUTO_INCREMENT = <n> appears in CREATE statement",
				},
				{
					Name:      "ddl_pk_named_id",
					Type:      SANStatTypeBool,
					Required:  false,
					Condition: "present when PRIMARY KEY (<col>) column name is parsed",
				},
				{
					Name:      "ddl_pk_is_integer",
					Type:      SANStatTypeBool,
					Required:  false,
					Condition: "present when PRIMARY KEY (<col>) and its column definition are parsed",
				},
				{
					Name:      "ddl_charset",
					Type:      SANStatTypeString,
					Required:  false,
					Condition: "present when controlledDDLWord resolves a charset in the allowlist",
				},
				{
					Name:          "ddl_charset_status",
					Type:          SANStatTypeString,
					Required:      false,
					AllowedValues: []string{"unknown"},
					Condition:     "present when charset syntax is detected outside valid DDL position or allowlist",
				},
				{
					Name:      "ddl_collation",
					Type:      SANStatTypeString,
					Required:  false,
					Condition: "present when controlledDDLWord resolves a collation in the allowlist",
				},
				{
					Name:          "ddl_collation_status",
					Type:          SANStatTypeString,
					Required:      false,
					AllowedValues: []string{"unknown"},
					Condition:     "present when collation syntax is detected outside valid DDL position or allowlist",
				},
				{
					Name:      "ddl_table_name_length",
					Type:      SANStatTypeInt64,
					Required:  false,
					Condition: "present when CREATE [TEMPORARY] TABLE name is parsed",
				},
				{
					Name:      "ddl_table_identifier_not_keyword",
					Type:      SANStatTypeBool,
					Required:  false,
					Condition: "present when CREATE [TEMPORARY] TABLE name is parsed",
				},
				{
					Name:      "ddl_table_prefix_conformant",
					Type:      SANStatTypeBool,
					Required:  false,
					Condition: "present when CREATE [TEMPORARY] TABLE name is parsed",
				},
			},
		},
	},
	ForbiddenKeys: []string{
		"statement_kind",
		"operation",
		"metadata_context_complete",
		"representative_anonymized_sql",
		"objects",
		"statement_ordinal",
		"statement_ordinal_ranges",
		"object_ranges_truncated",
	},
}

// ValidateSANGroupStatistics verifies that stats conforms strictly to SANStatisticsKeySchema
// for the given branch: no forbidden keys, all required universal and branch keys present,
// all keys in the allowlist, and matching types/enum values.
func ValidateSANGroupStatistics(branch string, stats map[string]any) error {
	var branchSchema *SANStatisticsBranchSchema
	for i := range SANStatisticsKeySchema.Branches {
		if SANStatisticsKeySchema.Branches[i].Branch == branch {
			branchSchema = &SANStatisticsKeySchema.Branches[i]
			break
		}
	}
	if branchSchema == nil {
		return fmt.Errorf("unknown SAN statistics branch %q", branch)
	}

	forbidden := make(map[string]bool, len(SANStatisticsKeySchema.ForbiddenKeys))
	for _, k := range SANStatisticsKeySchema.ForbiddenKeys {
		forbidden[k] = true
	}

	allowed := make(map[string]SANStatisticsKeySpec, len(SANStatisticsKeySchema.UniversalKeys)+len(branchSchema.Keys))
	for _, spec := range SANStatisticsKeySchema.UniversalKeys {
		allowed[spec.Name] = spec
	}
	for _, spec := range branchSchema.Keys {
		allowed[spec.Name] = spec
	}

	for k, v := range stats {
		if forbidden[k] {
			return fmt.Errorf("branch %s: forbidden key %q present in statistics", branch, k)
		}
		spec, ok := allowed[k]
		if !ok {
			return fmt.Errorf("branch %s: unexpected key %q not in statistics allowlist", branch, k)
		}
		if err := validateSANStatValue(branch, spec, v); err != nil {
			return err
		}
	}

	var missing []string
	for name, spec := range allowed {
		if spec.Required {
			if _, ok := stats[name]; !ok {
				missing = append(missing, name)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("branch %s: missing required statistics keys: %v", branch, missing)
	}
	return nil
}

func validateSANStatValue(branch string, spec SANStatisticsKeySpec, v any) error {
	switch spec.Type {
	case SANStatTypeString:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("branch %s: key %q has type %T, want string", branch, spec.Name, v)
		}
		if len(spec.AllowedValues) > 0 && !slices.Contains(spec.AllowedValues, s) {
			return fmt.Errorf("branch %s: key %q value %q not in allowlist %v", branch, spec.Name, s, spec.AllowedValues)
		}
	case SANStatTypeBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("branch %s: key %q has type %T, want bool", branch, spec.Name, v)
		}
	case SANStatTypeInt64:
		switch n := v.(type) {
		case int64, int, int32:
		case float64:
			if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n {
				return fmt.Errorf("branch %s: key %q float64 value %v is not a whole integer", branch, spec.Name, n)
			}
		default:
			return fmt.Errorf("branch %s: key %q has type %T, want int64", branch, spec.Name, v)
		}
	default:
		return fmt.Errorf("branch %s: unsupported schema type %q for key %q", branch, spec.Type, spec.Name)
	}
	return nil
}

type sanWireFieldDescriptor struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	JSONTag string `json:"json_tag"`
}

type sanWireStructDescriptor struct {
	Name   string                   `json:"name"`
	Fields []sanWireFieldDescriptor `json:"fields"`
}

func describeWireStruct(name string, example any) sanWireStructDescriptor {
	t := reflect.TypeOf(example)
	fields := make([]sanWireFieldDescriptor, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fields = append(fields, sanWireFieldDescriptor{
			Name:    f.Name,
			Type:    f.Type.String(),
			JSONTag: f.Tag.Get("json"),
		})
	}
	return sanWireStructDescriptor{
		Name:   name,
		Fields: fields,
	}
}

// SANContractDigest computes a deterministic SHA-256 hex digest over the declarative SAN
// wire contract surface (versions, wire struct field names/types/JSON tags, statistics schema,
// fingerprint.AlgorithmVersion, representative_shape format, and literal truncation behavior).
// Behavioral drift is guarded primarily by TestSANWireGolden and TestSANStatisticsWhitelist;
// this digest serves as a declarative mirror for schema and contract-constant changes.
func SANContractDigest() string {
	var probe strings.Builder
	probe.WriteString("SELECT '")
	probe.WriteString(strings.Repeat("a", promptprofile.MaxInlineLiteralChars+1))
	probe.WriteByte('\'')
	sample257 := probe.String()
	snapshot := struct {
		ContractVersion             string                    `json:"contract_version"`
		RequestSchemaVersion        int                       `json:"request_schema_version"`
		UserMessagePrefix           string                    `json:"user_message_prefix"`
		RepresentativeShapeTemplate string                    `json:"representative_shape_template"`
		FingerprintAlgorithmVersion string                    `json:"fingerprint_algorithm_version"`
		MaxInlineLiteralChars       int                       `json:"max_inline_literal_chars"`
		LiteralTruncationSample257  string                    `json:"literal_truncation_sample_257"`
		WireStructs                 []sanWireStructDescriptor `json:"wire_structs"`
		StatisticsSchema            SANStatisticsSchema       `json:"statistics_schema"`
	}{
		ContractVersion:             SANWireContractVersion,
		RequestSchemaVersion:        SANWireRequestSchemaVersion,
		UserMessagePrefix:           SANWireUserMessagePrefix,
		RepresentativeShapeTemplate: SANRepresentativeShapeTemplate,
		FingerprintAlgorithmVersion: fingerprint.AlgorithmVersion,
		MaxInlineLiteralChars:       promptprofile.MaxInlineLiteralChars,
		LiteralTruncationSample257:  promptprofile.TruncateSQL(sample257),
		WireStructs: []sanWireStructDescriptor{
			describeWireStruct("sanWireRequest", sanWireRequest{}),
			describeWireStruct("AnonymizedGroup", AnonymizedGroup{}),
			describeWireStruct("AnonymizedObjectReference", AnonymizedObjectReference{}),
			describeWireStruct("StatementOrdinalRange", StatementOrdinalRange{}),
			describeWireStruct("NormalizedEvidenceRef", NormalizedEvidenceRef{}),
			describeWireStruct("ToolDescriptor", ToolDescriptor{}),
		},
		StatisticsSchema: SANStatisticsKeySchema,
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
