// classify_refs.go holds classify domain helpers and operations.
package fingerprint

import (
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"strings"
)

// walkStatement performs role classification on a single-statement token stream.
// Any token that cannot be placed sets fail (caller falls back to compatible_lexer); roles are never guessed.
func walkStatement(toks []Token) walkResult {
	w := &walker{
		toks:           toks,
		maxDepth:       32,
		aliases:        map[string]tableIdentity{},
		tables:         map[string]tableIdentity{},
		ctes:           map[string]tableIdentity{},
		tableRelation:  RelationReference,
		columnRelation: RelationReference,
	}
	w.res.roles = make([]idRole, len(toks))
	w.res.keepRaw = make([]bool, len(toks))
	w.parseStatement()
	if w.pos < len(w.toks) {
		w.fail()
	}
	if !w.res.fail {
		w.resolveRoles()
	}
	return w.res
}

// peekWord returns the word text at offset (or empty string if not a word/quoted-ident).
func (w *walker) peekWord(offset int) string {
	i := w.pos + offset
	if i >= len(w.toks) {
		return ""
	}
	t := &w.toks[i]
	if t.Kind != TokWord {
		return ""
	}
	return t.Text
}

// skipIfNotExists consumes IF NOT EXISTS; returns true on match.
func (w *walker) skipIfNotExists() bool {
	if w.peekWord(0) == "if" && w.peekWord(1) == "not" && w.peekWord(2) == "exists" {
		w.pos += 3
		return true
	}
	return false
}

// skipIfExists consumes IF EXISTS; returns true on match.
func (w *walker) skipIfExists() bool {
	if w.peekWord(0) == "if" && w.peekWord(1) == "exists" {
		w.pos += 2
		return true
	}
	return false
}

func (w *walker) fail() { w.res.fail = true }

// markMetadataIncomplete records that model-visible metadata cannot be
// proven safe or complete even though the statement grammar was parsed.
func (w *walker) markMetadataIncomplete() {
	w.res.metadataIncomplete = true
}

func (w *walker) cur() *Token {
	if w.pos >= len(w.toks) {
		return nil
	}
	return &w.toks[w.pos]
}

func (w *walker) atWord(ws ...string) bool {
	t := w.cur()
	if t == nil || t.Kind != TokWord {
		return false
	}
	for _, x := range ws {
		if t.Text == x {
			return true
		}
	}
	return false
}

func (w *walker) atPunct(p string) bool {
	t := w.cur()
	return t != nil && t.IsPunct(p)
}

func (w *walker) eof() bool { return w.pos >= len(w.toks) }

// markRole records an identifier role for the token at the current position.
func (w *walker) markRole(kind privacy.IdentifierKind, name string) bool {
	if w.pos >= len(w.toks) {
		return false
	}
	role := idRole{
		kind:          kind,
		name:          name,
		relation:      RelationReference,
		quoted:        w.toks[w.pos].Kind == TokQuotedIdent,
		canonicalKind: kind,
		canonicalName: name,
	}
	if scopeID, ok := w.currentSelectScope(); ok {
		role.scopeID = scopeID
		role.hasScope = true
	}
	switch kind {
	case privacy.KindTable, privacy.KindCTE:
		role.relation = w.tableRelation
	case privacy.KindColumn:
		role.relation = w.columnRelation
	}
	w.res.roles[w.pos] = role
	return true
}

func (w *walker) markRoleWithRelation(kind privacy.IdentifierKind, name string, relation Relation) bool {
	pos := w.pos
	if !w.markRole(kind, name) {
		return false
	}
	w.res.roles[pos].relation = relation
	return true
}

func (w *walker) markRoleAt(pos int, kind privacy.IdentifierKind, name string) bool {
	old := w.pos
	w.pos = pos
	ok := w.markRole(kind, name)
	w.pos = old
	return ok
}

func (w *walker) setRelations(table, column Relation) {
	w.tableRelation = table
	w.columnRelation = column
}

func (w *walker) withRelations(table, column Relation, fn func()) {
	oldTable, oldColumn := w.tableRelation, w.columnRelation
	w.setRelations(table, column)
	defer func() { w.setRelations(oldTable, oldColumn) }()
	fn()
}

func (w *walker) currentSelectScope() (int, bool) {
	if len(w.scopeStack) == 0 {
		return 0, false
	}
	return w.scopeStack[len(w.scopeStack)-1], true
}

