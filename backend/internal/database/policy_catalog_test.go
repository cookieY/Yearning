package database

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidatePolicyCatalogSchemas(t *testing.T) {
	for _, schemas := range [][]string{
		nil,
		{},
		{""},
		{"app", "app"},
		{"app;drop"},
	} {
		if _, err := validatePolicyCatalogSchemas(schemas); err == nil {
			t.Fatalf("schemas %#v must be rejected", schemas)
		}
	}

	tooMany := make([]string, maxPolicyCatalogSchemas+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("schema_%d", i)
	}
	if _, err := validatePolicyCatalogSchemas(tooMany); !errors.Is(err, ErrPolicyCatalogTooLarge) {
		t.Fatalf("too many schemas error = %v", err)
	}

	got, err := validatePolicyCatalogSchemas([]string{"app", "audit"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "app,audit" {
		t.Fatalf("validated schemas = %#v", got)
	}
}

func TestDecodePolicyCatalogFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		rows []policyCatalogRow
		want error
	}{
		{
			name: "missing schema",
			rows: []policyCatalogRow{{Kind: policyCatalogStatusKind, Schema: "app", Status: "missing"}},
			want: ErrPolicyCatalogSchemaUnavailable,
		},
		{
			name: "unprivileged schema",
			rows: []policyCatalogRow{{Kind: policyCatalogStatusKind, Schema: "app", Status: "unprivileged"}},
			want: ErrMetadataVisibilityUnproven,
		},
		{
			name: "missing status",
			rows: []policyCatalogRow{},
			want: ErrMetadataVisibilityUnproven,
		},
		{
			name: "unknown kind",
			rows: []policyCatalogRow{
				{Kind: policyCatalogStatusKind, Schema: "app", Status: "visible"},
				{Kind: "function", Schema: "app", Name: "danger"},
			},
			want: ErrPolicyCatalogMalformed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := normalizePolicyCatalogRows(tt.rows, []string{"app"}); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNormalizePolicyCatalogRowsDeterministicAndDeduplicated(t *testing.T) {
	rows := []policyCatalogRow{
		{Kind: policyCatalogStatusKind, Schema: "app", Status: "visible"},
		{Kind: string(PolicyCatalogTable), Schema: "app", Name: "orders"},
		{Kind: string(PolicyCatalogView), Schema: "app", Name: "orders_view"},
		{Kind: string(PolicyCatalogColumn), Schema: "app", Relation: "orders", Name: "id"},
		{Kind: string(PolicyCatalogIndex), Schema: "app", Relation: "orders", Name: "orders_pkey"},
		{Kind: string(PolicyCatalogConstraint), Schema: "app", Relation: "orders", Name: "orders_pk"},
		{Kind: string(PolicyCatalogTable), Schema: "app", Name: "orders"},
	}
	got, err := normalizePolicyCatalogRows(rows, []string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaCount != 1 || len(got.Identifiers) != 5 {
		t.Fatalf("catalog = %+v", got)
	}
	wantOrder := []PolicyCatalogKind{
		PolicyCatalogColumn,
		PolicyCatalogConstraint,
		PolicyCatalogIndex,
		PolicyCatalogTable,
		PolicyCatalogView,
	}
	for i, want := range wantOrder {
		if got.Identifiers[i].Kind != want {
			t.Fatalf("identifier %d kind = %s, want %s", i, got.Identifiers[i].Kind, want)
		}
	}
}

func TestPolicyCatalogEntryLimitNeverTruncates(t *testing.T) {
	rows := []policyCatalogRow{{Kind: policyCatalogStatusKind, Schema: "app", Status: "visible"}}
	for i := 0; i <= maxPolicyCatalogIdentifiers; i++ {
		rows = append(rows, policyCatalogRow{
			Kind: string(PolicyCatalogTable), Schema: "app", Name: fmt.Sprintf("table_%d", i),
		})
	}
	if _, err := normalizePolicyCatalogRows(rows, []string{"app"}); !errors.Is(err, ErrPolicyCatalogTooLarge) {
		t.Fatalf("overflow error = %v", err)
	}
}
