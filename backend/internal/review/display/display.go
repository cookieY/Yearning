// Package display restores Run-scoped anonymous identifiers in authorized
// review text after the encrypted mapping has been loaded server-side.
//
// Provider inputs and persisted Review results remain anonymous. Callers must
// invoke this package only while constructing an authorized display copy, and
// only for summary, title, message, and suggestion fields.
package display

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

const mappingEnvelopeStatementIdx = 1

// ErrMappingUnavailable deliberately omits mapping contents and the Run ID.
// Callers should fail the display read closed and return a generic client error.
var ErrMappingUnavailable = errors.New("review display mapping unavailable")

// Renderer is an immutable Run-scoped identifier restorer. Its zero value and a
// nil receiver preserve input unchanged.
type Renderer struct {
	replacements  map[string]string
	maxTokenBytes int
}

// Load reads and authenticates the Run-level mapping envelope. Historical Runs
// without a mapping row remain displayable with their anonymous tokens intact.
// Every malformed, unauthenticated, or inconsistent mapping fails closed.
func Load(
	ctx context.Context,
	db *gorm.DB,
	crypter *crypto.Crypter,
	runID string,
) (*Renderer, error) {
	if ctx == nil || db == nil || crypter == nil || runID == "" {
		return nil, ErrMappingUnavailable
	}

	var row struct {
		ID                string `gorm:"column:id"`
		MappingEntryCount int32  `gorm:"column:mapping_entry_count"`
		Ciphertext        []byte `gorm:"column:identifier_mapping_ciphertext"`
		Nonce             []byte `gorm:"column:identifier_mapping_nonce"`
		KeyID             string `gorm:"column:identifier_mapping_key_id"`
		Algorithm         string `gorm:"column:identifier_mapping_algorithm"`
	}
	err := db.WithContext(ctx).
		Table("review_identifier_mappings").
		Select(
			"id, mapping_entry_count, identifier_mapping_ciphertext, identifier_mapping_nonce, "+
				"identifier_mapping_key_id, identifier_mapping_algorithm",
		).
		Where("review_run_id = ? AND statement_idx = ?", runID, mappingEnvelopeStatementIdx).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &Renderer{}, nil
	}
	if err != nil || row.KeyID == "" {
		return nil, ErrMappingUnavailable
	}

	return rendererFromEnvelope(crypter, privacy.EnvelopeBinding{
		ReviewRunID:  runID,
		StatementIdx: mappingEnvelopeStatementIdx,
	}, crypto.Envelope{
		Ciphertext: row.Ciphertext,
		Nonce:      row.Nonce,
		KeyID:      row.KeyID,
		Algorithm:  row.Algorithm,
	}, int(row.MappingEntryCount))
}

// section 18.75-A: auth rejections also carry a snapshot — "status 401" is the exact detail
// operators need to see at a glance (invalid key / uncharged account / domain permission).
func NewRendererFromPairs(pairs []privacy.MappingPair) (*Renderer, error) {
	return newRendererFromPairs(pairs, len(pairs))
}

func rendererFromEnvelope(
	crypter *crypto.Crypter,
	binding privacy.EnvelopeBinding,
	envelope crypto.Envelope,
	declaredCount int,
) (*Renderer, error) {
	pairs, err := privacy.OpenMapping(crypter, binding, envelope)
	if err != nil {
		return nil, ErrMappingUnavailable
	}
	return newRendererFromPairs(pairs, declaredCount)
}

// RestoreText returns a display copy with known complete tokens replaced. It
// performs one scan over the input and never rescans replacement output.
func (r *Renderer) RestoreText(text string) (string, int) {
	restored, count, _ := r.restoreText(text, -1)
	return restored, count
}

// RestorePointerLimit restores a nullable text field only when the complete
// restored value fits maxRunes. An over-limit value stays entirely anonymous,
// with a zero replacement count and no truncation.
func (r *Renderer) RestorePointerLimit(value *string, maxRunes int) (*string, int) {
	if value == nil {
		return nil, 0
	}
	if maxRunes < 0 {
		maxRunes = 0
	}
	restored, count, withinLimit := r.restoreText(*value, maxRunes)
	if !withinLimit {
		original := *value
		return &original, 0
	}
	return &restored, count
}

// RestorePointer copies and restores a nullable text field. It never mutates
// the caller's string or pointer.
func (r *Renderer) RestorePointer(value *string) (*string, int) {
	if value == nil {
		return nil, 0
	}
	restored, count := r.RestoreText(*value)
	return &restored, count
}