func (w *walker) pushSelectScope(parent int) int {
	id := len(w.selectScopes)
	w.selectScopes = append(w.selectScopes, selectScope{
		parent:           parent,
		aliases:          map[string]tableIdentity{},
		tables:           map[string]tableIdentity{},
		ambiguousAliases: map[string]bool{},
		ambiguousTables:  map[string]bool{},
	})
	w.scopeStack = append(w.scopeStack, id)
	return id
}

func (w *walker) popSelectScope(id int) {
	if len(w.scopeStack) == 0 || w.scopeStack[len(w.scopeStack)-1] != id {
		w.fail()
		return
	}
	w.scopeStack = w.scopeStack[:len(w.scopeStack)-1]
}

func registerScopeIdentity(
	bindings map[string]tableIdentity,
	ambiguous map[string]bool,
	key string,
	table tableIdentity,
) {
	if _, exists := bindings[key]; exists {
		ambiguous[key] = true
		return
	}
	bindings[key] = table
}

func (w *walker) registerSelectBinding(
	aliases bool,
	key string,
	table tableIdentity,
) {
	scopeID, ok := w.currentSelectScope()
	if !ok {
		return
	}
	scope := &w.selectScopes[scopeID]
	if aliases {
		registerScopeIdentity(scope.aliases, scope.ambiguousAliases, key, table)
	} else {
		registerScopeIdentity(scope.tables, scope.ambiguousTables, key, table)
	}
}

// wordTokName returns the original name of a word/quoted-ident token.
func wordTokName(t Token) (string, bool) {
	switch t.Kind {
	case TokWord:
		return t.Literal, true
	case TokQuotedIdent:
		return t.Text, true
	}
	return "", false
}

func (w *walker) isIdentLike() bool {
	t := w.cur()
	if t == nil {
		return false
	}
	_, ok := wordTokName(*t)
	return ok
}

// ---- Statement level ----

func (w *walker) parseStatement() {
	w.skipIf("explain", "analyze", "verbose")
	t := w.cur()
	if t == nil || t.Kind != TokWord {
		w.fail()
		return
	}
	head, ok := statementKindHeads[t.Text]
	if !ok {
		w.fail()
		return
	}
	kind := head
	if head == "with" {
		kind = w.parseWithCTEs()
		if w.res.fail {
			return
		}
		if kind == "" {
			w.fail()
			return
		}
	} else {
		w.pos++
	}
	w.res.kind = kind
	switch kind {
	case "select":
		w.parseSelectBody()
	case "insert":
		w.parseInsertBody()
	case "replace":
		w.parseInsertBody()
	case "update":
		w.parseUpdateBody()
	case "delete":
		w.parseDeleteBody()
	case "merge":
		w.parseMergeBody()
	case "create":
		w.parseCreateBody()
	case "alter":
		w.parseAlterBody()
	case "drop":
		w.parseDropBody()
	case "truncate":
		w.parseTruncateBody()
	case "rename":
		w.parseRenameBody()
	default:
		w.fail()
	}
	if w.res.fail {
		return
	}
	// Trailing lock clauses (SELECT) are handled inside parseSelectBody; all tokens should be consumed by this point.
}

func (w *walker) skipIf(ws ...string) bool {
	if w.atWord(ws...) {
		w.pos++
		return true
	}
	return false
}

func mustName(t Token) string {
	n, _ := wordTokName(t)
	return n
}

func identifierPartAt(toks []Token, pos int) (identifierPart, bool) {
	if pos < 0 || pos >= len(toks) {
		return identifierPart{}, false
	}
	name, ok := wordTokName(toks[pos])
	if !ok {
		return identifierPart{}, false
	}
	return identifierPart{name: name, quoted: toks[pos].Kind == TokQuotedIdent, pos: pos}, true
}

// parseParenSubquery consumes ( ... ) and recursively classifies the SELECT inside.
func (w *walker) parseParenSubquery() bool {
	if !w.atPunct("(") {
		return false
	}
	w.pos++
	w.depth++
	defer func() { w.depth-- }()
	if w.depth > w.maxDepth {
		return false
	}
	if w.atWord("select") {
		w.pos++
		w.parseSelectBody()
		return !w.res.fail && w.atPunct(")") && w.consumeClose()
	}
	// Parenthesized expressions outside derived tables / scalar subqueries are handled as expressions.
	w.parseExprSeq(false, true, ")")
	if w.res.fail {
		return false
	}
	return w.atPunct(")") && w.consumeClose()
}

func (w *walker) consumeClose() bool {
	if !w.atPunct(")") {
		return false
	}
	w.pos++
	return true
}

