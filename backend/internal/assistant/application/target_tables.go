// target_tables.go Target table discovery for AI Assistant turns.
package application

import (
	"context"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/assistant/tool"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

// resolveTablesToDescribe extracts target tables from authoritative sources (explicit target_table
// and input_sql) and supplements them only with live catalog-verified table names referenced at
// identifier boundaries in the natural-language prompt. Unverified SQL extraction over natural-language
// prose runs only as a fallback when no authoritative tables exist and catalog listing is unavailable,
// preventing English verbs like "Update note ..." from registering column names as phantom tables.
func (s *Services) resolveTablesToDescribe(
	ctx context.Context,
	dsTarget tool.DatasourceTarget,
	schemaName string,
	targetTable string,
	inputSQL string,
	content string,
	profile fingerprint.Profile,
) []string {
	tables, _ := s.resolveTablesAndCatalogToDescribe(ctx, dsTarget, schemaName, targetTable, inputSQL, content, profile)
	return tables
}

func (s *Services) resolveTablesAndCatalogToDescribe(
	ctx context.Context,
	dsTarget tool.DatasourceTarget,
	schemaName string,
	targetTable string,
	inputSQL string,
	content string,
	profile fingerprint.Profile,
) ([]string, []string) {
	tables := collectTargetTables(targetTable, inputSQL, "", profile)
	seen := make(map[string]bool, len(tables))
	for _, tbl := range tables {
		seen[strings.ToLower(tbl)] = true
	}
	listedFromCatalog := false
	var catalogTables []string
	if s.Authorizer != nil && s.Authorizer.Catalog != nil {
		if allTables, err := s.Authorizer.Catalog.ListTables(ctx, dsTarget, schemaName); err == nil {
			listedFromCatalog = true
			catalogTables = append([]string(nil), allTables...)
			lowContent := strings.ToLower(content)
			for _, tbl := range allTables {
				clean := strings.TrimSpace(tbl)
				if clean == "" {
					continue
				}
				lowTbl := strings.ToLower(clean)
				if !seen[lowTbl] && mentionsIdentifier(lowContent, lowTbl) {
					seen[lowTbl] = true
					tables = append(tables, clean)
				}
			}
			if len(tables) == 0 && len(allTables) == 1 && strings.TrimSpace(allTables[0]) != "" {
				tables = append(tables, strings.TrimSpace(allTables[0]))
			}
		}
	}
	if !listedFromCatalog && len(tables) == 0 {
		tables = collectTargetTables("", "", content, profile)
	}
	return tables, catalogTables
}

func isASCIIIdentByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

func mentionsIdentifier(lowText, lowIdent string) bool {
	if lowText == "" || lowIdent == "" {
		return false
	}
	offset := 0
	for {
		idx := strings.Index(lowText[offset:], lowIdent)
		if idx < 0 {
			return false
		}
		start := offset + idx
		end := start + len(lowIdent)
		leftOK := start == 0 || !isASCIIIdentByte(lowText[start-1])
		rightOK := end == len(lowText) || !isASCIIIdentByte(lowText[end])
		if leftOK && rightOK {
			return true
		}
		offset = start + 1
	}
}

func collectTargetTables(explicitTable, inputSQL, prompt string, profile fingerprint.Profile) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(t string) {
		clean := strings.Trim(strings.TrimSpace(t), "`\"'")
		if clean == "" {
			return
		}
		low := strings.ToLower(clean)
		if !seen[low] {
			seen[low] = true
			out = append(out, clean)
		}
	}
	if explicitTable != "" {
		add(explicitTable)
	}
	if inputSQL != "" {
		for _, t := range ExtractTablesFromSQL(inputSQL, profile) {
			add(t)
		}
	}
	if prompt != "" {
		for _, t := range ExtractTablesFromSQL(prompt, profile) {
			add(t)
		}
	}
	return out
}
