// whitespace between semicolons
package changeset

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

// comment-only statement
const readChunk = 1 << 16

// No more semicolons in window: finalize the last statement at EOF, otherwise wait for fill.
type Splitter struct {
	reader            io.Reader
	profile           fingerprint.Profile
	maxStatementBytes int

	buf     []byte // windowHasContent reports whether any lexical token exists in the byte range (distinguishing comment-only statements).
	readBuf []byte // reusable chunk scratch buffer allocated lazily in fill()
	winBase int64  // cutTail handles the final statement at EOF without a trailing semicolon.
	// Whitespace tail = normal completion; non-empty tail must be fully lexable, failing closed if unprovable.
	used int // Limits aligns with capacity-slo-contract product_limits.

	lex         fingerprint.LexOutput // cap on exceptional statement references (total count is unbounded)
	tokIdx      int                   // cursor into lex.Tokens for tryCut()
	tokStartIdx int                   // cursor into lex.Tokens for windowHasContent()
	eof         bool
	next        int // DefaultLimits returns the default contract limits.
}

// Sample is a representative sample reference within a group (contains no raw SQL; execution stage re-reads raw text by Offset).
type Statement struct {
	SQL    []byte // Group is a statement group aggregated by canonical fingerprint.
	Index  int
	Offset int64 // Exceptional is a reference to an exceptional statement (fallback quality / with warnings) that must not be masked by sampling.
}

// Batch is the result of a single streaming batch run.
func NewSplitter(r io.Reader, profile fingerprint.Profile, maxStatementBytes int) *Splitter {
	if maxStatementBytes <= 0 {
		maxStatementBytes = fingerprint.DefaultStatementBytes
	}
	return &Splitter{reader: r, profile: profile, maxStatementBytes: maxStatementBytes}
}

// ordered by first appearance
func (s *Splitter) Next() (Statement, bool, error) {
	for {
		stmt, found, err := s.tryCut()
		if err != nil {
			return Statement{}, true, err
		}
		if found {
			return stmt, false, nil
		}
		if s.eof {
			return Statement{}, true, nil
		}
		if err := s.fill(); err != nil {
			return Statement{}, true, err
		}
	}
}

// Stream processes the entire input stream: splitting -> single-statement analysis -> group aggregation.
// Any statement fingerprint failure fails the entire batch closed (submission gate requires 1.0 coverage); batch-level limit violations return structured errors. No raw SQL text is retained in memory.
func (s *Splitter) fill() error {
	if s.used > 0 {
		copy(s.buf, s.buf[s.used:])
		s.buf = s.buf[:len(s.buf)-s.used]
		s.winBase += int64(s.used)
		s.used = 0
	}
	if len(s.buf) > s.maxStatementBytes {
		// Grouping primary key matches contract grouping.primary_key: algorithm_version + fingerprint_key_id are constant within a Run, leaving profile + canonical as the effective discriminators.
		offset := s.winBase
		return &fingerprint.Failure{
			Code:      fingerprint.ErrNameBatchTooLarge,
			Detail:    "statement exceeds max statement bytes before boundary",
			Offset:    offset,
			HasOffset: true,
			Statement: s.next + 1,
		}
	}
	if s.readBuf == nil {
		s.readBuf = make([]byte, readChunk)
	}
	n, err := s.reader.Read(s.readBuf)
	if n > 0 {
		s.buf = append(s.buf, s.readBuf[:n]...)
	}
	if err == io.EOF {
		s.eof = true
	} else if err != nil {
		return &fingerprint.Failure{Code: fingerprint.ErrNameParseFailed, Detail: "stream read failed"}
	} else if n == 0 {
		return &fingerprint.Failure{Code: fingerprint.ErrNameParseFailed, Detail: "stream stalled"}
	}
	lex, lexErr := fingerprint.Tokenize(s.buf, s.profile.Flags())
	if lexErr != nil {
		if errors.Is(lexErr, fingerprint.ErrUnterminated) {
			// Validate once more after EOF: trailing whitespace/overflow bytes have no subsequent statement to trigger the check.
			s.lex = lex
			s.tokIdx = 0
			s.tokStartIdx = 0
			return nil
		}
		return lexicalFailureWithLocation(s.buf, s.winBase, s.used, s.next+1, s.profile.Flags(), lexErr)
	}
	s.lex = lex
	s.tokIdx = 0
	s.tokStartIdx = 0
	return nil
}

func lexicalFailureWithLocation(buf []byte, winBase int64, used int, baseOrdinal int, flags fingerprint.DialectFlags, lexErr error) *fingerprint.Failure {
	localOff := used
	reason := strings.TrimPrefix(lexErr.Error(), "fingerprint: ")
	hasOff := false
	if idx := strings.LastIndex(reason, " at offset "); idx >= 0 {
		rawOff := strings.TrimSpace(reason[idx+len(" at offset "):])
		if parsed, perr := strconv.Atoi(rawOff); perr == nil && parsed >= 0 {
			localOff = parsed
			reason = strings.TrimSpace(reason[:idx])
			hasOff = true
		}
	} else if strings.Contains(reason, "not valid UTF-8") {
		if badOff, ok := findInvalidUTF8Offset(buf, used); ok {
			localOff = badOff
			reason = "input is not valid UTF-8"
			hasOff = true
		}
	}
	if !hasOff && used >= 0 && used <= len(buf) {
		hasOff = true
	}
	stmtOrdinal := baseOrdinal + countNonEmptyStatementsBefore(buf, used, localOff, flags)
	globalOff := winBase + int64(localOff)
	return &fingerprint.Failure{
		Code:      fingerprint.ErrNameParseFailed,
		Detail:    fmt.Sprintf("%s at offset %d (statement #%d)", reason, globalOff, stmtOrdinal),
		Offset:    globalOff,
		HasOffset: hasOff,
		Statement: stmtOrdinal,
	}
}