// parseColumnListParen consumes ( col,... ) and records each identifier as a column.
func (w *walker) parseColumnListParen() bool {
	if !w.atPunct("(") {
		return false
	}
	w.pos++
	for !w.atPunct(")") {
		if w.eof() {
			return false
		}
		if !w.parseColumnName() {
			return false
		}
		if w.atPunct(",") {
			w.pos++
			continue
		}
		break
	}
	return w.consumeClose()
}

// parseColumnName handles (potentially qualified) column references: c / q.c / s.t.c / q.*.
// Records the starting token positions and backfills roles based on the parts structure.
func (w *walker) parseColumnName() bool {
	start := w.pos
	t := w.cur()
	if t == nil {
		return false
	}
	name, ok := identifierPartAt(w.toks, w.pos)
	if !ok {
		return false
	}
	w.pos++
	parts := []identifierPart{name}
	for w.atPunct(".") {
		w.pos++
		if w.atPunct("*") {
			parts = append(parts, identifierPart{name: "*", pos: w.pos})
			w.pos++
			break
		}
		n2, ok2 := identifierPartAt(w.toks, w.pos)
		if !ok2 {
			return false
		}
		parts = append(parts, n2)
		w.pos++
	}
	return w.markQualifiedRef(start, parts)
}

// markQualifiedRef backfills roles by part count: 1 column, 2 qualifier.column, 3 schema.table.column.
// A "*" in parts only appears at the end and occupies no role.
func (w *walker) markQualifiedRef(start int, parts []identifierPart) bool {
	switch len(parts) {
	case 1:
		if parts[0].name == "*" {
			return false
		}
		w.markQualifiedColumn(start, parts[0], tableIdentity{}, false, start)
	case 2:
		qualifier := parts[0]
		canonicalName := qualifier.name
		key := identityBindingKey(qualifier.name, qualifier.quoted)
		if alias, ok := w.aliases[key]; ok {
			canonicalName = alias.name
		} else if table, ok := w.tables[key]; ok {
			canonicalName = table.name
		}
		role := idRole{
			kind:          privacy.KindTable,
			name:          qualifier.name,
			relation:      RelationReference,
			quoted:        qualifier.quoted,
			canonicalKind: privacy.KindTable,
			canonicalName: canonicalName,
			qualifier:     true,
			qualifierPos:  start,
		}
		if scopeID, ok := w.currentSelectScope(); ok {
			role.scopeID = scopeID
			role.hasScope = true
		}
		w.res.roles[start] = role
		if parts[1].name != "*" {
			w.markQualifiedColumn(parts[1].pos, parts[1], tableIdentity{}, true, start)
		}
	case 3:
		schema := identifierIdentity{name: parts[0].name, quoted: parts[0].quoted}
		table := tableIdentity{name: parts[1].name, quoted: parts[1].quoted, schema: schema}
		w.res.roles[parts[0].pos] = idRole{
			kind: privacy.KindSchema, name: schema.name,
			relation: RelationReference, quoted: schema.quoted,
			canonicalKind: privacy.KindSchema, canonicalName: schema.name,
		}
		w.res.roles[parts[1].pos] = idRole{
			kind: privacy.KindTable, name: table.name,
			relation: RelationReference, quoted: table.quoted, table: table,
			canonicalKind: privacy.KindTable, canonicalName: table.name,
		}
		if parts[2].name != "*" {
			w.markQualifiedColumn(parts[2].pos, parts[2], table, false, -1)
		}
	default:
		return false
	}
	return true
}

func (w *walker) markQualifiedColumn(pos int, part identifierPart, parent tableIdentity, hasQualifier bool, qualifierPos int) {
	role := idRole{
		kind:          privacy.KindColumn,
		name:          part.name,
		relation:      w.columnRelation,
		quoted:        part.quoted,
		canonicalKind: privacy.KindColumn,
		canonicalName: part.name,
		parent:        parent,
		qualifierPos:  qualifierPos,
		// Reuse the qualifier field to indicate "has qualifier", with qualifierPos=-1 distinguishing three-part names.
		qualifier: hasQualifier,
	}
	if scopeID, ok := w.currentSelectScope(); ok {
		role.scopeID = scopeID
		role.hasScope = true
	}
	w.res.roles[pos] = role
}

