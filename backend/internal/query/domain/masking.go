// masking.go implements static sensitive-column masking analysis (algorithm ruled in RCP-20260828-FLOW-MASKING).
//
// Goal: close legacy alias-escape bypasses that matched only result column names -- when the vocabulary
// contains A, `A as B`, `concat(A,...) as C`, outer references after CTE renaming `WITH x AS (SELECT A AS C)`,
// and `SELECT *` over renamed FROM subqueries must all continue to be masked.
//
// Decision rules (fail-closed toward over-masking, never under-masking):
//  1. CTE chain propagation: if a CTE output column name matches the vocabulary or its expression references
//     a masked name (including names propagated via upstream CTE renames), mark that column as masked;
//     explicit column lists `WITH x(a,b)` map by position; `WITH RECURSIVE` runs a second propagation pass.
//  2. Each top-level SELECT output item: masked if its output name (alias or column name) hits the union of
//     the vocabulary and marked CTE column names, or if a masked column reference appears within its expression token span.
//     Column reference = bare word or word after a dot, excluding function call names (followed by "("),
//     SQL keywords, qualifier positions (A in A.B), and string/number tokens.
//  3. Star outputs (`*`/`t.*`): SELECT list items do not map 1:1 to runtime result columns when stars expand,
//     so runtime evaluation matches by column name: star expansion does not rename columns (the result name is
//     the underlying column or CTE output name), which is completely covered by MaskedNames.
//  4. UNION/INTERSECT/EXCEPT combine branches via positional OR across SELECT list items.
//
// Evaluation timing: analyzed once per Query Run execution and sealed into the encrypted cursor state,
// then reused across subsequent pages (matching query.timeout's "applies only to new Query Runs" semantics).
package domain

import (
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"golang.org/x/text/cases"
)

// MaskPlan is the output column masking plan for a single Query Run (AEAD-sealed into cursor state;
// contains only booleans and normalized names without raw SQL or vocabulary text).
type MaskPlan struct {
	// ItemCount: number of SELECT list items (equals result column count when no star expansion occurs, enabling direct positional mapping).
	ItemCount int `json:"item_count"`
	// ItemMasked: per-item static masking decision (used on the standard non-star path).
	ItemMasked []bool `json:"item_masked"`
	// Stars: whether each item is a star output (`*`/`t.*`).
	Stars []bool `json:"stars"`
	// ItemNames: output name of each item (empty string for complex unnamed expressions).
	ItemNames []string `json:"item_names"`
	// MaskedNames: runtime name-based masking set = vocabulary U marked CTE output names U output names
	// of marked ordinary items (covering star-expanded columns and duplicate column names).
	MaskedNames []string `json:"masked_names"`
	// UnnamedMasked: true if there is any unnamed expression item marked as masked (fails closed by masking
	// all unmapped columns when star expansion shifts positional alignment).
	UnnamedMasked bool `json:"unnamed_masked"`
}

// keywordSkip lists SQL keywords excluded from masked column reference matching (prevents false positives
// when a vocabulary entry shares a name with a SQL keyword that cannot act as a column reference).
var keywordSkip = map[string]bool{
	"select": true, "from": true, "where": true, "group": true, "order": true,
	"by": true, "having": true, "limit": true, "offset": true, "as": true,
	"and": true, "or": true, "not": true, "in": true, "is": true, "null": true,
	"like": true, "ilike": true, "between": true, "join": true, "on": true,
	"case": true, "when": true, "then": true, "else": true, "end": true,
	"distinct": true, "all": true, "union": true, "intersect": true,
	"except": true, "asc": true, "desc": true, "for": true, "with": true,
	"recursive": true, "cross": true, "inner": true, "left": true,
	"right": true, "outer": true, "full": true, "using": true, "cast": true,
	"interval": true, "exists": true, "any": true, "some": true,
	"true": true, "false": true, "window": true, "filter": true,
	"over": true, "partition": true, "rows": true, "range": true,
	"groups": true, "preceding": true, "following": true, "current": true,
	"unbounded": true, "lateral": true, "materialized": true,
}

// clauseHeads lists top-level clause keywords that terminate a SELECT list.
var clauseHeads = map[string]bool{
	"from": true, "where": true, "group": true, "having": true,
	"order": true, "limit": true, "offset": true, "window": true,
	"for": true, "union": true, "intersect": true, "except": true,
}

