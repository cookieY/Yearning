// statement_analysis.go Shared pure-compute SQL statement splitting, fingerprinting,
// and object-reference aggregation helpers used by grouping and ephemeral review runs.
package application

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strconv"
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
	out, valid, truncated := appendStatementOrdinalRangeTracked(ranges, ordinal)
	return out, valid && !truncated
}

// appendStatementOrdinalRangeTracked compresses monotonically increasing statement ordinals within a group
// into closed intervals. Malformed or non-monotonic ordinals return valid=false (fail closed); exceeding the
// display cap retains the first 512 intervals and reports valid=true, truncated=true so group metadata
// completeness is decoupled from the prompt display range cap.
func appendStatementOrdinalRangeTracked(
	ranges []statementOrdinalRange,
	ordinal int32,
) ([]statementOrdinalRange, bool, bool) {
	if ordinal < 1 {
		return ranges, false, false
	}
	if len(ranges) > 0 {
		last := &ranges[len(ranges)-1]
		if ordinal < last.Start {
			return ranges, false, false
		}
		if ordinal <= last.End {
			return ranges, true, false
		}
		if ordinal == last.End+1 {
			last.End = ordinal
			return ranges, true, false
		}
	}
	if len(ranges) >= maxStatementOrdinalRanges {
		return ranges, true, true
	}
	return append(ranges, statementOrdinalRange{Start: ordinal, End: ordinal}), true, false
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

func normalizedObjectRangesTracked(
	ref fingerprint.ObjectReference,
) ([]fingerprint.StatementOrdinalRange, bool, bool, bool) {
	if len(ref.StatementOrdinalRanges) == 0 {
		if ref.StatementOrdinal < 1 {
			return nil, false, false, false
		}
		return []fingerprint.StatementOrdinalRange{{
			Start: ref.StatementOrdinal,
			End:   ref.StatementOrdinal,
		}}, true, false, true
	}
	if len(ref.StatementOrdinalRanges) == 1 {
		r := ref.StatementOrdinalRanges[0]
		if r.Start < 1 || r.End < r.Start {
			return nil, false, false, false
		}
		return ref.StatementOrdinalRanges, true, false, false
	}
	capHint := len(ref.StatementOrdinalRanges)
	if capHint > maxObjectOrdinalRanges {
		capHint = maxObjectOrdinalRanges
	}
	out := make([]fingerprint.StatementOrdinalRange, 0, capHint)
	var last fingerprint.StatementOrdinalRange
	hasLast := false
	truncated := false
	for _, next := range ref.StatementOrdinalRanges {
		if next.Start < 1 || next.End < next.Start {
			return nil, false, false, false
		}
		if hasLast && next.Start < last.Start {
			return nil, false, false, false
		}
		last = next
		hasLast = true
		if truncated {
			continue
		}
		var ok, itemTruncated bool
		out, ok, itemTruncated = appendObjectOrdinalRangeTracked(out, next, maxObjectOrdinalRanges)
		if !ok {
			return nil, false, false, false
		}
		if itemTruncated {
			truncated = true
		}
	}
	return out, true, truncated, true
}

func mergeObjectOrdinalRangesTracked(
	existing []fingerprint.StatementOrdinalRange,
	incoming []fingerprint.StatementOrdinalRange,
	limit int,
) ([]fingerprint.StatementOrdinalRange, int, bool, bool) {
	out := make([]fingerprint.StatementOrdinalRange, len(existing), len(existing)+len(incoming))
	copy(out, existing)
	added := 0
	truncated := false
	var lastSeen fingerprint.StatementOrdinalRange
	hasLastSeen := len(out) > 0
	if hasLastSeen {
		lastSeen = out[len(out)-1]
	}
	for _, next := range incoming {
		if next.Start < 1 || next.End < next.Start {
			return append([]fingerprint.StatementOrdinalRange{}, existing...), 0, false, false
		}
		if hasLastSeen && next.Start < lastSeen.Start {
			return append([]fingerprint.StatementOrdinalRange{}, existing...), 0, false, false
		}
		lastSeen = next
		hasLastSeen = true
		if truncated {
			continue
		}
		before := len(out)
		var valid, itemTruncated bool
		out, valid, itemTruncated = appendObjectOrdinalRangeTracked(out, next, limit)
		if !valid {
			return append([]fingerprint.StatementOrdinalRange{}, existing...), 0, false, false
		}
		if itemTruncated {
			truncated = true
		}
		added += len(out) - before
	}
	return out, added, true, truncated
}

func appendObjectOrdinalRangeTracked(
	ranges []fingerprint.StatementOrdinalRange,
	next fingerprint.StatementOrdinalRange,
	limit int,
) ([]fingerprint.StatementOrdinalRange, bool, bool) {
	if next.Start < 1 || next.End < next.Start {
		return ranges, false, false
	}
	if len(ranges) > 0 {
		last := &ranges[len(ranges)-1]
		if next.Start < last.Start {
			return ranges, false, false
		}
		if next.Start <= last.End {
			if next.End > last.End {
				last.End = next.End
			}
			return ranges, true, false
		}
		if next.Start == last.End+1 {
			last.End = next.End
			return ranges, true, false
		}
	}
	if len(ranges) >= limit {
		return ranges, true, true
	}
	return append(ranges, next), true, false
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
	raw := firstOperationSlice(sql)
	if len(raw) == 0 {
		return "UNKNOWN"
	}
	return strings.ToUpper(string(raw))
}

// operationLower extracts the lowercase first SQL verb matching strings.ToLower(operationOf(sql))
// without double uppercase/lowercase allocations on the 100k hot path.
func operationLower(sql []byte) string {
	raw := firstOperationSlice(sql)
	if len(raw) == 0 {
		return "unknown"
	}
	if len(raw) == 1 && raw[0] >= 0x80 {
		return strings.ToLower(string(raw))
	}
	var stack [16]byte
	if len(raw) <= len(stack) {
		for i, b := range raw {
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			stack[i] = b
		}
		switch string(stack[:len(raw)]) {
		case "select":
			return "select"
		case "insert":
			return "insert"
		case "update":
			return "update"
		case "delete":
			return "delete"
		case "create":
			return "create"
		case "alter":
			return "alter"
		case "drop":
			return "drop"
		case "truncate":
			return "truncate"
		case "with":
			return "with"
		default:
			return string(stack[:len(raw)])
		}
	}
	return strings.ToLower(string(raw))
}

func firstOperationSlice(sql []byte) []byte {
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
				return sql[start : start+1]
			}
			return sql[start:i]
		}
	}
	return nil
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
	objectRangesTruncated       bool
	statementOrdinalRanges      []statementOrdinalRange
	metadataContextComplete     bool
	count                       int
	inMax, valMax               int
	unbounded                   bool      // Group contains an UPDATE/DELETE without WHERE
	facts                       stmtFacts // Statement structural facts within the group (no plaintext identifiers)
}

