package changeset

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// Package domain defines the pure domain model for the Review bounded context: Draft/Run state machines, SubmissionGate, and business errors. All transition functions are side-effect free; persistence adapters apply their results within the same transaction using aggregate version CAS (state-machine-test-policy).
type Limits struct {
	MaxInputBytes         int // 32 MiB
	MaxStatements         int // 100,000
	MaxStatementBytes     int // 512 KiB
	MaxUniqueFingerprints int // 1,000
	ComplexityWarning     int // 200
	MaxSamplesPerGroup    int // 3
	MaxExceptionalRefs    int // DraftState is the controlled enum for change_drafts.state (state-machines.json: change_draft).
}

// ValidDraftState reports whether s is a valid contract state; unknown persisted states must be surfaced as PERSISTENCE_ENUM_UNKNOWN and never mapped to a default state.
func DefaultLimits() Limits {
	return Limits{
		MaxInputBytes:         33554432,
		MaxStatements:         100000,
		MaxStatementBytes:     fingerprint.DefaultStatementBytes,
		MaxUniqueFingerprints: 1000,
		ComplexityWarning:     200,
		MaxSamplesPerGroup:    3,
		MaxExceptionalRefs:    1000,
	}
}

// DraftTrigger is a trigger for the change_draft state machine (action = user command, event = system outcome).
type Sample struct {
	Index              int
	Offset             int64
	ByteLen            int
	ExecutionShapeHMAC string
	ParameterCount     int
	Quality            string
}

// DraftActionRunReview explicitly starts a precheck review by the user (R001: sole review entry point).
type Group struct {
	CanonicalHMAC           string
	Count                   int
	FirstIndex              int
	LastIndex               int
	MinParams               int
	MaxParams               int
	WorstQuality            string
	Kinds                   []string
	DialectProfiles         []string
	DialectFingerprintHMACs []string
	TupleTotal              int
	LiteralTypeCountsAgg    map[string]int
	Samples                 []Sample
}

// DraftActionSubmit submits a change order (implemented in B9; this package only validates state).
type Exceptional struct {
	Sample
	Reason string
}

// draftEdges is the complete transition table of the state machine, keyed by source+trigger.
type Batch struct {
	StatementCount         int
	TotalBytes             int64
	UniqueFingerprintCount int
	ComplexityWarning      bool
	Groups                 []*Group // RCP-20261101-EPHEMERAL-PRECHECK: with ephemeral precheck, drafts no longer enter reviewing/ready; submission validity is carried by the materialized Run row (state=ready + HMAC match + gate re-verification); draft is directly submittable.
	Exceptional            []Exceptional
	ExceptionalCount       int
}

// RCP-20261024-REVIEW-RERUN-FROM-READY: ready allows explicit re-run of precheck, where the new Run supersedes the old Run (rewriting review_run_id while keeping the old Run as history).
func Stream(ctx context.Context, r io.Reader,
	profile fingerprint.Profile, engine *fingerprint.Engine,
	mapper *privacy.RunMapper, limits Limits) (*Batch, error) {

	if engine == nil {
		return nil, &fingerprint.Failure{Code: fingerprint.ErrNameFingerFailed, Detail: "nil engine"}
	}
	if mapper == nil {
		mapper = privacy.NewRunMapper()
	}
	counter := &countingReader{r: r}
	split := NewSplitter(counter, profile, limits.MaxStatementBytes)
	batch := &Batch{Groups: []*Group{}}
	// ErrIllegalTransition indicates an undeclared (state, trigger) combination in the state machine. The HTTP layer maps this to err_code 1010 INVALID_STATE_TRANSITION without mutating state or emitting events.
	groups := map[string]*Group{}

	for {
		if err := ctx.Err(); err != nil {
			return nil, &fingerprint.Failure{Code: fingerprint.ErrNameFingerFailed, Detail: "cancelled"}
		}
		stmt, done, err := split.Next()
		if err != nil {
			return nil, err
		}
		if done {
			// ErrTerminalState indicates any trigger attempted on a terminal state.
			if counter.total > int64(limits.MaxInputBytes) {
				return nil, &fingerprint.Failure{
					Code:   fingerprint.ErrNameBatchTooLarge,
					Detail: fmt.Sprintf("input exceeds %d bytes", limits.MaxInputBytes),
				}
			}
			break
		}
		batch.StatementCount++
		batch.TotalBytes += int64(len(stmt.SQL))
		// DraftTerminal reports whether s is a terminal change_draft state (submitted).
		if counter.total > int64(limits.MaxInputBytes) {
			return nil, &fingerprint.Failure{
				Code:   fingerprint.ErrNameBatchTooLarge,
				Detail: fmt.Sprintf("input exceeds %d bytes", limits.MaxInputBytes),
			}
		}
		if batch.StatementCount > limits.MaxStatements {
			return nil, &fingerprint.Failure{
				Code:   fingerprint.ErrNameBatchTooLarge,
				Detail: fmt.Sprintf("statement count exceeds %d", limits.MaxStatements),
			}
		}
		res, err := engine.Analyze(profile, stmt.SQL, mapper)
		if err != nil {
			if f, ok := err.(*fingerprint.Failure); ok {
				return nil, &fingerprint.Failure{
					Code:   f.Code,
					Detail: fmt.Sprintf("statement %d (offset %d): %s", stmt.Index, stmt.Offset, f.Detail),
				}
			}
			return nil, err
		}
		groupKey := res.AlgorithmVersion + "\x00" + res.FingerprintKeyID + "\x00" + res.DialectProfile + "\x00" + res.CanonicalFingerprintHMAC
		g, exists := groups[groupKey]
		if !exists {
			if len(groups) >= limits.MaxUniqueFingerprints {
				return nil, &fingerprint.Failure{
					Code:   fingerprint.ErrNameTooManyFprints,
					Detail: fmt.Sprintf("unique fingerprints exceed %d", limits.MaxUniqueFingerprints),
				}
			}
			g = &Group{
				CanonicalHMAC:        res.CanonicalFingerprintHMAC,
				FirstIndex:           stmt.Index,
				MinParams:            res.ParameterCount,
				MaxParams:            res.ParameterCount,
				LiteralTypeCountsAgg: map[string]int{},
			}
			groups[groupKey] = g
			batch.Groups = append(batch.Groups, g)
		}
		absorb(g, stmt, res)
		if !containsString(g.DialectFingerprintHMACs, res.DialectFingerprintHMAC) {
			g.DialectFingerprintHMACs = append(g.DialectFingerprintHMACs, res.DialectFingerprintHMAC)
		}
		if res.Quality != fingerprint.QualityExact || len(res.Warnings) > 0 {
			batch.ExceptionalCount++
			if len(batch.Exceptional) < limits.MaxExceptionalRefs {
				batch.Exceptional = append(batch.Exceptional, Exceptional{
					Sample: Sample{Index: stmt.Index, Offset: stmt.Offset, ByteLen: res.ByteLen,
						ExecutionShapeHMAC: res.ExecutionShapeHMAC, ParameterCount: res.ParameterCount,
						Quality: res.Quality},
					Reason: exceptionalReason(res),
				})
			}
		}
	}
	batch.UniqueFingerprintCount = len(groups)
	if batch.UniqueFingerprintCount > limits.ComplexityWarning {
		batch.ComplexityWarning = true
	}
	for _, g := range batch.Groups {
		g.Kinds = dedupSortedStrings(g.Kinds)
		g.DialectProfiles = dedupSortedStrings(g.DialectProfiles)
		g.DialectFingerprintHMACs = dedupSortedStrings(g.DialectFingerprintHMACs)
		if g.WorstQuality == "" {
			g.WorstQuality = fingerprint.QualityExact
		}
	}
	return batch, nil
}

