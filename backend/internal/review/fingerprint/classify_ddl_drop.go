// classify_ddl_drop.go holds DROP statement classification helpers.
package fingerprint

import "github.com/cookieY/YearningX/backend/internal/review/privacy"

// parseDropBody parses DROP TABLE/INDEX/VIEW/DATABASE|SCHEMA [IF EXISTS] names.
func (w *walker) parseDropBody() {
	kind, ok := w.parseDropKind()
	if !ok {
		w.fail()
		return
	}
	w.pos++
	w.skipIfExists()
	for {
		if !w.parseDropTarget(kind) {
			w.fail()
			return
		}
		if w.atPunct(",") {
			w.pos++
			continue
		}
		break
	}
	w.skipIf("cascade", "restrict")
}

func (w *walker) parseDropKind() (privacy.IdentifierKind, bool) {
	switch {
	case w.atWord("table"):
		return privacy.KindTable, true
	case w.atWord("index"):
		return privacy.KindIndex, true
	case w.atWord("view"):
		w.markMetadataIncomplete()
		return privacy.KindTable, true
	case w.atWord("database", "schema"):
		return privacy.KindSchema, true
	case w.atWord("sequence"):
		w.markMetadataIncomplete()
		return privacy.KindSchema, true
	case w.atWord("constraint"):
		return privacy.KindConstraint, true
	default:
		return "", false
	}
}

func (w *walker) parseDropTarget(kind privacy.IdentifierKind) bool {
	if !w.isIdentLike() {
		return false
	}
	first, _ := identifierPartAt(w.toks, w.pos)
	firstPos := w.pos
	w.pos++
	if w.atPunct(".") { // Qualified name
		return w.parseQualifiedDropTarget(kind, first, firstPos)
	}
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
	return true
}

func (w *walker) parseQualifiedDropTarget(kind privacy.IdentifierKind, first identifierPart, firstPos int) bool {
	w.pos++
	if !w.isIdentLike() {
		return false
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
	return true
}
