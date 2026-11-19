package database

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// TestGate_MetadataInterfacesTakeNoSQL Static half of acceptance gate "Agent cannot pass arbitrary metadata SQL":
// MetadataReader interface only accepts Context, PurposePool, and identifier name parameters;
// there are no SQL/query text parameters; SQL only appears on execution paths (Explain/Cursor/DML/DDL).
// The behavioral half (injection-shaped identifiers are rejected) is covered in injection tests and integration suite.
func TestGate_MetadataInterfacesTakeNoSQL(t *testing.T) {
	metadataType := reflect.TypeOf((*MetadataReader)(nil)).Elem()
	expectedParams := map[string][]string{
		"ListSchemas":         {"Context", "*PurposePool"},
		"ListTables":          {"Context", "*PurposePool", "string"},
		"ListColumns":         {"Context", "*PurposePool", "string", "string"},
		"ListIndexes":         {"Context", "*PurposePool", "string", "string"},
		"ReadSchemaSignature": {"Context", "*PurposePool", "string"},
	}
	if metadataType.NumMethod() != len(expectedParams) {
		t.Fatalf("MetadataReader method set changed: %d methods", metadataType.NumMethod())
	}
	for name, params := range expectedParams {
		m, ok := metadataType.MethodByName(name)
		if !ok {
			t.Fatalf("MetadataReader missing method %s", name)
		}
		var actual []string
		for j := 0; j < m.Type.NumIn(); j++ {
			actual = append(actual, m.Type.In(j).String())
		}
		want := make([]string, 0, len(params))
		for i, p := range params {
			switch {
			case p == "Context":
				want = append(want, "context.Context")
			case p == "*PurposePool":
				want = append(want, "*database.PurposePool")
			default:
				want = append(want, "string")
			}
			_ = i
		}
		if strings.Join(actual, ",") != strings.Join(want, ",") {
			t.Fatalf("%s parameter shape %v does not match allowed shape %v", name, actual, want)
		}
	}
	// Interface method set anchoring: adding methods to MetadataReader/Connector requires updating this gate,
	// ensuring auditability of "metadata/connection interfaces do not accept SQL text".
	connType := reflect.TypeOf((*Connector)(nil)).Elem()
	wantConnector := map[string]bool{
		"TestConnection": true, "OpenPurposePool": true,
		"ReadServerIdentity": true, "Close": true,
	}
	if connType.NumMethod() != len(wantConnector) {
		t.Fatalf("Connector method set changed (%d methods); SQL text surface must be re-evaluated", connType.NumMethod())
	}
	for i := 0; i < connType.NumMethod(); i++ {
		name := connType.Method(i).Name
		if !wantConnector[name] {
			t.Fatalf("Connector added method %s without SQL text surface review", name)
		}
	}
	// String parameters of both interfaces are restricted to named shapes for identifiers/credentials/statement fields:
	// []string variadic/slice inputs are disallowed before return values (ruling out concatenated SQL collections).
	for _, iface := range []reflect.Type{metadataType, connType} {
		for i := 0; i < iface.NumMethod(); i++ {
			m := iface.Method(i)
			for j := 0; j < m.Type.NumIn(); j++ {
				in := m.Type.In(j)
				if in.Kind() == reflect.Slice && in.Elem().Kind() == reflect.String {
					t.Fatalf("%s.%s carries string slice input (potential SQL concatenation surface)", iface.Name(), m.Name)
				}
			}
		}
	}
}

// TestGate_ExplainNeverExecutes Static half of acceptance gate "Explain never executes mutations":
// Dialect Explain prefix never contains execution options such as ANALYZE/ANALYSE/EXECUTE
// (the behavioral half asserts data is unchanged in the integration suite).
func TestGate_ExplainNeverExecutes(t *testing.T) {
	for _, e := range []string{"mysql", "postgresql"} {
		prefix := explainPrefix(e)
		upper := strings.ToUpper(prefix)
		for _, banned := range []string{"ANALYZE", "ANALYSE", "EXECUTE", "BUFFERS"} {
			if strings.Contains(upper, banned) {
				t.Fatalf("%s explain prefix %q contains execution option %s", e, prefix, banned)
			}
		}
	}
}

var insecureSkipVerifyPattern = regexp.MustCompile(`InsecureSkipVerify\s*:\s*true`)

// TestGate_NoTLSVerifySkip Legacy InitDSN defect (skipping server verification) must not recur.
func TestGate_NoTLSVerifySkip(t *testing.T) {
	for _, src := range packageSources(t) {
		if strings.HasSuffix(src.name, "_test.go") {
			continue
		}
		if loc := insecureSkipVerifyPattern.FindString(src.content); loc != "" {
			t.Fatalf("%s contains InsecureSkipVerify:true", src.name)
		}
	}
}

// TestGate_IdentifierInterpolationOnlyThroughQuoter Static half of acceptance gate "Identifiers only pass through safe quoting functions":
// schema context interpolation (USE/search_path) is only allowed in
// applySchemaContext (exec.go) and withSchemaQueryer (pool.go),
// both of which call ValidateIdentifier before quoting; no other file may concatenate identifier strings.
func TestGate_IdentifierInterpolationOnlyThroughQuoter(t *testing.T) {
	useLit := "USE " + "`"
	searchLit := "search_path = " + "\""
	for _, src := range packageSources(t) {
		if strings.HasSuffix(src.name, "_test.go") {
			continue
		}
		switch src.name {
		case "identifier.go", "pool.go", "exec.go":
			continue // Only allowed quoting path implementations
		}
		if strings.Contains(src.content, useLit) || strings.Contains(src.content, searchLit) {
			t.Fatalf("%s contains schema context concatenation; must go through applySchemaContext", src.name)
		}
	}
	// Within allowed files: USE/search_path statements must concatenate the quoted variable (direct schema value concatenation is forbidden).
	for _, src := range packageSources(t) {
		if src.name != "pool.go" && src.name != "exec.go" {
			continue
		}
		for _, line := range strings.Split(src.content, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.Contains(trimmed, useLit) && !strings.Contains(trimmed, searchLit) {
				continue
			}
			if !strings.Contains(trimmed, "quoted") {
				t.Fatalf("%s: schema context statement must reference quoted variable: %s", src.name, trimmed)
			}
		}
	}
}

type sourceFile struct {
	name    string
	content string
}

func packageSources(t *testing.T) []sourceFile {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var out []sourceFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out = append(out, sourceFile{name: e.Name(), content: string(data)})
	}
	return out
}