// cteInfo holds propagatable masking facts for a single CTE.
type cteInfo struct {
	name    string
	outputs []string // Output column names (empty string when unknown, e.g. star or unparseable item)
	masked  []bool   // Same length as outputs
	body    []fingerprint.Token
}

// AnalyzeMasking computes the MaskPlan for a statement that has already passed CheckSingleSelect.
// vocab is the set of sensitive column names (normalized to lowercase by this analyzer).
func AnalyzeMasking(sql string, postgres bool, vocab []string) (MaskPlan, *Error) {
	profile := fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
	if postgres {
		profile = fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}
	}
	lex, err := fingerprint.Tokenize([]byte(sql), profile.Flags())
	if err != nil || len(lex.Tokens) == 0 {
		return MaskPlan{}, ErrOnlySingleSelect()
	}
	toks := lex.Tokens
	// Normalize vocabulary entries to lowercase/case-fold (per RCP security impact: case-insensitive matching;
	// callers need not pre-normalize as normalization is an invariant responsibility of the analyzer).
	base := toSet(normalizeVocab(vocab))

	// CTE collection: merge WITH chains at any depth into a statement-wide effective set (documented
	// over-masking direction across scopes); run a second pass for RECURSIVE.
	ctes := collectCTEs(toks)
	ctePass(ctes, base, nil)
	maskedNames := cteMaskedNames(ctes)
	ctePass(ctes, base, maskedNames)
	effective := union(base, cteMaskedNames(ctes))

	// Combine UNION branches via positional OR.
	plan := MaskPlan{MaskedNames: normalizeVocab(vocab)}
	for n := range cteMaskedNames(ctes) {
		plan.MaskedNames = append(plan.MaskedNames, n)
	}
	for _, b := range splitBlocks(toks) {
		plan = mergeItemPlan(plan, analyzeBlock(b, effective))
	}
	// Merge output names of marked ordinary items into MaskedNames: when star expansion shifts positional
	// alignment, ` z ` in `select *, concat(a,'x') as z` is still masked by name.
	for i, m := range plan.ItemMasked {
		if m && i < len(plan.ItemNames) && plan.ItemNames[i] != "" {
			plan.MaskedNames = append(plan.MaskedNames, plan.ItemNames[i])
		}
	}
	// Renamed outputs from FROM subqueries (`select * from (select secret as c ...) x`):
	// merge masked names into MaskedNames so star-expanded columns are covered by name.
	for _, b := range splitBlocks(toks) {
		for _, nm := range fromSubqueryMaskedNames(b, effective) {
			plan.MaskedNames = append(plan.MaskedNames, nm)
		}
	}
	return plan, nil
}

// fromSubqueryMaskedNames collects masked output names from derived tables in the block's FROM clause.
func fromSubqueryMaskedNames(block []fingerprint.Token, vocab map[string]bool) []string {
	from := fromSpan(block)
	var out []string
	depth := 0
	start := -1
	for i, t := range from {
		if t.IsPunct("(") {
			if depth == 0 {
				start = i + 1
			}
			depth++
		} else if t.IsPunct(")") {
			depth--
			if depth == 0 && start >= 0 {
				names, masked := analyzeOutputs(from[start:i], vocab)
				for j, m := range masked {
					if m && j < len(names) && names[j] != "" {
						out = append(out, names[j])
					}
				}
				start = -1
			}
		}
	}
	return out
}

