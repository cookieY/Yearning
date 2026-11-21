// statement_analysis.go Shared pure-compute SQL statement splitting, fingerprinting,
// and object-reference aggregation helpers used by grouping and ephemeral review runs.
package application

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/changeset"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

const (
	maxStatementOrdinalRanges      = 512
	maxObjectOrdinalRanges         = 512
	maxObjectReferencesPerGroup    = 512
	maxObjectOrdinalRangesPerGroup = 4096
	maxObjectReferencesPerRun      = 4096
	maxObjectOrdinalRangesPerRun   = 16384
)

// statementOrdinalRange represents a contiguous range of statement ordinals for a fingerprint group within the SQL batch.
// JSON bounds are closed intervals [start, end].
type statementOrdinalRange struct {
	Start int32 `json:"start"`
	End   int32 `json:"end"`
}

// objectsAtStatementOrdinal copies single-statement object references and populates the original
// statement ordinal from the grouping phase. The fingerprint layer always outputs 0; must not mutate the shared Result slice in place.
func objectsAtStatementOrdinal(
	refs []fingerprint.ObjectReference,
	ordinal int32,
) []fingerprint.ObjectReference {
	out := make([]fingerprint.ObjectReference, 0, len(refs))
	for _, ref := range refs {
		ref.StatementOrdinal = ordinal
		ref.StatementOrdinalRanges = []fingerprint.StatementOrdinalRange{{
			Start: ordinal,
			End:   ordinal,
		}}
		out = append(out, ref)
	}
	return out
}

// mergeMetadataContextComplete takes the logical AND of metadata context completeness across the group:
// when any statement can only be proven via compatible_lexer fallback, the entire group must not be treated
// by the model as having complete object context to skip table-existence checks.
func mergeMetadataContextComplete(current, next bool) bool {
	return current && next
}

// appendStatementOrdinalRange compresses monotonically increasing statement ordinals within a group into closed intervals.
// Adjacent ordinals merge; interleaved ordinals remain separate intervals. When exceeding the cap, retains
// the first 512 intervals and returns false; the caller must mark the group as metadata context incomplete
// rather than treating truncated intervals as complete object context.
func appendStatementOrdinalRange(
	ranges []statementOrdinalRange,
	ordinal int32,
) ([]statementOrdinalRange, bool) {
	if ordinal < 1 {
		return ranges, false
	}
	if len(ranges) > 0 {
		last := &ranges[len(ranges)-1]
		if ordinal < last.Start {
			return ranges, false
		}
		if ordinal <= last.End {
			return ranges, true
		}
		if ordinal == last.End+1 {
			last.End = ordinal
			return ranges, true
		}
	}
	if len(ranges) >= maxStatementOrdinalRanges {
		return ranges, false
	}
	return append(ranges, statementOrdinalRange{Start: ordinal, End: ordinal}), true
}

// objectContextBudget bounds the complete Provider object context for a Run.
// Counts are shared across fingerprint groups by groupingPhase.
type objectContextBudget struct {
	objects int
	ranges  int
}

// objectMergeState is the per-group index and range-membership count.
type objectMergeState struct {
	index  map[string]int
	ranges int
}

func newObjectMergeState() *objectMergeState {
	return &objectMergeState{index: map[string]int{}}
}

// mergeObjects merges object references by semantic identity while retaining
// every occurrence as an inclusive statement range. The first ordinal remains
// for compatibility. Any per-object, per-group, or per-Run overflow returns
// false so the group's metadata context becomes incomplete while the accepted
// prefix stays bounded and available for review.
func mergeObjects(
	dst []fingerprint.ObjectReference,
	state *objectMergeState,
	refs []fingerprint.ObjectReference,
	budget *objectContextBudget,
) ([]fingerprint.ObjectReference, bool) {
	if state == nil {
		state = newObjectMergeState()
	}
	if state.index == nil {
		state.index = map[string]int{}
	}
	if budget == nil {
		budget = &objectContextBudget{}
	}
	if dst == nil {
		dst = []fingerprint.ObjectReference{}
	}
	complete := true
	for _, ref := range refs {
		ranges, ok := normalizedObjectRanges(ref)
		if !ok {
			complete = false
			continue
		}
		ref.StatementOrdinalRanges = ranges
		if ref.StatementOrdinal < 1 {
			ref.StatementOrdinal = ranges[0].Start
		}
		key := objectReferenceKey(ref)
		if index, exists := state.index[key]; exists {
			existing := dst[index]
			merged, added, ok := mergeObjectOrdinalRanges(
				existing.StatementOrdinalRanges, ranges, maxObjectOrdinalRanges)
			if !ok ||
				state.ranges+added > maxObjectOrdinalRangesPerGroup ||
				budget.ranges+added > maxObjectOrdinalRangesPerRun {
				complete = false
				continue
			}
			existing.StatementOrdinalRanges = merged
			dst[index] = existing
			state.ranges += added
			budget.ranges += added
			continue
		}
		added := len(ranges)
		if len(dst) >= maxObjectReferencesPerGroup ||
			budget.objects >= maxObjectReferencesPerRun ||
			state.ranges+added > maxObjectOrdinalRangesPerGroup ||
			budget.ranges+added > maxObjectOrdinalRangesPerRun {
			complete = false
			continue
		}
		state.index[key] = len(dst)
		dst = append(dst, ref)
		state.ranges += added
		budget.objects++
		budget.ranges += added
	}
	return dst, complete
}

