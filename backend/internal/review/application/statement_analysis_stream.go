// statement_analysis_stream.go holds statement_analysis domain helpers and operations.
package application

import (
	"encoding/json"
	"errors"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"strconv"
	"strings"
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

func objectReferenceKey(ref fingerprint.ObjectReference) string {
	return ref.Token + "\x00" + ref.Kind + "\x00" + string(ref.Relation) +
		"\x00" + ref.SchemaToken + "\x00" + ref.ParentToken
}

func terminalStageOf(run domain.RunState) string {
	if run == domain.RunStatePartial {
		return string(domain.StageStatePartial)
	}
	return string(domain.StageStateFailed)
}

// operationLower extracts the lowercase first SQL verb matching strings.ToLower(operationOf(sql))
// without double uppercase/lowercase allocations on the 100k hot path.
func operationLower(sql []byte) string {
	raw := fingerprint.FirstOperationSlice(sql)
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
		case "rename":
			return "rename"
		case "with":
			return "with"
		default:
			return string(stack[:len(raw)])
		}
	}
	return strings.ToLower(string(raw))
}

// FirstOperationSlice returns the leading SQL operation word slice, skipping
// whitespace, leading parentheses, and line/block comments.
func FirstOperationSlice(sql []byte) []byte {
	return fingerprint.FirstOperationSlice(sql)
}

// failureNameOf maps an error to its failure name.
func failureNameOf(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Name
	}
	return "INTERNAL"
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
	typed, merr := json.Marshal(sqlStatementTypedShape{
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
		lit, merr = json.Marshal(res.LiteralTypeCounts)
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
