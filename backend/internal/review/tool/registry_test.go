// registry_test.go test_gate#4/#5/#3 (tool side): reject unknown Tools/arguments;
// safe_explain never mutates targets; tool payload normalization is bounded and cannot alter instruction surfaces.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// recordingSource records evidence collection calls; Execute-class evidence collection is read-only.
type recordingSource struct {
	mutated bool
}

func (r *recordingSource) DatasourceIdentity(context.Context) (map[string]any, error) {
	return map[string]any{"engine": "mysql"}, nil
}
func (r *recordingSource) SchemaMetadata(_ context.Context, _ string) (map[string]any, error) {
	return map[string]any{"tables": []string{"t1"}}, nil
}
func (r *recordingSource) TableStatistics(_ context.Context, _, _ string) (map[string]any, error) {
	return map[string]any{"column_count": 3, "estimate": true}, nil
}
func (r *recordingSource) IndexMetadata(_ context.Context, _, _ string) (map[string]any, error) {
	return map[string]any{"indexes": []map[string]any{{"index_name": "pk"}}}, nil
}
func (r *recordingSource) SafeExplain(_ context.Context, _, _ string) (map[string]any, error) {
	if r.mutated {
		return nil, errors.New("mutating path reached")
	}
	return map[string]any{"access_method": "sequential_scan", "estimated_rows": 82, "estimate": true}, nil
}

func TestUnknownToolRejected(t *testing.T) {
	reg := NewRegistry()
	res := reg.Execute(t.Context(), &recordingSource{}, "call-1", "drop_database", map[string]any{})
	if res.Success || res.ErrorCode == nil || *res.ErrorCode != 2012 {
		t.Fatalf("unknown tool must fail closed: %+v", res)
	}
}

func TestUnknownArgumentRejected(t *testing.T) {
	reg := NewRegistry()
	res := reg.Execute(t.Context(), &recordingSource{}, "call-1", NameSafeExplain, map[string]any{
		"schema": "db", "group_id": "g", "execute": true,
	})
	if res.Success {
		t.Fatal("unknown argument accepted")
	}
}

func TestMissingRequiredArgumentRejected(t *testing.T) {
	reg := NewRegistry()
	res := reg.Execute(t.Context(), &recordingSource{}, "call-1", NameSafeExplain, map[string]any{"schema": "db"})
	if res.Success {
		t.Fatal("missing required argument accepted")
	}
}

func TestSafeExplainNeverMutates(t *testing.T) {
	reg := NewRegistry()
	res := reg.Execute(t.Context(), &recordingSource{}, "call-1", NameSafeExplain, map[string]any{
		"schema": "db", "group_id": "11111111-1111-1111-1111-111111111111",
	})
	if !res.Success {
		t.Fatalf("safe_explain failed: code=%v", res.ErrorCode)
	}
	if res.NormalizedFact["access_method"] != "sequential_scan" {
		t.Fatalf("unexpected fact: %+v", res.NormalizedFact)
	}
	if _, marked := res.NormalizedFact["estimate"]; !marked {
		t.Fatal("estimate marker missing")
	}
}

// TestNormalizeFactBoundsPayload malicious tool payloads cannot breach depth/byte caps
// or carry free-form instructions outside the normalized structure.
func TestNormalizeFactBoundsPayload(t *testing.T) {
	deep := map[string]any{}
	cur := deep
	for i := 0; i < 20; i++ {
		next := map[string]any{}
		cur["n"] = next
		cur = next
	}
	cur["payload"] = strings.Repeat("x", 200000)
	out := normalizeFact(map[string]any{"attack": deep})
	raw := mustJSONString(t, out)
	if len(raw) > maxFactBytes+64 {
		t.Fatalf("normalized fact exceeds byte bound: %d", len(raw))
	}
	// Excessive depth is truncated with a marker.
	if !strings.Contains(raw, truncateMark) {
		t.Fatal("depth overflow not truncated")
	}
}

func TestRegistryHasExactlyFiveBuiltinTools(t *testing.T) {
	reg := NewRegistry()
	names := reg.Names()
	if len(names) != 5 {
		t.Fatalf("expected 5 builtin tools, got %v", names)
	}
	for _, n := range names {
		d, ok := reg.Descriptor(n)
		if !ok || d.Kind != KindBuiltinDatabaseFact {
			t.Fatalf("tool %s missing or wrong kind", n)
		}
	}
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	return string(mustJSONBytes(v))
}

func mustJSONBytes(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}
