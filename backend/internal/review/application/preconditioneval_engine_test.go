package application

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

func TestPreconditionEvaluatorUsesEngineSpecificConditionalDDL(t *testing.T) {
	conditionalColumn := fingerprint.ValidityRequirement{
		Object:    preconditionColumn,
		State:     fingerprint.ValidityRequirementAbsent,
		Condition: fingerprint.ValidityConditionIfNotExists,
	}
	conditionalColumnEffect := fingerprint.ValidityEffect{
		Action:    fingerprint.ValidityEffectCreate,
		Object:    preconditionColumn,
		Condition: fingerprint.ValidityConditionIfNotExists,
	}
	columnProgram := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationAlter,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionTable),
					conditionalColumn,
				},
				[]fingerprint.ValidityEffect{conditionalColumnEffect},
			),
		},
	}
	columnFacts := ValidityFactIndex{
		preconditionTable:  {State: ValidityFactPresent, Complete: true},
		preconditionColumn: {State: ValidityFactAbsent, Complete: true},
	}

	for _, engine := range []string{"mysql", "tidb", "oceanbase"} {
		t.Run("unsupported_"+engine, func(t *testing.T) {
			result := evaluateValidityWithEngine(t, engine, columnProgram, columnFacts)
			if result.Status != ValidityUnknown || len(result.Violations) != 0 {
				t.Fatalf("conditional column result = %+v", result)
			}
			if result.Statements[0].Reason != ValidityReasonUnsupportedEngine {
				t.Fatalf("statement result = %+v", result.Statements[0])
			}
		})
	}

	t.Run("postgresql", func(t *testing.T) {
		result := evaluateValidityWithEngine(t, "postgresql", columnProgram, columnFacts)
		if result.Status != ValidityValid {
			t.Fatalf("conditional column result = %+v", result)
		}
	})

	conditionalIndex := fingerprint.ValidityRequirement{
		Object:    preconditionIndex,
		State:     fingerprint.ValidityRequirementAbsent,
		Condition: fingerprint.ValidityConditionIfNotExists,
	}
	indexProgram := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationCreate,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionTable),
					conditionalIndex,
				},
				[]fingerprint.ValidityEffect{{
					Action:    fingerprint.ValidityEffectCreate,
					Object:    preconditionIndex,
					Condition: fingerprint.ValidityConditionIfNotExists,
				}},
			),
		},
	}
	indexFacts := ValidityFactIndex{
		preconditionTable: {State: ValidityFactPresent, Complete: true},
		preconditionIndex: {State: ValidityFactAbsent, Complete: true},
	}
	t.Run("mysql_index_unknown", func(t *testing.T) {
		result := evaluateValidityWithEngine(t, "mysql", indexProgram, indexFacts)
		if result.Status != ValidityUnknown ||
			result.Statements[0].Reason != ValidityReasonUnsupportedEngine {
			t.Fatalf("conditional index result = %+v", result)
		}
	})
	t.Run("postgresql_index", func(t *testing.T) {
		result := evaluateValidityWithEngine(t, "postgresql", indexProgram, indexFacts)
		if result.Status != ValidityValid {
			t.Fatalf("conditional index result = %+v", result)
		}
	})

	conditionalDrop := fingerprint.ValidityRequirement{
		Object:    preconditionTable,
		State:     fingerprint.ValidityRequirementPresent,
		Condition: fingerprint.ValidityConditionIfExists,
	}
	tableDropProgram := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationDrop,
				[]fingerprint.ValidityRequirement{conditionalDrop},
				[]fingerprint.ValidityEffect{{
					Action:    fingerprint.ValidityEffectDrop,
					Object:    preconditionTable,
					Condition: fingerprint.ValidityConditionIfExists,
				}},
			),
		},
	}
	for _, engine := range []string{"mysql", "postgresql"} {
		t.Run("table_if_exists_"+engine, func(t *testing.T) {
			result := evaluateValidityWithEngine(t, engine, tableDropProgram, ValidityFactIndex{
				preconditionTable: {State: ValidityFactAbsent, Complete: true},
			})
			if result.Status != ValidityValid {
				t.Fatalf("DROP TABLE IF EXISTS result = %+v", result)
			}
		})
	}
}
