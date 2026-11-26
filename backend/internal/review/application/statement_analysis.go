// statement_analysis.go Shared pure-compute SQL statement splitting, fingerprinting,
// and object-reference aggregation helpers used by grouping and ephemeral review runs.
package application

import (
	"bytes"
	"encoding/hex"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/changeset"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"strings"
)

// appendStatementOrdinalRange compresses monotonically increasing statement ordinals within a group into closed intervals.
// Adjacent ordinals merge; interleaved ordinals remain separate intervals. When exceeding the cap, retains
// the first 512 intervals and returns false; the caller must mark the group as metadata context incomplete
// rather than treating truncated intervals as complete object context.
func appendStatementOrdinalRange(
	ranges []statementOrdinalRange,
	ordinal int32,
) ([]statementOrdinalRange, bool) {
	out, valid, truncated := appendStatementOrdinalRangeTracked(ranges, ordinal)
	return out, valid && !truncated
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

// mergeObjectsTracked merges object references by semantic identity while decoupling
// statement range display caps (512/4096/16384) from metadata completeness. It returns
// objectsComplete=false only when a range is malformed/out-of-order or when a distinct
// object identity cannot be recorded due to maxObjectReferencesPerGroup or maxObjectReferencesPerRun.
func mergeObjectsTracked(
	dst []fingerprint.ObjectReference,
	state *objectMergeState,
	refs []fingerprint.ObjectReference,
	budget *objectContextBudget,
) ([]fingerprint.ObjectReference, bool, bool) {
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
	objectsComplete := true
	rangesTruncated := false
	for _, ref := range refs {
		ranges, valid, normTruncated, owned := normalizedObjectRangesTracked(ref)
		if !valid {
			objectsComplete = false
			continue
		}
		if ref.StatementOrdinal < 1 {
			ref.StatementOrdinal = ranges[0].Start
		}
		key := objectReferenceKey(ref)
		if index, exists := state.index[key]; exists {
			if normTruncated {
				rangesTruncated = true
			}
			existing := dst[index]
			if len(ranges) == 1 {
				next := ranges[0]
				if len(existing.StatementOrdinalRanges) > 0 {
					last := &existing.StatementOrdinalRanges[len(existing.StatementOrdinalRanges)-1]
					if next.Start < last.Start {
						objectsComplete = false
						continue
					}
					if next.Start <= last.End {
						if next.End > last.End {
							last.End = next.End
						}
						continue
					}
					if next.Start == last.End+1 {
						last.End = next.End
						continue
					}
				}
				if len(existing.StatementOrdinalRanges) < maxObjectOrdinalRanges &&
					state.ranges < maxObjectOrdinalRangesPerGroup &&
					budget.ranges < maxObjectOrdinalRangesPerRun {
					existing.StatementOrdinalRanges = append(existing.StatementOrdinalRanges, next)
					dst[index] = existing
					state.ranges++
					budget.ranges++
				} else {
					rangesTruncated = true
				}
				continue
			}
			maxAdd := maxObjectOrdinalRanges - len(existing.StatementOrdinalRanges)
			if rem := maxObjectOrdinalRangesPerGroup - state.ranges; rem < maxAdd {
				maxAdd = rem
			}
			if rem := maxObjectOrdinalRangesPerRun - budget.ranges; rem < maxAdd {
				maxAdd = rem
			}
			if maxAdd < 0 {
				maxAdd = 0
			}
			merged, added, validMerge, mergeTruncated := mergeObjectOrdinalRangesTracked(
				existing.StatementOrdinalRanges,
				ranges,
				len(existing.StatementOrdinalRanges)+maxAdd,
			)
			if !validMerge {
				objectsComplete = false
				continue
			}
			if mergeTruncated {
				rangesTruncated = true
			}
			existing.StatementOrdinalRanges = merged
			dst[index] = existing
			state.ranges += added
			budget.ranges += added
			continue
		}
		if len(dst) >= maxObjectReferencesPerGroup || budget.objects >= maxObjectReferencesPerRun {
			objectsComplete = false
			continue
		}
		if normTruncated {
			rangesTruncated = true
		}
		avail := maxObjectOrdinalRanges
		if rem := maxObjectOrdinalRangesPerGroup - state.ranges; rem < avail {
			avail = rem
		}
		if rem := maxObjectOrdinalRangesPerRun - budget.ranges; rem < avail {
			avail = rem
		}
		if avail < 0 {
			avail = 0
		}
		if len(ranges) > avail {
			if avail == 0 {
				ranges = []fingerprint.StatementOrdinalRange{}
			} else if owned {
				ranges = ranges[:avail]
			} else {
				copied := make([]fingerprint.StatementOrdinalRange, avail)
				copy(copied, ranges[:avail])
				ranges = copied
			}
			owned = true
			rangesTruncated = true
		} else if !owned {
			copied := make([]fingerprint.StatementOrdinalRange, len(ranges))
			copy(copied, ranges)
			ranges = copied
			owned = true
		}
		ref.StatementOrdinalRanges = ranges
		added := len(ranges)
		state.index[key] = len(dst)
		dst = append(dst, ref)
		state.ranges += added
		budget.objects++
		budget.ranges += added
	}
	return dst, objectsComplete, rangesTruncated
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

// verifyHMAC verifies plaintext HMAC (case-insensitive hex).
func verifyHMAC(c *crypto.Crypter, plaintext []byte, expectedHex string) bool {
	mac, err := c.HMAC(sqlHMACPurpose, plaintext)
	if err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(mac), expectedHex)
}

// operationOf extracts the first SQL word from statement text as the operation name (uppercase).
func operationOf(sql []byte) string {
	raw := fingerprint.FirstOperationSlice(sql)
	if len(raw) == 0 {
		return "UNKNOWN"
	}
	return strings.ToUpper(string(raw))
}

// hasWhereClause detects whether an UPDATE/DELETE carries a WHERE clause (word-boundary match,
// ignoring occurrences inside strings or comments).
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
		case fingerprint.IsOperationWordByte(b):
			start := i
			for i < n && fingerprint.IsOperationWordByte(sql[i]) {
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

func analyzeRunStatementsStreaming(opts streamingAnalysisOptions) (*streamedRunAnalysis, error) {
	if opts.engine == nil {
		return nil, &domain.Error{Code: 2011, Name: "AI_PROVIDER_FAILED"}
	}
	profileFlags := opts.profile.Flags()
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
	valKeyBuf := make([]byte, 0, 256)
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
		lex, lexErr := fingerprint.Tokenize(st.SQL, profileFlags)
		if lexErr != nil {
			if validator != nil {
				if err := validator.ValidateStatement(ordinal, st); err != nil {
					return nil, err
				}
			}
			return nil, domain.FailureFromFingerprintCode(fingerprint.ErrNameParseFailed)
		}
		if validator != nil {
			if err := validator.ValidateStatementWithLex(ordinal, st, &lex); err != nil {
				return nil, err
			}
		}
		res, aerr := opts.engine.AnalyzeLexed(opts.profile, st.SQL, lex, opts.mapper)
		if aerr != nil {
			return nil, domain.FailureFromFingerprintCode(fingerprintFailureName(aerr))
		}
		op := operationLower(st.SQL)
		class := fingerprint.StatementClassOf(op)
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
		var ordinalValid bool
		aggs[idx].statementOrdinalRanges, ordinalValid, _ = appendStatementOrdinalRangeTracked(
			aggs[idx].statementOrdinalRanges,
			int32(ordinal),
		)
		var objectsComplete, objRangesTruncated bool
		aggs[idx].objects, objectsComplete, objRangesTruncated = mergeObjectsTracked(
			aggs[idx].objects,
			aggs[idx].objectState,
			statementObjects,
			objectBudget,
		)
		if objRangesTruncated {
			aggs[idx].objectRangesTruncated = true
		}
		aggs[idx].metadataContextComplete = mergeMetadataContextComplete(
			aggs[idx].metadataContextComplete,
			res.MetadataContextComplete,
		) && ordinalValid && objectsComplete
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
		mergeStmtFactsInto(&aggs[idx].facts, st.SQL, op, class)

		valKeyBuf = appendValidityKey(valKeyBuf[:0], res.Validity)
		vIdx, exists := validityIndex[string(valKeyBuf)]
		if !exists {
			vIdx = uint32(len(validityTable))
			validityIndex[string(valKeyBuf)] = vIdx
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
			callables, callablesOK := postgresCallableRefsFromTokens(lex.Tokens)
			postgresStatements = append(postgresStatements, postgresAnalyzedStatement{
				result:      fingerprint.Result{Validity: res.Validity},
				objects:     statementObjects,
				pathUnsafe:  postgresStatementPathUnsafeFromTokens(lex.Tokens),
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
	profileFlags := profile.Flags()
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
		lex, lexErr := fingerprint.Tokenize(st.SQL, profileFlags)
		if lexErr != nil {
			return nil, nil, domain.FailureFromFingerprintCode(fingerprint.ErrNameParseFailed)
		}
		res, aerr := engine.AnalyzeLexed(profile, st.SQL, lex, mapper)
		if aerr != nil {
			return nil, nil, domain.FailureFromFingerprintCode(fingerprintFailureName(aerr))
		}
		op := operationLower(st.SQL)
		class := fingerprint.StatementClassOf(op)
		statementObjects := objectsAtStatementOrdinal(res.Objects, int32(ordinal))
		callables, callablesOK := postgresCallableRefsFromTokens(lex.Tokens)
		stmts = append(stmts, analyzedStatement{
			res: res, operation: op, class: class,
			noWhere: (op == "update" || op == "delete") && !hasWhereClause(st.SQL),
			objects: statementObjects, pathUnsafe: postgresStatementPathUnsafeFromTokens(lex.Tokens),
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
		var ordinalValid bool
		aggs[idx].statementOrdinalRanges, ordinalValid, _ = appendStatementOrdinalRangeTracked(
			aggs[idx].statementOrdinalRanges,
			int32(ordinal),
		)
		var objectsComplete, objRangesTruncated bool
		aggs[idx].objects, objectsComplete, objRangesTruncated = mergeObjectsTracked(
			aggs[idx].objects,
			aggs[idx].objectState,
			statementObjects,
			objectBudget,
		)
		if objRangesTruncated {
			aggs[idx].objectRangesTruncated = true
		}
		aggs[idx].metadataContextComplete = mergeMetadataContextComplete(
			aggs[idx].metadataContextComplete,
			res.MetadataContextComplete,
		) && ordinalValid && objectsComplete
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
		mergeStmtFactsInto(&aggs[idx].facts, st.SQL, op, class)
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
