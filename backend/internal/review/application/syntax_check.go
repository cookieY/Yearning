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

type statementSyntaxValidator struct {
	profile       fingerprint.Profile
	rawSQL        []byte
	isMySQLFamily bool
	isPGFamily    bool
	mysqlParser   *parser.Parser
	seenShapes    map[string]struct{}
}

func newStatementSyntaxValidator(sql []byte, profile fingerprint.Profile) *statementSyntaxValidator {
	isMySQL := strings.EqualFold(profile.Mode, "mysql")
	isPG := strings.EqualFold(profile.Mode, "postgresql")
	var mysqlParser *parser.Parser
	if isMySQL {
		mysqlParser = parser.New()
	}
	return &statementSyntaxValidator{
		profile:       profile,
		rawSQL:        sql,
		isMySQLFamily: isMySQL,
		isPGFamily:    isPG,
		mysqlParser:   mysqlParser,
	}
}

func (v *statementSyntaxValidator) ValidateStatement(ordinal int, st changeset.Statement) error {
	if !v.isMySQLFamily && !v.isPGFamily {
		return nil
	}
	stmtText := strings.TrimSpace(string(st.SQL))
	if stmtText == "" {
		return nil
	}
	shouldValidate := ordinal <= syntaxCheckDirectLimit
	if !shouldValidate {
		if v.seenShapes == nil {
			v.seenShapes = make(map[string]struct{}, 64)
		}
		if len(v.seenShapes) < syntaxCheckMaxUniqueShapes {
			shape, ok := quickSyntaxShape(st.SQL, v.profile.Flags())
			if !ok {
				shouldValidate = true
			} else if _, exists := v.seenShapes[shape]; !exists {
				v.seenShapes[shape] = struct{}{}
				shouldValidate = true
			}
		}
	} else if ordinal == syntaxCheckDirectLimit {
		// Seed shape map once crossing into bulk mode so subsequent duplicate rows skip redundant parses.
		if shape, ok := quickSyntaxShape(st.SQL, v.profile.Flags()); ok {
			v.seenShapes = map[string]struct{}{shape: {}}
		}
	}
	if !shouldValidate {
		return nil
	}
	var parseErr error
	if v.isMySQLFamily {
		parseErr = validateMySQLStatementSyntax(v.mysqlParser, stmtText)
	} else {
		parseErr = validatePostgresStatementSyntax(stmtText)
	}
	if parseErr != nil {
		line, col := byteOffsetToLineCol(v.rawSQL, st.Offset)
		detail := fmt.Sprintf("line %d, col %d (offset %d, statement #%d, dialect %s): %s",
			line, col, st.Offset, ordinal, v.profile.String(), strings.TrimSpace(parseErr.Error()))
		return &domain.Error{
			Code:   domain.CodeSQLParseFailed,
			Name:   domain.ErrParseFailed.Name,
			Detail: detail,
		}
	}
	return nil
}

// countAndValidateStatements splits SQL by dialect profile, enforces batch statement count bounds,
// and validates statement syntax using the dialect-native AST parser (TiDB parser for MySQL-family
// dialects and official libpg_query for PostgreSQL-family dialects). Any syntax error fails fast
// with 2003 SQL_PARSE_FAILED before invoking AI review providers.
func (s *Services) countAndValidateStatements(sql []byte, profile fingerprint.Profile) (int, error) {
	split := changeset.NewSplitter(bytes.NewReader(sql), profile, 0)
	validator := newStatementSyntaxValidator(sql, profile)
	count := 0
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
		if err := validator.ValidateStatement(count, st); err != nil {
			return 0, err
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

// quickSyntaxShape tokenizes the statement using dialect-accurate lexical flags and normalizes
// literals and numbers so huge homogeneous bulk batches (e.g. 50,000 identical INSERT rows) skip
// redundant AST parses while still catching structurally distinct malformed SQL or lexing errors.
func quickSyntaxShape(stmt []byte, flags fingerprint.DialectFlags) (string, bool) {
	lex, err := fingerprint.Tokenize(stmt, flags)
	if err != nil {
		return "", false
	}
	var b strings.Builder
	b.Grow(len(stmt))
	for i, tok := range lex.Tokens {
		if i > 0 {
			b.WriteByte(' ')
		}
		switch tok.Kind {
		case fingerprint.TokString, fingerprint.TokDollarString:
			if tok.Prefix != 0 {
				b.WriteByte(tok.Prefix)
			}
			b.WriteString("?s")
		case fingerprint.TokNumber:
			b.WriteByte('#')
		case fingerprint.TokParam:
			b.WriteString("$?")
		case fingerprint.TokQuotedIdent:
			b.WriteByte('"')
			b.WriteString(tok.Text)
			b.WriteByte('"')
		default:
			b.WriteString(tok.Text)
		}
	}
	return b.String(), true
}