func normalizedObjectRanges(
	ref fingerprint.ObjectReference,
) ([]fingerprint.StatementOrdinalRange, bool) {
	if len(ref.StatementOrdinalRanges) == 0 {
		if ref.StatementOrdinal < 1 {
			return nil, false
		}
		return []fingerprint.StatementOrdinalRange{{
			Start: ref.StatementOrdinal,
			End:   ref.StatementOrdinal,
		}}, true
	}
	out := make([]fingerprint.StatementOrdinalRange, 0, len(ref.StatementOrdinalRanges))
	for _, next := range ref.StatementOrdinalRanges {
		var ok bool
		out, ok = appendObjectOrdinalRange(out, next, maxObjectOrdinalRanges)
		if !ok {
			return nil, false
		}
	}
	return out, true
}

func mergeObjectOrdinalRanges(
	existing []fingerprint.StatementOrdinalRange,
	incoming []fingerprint.StatementOrdinalRange,
	limit int,
) ([]fingerprint.StatementOrdinalRange, int, bool) {
	out := append([]fingerprint.StatementOrdinalRange{}, existing...)
	added := 0
	for _, next := range incoming {
		before := len(out)
		var ok bool
		out, ok = appendObjectOrdinalRange(out, next, limit)
		if !ok {
			return append([]fingerprint.StatementOrdinalRange{}, existing...), 0, false
		}
		added += len(out) - before
	}
	return out, added, true
}

func appendObjectOrdinalRange(
	ranges []fingerprint.StatementOrdinalRange,
	next fingerprint.StatementOrdinalRange,
	limit int,
) ([]fingerprint.StatementOrdinalRange, bool) {
	if next.Start < 1 || next.End < next.Start {
		return ranges, false
	}
	if len(ranges) > 0 {
		last := &ranges[len(ranges)-1]
		if next.Start < last.Start {
			return ranges, false
		}
		if next.Start <= last.End {
			if next.End > last.End {
				last.End = next.End
			}
			return ranges, true
		}
		if next.Start == last.End+1 {
			last.End = next.End
			return ranges, true
		}
	}
	if len(ranges) >= limit {
		return ranges, false
	}
	return append(ranges, next), true
}

func objectReferenceKey(ref fingerprint.ObjectReference) string {
	return ref.Token + "\x00" + ref.Kind + "\x00" + string(ref.Relation) +
		"\x00" + ref.SchemaToken + "\x00" + ref.ParentToken
}

// verifyHMAC verifies plaintext HMAC (case-insensitive hex).
func verifyHMAC(c *crypto.Crypter, plaintext []byte, expectedHex string) bool {
	mac, err := c.HMAC(sqlHMACPurpose, plaintext)
	if err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(mac), expectedHex)
}

func terminalStageOf(run domain.RunState) string {
	if run == domain.RunStatePartial {
		return string(domain.StageStatePartial)
	}
	return string(domain.StageStateFailed)
}