// parseTableRef handles [schema.]table[[AS] alias] or (subquery)[AS] alias.
// allowImplicitAlias is permitted only in DML contexts such as FROM/JOIN/UPDATE/DELETE/MERGE;
// a bare word after a DDL object name is an action/option keyword, not an alias.
func (w *walker) parseTableRef(allowImplicitAlias bool) bool {
	if w.atPunct("(") {
		return w.parseParenSubquery() && w.parseTrailingAlias(false, tableIdentity{})
	}
	if !w.isIdentLike() {
		return false
	}
	first, _ := identifierPartAt(w.toks, w.pos)
	w.pos++
	var table tableIdentity
	var schema identifierIdentity
	if w.atPunct(".") {
		w.pos++
		if !w.isIdentLike() {
			return false
		}
		second, _ := identifierPartAt(w.toks, w.pos)
		schema = identifierIdentity{name: first.name, quoted: first.quoted}
		table = tableIdentity{name: second.name, quoted: second.quoted, schema: schema}
		w.res.roles[first.pos] = idRole{
			kind: privacy.KindSchema, name: schema.name,
			relation: RelationReference, quoted: schema.quoted,
			// When v1 fingerprints were frozen, the leading segment of a qualified name entered the canonical sequence
			// as a table role; object graph schema refinement must not drift historical fingerprints.
			canonicalKind: privacy.KindTable, canonicalName: schema.name,
		}
		w.markRole(privacy.KindTable, table.name)
		w.res.roles[second.pos].table = table
		w.pos++
	} else {
		table = tableIdentity{name: first.name, quoted: first.quoted}
		if cte, ok := w.lookupCTE(first); ok {
			table = cte
			w.markRoleAt(first.pos, privacy.KindCTE, cte.name)
			w.res.roles[first.pos].namespace = "cte"
			w.res.roles[first.pos].table = cte
			w.res.roles[first.pos].canonicalKind = privacy.KindTable
		} else {
			w.markRoleAt(first.pos, privacy.KindTable, table.name)
			w.res.roles[first.pos].table = table
		}
	}
	w.lastTable = &table
	if scopeID, ok := w.currentSelectScope(); ok {
		scope := &w.selectScopes[scopeID]
		scope.visible = append(scope.visible, table)
		if w.tableRelation == RelationTarget || w.tableRelation == RelationWrite || w.tableRelation == RelationDefine {
			primary := table
			scope.primary = &primary
		}
	} else {
		w.visible = append(w.visible, table)
		if w.tableRelation == RelationTarget || w.tableRelation == RelationWrite || w.tableRelation == RelationDefine {
			primary := table
			w.primary = &primary
		}
	}
	w.tables[identityBindingKey(table.name, table.quoted)] = table
	if _, ok := w.currentSelectScope(); ok {
		w.registerSelectBinding(false, identityBindingKey(table.name, table.quoted), table)
	}
	return w.parseTrailingAlias(allowImplicitAlias, table)
}

// parseTrailingAlias handles [AS] alias and registers the qualifier mapping.
func (w *walker) parseTrailingAlias(allowImplicitAlias bool, table tableIdentity) bool {
	var alias identifierPart
	// When allowImplicitAlias is false (DDL object names), AS SELECT / AS WITH
	// introduces a query body (CREATE VIEW / CREATE TABLE ... AS SELECT|WITH)
	// rather than a table alias.
	if w.atWord("as") && (allowImplicitAlias || (w.peekWord(1) != "select" && w.peekWord(1) != "with")) {
		w.pos++
		if !w.isIdentLike() {
			return false
		}
		alias, _ = identifierPartAt(w.toks, w.pos)
		w.markRole(privacy.KindAlias, alias.name)
		w.res.roles[alias.pos].parent = table
		w.pos++
	} else if allowImplicitAlias && w.isIdentLike() && !nextIsClauseBoundary(w) {
		// Implicit table alias: FROM orders o
		alias, _ = identifierPartAt(w.toks, w.pos)
		if !isReserved(alias.name) {
			w.markRole(privacy.KindAlias, alias.name)
			w.res.roles[alias.pos].parent = table
			w.pos++
		} else {
			alias = identifierPart{}
		}
	}
	if alias.name != "" && table.name != "" {
		key := identityBindingKey(alias.name, alias.quoted)
		w.aliases[key] = table
		w.registerSelectBinding(true, key, table)
	}
	return true
}

func identityBindingKey(name string, quoted bool) string {
	if quoted {
		return "q\x00" + name
	}
	return "u\x00" + strings.ToLower(name)
}

func (w *walker) lookupCTE(part identifierPart) (tableIdentity, bool) {
	table, ok := w.ctes[identityBindingKey(part.name, part.quoted)]
	return table, ok
}

