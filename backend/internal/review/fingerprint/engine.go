package fingerprint

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// HMACPurpose matches cryptographic-key-contract hmac.purposes;
// isolation from AEAD/password-hashing keys is guaranteed by the keyring.
const HMACPurpose = "sql_fingerprint"

// QualityExact / QualityLexer are the only quality values permitted by the contract.
const (
	QualityExact = "exact"
	QualityLexer = "compatible_lexer"
)

// Error names (error-codes.json 2002-2006 domain).
const (
	ErrNameBatchTooLarge  = "SQL_BATCH_TOO_LARGE"
	ErrNameParseFailed    = "SQL_PARSE_FAILED"
	ErrNameFingerFailed   = "SQL_FINGERPRINT_FAILED"
	ErrNameAnonFailed     = "SQL_ANONYMIZATION_FAILED"
	ErrNameTooManyFprints = "TOO_MANY_FINGERPRINTS"
)

// Failure is a structured fingerprint failure (fail closed); Detail must never contain raw SQL or literals.
type Failure struct {
	Code      string
	Detail    string
	Offset    int64
	HasOffset bool
	Statement int
}

func (f *Failure) Error() string {
	return fmt.Sprintf("fingerprint: %s: %s", f.Code, f.Detail)
}

// Signer provides fingerprint HMAC and KeyID.
// The production implementation injects crypto.Crypter from the composition root (see CryptoSigner).
type Signer interface {
	KeyID() string
	HMACSHA256(data []byte) ([]byte, error)
}

// CrypterHMAC is the minimal interface satisfied by *crypto.Crypter.
type CrypterHMAC interface {
	HMAC(purpose string, data []byte) ([]byte, error)
	HMACKeyID(purpose string) (string, error)
}

// CryptoSigner adapts crypto.Crypter to Signer; purpose is fixed to sql_fingerprint.
type CryptoSigner struct{ C CrypterHMAC }

func (s CryptoSigner) KeyID() string {
	id, err := s.C.HMACKeyID(HMACPurpose)
	if err != nil {
		return ""
	}
	return id
}

func (s CryptoSigner) HMACSHA256(data []byte) ([]byte, error) {
	return s.C.HMAC(HMACPurpose, data)
}

// DefaultStatementBytes matches contract max_statement_bytes.
const DefaultStatementBytes = 524288

// Options are engine construction options.
type Options struct {
	MaxStatementBytes int // 0 uses DefaultStatementBytes
}

// Result is the complete contract output for a single statement.
type Result struct {
	AlgorithmVersion         string
	DialectProfile           string
	StatementKind            string
	FingerprintKeyID         string
	CanonicalFingerprintHMAC string
	DialectFingerprintHMAC   string
	ExecutionShapeHMAC       string
	AnonymizedSQL            string
	Objects                  []ObjectReference
	MetadataContextComplete  bool
	Validity                 StatementValidity
	LiteralTypeCounts        map[string]int
	TupleCount               int
	ParameterCount           int
	Quality                  string
	OptimizerHints           []string
	DialectFeatures          []string
	InListSizes              []int
	ByteLen                  int
	Warnings                 []string
}

// Engine executes lexing -> classification -> normalization -> dual fingerprinting -> anonymization for a single statement.
type Engine struct {
	signer Signer
	opts   Options
}

// NewEngine constructs an engine; fails closed if signer is nil.
func NewEngine(signer Signer, opts Options) (*Engine, error) {
	if signer == nil {
		return nil, &Failure{Code: ErrNameFingerFailed, Detail: "nil signer"}
	}
	if signer.KeyID() == "" {
		return nil, &Failure{Code: ErrNameFingerFailed, Detail: "signer key id unavailable"}
	}
	if opts.MaxStatementBytes <= 0 {
		opts.MaxStatementBytes = DefaultStatementBytes
	}
	return &Engine{signer: signer, opts: opts}, nil
}

