package application

import (
	"context"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

var (
	preconditionSchema = "schema_1"
	preconditionTable  = fingerprint.ValidityObject{
		Kind:        fingerprint.ValidityObjectTable,
		Token:       "table_1",
		SchemaToken: preconditionSchema,
	}
	preconditionColumn = fingerprint.ValidityObject{
		Kind:        fingerprint.ValidityObjectColumn,
		Token:       "column_1",
		SchemaToken: preconditionSchema,
		ParentToken: preconditionTable.Token,
	}
	preconditionIndex = fingerprint.ValidityObject{
		Kind:        fingerprint.ValidityObjectIndex,
		Token:       "index_1",
		SchemaToken: preconditionSchema,
		ParentToken: preconditionTable.Token,
	}
)

func statementValidityAt(
	ordinal int32,
	operation fingerprint.ValidityOperation,
	requirements []fingerprint.ValidityRequirement,
	effects []fingerprint.ValidityEffect,
) StatementValidityAtOrdinal {
	return StatementValidityAtOrdinal{
		Ordinal: ordinal,
		Facts: fingerprint.StatementValidity{
			Complete:            true,
			DefinitionsComplete: true,
			Operation:           operation,
			Requirements:        requirements,
			Effects:             effects,
		},
	}
}

func requirePresent(object fingerprint.ValidityObject) fingerprint.ValidityRequirement {
	return fingerprint.ValidityRequirement{
		Object:    object,
		State:     fingerprint.ValidityRequirementPresent,
		Condition: fingerprint.ValidityConditionNone,
	}
}

func requireAbsent(object fingerprint.ValidityObject) fingerprint.ValidityRequirement {
	return fingerprint.ValidityRequirement{
		Object:    object,
		State:     fingerprint.ValidityRequirementAbsent,
		Condition: fingerprint.ValidityConditionNone,
	}
}

func createEffect(object fingerprint.ValidityObject) fingerprint.ValidityEffect {
	return fingerprint.ValidityEffect{
		Action:    fingerprint.ValidityEffectCreate,
		Object:    object,
		Condition: fingerprint.ValidityConditionNone,
	}
}

func dropEffect(object fingerprint.ValidityObject) fingerprint.ValidityEffect {
	return fingerprint.ValidityEffect{
		Action:    fingerprint.ValidityEffectDrop,
		Object:    object,
		Condition: fingerprint.ValidityConditionNone,
	}
}

func evaluateValidity(t *testing.T, program ValidityProgram, facts ValidityFactIndex) ValidityResult {
	t.Helper()
	return evaluateValidityWithEngine(t, "", program, facts)
}

func evaluateValidityWithEngine(
	t *testing.T,
	engine string,
	program ValidityProgram,
	facts ValidityFactIndex,
) ValidityResult {
	t.Helper()
	result, err := NewPreconditionEvaluator().Evaluate(context.Background(), ValidityInput{
		Engine:  engine,
		Program: program,
		Facts:   facts,
	})
	if err != nil {
		t.Fatalf("evaluate preconditions: %v", err)
	}
	return result
}

func requireEvidenceIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	seen := make(map[string]bool, len(got))
	for _, id := range got {
		seen[id] = true
	}
	if len(got) != len(want) {
		t.Fatalf("evidence IDs = %v, want %v", got, want)
	}
	for _, id := range want {
		if !seen[id] {
			t.Fatalf("evidence IDs = %v, want %v", got, want)
		}
	}
}

func TestPreconditionEvaluatorRequiresPresentTable(t *testing.T) {
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{requirePresent(preconditionTable)},
				nil,
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {
			State:       ValidityFactAbsent,
			Complete:    true,
			EvidenceIDs: []string{"evidence-table-absent"},
		},
	})
	if result.Status != ValidityInvalid || len(result.Violations) != 1 {
		t.Fatalf("result = %+v, want one invalidity", result)
	}
	violation := result.Violations[0]
	if violation.Ordinal != 1 ||
		violation.Object != preconditionTable ||
		violation.Required != fingerprint.ValidityRequirementPresent ||
		violation.Observed != ValidityFactAbsent ||
		violation.Reason != ViolationRequiredPresentObservedAbsent ||
		len(violation.EvidenceIDs) != 1 ||
		violation.EvidenceIDs[0] != "evidence-table-absent" {
		t.Fatalf("violation = %+v", violation)
	}
}

