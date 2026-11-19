// state_distribution_test.go unit-level assertions for home page metrics alignment (RCP-20261023-HOME-METRICS-ALIGNMENT):
// the distribution key set is always the 18 enum values of ChangeOrderState, defaulting missing keys to 0,
// discarding out-of-enum keys, and preserving the input total.
// The integration layer (integration_test.go) complements this with real SQL aggregation asserting
// sum(keys) == change_order_total, locking down the metric definition across both layers.
//
// The key set is not hardcoded in this file: enums are read directly from api/openapi/yearning-v4.yaml and
// the db/migrations CHECK constraint so adding or removing a state in the contract fails this test immediately
// instead of letting three copies silently drift (review F3).
package dashboard

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	openAPIPath        = "../../../api/openapi/yearning-v4.yaml"
	changeOrderMigPath = "../../../db/migrations/000002_change_review.up.sql"
)

func TestNewStateDistributionHasAllStates(t *testing.T) {
	d := NewStateDistribution()
	if len(d) != len(ChangeOrderStates) {
		t.Fatalf("keys: got %d want %d", len(d), len(ChangeOrderStates))
	}
	for _, state := range ChangeOrderStates {
		count, ok := d[state]
		if !ok {
			t.Fatalf("missing key %q", state)
		}
		if count != 0 {
			t.Fatalf("key %q not zero-initialised: %d", state, count)
		}
	}
}

// TestChangeOrderStatesMatchesContractEnum compares Go ChangeOrderStates verbatim against
// the OpenAPI ChangeOrderState enum. Any addition or removal on either side fails the test.
func TestChangeOrderStatesMatchesContractEnum(t *testing.T) {
	want := openAPIChangeOrderStates(t)
	if len(want) != len(ChangeOrderStates) {
		t.Fatalf("enum length: contract=%d go=%d\ncontract: %v\ngo: %v",
			len(want), len(ChangeOrderStates), want, ChangeOrderStates)
	}
	for i, state := range want {
		if ChangeOrderStates[i] != state {
			t.Fatalf("enum[%d]: go=%q contract=%q", i, ChangeOrderStates[i], state)
		}
	}
}

// TestChangeOrderStatesMatchesDatabaseCheck proves the database CHECK constraint and Go enum
// share the exact same set: aggregation SQL can only emit states permitted by the constraint, whereas if the
// constraint allowed an extra state omitted from Go, StateDistributionFromRows would silently drop it and undercount.
func TestChangeOrderStatesMatchesDatabaseCheck(t *testing.T) {
	want := migrationChangeOrderStates(t)
	got := append([]string(nil), ChangeOrderStates...)
	sort.Strings(got)
	sort.Strings(want)
	if len(want) != len(got) {
		t.Fatalf("state count: migration=%d go=%d\nmigration: %v\ngo: %v",
			len(want), len(got), want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("state set mismatch at %d: migration=%q go=%q", i, want[i], got[i])
		}
	}
}

// openAPIChangeOrderStates reads the ChangeOrderState enum from the OpenAPI contract in YAML order.
func openAPIChangeOrderStates(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(openAPIPath)
	if err != nil {
		t.Fatalf("read openapi: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `yaml:"enum"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse openapi: %v", err)
	}
	schema, ok := doc.Components.Schemas["ChangeOrderState"]
	if !ok {
		t.Fatal("OpenAPI has no ChangeOrderState schema")
	}
	if len(schema.Enum) == 0 {
		t.Fatal("ChangeOrderState has no enum")
	}
	return schema.Enum
}

// migrationChangeOrderStates extracts the state set from the change_orders state CHECK constraint.
func migrationChangeOrderStates(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(changeOrderMigPath)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	// Locate the section from "create table change_orders (" to the first "current_stage_position"
	// and extract the parenthesized state CHECK enum there to avoid matching constraints from other tables.
	text := string(raw)
	start := strings.Index(text, "create table change_orders (")
	if start < 0 {
		t.Fatal("migration has no change_orders table")
	}
	end := strings.Index(text[start:], "current_stage_position")
	if end < 0 {
		t.Fatal("cannot delimit change_orders state column")
	}
	block := text[start : start+end]
	quoted := regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(block, -1)
	if len(quoted) == 0 {
		t.Fatal("no state literals in change_orders check constraint")
	}
	states := make([]string, 0, len(quoted))
	for _, m := range quoted {
		states = append(states, m[1])
	}
	return states
}

func TestStateDistributionFromRowsFillsZeroAndDropsUnknown(t *testing.T) {
	d := StateDistributionFromRows(map[string]int64{
		"completed":   41,
		"failed":      3,
		"not_a_state": 7, // Out-of-enum key: discarded
	})
	if len(d) != len(ChangeOrderStates) {
		t.Fatalf("keys: got %d want %d", len(d), len(ChangeOrderStates))
	}
	if d["completed"] != 41 || d["failed"] != 3 {
		t.Fatalf("known keys not carried over: %+v", d)
	}
	if d["submitted"] != 0 {
		t.Fatalf("absent key should be zero: %d", d["submitted"])
	}
	if _, ok := d["not_a_state"]; ok {
		t.Fatal("unknown state leaked into distribution")
	}
	var sum int64
	for _, count := range d {
		sum += count
	}
	if sum != 44 {
		t.Fatalf("sum: got %d want 44", sum)
	}
}