// Analyze analyzes a single statement (without trailing semicolon).
// mapper provides Run-scoped stable identifier mapping; when nil, an isolated temporary mapper is used.
func (e *Engine) Analyze(profile Profile, sql []byte, mapper *privacy.RunMapper) (Result, error) {
	if err := profile.Validate(); err != nil {
		return Result{}, &Failure{Code: ErrNameParseFailed, Detail: "dialect_profile_unknown"}
	}
	if len(sql) > e.opts.MaxStatementBytes {
		return Result{}, &Failure{Code: ErrNameBatchTooLarge, Detail: fmt.Sprintf("statement %d bytes exceeds %d", len(sql), e.opts.MaxStatementBytes)}
	}
	if mapper == nil {
		mapper = privacy.NewRunMapper()
	}
	lex, err := Tokenize(sql, profile.Flags())
	if err != nil {
		return Result{}, &Failure{Code: ErrNameParseFailed, Detail: "lexical proof failed: " + err.Error()}
	}
	if len(lex.Tokens) == 0 {
		return Result{}, &Failure{Code: ErrNameParseFailed, Detail: "empty statement"}
	}

	res := Result{
		AlgorithmVersion:  AlgorithmVersion,
		DialectProfile:    profile.String(),
		FingerprintKeyID:  e.signer.KeyID(),
		ByteLen:           len(sql),
		LiteralTypeCounts: map[string]int{},
		OptimizerHints:    dedupSorted(lex.Hints),
		DialectFeatures:   dedupSorted(lex.Features),
		Warnings:          []string{},
	}

	walk := walkStatement(lex.Tokens)
	kind := walk.kind
	if walk.fail || kind == "" {
		kind = fallbackKind(lex.Tokens)
		res.Quality = QualityLexer
		res.Warnings = append(res.Warnings, "role_walk_incomplete")
		res.DialectFeatures = dedupSorted(append(lex.Features, walk.features...))
		// Fallback mode does not use partial walker statistics: statement structure is unproven, so cardinalities are untrusted.
		res.InListSizes = []int{}
		res.TupleCount = 0
	} else {
		res.Quality = QualityExact
		res.DialectFeatures = dedupSorted(append(lex.Features, walk.features...))
	}
	res.StatementKind = kind

	st := newRenderState(lex.Tokens, walk)
	var canonical []string
	if res.Quality == QualityExact {
		canonical = st.CanonicalSequence()
		for _, s := range walk.specials {
			if s.kind == specialValuesTuples {
				res.TupleCount += s.count
			}
		}
		res.InListSizes = walk.inSizes
	} else {
		canonical = FallbackCanonicalSequence(lex)
	}
	res.ParameterCount, res.LiteralTypeCounts = countParams(lex.Tokens)

	canonHMAC, err := e.sign(AlgorithmVersion + "|canon|" + kind + "|" + strings.Join(canonical, "|"))
	if err != nil {
		return Result{}, &Failure{Code: ErrNameFingerFailed, Detail: "canonical hmac failed"}
	}
	res.CanonicalFingerprintHMAC = canonHMAC

	var dialectSeq string
	if res.Quality == QualityExact {
		dialectSeq = st.DialectSequence(profile, kind, res.DialectFeatures, res.OptimizerHints)
	} else {
		dialectSeq = fallbackDialectSequence(profile, kind, canonical, res.DialectFeatures, res.OptimizerHints)
	}
	dialectHMAC, err := e.sign(dialectSeq)
	if err != nil {
		return Result{}, &Failure{Code: ErrNameFingerFailed, Detail: "dialect hmac failed"}
	}
	res.DialectFingerprintHMAC = dialectHMAC

	var shapeSeq string
	if res.Quality == QualityExact {
		shapeSeq = st.ExecutionShapeSequence(kind, res.ParameterCount, res.TupleCount, res.InListSizes)
	} else {
		shapeSeq = AlgorithmVersion + "|kind=" + kind + "|lexer|" + strings.Join(canonical, "|")
	}
	shapeHMAC, err := e.sign(shapeSeq)
	if err != nil {
		return Result{}, &Failure{Code: ErrNameFingerFailed, Detail: "execution shape hmac failed"}
	}
	res.ExecutionShapeHMAC = shapeHMAC

	var anonymized string
	var objects []ObjectReference
	if res.Quality == QualityExact {
		anonymized, objects, err = RenderAnonymizedDetailed(lex.Tokens, walk, mapper, profile)
		res.MetadataContextComplete = !walk.metadataIncomplete
		if err == nil {
			res.Validity, err = extractStatementValidity(lex.Tokens, walk, mapper, profile.Engine)
		}
	} else {
		anonymized, err = RenderFallbackAnonymized(lex, mapper)
		res.MetadataContextComplete = false
		objects = []ObjectReference{}
		res.Validity = StatementValidity{Unsupported: "parser_fallback"}
	}
	if err != nil {
		return Result{}, &Failure{Code: ErrNameAnonFailed, Detail: "anonymization mapping failed (fail closed)"}
	}
	res.AnonymizedSQL = anonymized
	res.Objects = objects

	// Leak assertion: forbidden set = role names + literals + comment content; fails closed on any match.
	if err := privacy.AssertNoLeak(anonymized, forbiddenSet(lex, walk)); err != nil {
		return Result{}, &Failure{Code: ErrNameAnonFailed, Detail: "leak assertion failed"}
	}
	return res, nil
}