func TestPreconditionEvaluatorViolationBeforeLaterUnknownWins(t *testing.T) {
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationUpdate,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionTable),
					requirePresent(preconditionColumn),
				},
				nil,
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {
			State:       ValidityFactAbsent,
			Complete:    true,
			EvidenceIDs: []string{"evidence-table-absent"},
		},
	})
	if result.Status != ValidityInvalid || len(result.Violations) != 1 {
		t.Fatalf("known violation followed by unknown = %+v", result)
	}
	if result.Statements[0].Status != ValidityInvalid {
		t.Fatalf("statement result = %+v", result.Statements[0])
	}
	requireEvidenceIDs(t, result.Violations[0].EvidenceIDs, "evidence-table-absent")
}

func TestPreconditionEvaluatorRequiresOrdinalOnePrefix(t *testing.T) {
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				5,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{requirePresent(preconditionTable)},
				nil,
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {
			State:       ValidityFactAbsent,
			Complete:    true,
			EvidenceIDs: []string{"evidence-table-absent"},
		},
	})
	if result.Status != ValidityUnknown || len(result.Violations) != 0 {
		t.Fatalf("non-one prefix result = %+v", result)
	}
	if len(result.Statements) != 1 ||
		result.Statements[0].Reason != ValidityReasonIncompleteProgram {
		t.Fatalf("statement results = %+v", result.Statements)
	}
}

func TestPreconditionEvaluatorDoesNotCrossOrdinalGap(t *testing.T) {
	laterTable := preconditionTable
	laterTable.Token = "table_2"
	program := ValidityProgram{
		Complete: false,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{requirePresent(preconditionTable)},
				nil,
			),
			statementValidityAt(
				3,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{requirePresent(laterTable)},
				nil,
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {State: ValidityFactPresent, Complete: true},
		laterTable:        {State: ValidityFactAbsent, Complete: true},
	})
	if result.Status != ValidityUnknown || len(result.Violations) != 0 {
		t.Fatalf("ordinal gap result = %+v", result)
	}
	if result.Statements[1].Reason != ValidityReasonIncompleteProgram {
		t.Fatalf("gap statement = %+v", result.Statements[1])
	}
}

func TestPreconditionEvaluatorKeepsInvalidPrefixBeforeGap(t *testing.T) {
	laterTable := preconditionTable
	laterTable.Token = "table_2"
	program := ValidityProgram{
		Complete: false,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{requirePresent(preconditionTable)},
				nil,
			),
			statementValidityAt(
				3,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{requirePresent(laterTable)},
				nil,
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {
			State:       ValidityFactAbsent,
			Complete:    true,
			EvidenceIDs: []string{"evidence-table-absent"},
		},
		laterTable: {State: ValidityFactPresent, Complete: true},
	})
	if result.Status != ValidityInvalid || len(result.Violations) != 1 {
		t.Fatalf("invalid prefix before gap = %+v", result)
	}
	if result.Violations[0].Ordinal != 1 ||
		result.Statements[1].Reason != ValidityReasonIncompleteProgram {
		t.Fatalf("result = %+v", result)
	}
}

func TestPreconditionEvaluatorDoesNotPassEarlierUnknownToFindViolation(t *testing.T) {
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationUpdate,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionColumn),
					requirePresent(preconditionTable),
				},
				nil,
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {
			State:       ValidityFactAbsent,
			Complete:    true,
			EvidenceIDs: []string{"evidence-table-absent"},
		},
	})
	if result.Status != ValidityUnknown || len(result.Violations) != 0 {
		t.Fatalf("unknown before later violation = %+v", result)
	}
	if result.Statements[0].Reason != ValidityReasonMissingFact {
		t.Fatalf("statement result = %+v", result.Statements[0])
	}
}

