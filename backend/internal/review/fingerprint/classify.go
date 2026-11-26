package fingerprint

import (
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// idRole records the identifier role of a token.
type idRole struct {
	kind      privacy.IdentifierKind
	name      string // original name (used by mapper and leak forbidden set)
	relation  Relation
	quoted    bool
	namespace string
	scopeID   int
	hasScope  bool
	// canonicalKind/Name freeze the fingerprint identity when the role is first classified. Object graph
	// resolution may resolve a provisional table qualifier to an alias when the SELECT list precedes FROM,
	// but must never alter the already-frozen Canonical/Dialect/Shape fingerprints.
	canonicalKind privacy.IdentifierKind
	canonicalName string
	canonicalWord bool // Preserve legacy v1 unclassified-word fingerprint events.
	// qualifier indicates the role is provisionally classified as a qualifier; the SELECT list appears before FROM,
	// so its alias/table binding is resolved after the full statement is parsed.
	qualifier    bool
	qualifierPos int
	table        tableIdentity
	parent       tableIdentity
	// parentResolved freezes the relation scope that owned this role. Nested
	// SELECTs must not fill an unresolved inner column from an outer table
	// after their own scope has ended.
	parentResolved bool
}

// selectScope holds visible relations for a single SELECT query block. Because the SELECT list appears
// before FROM, qualifiers must be bound after the scope is fully parsed; nested queries check the local
// scope first and only fall back to outer scopes in SQL name-resolution order on a miss.
type selectScope struct {
	parent           int
	aliases          map[string]tableIdentity
	tables           map[string]tableIdentity
	ambiguousAliases map[string]bool
	ambiguousTables  map[string]bool
	visible          []tableIdentity
	primary          *tableIdentity
}

type identifierIdentity struct {
	name   string
	quoted bool
}

type tableIdentity struct {
	name      string
	quoted    bool
	namespace string
	schema    identifierIdentity
}

// specialSpan is a structural collapse instruction recorded by the walker.
type specialKind uint8

const (
	specialInList       specialKind = iota // IN (literal list) collapse
	specialValuesTuples                    // VALUES multi-tuple collapse
	specialLimitComma                      // MySQL LIMIT off,cnt -> canonicalized as limit cnt offset off
)

type specialSpan struct {
	kind     specialKind
	tokStart int // apply collapse within token interval [start,end)
	tokEnd   int
	count    int // element/tuple count
}

// walkResult is the role classification result.
type walkResult struct {
	roles              []idRole // same length as tokens; zero value indicates a non-identifier token
	keepRaw            []bool   // controlled word tokens whose surface text is explicitly preserved by the walker
	kind               string   // statement category
	features           []string // dialect feature markers (included in dialect fingerprint)
	specials           []specialSpan
	inSizes            []int // element count of each IN list (included in execution shape bucket)
	metadataIncomplete bool
	fail               bool
}

// statementKindHeads are the supported statement heads; any other head -> fail -> compatible_lexer.
var statementKindHeads = map[string]string{
	"select": "select", "insert": "insert", "update": "update", "delete": "delete",
	"replace": "replace", "merge": "merge", "create": "create", "alter": "alter",
	"drop": "drop", "truncate": "truncate", "rename": "rename", "with": "with",
}

// walker is a token-cursor-based clause-level classifier.
type walker struct {
	toks      []Token
	res       walkResult
	pos       int
	depth     int // subquery depth guard
	maxDepth  int // subquery depth limit
	castDepth int // CAST/CONVERT argument depth (type vs alias after AS)
	aliases   map[string]tableIdentity
	tables    map[string]tableIdentity
	ctes      map[string]tableIdentity
	visible   []tableIdentity
	primary   *tableIdentity
	lastTable *tableIdentity

	selectScopes []selectScope
	scopeStack   []int

	tableRelation  Relation
	columnRelation Relation
}

// parseWithCTEs parses a WITH chain and returns the main statement category.
func (w *walker) parseWithCTEs() string {
	w.pos++ // with
	recursive := false
	if w.atWord("recursive") {
		recursive = true
		w.pos++
	}
	for {
		if !w.isIdentLike() {
			return ""
		}
		namePos := w.pos
		cte := tableIdentity{name: mustName(*w.cur()), quoted: w.cur().Kind == TokQuotedIdent, namespace: "cte"}
		w.markRole(privacy.KindCTE, cte.name)
		w.res.roles[namePos].relation = RelationDefine
		w.res.roles[namePos].namespace = "cte"
		w.res.roles[namePos].table = cte
		w.res.roles[namePos].canonicalKind = privacy.KindTable
		cteKey := identityBindingKey(cte.name, cte.quoted)
		// A non-recursive CTE body resolves its own name as a physical table.
		// Only a WITH RECURSIVE body may resolve the current CTE to itself.
		if recursive {
			w.ctes[cteKey] = cte
		}
		w.pos++
		if w.atPunct("(") {
			columnStart := w.pos
			if !w.parseColumnListParen() {
				return ""
			}
			for i := columnStart; i < w.pos; i++ {
				if w.res.roles[i].kind == privacy.KindColumn {
					w.res.roles[i].parent = cte
					w.res.roles[i].parentResolved = true
				}
			}
		}
		if !w.atWord("as") {
			return ""
		}
		w.pos++
		if w.atWord("not") {
			w.pos++
		}
		if w.atWord("materialized") {
			w.pos++
		}
		if !w.atPunct("(") {
			return ""
		}
		if !w.parseParenSubquery() {
			return ""
		}
		// The CTE becomes visible after its body, so following CTEs and the
		// outer statement can reference it without changing body semantics.
		w.ctes[cteKey] = cte
		if w.atPunct(",") {
			w.pos++
			continue
		}
		break
	}
	t := w.cur()
	if t == nil || t.Kind != TokWord {
		return ""
	}
	kind, ok := statementKindHeads[t.Text]
	if !ok || kind == "with" {
		return ""
	}
	w.pos++
	return kind
}

type identifierPart struct {
	name   string
	quoted bool
	pos    int
}

func (w *walker) lookupAlias(part identifierPart) (tableIdentity, bool) {
	table, ok := w.aliases[identityBindingKey(part.name, part.quoted)]
	return table, ok
}

func (w *walker) lookupTable(part identifierPart) (tableIdentity, bool) {
	table, ok := w.tables[identityBindingKey(part.name, part.quoted)]
	return table, ok
}
