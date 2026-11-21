package application

import (
	"context"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

// ValidityStatus is the tri-state result of bounded execution-precondition
// analysis. Valid means no violation was proven in the modeled existence
// scope; it is not a general proof that every server check will succeed.
type ValidityStatus string

const (
	ValidityValid   ValidityStatus = "valid"
	ValidityInvalid ValidityStatus = "invalid"
	ValidityUnknown ValidityStatus = "unknown"
)

// ValidityFactState is one observed or batch-derived object state.
type ValidityFactState string

const (
	ValidityFactPresent ValidityFactState = "present"
	ValidityFactAbsent  ValidityFactState = "absent"
	ValidityFactUnknown ValidityFactState = "unknown"
)

// ValidityReason is a bounded, non-SQL reason code for a statement result.
type ValidityReason string

const (
	ValidityReasonComplete              ValidityReason = "complete"
	ValidityReasonMissingFact           ValidityReason = "missing_fact"
	ValidityReasonIncompleteStatement   ValidityReason = "incomplete_statement"
	ValidityReasonIncompleteDefinitions ValidityReason = "incomplete_definitions"
	ValidityReasonIncompleteProgram     ValidityReason = "incomplete_program"
	ValidityReasonAfterUnknown          ValidityReason = "after_unknown"
	ValidityReasonAfterInvalid          ValidityReason = "after_invalid"
	ValidityReasonUnsupportedEngine     ValidityReason = "unsupported_engine_semantics"
)

// ValidityViolationReason identifies which deterministic predicate failed.
type ValidityViolationReason string

const (
	ViolationRequiredPresentObservedAbsent ValidityViolationReason = "required_present_observed_absent"
	ViolationRequiredAbsentObservedPresent ValidityViolationReason = "required_absent_observed_present"
)

// StatementValidityAtOrdinal binds one original statement's parser facts to
// its one-based batch position. Fingerprint grouping is deliberately absent.
type StatementValidityAtOrdinal struct {
	Ordinal int32                         `json:"ordinal"`
	Facts   fingerprint.StatementValidity `json:"facts"`
}

// ValidityProgram is an ordered, bounded batch of original statements.
type ValidityProgram struct {
	Complete   bool                         `json:"complete"`
	Statements []StatementValidityAtOrdinal `json:"statements"`
}

// ValidityObjectFact is a scoped metadata observation. Missing entries mean
// unknown; callers must never infer absence from a missing map key.
type ValidityObjectFact struct {
	State       ValidityFactState `json:"state"`
	Complete    bool              `json:"complete"`
	EvidenceIDs []string          `json:"evidence_ids,omitempty"`
}

type ValidityFactIndex map[fingerprint.ValidityObject]ValidityObjectFact

// PreconditionViolation is the deterministic proof attached to a Critical
// system finding. It contains tokens and evidence IDs, never SQL or raw names.
type PreconditionViolation struct {
	Ordinal     int32                                `json:"ordinal"`
	Object      fingerprint.ValidityObject           `json:"object"`
	Required    fingerprint.ValidityRequirementState `json:"required"`
	Observed    ValidityFactState                    `json:"observed"`
	Condition   fingerprint.ValidityCondition        `json:"condition"`
	Reason      ValidityViolationReason              `json:"reason"`
	EvidenceIDs []string                             `json:"evidence_ids,omitempty"`
}

type ValidityStatementResult struct {
	Ordinal int32          `json:"ordinal"`
	Status  ValidityStatus `json:"status"`
	Reason  ValidityReason `json:"reason"`
}

type ValidityResult struct {
	Status     ValidityStatus            `json:"status"`
	Statements []ValidityStatementResult `json:"statements"`
	Violations []PreconditionViolation   `json:"violations,omitempty"`
}

type ValidityInput struct {
	Engine  string            `json:"engine,omitempty"`
	Program ValidityProgram   `json:"program"`
	Facts   ValidityFactIndex `json:"facts"`
}

// PreconditionEvaluator is a pure deterministic evaluator. Methods never
// connect to a database, execute SQL, or call a Provider.
type PreconditionEvaluator struct{}

// NewPreconditionEvaluator returns the deterministic in-process evaluator.
func NewPreconditionEvaluator() PreconditionEvaluator {
	return PreconditionEvaluator{}
}

func (PreconditionEvaluator) Evaluate(
	_ context.Context,
	in ValidityInput,
) (ValidityResult, error) {
	state := newEvaluatorState(in.Facts)
	result := ValidityResult{
		Status:     ValidityValid,
		Statements: make([]ValidityStatementResult, 0, len(in.Program.Statements)),
	}
	var priorOrdinal int32
	stop := ValidityStatus("")
	for _, statement := range in.Program.Statements {
		if statement.Ordinal != priorOrdinal+1 {
			if stop == "" {
				stop = ValidityUnknown
			}
			result.Statements = append(result.Statements, ValidityStatementResult{
				Ordinal: statement.Ordinal, Status: ValidityUnknown,
				Reason: ValidityReasonIncompleteProgram,
			})
			break
		}
		priorOrdinal = statement.Ordinal
		if stop == ValidityInvalid {
			result.Statements = append(result.Statements, ValidityStatementResult{
				Ordinal: statement.Ordinal, Status: ValidityUnknown,
				Reason: ValidityReasonAfterInvalid,
			})
			continue
		}
		if stop == ValidityUnknown {
			result.Statements = append(result.Statements, ValidityStatementResult{
				Ordinal: statement.Ordinal, Status: ValidityUnknown,
				Reason: ValidityReasonAfterUnknown,
			})
			continue
		}
		status, reason, violations := state.evaluate(statement, in.Engine)
		result.Statements = append(result.Statements, ValidityStatementResult{
			Ordinal: statement.Ordinal, Status: status, Reason: reason,
		})
		if status == ValidityInvalid {
			result.Violations = append(result.Violations, violations...)
			stop = ValidityInvalid
			continue
		}
		if status == ValidityUnknown {
			stop = ValidityUnknown
		}
	}

	if stop == ValidityInvalid {
		result.Status = ValidityInvalid
	} else if stop == ValidityUnknown || !in.Program.Complete {
		result.Status = ValidityUnknown
	}
	if len(result.Statements) == 0 && !in.Program.Complete {
		result.Status = ValidityUnknown
	}
	return result, nil
}

type evaluatorState struct {
	objects          map[fingerprint.ValidityObject]ValidityObjectFact
	tableDefinitions map[fingerprint.ValidityObject]bool
}

func newEvaluatorState(facts ValidityFactIndex) *evaluatorState {
	out := &evaluatorState{
		objects:          make(map[fingerprint.ValidityObject]ValidityObjectFact, len(facts)),
		tableDefinitions: map[fingerprint.ValidityObject]bool{},
	}
	for object, fact := range facts {
		fact.EvidenceIDs = append([]string(nil), fact.EvidenceIDs...)
		if !fact.Complete {
			fact.State = ValidityFactUnknown
		}
		if fact.State == "" {
			fact.State = ValidityFactUnknown
		}
		out.objects[object] = fact
	}
	return out
}

func (s *evaluatorState) evaluate(
	statement StatementValidityAtOrdinal,
	engine string,
) (ValidityStatus, ValidityReason, []PreconditionViolation) {
	if statement.Ordinal <= 0 || statement.Facts.Operation == "" {
		return ValidityUnknown, ValidityReasonIncompleteStatement, nil
	}
	if !statement.Facts.Complete {
		return ValidityUnknown, ValidityReasonIncompleteStatement, nil
	}
	if hasUnsupportedEngineCondition(statement.Facts, engine) {
		return ValidityUnknown, ValidityReasonUnsupportedEngine, nil
	}

	before := make(map[fingerprint.ValidityObject]ValidityObjectFact, len(s.objects))
	for object, fact := range s.objects {
		before[object] = fact
	}

	uncertainObjects := map[fingerprint.ValidityObject]bool{}
	skippedEffects := map[fingerprint.ValidityObject]fingerprint.ValidityCondition{}
	var violations []PreconditionViolation
	for _, requirement := range statement.Facts.Requirements {
		if !validRequirement(requirement) {
			return ValidityUnknown, ValidityReasonIncompleteStatement, nil
		}

		fact, reason := s.get(before, requirement.Object)
		if fact.State == ValidityFactUnknown {
			if conditionAllowsUnknown(requirement) {
				uncertainObjects[requirement.Object] = true
				continue
			}
			if len(violations) > 0 {
				return ValidityInvalid, ValidityReasonComplete, violations
			}
			return ValidityUnknown, reason, nil
		}
		if fact.State == stateForRequirement(requirement.State) {
			continue
		}
		if conditionAllowsOpposite(requirement, fact.State) {
			skippedEffects[requirement.Object] = requirement.Condition
			continue
		}
		violationReason := ViolationRequiredPresentObservedAbsent
		if requirement.State == fingerprint.ValidityRequirementAbsent {
			violationReason = ViolationRequiredAbsentObservedPresent
		}
		violations = append(violations, PreconditionViolation{
			Ordinal: statement.Ordinal, Object: requirement.Object,
			Required: requirement.State, Observed: fact.State,
			Condition: requirement.Condition, Reason: violationReason,
			EvidenceIDs: append([]string(nil), fact.EvidenceIDs...),
		})
	}
	if len(violations) > 0 {
		return ValidityInvalid, ValidityReasonComplete, violations
	}

	if errReason := s.applyEffects(
		statement,
		before,
		uncertainObjects,
		skippedEffects,
	); errReason != "" {
		return ValidityUnknown, errReason, nil
	}
	return ValidityValid, ValidityReasonComplete, nil
}

func (s *evaluatorState) applyEffects(
	statement StatementValidityAtOrdinal,
	before map[fingerprint.ValidityObject]ValidityObjectFact,
	uncertainObjects map[fingerprint.ValidityObject]bool,
	skippedEffects map[fingerprint.ValidityObject]fingerprint.ValidityCondition,
) ValidityReason {
	noOpCreateParents := map[fingerprint.ValidityObject]bool{}
	unknownCreateParents := map[fingerprint.ValidityObject]bool{}
	for _, effect := range statement.Facts.Effects {
		if !validEffect(effect) {
			return ValidityReasonIncompleteStatement
		}
		if effectIsSkipped(effect, skippedEffects) {
			if effect.Object.Kind == fingerprint.ValidityObjectTable {
				noOpCreateParents[effect.Object] = true
			}
			continue
		}
		evidence := s.effectEvidence(before, effect)
		uncertain := uncertainObjects[effect.Object] ||
			(effect.Object.ParentToken != "" && uncertainObjects[parentObject(effect.Object)])
		if effect.Replacement != nil &&
			(uncertainObjects[*effect.Replacement] || uncertainObjects[parentObject(*effect.Replacement)]) {
			uncertain = true
		}

		switch effect.Action {
		case fingerprint.ValidityEffectCreate:
			if effect.Object.Kind == fingerprint.ValidityObjectTable {
				if effect.Condition == fingerprint.ValidityConditionIfNotExists {
					pre, _ := s.get(before, effect.Object)
					switch pre.State {
					case ValidityFactPresent:
						noOpCreateParents[effect.Object] = true
						continue
					case ValidityFactUnknown:
						s.set(
							effect.Object,
							ValidityFactPresent,
							appendUnique(pre.EvidenceIDs, evidence...),
						)
						s.tableDefinitions[effect.Object] = false
						unknownCreateParents[effect.Object] = true
						continue
					}
				}
				s.set(effect.Object, ValidityFactPresent, evidence)
				s.tableDefinitions[effect.Object] = statement.Facts.DefinitionsComplete
				if !statement.Facts.DefinitionsComplete {
					unknownCreateParents[effect.Object] = true
				}
				continue
			}
			if effect.Object.Kind == fingerprint.ValidityObjectIndex {
				s.set(effect.Object, ValidityFactPresent, evidence)
				continue
			}
			parent := parentObject(effect.Object)
			if noOpCreateParents[parent] {
				continue
			}
			if unknownCreateParents[parent] || uncertain {
				s.set(effect.Object, ValidityFactUnknown, evidence)
				continue
			}
			s.set(effect.Object, ValidityFactPresent, evidence)
		case fingerprint.ValidityEffectDrop:
			if effect.Object.Kind == fingerprint.ValidityObjectTable && uncertain {
				s.set(effect.Object, ValidityFactAbsent, evidence)
				s.dropTableChildren(effect.Object, evidence)
				delete(s.tableDefinitions, effect.Object)
				continue
			}
			s.set(effect.Object, ValidityFactAbsent, evidence)
			if effect.Object.Kind == fingerprint.ValidityObjectTable {
				s.dropTableChildren(effect.Object, evidence)
				delete(s.tableDefinitions, effect.Object)
			}
		case fingerprint.ValidityEffectRename:
			if effect.Replacement == nil {
				return ValidityReasonIncompleteStatement
			}
			if uncertain {
				s.set(effect.Object, ValidityFactUnknown, evidence)
				s.set(*effect.Replacement, ValidityFactUnknown, evidence)
				if effect.Object.Kind == fingerprint.ValidityObjectTable {
					delete(s.tableDefinitions, effect.Object)
					s.tableDefinitions[*effect.Replacement] = false
				}
				continue
			}
			s.rename(effect.Object, *effect.Replacement, evidence)
		default:
			return ValidityReasonIncompleteStatement
		}
	}
	return ""
}

func effectIsSkipped(
	effect fingerprint.ValidityEffect,
	skippedEffects map[fingerprint.ValidityObject]fingerprint.ValidityCondition,
) bool {
	condition, ok := skippedEffects[effect.Object]
	if ok && effect.Condition == condition {
		return true
	}
	if effect.Object.ParentToken == "" {
		return false
	}
	condition, ok = skippedEffects[parentObject(effect.Object)]
	return ok && effect.Condition == condition
}

func (s *evaluatorState) effectEvidence(
	before map[fingerprint.ValidityObject]ValidityObjectFact,
	effect fingerprint.ValidityEffect,
) []string {
	out := []string{}
	add := func(object fingerprint.ValidityObject) {
		if fact, ok := before[object]; ok {
			out = appendUnique(out, fact.EvidenceIDs...)
		}
		if fact, ok := s.objects[object]; ok {
			out = appendUnique(out, fact.EvidenceIDs...)
		}
	}
	add(effect.Object)
	if effect.Object.ParentToken != "" {
		add(parentObject(effect.Object))
	}
	if effect.Replacement == nil {
		return out
	}
	add(*effect.Replacement)
	if effect.Replacement.ParentToken != "" {
		add(parentObject(*effect.Replacement))
	}
	return out
}

func (s *evaluatorState) get(
	before map[fingerprint.ValidityObject]ValidityObjectFact,
	object fingerprint.ValidityObject,
) (ValidityObjectFact, ValidityReason) {
	if fact, ok := s.objects[object]; ok {
		if !fact.Complete || fact.State == ValidityFactUnknown || fact.State == "" {
			fact.State = ValidityFactUnknown
			return fact, ValidityReasonMissingFact
		}
		return fact, ""
	}
	if object.Kind == fingerprint.ValidityObjectColumn ||
		object.Kind == fingerprint.ValidityObjectIndex {
		parent := parentObject(object)
		if complete, ok := s.tableDefinitions[parent]; ok {
			if !complete {
				return ValidityObjectFact{State: ValidityFactUnknown}, ValidityReasonIncompleteDefinitions
			}
			return ValidityObjectFact{State: ValidityFactAbsent, Complete: true}, ""
		}
	}
	// Requirements in one statement are atomic; effects from that statement
	// are applied only after all requirements have been checked.
	if fact, ok := before[object]; ok {
		return fact, ""
	}
	return ValidityObjectFact{State: ValidityFactUnknown}, ValidityReasonMissingFact
}

func (s *evaluatorState) set(
	object fingerprint.ValidityObject,
	state ValidityFactState,
	evidence []string,
) {
	fact := ValidityObjectFact{
		State: state, Complete: state != ValidityFactUnknown,
		EvidenceIDs: append([]string(nil), evidence...),
	}
	if old, ok := s.objects[object]; ok && len(fact.EvidenceIDs) == 0 {
		fact.EvidenceIDs = append([]string(nil), old.EvidenceIDs...)
	}
	s.objects[object] = fact
}

func (s *evaluatorState) dropTableChildren(
	table fingerprint.ValidityObject,
	evidence []string,
) {
	for object, fact := range s.objects {
		if (object.Kind == fingerprint.ValidityObjectColumn ||
			object.Kind == fingerprint.ValidityObjectIndex) &&
			object.ParentToken == table.Token &&
			object.SchemaToken == table.SchemaToken {
			fact.State = ValidityFactAbsent
			fact.Complete = true
			fact.EvidenceIDs = appendUnique(fact.EvidenceIDs, evidence...)
			s.objects[object] = fact
		}
	}
}

func (s *evaluatorState) rename(
	oldObject, newObject fingerprint.ValidityObject,
	evidence []string,
) {
	oldFact, ok := s.objects[oldObject]
	if !ok {
		oldFact = ValidityObjectFact{State: ValidityFactPresent, Complete: true}
	}
	newFact, newKnown := s.objects[newObject]
	evidence = appendUnique(evidence, oldFact.EvidenceIDs...)
	if newKnown {
		evidence = appendUnique(evidence, newFact.EvidenceIDs...)
	}
	s.set(oldObject, ValidityFactAbsent, evidence)
	s.set(newObject, ValidityFactPresent, evidence)

	if oldObject.Kind != fingerprint.ValidityObjectTable {
		return
	}
	if complete, known := s.tableDefinitions[oldObject]; known {
		delete(s.tableDefinitions, oldObject)
		s.tableDefinitions[newObject] = complete
	}
	for object, fact := range s.objects {
		if (object.Kind != fingerprint.ValidityObjectColumn &&
			object.Kind != fingerprint.ValidityObjectIndex) ||
			object.ParentToken != oldObject.Token ||
			object.SchemaToken != oldObject.SchemaToken {
			continue
		}
		renamed := object
		renamed.ParentToken = newObject.Token
		renamed.SchemaToken = newObject.SchemaToken
		delete(s.objects, object)
		fact.EvidenceIDs = appendUnique(evidence, fact.EvidenceIDs...)
		s.objects[renamed] = fact
	}
}

func parentObject(object fingerprint.ValidityObject) fingerprint.ValidityObject {
	return fingerprint.ValidityObject{
		Kind: fingerprint.ValidityObjectTable, Token: object.ParentToken,
		SchemaToken: object.SchemaToken,
	}
}

func stateForRequirement(state fingerprint.ValidityRequirementState) ValidityFactState {
	if state == fingerprint.ValidityRequirementAbsent {
		return ValidityFactAbsent
	}
	return ValidityFactPresent
}

func conditionAllowsUnknown(requirement fingerprint.ValidityRequirement) bool {
	return (requirement.State == fingerprint.ValidityRequirementPresent &&
		requirement.Condition == fingerprint.ValidityConditionIfExists) ||
		(requirement.State == fingerprint.ValidityRequirementAbsent &&
			requirement.Condition == fingerprint.ValidityConditionIfNotExists)
}

func conditionAllowsOpposite(
	requirement fingerprint.ValidityRequirement,
	observed ValidityFactState,
) bool {
	if requirement.State == fingerprint.ValidityRequirementPresent &&
		requirement.Condition == fingerprint.ValidityConditionIfExists &&
		observed == ValidityFactAbsent {
		return true
	}
	return requirement.State == fingerprint.ValidityRequirementAbsent &&
		requirement.Condition == fingerprint.ValidityConditionIfNotExists &&
		observed == ValidityFactPresent
}

func validRequirement(requirement fingerprint.ValidityRequirement) bool {
	if requirement.Object.Token == "" {
		return false
	}
	switch requirement.Object.Kind {
	case fingerprint.ValidityObjectTable,
		fingerprint.ValidityObjectColumn,
		fingerprint.ValidityObjectIndex:
	default:
		return false
	}
	return (requirement.State == fingerprint.ValidityRequirementPresent ||
		requirement.State == fingerprint.ValidityRequirementAbsent) &&
		validValidityCondition(requirement.Condition)
}

func validEffect(effect fingerprint.ValidityEffect) bool {
	if effect.Object.Token == "" {
		return false
	}
	switch effect.Object.Kind {
	case fingerprint.ValidityObjectTable,
		fingerprint.ValidityObjectColumn,
		fingerprint.ValidityObjectIndex:
	default:
		return false
	}
	if !validValidityCondition(effect.Condition) {
		return false
	}
	switch effect.Action {
	case fingerprint.ValidityEffectCreate, fingerprint.ValidityEffectDrop:
		return effect.Replacement == nil
	case fingerprint.ValidityEffectRename:
		return effect.Replacement != nil && effect.Replacement.Token != ""
	default:
		return false
	}
}

func validValidityCondition(condition fingerprint.ValidityCondition) bool {
	switch condition {
	case fingerprint.ValidityConditionNone,
		fingerprint.ValidityConditionIfExists,
		fingerprint.ValidityConditionIfNotExists:
		return true
	default:
		return false
	}
}

func hasUnsupportedEngineCondition(
	facts fingerprint.StatementValidity,
	engine string,
) bool {
	for _, requirement := range facts.Requirements {
		if !fingerprint.ValidityConditionSupported(
			engine,
			facts.Operation,
			requirement.Object.Kind,
			requirement.Condition,
		) {
			return true
		}
	}
	return false
}

func appendUnique(dst []string, values ...string) []string {
	seen := make(map[string]bool, len(dst)+len(values))
	for _, value := range dst {
		seen[value] = true
	}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		dst = append(dst, value)
		seen[value] = true
	}
	return dst
}