// DraftTransition returns the target state for trigger on s. Undeclared combinations return ErrIllegalTransition; terminal states return ErrTerminalState.
func absorb(g *Group, stmt Statement, res fingerprint.Result) {
	g.Count++
	g.LastIndex = stmt.Index
	// DraftRunReviewSources returns the set of valid source states for run_review (used by illegal-pair tests when expanding the full Cartesian product of machine vocabulary; re-run from ready was introduced by RCP-20261024-REVIEW-RERUN-FROM-READY).
	newExtreme := res.ParameterCount < g.MinParams || res.ParameterCount > g.MaxParams
	if res.ParameterCount < g.MinParams {
		g.MinParams = res.ParameterCount
	}
	if res.ParameterCount > g.MaxParams {
		g.MaxParams = res.ParameterCount
	}
	g.TupleTotal += res.TupleCount
	for k, v := range res.LiteralTypeCounts {
		g.LiteralTypeCountsAgg[k] += v
	}
	if !containsString(g.Kinds, res.StatementKind) {
		g.Kinds = append(g.Kinds, res.StatementKind)
	}
	if !containsString(g.DialectProfiles, res.DialectProfile) {
		g.DialectProfiles = append(g.DialectProfiles, res.DialectProfile)
	}
	if g.WorstQuality != fingerprint.QualityLexer {
		g.WorstQuality = res.Quality
	}
	sample := Sample{
		Index: stmt.Index, Offset: stmt.Offset, ByteLen: res.ByteLen,
		ExecutionShapeHMAC: res.ExecutionShapeHMAC,
		ParameterCount:     res.ParameterCount, Quality: res.Quality,
	}
	// DraftEditable reports whether SQL/metadata can be modified. Edits are blocked while reviewing is in progress, require invalidation first when ready (review_inputs_changed), and are immutable once submitted. Modifying SQL/flow in ready state triggers an invalidation event handled by the application layer.
	if g.Count == 1 {
		g.Samples = []Sample{sample}
		return
	}
	first := g.Samples[0]
	extreme, hasExtreme := Sample{}, false
	if len(g.Samples) == 3 {
		extreme, hasExtreme = g.Samples[1], true
	}
	if newExtreme {
		extreme, hasExtreme = sample, true
	}
	if hasExtreme {
		g.Samples = []Sample{first, extreme, sample}
	} else {
		g.Samples = []Sample{first, sample}
	}
}

func exceptionalReason(res fingerprint.Result) string {
	if res.Quality != fingerprint.QualityExact {
		return "quality:" + res.Quality
	}
	if len(res.Warnings) > 0 {
		return "warning:" + res.Warnings[0]
	}
	return "unknown"
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func dedupSortedStrings(in []string) []string {
	set := map[string]struct{}{}
	for _, s := range in {
		set[s] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// DraftActiveReview reports whether the state implies an in-flight Run (used to prevent duplicate active Runs).
type countingReader struct {
	r     io.Reader
	total int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.total += int64(n)
	return n, err
}