func TestPreconditionEvaluatorUsesOrderedEffects(t *testing.T) {
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationCreate,
				[]fingerprint.ValidityRequirement{requireAbsent(preconditionTable)},
				[]fingerprint.ValidityEffect{createEffect(preconditionTable)},
			),
			statementValidityAt(
				2,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{requirePresent(preconditionTable)},
				nil,
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {State: ValidityFactAbsent, Complete: true},
	})
	if result.Status != ValidityValid {
		t.Fatalf("create-then-select result = %+v", result)
	}
	if len(result.Statements) != 2 ||
		result.Statements[0].Status != ValidityValid ||
		result.Statements[1].Status != ValidityValid {
		t.Fatalf("statement results = %+v", result.Statements)
	}
}

func TestPreconditionEvaluatorRejectsDuplicateCreate(t *testing.T) {
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationCreate,
				[]fingerprint.ValidityRequirement{requireAbsent(preconditionTable)},
				[]fingerprint.ValidityEffect{createEffect(preconditionTable)},
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {State: ValidityFactPresent, Complete: true},
	})
	if result.Status != ValidityInvalid || len(result.Violations) != 1 {
		t.Fatalf("duplicate create result = %+v", result)
	}
	if result.Violations[0].Reason != ViolationRequiredAbsentObservedPresent {
		t.Fatalf("duplicate create violation = %+v", result.Violations[0])
	}
}

