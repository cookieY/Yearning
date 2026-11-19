package application

import (
	"encoding/json"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
)

func TestPostgresCallableDecodeRequiresResultType(t *testing.T) {
	for _, kind := range []database.PostgresCallableKind{database.PostgresCallableFunction, database.PostgresCallableOperator} {
		name := "lower"
		if kind == database.PostgresCallableOperator {
			name = "="
		}
		t.Run(string(kind), func(t *testing.T) {
			binding, complete := preparePostgresObjectBindingsWithCallables(testPostgresPath(), []StatementValidityAtOrdinal{testStatement(1)}, nil, []database.PostgresCallableResolution{{Request: database.PostgresCallableRequest{Kind: kind, Name: name}, Winners: []database.PostgresCallableWinner{postgresTestWinner("pg_catalog", "23,23", "boolean", 96)}}}, true, nil)
			if !complete {
				t.Fatal("valid proof not complete")
			}
			if _, ok := decodeFrozenPostgresPathBinding(binding); !ok {
				t.Fatal("valid proof rejected")
			}
			raw, err := json.Marshal(binding)
			if err != nil {
				t.Fatal(err)
			}
			for _, missing := range []bool{false, true} {
				var value map[string]any
				if err := json.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				callable := value["callables"].(map[string]any)[database.PostgresCallablePayloadKey(kind, name)].(map[string]any)
				winner := callable["winners"].([]any)[0].(map[string]any)
				if missing {
					delete(winner, "result_type")
				} else {
					winner["result_type"] = ""
				}
				if _, ok := decodeFrozenPostgresPathBinding(value); ok {
					t.Fatalf("incomplete result type accepted: missing=%v", missing)
				}
			}
		})
	}
}
