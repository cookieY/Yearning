package application

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

type policyCatalogIdentity struct {
	kind                   database.PolicyCatalogKind
	schema, relation, name string
}

// genericPolicyColumnNames is a frozen mirror of contract ai-review-runtime-contract.json
// policy_text_privacy.generic_column_name_allowlist.words
// (RCP-20261031-POLICY-TEXT-GENERIC-COLUMN-ALLOWLIST, standard generic set ruled by owner on 2026-10-31).
// Zero-specificity generic column names do not constitute object-existence disclosure; the exemption
// applies solely to catalog-word collision checks on skill/knowledge text — schema/table/view/index/
// constraint names, database names, statement identifiers, and mapper-token collision checks are never exempted.
// Extensions require an RCP and lockstep updates across the contract, this mirror, and validate_contracts.py.
var genericPolicyColumnNames = map[string]struct{}{
	"id": {}, "name": {}, "code": {}, "type": {},
	"status": {}, "value": {}, "label": {}, "title": {},
	"remark": {}, "comment": {}, "version": {},
	"created_at": {}, "updated_at": {}, "deleted_at": {},
	"created_by": {}, "updated_by": {},
}

// genericColumnExemptionSet collects allowlisted column-class identifiers from the catalog
// (normalized to lowercase). If a word form is also occupied by any non-column identifier (schema/table/view/
// index/constraint, including a column's owning table relation) or database name, it is not exempted —
// the lexical set is flat without kind metadata, so err on the side of blocking (fail closed).
func genericColumnExemptionSet(catalog database.PolicyCatalog, databaseName string) map[string]struct{} {
	candidates := map[string]struct{}{}
	blocked := map[string]struct{}{}
	for _, identifier := range catalog.Identifiers {
		switch identifier.Kind {
		case database.PolicyCatalogColumn:
			if low := strings.ToLower(identifier.Name); low != "" {
				if _, ok := genericPolicyColumnNames[low]; ok {
					candidates[low] = struct{}{}
				}
			}
			if identifier.Relation != "" {
				blocked[strings.ToLower(identifier.Relation)] = struct{}{}
			}
		default:
			blocked[strings.ToLower(identifier.Name)] = struct{}{}
		}
	}
	if databaseName != "" {
		blocked[strings.ToLower(databaseName)] = struct{}{}
	}
	for word := range candidates {
		if _, clash := blocked[word]; clash {
			delete(candidates, word)
		}
	}
	return candidates
}

// exemptPolicyTextLexical returns the text-collision lexical set after removing exempted words (exemption
// matching is case-insensitive; call sites must filter after constructing the validation word set and
// before appending mapper originals and bound schemas — statement identifiers and database names do not
// participate in the exemption). The full lexical set is still used by policyMapperTokenCollidesWithCatalog —
// token collision checks are never exempted.
func exemptPolicyTextLexical(lexical []string, exempt map[string]struct{}) []string {
	if len(exempt) == 0 {
		return lexical
	}
	out := make([]string, 0, len(lexical))
	for _, value := range lexical {
		if _, ok := exempt[strings.ToLower(value)]; ok {
			continue
		}
		out = append(out, value)
	}
	return out
}

func policyCatalogAuthorizedSchemas(binding *stageSchemaBinding) ([]string, error) {
	if binding == nil || !binding.defensible {
		return nil, ErrPolicyPrivacy
	}
	set := make(map[string]struct{}, len(binding.schemas))
	for schema := range binding.schemas {
		if schema != "" {
			set[schema] = struct{}{}
		}
	}
	if binding.postgres != nil {
		for _, relation := range binding.postgres.Relations {
			if relation.Supported && relation.Present &&
				relation.Schema != "" &&
				relation.Schema != "pg_catalog" &&
				!strings.HasPrefix(relation.Schema, "pg_temp_") &&
				relation.Schema != "pg_temp" {
				set[relation.Schema] = struct{}{}
			}
		}
	}
	if len(set) == 0 {
		return nil, ErrPolicyPrivacy
	}
	out := make([]string, 0, len(set))
	for schema := range set {
		out = append(out, schema)
	}
	sort.Strings(out)
	return out, nil
}

