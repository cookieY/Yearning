
/*
 * Copyright 2018 Xiaomi, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package soar

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"vitess.io/vitess/go/vt/sqlparser"
)

// TokenType
const (
	TokenTypeWhitespace       = 0
	TokenTypeWord             = 1
	TokenTypeQuote            = 2
	TokenTypeBacktickQuote    = 3
	TokenTypeReserved         = 4
	TokenTypeReservedToplevel = 5
	TokenTypeReservedNewline  = 6
	TokenTypeBoundary         = 7
	TokenTypeComment          = 8
	TokenTypeBlockComment     = 9
	TokenTypeNumber           = 10
	TokenTypeError            = 11
	TokenTypeVariable         = 12
)

var maxCachekeySize = 15
var cacheHits int
var cacheMisses int
var tokenCache map[string]Token

var tokenBoundaries = []string{
	// multi character
	"(>=)", "(<=)", "(!=)", "(<>)",
	// single characters
	",", ";", ":", "\\)", "\\(", "\\.", "=", "<", ">", "\\+", "-", "\\*", "/", "!", "\\^", "%", "\\|", "&", "#",
}

var tokenReserved = []string{
	"ACCESSIBLE", "ACTION", "AGAINST", "AGGREGATE", "ALGORITHM", "ALL", "ALTER", "ANALYSE", "ANALYZE", "AS", "ASC",
	"AUTOCOMMIT", "AUTO_INCREMENT", "BACKUP", "BEGIN", "BETWEEN", "BINLOG", "BOTH", "CASCADE", "CASE", "CHANGE", "CHANGED", "CHARACTER SET",
	"CHARSET", "CHECK", "CHECKSUM", "COLLATE", "COLLATION", "COLUMN", "COLUMNS", "COMMENT", "COMMIT", "COMMITTED", "COMPRESSED", "CONCURRENT",
	"CONSTRAINT", "CONTAINS", "CONVERT", "CREATE", "CROSS", "CURRENT_TIMESTAMP", "DATABASE", "DATABASES", "DAY", "DAY_HOUR", "DAY_MINUTE",
	"DAY_SECOND", "DEFAULT", "DEFINER", "DELAYED", "DELETE", "DESC", "DESCRIBE", "DETERMINISTIC", "DISTINCT", "DISTINCTROW", "DIV",
	"DO", "DUMPFILE", "DUPLICATE", "DYNAMIC", "ELSE", "ENCLOSED", "END", "ENGINE", "ENGINE_TYPE", "ENGINES", "ESCAPE", "ESCAPED", "EVENTS", "EXEC",
	"EXECUTE", "EXISTS", "EXPLAIN", "EXTENDED", "FAST", "FIELDS", "FILE", "FIRST", "FIXED", "FLUSH", "FOR", "FORCE", "FOREIGN", "FULL", "FULLTEXT",
	"FUNCTION", "GLOBAL", "GRANT", "GRANTS", "GROUP_CONCAT", "HEAP", "HIGH_PRIORITY", "HOSTS", "HOUR", "HOUR_MINUTE",
	"HOUR_SECOND", "IDENTIFIED", "IF", "IFNULL", "IGNORE", "IN", "INDEX", "INDEXES", "INFILE", "INSERT", "INSERT_ID", "INSERT_METHOD", "INTERVAL",
	"INTO", "INVOKER", "IS", "ISOLATION", "KEY", "KEYS", "KILL", "LAST_INSERT_ID", "LEADING", "LEVEL", "LIKE", "LINEAR",
	"LINES", "LOAD", "LOCAL", "LOCK", "LOCKS", "LOGS", "LOW_PRIORITY", "MARIA", "MASTER", "MASTER_CONNECT_RETRY", "MASTER_HOST", "MASTER_LOG_FILE",
	"MATCH", "MAX_CONNECTIONS_PER_HOUR", "MAX_QUERIES_PER_HOUR", "MAX_ROWS", "MAX_UPDATES_PER_HOUR", "MAX_USER_CONNECTIONS",
	"MEDIUM", "MERGE", "MINUTE", "MINUTE_SECOND", "MIN_ROWS", "MODE", "MODIFY",
	"MONTH", "MRG_MYISAM", "MYISAM", "NAMES", "NATURAL", "NOT", "NOW()", "NULL", "OFFSET", "ON", "OPEN", "OPTIMIZE", "OPTION", "OPTIONALLY",
	"ON UPDATE", "ON DELETE", "OUTFILE", "PACK_KEYS", "PAGE", "PARTIAL", "PARTITION", "PARTITIONS", "PASSWORD", "PRIMARY", "PRIVILEGES", "PROCEDURE",
	"PROCESS", "PROCESSLIST", "PURGE", "QUICK", "RANGE", "RAID0", "RAID_CHUNKS", "RAID_CHUNKSIZE", "RAID_TYPE", "READ", "READ_ONLY",
	"READ_WRITE", "REFERENCES", "REGEXP", "RELOAD", "RENAME", "REPAIR", "REPEATABLE", "REPLACE", "REPLICATION", "RESET", "RESTORE", "RESTRICT",
	"RETURN", "RETURNS", "REVOKE", "RLIKE", "ROLLBACK", "ROW", "ROWS", "ROW_FORMAT", "SECOND", "SECURITY", "SEPARATOR",
	"SERIALIZABLE", "SESSION", "SHARE", "SHOW", "SHUTDOWN", "SLAVE", "SONAME", "SOUNDS", "SQL", "SQL_AUTO_IS_NULL", "SQL_BIG_RESULT",
	"SQL_BIG_SELECTS", "SQL_BIG_TABLES", "SQL_BUFFER_RESULT", "SQL_CALC_FOUND_ROWS", "SQL_LOG_BIN", "SQL_LOG_OFF", "SQL_LOG_UPDATE",
	"SQL_LOW_PRIORITY_UPDATES", "SQL_MAX_JOIN_SIZE", "SQL_QUOTE_SHOW_CREATE", "SQL_SAFE_UPDATES", "SQL_SELECT_LIMIT", "SQL_SLAVE_SKIP_COUNTER",
	"SQL_SMALL_RESULT", "SQL_WARNINGS", "SQL_CACHE", "SQL_NO_CACHE", "START", "STARTING", "STATUS", "STOP", "STORAGE",
	"STRAIGHT_JOIN", "STRING", "STRIPED", "SUPER", "TABLE", "TABLES", "TEMPORARY", "TERMINATED", "THEN", "TO", "TRAILING", "TRANSACTIONAL", "TRUE",
	"TRUNCATE", "TYPE", "TYPES", "UNCOMMITTED", "UNIQUE", "UNLOCK", "UNSIGNED", "USAGE", "USE", "USING", "VARIABLES",
	"VIEW", "WHEN", "WITH", "WORK", "WRITE", "YEAR_MONTH",
}

var tokenReservedTopLevel = []string{
	"SELECT", "FROM", "WHERE", "SET", "ORDER BY", "GROUP BY", "LIMIT", "DROP",
	"VALUES", "UPDATE", "HAVING", "ADD", "AFTER", "ALTER TABLE", "DELETE FROM", "UNION ALL", "UNION", "EXCEPT", "INTERSECT",
}

var tokenFunction = []string{
	"ABS", "ACOS", "ADDDATE", "ADDTIME", "AES_DECRYPT", "AES_ENCRYPT", "AREA", "ASBINARY", "ASCII", "ASIN", "ASTEXT", "ATAN", "ATAN2",
	"AVG", "BDMPOLYFROMTEXT", "BDMPOLYFROMWKB", "BDPOLYFROMTEXT", "BDPOLYFROMWKB", "BENCHMARK", "BIN", "BIT_AND", "BIT_COUNT", "BIT_LENGTH",
	"BIT_OR", "BIT_XOR", "BOUNDARY", "BUFFER", "CAST", "CEIL", "CEILING", "CENTROID", "CHAR", "CHARACTER_LENGTH", "CHARSET", "CHAR_LENGTH",
	"COALESCE", "COERCIBILITY", "COLLATION", "COMPRESS", "CONCAT", "CONCAT_WS", "CONNECTION_ID", "CONTAINS", "CONV", "CONVERT", "CONVERT_TZ",
	"CONVEXHULL", "COS", "COT", "COUNT", "CRC32", "CROSSES", "CURDATE", "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_USER",
	"CURTIME", "DATABASE", "DATE", "DATEDIFF", "DATE_ADD", "DATE_DIFF", "DATE_FORMAT", "DATE_SUB", "DAY", "DAYNAME", "DAYOFMONTH", "DAYOFWEEK",
	"DAYOFYEAR", "DECODE", "DEFAULT", "DEGREES", "DES_DECRYPT", "DES_ENCRYPT", "DIFFERENCE", "DIMENSION", "DISJOINT", "DISTANCE", "ELT", "ENCODE",
	"ENCRYPT", "ENDPOINT", "ENVELOPE", "EQUALS", "EXP", "EXPORT_SET", "EXTERIORRING", "EXTRACT", "EXTRACTVALUE", "FIELD", "FIND_IN_SET", "FLOOR",
	"FORMAT", "FOUND_ROWS", "FROM_DAYS", "FROM_UNIXTIME", "GEOMCOLLFROMTEXT", "GEOMCOLLFROMWKB", "GEOMETRYCOLLECTION", "GEOMETRYCOLLECTIONFROMTEXT",
	"GEOMETRYCOLLECTIONFROMWKB", "GEOMETRYFROMTEXT", "GEOMETRYFROMWKB", "GEOMETRYN", "GEOMETRYTYPE", "GEOMFROMTEXT", "GEOMFROMWKB", "GET_FORMAT",
	"GET_LOCK", "GLENGTH", "GREATEST", "GROUP_CONCAT", "GROUP_UNIQUE_USERS", "HEX", "HOUR", "IF", "IFNULL", "INET_ATON", "INET_NTOA", "INSERT", "INSTR",
	"INTERIORRINGN", "INTERSECTION", "INTERSECTS", "INTERVAL", "ISCLOSED", "ISEMPTY", "ISNULL", "ISRING", "ISSIMPLE", "IS_FREE_LOCK", "IS_USED_LOCK",
	"LAST_DAY", "LAST_INSERT_ID", "LCASE", "LEAST", "LEFT", "LENGTH", "LINEFROMTEXT", "LINEFROMWKB", "LINESTRING", "LINESTRINGFROMTEXT", "LINESTRINGFROMWKB",
	"LN", "LOAD_FILE", "LOCALTIME", "LOCALTIMESTAMP", "LOCATE", "LOG", "LOG10", "LOG2", "LOWER", "LPAD", "LTRIM", "MAKEDATE", "MAKETIME", "MAKE_SET",
	"MASTER_POS_WAIT", "MAX", "MBRCONTAINS", "MBRDISJOINT", "MBREQUAL", "MBRINTERSECTS", "MBROVERLAPS", "MBRTOUCHES", "MBRWITHIN", "MD5", "MICROSECOND",
	"MID", "MIN", "MINUTE", "MLINEFROMTEXT", "MLINEFROMWKB", "MOD", "MONTH", "MONTHNAME", "MPOINTFROMTEXT", "MPOINTFROMWKB", "MPOLYFROMTEXT", "MPOLYFROMWKB",
	"MULTILINESTRING", "MULTILINESTRINGFROMTEXT", "MULTILINESTRINGFROMWKB", "MULTIPOINT", "MULTIPOINTFROMTEXT", "MULTIPOINTFROMWKB", "MULTIPOLYGON",
	"MULTIPOLYGONFROMTEXT", "MULTIPOLYGONFROMWKB", "NAME_CONST", "NULLIF", "NUMGEOMETRIES", "NUMINTERIORRINGS", "NUMPOINTS", "OCT", "OCTET_LENGTH",
	"OLD_PASSWORD", "ORD", "OVERLAPS", "PASSWORD", "PERIOD_ADD", "PERIOD_DIFF", "PI", "POINT", "POINTFROMTEXT", "POINTFROMWKB", "POINTN", "POINTONSURFACE",
	"POLYFROMTEXT", "POLYFROMWKB", "POLYGON", "POLYGONFROMTEXT", "POLYGONFROMWKB", "POSITION", "POW", "POWER", "QUARTER", "QUOTE", "RADIANS", "RAND",
	"RELATED", "RELEASE_LOCK", "REPEAT", "REPLACE", "REVERSE", "RIGHT", "ROUND", "ROW_COUNT", "RPAD", "RTRIM", "SCHEMA", "SECOND", "SEC_TO_TIME",
	"SESSION_USER", "SHA", "SHA1", "SIGN", "SIN", "SLEEP", "SOUNDEX", "SPACE", "SQRT", "SRID", "STARTPOINT", "STD", "STDDEV", "STDDEV_POP", "STDDEV_SAMP",
	"STRCMP", "STR_TO_DATE", "SUBDATE", "SUBSTR", "SUBSTRING", "SUBSTRING_INDEX", "SUBTIME", "SUM", "SYMDIFFERENCE", "SYSDATE", "SYSTEM_USER", "TAN",
	"TIME", "TIMEDIFF", "TIMESTAMP", "TIMESTAMPADD", "TIMESTAMPDIFF", "TIME_FORMAT", "TIME_TO_SEC", "TOUCHES", "TO_DAYS", "TRIM", "TRUNCATE", "UCASE",
	"UNCOMPRESS", "UNCOMPRESSED_LENGTH", "UNHEX", "UNIQUE_USERS", "UNIX_TIMESTAMP", "UPDATEXML", "UPPER", "USER", "UTC_DATE", "UTC_TIME", "UTC_TIMESTAMP",
	"UUID", "VARIANCE", "VAR_POP", "VAR_SAMP", "VERSION", "WEEK", "WEEKDAY", "WEEKOFYEAR", "WITHIN", "X", "Y", "YEAR", "YEARWEEK",
}

var tokenReservedNewLine = []string{
	"LEFT OUTER JOIN", "RIGHT OUTER JOIN", "LEFT JOIN", "RIGHT JOIN", "OUTER JOIN", "INNER JOIN", "JOIN", "XOR", "OR", "AND",
}

var regBoundariesString string
var regReservedToplevelString string
var regReservedNewlineString string
var regReservedString string
var regFunctionString string

func init() {
	var regs []string
	regs = append(regs, tokenBoundaries...)
	regBoundariesString = "(" + strings.Join(regs, "|") + ")"

	regs = make([]string, 0)
	for _, reg := range tokenReservedTopLevel {
		regs = append(regs, regexp.QuoteMeta(reg))
	}
	regReservedToplevelString = "(" + strings.Join(regs, "|") + ")"

	regs = make([]string, 0)
	for _, reg := range tokenReservedNewLine {
		regs = append(regs, regexp.QuoteMeta(reg))
	}
	regReservedNewlineString = "(" + strings.Join(regs, "|") + ")"

	regs = make([]string, 0)
	for _, reg := range tokenReserved {
		regs = append(regs, regexp.QuoteMeta(reg))
	}
	regReservedString = "(" + strings.Join(regs, "|") + ")"

	regs = make([]string, 0)
	for _, reg := range tokenFunction {
		regs = append(regs, regexp.QuoteMeta(reg))
	}
	regFunctionString = "(" + strings.Join(regs, "|") + ")"
}

// TokenString sqlparser tokens
var TokenString = map[int]string{
	sqlparser.LEX_ERROR:               "",
	sqlparser.UNION:                   "union",
	sqlparser.SELECT:                  "select",
	sqlparser.STREAM:                  "stream",
	sqlparser.INSERT:                  "insert",
	sqlparser.UPDATE:                  "update",
	sqlparser.DELETE:                  "delete",
	sqlparser.FROM:                    "from",
	sqlparser.WHERE:                   "where",
	sqlparser.GROUP:                   "group",
	sqlparser.HAVING:                  "having",
	sqlparser.ORDER:                   "order",
	sqlparser.BY:                      "by",
	sqlparser.LIMIT:                   "limit",
	sqlparser.OFFSET:                  "offset",
	sqlparser.FOR:                     "for",
	sqlparser.ALL:                     "all",
	sqlparser.DISTINCT:                "distinct",
	sqlparser.AS:                      "as",
	sqlparser.EXISTS:                  "exists",
	sqlparser.ASC:                     "asc",
	sqlparser.DESC:                    "desc",
	sqlparser.INTO:                    "into",
	sqlparser.DUPLICATE:               "duplicate",
	sqlparser.KEY:                     "key",
	sqlparser.DEFAULT:                 "default",
	sqlparser.SET:                     "set",
	sqlparser.LOCK:                    "lock",
	sqlparser.KEYS:                    "keys",
	sqlparser.VALUES:                  "values",
	sqlparser.LAST_INSERT_ID:          "last_insert_id",
	sqlparser.NEXT:                    "next",
	sqlparser.VALUE:                   "value",
	sqlparser.SHARE:                   "share",
	sqlparser.MODE:                    "mode",
	sqlparser.SQL_NO_CACHE:            "sql_no_cache",
	sqlparser.SQL_CACHE:               "sql_cache",
	sqlparser.JOIN:                    "join",
	sqlparser.STRAIGHT_JOIN:           "straight_join",
	sqlparser.LEFT:                    "left",
	sqlparser.RIGHT:                   "right",
	sqlparser.INNER:                   "inner",
	sqlparser.OUTER:                   "outer",
	sqlparser.CROSS:                   "cross",
	sqlparser.NATURAL:                 "natural",
	sqlparser.USE:                     "use",
	sqlparser.FORCE:                   "force",
	sqlparser.ON:                      "on",
	sqlparser.USING:                   "using",
	sqlparser.ID:                      "id",
	sqlparser.HEX:                     "hex",
	sqlparser.STRING:                  "string",
	sqlparser.INTEGRAL:                "integral",
	sqlparser.FLOAT:                   "float",
	sqlparser.HEXNUM:                  "hexnum",
	sqlparser.VALUE_ARG:               "?",
	sqlparser.LIST_ARG:                ":",
	sqlparser.COMMENT:                 "",
	sqlparser.COMMENT_KEYWORD:         "comment",
	sqlparser.BIT_LITERAL:             "bit_literal",
	sqlparser.NULL:                    "null",
	sqlparser.TRUE:                    "true",
	sqlparser.FALSE:                   "false",
	sqlparser.OR:                      "||",
	sqlparser.AND:                     "&&",
	sqlparser.NOT:                     "not",
	sqlparser.BETWEEN:                 "between",
	sqlparser.CASE:                    "case",
	sqlparser.WHEN:                    "when",
	sqlparser.THEN:                    "then",
	sqlparser.ELSE:                    "else",
	sqlparser.END:                     "end",
	sqlparser.LE:                      "<",
	sqlparser.GE:                      ">=",
	sqlparser.NE:                      "<>",
	sqlparser.NULL_SAFE_EQUAL:         "<=>",
	sqlparser.IS:                      "is",
	sqlparser.LIKE:                    "like",
	sqlparser.REGEXP:                  "regexp",
	sqlparser.IN:                      "in",
	sqlparser.SHIFT_LEFT:              "<<",
	sqlparser.SHIFT_RIGHT:             ">>",
	sqlparser.DIV:                     "div",
	sqlparser.MOD:                     "mod",
	sqlparser.UNARY:                   "unary",
	sqlparser.COLLATE:                 "collate",
	sqlparser.BINARY:                  "binary",
	sqlparser.UNDERSCORE_BINARY:       "_binary",
	sqlparser.INTERVAL:                "interval",
	sqlparser.JSON_EXTRACT_OP:         "->>",
	sqlparser.JSON_UNQUOTE_EXTRACT_OP: "->",
	sqlparser.CREATE:                  "create",
	sqlparser.ALTER:                   "alter",
	sqlparser.DROP:                    "drop",
	sqlparser.RENAME:                  "rename",
	sqlparser.ANALYZE:                 "analyze",
	sqlparser.ADD:                     "add",
	sqlparser.SCHEMA:                  "schema",
	sqlparser.TABLE:                   "table",
	sqlparser.INDEX:                   "index",
	sqlparser.VIEW:                    "view",
	sqlparser.TO:                      "to",
	sqlparser.IGNORE:                  "ignore",
	sqlparser.IF:                      "if",
	sqlparser.UNIQUE:                  "unique",
	sqlparser.PRIMARY:                 "primary",
	sqlparser.COLUMN:                  "column",
	sqlparser.CONSTRAINT:              "constraint",
	sqlparser.SPATIAL:                 "spatial",
	sqlparser.FULLTEXT:                "fulltext",
	sqlparser.FOREIGN:                 "foreign",
	sqlparser.SHOW:                    "show",
	sqlparser.DESCRIBE:                "describe",
	sqlparser.EXPLAIN:                 "explain",
	sqlparser.DATE:                    "date",
	sqlparser.ESCAPE:                  "escape",
	sqlparser.REPAIR:                  "repair",
	sqlparser.OPTIMIZE:                "optimize",
	sqlparser.TRUNCATE:                "truncate",
	sqlparser.MAXVALUE:                "maxvalue",
	sqlparser.PARTITION:               "partition",
	sqlparser.REORGANIZE:              "reorganize",
	sqlparser.LESS:                    "less",
	sqlparser.THAN:                    "than",
	sqlparser.PROCEDURE:               "procedure",
	sqlparser.TRIGGER:                 "trigger",
	sqlparser.VINDEX:                  "vindex",
	sqlparser.VINDEXES:                "vindexes",
	sqlparser.STATUS:                  "status",
	sqlparser.VARIABLES:               "variables",
	sqlparser.BEGIN:                   "begin",
	sqlparser.START:                   "start",
	sqlparser.TRANSACTION:             "transaction",
	sqlparser.COMMIT:                  "commit",
	sqlparser.ROLLBACK:                "rollback",
	sqlparser.BIT:                     "bit",
	sqlparser.TINYINT:                 "tinyint",
	sqlparser.SMALLINT:                "smallint",
	sqlparser.MEDIUMINT:               "mediumint",
	sqlparser.INT:                     "int",
	sqlparser.INTEGER:                 "integer",
	sqlparser.BIGINT:                  "bigint",
	sqlparser.INTNUM:                  "intnum",
	sqlparser.REAL:                    "real",
	sqlparser.DOUBLE:                  "double",
	sqlparser.FLOAT_TYPE:              "float_type",
	sqlparser.DECIMAL:                 "decimal",
	sqlparser.NUMERIC:                 "numeric",
	sqlparser.TIME:                    "time",
	sqlparser.TIMESTAMP:               "timestamp",
	sqlparser.DATETIME:                "datetime",
	sqlparser.YEAR:                    "year",
	sqlparser.CHAR:                    "char",
	sqlparser.VARCHAR:                 "varchar",
	sqlparser.BOOL:                    "bool",
	sqlparser.CHARACTER:               "character",
	sqlparser.VARBINARY:               "varbinary",
	sqlparser.NCHAR:                   "nchar",
	sqlparser.TEXT:                    "text",
	sqlparser.TINYTEXT:                "tinytext",
	sqlparser.MEDIUMTEXT:              "mediumtext",
	sqlparser.LONGTEXT:                "longtext",
	sqlparser.BLOB:                    "blob",
	sqlparser.TINYBLOB:                "tinyblob",
	sqlparser.MEDIUMBLOB:              "mediumblob",
	sqlparser.LONGBLOB:                "longblob",
	sqlparser.JSON:                    "json",
	sqlparser.ENUM:                    "enum",
	sqlparser.GEOMETRY:                "geometry",
	sqlparser.POINT:                   "point",
	sqlparser.LINESTRING:              "linestring",
	sqlparser.POLYGON:                 "polygon",
	sqlparser.GEOMETRYCOLLECTION:      "geometrycollection",
	sqlparser.MULTIPOINT:              "multipoint",
	sqlparser.MULTILINESTRING:         "multilinestring",
	sqlparser.MULTIPOLYGON:            "multipolygon",
	sqlparser.NULLX:                   "nullx",
	sqlparser.AUTO_INCREMENT:          "auto_increment",
	sqlparser.APPROXNUM:               "approxnum",
	sqlparser.SIGNED:                  "signed",
	sqlparser.UNSIGNED:                "unsigned",
	sqlparser.ZEROFILL:                "zerofill",
	sqlparser.DATABASES:               "databases",
	sqlparser.TABLES:                  "tables",
	sqlparser.VITESS_KEYSPACES:        "vitess_keyspaces",
	sqlparser.VITESS_SHARDS:           "vitess_shards",
	sqlparser.VITESS_TABLETS:          "vitess_tablets",
	sqlparser.VSCHEMA_TABLES:          "vschema_tables",
	sqlparser.NAMES:                   "names",
	sqlparser.CHARSET:                 "charset",
	sqlparser.GLOBAL:                  "global",
	sqlparser.SESSION:                 "session",
	sqlparser.CURRENT_TIMESTAMP:       "current_timestamp",
	sqlparser.DATABASE:                "database",
	sqlparser.CURRENT_DATE:            "current_date",
	sqlparser.CURRENT_TIME:            "current_time",
	sqlparser.LOCALTIME:               "localtime",
	sqlparser.LOCALTIMESTAMP:          "localtimestamp",
	sqlparser.UTC_DATE:                "utc_date",
	sqlparser.UTC_TIME:                "utc_time",
	sqlparser.UTC_TIMESTAMP:           "utc_timestamp",
	sqlparser.REPLACE:                 "replace",
	sqlparser.CONVERT:                 "convert",
	sqlparser.CAST:                    "cast",
	sqlparser.SUBSTR:                  "substr",
	sqlparser.SUBSTRING:               "substring",
	sqlparser.GROUP_CONCAT:            "group_concat",
	sqlparser.SEPARATOR:               "separator",
	sqlparser.MATCH:                   "match",
	sqlparser.AGAINST:                 "against",
	sqlparser.BOOLEAN:                 "boolean",
	sqlparser.LANGUAGE:                "language",
	sqlparser.WITH:                    "with",
	sqlparser.QUERY:                   "query",
	sqlparser.EXPANSION:               "expansion",
	sqlparser.UNUSED:                  "",
}

// 这个变更从vitess更新过来，如果vitess新开了一个关键字这里也要同步开
var mySQLKeywords = map[string]string{
	"add":                "ADD",
	"against":            "AGAINST",
	"all":                "ALL",
	"alter":              "ALTER",
	"analyze":            "ANALYZE",
	"and":                "AND",
	"as":                 "AS",
	"asc":                "ASC",
	"auto_increment":     "AUTO_INCREMENT",
	"begin":              "BEGIN",
	"between":            "BETWEEN",
	"bigint":             "BIGINT",
	"binary":             "BINARY",
	"_binary":            "UNDERSCORE_BINARY",
	"bit":                "BIT",
	"blob":               "BLOB",
	"bool":               "BOOL",
	"boolean":            "BOOLEAN",
	"by":                 "BY",
	"case":               "CASE",
	"cast":               "CAST",
	"char":               "CHAR",
	"character":          "CHARACTER",
	"charset":            "CHARSET",
	"collate":            "COLLATE",
	"column":             "COLUMN",
	"comment":            "COMMENT_KEYWORD",
	"commit":             "COMMIT",
	"constraint":         "CONSTRAINT",
	"convert":            "CONVERT",
	"substr":             "SUBSTR",
	"substring":          "SUBSTRING",
	"create":             "CREATE",
	"cross":              "CROSS",
	"current_date":       "CURRENT_DATE",
	"current_time":       "CURRENT_TIME",
	"current_timestamp":  "CURRENT_TIMESTAMP",
	"database":           "DATABASE",
	"databases":          "DATABASES",
	"date":               "DATE",
	"datetime":           "DATETIME",
	"decimal":            "DECIMAL",
	"default":            "DEFAULT",
	"delete":             "DELETE",
	"desc":               "DESC",
	"describe":           "DESCRIBE",
	"distinct":           "DISTINCT",
	"div":                "DIV",
	"double":             "DOUBLE",
	"drop":               "DROP",
	"duplicate":          "DUPLICATE",
	"else":               "ELSE",
	"end":                "END",
	"enum":               "ENUM",
	"escape":             "ESCAPE",
	"exists":             "EXISTS",
	"explain":            "EXPLAIN",
	"expansion":          "EXPANSION",
	"false":              "FALSE",
	"float":              "FLOAT_TYPE",
	"for":                "FOR",
	"force":              "FORCE",
	"foreign":            "FOREIGN",
	"from":               "FROM",
	"fulltext":           "FULLTEXT",
	"geometry":           "GEOMETRY",
	"geometrycollection": "GEOMETRYCOLLECTION",
	"global":             "GLOBAL",
	"group":              "GROUP",
	"group_concat":       "GROUP_CONCAT",
	"having":             "HAVING",
	"if":                 "IF",
	"ignore":             "IGNORE",
	"in":                 "IN",
	"index":              "INDEX",
	"inner":              "INNER",
	"insert":             "INSERT",
	"int":                "INT",
	"integer":            "INTEGER",
	"interval":           "INTERVAL",
	"into":               "INTO",
	"is":                 "IS",
	"join":               "JOIN",
	"json":               "JSON",
	"key":                "KEY",
	"keys":               "KEYS",
	"key_block_size":     "KEY_BLOCK_SIZE",
	"language":           "LANGUAGE",
	"last_insert_id":     "LAST_INSERT_ID",
	"left":               "LEFT",
	"less":               "LESS",
	"like":               "LIKE",
	"limit":              "LIMIT",
	"linestring":         "LINESTRING",
	"localtime":          "LOCALTIME",
	"localtimestamp":     "LOCALTIMESTAMP",
	"lock":               "LOCK",
	"longblob":           "LONGBLOB",
	"longtext":           "LONGTEXT",
	"match":              "MATCH",
	"maxvalue":           "MAXVALUE",
	"mediumblob":         "MEDIUMBLOB",
	"mediumint":          "MEDIUMINT",
	"mediumtext":         "MEDIUMTEXT",
	"mod":                "MOD",
	"mode":               "MODE",
	"multilinestring":    "MULTILINESTRING",
	"multipoint":         "MULTIPOINT",
	"multipolygon":       "MULTIPOLYGON",
	"names":              "NAMES",
	"natural":            "NATURAL",
	"nchar":              "NCHAR",
	"next":               "NEXT",
	"not":                "NOT",
	"null":               "NULL",
	"numeric":            "NUMERIC",
	"offset":             "OFFSET",
	"on":                 "ON",
	"optimize":           "OPTIMIZE",
	"or":                 "OR",
	"order":              "ORDER",
	"outer":              "OUTER",
	"partition":          "PARTITION",
	"point":              "POINT",
	"polygon":            "POLYGON",
	"primary":            "PRIMARY",
	"procedure":          "PROCEDURE",
	"query":              "QUERY",
	"real":               "REAL",
	"regexp":             "REGEXP",
	"rename":             "RENAME",
	"reorganize":         "REORGANIZE",
	"repair":             "REPAIR",
	"replace":            "REPLACE",
	"right":              "RIGHT",
	"rlike":              "REGEXP",
	"rollback":           "ROLLBACK",
	"schema":             "SCHEMA",
	"select":             "SELECT",
	"separator":          "SEPARATOR",
	"session":            "SESSION",
	"set":                "SET",
	"share":              "SHARE",
	"show":               "SHOW",
	"signed":             "SIGNED",
	"smallint":           "SMALLINT",
	"spatial":            "SPATIAL",
	"sql_cache":          "SQL_CACHE",
	"sql_no_cache":       "SQL_NO_CACHE",
	"start":              "START",
	"status":             "STATUS",
	"straight_join":      "STRAIGHT_JOIN",
	"stream":             "STREAM",
	"table":              "TABLE",
	"tables":             "TABLES",
	"text":               "TEXT",
	"than":               "THAN",
	"then":               "THEN",
	"time":               "TIME",
	"timestamp":          "TIMESTAMP",
	"tinyblob":           "TINYBLOB",
	"tinyint":            "TINYINT",
	"tinytext":           "TINYTEXT",
	"to":                 "TO",
	"transaction":        "TRANSACTION",
	"trigger":            "TRIGGER",
	"true":               "TRUE",
	"truncate":           "TRUNCATE",
	"union":              "UNION",
	"unique":             "UNIQUE",
	"unsigned":           "UNSIGNED",
	"update":             "UPDATE",
	"use":                "USE",
	"using":              "USING",
	"utc_date":           "UTC_DATE",
	"utc_time":           "UTC_TIME",
	"utc_timestamp":      "UTC_TIMESTAMP",
	"values":             "VALUES",
	"variables":          "VARIABLES",
	"varbinary":          "VARBINARY",
	"varchar":            "VARCHAR",
	"vindex":             "VINDEX",
	"vindexes":           "VINDEXES",
	"view":               "VIEW",
	"vitess_keyspaces":   "VITESS_KEYSPACES",
	"vitess_shards":      "VITESS_SHARDS",
	"vitess_tablets":     "VITESS_TABLETS",
	"vschema_tables":     "VSCHEMA_TABLES",
	"when":               "WHEN",
	"where":              "WHERE",
	"with":               "WITH",
	"year":               "YEAR",
	"zerofill":           "ZEROFILL",
}

// Token 基本定义
type Token struct {
	Type int
	Val  string
	i    int
}

// Tokenizer 用于初始化token
func Tokenizer(sql string) []Token {
	var tokens []Token
	tkn := sqlparser.NewStringTokenizer(sql)
	typ, val := tkn.Scan()
	for typ != 0 {
		if val != nil {
			tokens = append(tokens, Token{Type: typ, Val: string(val)})
		} else {
			if typ > 255 {
				if v, ok := TokenString[typ]; ok {
					tokens = append(tokens, Token{
						Type: typ,
						Val:  v,
					})
				} else {
					tokens = append(tokens, Token{
						Type: typ,
						Val:  "",
					})
				}
			} else {
				tokens = append(tokens, Token{
					Type: typ,
					Val:  fmt.Sprintf("%c", typ),
				})
			}
		}
		typ, val = tkn.Scan()
	}
	return tokens
}

// IsMysqlKeyword 判断是否是关键字
func IsMysqlKeyword(name string) bool {
	_, ok := mySQLKeywords[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// getNextToken 从 buf 中获取 token
func getNextToken(buf string, previous Token) Token {
	var typ int // TOKEN_TYPE

	// Whitespace
	whiteSpaceReg := regexp.MustCompile(`^\s+`)
	if whiteSpaceReg.MatchString(buf) {
		return Token{
			Type: TokenTypeWhitespace,
			Val:  " ",
		}
	}

	// Comment (#, --, /**/)
	if buf[0] == '#' || (len(buf) > 1 && (buf[:2] == "--" || buf[:2] == "/*")) {
		var last int
		if buf[0] == '-' || buf[0] == '#' {
			// Comment until end of line
			last = strings.Index(buf, "\n")
			typ = TokenTypeComment
		} else {
			// Comment until closing comment tag
			last = strings.Index(buf[2:], "*/") + 2
			typ = TokenTypeBlockComment
		}
		if last == 0 {
			last = len(buf)
		}
		return Token{
			Type: typ,
			Val:  buf[:last],
		}
	}

	// Quoted String
	if buf[0] == '"' || buf[0] == '\'' || buf[0] == '`' || buf[0] == '[' {
		var typ int
		switch buf[0] {
		case '`', '[':
			typ = TokenTypeBacktickQuote
		default:
			typ = TokenTypeQuote
		}
		return Token{
			Type: typ,
			Val:  getQuotedString(buf),
		}
	}

	// User-defined Variable
	if (buf[0] == '@' || buf[0] == ':') && len(buf) > 1 {
		ret := Token{
			Type: TokenTypeVariable,
			Val:  "",
		}

		if buf[1] == '"' || buf[1] == '\'' || buf[1] == '`' {
			// If the variable name is quoted
			ret.Val = string(buf[0]) + getQuotedString(buf[1:])
		} else {
			// Non-quoted variable name
			varReg := regexp.MustCompile(`^(` + string(buf[0]) + `[a-zA-Z0-9\._\$]+)`)
			if varReg.MatchString(buf) {
				ret.Val = varReg.FindString(buf)
			}
		}

		if ret.Val != "" {
			return ret
		}
	}

	// Number(decimal, binary, hex...)
	numReg := regexp.MustCompile(`^([0-9]+(\.[0-9]+)?|0x[0-9a-fA-F]+|0b[01]+)($|\s|"'` + "`|" + regBoundariesString + ")")
	if numReg.MatchString(buf) {
		return Token{
			Type: TokenTypeNumber,
			Val:  numReg.FindString(buf),
		}
	}

	// Boundary Character(punctuation and symbols)
	boundaryReg := regexp.MustCompile(`^(` + regBoundariesString + `)`)
	if boundaryReg.MatchString(buf) {
		return Token{
			Type: TokenTypeBoundary,
			Val:  boundaryReg.FindString(buf),
		}
	}
	sqlUpper := strings.ToUpper(buf)
	// A reserved word cannot be preceded by a '.'
	// this makes it so in "mytable.from", "from" is not considered a reserved word
	if previous.Val != "." {
		// Top Level Reserved Word
		reservedToplevelReg := regexp.MustCompile(`^(` + regReservedToplevelString + `)($|\s|` + regBoundariesString + `)`)
		if reservedToplevelReg.MatchString(sqlUpper) {
			return Token{
				Type: TokenTypeReservedToplevel,
				Val:  reservedToplevelReg.FindString(sqlUpper),
			}
		}

		// Newline Reserved Word
		reservedNewlineReg := regexp.MustCompile(`^(` + regReservedNewlineString + `)($|\s|` + regBoundariesString + `)`)
		if reservedNewlineReg.MatchString(sqlUpper) {
			return Token{
				Type: TokenTypeReservedNewline,
				Val:  reservedNewlineReg.FindString(sqlUpper),
			}
		}

		// Other Reserved Word
		reservedReg := regexp.MustCompile(`^(` + regReservedString + `)($|\s|` + regBoundariesString + `)`)
		if reservedNewlineReg.MatchString(sqlUpper) {
			return Token{
				Type: TokenTypeReserved,
				Val:  reservedReg.FindString(sqlUpper),
			}
		}

	}

	// function
	// A function must be succeeded by '('
	// this makes it so "count(" is considered a function, but "count" alone is not
	functionReg := regexp.MustCompile(`^(` + regFunctionString + `)($|\s|` + regBoundariesString + `)`)
	if functionReg.MatchString(sqlUpper) {
		return Token{
			Type: TokenTypeReserved,
			Val:  functionReg.FindString(sqlUpper),
		}
	}

	// Non reserved word
	noReservedReg := regexp.MustCompile(`(.*?)($|\s|["'` + "`]|" + regBoundariesString + `)`)
	if noReservedReg.MatchString(buf) {
		return Token{
			Type: TokenTypeWord,
			Val:  noReservedReg.FindString(buf),
		}
	}
	return Token{}
}

