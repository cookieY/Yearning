package database

import (
	"errors"
	"testing"
)

func TestPostgresCallableRejectsMissingResultType(t *testing.T) {
	for _, kind := range []PostgresCallableKind{PostgresCallableFunction, PostgresCallableOperator} {
		name := "lower"
		if kind == PostgresCallableOperator {
			name = "="
		}
		t.Run(string(kind), func(t *testing.T) {
			request := PostgresCallableRequest{Kind: kind, Name: name}
			got, err := buildPostgresCallableResolutions(validSearchPathForTest(), []PostgresCallableRequest{request}, []postgresCallableBatchRow{{Kind: string(kind), Name: name, Winners: []postgresCallableBatchWinner{{Schema: "pg_catalog", Signature: "23,23", OID: 96, PgCatalog: true}}}})
			if !errors.Is(err, ErrPostgresCallableUnresolved) || got != nil {
				t.Fatalf("missing result type accepted: %+v err=%v", got, err)
			}
		})
	}
}