func TestPreconditionEvaluatorPreservesEvidenceThroughDerivedEffects(t *testing.T) {
	tests := []struct {
		name       string
		first      StatementValidityAtOrdinal
		second     StatementValidityAtOrdinal
		initial    ValidityFactIndex
		wantReason ValidityViolationReason
	}{
		{
			name: "create then duplicate",
			first: statementValidityAt(
				1,
				fingerprint.ValidityOperationCreate,
				[]fingerprint.ValidityRequirement{requireAbsent(preconditionTable)},
				[]fingerprint.ValidityEffect{createEffect(preconditionTable)},
			),
			second: statementValidityAt(
				2,
				fingerprint.ValidityOperationCreate,
				[]fingerprint.ValidityRequirement{requireAbsent(preconditionTable)},
				[]fingerprint.ValidityEffect{createEffect(preconditionTable)},
			),
			initial: ValidityFactIndex{
				preconditionTable: {
					State:       ValidityFactAbsent,
					Complete:    true,
					EvidenceIDs: []string{"evidence-table-absent"},
				},
			},
			wantReason: ViolationRequiredAbsentObservedPresent,
		},
		{
			name: "drop then use",
			first: statementValidityAt(
				1,
				fingerprint.ValidityOperationDrop,
				[]fingerprint.ValidityRequirement{requirePresent(preconditionTable)},
				[]fingerprint.ValidityEffect{dropEffect(preconditionTable)},
			),
			second: statementValidityAt(
				2,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{requirePresent(preconditionTable)},
				nil,
			),
			initial: ValidityFactIndex{
				preconditionTable: {
					State:       ValidityFactPresent,
					Complete:    true,
					EvidenceIDs: []string{"evidence-table-present"},
				},
			},
			wantReason: ViolationRequiredPresentObservedAbsent,
		},
		{
			name: "create column then duplicate",
			first: statementValidityAt(
				1,
				fingerprint.ValidityOperationAlter,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionTable),
					requireAbsent(preconditionColumn),
				},
				[]fingerprint.ValidityEffect{createEffect(preconditionColumn)},
			),
			second: statementValidityAt(
				2,
				fingerprint.ValidityOperationAlter,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionTable),
					requireAbsent(preconditionColumn),
				},
				[]fingerprint.ValidityEffect{createEffect(preconditionColumn)},
			),
			initial: ValidityFactIndex{
				preconditionTable: {
					State:       ValidityFactPresent,
					Complete:    true,
					EvidenceIDs: []string{"evidence-table-present"},
				},
				preconditionColumn: {
					State:       ValidityFactAbsent,
					Complete:    true,
					EvidenceIDs: []string{"evidence-column-absent"},
				},
			},
			wantReason: ViolationRequiredAbsentObservedPresent,
		},
		{
			name: "drop column then use",
			first: statementValidityAt(
				1,
				fingerprint.ValidityOperationAlter,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionTable),
					requirePresent(preconditionColumn),
				},
				[]fingerprint.ValidityEffect{dropEffect(preconditionColumn)},
			),
			second: statementValidityAt(
				2,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionTable),
					requirePresent(preconditionColumn),
				},
				nil,
			),
			initial: ValidityFactIndex{
				preconditionTable: {
					State:       ValidityFactPresent,
					Complete:    true,
					EvidenceIDs: []string{"evidence-table-present"},
				},
				preconditionColumn: {
					State:       ValidityFactPresent,
					Complete:    true,
					EvidenceIDs: []string{"evidence-column-present"},
				},
			},
			wantReason: ViolationRequiredPresentObservedAbsent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := evaluateValidity(t, ValidityProgram{
				Complete: true,
				Statements: []StatementValidityAtOrdinal{
					tt.first,
					tt.second,
				},
			}, tt.initial)
			if result.Status != ValidityInvalid || len(result.Violations) != 1 {
				t.Fatalf("result = %+v, want one derived invalidity", result)
			}
			if result.Violations[0].Ordinal != 2 || result.Violations[0].Reason != tt.wantReason {
				t.Fatalf("violation = %+v", result.Violations[0])
			}
			switch tt.second.Facts.Requirements[0].Object {
			case preconditionTable:
				switch {
				case tt.name == "create then duplicate":
					requireEvidenceIDs(t, result.Violations[0].EvidenceIDs, "evidence-table-absent")
				case tt.name == "drop then use":
					requireEvidenceIDs(t, result.Violations[0].EvidenceIDs, "evidence-table-present")
				}
			case preconditionColumn:
				switch tt.name {
				case "create column then duplicate":
					requireEvidenceIDs(
						t,
						result.Violations[0].EvidenceIDs,
						"evidence-column-absent",
						"evidence-table-present",
					)
				case "drop column then use":
					requireEvidenceIDs(
						t,
						result.Violations[0].EvidenceIDs,
						"evidence-column-present",
						"evidence-table-present",
					)
				}
			}
		})
	}
}

func TestPreconditionEvaluatorHandlesConditionalNoOps(t *testing.T) {
	conditionalCreate := fingerprint.ValidityRequirement{
		Object:    preconditionTable,
		State:     fingerprint.ValidityRequirementAbsent,
		Condition: fingerprint.ValidityConditionIfNotExists,
	}
	conditionalDrop := fingerprint.ValidityRequirement{
		Object:    preconditionTable,
		State:     fingerprint.ValidityRequirementPresent,
		Condition: fingerprint.ValidityConditionIfExists,
	}
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationCreate,
				[]fingerprint.ValidityRequirement{conditionalCreate},
				[]fingerprint.ValidityEffect{{
					Action:    fingerprint.ValidityEffectCreate,
					Object:    preconditionTable,
					Condition: fingerprint.ValidityConditionIfNotExists,
				}},
			),
			statementValidityAt(
				2,
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

	for _, initial := range []ValidityFactState{ValidityFactPresent, ValidityFactAbsent, ValidityFactUnknown} {
		t.Run(string(initial), func(t *testing.T) {
			facts := ValidityFactIndex{}
			if initial != ValidityFactUnknown {
				facts[preconditionTable] = ValidityObjectFact{State: initial, Complete: true}
			}
			result := evaluateValidity(t, program, facts)
			if result.Status != ValidityValid {
				t.Fatalf("conditional create/drop from %q = %+v", initial, result)
			}
		})
	}
}