// sqlStatementTypedShape matches the exact JSON schema and alphabetical field order of sql_statements.typed_shape.
type sqlStatementTypedShape struct {
	AnonymizedSQL           string                        `json:"anonymized_sql"`
	DialectProfile          string                        `json:"dialect_profile"`
	ExecutionValidity       fingerprint.StatementValidity `json:"execution_validity"`
	Kind                    string                        `json:"kind"`
	MetadataContextComplete bool                          `json:"metadata_context_complete"`
	Objects                 []fingerprint.ObjectReference `json:"objects"`
	Operation               string                        `json:"operation"`
	ParameterCount          int                           `json:"parameter_count"`
	Quality                 string                        `json:"quality"`
	StatementOrdinal        int32                         `json:"statement_ordinal"`
	TupleCount              int                           `json:"tuple_count"`
}

var emptyJSONObjectBytes = []byte("{}")

func buildSQLStatementModel(
	runID, stmtID string,
	ordinal int32,
	res fingerprint.Result,
	op, class string,
	objects []fingerprint.ObjectReference,
) (persistence.SQLStatementModel, error) {
	typed, merr := jsonMarshal(sqlStatementTypedShape{
		AnonymizedSQL:           res.AnonymizedSQL,
		DialectProfile:          res.DialectProfile,
		ExecutionValidity:       res.Validity,
		Kind:                    res.StatementKind,
		MetadataContextComplete: res.MetadataContextComplete,
		Objects:                 objects,
		Operation:               op,
		ParameterCount:          res.ParameterCount,
		Quality:                 res.Quality,
		StatementOrdinal:        ordinal,
		TupleCount:              res.TupleCount,
	})
	if merr != nil {
		return persistence.SQLStatementModel{}, merr
	}
	var lit []byte
	if res.LiteralTypeCounts != nil && len(res.LiteralTypeCounts) == 0 {
		lit = emptyJSONObjectBytes
	} else {
		lit, merr = jsonMarshal(res.LiteralTypeCounts)
		if merr != nil {
			return persistence.SQLStatementModel{}, merr
		}
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

func appendLenString(buf []byte, s string) []byte {
	buf = strconv.AppendInt(buf, int64(len(s)), 10)
	buf = append(buf, ':')
	return append(buf, s...)
}

func appendValidityObjectKey(buf []byte, o fingerprint.ValidityObject) []byte {
	buf = appendLenString(buf, string(o.Kind))
	buf = appendLenString(buf, o.Token)
	buf = appendLenString(buf, o.SchemaToken)
	return appendLenString(buf, o.ParentToken)
}

func appendValidityKey(buf []byte, v fingerprint.StatementValidity) []byte {
	if v.Complete {
		buf = append(buf, '1')
	} else {
		buf = append(buf, '0')
	}
	if v.DefinitionsComplete {
		buf = append(buf, '1')
	} else {
		buf = append(buf, '0')
	}
	buf = appendLenString(buf, string(v.Operation))
	buf = appendLenString(buf, v.Unsupported)
	buf = strconv.AppendInt(buf, int64(len(v.Requirements)), 10)
	buf = append(buf, ';')
	for _, req := range v.Requirements {
		buf = appendValidityObjectKey(buf, req.Object)
		buf = appendLenString(buf, string(req.State))
		buf = appendLenString(buf, string(req.Condition))
	}
	buf = strconv.AppendInt(buf, int64(len(v.Effects)), 10)
	buf = append(buf, ';')
	for _, eff := range v.Effects {
		buf = appendLenString(buf, string(eff.Action))
		buf = appendValidityObjectKey(buf, eff.Object)
		if eff.Replacement == nil {
			buf = append(buf, '0')
		} else {
			buf = append(buf, '1')
			buf = appendValidityObjectKey(buf, *eff.Replacement)
		}
		buf = appendLenString(buf, string(eff.Condition))
	}
	return buf
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
		class := statementClassOf(op)
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
