package fingerprint

import (
	"strings"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

// reservedWords are structural reserved words: they participate in clause syntax control and are preserved in anonymized output.
// Non-reserved dictionary words (date/count, etc.) in expression positions are anonymized as column identifiers.
var reservedWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
select from where group by having order limit offset union all distinct as on using
join inner left right full outer cross natural straight_join lateral
asc desc nulls first last
into values value row rows set update delete insert replace merge matched when then
else end case exists not no and or xor in is null like ilike between similar
interval returning conflict do nothing duplicate key for share nowait skip locked
window partition unbounded preceding following current row
cascade restrict create alter drop truncate table view index database schema
sequence add modify change rename column constraint primary unique foreign
references check concurrently materialized temporary temp unlogged with recursive
default collate charset character engine comment
restore
`) {
		reservedWords[w] = true
	}
}

// typeWords are used in CAST targets, DDL column types, and :: type contexts; these words are preserved in those contexts.
var typeWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
int integer tinyint smallint mediumint bigint decimal numeric float double real bit
boolean bool char varchar nchar nvarchar character varying national text tinytext
mediumtext longtext blob tinyblob mediumblob longblob binary varbinary json jsonb
date datetime timestamp timestamptz time timetz year enum serial smallserial bigserial
serial2 serial4 serial8 int2 int4 int8 float4 float8 uuid bytea money xml array
inet cidr macaddr macaddr8 regclass
interval unsigned signed zerofill identity timezone zone
`) {
		typeWords[w] = true
	}
}

func isReserved(w string) bool { return reservedWords[strings.ToLower(w)] }
func isTypeWord(w string) bool { return typeWords[strings.ToLower(w)] }

