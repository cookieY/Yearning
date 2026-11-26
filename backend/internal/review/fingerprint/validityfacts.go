package fingerprint

import (
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// ValidityOperation is the parser-confirmed statement operation used by the
// deterministic execution-precondition evaluator.
type ValidityOperation string

const (
	ValidityOperationSelect   ValidityOperation = "select"
	ValidityOperationInsert   ValidityOperation = "insert"
	ValidityOperationUpdate   ValidityOperation = "update"
	ValidityOperationDelete   ValidityOperation = "delete"
	ValidityOperationMerge    ValidityOperation = "merge"
	ValidityOperationCreate   ValidityOperation = "create"
	ValidityOperationAlter    ValidityOperation = "alter"
	ValidityOperationDrop     ValidityOperation = "drop"
	ValidityOperationTruncate ValidityOperation = "truncate"
)

// ValidityObjectKind is one of the object classes covered by the bounded
// execution-precondition model.
type ValidityObjectKind string

const (
	ValidityObjectTable  ValidityObjectKind = "table"
	ValidityObjectColumn ValidityObjectKind = "column"
	ValidityObjectIndex  ValidityObjectKind = "index"
)

// ValidityCondition records conditional DDL syntax that turns an otherwise
// invalid precondition into a successful no-op.
type ValidityCondition string

const (
	ValidityConditionNone        ValidityCondition = "none"
	ValidityConditionIfExists    ValidityCondition = "if_exists"
	ValidityConditionIfNotExists ValidityCondition = "if_not_exists"
)

// ValidityRequirementState is the observed state required by a statement.
type ValidityRequirementState string

const (
	ValidityRequirementPresent ValidityRequirementState = "present"
	ValidityRequirementAbsent  ValidityRequirementState = "absent"
)

// ValidityEffectAction is a deterministic state transition. A rename keeps
// Object as the old identity and Replacement as the new identity.
type ValidityEffectAction string

const (
	ValidityEffectCreate ValidityEffectAction = "create"
	ValidityEffectDrop   ValidityEffectAction = "drop"
	ValidityEffectRename ValidityEffectAction = "rename"
)

// ValidityObject contains only RunMapper tokens and their relationship scope.
// ParentToken distinguishes same-named columns or indexes on different tables.
type ValidityObject struct {
	Kind        ValidityObjectKind `json:"kind"`
	Token       string             `json:"token"`
	SchemaToken string             `json:"schema_token,omitempty"`
	ParentToken string             `json:"parent_token,omitempty"`
}

// ValidityRequirement is a parser-confirmed existence predicate.
type ValidityRequirement struct {
	Object    ValidityObject           `json:"object"`
	State     ValidityRequirementState `json:"state"`
	Condition ValidityCondition        `json:"condition"`
}

// ValidityEffect is a parser-confirmed existence transition.
type ValidityEffect struct {
	Action      ValidityEffectAction `json:"action"`
	Object      ValidityObject       `json:"object"`
	Replacement *ValidityObject      `json:"replacement,omitempty"`
	Condition   ValidityCondition    `json:"condition"`
}

// StatementValidity is the per-original-statement semantic program consumed
// by the ordered evaluator. Complete means the statement's addressed-object
// preconditions/effects are safe to model. DefinitionsComplete additionally
// says every addressable column/index definition was captured.
type StatementValidity struct {
	Complete            bool                  `json:"complete"`
	DefinitionsComplete bool                  `json:"definitions_complete"`
	Operation           ValidityOperation     `json:"operation"`
	Requirements        []ValidityRequirement `json:"requirements,omitempty"`
	Effects             []ValidityEffect      `json:"effects,omitempty"`
	Unsupported         string                `json:"unsupported,omitempty"`
}

type validityBuilder struct {
	toks   []Token
	walk   walkResult
	mapper *privacy.RunMapper
	out    StatementValidity
}

// addCreateTableObjectRequirements keeps only objects that must already exist.
// Columns and indexes mentioned by in-table constraints are declarations
// against the target being created, not metadata rows from the pre-statement
// catalog. A REFERENCES clause is the one exception: its external table and
// referenced columns are real preconditions.
func (b *validityBuilder) addCreateTableObjectRequirements(
	start, end int,
	target ValidityObject,
) {
	declaredColumns := map[ValidityObject]bool{}
	for pos := start; pos < end; pos++ {
		role := b.walk.roles[pos]
		if role.kind != privacy.KindColumn || role.relation != RelationDefine {
			continue
		}
		if object, ok := b.objectAt(pos); ok {
			declaredColumns[object] = true
		}
	}

	referencedColumns := map[int]bool{}
	for pos := start; pos < end; pos++ {
		if b.toks[pos].Kind != TokWord || b.toks[pos].Text != "references" {
			continue
		}
		tablePos := -1
		for next := pos + 1; next < end; next++ {
			if b.walk.roles[next].kind == privacy.KindTable {
				tablePos = next
				break
			}
		}
		if tablePos < 0 {
			continue
		}
		referencedTable, ok := b.objectAt(tablePos)
		if !ok {
			b.invalidate("unmapped_create_table_reference")
			continue
		}
		for next := tablePos + 1; next < end; next++ {
			if b.toks[next].IsPunct(",") {
				break
			}
			if !b.toks[next].IsPunct("(") {
				continue
			}
			closePos := matchingParenPosition(b.toks, next)
			if closePos < 0 {
				break
			}
			for columnPos := next + 1; columnPos < closePos; columnPos++ {
				role := b.walk.roles[columnPos]
				if role.kind != privacy.KindColumn ||
					role.relation != RelationReference {
					continue
				}
				column, mapped := b.objectAt(columnPos)
				if !mapped {
					b.invalidate("unmapped_create_table_reference")
					continue
				}
				if referencedTable == target {
					if declaredColumns[column] {
						// A self-referencing FK binds to a column created by
						// this same statement. It is not a catalog
						// precondition.
						continue
					}
					b.invalidate("unresolved_create_table_self_reference")
					continue
				}
				referencedColumns[columnPos] = true
			}
			break
		}
	}

	for pos := start; pos < end; pos++ {
		role := b.walk.roles[pos]
		switch role.kind {
		case privacy.KindTable:
			if role.relation == RelationDefine {
				continue
			}
			object, ok := b.objectAt(pos)
			if ok && object != target {
				b.addRequirement(ValidityRequirement{
					Object: object, State: ValidityRequirementPresent,
					Condition: ValidityConditionNone,
				})
			}
		case privacy.KindColumn:
			if !referencedColumns[pos] {
				continue
			}
			object, ok := b.objectAt(pos)
			if ok {
				b.addRequirement(ValidityRequirement{
					Object: object, State: ValidityRequirementPresent,
					Condition: ValidityConditionNone,
				})
			}
		case privacy.KindIndex:
			if role.relation == RelationDefine {
				continue
			}
			object, ok := b.objectAt(pos)
			if ok {
				b.addRequirement(ValidityRequirement{
					Object: object, State: ValidityRequirementPresent,
					Condition: ValidityConditionNone,
				})
			}
		}
	}
}

type tokenSpan struct {
	start int
	end   int
}

func (b *validityBuilder) parseAlterAction(
	table ValidityObject,
	start, end int,
) error {
	first := firstWord(b.toks, start, end)
	if first < 0 {
		return nil
	}
	switch b.toks[first].Text {
	case "add":
		p := first + 1
		skipWords(b.toks, &p, end, "column")
		condition := conditionFromSpan(b.toks, p, end)
		if columns := b.rolePositions(p, end, privacy.KindColumn, RelationDefine); len(columns) > 0 {
			object, ok := b.objectAt(columns[0])
			if !ok {
				b.invalidate("unmapped_add_column")
				return nil
			}
			b.addRequirement(ValidityRequirement{
				Object: object, State: ValidityRequirementAbsent, Condition: condition,
			})
			b.out.Effects = append(b.out.Effects, ValidityEffect{
				Action: ValidityEffectCreate, Object: object, Condition: condition,
			})
			b.addObjectRequirements(p, end, true)
			return nil
		}
		skipWords(b.toks, &p, end, "index", "key")
		condition = conditionFromSpan(b.toks, p, end)
		if indexes := b.rolePositions(first+1, end, privacy.KindIndex, RelationDefine); len(indexes) > 0 {
			object, ok := b.objectAt(indexes[0])
			if !ok {
				b.invalidate("unmapped_add_index")
				return nil
			}
			b.addRequirement(ValidityRequirement{
				Object: object, State: ValidityRequirementAbsent, Condition: condition,
			})
			b.out.Effects = append(b.out.Effects, ValidityEffect{
				Action: ValidityEffectCreate, Object: object, Condition: condition,
			})
			b.addObjectRequirements(first+1, end, true)
			return nil
		}
		if hasAnyWord(b.toks, first+1, end, "primary", "unique", "foreign", "check", "constraint") {
			b.invalidate("unsupported_alter_constraint")
			return nil
		}
		b.invalidate("unsupported_add_action")
	case "drop":
		p := first + 1
		skipWords(b.toks, &p, end, "column")
		condition := conditionFromSpan(b.toks, p, end)
		if indexes := b.rolePositions(p, end, privacy.KindIndex, RelationTarget); len(indexes) > 0 {
			object, ok := b.objectAt(indexes[0])
			if !ok {
				b.invalidate("unmapped_drop_index")
				return nil
			}
			b.addRequirement(ValidityRequirement{
				Object: object, State: ValidityRequirementPresent, Condition: condition,
			})
			b.out.Effects = append(b.out.Effects, ValidityEffect{
				Action: ValidityEffectDrop, Object: object, Condition: condition,
			})
			return nil
		}
		p = first + 1
		skipWords(b.toks, &p, end, "column")
		condition = conditionFromSpan(b.toks, p, end)
		if columns := b.rolePositions(p, end, privacy.KindColumn, RelationTarget); len(columns) > 0 {
			object, ok := b.objectAt(columns[0])
			if !ok {
				b.invalidate("unmapped_drop_column")
				return nil
			}
			b.addRequirement(ValidityRequirement{
				Object: object, State: ValidityRequirementPresent, Condition: condition,
			})
			b.out.Effects = append(b.out.Effects, ValidityEffect{
				Action: ValidityEffectDrop, Object: object, Condition: condition,
			})
			return nil
		}
		if hasAnyWord(b.toks, p, end, "primary", "foreign", "constraint", "check") {
			b.invalidate("unsupported_alter_constraint")
			return nil
		}
		b.invalidate("unsupported_drop_action")
	case "rename":
		return b.parseAlterRename(table, first+1, end)
	case "modify":
		columns := b.rolePositions(first+1, end, privacy.KindColumn, RelationTarget)
		if len(columns) == 0 {
			b.invalidate("unsupported_modify_action")
			return nil
		}
		object, ok := b.objectAt(columns[0])
		if !ok {
			b.invalidate("unmapped_modify_column")
			return nil
		}
		b.addRequirement(ValidityRequirement{
			Object: object, State: ValidityRequirementPresent,
			Condition: ValidityConditionNone,
		})
	case "change":
		b.invalidate("unsupported_change_action")
	case "alter":
		columns := b.rolePositions(first+1, end, privacy.KindColumn, "")
		if len(columns) == 0 {
			b.invalidate("unsupported_alter_column_action")
			return nil
		}
		object, ok := b.objectAt(columns[0])
		if !ok {
			b.invalidate("unmapped_alter_column")
			return nil
		}
		b.addRequirement(ValidityRequirement{
			Object: object, State: ValidityRequirementPresent,
			Condition: ValidityConditionNone,
		})
	case "convert", "enable", "disable", "validate", "owner", "cluster",
		"set", "attach", "detach", "replica":
		b.addObjectRequirements(first+1, end, true)
	default:
		b.invalidate("unsupported_alter_action")
	}
	return nil
}
