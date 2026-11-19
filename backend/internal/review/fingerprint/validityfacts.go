package fingerprint

import (
	"strings"

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

// extractStatementValidity converts the exact-parse role graph into a bounded
// execution program. It intentionally runs before fingerprint grouping and
// never reparses rendered SQL.
func extractStatementValidity(
	toks []Token,
	walk walkResult,
	mapper *privacy.RunMapper,
	engine string,
) (StatementValidity, error) {
	op := validityOperationOf(walk.kind)
	out := StatementValidity{
		Complete:            !walk.fail && op != "",
		DefinitionsComplete: !walk.fail && op != "",
		Operation:           op,
	}
	if mapper == nil {
		return StatementValidity{Unsupported: "mapper_unavailable"}, nil
	}
	if walk.fail || op == "" {
		out.Unsupported = "parser_fallback"
		return out, nil
	}
	if hasLeadingWord(toks, "explain", "analyze", "verbose") {
		out.Complete = false
		out.DefinitionsComplete = false
		out.Unsupported = "explain_or_analyze_prefix"
		return out, nil
	}

	b := &validityBuilder{
		toks:   toks,
		walk:   walk,
		mapper: mapper,
		out:    out,
	}
	switch b.out.Operation {
	case ValidityOperationSelect, ValidityOperationInsert,
		ValidityOperationUpdate, ValidityOperationDelete,
		ValidityOperationMerge, ValidityOperationTruncate:
		b.addObjectRequirements(0, len(toks), false)
	case ValidityOperationCreate:
		if err := b.parseCreate(); err != nil {
			return StatementValidity{}, err
		}
	case ValidityOperationDrop:
		if err := b.parseDrop(); err != nil {
			return StatementValidity{}, err
		}
	case ValidityOperationAlter:
		if err := b.parseAlter(); err != nil {
			return StatementValidity{}, err
		}
	default:
		b.out.Complete = false
		b.out.DefinitionsComplete = false
		b.out.Unsupported = "unsupported_operation"
	}
	b.gateEngineConditions(engine)
	return b.out, nil
}

type validityBuilder struct {
	toks   []Token
	walk   walkResult
	mapper *privacy.RunMapper
	out    StatementValidity
}

func (b *validityBuilder) invalidate(reason string) {
	b.out.Complete = false
	b.out.DefinitionsComplete = false
	if b.out.Unsupported == "" {
		b.out.Unsupported = reason
	}
}

func (b *validityBuilder) gateEngineConditions(engine string) {
	if engine == "" {
		return
	}
	for _, requirement := range b.out.Requirements {
		if !ValidityConditionSupported(
			engine,
			b.out.Operation,
			requirement.Object.Kind,
			requirement.Condition,
		) {
			b.invalidate("engine_unsupported_conditional_ddl")
			return
		}
	}
}

// ValidityConditionSupported reports whether the bounded conditional-DDL
// grammar is known to be supported across every contracted version of the
// actual engine. An empty engine is reserved for pure model tests. Vendor
// engines remain unknown until their full supported version range is proven.
func ValidityConditionSupported(
	engine string,
	operation ValidityOperation,
	objectKind ValidityObjectKind,
	condition ValidityCondition,
) bool {
	if condition == ValidityConditionNone {
		return true
	}
	if engine == "" {
		return true
	}
	engine = strings.ToLower(strings.TrimSpace(engine))
	switch engine {
	case "mysql":
		return operation == ValidityOperationCreate &&
			objectKind == ValidityObjectTable &&
			condition == ValidityConditionIfNotExists ||
			operation == ValidityOperationDrop &&
				objectKind == ValidityObjectTable &&
				condition == ValidityConditionIfExists
	case "postgresql":
		switch objectKind {
		case ValidityObjectTable:
			return operation == ValidityOperationCreate &&
				condition == ValidityConditionIfNotExists ||
				operation == ValidityOperationDrop &&
					condition == ValidityConditionIfExists
		case ValidityObjectIndex:
			return operation == ValidityOperationCreate &&
				condition == ValidityConditionIfNotExists ||
				operation == ValidityOperationDrop &&
					condition == ValidityConditionIfExists
		case ValidityObjectColumn:
			return operation == ValidityOperationAlter &&
				(condition == ValidityConditionIfNotExists ||
					condition == ValidityConditionIfExists)
		}
	}
	return false
}

func (b *validityBuilder) addRequirement(req ValidityRequirement) {
	if req.Object.Token == "" {
		return
	}
	for _, current := range b.out.Requirements {
		if current == req {
			return
		}
	}
	b.out.Requirements = append(b.out.Requirements, req)
}

func (b *validityBuilder) objectAt(pos int) (ValidityObject, bool) {
	if pos < 0 || pos >= len(b.walk.roles) {
		return ValidityObject{}, false
	}
	role := b.walk.roles[pos]
	var kind ValidityObjectKind
	switch role.kind {
	case privacy.KindTable:
		kind = ValidityObjectTable
	case privacy.KindColumn:
		kind = ValidityObjectColumn
	case privacy.KindIndex:
		kind = ValidityObjectIndex
	default:
		return ValidityObject{}, false
	}
	token, err := roleToken(b.mapper, role)
	if err != nil || token == "" {
		return ValidityObject{}, false
	}
	object := ValidityObject{Kind: kind, Token: token}
	if kind == ValidityObjectTable {
		if role.table.schema.name != "" {
			schema, err := identityToken(b.mapper, role.table.schema)
			if err != nil {
				return ValidityObject{}, false
			}
			object.SchemaToken = schema
		}
		return object, true
	}
	if role.parent.name == "" {
		return object, true
	}
	parent, err := tableToken(b.mapper, role.parent)
	if err != nil {
		return ValidityObject{}, false
	}
	object.ParentToken = parent
	if role.parent.schema.name != "" {
		schema, err := identityToken(b.mapper, role.parent.schema)
		if err != nil {
			return ValidityObject{}, false
		}
		object.SchemaToken = schema
	}
	return object, true
}

func (b *validityBuilder) rolePositions(
	start, end int,
	kind privacy.IdentifierKind,
	relation Relation,
) []int {
	if start < 0 {
		start = 0
	}
	if end > len(b.walk.roles) {
		end = len(b.walk.roles)
	}
	out := make([]int, 0)
	for i := start; i < end; i++ {
		role := b.walk.roles[i]
		if role.kind != kind || role.name == "" {
			continue
		}
		if relation != "" && role.relation != relation {
			continue
		}
		out = append(out, i)
	}
	return out
}

func (b *validityBuilder) physicalParentIsCTE(pos int) bool {
	if pos < 0 || pos >= len(b.walk.roles) {
		return false
	}
	return b.walk.roles[pos].parent.namespace == "cte"
}

// addObjectRequirements derives existence requirements from the exact parser's
// physical relations. Definition-side objects are excluded because their
// create effects and absence preconditions are added by the DDL handlers.
func (b *validityBuilder) addObjectRequirements(start, end int, skipTableTargets bool) {
	for pos := start; pos < end && pos < len(b.walk.roles); pos++ {
		role := b.walk.roles[pos]
		switch role.kind {
		case privacy.KindTable:
			if skipTableTargets && role.relation == RelationDefine {
				continue
			}
			object, ok := b.objectAt(pos)
			if ok {
				b.addRequirement(ValidityRequirement{
					Object: object, State: ValidityRequirementPresent,
					Condition: ValidityConditionNone,
				})
			}
		case privacy.KindColumn:
			if role.relation == RelationDefine || b.physicalParentIsCTE(pos) {
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

func matchingParenPosition(toks []Token, open int) int {
	if open < 0 || open >= len(toks) || !toks[open].IsPunct("(") {
		return -1
	}
	depth := 0
	for pos := open; pos < len(toks); pos++ {
		switch {
		case toks[pos].IsPunct("("):
			depth++
		case toks[pos].IsPunct(")"):
			depth--
			if depth == 0 {
				return pos
			}
		}
	}
	return -1
}

func (b *validityBuilder) parseCreate() error {
	subtype, subtypePos := createSubtype(b.toks)
	switch subtype {
	case "table":
		return b.parseCreateTable(subtypePos + 1)
	case "index":
		return b.parseCreateIndex(subtypePos + 1)
	case "view", "materialized":
		b.invalidate("unsupported_view")
		return nil
	case "schema", "database":
		b.invalidate("unsupported_schema_object")
		return nil
	default:
		b.invalidate("unsupported_create_object")
		return nil
	}
}

func (b *validityBuilder) parseCreateTable(start int) error {
	targets := b.rolePositions(start, len(b.toks), privacy.KindTable, RelationDefine)
	if len(targets) == 0 {
		b.invalidate("missing_create_table_target")
		return nil
	}
	targetPos := targets[0]
	target, ok := b.objectAt(targetPos)
	if !ok {
		b.invalidate("unmapped_create_table_target")
		return nil
	}
	condition := conditionFromPosition(b.toks, start)
	b.addRequirement(ValidityRequirement{
		Object: target, State: ValidityRequirementAbsent, Condition: condition,
	})
	b.addCreateTableObjectRequirements(start, len(b.toks), target)
	b.out.Effects = append(b.out.Effects, ValidityEffect{
		Action: ValidityEffectCreate, Object: target, Condition: condition,
	})

	if hasTopLevelWord(b.toks, targetPos+1, "as") {
		b.invalidate("create_table_as_select")
		return nil
	}
	if hasRolelessWord(b.toks, b.walk.roles, "like") {
		b.out.DefinitionsComplete = false
	}

	for _, pos := range b.rolePositions(start, len(b.toks), privacy.KindColumn, RelationDefine) {
		object, ok := b.objectAt(pos)
		if !ok {
			b.out.DefinitionsComplete = false
			continue
		}
		b.out.Effects = append(b.out.Effects, ValidityEffect{
			Action: ValidityEffectCreate, Object: object, Condition: condition,
		})
	}
	for _, pos := range b.rolePositions(start, len(b.toks), privacy.KindIndex, RelationDefine) {
		object, ok := b.objectAt(pos)
		if !ok {
			b.out.DefinitionsComplete = false
			continue
		}
		b.out.Effects = append(b.out.Effects, ValidityEffect{
			Action: ValidityEffectCreate, Object: object, Condition: condition,
		})
	}
	if b.walk.metadataIncomplete {
		b.out.DefinitionsComplete = false
	}
	return nil
}

func (b *validityBuilder) parseCreateIndex(start int) error {
	if b.walk.metadataIncomplete {
		b.invalidate("unsupported_index_definition")
		return nil
	}
	targets := b.rolePositions(start, len(b.toks), privacy.KindIndex, RelationDefine)
	if len(targets) == 0 {
		b.invalidate("missing_create_index_target")
		return nil
	}
	targetPos := targets[0]
	target, ok := b.objectAt(targetPos)
	if !ok {
		b.invalidate("unmapped_create_index_target")
		return nil
	}
	condition := conditionFromPosition(b.toks, start)
	b.addRequirement(ValidityRequirement{
		Object: target, State: ValidityRequirementAbsent, Condition: condition,
	})
	b.addObjectRequirements(start, len(b.toks), true)
	b.out.Effects = append(b.out.Effects, ValidityEffect{
		Action: ValidityEffectCreate, Object: target, Condition: condition,
	})
	return nil
}

func (b *validityBuilder) parseDrop() error {
	kind, kindPos := dropSubtype(b.toks)
	condition := conditionFromPosition(b.toks, kindPos+1)
	var privacyKind privacy.IdentifierKind
	var objectKind ValidityObjectKind
	switch kind {
	case "table":
		privacyKind, objectKind = privacy.KindTable, ValidityObjectTable
	case "index":
		privacyKind, objectKind = privacy.KindIndex, ValidityObjectIndex
	default:
		b.invalidate("unsupported_drop_object")
		return nil
	}
	targets := b.rolePositions(kindPos+1, len(b.toks), privacyKind, RelationTarget)
	if len(targets) == 0 {
		b.invalidate("missing_drop_target")
		return nil
	}
	for _, pos := range targets {
		target, ok := b.objectAt(pos)
		if !ok || target.Kind != objectKind {
			b.invalidate("unmapped_drop_target")
			continue
		}
		b.addRequirement(ValidityRequirement{
			Object: target, State: ValidityRequirementPresent, Condition: condition,
		})
		b.out.Effects = append(b.out.Effects, ValidityEffect{
			Action: ValidityEffectDrop, Object: target, Condition: condition,
		})
	}
	return nil
}

func (b *validityBuilder) parseAlter() error {
	tableWord := wordPosition(b.toks, "table", 0)
	if tableWord < 0 {
		b.invalidate("unsupported_alter_object")
		return nil
	}
	targets := b.rolePositions(tableWord+1, len(b.toks), privacy.KindTable, RelationTarget)
	if len(targets) == 0 {
		b.invalidate("missing_alter_target")
		return nil
	}
	targetPos := targets[0]
	target, ok := b.objectAt(targetPos)
	if !ok {
		b.invalidate("unmapped_alter_target")
		return nil
	}
	tableCondition := ValidityConditionNone
	if targetPos > tableWord+1 {
		tableCondition = conditionFromSpan(b.toks, tableWord+1, targetPos)
	}
	b.addRequirement(ValidityRequirement{
		Object: target, State: ValidityRequirementPresent, Condition: tableCondition,
	})

	spans := topLevelSpans(b.toks, targetPos+1)
	if len(spans) != 1 {
		b.invalidate("unsupported_sequential_alter_actions")
		return nil
	}
	for _, span := range spans {
		if err := b.parseAlterAction(target, span.start, span.end); err != nil {
			return err
		}
	}
	return nil
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

func (b *validityBuilder) parseAlterRename(
	table ValidityObject,
	start, end int,
) error {
	if hasAnyWord(b.toks, start, end, "column") {
		columns := b.rolePositions(start, end, privacy.KindColumn, "")
		if len(columns) < 2 {
			b.invalidate("incomplete_column_rename")
			return nil
		}
		oldObject, oldOK := b.objectAt(columns[0])
		newObject, newOK := b.objectAt(columns[len(columns)-1])
		if !oldOK || !newOK {
			b.invalidate("unmapped_column_rename")
			return nil
		}
		b.addRename(table, oldObject, newObject)
		return nil
	}
	if hasAnyWord(b.toks, start, end, "index", "key") {
		indexes := b.rolePositions(start, end, privacy.KindIndex, "")
		if len(indexes) < 2 {
			b.invalidate("incomplete_index_rename")
			return nil
		}
		oldObject, oldOK := b.objectAt(indexes[0])
		newObject, newOK := b.objectAt(indexes[len(indexes)-1])
		if !oldOK || !newOK {
			b.invalidate("unmapped_index_rename")
			return nil
		}
		b.addRename(table, oldObject, newObject)
		return nil
	}
	tables := b.rolePositions(start, end, privacy.KindTable, "")
	if len(tables) == 0 {
		b.invalidate("incomplete_table_rename")
		return nil
	}
	newTable, ok := b.objectAt(tables[len(tables)-1])
	if !ok {
		b.invalidate("unmapped_table_rename")
		return nil
	}
	b.addRename(table, table, newTable)
	return nil
}

func (b *validityBuilder) addRename(
	table, oldObject, newObject ValidityObject,
) {
	if oldObject.Kind != ValidityObjectTable {
		oldObject.ParentToken = table.Token
		newObject.ParentToken = table.Token
		oldObject.SchemaToken = table.SchemaToken
		newObject.SchemaToken = table.SchemaToken
	}
	b.addRequirement(ValidityRequirement{
		Object: oldObject, State: ValidityRequirementPresent,
		Condition: ValidityConditionNone,
	})
	b.addRequirement(ValidityRequirement{
		Object: newObject, State: ValidityRequirementAbsent,
		Condition: ValidityConditionNone,
	})
	replacement := newObject
	b.out.Effects = append(b.out.Effects, ValidityEffect{
		Action: ValidityEffectRename, Object: oldObject,
		Replacement: &replacement, Condition: ValidityConditionNone,
	})
}
