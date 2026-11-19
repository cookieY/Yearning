package application

import (
	"encoding/json"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

// Test the production comparator, supplementing the catalog/send-time fixture.
func TestPostgresCallableVerificationIdentityDrift(t *testing.T) {
	var binding reviewapp.FrozenPostgresPathBinding
	err := json.Unmarshal([]byte(`{"callables_checked":true,"callables_requested":["operator:~~"],"callables":{"operator:~~":{"kind":"operator","name":"~~","winners":[{"schema":"pg_catalog","signature":"25,25","result_type":"16","oid":1209,"pg_catalog":true}]}}}`), &binding)
	if err != nil {
		t.Fatal(err)
	}
	winner := database.PostgresCallableWinner{Schema: "pg_catalog", Signature: "25,25", ResultType: "16", OID: 1209, PgCatalog: true}
	req := database.PostgresCallableRequest{Kind: database.PostgresCallableOperator, Name: "~~"}
	if err := verifyPostgresCallables(binding, []database.PostgresCallableResolution{{Request: req, Winners: []database.PostgresCallableWinner{winner}}}); err != nil {
		t.Fatalf("unchanged identity: %v", err)
	}
	cases := []struct {
		name   string
		winner database.PostgresCallableWinner
	}{
		{"schema", database.PostgresCallableWinner{Schema: "user_schema", Signature: "25,25", ResultType: "16", OID: 1209, PgCatalog: true}},
		{"signature", database.PostgresCallableWinner{Schema: "pg_catalog", Signature: "2950,25", ResultType: "16", OID: 1209, PgCatalog: true}},
		{"oid", database.PostgresCallableWinner{Schema: "pg_catalog", Signature: "25,25", ResultType: "16", OID: 9999, PgCatalog: true}},
		{"result", database.PostgresCallableWinner{Schema: "pg_catalog", Signature: "25,25", ResultType: "25", OID: 1209, PgCatalog: true}},
		{"catalog flag", database.PostgresCallableWinner{Schema: "pg_catalog", Signature: "25,25", ResultType: "16", OID: 1209, PgCatalog: false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if verifyPostgresCallables(binding, []database.PostgresCallableResolution{{Request: req, Winners: []database.PostgresCallableWinner{tc.winner}}}) == nil {
				t.Fatal("changed identity accepted")
			}
		})
	}
	if verifyPostgresCallables(binding, nil) == nil {
		t.Fatal("missing resolution accepted")
	}
	if verifyPostgresCallables(binding, []database.PostgresCallableResolution{{Request: req}}) == nil {
		t.Fatal("missing winner accepted")
	}
}