func TestPreconditionEvaluatorAppliesConditionalEffectsPerTarget(t *testing.T) {
	absentTable := preconditionTable
	absentTable.Token = "t_absent"
	presentTable := preconditionTable
	presentTable.Token = "t_present"

	conditionalDrop := func(object fingerprint.ValidityObject) fingerprint.ValidityRequirement {
		return fingerprint.ValidityRequirement{
			Object:    object,
			State:     fingerprint.ValidityRequirementPresent,
			Condition: fingerprint.ValidityConditionIfExists,
		}
	}
	conditionalDropEffect := func(object fingerprint.ValidityObject) fingerprint.ValidityEffect {
		return fingerprint.ValidityEffect{
			Action:    fingerprint.ValidityEffectDrop,
			Object:    object,
			Condition: fingerprint.ValidityConditionIfExists,
		}
	}

	t.Run("mixed present and absent tables", func(t *testing.T) {
		program := ValidityProgram{
			Complete: true,
			Statements: []StatementValidityAtOrdinal{
				statementValidityAt(
					1,
					fingerprint.ValidityOperationDrop,
					[]fingerprint.ValidityRequirement{
						conditionalDrop(absentTable),
						conditionalDrop(presentTable),
					},
					[]fingerprint.ValidityEffect{
						conditionalDropEffect(absentTable),
						conditionalDropEffect(presentTable),
					},
				),
				statementValidityAt(
					2,
					fingerprint.ValidityOperationSelect,
					[]fingerprint.ValidityRequirement{requirePresent(presentTable)},
					nil,
				),
			},
		}
		result := evaluateValidity(t, program, ValidityFactIndex{
			absentTable:  {State: ValidityFactAbsent, Complete: true},
			presentTable: {State: ValidityFactPresent, Complete: true},
		})
		if result.Status != ValidityInvalid || len(result.Violations) != 1 {
			t.Fatalf("mixed conditional DROP result = %+v", result)
		}
		if result.Violations[0].Ordinal != 2 ||
			result.Violations[0].Object != presentTable ||
			result.Violations[0].Observed != ValidityFactAbsent {
			t.Fatalf("mixed conditional DROP violation = %+v", result.Violations[0])
		}
	})

	t.Run("duplicate target still drops once", func(t *testing.T) {
		program := ValidityProgram{
			Complete: true,
			Statements: []StatementValidityAtOrdinal{
				statementValidityAt(
					1,
					fingerprint.ValidityOperationDrop,
					[]fingerprint.ValidityRequirement{
						conditionalDrop(presentTable),
						conditionalDrop(presentTable),
					},
					[]fingerprint.ValidityEffect{
						conditionalDropEffect(presentTable),
						conditionalDropEffect(presentTable),
					},
				),
				statementValidityAt(
					2,
					fingerprint.ValidityOperationSelect,
					[]fingerprint.ValidityRequirement{requirePresent(presentTable)},
					nil,
				),
			},
		}
		result := evaluateValidity(t, program, ValidityFactIndex{
			presentTable: {State: ValidityFactPresent, Complete: true},
		})
		if result.Status != ValidityInvalid || len(result.Violations) != 1 ||
			result.Violations[0].Object != presentTable {
			t.Fatalf("duplicate conditional DROP result = %+v", result)
		}
	})

	t.Run("mixed present and absent indexes", func(t *testing.T) {
		absentIndex := preconditionIndex
		absentIndex.Token = "i_absent"
		presentIndex := preconditionIndex
		presentIndex.Token = "i_present"
		program := ValidityProgram{
			Complete: true,
			Statements: []StatementValidityAtOrdinal{
				statementValidityAt(
					1,
					fingerprint.ValidityOperationDrop,
					[]fingerprint.ValidityRequirement{
						conditionalDrop(absentIndex),
						conditionalDrop(presentIndex),
					},
					[]fingerprint.ValidityEffect{
						conditionalDropEffect(absentIndex),
						conditionalDropEffect(presentIndex),
					},
				),
				statementValidityAt(
					2,
					fingerprint.ValidityOperationSelect,
					[]fingerprint.ValidityRequirement{requirePresent(presentIndex)},
					nil,
				),
			},
		}
		result := evaluateValidity(t, program, ValidityFactIndex{
			absentIndex:  {State: ValidityFactAbsent, Complete: true},
			presentIndex: {State: ValidityFactPresent, Complete: true},
		})
		if result.Status != ValidityInvalid || len(result.Violations) != 1 ||
			result.Violations[0].Object != presentIndex {
			t.Fatalf("mixed conditional index DROP result = %+v", result)
		}
	})
}