func appendIfMissing(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// operationOf extracts the first SQL word from statement text as the operation name (uppercase).
// Deterministic operation summary for FakeRunner; replaced by structured analysis in the B7 real engine.
func operationOf(sql []byte) string {
	i := 0
	n := len(sql)
	for i < n {
		b := sql[i]
		switch {
		case b == ' ' || b == '\t' || b == '\r' || b == '\n':
			i++
			continue
		case b == '(':
			i++ // Nested leading parentheses
			continue
		case b == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
			continue
		case b == '/' && i+1 < n && sql[i+1] == '*':
			i += 2
			for i+1 < n && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			i += 2
			continue
		default:
			start := i
			for i < n && isOpWordByte(sql[i]) {
				i++
			}
			if i == start {
				return strings.ToUpper(string(sql[start : start+1]))
			}
			return strings.ToUpper(string(sql[start:i]))
		}
	}
	return "UNKNOWN"
}

// statementClassOf classifies statement head -> DDL/DML (schema CHECK ddl|dml;
// read statements are classified under dml to participate in data access review).
func statementClassOf(op string) string {
	switch op {
	case "create", "alter", "drop", "truncate":
		return "ddl"
	default:
		return "dml"
	}
}

// hasWhereClause detects whether an UPDATE/DELETE carries a WHERE clause (word-boundary match,
// ignoring occurrences inside strings or comments; structural heuristic material for fake runner).
func hasWhereClause(sql []byte) bool {
	i, n := 0, len(sql)
	for i < n {
		b := sql[i]
		switch {
		case b == '\'':
			i++
			for i < n && sql[i] != '\'' {
				if sql[i] == '\\' {
					i++
				}
				i++
			}
			i++
		case b == '"' || b == '`':
			q := b
			i++
			for i < n && sql[i] != q {
				i++
			}
			i++
		case b == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
		case b == '/' && i+1 < n && sql[i+1] == '*':
			i += 2
			for i+1 < n && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			i += 2
		case isOpWordByte(b):
			start := i
			for i < n && isOpWordByte(sql[i]) {
				i++
			}
			if strings.EqualFold(string(sql[start:i]), "where") {
				return true
			}
		default:
			i++
		}
	}
	return false
}

func isOpWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// failureNameOf maps an error to its failure name.
func failureNameOf(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Name
	}
	return "INTERNAL"
}

func cryptoEnvelopeOf(p persistence.DraftSQLPayloadModel) crypto.Envelope {
	return crypto.Envelope{
		Ciphertext: p.SQLCiphertext, Nonce: p.SQLNonce,
		KeyID: p.SQLKeyID, Algorithm: p.SQLAlgorithm,
	}
}

// ---- Pure-compute extraction for the grouping phase (RCP-20261101-EPHEMERAL-PRECHECK) ----
// The DB path (groupingPhase) and ephemeral Runs (ephemeral_agent.go) share the exact same
// single-pass streaming analysis; this function never touches the database and returns business errors
// directly for the caller to decide how to converge (DB path = failDraftNoRun; ephemeral = in-memory terminal state).

type analyzedStatement struct {
	res        fingerprint.Result
	operation  string
	class      string // ddl|dml (sql_statements.statement_kind)
	noWhere    bool   // UPDATE/DELETE without WHERE (fake runner structural heuristic)
	objects    []fingerprint.ObjectReference
	pathUnsafe bool
	callables  []postgresCallableRef
	callableOK bool
}

type aggregatedGroup struct {
	canon, dialect              string
	representativeAnonymizedSQL string
	representativeOrdinal       int32
	repKind                     string
	repOperation                string
	repSQL                      string
	sizeMax                     int64
	objects                     []fingerprint.ObjectReference
	objectState                 *objectMergeState
	statementOrdinalRanges      []statementOrdinalRange
	metadataContextComplete     bool
	count                       int
	inMax, valMax               int
	unbounded                   bool      // Group contains an UPDATE/DELETE without WHERE
	facts                       stmtFacts // Statement structural facts within the group (no plaintext identifiers)
}

