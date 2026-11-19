package fingerprint

import (
	"fmt"
	"strings"
)

func validityOperationOf(kind string) ValidityOperation {
	switch kind {
	case "select":
		return ValidityOperationSelect
	case "insert", "replace":
		return ValidityOperationInsert
	case "update":
		return ValidityOperationUpdate
	case "delete":
		return ValidityOperationDelete
	case "merge":
		return ValidityOperationMerge
	case "create":
		return ValidityOperationCreate
	case "alter":
		return ValidityOperationAlter
	case "drop":
		return ValidityOperationDrop
	case "truncate":
		return ValidityOperationTruncate
	default:
		return ""
	}
}

func createSubtype(toks []Token) (string, int) {
	for i := 1; i < len(toks) && toks[i].Kind == TokWord; i++ {
		switch toks[i].Text {
		case "or", "replace", "temporary", "temp", "unlogged", "global", "local":
			continue
		case "table", "index", "view", "schema", "database":
			return toks[i].Text, i
		case "materialized":
			return "materialized", i
		case "unique", "fulltext", "spatial":
			for j := i + 1; j < len(toks) && toks[j].Kind == TokWord; j++ {
				if toks[j].Text == "index" {
					return "index", j
				}
			}
			return "", -1
		default:
			return "", -1
		}
	}
	return "", -1
}

func dropSubtype(toks []Token) (string, int) {
	for i := 1; i < len(toks); i++ {
		if toks[i].Kind == TokWord {
			return toks[i].Text, i
		}
	}
	return "", -1
}

func conditionFromPosition(toks []Token, start int) ValidityCondition {
	if start < 0 {
		start = 0
	}
	end := start + 5
	if end > len(toks) {
		end = len(toks)
	}
	return conditionFromSpan(toks, start, end)
}

func conditionFromSpan(toks []Token, start, end int) ValidityCondition {
	if start < 0 {
		start = 0
	}
	if end > len(toks) {
		end = len(toks)
	}
	for i := start; i < end; i++ {
		if toks[i].Kind != TokWord {
			continue
		}
		if toks[i].Text == "if" {
			if i+1 < end && toks[i+1].IsWord("exists") {
				return ValidityConditionIfExists
			}
			if i+2 < end && toks[i+1].IsWord("not") && toks[i+2].IsWord("exists") {
				return ValidityConditionIfNotExists
			}
		}
	}
	return ValidityConditionNone
}

func hasLeadingWord(toks []Token, words ...string) bool {
	if len(toks) == 0 || toks[0].Kind != TokWord {
		return false
	}
	for _, word := range words {
		if toks[0].Text == word {
			return true
		}
	}
	return false
}

func hasTopLevelWord(toks []Token, start int, word string) bool {
	depth := 0
	for i := start; i < len(toks); i++ {
		switch toks[i].Text {
		case "(":
			if toks[i].Kind == TokPunct {
				depth++
			}
		case ")":
			if toks[i].Kind == TokPunct && depth > 0 {
				depth--
			}
		default:
			if depth == 0 && toks[i].IsWord(word) {
				return true
			}
		}
	}
	return false
}

func hasRolelessWord(toks []Token, roles []idRole, word string) bool {
	for i := 0; i < len(toks) && i < len(roles); i++ {
		if toks[i].IsWord(word) && roles[i].kind == "" {
			return true
		}
	}
	return false
}

func wordPosition(toks []Token, word string, start int) int {
	if start < 0 {
		start = 0
	}
	for i := start; i < len(toks); i++ {
		if toks[i].IsWord(word) {
			return i
		}
	}
	return -1
}

func firstWord(toks []Token, start, end int) int {
	if start < 0 {
		start = 0
	}
	if end > len(toks) {
		end = len(toks)
	}
	for i := start; i < end; i++ {
		if toks[i].Kind == TokWord {
			return i
		}
	}
	return -1
}

func skipWords(toks []Token, pos *int, end int, words ...string) {
	for *pos < end && toks[*pos].Kind == TokWord {
		found := false
		for _, word := range words {
			if toks[*pos].Text == word {
				found = true
				break
			}
		}
		if !found {
			return
		}
		*pos++
	}
}

func hasAnyWord(toks []Token, start, end int, words ...string) bool {
	if start < 0 {
		start = 0
	}
	if end > len(toks) {
		end = len(toks)
	}
	for i := start; i < end; i++ {
		if toks[i].Kind != TokWord {
			continue
		}
		for _, word := range words {
			if toks[i].Text == word {
				return true
			}
		}
	}
	return false
}

func topLevelSpans(toks []Token, start int) []tokenSpan {
	if start < 0 {
		start = 0
	}
	out := []tokenSpan{}
	partStart := start
	depth := 0
	for i := start; i < len(toks); i++ {
		switch {
		case toks[i].IsPunct("("):
			depth++
		case toks[i].IsPunct(")"):
			if depth > 0 {
				depth--
			}
		case depth == 0 && toks[i].IsPunct(","):
			if partStart < i {
				out = append(out, tokenSpan{start: partStart, end: i})
			}
			partStart = i + 1
		}
	}
	if partStart < len(toks) {
		out = append(out, tokenSpan{start: partStart, end: len(toks)})
	}
	return out
}

func (o ValidityObject) String() string {
	return fmt.Sprintf("%s:%s/%s/%s", o.Kind, o.SchemaToken, o.ParentToken, o.Token)
}

func canonicalValidityReason(reason string) string {
	return strings.TrimSpace(reason)
}
