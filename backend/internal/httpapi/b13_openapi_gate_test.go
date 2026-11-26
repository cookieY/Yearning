//go:build integration

// b13_openapi_gate_test.go Gate 1/3 evidence: real HTTP responses validated field-by-field
// against frozen OpenAPI schemas (including detection of extra fields under additionalProperties:false --
// the exact drift category fixed by this package). The validator covers the JSON Schema subset used by
// this contract: type/enum/required/properties/additionalProperties/items/$ref/allOf/
// oneOf/minimum/minLength/pattern/uniqueItems.
package httpapi

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/cookieY/YearningX/backend/internal/platform/systz"
)

type openAPIDoc struct {
	Components struct {
		Schemas map[string]map[string]any `yaml:"schemas"`
	} `yaml:"components"`
}

func loadSchemas(t *testing.T) map[string]map[string]any {
	t.Helper()
	data, err := os.ReadFile(repoOpenAPIPath)
	if err != nil {
		t.Fatalf("read openapi: %v", err)
	}
	var doc openAPIDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse openapi: %v", err)
	}
	if len(doc.Components.Schemas) == 0 {
		t.Fatal("no schemas parsed")
	}
	return doc.Components.Schemas
}

// validateSchema recursively validates value against schema; returns list of violations.
func validateSchema(schemas map[string]map[string]any, schema map[string]any, value any, path string) []string {
	if ref, ok := schema["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		target, ok := schemas[name]
		if !ok {
			return []string{fmt.Sprintf("%s: unknown $ref %s", path, ref)}
		}
		return validateSchema(schemas, target, value, path)
	}
	if raw, ok := schema["allOf"].([]any); ok {
		var errs []string
		for _, sub := range raw {
			if m, ok := sub.(map[string]any); ok {
				errs = append(errs, validateSchema(schemas, m, value, path)...)
			}
		}
		return errs
	}
	if raw, ok := schema["oneOf"].([]any); ok {
		for _, sub := range raw {
			if m, ok := sub.(map[string]any); ok {
				if len(validateSchema(schemas, m, value, path)) == 0 {
					return nil
				}
			}
		}
		return []string{fmt.Sprintf("%s: matched no oneOf branch", path)}
	}
	// type may be a string or an array.
	types := []string{}
	switch tv := schema["type"].(type) {
	case string:
		types = []string{tv}
	case []any:
		for _, t := range tv {
			if s, ok := t.(string); ok {
				types = append(types, s)
			}
		}
	}
	if len(types) > 0 && !typeMatches(types, value) {
		return []string{fmt.Sprintf("%s: value %#v not of type %v", path, value, types)}
	}
	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
		found := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(value) {
				found = true
			}
		}
		if !found {
			return []string{fmt.Sprintf("%s: %v not in enum %v", path, value, enum)}
		}
	}
	obj, isObj := value.(map[string]any)
	if isObj {
		for _, r := range toStrings(schema["required"]) {
			if _, ok := obj[r]; !ok {
				return []string{fmt.Sprintf("%s: required field %q missing", path, r)}
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for k, v := range obj {
			sub, ok := props[k]
			if !ok {
				if ap, has := schema["additionalProperties"]; has && ap == false {
					return []string{fmt.Sprintf("%s: additional field %q not allowed", path, k)}
				}
				continue
			}
			if m, ok := sub.(map[string]any); ok {
				if errs := validateSchema(schemas, m, v, path+"."+k); len(errs) > 0 {
					return errs
				}
			}
		}
	}
	if arr, ok := value.([]any); ok {
		if items, ok := schema["items"].(map[string]any); ok {
			for i, item := range arr {
				if errs := validateSchema(schemas, items, item,
					fmt.Sprintf("%s[%d]", path, i)); len(errs) > 0 {
					return errs
				}
			}
		}
		if schema["uniqueItems"] == true {
			seen := map[string]bool{}
			for _, item := range arr {
				key := fmt.Sprint(item)
				if seen[key] {
					return []string{fmt.Sprintf("%s: duplicate item %v", path, item)}
				}
				seen[key] = true
			}
		}
	}
	if min, ok := schema["minimum"].(int); ok && len(types) == 1 && types[0] == "integer" {
		if n, ok := value.(float64); ok && n < float64(min) {
			return []string{fmt.Sprintf("%s: %v < minimum %d", path, n, min)}
		}
	}
	if ml, ok := schema["minLength"].(int); ok {
		if s, ok := value.(string); ok && len(s) < ml {
			return []string{fmt.Sprintf("%s: length %d < minLength %d", path, len(s), ml)}
		}
	}
	if pat, ok := schema["pattern"].(string); ok {
		if s, ok := value.(string); ok {
			if matched := regexp.MustCompile(pat).MatchString(s); !matched {
				return []string{fmt.Sprintf("%s: %q fails pattern %s", path, s, pat)}
			}
		}
	}
	return nil
}

func toStrings(raw any) []string {
	out := []string{}
	if arr, ok := raw.([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func typeMatches(types []string, value any) bool {
	for _, t := range types {
		switch t {
		case "object":
			if _, ok := value.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := value.([]any); ok {
				return true
			}
		case "string":
			if _, ok := value.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := value.(bool); ok {
				return true
			}
		case "integer":
			if _, ok := value.(float64); ok {
				return true
			}
		case "number":
			if _, ok := value.(float64); ok {
				return true
			}
		case "null":
			if value == nil {
				return true
			}
		}
	}
	return false
}

// TestResponsesMatchFrozenOpenAPI gate evidence: real response data from the four settings read
// surfaces + datasource view pass validation against their corresponding frozen schemas.
func TestResponsesMatchFrozenOpenAPI(t *testing.T) {
	schemas := loadSchemas(t)
	fx := newB3Fixture(t)
	fx.seedAdmin(t)
	if code, body, _ := fx.admin.login("admin", adminPassword); code != 200 || errCode(body) != 0 {
		t.Fatalf("login: %d %v", code, body)
	}
	fx.db.Exec(`delete from system_settings where namespace in ('general','query')`)
	systz.InvalidateDB(fx.db)
	fx.db.Exec(`delete from audit_events where event_type = 'settings.replaced'`)

	mustValidate := func(op string, schemaName string, body map[string]any) {
		t.Helper()
		if errCode(body) != 0 {
			t.Fatalf("%s: err_code %d", op, errCode(body))
		}
		data := dataMap(body)
		errs := validateSchema(schemas, map[string]any{
			"$ref": "#/components/schemas/" + schemaName}, data, op+".data")
		if len(errs) > 0 {
			t.Fatalf("%s: schema violations: %s", op, strings.Join(errs, "; "))
		}
	}

	// GET settings namespace -> SettingsNamespace.
	_, body, _ := fx.admin.do("GET", "/api/v4/admin/settings/query", nil, false)
	mustValidate("getSettingsNamespace", "SettingsNamespace", body)

	// assess -> SettingsImpactAssessment (including effects{kind, count, consequence}).
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/settings/query/impact-assessments",
		map[string]any{"settings": map[string]any{"approval_enabled": false}}, true)
	mustValidate("assessSettingsImpact", "SettingsImpactAssessment", body)

	// schema -> SettingsSchema (schema_version + ui_hints present).
	_, body, _ = fx.admin.do("GET", "/api/v4/admin/settings/ai-budget/schema", nil, false)
	mustValidate("getSettingsSchema", "SettingsSchema", body)

	// Low-impact write -> revision trail -> revisions Page envelope (items=SettingsRevision).
	_, body, _ = fx.admin.putWithIfMatch("/api/v4/admin/settings/general",
		map[string]any{"settings": map[string]any{"default_locale": "en-US"}}, "1")
	if errCode(body) != 0 {
		t.Fatalf("replace: %v", body)
	}
	mustValidate("replaceSettingsNamespace", "SettingsNamespace", body)
	_, body, _ = fx.admin.do("GET", "/api/v4/admin/settings/general/revisions?limit=10", nil, false)
	mustValidate("listSettingsRevisions", "Page", body)

	// Datasource creation view -> Datasource (tls_verified required).
	create := datasourceWrite("ds-openapi", "db-gate.internal", []any{
		replaceCred("review", "rev-user", map[string]any{"value": "revpw-9"}, nil),
	}, nil)
	_, body, _ = fx.admin.do("POST", "/api/v4/admin/datasources", create, true)
	mustValidate("createDatasource", "Datasource", body)
}
