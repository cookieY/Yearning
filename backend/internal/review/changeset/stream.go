package changeset

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// Limits caps input streaming, fingerprint cardinality, and sampled references per batch.
type Limits struct {
	MaxInputBytes         int // 32 MiB
	MaxStatements         int // 100,000
	MaxStatementBytes     int // 512 KiB
	MaxUniqueFingerprints int // 1,000
	ComplexityWarning     int // 200
	MaxSamplesPerGroup    int // 3
	MaxExceptionalRefs    int // 1,000
}

// DefaultLimits returns the contract defaults for streaming statement classification and grouping.
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

// Sample captures metadata for an anonymized representative statement within a fingerprint group.
type Sample struct {
	Index              int
	Offset             int64
	ByteLen            int
	ExecutionShapeHMAC string
	ParameterCount     int
	Quality            string
}

// Group aggregates statements sharing a canonical execution shape fingerprint.
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

// Exceptional records a statement flagged due to non-exact parse quality or warnings.
type Exceptional struct {
	Sample
	Reason string
}

// Batch is the summarized structural grouping artifact produced by Stream.
type Batch struct {
	StatementCount         int
	TotalBytes             int64
	UniqueFingerprintCount int
	ComplexityWarning      bool
	Groups                 []*Group
	Exceptional            []Exceptional
	ExceptionalCount       int
}

// Stream splits raw SQL from r, analyzes each statement into fingerprint groups, and enforces safety limits.
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
		if !slices.Contains(g.DialectFingerprintHMACs, res.DialectFingerprintHMAC) {
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

// absorb merges a statement analysis result into its Group and maintains up to 3 samples (first, extreme, last).
func absorb(g *Group, stmt Statement, res fingerprint.Result) {
	g.Count++
	g.LastIndex = stmt.Index
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
	if !slices.Contains(g.Kinds, res.StatementKind) {
		g.Kinds = append(g.Kinds, res.StatementKind)
	}
	if !slices.Contains(g.DialectProfiles, res.DialectProfile) {
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

type countingReader struct {
	r     io.Reader
	total int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.total += int64(n)
	return n, err
}