func (r *Renderer) restoreText(text string, maxRunes int) (string, int, bool) {
	if r == nil || len(r.replacements) == 0 || text == "" {
		if maxRunes >= 0 && utf8.RuneCountInString(text) > maxRunes {
			return "", 0, false
		}
		return text, 0, true
	}

	var b strings.Builder
	b.Grow(len(text))
	replaced := 0
	runeCount := 0
	var identifierQuote byte
	appendText := func(value string) bool {
		runes := utf8.RuneCountInString(value)
		if maxRunes >= 0 && (runes > maxRunes || runeCount > maxRunes-runes) {
			return false
		}
		b.WriteString(value)
		runeCount += runes
		return true
	}

	for i := 0; i < len(text); {
		if identifierQuote != 0 {
			if text[i] == identifierQuote {
				if i+1 < len(text) && text[i+1] == identifierQuote {
					if !appendText(text[i : i+2]) {
						return "", 0, false
					}
					i += 2
					continue
				}
				if !appendText(text[i : i+1]) {
					return "", 0, false
				}
				identifierQuote = 0
				i++
				continue
			}
		} else {
			if end, ok := sqlSkippedRange(text, i); ok {
				if !appendText(text[i:end]) {
					return "", 0, false
				}
				i = end
				continue
			}
			if text[i] == '`' {
				run := backtickRunLength(text, i)
				if run >= 3 {
					// Markdown fenced code delimiters are visible syntax. Consume
					// them without entering a SQL identifier quote so fenced SQL
					// receives the normal literal/comment/identifier scan.
					if !appendText(text[i : i+run]) {
						return "", 0, false
					}
					i += run
					continue
				}
				if run == 2 {
					end := markdownInlineCodeEnd(text, i, run)
					if !appendText(text[i:end]) {
						return "", 0, false
					}
					i = end
					continue
				}
				end := singleBacktickSpanEnd(text, i)
				if end < 0 {
					if !appendText(text[i:]) {
						return "", 0, false
					}
					i = len(text)
					continue
				}
				content := text[i+1 : end]
				if !isSimpleBacktickIdentifier(content) {
					if isLikelySQLFragment(content) {
						contentLimit := -1
						if maxRunes >= 0 {
							contentLimit = maxRunes - runeCount - 2
							if contentLimit < 0 {
								return "", 0, false
							}
						}
						restored, count, withinLimit := r.restoreText(content, contentLimit)
						if !withinLimit ||
							!appendText(text[i:i+1]) ||
							!appendText(restored) ||
							!appendText(text[end:end+1]) {
							return "", 0, false
						}
						replaced += count
						i = end + 1
						continue
					}
					// Ambiguous inline content is safer kept anonymous whole.
					if !appendText(text[i : end+1]) {
						return "", 0, false
					}
					i = end + 1
					continue
				}
			}
			if text[i] == '"' || text[i] == '`' {
				if !appendText(text[i : i+1]) {
					return "", 0, false
				}
				identifierQuote = text[i]
				i++
				continue
			}
		}

		if i > 0 && isASCIIIdentifier(text[i-1]) {
			_, size := utf8.DecodeRuneInString(text[i:])
			if !appendText(text[i : i+size]) {
				return "", 0, false
			}
			i += size
			continue
		}

		remaining := len(text) - i
		limit := r.maxTokenBytes
		if limit > remaining {
			limit = remaining
		}
		matched := false
		for n := limit; n >= 1; n-- {
			end := i + n
			if end < len(text) && isASCIIIdentifier(text[end]) {
				continue
			}
			original, ok := r.replacements[text[i:end]]
			if !ok {
				continue
			}
			if !appendText(original) {
				return "", 0, false
			}
			replaced++
			i = end
			matched = true
			break
		}
		if matched {
			continue
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		if !appendText(text[i : i+size]) {
			return "", 0, false
		}
		i += size
	}
	return b.String(), replaced, true
}

func newRendererFromPairs(pairs []privacy.MappingPair, declaredCount int) (*Renderer, error) {
	if declaredCount < 0 || len(pairs) != declaredCount {
		return nil, ErrMappingUnavailable
	}

	mapper := privacy.NewRunMapper()
	if err := mapper.Restore(pairs); err != nil || mapper.Len() != declaredCount {
		return nil, ErrMappingUnavailable
	}
	validated := mapper.Snapshot()
	r := &Renderer{replacements: make(map[string]string, len(validated))}
	for _, pair := range validated {
		if pair.Token == "" || pair.Original == "" {
			return nil, ErrMappingUnavailable
		}
		if _, exists := r.replacements[pair.Token]; exists {
			return nil, ErrMappingUnavailable
		}
		r.replacements[pair.Token] = pair.Original
		if len(pair.Token) > r.maxTokenBytes {
			r.maxTokenBytes = len(pair.Token)
		}
	}
	return r, nil
}

func isASCIIIdentifier(c byte) bool {
	return c >= 'a' && c <= 'z' ||
		c >= 'A' && c <= 'Z' ||
		c >= '0' && c <= '9' ||
		c == '_' || c == '$'
}

// sqlSkippedRange conservatively identifies source ranges that must remain
// byte-for-byte anonymous. This is a display-safety scanner, not a complete SQL
// parser: unterminated literals/comments consume the remainder, PostgreSQL
// dollar tags use ASCII identifier syntax, and ambiguous starts favor keeping
// text hidden. Double-quoted and backtick identifiers are handled separately so
// their known tokens can still be restored.
func sqlSkippedRange(text string, i int) (int, bool) {
	switch text[i] {
	case '\'':
		if !isSingleQuotedLiteralStart(text, i) {
			return 0, false
		}
		return singleQuotedLiteralEnd(text, i), true
	case '#':
		return lineCommentEnd(text, i), true
	case '-':
		if i+1 < len(text) && text[i+1] == '-' {
			return lineCommentEnd(text, i), true
		}
	case '/':
		if i+1 < len(text) && text[i+1] == '*' {
			return blockCommentEnd(text, i), true
		}
	case '$':
		if i == 0 || !isASCIIIdentifier(text[i-1]) {
			return dollarQuotedLiteralEnd(text, i)
		}
	}
	return 0, false
}

func isSingleQuotedLiteralStart(text string, i int) bool {
	if i == 0 {
		return true
	}

	prefixStart := i
	for prefixStart > 0 && isASCIIIdentifier(text[prefixStart-1]) {
		prefixStart--
	}
	prefix := strings.ToLower(text[prefixStart:i])
	if prefix == "" {
		return true
	}
	if strings.HasPrefix(prefix, "_") {
		return true
	}
	switch prefix {
	case "b", "e", "n", "u", "x",
		"select", "where", "values", "in", "like", "and", "or",
		"then", "else", "when", "case", "return", "escape":
		return true
	}
	for i := range len(prefix) {
		if isASCIILetter(prefix[i]) {
			// An ASCII word immediately before the quote is a natural-language
			// contraction or possessive unless it is a known SQL introducer.
			return false
		}
	}
	return true
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func singleQuotedLiteralEnd(text string, start int) int {
	for i := start + 1; i < len(text); {
		switch text[i] {
		case '\\':
			i++
			if i < len(text) {
				_, size := utf8.DecodeRuneInString(text[i:])
				i += size
			}
		case '\'':
			if i+1 < len(text) && text[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1
		default:
			_, size := utf8.DecodeRuneInString(text[i:])
			i += size
		}
	}
	return len(text)
}

func lineCommentEnd(text string, start int) int {
	if newline := strings.IndexByte(text[start:], '\n'); newline >= 0 {
		return start + newline + 1
	}
	return len(text)
}

func blockCommentEnd(text string, start int) int {
	depth := 1
	for i := start + 2; i < len(text); {
		if i+1 < len(text) && text[i] == '/' && text[i+1] == '*' {
			depth++
			i += 2
			continue
		}
		if i+1 < len(text) && text[i] == '*' && text[i+1] == '/' {
			depth--
			i += 2
			if depth == 0 {
				return i
			}
			continue
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		i += size
	}
	return len(text)
}

func dollarQuotedLiteralEnd(text string, start int) (int, bool) {
	i := start + 1
	if i >= len(text) {
		return 0, false
	}
	if text[i] != '$' {
		if !isDollarTagStart(text[i]) {
			return 0, false
		}
		i++
		for i < len(text) && isDollarTagContinue(text[i]) {
			i++
		}
		if i >= len(text) || text[i] != '$' {
			return 0, false
		}
	}
	i++
	delimiter := text[start:i]
	if closeAt := strings.Index(text[i:], delimiter); closeAt >= 0 {
		return i + closeAt + len(delimiter), true
	}
	return len(text), true
}

func isDollarTagStart(c byte) bool {
	return isASCIILetter(c) || c == '_'
}

func isDollarTagContinue(c byte) bool {
	return isDollarTagStart(c) || c >= '0' && c <= '9'
}

func backtickRunLength(text string, start int) int {
	end := start
	for end < len(text) && text[end] == '`' {
		end++
	}
	return end - start
}

func singleBacktickSpanEnd(text string, start int) int {
	for i := start + 1; i < len(text); {
		offset := strings.IndexByte(text[i:], '`')
		if offset < 0 {
			return -1
		}
		i += offset
		run := backtickRunLength(text, i)
		if run == 1 {
			return i
		}
		i += run
	}
	return -1
}

func isSimpleBacktickIdentifier(content string) bool {
	if content == "" {
		return false
	}
	for i := range len(content) {
		if !isASCIIIdentifier(content[i]) && content[i] != '.' {
			return false
		}
	}
	return true
}

func isLikelySQLFragment(content string) bool {
	return strings.IndexAny(content, " \t\r\n'=;(),*/#<>!+-|:") >= 0
}

// markdownInlineCodeEnd conservatively keeps a double-backtick inline code span
// anonymous through its matching delimiter. A missing close protects the
// remainder rather than exposing possible code content.
func markdownInlineCodeEnd(text string, start, run int) int {
	for i := start + run; i < len(text); {
		offset := strings.IndexByte(text[i:], '`')
		if offset < 0 {
			return len(text)
		}
		i += offset
		closeRun := backtickRunLength(text, i)
		if closeRun == run {
			return i + closeRun
		}
		i += closeRun
	}
	return len(text)
}