// getQuotedString 获取quote
func getQuotedString(buf string) string {
	// This checks for the following patterns:
	// 1. backtick quoted string using `` to escape
	// 2. double quoted string using "" or \" to escape
	// 3. single quoted string using '' or \' to escape
	start := string(buf[0])
	switch start {
	case "\"", "`", "'":
		reg := fmt.Sprintf(`(^%s[^%s\\]*(?:\\.[^%s\\]*)*(%s|$))+`, start, start, start, start)
		quotedReg := regexp.MustCompile(reg)
		if quotedReg.MatchString(buf) {
			buf = quotedReg.FindString(buf)
		} else {
			buf = ""
		}
	default:
		buf = ""
	}
	return buf
}

// Tokenize 序列化token
func Tokenize(sql string) []Token {
	var token Token
	var tokenLength int
	var tokens []Token
	tokenCache = make(map[string]Token)

	// Used to make sure the string keeps shrinking on each iteration
	oldStringLen := len(sql) + 1

	currentLength := len(sql)
	for currentLength > 0 {
		// If the string stopped shrinking, there was a problem
		if oldStringLen <= currentLength {
			tokens = []Token{
				{
					Type: TokenTypeError,
					Val:  sql,
				},
			}
			return tokens
		}

		oldStringLen = currentLength
		cacheKey := ""
		// Determine if we can use caching
		if currentLength >= maxCachekeySize {
			cacheKey = sql[:maxCachekeySize]
		}

		// See if the token is already cached
		if _, ok := tokenCache[cacheKey]; ok {
			// Retrieve from cache
			token = tokenCache[cacheKey]
			tokenLength = len(token.Val)
			cacheHits = cacheHits + 1
		} else {
			// Get the next token and the token type
			token = getNextToken(sql, token)
			tokenLength = len(token.Val)
			cacheMisses = cacheMisses + 1
			// If the token is shorter than the max length, store it in cache
			if cacheKey != "" && tokenLength < maxCachekeySize {
				tokenCache[cacheKey] = token
			}
		}

		tokens = append(tokens, token)

		// Advance the string
		sql = sql[tokenLength:]
		currentLength = currentLength - tokenLength
	}
	return tokens
}

