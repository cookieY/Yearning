package database

import (
	"errors"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

// nonExecutingExplainSQL is the final statement construction boundary. Only
// ordinary plan-able statement heads may follow the fixed non-executing
// options. In particular ANALYZE and nested EXPLAIN must never become options
// of the prefix, regardless of earlier statement classification.
func nonExecutingExplainSQL(engine, statement string) (string, error) {
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return "", ErrStatementRequired
	}
	if engine != "mysql" && engine != "postgresql" {
		return "", ErrUnsupportedTarget
	}
	profile := fingerprint.Profile{Engine: engine, Mode: engine}
	lex, err := fingerprint.Tokenize([]byte(statement), profile.Flags())
	if err != nil || len(lex.Tokens) == 0 {
		return "", errors.New("database: explain statement rejected")
	}
	for _, token := range lex.Tokens {
		if token.IsPunct(";") {
			return "", errors.New("database: explain requires a single statement")
		}
	}
	head := lex.Tokens[0]
	if head.Kind != fingerprint.TokWord {
		return "", errors.New("database: explain statement rejected")
	}
	switch head.Text {
	case "select", "with", "insert", "update", "delete", "merge", "replace":
		return explainPrefix(engine) + statement, nil
	default:
		return "", errors.New("database: explain statement rejected")
	}
}