func buildSQLStatementModel(
	runID, stmtID string,
	ordinal int32,
	res fingerprint.Result,
	op, class string,
	objects []fingerprint.ObjectReference,
) (persistence.SQLStatementModel, error) {
	typed, merr := jsonMarshal(map[string]any{
		"kind":                      res.StatementKind,
		"operation":                 op,
		"dialect_profile":           res.DialectProfile,
		"quality":                   res.Quality,
		"tuple_count":               res.TupleCount,
		"parameter_count":           res.ParameterCount,
		"anonymized_sql":            res.AnonymizedSQL,
		"objects":                   objects,
		"statement_ordinal":         ordinal,
		"metadata_context_complete": res.MetadataContextComplete,
		"execution_validity":        res.Validity,
	})
	if merr != nil {
		return persistence.SQLStatementModel{}, merr
	}
	lit, merr := jsonMarshal(res.LiteralTypeCounts)
	if merr != nil {
		return persistence.SQLStatementModel{}, merr
	}
	return persistence.SQLStatementModel{
		ID:                       stmtID,
		ReviewRunID:              runID,
		Ordinal:                  ordinal,
		StatementKind:            class,
		Operation:                op,
		SizeBytes:                int32(res.ByteLen),
		AlgorithmVersion:         res.AlgorithmVersion,
		DialectProfile:           res.DialectProfile,
		FingerprintQuality:       res.Quality,
		FingerprintKeyID:         res.FingerprintKeyID,
		CanonicalFingerprintHMAC: res.CanonicalFingerprintHMAC,
		DialectFingerprintHMAC:   res.DialectFingerprintHMAC,
		ExecutionShapeHMAC:       res.ExecutionShapeHMAC,
		TypedShape:               typed,
		LiteralTypeCounts:        lit,
		TupleCount:               int32(res.TupleCount),
		ParameterCount:           int32(res.ParameterCount),
	}, nil
}

type streamingAnalysisOptions struct {
	runID           string
	rawSQL          []byte
	profile         fingerprint.Profile
	mapper          *privacy.RunMapper
	engine          *fingerprint.Engine
	expectedCount   int32
	validateSyntax  bool
	inlineLimit     int
	collectPostgres bool
	beat            func(completed, total int64)
}

type streamedRunAnalysis struct {
	statementCount     int32
	groups             []aggregatedGroup
	inlineStatements   []persistence.SQLStatementModel
	repOrdinalToStmtID map[int32]string
	validityTable      []fingerprint.StatementValidity
	validityRefs       []compactStmtValidityRef
	postgresStatements []postgresAnalyzedStatement
}