// Compress compress sql
// this method is inspired by eversql.com
func Compress(sql string) string {
	regLineTab := regexp.MustCompile(`(?i)([\n\t])`)
	regSpace := regexp.MustCompile(`\s\s+`)
	sql = regSpace.ReplaceAllString(regLineTab.ReplaceAllString(sql, " "), " ")
	return sql
}

// SplitStatement SQL切分
// return original sql, remove comment sql, left over buf
func SplitStatement(buf []byte, delimiter []byte) (string, string, []byte) {
	var singleLineComment bool
	var multiLineComment bool
	var quoted bool
	var quoteRune byte
	var sql string

	for i := 0; i < len(buf); i++ {
		b := buf[i]
		// single line comment
		if b == '-' {
			if i+2 < len(buf) && buf[i+1] == '-' && buf[i+2] == ' ' {
				singleLineComment = true
				i = i + 2
				continue
			}
			if i+2 < len(buf) && i == 0 && buf[i+1] == '-' && (buf[i+2] == '\n' || buf[i+2] == '\r') {
				sql = "--\n"
				break
			}
		}

		if b == '#' {
			if !multiLineComment && !quoted && !singleLineComment {
				singleLineComment = true
				continue
			}
		}

		// new line end single line comment
		if b == '\r' || b == '\n' {
			if singleLineComment {
				sql = string(buf[:i])
				singleLineComment = false
				if strings.HasPrefix(strings.TrimSpace(sql), "--") ||
					strings.HasPrefix(strings.TrimSpace(sql), "#") {
					// just comment, query start with '--', '#'
					break
				}
				// comment in multi-line sql
				continue
			}
			continue
		}

		// multi line comment
		// https://dev.mysql.com/doc/refman/8.0/en/comments.html
		// https://dev.mysql.com/doc/refman/8.0/en/optimizer-hints.html
		if b == '/' && i+1 < len(buf) && buf[i+1] == '*' {
			if !multiLineComment && !singleLineComment && !quoted &&
				(buf[i+2] != '!' && buf[i+2] != '+') {
				i = i + 2
				multiLineComment = true
				continue
			}
		}

		if b == '*' && i+1 < len(buf) && buf[i+1] == '/' {
			if multiLineComment && !quoted && !singleLineComment {
				i = i + 2
				multiLineComment = false
				// '/*comment*/'
				if i == len(buf) {
					sql = string(buf[:i])
				}
				// '/*comment*/;', 'select 1/*comment*/;'
				if string(buf[i:]) == string(delimiter) {
					sql = string(buf)
				}
				continue
			}
		}

		// quoted string
		if b == '`' || b == '\'' || b == '"' {
			if i > 1 && buf[i-1] != '\\' {
				if quoted && b == quoteRune {
					quoted = false
				} else {
					quoted = true
					quoteRune = b
				}
			}
		}

		// delimiter
		if !quoted && !singleLineComment && !multiLineComment {
			eof := true
			for k, c := range delimiter {
				if len(buf) > i+k && buf[i+k] != c {
					eof = false
				}
			}
			if eof {
				i = i + len(delimiter)
				sql = string(buf[:i])
				break
			}
		}

		// ended of buf
		if i == len(buf)-1 {
			sql = string(buf)
		}
	}
	orgSQL := string(buf[:len(sql)])
	buf = buf[len(sql):]
	return orgSQL, strings.TrimSuffix(sql, string(delimiter)), buf
}

// LeftNewLines cal left new lines in space
func LeftNewLines(buf []byte) int {
	newLines := 0
	for _, b := range buf {
		if !unicode.IsSpace(rune(b)) {
			break
		}
		if b == byte('\n') {
			newLines++
		}
	}
	return newLines
}

// NewLines cal all new lines
func NewLines(buf []byte) int {
	newLines := 0
	for _, b := range buf {
		if b == byte('\n') {
			newLines++
		}
	}
	return newLines
}