func (e *Engine) sign(seq string) (string, error) {
	mac, err := e.signer.HMACSHA256([]byte(seq))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(mac), nil
}

// countParams counts total parameters and literal type occurrences.
func countParams(toks []Token) (int, map[string]int) {
	counts := map[string]int{}
	total := 0
	for _, t := range toks {
		if lt, ok := LiteralTypeOf(t); ok {
			counts[string(lt)]++
			total++
		}
	}
	return total, counts
}

// forbiddenSet collects the leak forbidden set: identifier role names + literal contents + full comment text.
func forbiddenSet(lex LexOutput, walk walkResult) []privacy.Forbidden {
	var out []privacy.Forbidden
	seen := map[string]struct{}{}
	add := func(val, what string) {
		if val == "" {
			return
		}
		key := what + "\x00" + strings.ToLower(val)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, privacy.Forbidden{Value: val, What: what})
	}
	for i, r := range walk.roles {
		if r.kind != "" && i < len(lex.Tokens) {
			add(r.name, "identifier:"+string(r.kind))
		}
	}
	st := newRenderState(lex.Tokens, walk)
	for i, t := range lex.Tokens {
		if i < len(walk.roles) && walk.roles[i].kind != "" {
			continue
		}
		switch t.Kind {
		case TokWord:
			if _, isLit := ClassifyWordLiteral(t.Text); isLit ||
				isReserved(t.Text) ||
				st.keepRaw[i] ||
				st.isControlledFunctionPosition(i) {
				continue
			}
			// Any remaining word is on the generic/anonymized path. Include it
			// in the leak assertion so a future renderer regression fails closed.
			add(t.Text, "metadata:word")
		case TokQuotedIdent:
			add(t.Text, "metadata:quoted_identifier")
		}
	}
	for _, t := range lex.Tokens {
		switch t.Kind {
		case TokString, TokDollarString:
			if t.Text != "" {
				add(t.Text, "literal:string")
			}
		case TokNumber:
			add(t.Text, "literal:number")
		case TokWord:
			// Word literals (null/true/false) also enter the forbidden set: upon a rendering regression,
			// AssertNoLeak must catch raw leakage of these literals.
			if _, isLit := ClassifyWordLiteral(t.Text); isLit {
				add(t.Text, "literal:word")
			}
		}
	}
	for _, c := range lex.Comments {
		add(c, "comment")
	}
	return out
}

// fallbackKind infers the statement category from the first word; returns "other" if unknown.
func fallbackKind(toks []Token) string {
	for _, t := range toks {
		if t.Kind == TokWord {
			if _, ok := statementKindHeads[t.Text]; ok {
				return t.Text
			}
			return "other"
		}
		if t.Kind == TokQuotedIdent || t.Kind == TokPunct {
			continue
		}
		return "other"
	}
	return "other"
}

// fallbackDialectSequence builds the fallback dialect sequence (structure + profile + features + Hint).
func fallbackDialectSequence(profile Profile, kind string, canonical []string, features, hints []string) string {
	var b strings.Builder
	b.WriteString(AlgorithmVersion)
	b.WriteString("|lexer|profile=")
	b.WriteString(profile.String())
	b.WriteString("|kind=")
	b.WriteString(kind)
	for _, e := range canonical {
		b.WriteString("|")
		b.WriteString(e)
	}
	fs := append([]string{}, features...)
	sort.Strings(fs)
	b.WriteString("|features=")
	b.WriteString(strings.Join(fs, ","))
	hs := append([]string{}, hints...)
	sort.Strings(hs)
	b.WriteString("|hints=")
	b.WriteString(strings.Join(hs, ","))
	return b.String()
}

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
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
