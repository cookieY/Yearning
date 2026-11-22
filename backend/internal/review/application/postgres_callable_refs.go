package application

import (
	"strings"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

type postgresCallableRef struct {
	Kind database.PostgresCallableKind
	Name string
}

const postgresCallableMaxRefs = 4096

func postgresOperatorToken(token fingerprint.Token) bool {
	if token.Kind != fingerprint.TokPunct || token.Text == "" {
		return false
	}
	for _, r := range token.Text {
		if !strings.ContainsRune("+-*/<>=~!@#%^&|`?", r) {
			return false
		}
	}
	return true
}

// postgresKeywordOperators follows PostgreSQL's gram.y/parse_expr.c rewrites.
// Only path-resolved operators belong here. AT TIME ZONE/LOCAL and OVERLAPS,
// plus LIKE/SIMILAR escape helpers, use SystemFuncName (fixed pg_catalog).
// NOT IN conservatively includes both rewrites: lists use <>, subqueries =.
// ANY/ALL/SOME keep their written preceding operator; they do not imply =.
func postgresKeywordOperators(tokens []fingerprint.Token, i int) ([]string, int) {
	word := func(offset int, want string) bool {
		return i+offset < len(tokens) && tokens[i+offset].IsWord(want)
	}
	not := word(0, "not")
	offset := 0
	if not {
		offset = 1
	}
	switch {
	case word(offset, "like"):
		if not {
			return []string{"!~~"}, 2
		}
		return []string{"~~"}, 1
	case word(offset, "ilike"):
		if not {
			return []string{"!~~*"}, 2
		}
		return []string{"~~*"}, 1
	case word(offset, "similar") && word(offset+1, "to"):
		if not {
			return []string{"!~"}, 3
		}
		return []string{"~"}, 2
	case word(offset, "between"):
		if not {
			return []string{"<", ">"}, 2
		}
		return []string{">=", "<="}, 1
	case word(offset, "in") && i+offset+1 < len(tokens) && tokens[i+offset+1].IsPunct("("):
		if not {
			return []string{"<>", "="}, 2
		}
		return []string{"="}, 1
	case word(0, "is") && word(1, "distinct") && word(2, "from"):
		return []string{"="}, 3
	case word(0, "is") && word(1, "not") && word(2, "distinct") && word(3, "from"):
		return []string{"="}, 4
	case word(0, "nullif") && i+1 < len(tokens) && tokens[i+1].IsPunct("("):
		return []string{"="}, 1
	case word(0, "case") && i+1 < len(tokens) && !word(1, "when"):
		return []string{"="}, 1
	}
	return nil, 0
}

// postgresFixedCallableSyntax consumes only keyword spelling, never arguments.
// Quoted identifiers are handled separately as real, case-sensitive names.
func postgresFixedCallableSyntax(tokens []fingerprint.Token, i int) int {
	word := func(offset int, want string) bool {
		return i+offset < len(tokens) && tokens[i+offset].IsWord(want)
	}
	if word(0, "at") && word(1, "time") && word(2, "zone") {
		return 3
	}
	if word(0, "at") && word(1, "local") {
		return 2
	}
	if word(0, "overlaps") && i > 0 && tokens[i-1].IsPunct(")") {
		return 1
	}
	if word(0, "any") || word(0, "all") || word(0, "some") {
		return 1
	}
	return 0
}

// postgresCallableRefs extracts path-dependent names without executing SQL.
// Catalog proof is conservative across all visible overload signatures. Names
// the identifier validator cannot represent and tokenization failures fail closed.
func postgresCallableRefs(statementSQL string) ([]postgresCallableRef, bool) {
	lexed, err := fingerprint.Tokenize([]byte(statementSQL), fingerprint.Profile{Mode: "postgresql"}.Flags())
	if err != nil {
		return nil, false
	}
	return postgresCallableRefsFromTokens(lexed.Tokens)
}

func postgresCallableRefsFromTokens(tokens []fingerprint.Token) ([]postgresCallableRef, bool) {
	refs := make([]postgresCallableRef, 0, 8)
	seen := map[string]bool{}
	add := func(kind database.PostgresCallableKind, name string) bool {
		if database.ValidatePostgresCallableName(kind, name) != nil {
			return false
		}
		key := database.PostgresCallablePayloadKey(kind, name)
		if seen[key] {
			return true
		}
		if len(refs) >= postgresCallableMaxRefs {
			return false
		}
		seen[key] = true
		refs = append(refs, postgresCallableRef{Kind: kind, Name: name})
		return true
	}
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if token.Kind == fingerprint.TokWord || token.Kind == fingerprint.TokQuotedIdent {
			if i > 0 && tokens[i-1].IsPunct(".") {
				continue
			}
			if token.Kind == fingerprint.TokWord {
				if names, consumed := postgresKeywordOperators(tokens, i); consumed != 0 {
					for _, name := range names {
						if !add(database.PostgresCallableOperator, name) {
							return nil, false
						}
					}
					i += consumed - 1
					continue
				}
				if consumed := postgresFixedCallableSyntax(tokens, i); consumed != 0 {
					i += consumed - 1
					continue
				}
			}
			if i+1 < len(tokens) && tokens[i+1].IsPunct("(") {
				// The tokenizer already folds unquoted words and unescapes quoted names.
				// Lowercasing a quoted function would look up a different callable.
				if !add(database.PostgresCallableFunction, token.Text) {
					return nil, false
				}
			}
			continue
		}
		if !postgresOperatorToken(token) {
			continue
		}
		name := token.Text
		for i+1 < len(tokens) && postgresOperatorToken(tokens[i+1]) && tokens[i].End == tokens[i+1].Start {
			i++
			name += tokens[i].Text
		}
		// PostgreSQL's scanner canonicalizes != to <> before operator lookup.
		if name == "!=" {
			name = "<>"
		}
		if !add(database.PostgresCallableOperator, name) {
			return nil, false
		}
	}
	return refs, true
}