func TestPreconditionEvaluatorConditionalCreatePreservesExistingDefinition(t *testing.T) {
	conditionalCreate := fingerprint.ValidityRequirement{
		Object:    preconditionTable,
		State:     fingerprint.ValidityRequirementAbsent,
		Condition: fingerprint.ValidityConditionIfNotExists,
	}
	createNoOp := fingerprint.ValidityEffect{
		Action:    fingerprint.ValidityEffectCreate,
		Object:    preconditionTable,
		Condition: fingerprint.ValidityConditionIfNotExists,
	}
	createColumnNoOp := fingerprint.ValidityEffect{
		Action:    fingerprint.ValidityEffectCreate,
		Object:    preconditionColumn,
		Condition: fingerprint.ValidityConditionIfNotExists,
	}
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationCreate,
				[]fingerprint.ValidityRequirement{conditionalCreate},
				[]fingerprint.ValidityEffect{createNoOp, createColumnNoOp},
			),
			statementValidityAt(
				2,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionTable),
					requirePresent(preconditionColumn),
				},
				nil,
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {
			State:       ValidityFactPresent,
			Complete:    true,
			EvidenceIDs: []string{"evidence-table-present"},
		},
		preconditionColumn: {
			State:       ValidityFactAbsent,
			Complete:    true,
			EvidenceIDs: []string{"evidence-column-absent"},
		},
	})
	if result.Status != ValidityInvalid || len(result.Violations) != 1 {
		t.Fatalf("conditional create overwrote the existing definition: %+v", result)
	}
	if result.Violations[0].Object != preconditionColumn ||
		result.Violations[0].Reason != ViolationRequiredPresentObservedAbsent {
		t.Fatalf("violation = %+v", result.Violations[0])
	}
}

func TestPreconditionEvaluatorMissingFactIsUnknown(t *testing.T) {
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{requirePresent(preconditionTable)},
				nil,
			),
		},
	}
	result := evaluateValidity(t, program, nil)
	if result.Status != ValidityUnknown || len(result.Violations) != 0 {
		t.Fatalf("missing fact result = %+v", result)
	}
	if result.Statements[0].Reason != ValidityReasonMissingFact {
		t.Fatalf("statement reason = %q", result.Statements[0].Reason)
	}
}

