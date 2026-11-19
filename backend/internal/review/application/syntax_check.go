package application

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
	pg_query "github.com/wasilibs/go-pgquery"

	"github.com/cookieY/YearningX/backend/internal/review/changeset"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

const (
	// Validate every statement directly for interactive batches up to this count.
	syntaxCheckDirectLimit = 200
	// Beyond syntaxCheckDirectLimit (bulk imports), validate each unseen normalized shape up to this cap.
	syntaxCheckMaxUniqueShapes = 500
)

// countAndValidateStatements splits SQL by dialect profile, enforces batch statement count bounds,
// and validates statement syntax using the dialect-native AST parser (TiDB parser for MySQL-family
// dialects and official libpg_query for PostgreSQL-family dialects). Any syntax error fails fast
// with 2003 SQL_PARSE_FAILED before invoking AI review providers.
func (s *Services) countAndValidateStatements(sql []byte, profile fingerprint.Profile) (int, error) {
	split := changeset.NewSplitter(bytes.NewReader(sql), profile, 0)
	count := 0
	validatedCount := 0
	var seenShapes map[string]struct{}
	var mysqlParser *parser.Parser
	isMySQLFamily := strings.EqualFold(profile.Mode, "mysql")
	isPGFamily := strings.EqualFold(profile.Mode, "postgresql")
	if isMySQLFamily {
		mysqlParser = parser.New()
	}

	for {
		st, done, err := split.Next()
		if err != nil {
			base := domain.FailureFromFingerprintCode(fingerprintFailureName(err))
			if detail := formatSplitterFailureDetail(sql, profile, err); detail != "" {
				return 0, &domain.Error{Code: base.Code, Name: base.Name, Detail: detail}
			}
			return 0, base
		}
		if done {
			break
		}
		count++
		if count > 100000 {
			return 0, domain.ErrBatchTooLarge
		}

		if !isMySQLFamily && !isPGFamily {
			continue
		}

		stmtText := strings.TrimSpace(string(st.SQL))
		if stmtText == "" {
			continue
		}

		shouldValidate := count <= syntaxCheckDirectLimit
		if !shouldValidate {
			if seenShapes == nil {
				seenShapes = make(map[string]struct{}, 64)
			}
			if validatedCount < syntaxCheckMaxUniqueShapes {
				shape := quickSyntaxShape(stmtText)
				if _, exists := seenShapes[shape]; !exists {
					seenShapes[shape] = struct{}{}
					shouldValidate = true
				}
			}
		} else if count == syntaxCheckDirectLimit {
			// Seed shape map once crossing into bulk mode so subsequent duplicate rows skip redundant parses.
			seenShapes = map[string]struct{}{quickSyntaxShape(stmtText): {}}
		}

		if !shouldValidate {
			continue
		}
		validatedCount++

		var parseErr error
		if isMySQLFamily {
			parseErr = validateMySQLStatementSyntax(mysqlParser, stmtText)
		} else {
			parseErr = validatePostgresStatementSyntax(stmtText)
		}
		if parseErr != nil {
			line, col := byteOffsetToLineCol(sql, st.Offset)
			detail := fmt.Sprintf("line %d, col %d (offset %d, statement #%d, dialect %s): %s",
				line, col, st.Offset, count, profile.String(), strings.TrimSpace(parseErr.Error()))
			return 0, &domain.Error{
				Code:   domain.CodeSQLParseFailed,
				Name:   domain.ErrParseFailed.Name,
				Detail: detail,
			}
		}
	}
	return count, nil
}

func validateMySQLStatementSyntax(p *parser.Parser, stmtText string) error {
	if p == nil {
		p = parser.New()
	}
	stmts, _, err := p.ParseSQL(stmtText)
	if err != nil {
		return err
	}
	if len(stmts) == 0 {
		return fmt.Errorf("empty statement")
	}
	for _, node := range stmts {
		if ct, ok := node.(*ast.CreateTableStmt); ok {
			// TiDB's grammar permits bare "CREATE TABLE x" without columns/LIKE/AS SELECT,
			// whereas MySQL strictly requires at least one column definition (Error 4028).
			if len(ct.Cols) == 0 && ct.Select == nil && ct.ReferTable == nil {
				return fmt.Errorf("CREATE TABLE requires at least one column definition")
			}
		}
	}
	return nil
}

func validatePostgresStatementSyntax(stmtText string) error {
	tree, err := pg_query.Parse(stmtText)
	if err != nil {
		return err
	}
	if tree == nil || len(tree.Stmts) == 0 {
		return fmt.Errorf("empty statement")
	}
	return nil
}

// quickSyntaxShape normalizes string literals and numeric constants so huge homogeneous bulk
// batches (e.g. 50,000 identical INSERT rows) skip redundant AST parses while still catching
// any structurally distinct malformed statement later in the batch.
func quickSyntaxShape(stmt string) string {
	var b strings.Builder
	b.Grow(len(stmt))
	var lastByte byte
	writeByte := func(c byte) {
		b.WriteByte(c)
		lastByte = c
	}
	inSingle := false
	inDouble := false
	for i := 0; i < len(stmt); i++ {
		ch := stmt[i]
		if inSingle {
			if ch == '\\' && i+1 < len(stmt) {
				i++
				continue
			}
			if ch == '\'' {
				if i+1 < len(stmt) && stmt[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if ch == '"' {
				if i+1 < len(stmt) && stmt[i+1] == '"' {
					i++
					continue
				}
				inDouble = false
			}
			continue
		}
		switch {
		case ch == '\'':
			inSingle = true
			writeByte('?')
		case ch == '"':
			inDouble = true
			writeByte('"')
		case ch >= '0' && ch <= '9':
			if lastByte != '#' {
				writeByte('#')
			}
		case ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n':
			if lastByte != 0 && lastByte != ' ' {
				writeByte(' ')
			}
		default:
			if ch >= 'a' && ch <= 'z' {
				writeByte(ch - ('a' - 'A'))
			} else {
				writeByte(ch)
			}
		}
	}
	return b.String()
}