// lookupScopedRelation resolves each scope level as one namespace: an alias
// masks same-named tables in that scope, and any local declaration masks a
// same-named relation inherited from an outer query.
func lookupScopedRelation(
	scopes []selectScope,
	scopeID int,
	key string,
) (privacy.IdentifierKind, tableIdentity, bool, bool) {
	for current := scopeID; current >= 0; current = scopes[current].parent {
		scope := &scopes[current]
		if table, ok := scope.aliases[key]; ok {
			return privacy.KindAlias, table, !scope.ambiguousAliases[key], true
		}
		if scope.ambiguousAliases[key] {
			return "", tableIdentity{}, false, true
		}
		if table, ok := scope.tables[key]; ok {
			return privacy.KindTable, table, !scope.ambiguousTables[key], true
		}
		if scope.ambiguousTables[key] {
			return "", tableIdentity{}, false, true
		}
	}
	return "", tableIdentity{}, false, false
}

func (w *walker) resolveQualifiedIdentity(
	role *idRole,
	part identifierPart,
) (privacy.IdentifierKind, tableIdentity) {
	key := identityBindingKey(part.name, part.quoted)
	if role.hasScope {
		if kind, table, unique, found := lookupScopedRelation(
			w.selectScopes, role.scopeID, key,
		); found {
			if !unique {
				return "", tableIdentity{}
			}
			return kind, table
		}
	} else {
		if table, ok := w.aliases[key]; ok {
			return privacy.KindAlias, table
		}
		if table, ok := w.tables[key]; ok {
			return privacy.KindTable, table
		}
	}
	return "", tableIdentity{}
}

func (w *walker) defaultParentForRole(role idRole) (tableIdentity, bool) {
	if role.hasScope {
		for current := role.scopeID; current >= 0; current = w.selectScopes[current].parent {
			scope := &w.selectScopes[current]
			if scope.primary != nil {
				return *scope.primary, true
			}
			switch len(scope.visible) {
			case 0:
				continue
			case 1:
				return scope.visible[0], true
			default:
				return tableIdentity{}, false
			}
		}
		return tableIdentity{}, false
	}
	return w.defaultParent()
}

// resolveRoles resolves qualifiers that appeared in the SELECT list before FROM after the full statement
// is parsed, and backfills uniquely provable parent tables for columns/indexes. When a parent cannot be
// uniquely proven, it remains unset so callers treat the relationship as incomplete rather than guessing cross-table ownership.
func (w *walker) resolveRoles() {
	for i := range w.res.roles {
		role := &w.res.roles[i]
		if !role.qualifier || role.kind != privacy.KindTable {
			continue
		}
		part := identifierPart{name: role.name, quoted: role.quoted, pos: i}
		kind, table := w.resolveQualifiedIdentity(role, part)
		switch kind {
		case privacy.KindAlias:
			role.kind = privacy.KindAlias
			role.parent = table
		case privacy.KindTable:
			role.table = table
		default:
			role.table = tableIdentity{name: role.name, quoted: role.quoted}
		}
	}
	for i := range w.res.roles {
		role := &w.res.roles[i]
		if role.kind != privacy.KindColumn || !role.qualifier {
			continue
		}
		q := role.qualifierPos
		if q < 0 || q >= len(w.res.roles) {
			continue
		}
		qualifier := w.res.roles[q]
		switch qualifier.kind {
		case privacy.KindAlias:
			role.parent = qualifier.parent
		case privacy.KindTable:
			role.parent = qualifier.table
		}
	}
	for i := range w.res.roles {
		role := &w.res.roles[i]
		if role.parentResolved {
			continue
		}
		switch role.kind {
		case privacy.KindColumn, privacy.KindIndex, privacy.KindConstraint:
			defaultParent, hasDefault := w.defaultParentForRole(*role)
			if hasDefault && role.parent.name == "" {
				role.parent = defaultParent
			}
			role.parentResolved = true
		}
	}
}

func (w *walker) defaultParent() (tableIdentity, bool) {
	if w.primary != nil {
		return *w.primary, true
	}
	if len(w.visible) == 1 {
		return w.visible[0], true
	}
	return tableIdentity{}, false
}

// nextIsClauseBoundary reports whether the current bare word is a clause boundary rather than an implicit alias.
func nextIsClauseBoundary(w *walker) bool {
	t := w.cur()
	if t == nil {
		return true
	}
	if t.Kind != TokWord {
		return false
	}
	switch t.Text {
	case "where", "on", "join", "inner", "left", "right", "full", "cross", "group",
		"order", "having", "limit", "union", "set", "values", "returning", "for",
		"window", "using", "when", "as":
		return true
	}
	return false
}
