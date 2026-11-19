package application

import (
	"strconv"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/database"
)

const (
	maxSchemaMetadataTables = 200
	maxTableMetadataColumns = 128
	maxIndexMetadataEntries = 32
	maxIndexColumnEntries   = 256
)

type classifiedMetadataType struct {
	class           string
	elementClass    string
	arrayDimensions int
	length          int
	hasLength       bool
	precision       int
	hasPrecision    bool
	scale           int
	hasScale        bool
}

func absentTableMetadataFact(schema, table string) map[string]any {
	return map[string]any{
		"schema":            schema,
		"table":             table,
		"existence":         "absent",
		"columns":           []map[string]any{},
		"metadata_complete": true,
		"truncated":         false,
		"estimate":          false,
	}
}

func absentTableStatisticsFact(schema, table string) map[string]any {
	return map[string]any{
		"schema":            schema,
		"table":             table,
		"existence":         "absent",
		"metadata_complete": true,
		"truncated":         false,
		"estimate":          false,
	}
}

func absentIndexMetadataFact(schema, table string) map[string]any {
	return map[string]any{
		"schema":            schema,
		"table":             table,
		"existence":         "absent",
		"index_count":       0,
		"indexes":           []map[string]any{},
		"metadata_complete": true,
		"truncated":         false,
	}
}

// classifyMetadataType maps engine type spellings to a closed vocabulary.
// It parses only numeric modifiers from recognized built-ins; a custom or
// unexpected type is reduced to "other" without retaining its name or suffix.
func classifyMetadataType(raw string) classifiedMetadataType {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	arrayDimensions := 0
	for strings.HasSuffix(normalized, "[]") {
		arrayDimensions++
		normalized = strings.TrimSpace(strings.TrimSuffix(normalized, "[]"))
	}
	normalized = strings.TrimSpace(strings.TrimSuffix(normalized, " unsigned"))
	normalized = strings.TrimSpace(strings.TrimSuffix(normalized, " zerofill"))

	base := normalized
	var params []string
	if open := strings.IndexByte(normalized, '('); open >= 0 && strings.HasSuffix(normalized, ")") {
		base = strings.TrimSpace(normalized[:open])
		inside := normalized[open+1 : len(normalized)-1]
		for _, part := range strings.Split(inside, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				return classifiedMetadataType{class: "other"}
			}
			for _, r := range part {
				if r < '0' || r > '9' {
					return classifiedMetadataType{class: "other"}
				}
			}
			params = append(params, part)
		}
	}

	class := typeClassForBase(base)
	out := classifiedMetadataType{class: class}
	if arrayDimensions > 0 {
		if class == "other" {
			return classifiedMetadataType{class: "other"}
		}
		out.class = "array"
		out.elementClass = class
		out.arrayDimensions = arrayDimensions
	}
	switch class {
	case "string", "binary":
		if len(params) == 1 {
			if n, err := strconv.Atoi(params[0]); err == nil && n >= 0 && n <= 1_000_000 {
				out.length, out.hasLength = n, true
			}
		}
	case "decimal":
		if len(params) >= 1 && len(params) <= 2 {
			if n, err := strconv.Atoi(params[0]); err == nil && n >= 0 && n <= 1000 {
				out.precision, out.hasPrecision = n, true
			}
			if len(params) == 2 {
				if n, err := strconv.Atoi(params[1]); err == nil && n >= 0 && n <= 1000 {
					out.scale, out.hasScale = n, true
				}
			}
		}
	}
	return out
}

func typeClassForBase(base string) string {
	switch base {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint",
		"int2", "int4", "int8", "smallserial", "serial", "bigserial":
		return "integer"
	case "numeric", "decimal", "number", "float", "real", "double", "double precision", "money":
		return "decimal"
	case "char", "character", "character varying", "varchar", "varchar2", "nvarchar", "nchar",
		"text", "tinytext", "mediumtext", "longtext", "citext", "name":
		return "string"
	case "date", "datetime", "timestamp", "timestamp with time zone", "timestamp without time zone",
		"timestamptz", "time", "time with time zone", "time without time zone", "timetz",
		"interval", "year":
		return "temporal"
	case "bool", "boolean":
		return "boolean"
	case "binary", "varbinary", "blob", "tinyblob", "mediumblob", "longblob", "bytea", "bit", "bit varying":
		return "binary"
	case "json", "jsonb":
		return "json"
	case "uuid":
		return "uuid"
	default:
		return "other"
	}
}

func boundedMetadataTables(tables []string) []string {
	limit := len(tables)
	if limit > maxSchemaMetadataTables {
		limit = maxSchemaMetadataTables
	}
	return append([]string(nil), tables[:limit]...)
}

func metadataColumnFacts(columns []database.MetadataColumn) ([]map[string]any, int, bool) {
	total := len(columns)
	limit := total
	if limit > maxTableMetadataColumns {
		limit = maxTableMetadataColumns
	}
	out := make([]map[string]any, 0, limit)
	complete := total <= maxTableMetadataColumns
	for _, column := range columns[:limit] {
		typ := classifyMetadataType(column.DatabaseType)
		typeStatus := "observed"
		if typ.class == "other" {
			// The raw custom/domain type is deliberately removed from the
			// Provider channel. "other" therefore preserves privacy but does
			// not prove that the column type is known.
			typeStatus = "unknown"
			complete = false
		}
		item := map[string]any{
			"column_name": column.ColumnName,
			"ordinal":     column.Ordinal,
			"type_class":  typ.class,
			"type_status": typeStatus,
			"nullable":    column.Nullable,
			"has_default": column.ColumnDeflt != "",
			"has_comment": column.ColumnRemark != "",
		}
		if typ.class == "array" {
			item["element_type_class"] = typ.elementClass
			item["array_dimensions"] = typ.arrayDimensions
		}
		if typ.hasLength {
			item["type_length"] = typ.length
		}
		if typ.hasPrecision {
			item["numeric_precision"] = typ.precision
		}
		if typ.hasScale {
			item["numeric_scale"] = typ.scale
		}
		out = append(out, item)
	}
	return out, total, complete
}