// builtinFunctions is the closed allowlist for function names that may remain
// visible to the model. Unqualified user-defined functions and functions
// reached through an explicit schema are anonymized.
var builtinFunctions = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
count sum avg min max total group_concat string_agg array_agg bool_and bool_or every
lower upper trim ltrim rtrim length char_length character_length substring substr
replace concat concat_ws coalesce nullif greatest least left right position strpos
abs ceil ceiling floor round trunc mod power pow sqrt exp ln log log10 sign random rand
now current_date current_time current_timestamp localtime localtimestamp
date_add date_sub adddate subdate datediff date_format str_to_date date_trunc
extract age make_interval to_char to_date to_timestamp
if iif
json_extract json_value json_query json_object json_array jsonb_extract_path
jsonb_extract_path_text jsonb_array_elements jsonb_object_keys row_to_json
row_number rank dense_rank percent_rank cume_dist lag lead first_value last_value
nth_value ntile
cast convert numeric
`) {
		builtinFunctions[w] = true
	}
}

func isBuiltinFunction(w string) bool {
	return builtinFunctions[strings.ToLower(w)]
}

var controlledStorageEngines = map[string]bool{
	"archive": true, "blackhole": true, "csv": true, "federated": true,
	"innodb": true, "memory": true, "merge": true, "myisam": true,
	"ndb": true, "ndbcluster": true, "rocksdb": true,
}

var controlledCharsets = map[string]bool{
	"ascii": true, "big5": true, "binary": true, "gb18030": true, "gbk": true,
	"latin1": true, "ucs2": true, "utf8": true, "utf8mb3": true, "utf8mb4": true,
	"utf16": true, "utf16le": true, "utf32": true,
}

var controlledCollations = map[string]bool{
	"binary": true, "default": true,
	"big5_chinese_ci": true, "gb18030_chinese_ci": true, "gbk_chinese_ci": true,
	"latin1_bin": true, "latin1_general_ci": true, "latin1_swedish_ci": true,
	"utf8_bin": true, "utf8_general_ci": true, "utf8_unicode_ci": true,
	"utf8mb3_bin": true, "utf8mb3_general_ci": true, "utf8mb3_unicode_ci": true,
	"utf8mb4_bin": true, "utf8mb4_general_ci": true, "utf8mb4_unicode_ci": true,
	"utf8mb4_0900_ai_ci": true, "utf8mb4_0900_as_ci": true,
	"utf8mb4_0900_as_cs": true, "utf8mb4_0900_bin": true,
}

var controlledIndexMethods = map[string]bool{
	"btree": true, "brin": true, "gin": true, "gist": true, "hash": true,
	"hnsw": true, "ivfflat": true, "rtree": true, "spgist": true,
}

var controlledRowFormats = map[string]bool{
	"default": true, "dynamic": true, "fixed": true, "compressed": true, "redundant": true, "compact": true,
}

var controlledAlterAlgorithms = map[string]bool{
	"default": true, "instant": true, "inplace": true, "copy": true,
}

var controlledAlterLocks = map[string]bool{
	"default": true, "none": true, "shared": true, "exclusive": true,
}

var controlledInsertMethods = map[string]bool{
	"no": true, "first": true, "last": true,
}

func isControlledStorageEngine(w string) bool {
	return controlledStorageEngines[strings.ToLower(w)]
}

func isControlledCharset(w string) bool {
	return controlledCharsets[strings.ToLower(w)]
}

func isControlledCollation(w string) bool {
	return controlledCollations[strings.ToLower(w)]
}

func isControlledIndexMethod(w string) bool {
	return controlledIndexMethods[strings.ToLower(w)]
}

func isControlledRowFormat(w string) bool {
	return controlledRowFormats[strings.ToLower(w)]
}

func isControlledAlterAlgorithm(w string) bool {
	return controlledAlterAlgorithms[strings.ToLower(w)]
}

func isControlledAlterLock(w string) bool {
	return controlledAlterLocks[strings.ToLower(w)]
}

func isControlledInsertMethod(w string) bool {
	return controlledInsertMethods[strings.ToLower(w)]
}

// selectStops are boundary words for the SELECT clause loop.
var selectStops = []string{"from", "where", "group", "having", "order", "limit", "offset", "union", "window", "for", "when"}

// parseSelectBody parses the SELECT body (including FROM/JOIN/WHERE/GROUP/ORDER/LIMIT/lock/UNION).
func (w *walker) parseSelectBody() {
	parent := -1
	if current, ok := w.currentSelectScope(); ok {
		parent = current
	}
	w.parseSelectBodyWithParent(parent)
}

func (w *walker) parseSelectBodyWithParent(parent int) {
	scopeID := w.pushSelectScope(parent)
	defer w.popSelectScope(scopeID)
	if w.atWord("distinct") {
		w.pos++
		if w.atWord("on") {
			w.pos++
			if !w.parseParenExpr() {
				w.fail()
				return
			}
		}
	} else if w.atWord("all") {
		w.pos++
	}
	w.withRelations(RelationRead, RelationRead, func() {
		w.parseExprSeq(true, true, selectStops...)
	})
	for !w.eof() && !w.res.fail {
		if w.atPunct(")") {
			return // Subquery closing is handled by the parenthesis consumer
		}
		switch {
		case w.atWord("from"):
			w.pos++
			w.withRelations(RelationRead, RelationRead, w.parseFromClause)
		case w.atWord("where"):
			w.pos++
			w.withRelations(RelationFilter, RelationFilter, func() {
				w.parseExprSeq(false, true, append(selectStops, "returning")...)
			})
		case w.atWord("group"):
			w.pos++
			if !w.atWord("by") {
				w.fail()
				return
			}
			w.pos++
			w.withRelations(RelationFilter, RelationFilter, func() {
				w.parseExprSeq(false, true, selectStops...)
			})
		case w.atWord("having"):
			w.pos++
			w.withRelations(RelationFilter, RelationFilter, func() {
				w.parseExprSeq(false, true, selectStops...)
			})
		case w.atWord("window"):
			w.pos++
			for {
				if !w.isIdentLike() {
					w.fail()
					return
				}
				w.markRole(privacy.KindAlias, mustName(*w.cur()))
				w.pos++
				if !w.atWord("as") {
					w.fail()
					return
				}
				w.pos++
				if !w.parseParenExpr() {
					w.fail()
					return
				}
				if w.atPunct(",") {
					w.pos++
					continue
				}
				break
			}
		case w.atWord("order"):
			w.pos++
			if !w.atWord("by") {
				w.fail()
				return
			}
			w.pos++
			w.withRelations(RelationFilter, RelationFilter, w.parseOrderItems)
		case w.atWord("limit"):
			w.parseLimitClause()
		case w.atWord("offset"):
			w.pos++
			if !w.atLiteral() {
				w.fail()
				return
			}
			w.pos++
			w.skipIf("row", "rows")
		case w.atWord("union"):
			w.pos++
			w.skipIf("all", "distinct")
			if !w.atWord("select") {
				w.fail()
				return
			}
			w.pos++
			// UNION branches are peer query blocks and do not inherit FROM relations from the preceding branch.
			w.parseSelectBodyWithParent(parent)
		case w.atWord("for"):
			w.parseLockClause()
		default:
			w.fail()
			return
		}
	}
}

// atLiteral reports whether the current position is a literal token (including null/true/false word literals).
func (w *walker) atLiteral() bool {
	t := w.cur()
	if t == nil {
		return false
	}
	switch t.Kind {
	case TokString, TokNumber, TokDollarString, TokParam:
		return true
	case TokWord:
		return t.Text == "null" || t.Text == "true" || t.Text == "false"
	}
	return false
}

// parseFromClause parses table references and JOIN chains.
func (w *walker) parseFromClause() {
	if w.eof() || w.atWord(selectStops...) || w.atWord("returning") || w.atPunct(")") {
		return
	}
	w.tableRelation = RelationRead
	w.columnRelation = RelationRead
	if !w.parseTableRef(true) {
		w.fail()
		return
	}
	for {
		if w.eof() {
			return
		}
		if w.atPunct(",") {
			w.pos++
			w.tableRelation = RelationRead
			w.columnRelation = RelationRead
			if !w.parseTableRef(true) {
				w.fail()
				return
			}
			continue
		}
		if w.atWord("join", "inner", "left", "right", "full", "cross", "natural", "straight_join", "lateral") {
			for w.atWord("join", "inner", "left", "right", "full", "outer", "cross", "natural", "straight_join", "lateral") {
				w.pos++
			}
			w.tableRelation = RelationJoin
			w.columnRelation = RelationFilter
			if !w.parseTableRef(true) {
				w.fail()
				return
			}
			if w.atWord("on") {
				w.pos++
				w.withRelations(RelationJoin, RelationFilter, func() {
					w.parseExprSeq(false, true, selectStops...)
				})
			} else if w.atWord("using") {
				w.pos++
				w.columnRelation = RelationFilter
				if !w.parseColumnListParen() {
					w.fail()
					return
				}
			}
			continue
		}
		return
	}
}

// parseOrderItems parses ORDER BY items: expr [COLLATE x] [ASC|DESC] [NULLS FIRST|LAST].
func (w *walker) parseOrderItems() {
	for {
		w.parseExprSeq(false, false, "asc", "desc", "nulls", "limit", "offset", "for", "union", "returning")
		if w.atWord("collate") {
			w.pos++
			if w.isIdentLike() {
				if isControlledCollation(w.cur().Text) {
					w.keepRawAt(w.pos)
				} else {
					w.markMetadataIncomplete()
				}
				w.pos++
			} else if w.cur() != nil && w.cur().Kind == TokString {
				w.markMetadataIncomplete()
				w.pos++
			} else {
				w.fail()
				return
			}
		}
		w.skipIf("asc", "desc")
		if w.atWord("nulls") {
			w.pos++
			if !w.skipIf("first", "last") {
				w.fail()
				return
			}
		}
		if w.atPunct(",") {
			w.pos++
			continue
		}
		break
	}
}

// parseLimitClause parses LIMIT: MySQL LIMIT [off,]cnt / PG LIMIT cnt [OFFSET n].
func (w *walker) parseLimitClause() {
	w.pos++ // limit
	if w.atWord("all") {
		w.pos++
		return
	}
	if !w.atLiteral() {
		w.fail()
		return
	}
	first := w.pos
	w.pos++
	if w.atPunct(",") {
		// MySQL two-argument form: LIMIT offset, count -> canonicalized as limit count offset offset.
		w.pos++
		if !w.atLiteral() {
			w.fail()
			return
		}
		w.pos++
		w.res.specials = append(w.res.specials, specialSpan{kind: specialLimitComma, tokStart: first, tokEnd: w.pos})
	}
}

// parseLockClause parses FOR UPDATE/SHARE [OF tbls] [NOWAIT|SKIP LOCKED] and MySQL LOCK IN SHARE MODE.
func (w *walker) parseLockClause() {
	w.pos++ // for
	if !w.atWord("update", "share") {
		w.fail()
		return
	}
	lockKind := mustName(*w.cur())
	w.pos++
	w.res.features = append(w.res.features, "lock:"+strings.ToLower(lockKind))
	if w.atWord("of") {
		w.pos++
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
	}
	if w.atWord("nowait") {
		w.pos++
		w.res.features = append(w.res.features, "lock:nowait")
	} else if w.atWord("skip") {
		w.pos++
		if !w.atWord("locked") {
			w.fail()
			return
		}
		w.pos++
		w.res.features = append(w.res.features, "lock:skip_locked")
	}
}

// parseInsertBody parses the INSERT/REPLACE body.
func (w *walker) parseInsertBody() {
	w.skipIf("ignore")
	if w.atWord("into") {
		w.pos++
	}
	w.tableRelation = RelationWrite
	w.columnRelation = RelationWrite
	if !w.parseTableRef(false) {
		w.fail()
		return
	}
	if w.atPunct("(") {
		w.columnRelation = RelationWrite
		if !w.parseColumnListParen() {
			w.fail()
			return
		}
	}
	switch {
	case w.atWord("values", "value"):
		w.pos++
		w.parseValuesTuples()
	case w.atWord("select"):
		w.pos++
		w.withRelations(RelationRead, RelationRead, w.parseSelectBody)
	case w.atWord("set"): // MySQL INSERT ... SET a=1
		w.pos++
		w.withRelations(RelationWrite, RelationWrite, w.parseSetAssignments)
	default:
		w.fail()
		return
	}
	w.parseInsertTails()
}

// parseValuesTuples parses VALUES (...),(...) and collapses tuple cardinality.
func (w *walker) parseValuesTuples() {
	tupleStart := w.pos
	count := 0
	for {
		if !w.atPunct("(") {
			w.fail()
			return
		}
		w.pos++
		count++
		w.parseExprSeq(false, true, ")")
		if w.res.fail {
			return
		}
		if !w.consumeClose() {
			w.fail()
			return
		}
		if w.atPunct(",") {
			w.pos++
			continue
		}
		break
	}
	w.res.specials = append(w.res.specials, specialSpan{kind: specialValuesTuples, tokStart: tupleStart, tokEnd: w.pos, count: count})
}

// parseInsertTails parses ON CONFLICT / ON DUPLICATE KEY UPDATE / RETURNING.
func (w *walker) parseInsertTails() {
	for !w.eof() {
		switch {
		case w.atWord("on"):
			w.pos++
			if w.atWord("conflict") {
				w.pos++
				w.res.features = append(w.res.features, "on_conflict")
				if w.atPunct("(") {
					w.columnRelation = RelationFilter
					if !w.parseColumnListParen() {
						w.fail()
						return
					}
				}
				if w.atWord("on") {
					w.pos++
					w.parseExprSeq(false, true, "do", "where", "returning")
				}
				if !w.atWord("do") {
					w.fail()
					return
				}
				w.pos++
				switch {
				case w.atWord("nothing"):
					w.pos++
				case w.atWord("update"):
					w.pos++
					if !w.atWord("set") {
						w.fail()
						return
					}
					w.pos++
					w.withRelations(RelationWrite, RelationWrite, w.parseSetAssignments)
				default:
					w.fail()
					return
				}
			} else if w.atWord("duplicate") {
				w.pos++
				if !w.atWord("key") {
					w.fail()
					return
				}
				w.pos++
				if !w.atWord("update") {
					w.fail()
					return
				}
				w.pos++
				w.res.features = append(w.res.features, "on_duplicate_key_update")
				w.withRelations(RelationWrite, RelationWrite, w.parseSetAssignments)
			} else {
				w.fail()
				return
			}
		case w.atWord("returning"):
			w.pos++
			w.res.features = append(w.res.features, "returning")
			w.withRelations(RelationRead, RelationRead, func() {
				w.parseExprSeq(true, true, selectStops...)
			})
		default:
			return
		}
	}
}

// parseUpdateBody parses the UPDATE body.
func (w *walker) parseUpdateBody() {
	if w.atWord("only") { // PG UPDATE ONLY tbl
		w.pos++
	}
	for { // MySQL UPDATE t1, t2 SET ...
		w.tableRelation = RelationTarget
		w.columnRelation = RelationFilter
		if !w.parseTableRef(true) {
			w.fail()
			return
		}
		if w.atPunct(",") {
			w.pos++
			continue
		}
		break
	}
	if !w.atWord("set") {
		w.fail()
		return
	}
	w.pos++
	w.withRelations(RelationWrite, RelationWrite, w.parseSetAssignments)
	if w.atWord("from") { // PG UPDATE ... FROM
		w.pos++
		w.withRelations(RelationRead, RelationRead, w.parseFromClause)
	}
	if w.atWord("where") {
		w.pos++
		w.withRelations(RelationFilter, RelationFilter, func() {
			w.parseExprSeq(false, true, "returning")
		})
	}
	if w.atWord("returning") {
		w.pos++
		w.res.features = append(w.res.features, "returning")
		w.withRelations(RelationRead, RelationRead, func() {
			w.parseExprSeq(true, true)
		})
	}
}

// parseSetAssignments parses SET col=expr[, ...].
func (w *walker) parseSetAssignments() {
	for {
		if !w.parseColumnName() {
			w.fail()
			return
		}
		if !w.atPunct("=") && !w.atPunct(":=") {
			w.fail()
			return
		}
		w.pos++
		w.parseExprSeq(false, false, "where", "returning", "from", "on", "when")
		if w.atPunct(",") {
			w.pos++
			continue
		}
		break
	}
}

// parseDeleteBody parses the DELETE body.
func (w *walker) parseDeleteBody() {
	if !w.atWord("from") {
		w.fail()
		return
	}
	w.pos++
	// MySQL multi-table delete: DELETE t1, t2 FROM ...
	for {
		w.tableRelation = RelationTarget
		w.columnRelation = RelationFilter
		if !w.parseTableRef(true) {
			w.fail()
			return
		}
		if w.atPunct(",") {
			w.pos++
			continue
		}
		break
	}
	if w.atWord("using") {
		w.pos++
		w.withRelations(RelationRead, RelationRead, w.parseFromClause)
	}
	if w.atWord("where") {
		w.pos++
		w.withRelations(RelationFilter, RelationFilter, func() {
			w.parseExprSeq(false, true, "returning")
		})
	}
	if w.atWord("returning") {
		w.pos++
		w.res.features = append(w.res.features, "returning")
		w.withRelations(RelationRead, RelationRead, func() {
			w.parseExprSeq(true, true)
		})
	}
}

// parseMergeBody parses the MERGE body.
func (w *walker) parseMergeBody() {
	if !w.atWord("into") {
		w.fail()
		return
	}
	w.pos++
	w.tableRelation = RelationWrite
	w.columnRelation = RelationWrite
	if !w.parseTableRef(false) {
		w.fail()
		return
	}
	if !w.atWord("using") {
		w.fail()
		return
	}
	w.pos++
	w.tableRelation = RelationRead
	w.columnRelation = RelationRead
	if !w.parseTableRef(true) {
		w.fail()
		return
	}
	if !w.atWord("on") {
		w.fail()
		return
	}
	w.pos++
	w.withRelations(RelationFilter, RelationFilter, func() {
		w.parseExprSeq(false, true, "when")
	})
	for w.atWord("when") {
		w.pos++
		if w.atWord("not") {
			w.pos++
		}
		if !w.atWord("matched") {
			w.fail()
			return
		}
		w.pos++
		if w.atWord("and") {
			w.pos++
			w.parseExprSeq(false, true, "then")
		}
		if !w.atWord("then") {
			w.fail()
			return
		}
		w.pos++
		switch {
		case w.atWord("update"):
			w.pos++
			if !w.atWord("set") {
				w.fail()
				return
			}
			w.pos++
			w.withRelations(RelationWrite, RelationWrite, w.parseSetAssignments)
		case w.atWord("insert"):
			w.pos++
			if w.atPunct("(") {
				w.columnRelation = RelationWrite
				if !w.parseColumnListParen() {
					w.fail()
					return
				}
			}
			if w.atWord("values", "value") {
				w.pos++
				w.parseValuesTuples()
			}
		case w.atWord("delete"):
			w.pos++
		default:
			w.fail()
			return
		}
	}
}