func validateCompletePolicyCatalog(
	catalog database.PolicyCatalog,
	authorized []string,
	databaseName string,
) (map[policyCatalogIdentity]struct{}, []string, error) {
	if len(authorized) == 0 ||
		catalog.SchemaCount != len(authorized) ||
		!validPolicyCatalogTextIdentifier(databaseName) {
		return nil, nil, ErrPolicyPrivacy
	}
	allowed := make(map[string]struct{}, len(authorized))
	for _, schema := range authorized {
		allowed[schema] = struct{}{}
	}
	seenSchemas := make(map[string]struct{}, len(authorized))
	seenIdentities := make(map[policyCatalogIdentity]struct{}, len(catalog.Identifiers))
	index := make(map[policyCatalogIdentity]struct{}, len(catalog.Identifiers))
	lexicalSet := make(map[string]struct{})
	for _, identifier := range catalog.Identifiers {
		if _, ok := allowed[identifier.Schema]; !ok ||
			!validPolicyCatalogTextIdentifier(identifier.Schema) ||
			!validPolicyCatalogTextIdentifier(identifier.Relation) ||
			!validPolicyCatalogTextIdentifier(identifier.Name) ||
			identifier.Name == "" {
			return nil, nil, ErrPolicyPrivacy
		}
		switch identifier.Kind {
		case database.PolicyCatalogSchema:
			if identifier.Name != identifier.Schema || identifier.Relation != "" {
				return nil, nil, ErrPolicyPrivacy
			}
			seenSchemas[identifier.Schema] = struct{}{}
		case database.PolicyCatalogTable, database.PolicyCatalogView:
			if identifier.Relation != "" {
				return nil, nil, ErrPolicyPrivacy
			}
		case database.PolicyCatalogColumn, database.PolicyCatalogIndex, database.PolicyCatalogConstraint:
			if identifier.Relation == "" {
				return nil, nil, ErrPolicyPrivacy
			}
		default:
			return nil, nil, ErrPolicyPrivacy
		}
		identity := policyCatalogIdentity{
			kind: identifier.Kind, schema: identifier.Schema,
			relation: identifier.Relation, name: identifier.Name,
		}
		if _, duplicate := seenIdentities[identity]; duplicate {
			return nil, nil, ErrPolicyPrivacy
		}
		seenIdentities[identity] = struct{}{}
		index[identity] = struct{}{}
		for _, value := range []string{identifier.Schema, identifier.Relation, identifier.Name} {
			if value != "" {
				lexicalSet[value] = struct{}{}
			}
		}
	}
	// The target database is a private connection locator, not a catalog
	// object or metadata-tool fact. It participates only in raw-text
	// rejection and mapper-token collision checks.
	if databaseName != "" {
		lexicalSet[databaseName] = struct{}{}
	}
	for id := range index {
		table := policyCatalogIdentity{kind: database.PolicyCatalogTable, schema: id.schema, name: id.name}
		view := policyCatalogIdentity{kind: database.PolicyCatalogView, schema: id.schema, name: id.name}
		if id.kind == database.PolicyCatalogTable || id.kind == database.PolicyCatalogView {
			_, hasTable := index[table]
			_, hasView := index[view]
			if hasTable && hasView {
				return nil, nil, ErrPolicyPrivacy
			}
		}
		if id.relation != "" {
			table.name = id.relation
			view.name = id.relation
			_, hasTable := index[table]
			_, hasView := index[view]
			if hasTable == hasView {
				return nil, nil, ErrPolicyPrivacy
			}
		}
	}
	if len(seenSchemas) != len(authorized) {
		return nil, nil, ErrPolicyPrivacy
	}
	lexical := make([]string, 0, len(lexicalSet))
	for value := range lexicalSet {
		lexical = append(lexical, value)
	}
	sort.Slice(lexical, func(i, j int) bool {
		if len(lexical[i]) != len(lexical[j]) {
			return len(lexical[i]) > len(lexical[j])
		}
		return lexical[i] < lexical[j]
	})
	return index, lexical, nil
}

func validPolicyCatalogTextIdentifier(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == 0 {
			return false
		}
	}
	return true
}

func policyTextLeaksCatalogIdentifier(value string, lexical []string) bool {
	for _, identifier := range lexical {
		// Unquoted database identifiers may resolve to a catalog object with a
		// different case. Reference matching stays exact; raw-name rejection
		// is deliberately case-insensitive in either direction.
		if policyIdentifierPresent(value, identifier, true) {
			return true
		}
	}
	return false
}

func policyMapperTokenCollidesWithCatalog(mapper *privacy.RunMapper, lexical []string) bool {
	if mapper == nil {
		return true
	}
	known := make(map[string]struct{}, len(lexical))
	for _, value := range lexical {
		known[strings.ToLower(value)] = struct{}{}
	}
	for _, pair := range mapper.Snapshot() {
		if _, collision := known[strings.ToLower(pair.Token)]; collision {
			return true
		}
	}
	return false
}
