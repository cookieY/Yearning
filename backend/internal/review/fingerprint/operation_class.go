package fingerprint

import "strings"

// IsOperationWordByte reports whether b is an ASCII SQL keyword/identifier word byte.
func IsOperationWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// FirstOperationSlice extracts the leading SQL operation word slice from sql,
// skipping ASCII whitespace, leading parentheses, `--` line comments, and `/* ... */`
// block comments. Shared by review statement analysis and execution preflight so
// statement head classification can never drift between phases.
func FirstOperationSlice(sql []byte) []byte {
	i := 0
	n := len(sql)
	for i < n {
		b := sql[i]
		switch {
		case b == ' ' || b == '\t' || b == '\r' || b == '\n':
			i++
			continue
		case b == '(':
			i++
			continue
		case b == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
			continue
		case b == '/' && i+1 < n && sql[i+1] == '*':
			i += 2
			for i+1 < n && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			i += 2
			continue
		default:
			start := i
			for i < n && IsOperationWordByte(sql[i]) {
				i++
			}
			if i == start {
				return sql[start : start+1]
			}
			return sql[start:i]
		}
	}
	return nil
}

// StatementClassOf classifies a SQL operation verb into the persisted statement_kind
// domain ("ddl" for CREATE/ALTER/DROP/TRUNCATE/RENAME; "dml" for all other verbs,
// including DML, SELECT/CTE reads, and GRANT/REVOKE).
func StatementClassOf(op string) string {
	switch strings.ToLower(op) {
	case "create", "alter", "drop", "truncate", "rename":
		return "ddl"
	default:
		return "dml"
	}
}
