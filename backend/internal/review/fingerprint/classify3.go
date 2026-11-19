package fingerprint

import (
	"strings"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// parseCreateBody parses the CREATE body: TABLE/INDEX/VIEW/DATABASE|SCHEMA.
// TRIGGER/PROCEDURE/FUNCTION/EVENT bodies cannot prove role classification -> fail -> compatible_lexer.
func (w *walker) parseCreateBody() {
	for w.atWord("or", "replace", "temporary", "temp", "unlogged", "global", "local") {
		w.pos++
	}
	switch {
	case w.atWord("table"):
		w.pos++
		w.setRelations(RelationDefine, RelationDefine)
		w.parseCreateTable()
	case w.atWord("unique", "fulltext", "spatial"):
		w.pos++
		if w.atWord("index") {
			w.pos++
			w.setRelations(RelationReference, RelationReference)
			w.parseCreateIndex()
			return
		}
		w.fail()
	case w.atWord("index"):
		w.pos++
		w.setRelations(RelationReference, RelationReference)
		w.parseCreateIndex()
	case w.atWord("materialized"):
		w.pos++
		if !w.atWord("view") {
			w.fail()
			return
		}
		w.pos++
		w.setRelations(RelationDefine, RelationDefine)
		w.parseCreateView()
	case w.atWord("view"):
		w.pos++
		w.setRelations(RelationDefine, RelationDefine)
		w.parseCreateView()
	case w.atWord("database", "schema"):
		w.pos++
		w.skipIfNotExists()
		if !w.isIdentLike() {
			w.fail()
			return
		}
		w.markRoleWithRelation(privacy.KindSchema, mustName(*w.cur()), RelationDefine)
		w.pos++
		w.consumeRawOptions()
	default:
		w.fail()
	}
}

// keepRawAt marks a token position to preserve its surface text (non-identifier words such as type names or option values).
func (w *walker) keepRawAt(i int) {
	if i >= 0 && i < len(w.toks) {
		w.res.keepRaw[i] = true
	}
}

// parseCreateTable parses CREATE TABLE [IF NOT EXISTS] name ( elements ) options
// or CREATE TABLE name LIKE source.
func (w *walker) parseCreateTable() {
	w.skipIfNotExists()
	w.tableRelation = RelationDefine
	w.columnRelation = RelationDefine
	if !w.parseTableRef(false) {
		w.fail()
		return
	}
	if w.atPunct("(") {
		w.pos++
		if !w.parseTableElements() {
			w.fail()
			return
		}
	}
	if w.atWord("like") { // Top-level LIKE copy: source table must be anonymized with the table role
		w.pos++
		w.withRelations(RelationReference, RelationReference, func() {
			if !w.parseTableRef(false) {
				w.fail()
			}
		})
	}
	w.consumeRawOptions()
}

// parseTableElements parses the table element list until the closing parenthesis.
func (w *walker) parseTableElements() bool {
	w.columnRelation = RelationDefine
	for {
		if w.eof() {
			return false
		}
		if w.atPunct(")") {
			w.pos++
			return true
		}
		if !w.parseTableElement() {
			return false
		}
		if w.atPunct(",") {
			w.pos++
			continue
		}
	}
}

// parseTableElement parses a single column definition / constraint / index definition.
func (w *walker) parseTableElement() bool {
	switch {
	case w.atWord("primary"):
		w.pos++
		if !w.atWord("key") {
			return false
		}
		w.pos++
		return w.parseIndexTail("")
	case w.atWord("unique"):
		w.pos++
		w.skipIf("key", "index")
		name := ""
		if w.isIdentLike() && !w.atPunct("(") {
			name = mustName(*w.cur())
			w.markRoleWithRelation(privacy.KindIndex, name, RelationDefine)
			w.pos++
		}
		return w.parseIndexTail(name)
	case w.atWord("key", "index", "fulltext", "spatial"):
		w.pos++
		name := ""
		if w.isIdentLike() && !w.atPunct("(") {
			name = mustName(*w.cur())
			w.markRoleWithRelation(privacy.KindIndex, name, RelationDefine)
			w.pos++
		}
		return w.parseIndexTail(name)
	case w.atWord("foreign"):
		w.pos++
		if !w.atWord("key") {
			return false
		}
		w.pos++
		name := ""
		if w.isIdentLike() && !w.atPunct("(") {
			name = mustName(*w.cur())
			w.markRoleWithRelation(privacy.KindConstraint, name, RelationDefine)
			w.pos++
		}
		if !w.parseIndexTail(name) {
			return false
		}
		return w.parseReferencesClause()
	case w.atWord("constraint"):
		w.pos++
		name := ""
		if w.isIdentLike() {
			name = mustName(*w.cur())
			w.markRoleWithRelation(privacy.KindConstraint, name, RelationDefine)
			w.pos++
		}
		switch {
		case w.atWord("primary"):
			w.pos++
			if !w.atWord("key") {
				return false
			}
			w.pos++
			return w.parseIndexTail(name)
		case w.atWord("unique"):
			w.pos++
			w.skipIf("key", "index")
			if w.isIdentLike() && !w.atPunct("(") {
				n2 := mustName(*w.cur())
				w.markRoleWithRelation(privacy.KindIndex, n2, RelationDefine)
				w.pos++
			}
			return w.parseIndexTail(name)
		case w.atWord("foreign"):
			w.pos++
			if !w.atWord("key") {
				return false
			}
			w.pos++
			if w.isIdentLike() && !w.atPunct("(") {
				n2 := mustName(*w.cur())
				w.markRoleWithRelation(privacy.KindConstraint, n2, RelationDefine)
				w.pos++
			}
			if !w.parseIndexTail(name) {
				return false
			}
			return w.parseReferencesClause()
		case w.atWord("check"):
			w.pos++
			if !w.parseParenExpr() {
				return false
			}
			w.skipIf("no")
			if w.atWord("inherit") {
				w.pos++
			}
			return true
		case w.atWord("exclude"):
			w.pos++
			w.skipIf("using")
			if w.isIdentLike() {
				w.pos++
			}
			return w.parseParenExpr()
		default:
			return false
		}
	case w.atWord("check"):
		w.pos++
		return w.parseParenExpr()
	case w.atWord("like"): // CREATE TABLE x (LIKE y ...)
		w.pos++
		ok := true
		w.withRelations(RelationReference, RelationReference, func() {
			ok = w.parseTableRef(false)
		})
		if !ok {
			return false
		}
		w.skipIf("including", "excluding")
		for w.isIdentLike() || w.atWord("all") {
			w.pos++
			break
		}
		return true
	default:
		return w.parseColumnDefinition()
	}
}

// parseIndexTail parses ( col [len] [asc|desc] , ... ) [USING method] and PG INCLUDE clauses.
func (w *walker) parseIndexTail(name string) bool {
	if !w.atPunct("(") {
		return false
	}
	w.pos++
	oldColumnRelation := w.columnRelation
	w.columnRelation = RelationReference
	defer func() { w.columnRelation = oldColumnRelation }()
	for !w.atPunct(")") {
		if w.eof() {
			return false
		}
		if w.atPunct("(") {
			// A key that starts with parentheses is an expression. The role
			// graph can still capture nested column references for privacy,
			// but the bounded validity model cannot treat expression
			// functions as catalog columns.
			w.markMetadataIncomplete()
			if !w.parseParenExpr() {
				return false
			}
		} else {
			if !w.parseColumnName() {
				return false
			}
			if w.atPunct("(") && !indexParenIsPrefixLength(w.toks, w.pos) {
				w.markMetadataIncomplete()
			}
		}
		// Prefix length: col(10) / PG expressions are already handled by parentheses inside parseColumnName.
		if w.atPunct("(") {
			if !w.parseParenExpr() {
				return false
			}
		}
		w.skipIf("asc", "desc")
		if w.atWord("nulls") {
			w.pos++
			if !w.skipIf("first", "last") {
				return false
			}
		}
		if w.atPunct(",") {
			w.pos++
			continue
		}
		break
	}
	if !w.consumeClose() {
		return false
	}
	if w.atWord("using") { // MySQL USING BTREE (PG method name after ON is tolerated here)
		w.pos++
		if w.isIdentLike() || w.cur() != nil && w.cur().Kind == TokWord {
			if isControlledIndexMethod(w.cur().Text) {
				w.keepRawAt(w.pos)
			} else {
				w.markMetadataIncomplete()
			}
			w.pos++
		}
	}
	return true
}

func indexParenIsPrefixLength(toks []Token, open int) bool {
	closePos := matchingParenPosition(toks, open)
	if closePos < 0 || closePos == open+1 {
		return false
	}
	for pos := open + 1; pos < closePos; pos++ {
		if toks[pos].Kind != TokNumber {
			return false
		}
	}
	return true
}

// parseReferencesClause parses REFERENCES tbl [(cols)] [ON DELETE/UPDATE action].
func (w *walker) parseReferencesClause() bool {
	if !w.atWord("references") {
		return false
	}
	w.pos++
	ok := true
	w.withRelations(RelationReference, RelationReference, func() {
		ok = w.parseTableRef(false)
	})
	if !ok {
		return false
	}
	if w.atPunct("(") {
		w.columnRelation = RelationReference
		if !w.parseColumnListParen() {
			return false
		}
	}
	for w.atWord("on") {
		w.pos++
		if !w.atWord("delete", "update") {
			return false
		}
		w.pos++
		if w.atWord("set") {
			w.pos++
			if !w.atWord("null", "default") {
				return false
			}
			w.pos++
		} else if w.atWord("no") {
			w.pos++
			if !w.atWord("action") {
				return false
			}
			w.pos++
		} else {
			w.skipIf("cascade", "restrict")
		}
	}
	return true
}

// parseColumnDefinition parses a column definition: name type attrs*.
func (w *walker) parseColumnDefinition() bool {
	if !w.isIdentLike() {
		return false
	}
	w.markRole(privacy.KindColumn, mustName(*w.cur()))
	w.pos++
	if !w.parseTypeName() {
		return false
	}
	return w.parseColumnAttrs()
}

// parseTypeName consumes type words and length/precision/array suffixes.
func (w *walker) parseTypeName() bool {
	t := w.cur()
	if t == nil || (t.Kind != TokWord && t.Kind != TokQuotedIdent) {
		return false
	}
	parts := 0
	firstKind := t.Kind
	firstText := t.Text
	for {
		parts++
		w.pos++
		if !w.atPunct(".") {
			break
		}
		w.pos++
		if !w.isIdentLike() {
			return false
		}
	}
	if parts == 1 && firstKind == TokWord && isTypeWord(firstText) {
		w.keepRawAt(w.pos - 1)
	} else {
		// Custom/quoted/qualified types are user schema material. Keep the
		// structural type position but anonymize every name segment.
		w.markMetadataIncomplete()
	}
	if w.atPunct("(") {
		if !w.parseParenExpr() {
			return false
		}
	}
	// Multi-word types: double precision / character varying.
	if parts == 1 && firstKind == TokWord && isTypeWord(firstText) && w.atWord("precision", "varying") {
		w.keepRawAt(w.pos)
		w.pos++
	}
	for w.atPunct("[") { // Array suffix
		w.pos++
		for !w.atPunct("]") {
			if w.eof() {
				return false
			}
			w.pos++
		}
		w.pos++
	}
	return true
}

// parseColumnAttrs consumes column attributes until an element boundary (comma / right paren / constraint keyword).
func (w *walker) parseColumnAttrs() bool {
	for !w.eof() {
		switch {
		case w.atPunct(",") || w.atPunct(")"):
			return true
		case w.atWord("null", "not"):
			w.pos++
		case w.atWord("default"):
			w.pos++
			w.parseExprSeq(false, false, ",", ")")
		case w.atWord("auto_increment", "autoincrement", "identity", "primary", "unique",
			"unsigned", "signed", "zerofill", "invisible", "visible", "stored", "virtual",
			"instant", "comment_cant_appear"):
			if w.atWord("primary") {
				w.pos++
				if !w.atWord("key") {
					return false
				}
				w.pos++
				continue
			}
			w.pos++
		case w.atWord("comment"):
			w.pos++
			if w.cur() != nil && w.cur().Kind == TokString {
				w.pos++
			} else {
				return false
			}
		case w.atWord("collate", "charset") || w.atWord("character"):
			isCollation := w.atWord("collate")
			w.pos++
			if w.atWord("set") {
				w.pos++
			}
			if w.isIdentLike() {
				controlled := isControlledCharset(w.cur().Text)
				if isCollation {
					controlled = isControlledCollation(w.cur().Text)
				}
				if controlled {
					w.keepRawAt(w.pos)
				} else {
					w.markMetadataIncomplete()
				}
				w.pos++
			} else if w.cur() != nil && w.cur().Kind == TokString {
				w.markMetadataIncomplete()
				w.pos++
			} else {
				return false
			}
		case w.atWord("generated"):
			w.pos++
			if !w.atWord("always") {
				return false
			}
			w.pos++
			if !w.atWord("as") {
				return false
			}
			w.pos++
			if !w.parseParenExpr() {
				return false
			}
			w.skipIf("stored", "virtual")
		case w.atWord("as"): // PG generated column shorthand
			w.pos++
			if !w.parseParenExpr() {
				return false
			}
			w.skipIf("stored", "virtual")
		case w.atWord("check"):
			w.pos++
			if !w.parseParenExpr() {
				return false
			}
		case w.atWord("references"):
			if !w.parseReferencesClause() {
				return false
			}
		case w.atWord("on"): // ON UPDATE CURRENT_TIMESTAMP
			w.pos++
			if !w.atWord("update") {
				return false
			}
			w.pos++
			w.parseExprSeq(false, false, ",", ")")
		case w.atWord("first", "after"):
			w.pos++
			if w.isIdentLike() {
				w.markRole(privacy.KindColumn, mustName(*w.cur()))
				w.pos++
			}
		case w.atWord("using"):
			w.pos++
			if w.isIdentLike() {
				if isControlledIndexMethod(w.cur().Text) {
					w.keepRawAt(w.pos)
				} else {
					w.markMetadataIncomplete()
				}
				w.pos++
			} else {
				return false
			}
		default:
			// Unknown DDL vocabulary may itself contain schema or vendor
			// metadata. Consume it structurally, anonymize it, and mark the
			// model-visible context incomplete.
			t := w.cur()
			if t != nil && (t.Kind == TokWord || t.Kind == TokString || t.Kind == TokNumber) {
				w.markMetadataIncomplete()
				w.pos++
				continue
			}
			return false
		}
	}
	return true
}

// parseCreateIndex parses CREATE [UNIQUE] INDEX [CONCURRENTLY] [IF NOT EXISTS] idx ON tbl [USING m] (cols).
func (w *walker) parseCreateIndex() {
	if w.atWord("concurrently") {
		w.pos++
		w.res.features = append(w.res.features, "concurrently")
	}
	if w.atWord("if") {
		w.pos++
		if !w.atWord("not") {
			w.fail()
			return
		}
		w.pos++
		if !w.atWord("exists") {
			w.fail()
			return
		}
		w.pos++
	}
	if !w.isIdentLike() {
		w.fail()
		return
	}
	w.markRoleWithRelation(privacy.KindIndex, mustName(*w.cur()), RelationDefine)
	w.pos++
	if !w.atWord("on") {
		w.fail()
		return
	}
	w.pos++
	if !w.parseTableRef(false) {
		w.fail()
		return
	}
	if w.lastTable != nil {
		table := *w.lastTable
		w.primary = &table
	}
	if w.atWord("using") {
		w.pos++
		if w.isIdentLike() {
			if isControlledIndexMethod(w.cur().Text) {
				w.keepRawAt(w.pos)
			} else {
				w.markMetadataIncomplete()
			}
			w.pos++
		} else {
			w.fail()
			return
		}
	}
	if !w.parseIndexTail("") {
		w.fail()
		return
	}
	if w.atWord("include") {
		w.pos++
		if !w.parseColumnListParen() {
			w.fail()
			return
		}
	}
	if w.atWord("where") {
		w.pos++
		w.parseExprSeq(false, true)
	}
	w.consumeRawOptions()
}

// parseCreateView parses CREATE VIEW name [(cols)] AS select.
func (w *walker) parseCreateView() {
	// Views are not base tables. Until their complete relation definition is
	// exposed by a dedicated safe fact, table metadata cannot prove coverage.
	w.markMetadataIncomplete()
	if w.atWord("if") {
		w.pos++
		if !w.atWord("not") {
			w.fail()
			return
		}
		w.pos++
		if !w.atWord("exists") {
			w.fail()
			return
		}
		w.pos++
	}
	if !w.parseTableRef(false) {
		w.fail()
		return
	}
	if w.atPunct("(") {
		if !w.parseColumnListParen() {
			w.fail()
			return
		}
	}
	if !w.atWord("as") {
		w.fail()
		return
	}
	w.pos++
	if !w.atWord("select") {
		w.fail()
		return
	}
	w.pos++
	w.parseSelectBody()
}

// parseAlterBody parses the ALTER TABLE/INDEX/VIEW/DATABASE|SCHEMA body.
func (w *walker) parseAlterBody() {
	switch {
	case w.atWord("table"):
		w.pos++
		w.skipIfExists()
		w.setRelations(RelationTarget, RelationTarget)
		if !w.parseTableRef(false) {
			w.fail()
			return
		}
		w.parseAlterActions()
	case w.atWord("index"):
		w.pos++
		if !w.isIdentLike() {
			w.fail()
			return
		}
		w.markRoleWithRelation(privacy.KindIndex, mustName(*w.cur()), RelationTarget)
		w.pos++
		w.consumeRawOptions()
	case w.atWord("view"):
		w.markMetadataIncomplete()
		w.pos++
		w.setRelations(RelationTarget, RelationTarget)
		if !w.parseTableRef(false) {
			w.fail()
			return
		}
		for w.atWord("alter", "rename", "set", "owner", "to") && !w.eof() {
			w.pos++
			if w.isIdentLike() {
				w.pos++
			}
		}
	case w.atWord("database", "schema"):
		w.pos++
		if w.isIdentLike() {
			w.markRoleWithRelation(privacy.KindSchema, mustName(*w.cur()), RelationTarget)
			w.pos++
		}
		w.consumeRawOptions()
	default:
		w.fail()
	}
}

// parseAlterActions parses the list of ALTER actions.
func (w *walker) parseAlterActions() {
	for {
		if w.eof() {
			return
		}
		switch {
		case w.atWord("add"):
			w.pos++
			if w.atWord("column") {
				w.pos++
				w.skipIfNotExists()
				old := w.columnRelation
				w.columnRelation = RelationDefine
				ok := w.parseColumnDefinition()
				w.columnRelation = old
				if !ok {
					w.fail()
					return
				}
			} else if w.atWord("primary") {
				w.pos++
				if !w.atWord("key") {
					w.fail()
					return
				}
				w.pos++
				if !w.parseIndexTail("") {
					w.fail()
					return
				}
			} else if w.atWord("unique", "fulltext", "spatial") {
				w.pos++
				w.skipIf("key", "index")
				w.skipIfNotExists()
				name := ""
				if w.isIdentLike() && !w.atPunct("(") {
					name = mustName(*w.cur())
					w.markRoleWithRelation(privacy.KindIndex, name, RelationDefine)
					w.pos++
				}
				if !w.parseIndexTail(name) {
					w.fail()
					return
				}
			} else if w.atWord("index", "key") {
				w.pos++
				w.skipIfNotExists()
				name := ""
				if w.isIdentLike() && !w.atPunct("(") {
					name = mustName(*w.cur())
					w.markRoleWithRelation(privacy.KindIndex, name, RelationDefine)
					w.pos++
				}
				if !w.parseIndexTail(name) {
					w.fail()
					return
				}
			} else if w.atWord("constraint") {
				w.pos++
				name := ""
				if w.isIdentLike() {
					name = mustName(*w.cur())
					w.markRoleWithRelation(privacy.KindConstraint, name, RelationDefine)
					w.pos++
				}
				if w.atWord("foreign") {
					w.pos++
					if !w.atWord("key") {
						w.fail()
						return
					}
					w.pos++
					if !w.parseIndexTail(name) {
						w.fail()
						return
					}
					if !w.parseReferencesClause() {
						w.fail()
						return
					}
				} else if w.atWord("unique") {
					w.pos++
					w.skipIf("key", "index")
					if !w.parseIndexTail(name) {
						w.fail()
						return
					}
				} else if w.atWord("check") {
					w.pos++
					if !w.parseParenExpr() {
						w.fail()
						return
					}
				} else {
					w.fail()
					return
				}
			} else if w.atWord("foreign") {
				w.pos++
				if !w.atWord("key") {
					w.fail()
					return
				}
				w.pos++
				if !w.parseIndexTail("") {
					w.fail()
					return
				}
				if !w.parseReferencesClause() {
					w.fail()
					return
				}
			} else if w.atWord("check") {
				w.pos++
				if !w.parseParenExpr() {
					w.fail()
					return
				}
			} else {
				// ADD [COLUMN] may omit COLUMN.
				w.skipIfNotExists()
				old := w.columnRelation
				w.columnRelation = RelationDefine
				ok := w.parseColumnDefinition()
				w.columnRelation = old
				if !ok {
					w.fail()
					return
				}
			}
		case w.atWord("drop"):
			w.pos++
			w.skipIf("column")
			if w.atWord("primary") {
				w.pos++
				if !w.atWord("key") {
					w.fail()
					return
				}
				w.pos++
			} else if w.atWord("index", "key") {
				w.pos++
				w.skipIfExists()
				if !w.dropName(privacy.KindIndex) {
					return
				}
			} else if w.atWord("foreign") {
				w.pos++
				if !w.atWord("key") {
					w.fail()
					return
				}
				w.pos++
				w.skipIfExists()
				if !w.dropName(privacy.KindConstraint) {
					return
				}
			} else if w.atWord("constraint") {
				w.pos++
				w.skipIfExists()
				if !w.dropName(privacy.KindConstraint) {
					return
				}
			} else if w.atWord("check") {
				w.pos++
				w.skipIfExists()
				if !w.dropName(privacy.KindConstraint) {
					return
				}
			} else {
				w.skipIfExists()
				if !w.dropName(privacy.KindColumn) {
					return
				}
			}
			w.skipIf("cascade", "restrict")
		case w.atWord("modify", "change"):
			isChange := w.atWord("change")
			w.pos++
			w.skipIf("column")
			if !w.isIdentLike() {
				w.fail()
				return
			}
			w.markRole(privacy.KindColumn, mustName(*w.cur()))
			w.res.roles[w.pos].relation = RelationTarget
			w.pos++
			if isChange {
				if !w.isIdentLike() {
					w.fail()
					return
				}
				w.markRole(privacy.KindColumn, mustName(*w.cur()))
				w.res.roles[w.pos].relation = RelationDefine
				w.pos++
			}
			old := w.columnRelation
			w.columnRelation = RelationDefine
			ok := w.parseColumnDefinition()
			w.columnRelation = old
			if !ok {
				w.fail()
				return
			}
		case w.atWord("rename"):
			w.pos++
			switch {
			case w.atWord("column"):
				w.pos++
				if !w.isIdentLike() {
					w.fail()
					return
				}
				w.markRole(privacy.KindColumn, mustName(*w.cur()))
				w.res.roles[w.pos].relation = RelationTarget
				w.pos++
				if !w.atWord("to") {
					w.fail()
					return
				}
				w.pos++
				if !w.isIdentLike() {
					w.fail()
					return
				}
				w.markRole(privacy.KindColumn, mustName(*w.cur()))
				w.res.roles[w.pos].relation = RelationDefine
				w.pos++
			case w.atWord("index", "key"):
				w.pos++
				if !w.isIdentLike() {
					w.fail()
					return
				}
				w.markRole(privacy.KindIndex, mustName(*w.cur()))
				w.pos++
				if !w.atWord("to") {
					w.fail()
					return
				}
				w.pos++
				if !w.isIdentLike() {
					w.fail()
					return
				}
				w.markRole(privacy.KindIndex, mustName(*w.cur()))
				w.pos++
			case w.atWord("to", "as"):
				w.pos++
				if !w.parseTableRef(false) {
					w.fail()
					return
				}
			default:
				if !w.parseTableRef(false) {
					w.fail()
					return
				}
			}
		case w.atWord("alter"):
			w.pos++
			w.skipIf("column")
			if !w.isIdentLike() {
				w.fail()
				return
			}
			w.markRole(privacy.KindColumn, mustName(*w.cur()))
			w.pos++
			switch {
			case w.atWord("set"):
				w.pos++
				if w.atWord("default") {
					w.pos++
					w.parseExprSeq(false, false, ",", ")", "cascade")
				} else if w.atWord("not") {
					w.pos++
					if !w.atWord("null") {
						w.fail()
						return
					}
					w.pos++
				} else {
					w.fail()
					return
				}
			case w.atWord("drop"):
				w.pos++
				if !w.skipIf("default", "not", "expression", "identity") {
					w.fail()
					return
				}
				if w.atWord("null") {
					w.pos++
				}
			case w.atWord("type"):
				w.pos++
				if !w.parseTypeName() {
					w.fail()
					return
				}
			case w.atWord("add", "drop"):
				w.pos++
				if !w.skipIf("generated") {
					w.fail()
					return
				}
			default:
				w.fail()
				return
			}
		case w.atWord("convert"):
			w.pos++
			if !w.atWord("to") {
				w.fail()
				return
			}
			w.pos++
			w.consumeRawOptions()
		case w.atWord("enable", "disable", "validate", "owner", "cluster", "set", "attach", "detach", "replica"):
			w.pos++
			w.consumeRawOptions()
		default:
			w.fail()
			return
		}
		if w.atPunct(",") {
			w.pos++
			continue
		}
		return
	}
}

// dropName consumes and marks the object name to be dropped.
func (w *walker) dropName(kind privacy.IdentifierKind) bool {
	if !w.isIdentLike() {
		w.fail()
		return false
	}
	w.markRoleWithRelation(kind, mustName(*w.cur()), RelationTarget)
	w.pos++
	return true
}

// parseDropBody parses DROP TABLE/INDEX/VIEW/DATABASE|SCHEMA [IF EXISTS] names.
func (w *walker) parseDropBody() {
	var kind privacy.IdentifierKind
	switch {
	case w.atWord("table"):
		kind = privacy.KindTable
	case w.atWord("index"):
		kind = privacy.KindIndex
	case w.atWord("view"):
		w.markMetadataIncomplete()
		kind = privacy.KindTable
	case w.atWord("database", "schema"):
		kind = privacy.KindSchema
	case w.atWord("sequence"):
		w.markMetadataIncomplete()
		kind = privacy.KindSchema
	case w.atWord("constraint"):
		kind = privacy.KindConstraint
	default:
		w.fail()
		return
	}
	w.pos++
	w.skipIfExists()
	for {
		if !w.isIdentLike() {
			w.fail()
			return
		}
		first, _ := identifierPartAt(w.toks, w.pos)
		firstPos := w.pos
		w.pos++
		if w.atPunct(".") { // Qualified name
			w.pos++
			if !w.isIdentLike() {
				w.fail()
				return
			}
			if kind == privacy.KindTable {
				// DROP TABLE schema.table: schema is a locating reference, and the second name is the drop target.
				schema := identifierIdentity{name: first.name, quoted: first.quoted}
				w.res.roles[firstPos] = idRole{
					kind: privacy.KindSchema, name: schema.name,
					relation: RelationReference, quoted: schema.quoted,
					canonicalKind: privacy.KindTable, canonicalName: schema.name,
				}
				table, _ := identifierPartAt(w.toks, w.pos)
				tableIdentity := tableIdentity{
					name: table.name, quoted: table.quoted, schema: schema,
				}
				w.markRoleWithRelation(privacy.KindTable, tableIdentity.name, RelationTarget)
				w.res.roles[w.pos].table = tableIdentity
			} else {
				w.markRoleWithRelation(kind, first.name, RelationTarget)
				w.markRoleWithRelation(kind, mustName(*w.cur()), RelationTarget)
			}
			w.pos++
		} else {
			// The cursor already points past the name. Mark its actual token,
			// otherwise unqualified DROP targets disappear from the object graph.
			w.markRoleAt(firstPos, kind, first.name)
			w.res.roles[firstPos].relation = RelationTarget
			if w.toks[firstPos].Kind == TokWord {
				w.res.roles[firstPos].canonicalWord = true
			} else {
				w.res.roles[firstPos].canonicalKind = privacy.KindGeneric
			}
			// v1 accidentally assigned the target's canonical role to the next
			// token (comma/CASCADE). Retain that hash input only; it is not a
			// model-visible object or identifier role.
			if w.pos < len(w.res.roles) {
				w.res.roles[w.pos] = idRole{canonicalKind: kind, canonicalName: first.name}
			}
			if kind == privacy.KindTable {
				w.res.roles[firstPos].table = tableIdentity{name: first.name, quoted: first.quoted}
			}
			if kind == privacy.KindSchema {
				w.res.roles[firstPos].relation = RelationTarget
			}
			if kind == privacy.KindIndex || kind == privacy.KindConstraint {
				w.res.roles[firstPos].parent = tableIdentity{}
			}
		}
		if w.atPunct(",") {
			w.pos++
			continue
		}
		break
	}
	w.skipIf("cascade", "restrict")
}

// parseTruncateBody parses TRUNCATE [TABLE] [ONLY] name[, ...].
func (w *walker) parseTruncateBody() {
	w.skipIf("table", "only")
	w.tableRelation = RelationTarget
	w.columnRelation = RelationFilter
	for {
		if !w.parseTableRef(false) {
			w.fail()
			return
		}
		if w.atPunct(",") {
			w.pos++
			continue
		}
		break
	}
	w.skipIf("restart", "continue")
	if w.atWord("identity") {
		w.pos++
	}
	if w.atWord("cascade", "restrict") {
		w.pos++
	}
}

// consumeRawOptions consumes the unstructured DDL option tail. Only closed
// allowlists may remain model-visible; every other word is anonymized and
// marks the metadata context incomplete.
func (w *walker) consumeRawOptions() {
	for !w.eof() {
		t := w.cur()
		switch t.Kind {
		case TokWord:
			switch {
			case isReserved(t.Text):
				// Structural words remain visible through the renderer.
			case isControlledStorageEngine(t.Text),
				isControlledCharset(t.Text),
				isControlledCollation(t.Text),
				isControlledIndexMethod(t.Text):
				w.keepRawAt(w.pos)
			default:
				w.markMetadataIncomplete()
			}
			w.pos++
		case TokNumber, TokString:
			w.pos++
		case TokPunct:
			if t.Text == "=" || t.Text == "," || t.Text == "(" || t.Text == ")" {
				w.pos++
				continue
			}
			return
		default:
			return
		}
	}
}

// toLowerName normalizes a lowercase comparison key.
func toLowerName(s string) string { return strings.ToLower(s) }