func analyzeRunStatementsStreaming(opts streamingAnalysisOptions) (*streamedRunAnalysis, error) {
	if opts.engine == nil {
		return nil, &domain.Error{Code: 2011, Name: "AI_PROVIDER_FAILED"}
	}
	split := changeset.NewSplitter(bytes.NewReader(opts.rawSQL), opts.profile, 0)
	var validator *statementSyntaxValidator
	if opts.validateSyntax {
		validator = newStatementSyntaxValidator(opts.rawSQL, opts.profile)
	}
	groupIndex := map[string]int{}
	var aggs []aggregatedGroup
	objectBudget := &objectContextBudget{}
	validityIndex := map[string]uint32{}
	var validityTable []fingerprint.StatementValidity
	var validityRefs []compactStmtValidityRef
	var inlineStatements []persistence.SQLStatementModel
	if opts.inlineLimit > 0 {
		capInit := opts.inlineLimit
		if opts.expectedCount > 0 && int(opts.expectedCount) < capInit {
			capInit = int(opts.expectedCount)
		}
		inlineStatements = make([]persistence.SQLStatementModel, 0, capInit)
	}
	repOrdinalToStmtID := make(map[int32]string)
	var postgresStatements []postgresAnalyzedStatement
	if opts.collectPostgres && opts.expectedCount > 0 {
		postgresStatements = make([]postgresAnalyzedStatement, 0, opts.expectedCount)
	}

	ordinal := 0
	for {
		st, done, ferr := split.Next()
		if ferr != nil {
			base := domain.FailureFromFingerprintCode(fingerprintFailureName(ferr))
			if opts.validateSyntax {
				if detail := formatSplitterFailureDetail(opts.rawSQL, opts.profile, ferr); detail != "" {
					return nil, &domain.Error{Code: base.Code, Name: base.Name, Detail: detail}
				}
			}
			return nil, base
		}
		if done {
			break
		}
		ordinal++
		if ordinal > 100000 {
			return nil, domain.ErrBatchTooLarge
		}
		if validator != nil {
			if err := validator.ValidateStatement(ordinal, st); err != nil {
				return nil, err
			}
		}
		res, aerr := opts.engine.Analyze(opts.profile, st.SQL, opts.mapper)
		if aerr != nil {
			return nil, domain.FailureFromFingerprintCode(fingerprintFailureName(aerr))
		}
		op := strings.ToLower(operationOf(st.SQL))
		class := statementClassOf(op)
		noWhere := (op == "update" || op == "delete") && !hasWhereClause(st.SQL)
		statementObjects := objectsAtStatementOrdinal(res.Objects, int32(ordinal))

		key := res.CanonicalFingerprintHMAC + "\x00" + res.DialectFingerprintHMAC
		idx, ok := groupIndex[key]
		if !ok {
			if len(aggs) >= 1000 {
				return nil, domain.ErrTooManyFingerprints
			}
			idx = len(aggs)
			groupIndex[key] = idx
			repStmtID := newID()
			repOrdinalToStmtID[int32(ordinal)] = repStmtID
			aggs = append(aggs, aggregatedGroup{
				canon:                       res.CanonicalFingerprintHMAC,
				dialect:                     res.DialectFingerprintHMAC,
				representativeAnonymizedSQL: res.AnonymizedSQL,
				representativeOrdinal:       int32(ordinal),
				repKind:                     class,
				repOperation:                op,
				repSQL:                      string(st.SQL),
				objects:                     []fingerprint.ObjectReference{},
				objectState:                 newObjectMergeState(),
				statementOrdinalRanges:      []statementOrdinalRange{},
				metadataContextComplete:     true,
			})
		}
		aggs[idx].count++
		if int64(res.ByteLen) > aggs[idx].sizeMax {
			aggs[idx].sizeMax = int64(res.ByteLen)
		}
		var ordinalRangeComplete bool
		aggs[idx].statementOrdinalRanges, ordinalRangeComplete = appendStatementOrdinalRange(
			aggs[idx].statementOrdinalRanges,
			int32(ordinal),
		)
		var objectContextComplete bool
		aggs[idx].objects, objectContextComplete = mergeObjects(
			aggs[idx].objects,
			aggs[idx].objectState,
			statementObjects,
			objectBudget,
		)
		aggs[idx].metadataContextComplete = mergeMetadataContextComplete(
			aggs[idx].metadataContextComplete,
			res.MetadataContextComplete,
		) && ordinalRangeComplete && objectContextComplete
		if noWhere {
			aggs[idx].unbounded = true
		}
		for _, n := range res.InListSizes {
			if n > aggs[idx].inMax {
				aggs[idx].inMax = n
			}
		}
		if res.TupleCount > aggs[idx].valMax {
			aggs[idx].valMax = res.TupleCount
		}
		mergeFactsInto(&aggs[idx].facts, extractStmtFacts(st.SQL, op, class))

		valKeyBytes, verr := json.Marshal(res.Validity)
		if verr != nil {
			return nil, verr
		}
		valKey := string(valKeyBytes)
		vIdx, exists := validityIndex[valKey]
		if !exists {
			vIdx = uint32(len(validityTable))
			validityIndex[valKey] = vIdx
			validityTable = append(validityTable, res.Validity)
		}
		validityRefs = append(validityRefs, compactStmtValidityRef{
			validityIdx: vIdx,
			groupIdx:    uint16(idx),
		})

		if inlineStatements != nil {
			if ordinal <= opts.inlineLimit {
				stmtID := repOrdinalToStmtID[int32(ordinal)]
				if stmtID == "" {
					stmtID = newID()
				}
				rowModel, berr := buildSQLStatementModel(opts.runID, stmtID, int32(ordinal), res, op, class, statementObjects)
				if berr != nil {
					return nil, berr
				}
				inlineStatements = append(inlineStatements, rowModel)
			} else {
				// Crossing the small-batch threshold drops inline statement rows; submit rebuilds them streaming.
				inlineStatements = nil
			}
		}

		if opts.collectPostgres {
			rawStmt := string(st.SQL)
			callables, callablesOK := postgresCallableRefs(rawStmt)
			postgresStatements = append(postgresStatements, postgresAnalyzedStatement{
				result:      fingerprint.Result{Validity: res.Validity},
				objects:     statementObjects,
				pathUnsafe:  postgresStatementPathUnsafe(rawStmt),
				callables:   callables,
				callablesOK: callablesOK,
			})
		}

		if opts.beat != nil && ordinal%5000 == 0 {
			opts.beat(int64(ordinal), int64(opts.expectedCount))
		}
	}
	if ordinal == 0 {
		if opts.validateSyntax || opts.expectedCount == 0 {
			return nil, domain.ErrBatchTooLarge
		}
		return nil, &domain.Error{Code: domain.CodeSQLParseFailed, Name: "SQL_PARSE_FAILED"}
	}
	if opts.expectedCount > 0 && int32(ordinal) != opts.expectedCount {
		return nil, &domain.Error{Code: domain.CodeSQLParseFailed, Name: "SQL_PARSE_FAILED"}
	}
	if len(aggs) > 1000 {
		return nil, domain.ErrTooManyFingerprints
	}
	return &streamedRunAnalysis{
		statementCount:     int32(ordinal),
		groups:             aggs,
		inlineStatements:   inlineStatements,
		repOrdinalToStmtID: repOrdinalToStmtID,
		validityTable:      validityTable,
		validityRefs:       validityRefs,
		postgresStatements: postgresStatements,
	}, nil
}