func TestPreconditionEvaluatorUnknownPrefixBlocksLaterProof(t *testing.T) {
	incomplete := StatementValidityAtOrdinal{
		Ordinal: 1,
		Facts: fingerprint.StatementValidity{
			Complete:    false,
			Operation:   fingerprint.ValidityOperationAlter,
			Unsupported: "unsupported_action",
		},
	}
	program := ValidityProgram{
		Complete: false,
		Statements: []StatementValidityAtOrdinal{
			incomplete,
			statementValidityAt(
				2,
				fingerprint.ValidityOperationDrop,
				[]fingerprint.ValidityRequirement{requirePresent(preconditionTable)},
				[]fingerprint.ValidityEffect{dropEffect(preconditionTable)},
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {State: ValidityFactAbsent, Complete: true},
	})
	if result.Status != ValidityUnknown || len(result.Violations) != 0 {
		t.Fatalf("unknown-prefix result = %+v", result)
	}
	if result.Statements[1].Reason != ValidityReasonAfterUnknown {
		t.Fatalf("later statement result = %+v", result.Statements[1])
	}
}

func TestPreconditionEvaluatorProvenInvalidPrefixWins(t *testing.T) {
	program := ValidityProgram{
		Complete: false,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{requirePresent(preconditionTable)},
				nil,
			),
			{
				Ordinal: 2,
				Facts: fingerprint.StatementValidity{
					Complete:    false,
					Operation:   fingerprint.ValidityOperationAlter,
					Unsupported: "unsupported_action",
				},
			},
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {State: ValidityFactAbsent, Complete: true},
	})
	if result.Status != ValidityInvalid || len(result.Violations) != 1 {
		t.Fatalf("invalid-prefix result = %+v", result)
	}
	if result.Statements[1].Reason != ValidityReasonAfterInvalid {
		t.Fatalf("later statement result = %+v", result.Statements[1])
	}
}

func TestPreconditionEvaluatorColumnAndIndexTransitions(t *testing.T) {
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationAlter,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionTable),
					requireAbsent(preconditionColumn),
					requireAbsent(preconditionIndex),
				},
				[]fingerprint.ValidityEffect{
					createEffect(preconditionColumn),
					createEffect(preconditionIndex),
				},
			),
			statementValidityAt(
				2,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionColumn),
					requirePresent(preconditionIndex),
				},
				nil,
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable:  {State: ValidityFactPresent, Complete: true},
		preconditionColumn: {State: ValidityFactAbsent, Complete: true},
		preconditionIndex:  {State: ValidityFactAbsent, Complete: true},
	})
	if result.Status != ValidityValid {
		t.Fatalf("column/index transitions = %+v", result)
	}
}

func TestPreconditionEvaluatorRenameMovesObjectIdentity(t *testing.T) {
	renamedTable := preconditionTable
	renamedTable.Token = "table_2"
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			statementValidityAt(
				1,
				fingerprint.ValidityOperationAlter,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionTable),
					requireAbsent(renamedTable),
				},
				[]fingerprint.ValidityEffect{{
					Action:      fingerprint.ValidityEffectRename,
					Object:      preconditionTable,
					Replacement: &renamedTable,
					Condition:   fingerprint.ValidityConditionNone,
				}},
			),
			statementValidityAt(
				2,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{requirePresent(renamedTable)},
				nil,
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {State: ValidityFactPresent, Complete: true},
		renamedTable:      {State: ValidityFactAbsent, Complete: true},
	})
	if result.Status != ValidityValid {
		t.Fatalf("rename result = %+v", result)
	}
}

func TestPreconditionEvaluatorDefinitionsIncompleteOnlyPoisonsDependents(t *testing.T) {
	createTable := statementValidityAt(
		1,
		fingerprint.ValidityOperationCreate,
		[]fingerprint.ValidityRequirement{requireAbsent(preconditionTable)},
		[]fingerprint.ValidityEffect{createEffect(preconditionTable)},
	)
	createTable.Facts.DefinitionsComplete = false
	program := ValidityProgram{
		Complete: true,
		Statements: []StatementValidityAtOrdinal{
			createTable,
			statementValidityAt(
				2,
				fingerprint.ValidityOperationSelect,
				[]fingerprint.ValidityRequirement{
					requirePresent(preconditionTable),
					requirePresent(preconditionColumn),
				},
				nil,
			),
		},
	}
	result := evaluateValidity(t, program, ValidityFactIndex{
		preconditionTable: {State: ValidityFactAbsent, Complete: true},
	})
	if result.Status != ValidityUnknown {
		t.Fatalf("incomplete definitions result = %+v", result)
	}
	if result.Statements[1].Reason != ValidityReasonIncompleteDefinitions {
		t.Fatalf("dependent reason = %q", result.Statements[1].Reason)
	}
}
