package fingerprint

import (
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"strings"
)

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
	for !w.eof() {
		switch {
		case w.atWord("using"): // MySQL USING BTREE (PG method name after ON is tolerated here)
			w.pos++
			if w.isIdentLike() || w.cur() != nil && w.cur().Kind == TokWord {
				if isControlledIndexMethod(w.cur().Text) {
					w.keepRawAt(w.pos)
				} else {
					w.markMetadataIncomplete()
				}
				w.pos++
			}
		case w.atWord("comment"):
			w.pos++
			if w.cur() != nil && w.cur().Kind == TokString {
				w.pos++
			} else {
				return false
			}
		case w.atWord("visible", "invisible"):
			// Keep anonymized (matching parseColumnAttrs) so a table combining
			// an INVISIBLE/VISIBLE column and index never trips forbiddenSet.
			w.pos++
		case w.atWord("key_block_size"):
			w.tryConsumeControlledTableOption()
		default:
			return true
		}
	}
	return true
}

var columnAttrExprStops = []string{
	",", ")",
	"not", "null", "comment",
	"auto_increment", "autoincrement", "identity",
	"primary", "unique", "check", "references",
	"on", "generated", "collate", "charset", "character",
	"after", "first", "invisible", "visible", "stored", "virtual", "using",
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
			if w.atWord("null") {
				w.pos++
				continue
			}
			w.parseExprSeq(false, false, columnAttrExprStops...)
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
			if w.atWord("always") {
				w.pos++
			} else if w.atWord("by") {
				w.pos++
				if !w.atWord("default") {
					return false
				}
				w.pos++
			} else {
				return false
			}
			if !w.atWord("as") {
				return false
			}
			w.pos++
			if w.atWord("identity") {
				// Keep anonymized (matching parseColumnAttrs and DROP IDENTITY) so
				// a statement combining a column named identity or DROP IDENTITY
				// never trips forbiddenSet.
				w.pos++
				if w.atPunct("(") {
					if !w.parseParenExpr() {
						return false
					}
				}
				continue
			}
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
			w.parseExprSeq(false, false, columnAttrExprStops...)
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

// parseAlterActions parses the list of ALTER actions.
func (w *walker) parseAlterActions() {
	for {
		wasTableOption := false
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
		case w.atWord("comment"):
			wasTableOption = true
			w.pos++
			if w.atPunct("=") {
				w.pos++
			}
			if w.cur() != nil && w.cur().Kind == TokString {
				w.pos++
			} else {
				w.fail()
				return
			}
		case w.atWord("engine"):
			wasTableOption = true
			w.pos++
			if w.atPunct("=") {
				w.pos++
			}
			if w.cur() != nil && w.cur().Kind == TokWord {
				if isControlledStorageEngine(w.cur().Text) {
					w.keepRawAt(w.pos)
				} else {
					w.markMetadataIncomplete()
				}
				w.pos++
			} else {
				w.fail()
				return
			}
		case w.atWord("default", "charset", "character", "collate"):
			wasTableOption = true
			w.skipIf("default")
			if w.atWord("character") {
				w.pos++
				if !w.atWord("set") {
					w.fail()
					return
				}
				w.pos++
			} else if w.atWord("charset", "collate") {
				isCollation := w.atWord("collate")
				w.pos++
				if w.atPunct("=") {
					w.pos++
				}
				if w.cur() != nil && w.cur().Kind == TokWord {
					ok := isControlledCharset(w.cur().Text)
					if isCollation {
						ok = isControlledCollation(w.cur().Text)
					}
					if ok {
						w.keepRawAt(w.pos)
					} else {
						w.markMetadataIncomplete()
					}
					w.pos++
				} else {
					w.fail()
					return
				}
				break
			} else {
				w.fail()
				return
			}
			if w.atPunct("=") {
				w.pos++
			}
			if w.cur() != nil && w.cur().Kind == TokWord {
				if isControlledCharset(w.cur().Text) {
					w.keepRawAt(w.pos)
				} else {
					w.markMetadataIncomplete()
				}
				w.pos++
			} else {
				w.fail()
				return
			}
		case w.tryConsumeControlledTableOption():
			wasTableOption = true
			// Handled single MySQL ALTER TABLE option (ALGORITHM/LOCK/AUTO_INCREMENT/ROW_FORMAT/etc.).
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
		if wasTableOption && w.isAlterTableOptionStart() {
			continue
		}
		return
	}
}

// toLowerName normalizes a lowercase comparison key.
func toLowerName(s string) string { return strings.ToLower(s) }