func analyzeRunStatements(split *changeset.Splitter, profile fingerprint.Profile,
	mapper *privacy.RunMapper, engine *fingerprint.Engine, expectedCount int32,
	beat func(completed, total int64),
) ([]analyzedStatement, []aggregatedGroup, error) {
	var stmts []analyzedStatement
	groupIndex := map[string]int{}
	var aggs []aggregatedGroup
	objectBudget := &objectContextBudget{}
	ordinal := 0
	for {
		st, done, ferr := split.Next()
		if ferr != nil {
			return nil, nil, domain.FailureFromFingerprintCode(fingerprintFailureName(ferr))
		}
		if done {
			break
		}
		ordinal++
		res, aerr := engine.Analyze(profile, st.SQL, mapper)
		if aerr != nil {
			return nil, nil, domain.FailureFromFingerprintCode(fingerprintFailureName(aerr))
		}
		op := strings.ToLower(operationOf(st.SQL))
		statementObjects := objectsAtStatementOrdinal(res.Objects, int32(ordinal))
		callables, callablesOK := postgresCallableRefs(string(st.SQL))
		stmts = append(stmts, analyzedStatement{
			res: res, operation: op, class: statementClassOf(op),
			noWhere: (op == "update" || op == "delete") && !hasWhereClause(st.SQL),
			objects: statementObjects, pathUnsafe: postgresStatementPathUnsafe(string(st.SQL)),
			callables: callables, callableOK: callablesOK,
		})
		key := res.CanonicalFingerprintHMAC + "\x00" + res.DialectFingerprintHMAC
		idx, ok := groupIndex[key]
		if !ok {
			idx = len(aggs)
			groupIndex[key] = idx
			aggs = append(aggs, aggregatedGroup{
				canon: res.CanonicalFingerprintHMAC, dialect: res.DialectFingerprintHMAC,
				representativeAnonymizedSQL: res.AnonymizedSQL,
				representativeOrdinal:       int32(ordinal),
				objects:                     []fingerprint.ObjectReference{},
				objectState:                 newObjectMergeState(),
				statementOrdinalRanges:      []statementOrdinalRange{},
				metadataContextComplete:     true,
			})
		}
		aggs[idx].count++
		var ordinalRangeComplete bool
		aggs[idx].statementOrdinalRanges, ordinalRangeComplete = appendStatementOrdinalRange(
			aggs[idx].statementOrdinalRanges,
			int32(ordinal),
		)
		var objectContextComplete bool
		aggs[idx].objects, objectContextComplete = mergeObjects(
			aggs[idx].objects,
			aggs[idx].objectState,
			statementObjects,
			objectBudget,
		)
		aggs[idx].metadataContextComplete = mergeMetadataContextComplete(
			aggs[idx].metadataContextComplete,
			res.MetadataContextComplete,
		) && ordinalRangeComplete && objectContextComplete
		if stmts[len(stmts)-1].noWhere {
			aggs[idx].unbounded = true
		}
		for _, n := range res.InListSizes {
			if n > aggs[idx].inMax {
				aggs[idx].inMax = n
			}
		}
		if res.TupleCount > aggs[idx].valMax {
			aggs[idx].valMax = res.TupleCount
		}
		mergeFactsInto(&aggs[idx].facts, extractStmtFacts(st.SQL, op, stmts[len(stmts)-1].class))
		if beat != nil && ordinal%5000 == 0 {
			beat(int64(ordinal), int64(expectedCount))
		}
	}
	if ordinal == 0 || int32(ordinal) != expectedCount {
		return nil, nil, &domain.Error{Code: domain.CodeSQLParseFailed, Name: "SQL_PARSE_FAILED"}
	}
	if len(aggs) > 1000 {
		return nil, nil, domain.ErrTooManyFingerprints
	}
	return stmts, aggs, nil
}