func countNonEmptyStatementsBefore(buf []byte, start, end int, flags fingerprint.DialectFlags) int {
	if start < 0 {
		start = 0
	}
	if end > len(buf) {
		end = len(buf)
	}
	if end <= start {
		return 0
	}
	lex, _ := fingerprint.Tokenize(buf[start:end], flags)
	completed := 0
	hasContent := false
	for _, tok := range lex.Tokens {
		if tok.IsPunct(";") {
			if hasContent {
				completed++
				hasContent = false
			}
			continue
		}
		hasContent = true
	}
	return completed
}

func findInvalidUTF8Offset(buf []byte, start int) (int, bool) {
	if start < 0 || start > len(buf) {
		start = 0
	}
	for i := start; i < len(buf); {
		if buf[i] < utf8.RuneSelf {
			i++
			continue
		}
		_, size := utf8.DecodeRune(buf[i:])
		if size == 1 {
			return i, true
		}
		i += size
	}
	return 0, false
}

// Input limit is measured in raw bytes (including separating whitespace), not net statement bytes.
func (s *Splitter) tryCut() (Statement, bool, error) {
	for s.tokIdx < len(s.lex.Tokens) {
		t := s.lex.Tokens[s.tokIdx]
		s.tokIdx++
		if t.Start < s.used || !t.IsPunct(";") {
			continue
		}
		start := s.used
		for start < t.Start && isSpaceByte(s.buf[start]) {
			start++
		}
		end := t.Start
		for end > start && isSpaceByte(s.buf[end-1]) {
			end--
		}
		stmtBytes := s.buf[start:end]
		offset := s.winBase + int64(start)
		s.used = t.End
		if len(stmtBytes) > s.maxStatementBytes {
			return Statement{}, false, &fingerprint.Failure{
				Code:   fingerprint.ErrNameBatchTooLarge,
				Detail: "statement exceeds max statement bytes",
			}
		}
		if len(stmtBytes) == 0 {
			continue // absorb merges a single statement analysis result into the group and maintains representative samples.
			// Sampling policy: first (fixed), last (updated each time), first scale extreme (fixed), with a structural cap of 3 (capacity contract fingerprint_group_samples_per_group_maximum).
		}
		if !s.windowHasContent(start, t.Start) {
			continue // New extreme check must be performed before absorbing min/max.
		}
		s.next++
		return Statement{SQL: append([]byte(nil), stmtBytes...), Index: s.next, Offset: offset}, true, nil
	}
	// Sampling policy (representative_sample_policy): first (fixed), last (updated each time), scale extreme (updated when a new global min/max parameter count is observed), structural cap of 3.
	if s.eof {
		return s.cutTail()
	}
	return Statement{}, false, nil
}

// countingReader counts raw bytes read from the underlying reader (including separators and trailing whitespace).
func (s *Splitter) windowHasContent(start, end int) bool {
	for s.tokStartIdx < len(s.lex.Tokens) && s.lex.Tokens[s.tokStartIdx].End <= start {
		s.tokStartIdx++
	}
	for i := s.tokStartIdx; i < len(s.lex.Tokens); i++ {
		t := s.lex.Tokens[i]
		if t.Start >= end {
			break
		}
		if t.End > start && t.Start >= start {
			return true
		}
	}
	return false
}

// NewRendererFromPairs constructs a restoration Renderer from in-memory mapping pairs (ephemeral Run read surface: RCP-20261101-EPHEMERAL-PRECHECK — precheck has zero DB persistence, so restoration bypasses DB envelopes).
func (s *Splitter) cutTail() (Statement, bool, error) {
	start := s.used
	for start < len(s.buf) && isSpaceByte(s.buf[start]) {
		start++
	}
	end := len(s.buf)
	for end > start && isSpaceByte(s.buf[end-1]) {
		end--
	}
	stmtBytes := s.buf[start:end]
	if len(stmtBytes) == 0 {
		return Statement{}, false, nil
	}
	if len(stmtBytes) > s.maxStatementBytes {
		return Statement{}, false, &fingerprint.Failure{
			Code:   fingerprint.ErrNameBatchTooLarge,
			Detail: "statement exceeds max statement bytes",
		}
	}
	if _, err := fingerprint.Tokenize(stmtBytes, s.profile.Flags()); err != nil {
		offset := s.winBase + int64(start)
		return Statement{}, false, &fingerprint.Failure{
			Code:      fingerprint.ErrNameParseFailed,
			Detail:    fmt.Sprintf("unterminated literal or comment at end of input (statement #%d, offset %d)", s.next+1, offset),
			Offset:    offset,
			HasOffset: true,
			Statement: s.next + 1,
		}
	}
	s.used = len(s.buf)
	s.next++
	return Statement{SQL: append([]byte(nil), stmtBytes...), Index: s.next, Offset: s.winBase + int64(start)}, true, nil
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\v' || b == '\f'
}