// fromSpan extracts the token span of the first top-level FROM clause in the block.
func fromSpan(block []fingerprint.Token) []fingerprint.Token {
	depth := 0
	start, end := -1, len(block)
	for i, t := range block {
		if t.IsPunct("(") {
			depth++
			continue
		}
		if t.IsPunct(")") {
			depth--
			continue
		}
		if depth != 0 || t.Kind != fingerprint.TokWord {
			continue
		}
		if t.Text == "from" && start < 0 {
			start = i + 1
			continue
		}
		if start >= 0 && clauseHeads[t.Text] {
			end = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	return block[start:end]
}

// ColumnMask computes at runtime whether each actual result column must be masked.
// Uses direct positional mapping when no star expansion occurred (len(columns) == ItemCount); otherwise
// evaluates against MaskedNames, failing closed by masking all unmapped columns if UnnamedMasked is set.
func (p MaskPlan) ColumnMask(columnNames []string) []bool {
	nameSet := toSet(p.MaskedNames)
	itemNames := map[string]bool{}
	for _, n := range p.ItemNames {
		if n != "" {
			itemNames[FoldIdentifier(n)] = true
		}
	}
	out := make([]bool, len(columnNames))
	for j, col := range columnNames {
		lower := col
		if m := lookupLower(col); m != "" {
			lower = m
		}
		if nameSet[lower] {
			out[j] = true
			continue
		}
		switch {
		case len(columnNames) == p.ItemCount:
			out[j] = j < len(p.ItemMasked) && p.ItemMasked[j]
		case p.UnnamedMasked && !itemNames[FoldIdentifier(col)]:
			out[j] = true
		}
	}
	return out
}

var identifierCaseFolder = cases.Fold()

// FoldIdentifier maps an identifier to a stable key using Unicode default full case folding;
// covers non-ASCII equivalence classes such as Σ/σ/ς, K/k/K, and ß/ss.
func FoldIdentifier(s string) string {
	return identifierCaseFolder.String(s)
}

// lookupLower provides internal analyzer compatibility by delegating uniformly to full Unicode case folding.
func lookupLower(s string) string {
	return FoldIdentifier(s)
}

// mergeItemPlan ORs a single branch's item-level plan into the aggregate plan (the first branch establishes the shape).
func mergeItemPlan(plan MaskPlan, blk itemPlan) MaskPlan {
	if plan.ItemCount == 0 {
		plan.ItemCount = len(blk.masked)
		plan.ItemMasked = blk.masked
		plan.Stars = blk.stars
		plan.ItemNames = blk.names
		plan.UnnamedMasked = blk.unnamedMasked
		return plan
	}
	n := plan.ItemCount
	if len(blk.masked) > n {
		n = len(blk.masked)
	}
	grow := func(b []bool) []bool {
		out := make([]bool, n)
		copy(out, b)
		return out
	}
	im := grow(plan.ItemMasked)
	st := grow(plan.Stars)
	nm := make([]string, n)
	copy(nm, plan.ItemNames)
	for i := 0; i < n && i < len(blk.masked); i++ {
		im[i] = im[i] || blk.masked[i]
		st[i] = st[i] || blk.stars[i]
		if nm[i] == "" {
			nm[i] = blk.names[i]
		}
	}
	plan.ItemCount, plan.ItemMasked, plan.Stars, plan.ItemNames = n, im, st, nm
	plan.UnnamedMasked = plan.UnnamedMasked || blk.unnamedMasked
	return plan
}

// itemPlan holds the item-level analysis result for a single SELECT block.
type itemPlan struct {
	masked        []bool
	stars         []bool
	names         []string
	unnamedMasked bool
}

// ---- CTE collection and propagation ----

// collectCTEs scans all WITH chains and extracts CTE definitions (name + body token slice).
func collectCTEs(toks []fingerprint.Token) []*cteInfo {
	var out []*cteInfo
	for i, t := range toks {
		if t.Kind != fingerprint.TokWord || t.Text != "with" {
			continue
		}
		pos := i + 1
		if tokenWord(toks, pos) == "recursive" {
			pos++
		}
		for {
			name := tokenWord(toks, pos)
			if name == "" {
				break
			}
			pos++
			var explicit []string
			if pos < len(toks) && toks[pos].IsPunct("(") {
				end, ok := matchParen(toks, pos)
				if !ok {
					break
				}
				explicit = parenWords(toks, pos+1, end)
				pos = end + 1
			}
			if tokenWord(toks, pos) != "as" {
				break
			}
			pos++
			if tokenWord(toks, pos) == "not" {
				pos++
			}
			if tokenWord(toks, pos) == "materialized" {
				pos++
			}
			if pos >= len(toks) || !toks[pos].IsPunct("(") {
				break
			}
			end, ok := matchParen(toks, pos)
			if !ok {
				break
			}
			out = append(out, &cteInfo{
				name: name, outputs: explicit, body: toks[pos+1 : end],
			})
			pos = end + 1
			if pos < len(toks) && toks[pos].IsPunct(",") {
				pos++
				continue
			}
			break
		}
	}
	return out
}

// ctePass computes output masks for each CTE against the current effective vocabulary (visible in declaration
// order; a second pass covers recursive references with the full set).
func ctePass(ctes []*cteInfo, base, extra map[string]bool) {
	effective := base
	if extra != nil {
		effective = union(base, extra)
	}
	for idx, c := range ctes {
		names, masked := analyzeOutputs(c.body, effective)
		if len(c.outputs) > 0 {
			// Explicit column list: map body outputs by position.
			exp := make([]bool, len(c.outputs))
			for i := range c.outputs {
				if i < len(masked) {
					exp[i] = masked[i]
				}
			}
			c.masked = exp
		} else {
			c.outputs, c.masked = names, masked
		}
		// Subsequent CTEs can see all previously derived CTE results.
		effective = union(effective, cteMaskedNames(ctes[:idx+1]))
	}
}

// cteMaskedNames returns the set of CTE output column names marked as masked.
func cteMaskedNames(ctes []*cteInfo) map[string]bool {
	out := map[string]bool{}
	for _, c := range ctes {
		for i, n := range c.outputs {
			if n != "" && i < len(c.masked) && c.masked[i] {
				out[FoldIdentifier(n)] = true
			}
		}
	}
	return out
}

// ---- Block and SELECT list analysis ----

// splitBlocks splits tokens by top-level UNION/INTERSECT/EXCEPT (ignoring parenthesized subqueries
// and consuming ALL in UNION ALL so it does not leak into the next block).
func splitBlocks(toks []fingerprint.Token) [][]fingerprint.Token {
	blocks := [][]fingerprint.Token{}
	start := 0
	depth := 0
	for i, t := range toks {
		if t.IsPunct("(") {
			depth++
		} else if t.IsPunct(")") {
			depth--
		} else if depth == 0 && t.Kind == fingerprint.TokWord {
			switch t.Text {
			case "union", "intersect", "except":
				blocks = append(blocks, toks[start:i])
				start = i + 1
				if start < len(toks) && toks[start].IsWord("all") {
					start++
				}
			}
		}
	}
	blocks = append(blocks, toks[start:])
	return blocks
}

// selectListSpan locates the top-level SELECT list token span within a block (skipping any WITH prefix
// and DISTINCT/ALL modifiers, returning the SELECT list token slice).
func selectListSpan(toks []fingerprint.Token) []fingerprint.Token {
	pos := 0
	if tokenWord(toks, 0) == "with" {
		pos = skipWITHPrefix(toks)
	}
	if tokenWord(toks, pos) != "select" {
		return nil
	}
	pos++
	if w := tokenWord(toks, pos); w == "distinct" || w == "all" {
		pos++
	}
	listEnd := len(toks)
	depth := 0
	for i := pos; i < len(toks); i++ {
		t := toks[i]
		if t.IsPunct("(") {
			depth++
		} else if t.IsPunct(")") {
			depth--
		} else if depth == 0 && t.Kind == fingerprint.TokWord && clauseHeads[t.Text] {
			listEnd = i
			break
		}
	}
	return toks[pos:listEnd]
}

// analyzeBlock analyzes a single SELECT block.
func analyzeBlock(block []fingerprint.Token, vocab map[string]bool) itemPlan {
	list := selectListSpan(block)
	if list == nil {
		return itemPlan{}
	}
	out := itemPlan{}
	for _, item := range splitTopLevel(list) {
		name, masked, star := analyzeItem(item, vocab)
		out.names = append(out.names, name)
		out.masked = append(out.masked, masked)
		out.stars = append(out.stars, star)
		if masked && name == "" {
			out.unnamedMasked = true
		}
	}
	return out
}

// analyzeOutputs analyzes CTE body or subquery outputs: returns output names and mask flags (without star expansion differences).
func analyzeOutputs(body []fingerprint.Token, vocab map[string]bool) (names []string, masked []bool) {
	list := selectListSpan(body)
	if list == nil {
		return nil, nil
	}
	for _, item := range splitTopLevel(list) {
		name := outputName(item)
		names = append(names, name)
		masked = append(masked, (name != "" && vocab[FoldIdentifier(name)]) || containsMaskedRef(item, vocab))
	}
	return names, masked
}

// analyzeItem evaluates a single SELECT list item: returns its output name, mask flag, and star flag.
func analyzeItem(item []fingerprint.Token, vocab map[string]bool) (name string, masked, star bool) {
	if len(item) == 0 {
		return "", false, false
	}
	if item[len(item)-1].IsPunct("*") {
		if len(item) == 1 || (len(item) >= 3 && item[len(item)-2].IsPunct(".")) {
			return "", false, true
		}
	}
	name = outputName(item)
	if name != "" && vocab[lookupLower(name)] {
		return name, true, false
	}
	return name, containsMaskedRef(item, vocab), false
}

// outputName extracts the output column name: word after top-level AS > trailing word of a qualified chain > trailing implicit alias
// (accepting both TokWord and TokQuotedIdent as identifiers).
func outputName(item []fingerprint.Token) string {
	isIdent := func(t fingerprint.Token) bool {
		return t.Kind == fingerprint.TokWord || t.Kind == fingerprint.TokQuotedIdent
	}
	depth := 0
	for i, t := range item {
		if t.IsPunct("(") {
			depth++
		} else if t.IsPunct(")") {
			depth--
		} else if depth == 0 && t.IsWord("as") {
			if i+1 < len(item) && isIdent(item[i+1]) {
				return item[i+1].Text
			}
			return ""
		}
	}
	last := item[len(item)-1]
	if !isIdent(last) {
		return ""
	}
	if len(item) == 1 {
		return last.Text
	}
	prev := item[len(item)-2]
	switch {
	case prev.IsPunct("."):
		return last.Text // Qualified column reference: the column name itself is the output name
	case isIdent(prev) || prev.Kind == fingerprint.TokNumber ||
		prev.Kind == fingerprint.TokString || prev.IsPunct(")") || prev.IsPunct("*"):
		return last.Text // Implicit alias
	default:
		return ""
	}
}

// containsMaskedRef reports whether an expression references any masked column (bare word or word after a dot;
// excludes function call names, qualifier positions, SQL keywords, and non-word tokens; quoted identifiers
// like `secret` or "SECRET" are matched after unquoting and case-folding).
func containsMaskedRef(item []fingerprint.Token, vocab map[string]bool) bool {
	for i, t := range item {
		if t.Kind != fingerprint.TokWord && t.Kind != fingerprint.TokQuotedIdent {
			continue
		}
		if !vocab[lookupLower(t.Text)] {
			continue
		}
		if keywordSkip[lookupLower(t.Text)] {
			continue
		}
		if i+1 < len(item) {
			n := item[i+1]
			if n.IsPunct("(") || n.IsPunct(".") {
				continue // Function name or qualifier position
			}
		}
		return true
	}
	return false
}

// ---- Helpers ----

func skipWITHPrefix(toks []fingerprint.Token) int {
	pos := 1
	if tokenWord(toks, pos) == "recursive" {
		pos++
	}
	for {
		if tokenWord(toks, pos) == "" {
			return pos
		}
		pos++
		if pos < len(toks) && toks[pos].IsPunct("(") {
			end, ok := matchParen(toks, pos)
			if !ok {
				return len(toks)
			}
			pos = end + 1
		}
		if tokenWord(toks, pos) != "as" {
			return pos
		}
		pos++
		if tokenWord(toks, pos) == "not" {
			pos++
		}
		if tokenWord(toks, pos) == "materialized" {
			pos++
		}
		if pos >= len(toks) || !toks[pos].IsPunct("(") {
			return pos
		}
		end, ok := matchParen(toks, pos)
		if !ok {
			return len(toks)
		}
		pos = end + 1
		if pos < len(toks) && toks[pos].IsPunct(",") {
			pos++
			continue
		}
		return pos
	}
}

// parenWords extracts a top-level comma-separated sequence of identifier words inside parentheses (for explicit CTE column lists).
func parenWords(toks []fingerprint.Token, start, end int) []string {
	var out []string
	for i := start; i < end; i++ {
		if toks[i].Kind == fingerprint.TokWord {
			out = append(out, toks[i].Text)
		}
	}
	return out
}

// splitTopLevel splits a token slice by top-level commas.
func splitTopLevel(toks []fingerprint.Token) [][]fingerprint.Token {
	var out [][]fingerprint.Token
	start := 0
	depth := 0
	for i, t := range toks {
		if t.IsPunct("(") {
			depth++
		} else if t.IsPunct(")") {
			depth--
		} else if depth == 0 && t.IsPunct(",") {
			out = append(out, toks[start:i])
			start = i + 1
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}

func toSet(words []string) map[string]bool {
	out := map[string]bool{}
	for _, w := range words {
		if w != "" {
			out[FoldIdentifier(w)] = true
		}
	}
	return out
}

// normalizeVocab normalizes vocabulary entries into Unicode case-folded keys (sharing the same domain as token words and column names).
func normalizeVocab(words []string) []string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		out = append(out, lookupLower(w))
	}
	return out
}

func union(a, b map[string]bool) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}
