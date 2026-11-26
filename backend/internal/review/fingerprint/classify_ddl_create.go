// classify_ddl_create.go holds CREATE/DDL statement and column classification helpers.
package fingerprint

import "github.com/cookieY/YearningX/backend/internal/review/privacy"

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
	if w.atWord("as") && (w.peekWord(1) == "select" || w.peekWord(1) == "with") {
		w.pos++
		w.parseQueryTail()
		return
	}
}

// parseQueryTail parses a SELECT or WITH ... SELECT query body following AS
// in CREATE VIEW ... AS <query> and CREATE TABLE ... AS <query>.
func (w *walker) parseQueryTail() {
	switch {
	case w.atWord("select"):
		w.pos++
		w.parseSelectBody()
	case w.atWord("with"):
		kind := w.parseWithCTEs()
		if w.res.fail || kind != "select" {
			w.fail()
			return
		}
		w.parseSelectBody()
	default:
		w.fail()
	}
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
	w.parseQueryTail()
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

// parseRenameBody parses RENAME TABLE old_tbl TO new_tbl [, old_tbl2 TO new_tbl2 ...].
// Non-TABLE forms (for example RENAME USER) fail and fall back to compatible_lexer.
func (w *walker) parseRenameBody() {
	if !w.atWord("table") {
		w.fail()
		return
	}
	w.pos++
	for {
		w.setRelations(RelationTarget, RelationReference)
		if !w.parseTableRef(false) {
			w.fail()
			return
		}
		if !w.atWord("to") {
			w.fail()
			return
		}
		w.keepRawAt(w.pos)
		w.pos++
		w.setRelations(RelationDefine, RelationReference)
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
}

// isAlterTableOptionStart reports whether the current token starts a MySQL
// ALTER TABLE table_option (which MySQL allows separating with whitespace or commas).
func (w *walker) isAlterTableOptionStart() bool {
	return w.atWord(
		"comment", "engine", "default", "charset", "character", "collate",
		"auto_increment", "key_block_size", "checksum", "delay_key_write",
		"stats_persistent", "stats_auto_recalc", "stats_sample_pages", "pack_keys",
		"row_format", "algorithm", "lock", "insert_method", "compression",
	)
}

// tryConsumeControlledTableOption consumes a single MySQL/DDL table-option
// key[=]value pair in option-tail or ALTER TABLE action position. Keywords and
// values are validated contextually without keepRawAt (never in global
// reservedWords) so user identifiers or string literals with the same names
// never collide with forbiddenSet leak assertions, while MetadataContextComplete
// remains true for controlled values and fails closed on unknown values.
func (w *walker) tryConsumeControlledTableOption() bool {
	if w.eof() || w.cur().Kind != TokWord {
		return false
	}
	keyPos := w.pos
	nextAfterOptEq := func() int {
		next := keyPos + 1
		if next < len(w.toks) && w.toks[next].Kind == TokPunct && w.toks[next].Text == "=" {
			next++
		}
		return next
	}
	consumeNumericOption := func(allowDefault bool) bool {
		valPos := nextAfterOptEq()
		if valPos < len(w.toks) {
			tok := w.toks[valPos]
			if tok.Kind == TokNumber || (allowDefault && tok.Kind == TokWord && tok.Text == "default") {
				w.pos = valPos + 1
				return true
			}
		}
		w.markMetadataIncomplete()
		w.pos = keyPos + 1
		return true
	}
	consumeWordOption := func(allowed func(string) bool) bool {
		valPos := nextAfterOptEq()
		if valPos < len(w.toks) && w.toks[valPos].Kind == TokWord && allowed(w.toks[valPos].Text) {
			w.pos = valPos + 1
			return true
		}
		w.markMetadataIncomplete()
		w.pos = keyPos + 1
		return true
	}

	switch {
	case w.atWord("auto_increment", "key_block_size", "checksum", "delay_key_write"):
		return consumeNumericOption(false)
	case w.atWord("stats_persistent", "stats_auto_recalc", "stats_sample_pages", "pack_keys"):
		return consumeNumericOption(true)
	case w.atWord("row_format"):
		return consumeWordOption(isControlledRowFormat)
	case w.atWord("algorithm"):
		return consumeWordOption(isControlledAlterAlgorithm)
	case w.atWord("lock"):
		return consumeWordOption(isControlledAlterLock)
	case w.atWord("insert_method"):
		return consumeWordOption(isControlledInsertMethod)
	case w.atWord("compression"):
		valPos := nextAfterOptEq()
		if valPos < len(w.toks) && w.toks[valPos].Kind == TokString {
			w.pos = valPos + 1
			return true
		}
		w.markMetadataIncomplete()
		w.pos = keyPos + 1
		return true
	default:
		return false
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
			if t.Text == "as" && (w.peekWord(1) == "select" || w.peekWord(1) == "with") {
				return
			}
			if w.tryConsumeControlledTableOption() {
				continue
			}
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
