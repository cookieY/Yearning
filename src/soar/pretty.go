package soar

import (
	"container/list"
	"regexp"
	"strings"
)

func PrettyFormat(query string) string {
	// This variable will be populated with formatted html
	// this func by XiaoMi SOAR
	result := ""
	// Use an actual tab while formatting and then switch out with self::$tab at the end
	tab := "  "
	indentLevel := 0
	var newline bool
	var inlineParentheses bool
	var increaseSpecialIndent bool
	var increaseBlockIndent bool
	var addedNewline bool
	var inlineCount int
	var inlineIndented bool
	var clauseLimit bool
	indentTypes := list.New()

	// Tokenize String
	originalTokens := Tokenize(query)

	// Remove existing whitespace//
	var tokens []Token
	for i, token := range originalTokens {
		if token.Type != TokenTypeWhitespace {
			token.i = i
			tokens = append(tokens, token)
		}
	}

	for i, token := range tokens {
		highlighted := token.Val

		// If we are increasing the special indent level now
		if increaseSpecialIndent {
			indentLevel++
			increaseSpecialIndent = false
			indentTypes.PushFront("special")
		}

		// If we are increasing the block indent level now
		if increaseBlockIndent {
			indentLevel++
			increaseBlockIndent = false
			indentTypes.PushFront("block")
		}

		// If we need a new line before the token
		if newline {
			result += "\n" + strings.Repeat(tab, indentLevel)
			newline = false
			addedNewline = true
		} else {
			addedNewline = false
		}

		// Display comments directly where they appear in the source
		if token.Type == TokenTypeComment || token.Type == TokenTypeBlockComment {
			if token.Type == TokenTypeBlockComment {
				indent := strings.Repeat(tab, indentLevel)
				result += "\n" + indent
				highlighted = strings.Replace(highlighted, "\n", "\n"+indent, -1)
			}

			result += highlighted
			newline = true
			continue
		}

		if inlineParentheses {
			// End of inline parentheses
			if token.Val == ")" {
				result = strings.TrimRight(result, " ")

				if inlineIndented {
					indentTypes.Remove(indentTypes.Front())
					if indentLevel > 0 {
						indentLevel--
					}
					result += strings.Repeat(tab, indentLevel)
				}

				inlineParentheses = false

				result += highlighted + " "
				continue
			}

			if token.Val == "," {
				if inlineCount >= 30 {
					inlineCount = 0
					newline = true
				}
			}

			inlineCount += len(token.Val)
		}

		// Opening parentheses increase the block indent level and start a new line
		if token.Val == "(" {
			// First check if this should be an inline parentheses block
			// Examples are "NOW()", "COUNT(*)", "int(10)", key(`somecolumn`), DECIMAL(7,2)
			// Allow up to 3 non-whitespace tokens inside inline parentheses
			length := 0
			for j := 1; j <= 250; j++ {
				// Reached end of string
				if i+j >= len(tokens) {
					break
				}

				next := tokens[i+j]

				// Reached closing parentheses, able to inline it
				if next.Val == ")" {
					inlineParentheses = true
					inlineCount = 0
					inlineIndented = false
					break
				}

				// Reached an invalid token for inline parentheses
				if next.Val == ";" || next.Val == "(" {
					break
				}

				// Reached an invalid token type for inline parentheses
				if next.Type == TokenTypeReservedToplevel ||
					next.Type == TokenTypeReservedNewline ||
					next.Type == TokenTypeComment ||
					next.Type == TokenTypeBlockComment {
					break
				}

				length += len(next.Val)
			}

			if inlineParentheses && length > 30 {
				increaseBlockIndent = true
				inlineIndented = true
				newline = true
			}

			// Take out the preceding space unless there was whitespace there in the original query
			if token.i != 0 && (token.i-1) > len(originalTokens)-1 &&
				originalTokens[token.i-1].Type != TokenTypeWhitespace {

				result = strings.TrimRight(result, " ")
			}

			if inlineParentheses {
				increaseBlockIndent = true
				// Add a newline after the parentheses
				newline = true
			}

		} else if token.Val == ")" {
			// Closing parentheses decrease the block indent level
			// Remove whitespace before the closing parentheses
			result = strings.TrimRight(result, " ")

			if indentLevel > 0 {
				indentLevel--
			}

			// Reset indent level
			for j := indentTypes.Front(); indentTypes.Len() > 0; indentTypes.Remove(j) {
				if j.Value.(string) == "special" {
					if indentLevel > 0 {
						indentLevel--
					} else {
						break
					}
				} else {
					break
				}
			}

			if indentLevel < 0 {
				// This is an error
				indentLevel = 0
			}

			// Add a newline before the closing parentheses (if not already added)
			if !addedNewline {
				result += "\n" + strings.Repeat(tab, indentLevel)
			}

		} else if token.Type == TokenTypeReservedToplevel {
			// Top level reserved words start a new line and increase the special indent level
			increaseSpecialIndent = true

			// If the last indent type was 'special', decrease the special indent for this round
			if indentTypes.Len() > 0 && indentTypes.Front().Value.(string) == "special" {
				if indentLevel > 0 {
					indentLevel--
				}
				indentTypes.Remove(indentTypes.Front())
			}

			// Add a newline after the top level reserved word
			newline = true
			// Add a newline before the top level reserved word (if not already added)
			if !addedNewline {
				result += "\n" + strings.Repeat(tab, indentLevel)
			} else {
				// If we already added a newline, redo the indentation since it may be different now
				result = strings.TrimSuffix(result, tab) + strings.Repeat(tab, indentLevel)
			}

			// If the token may have extra whitespace
			if strings.Index(token.Val, " ") != 0 ||
				strings.Index(token.Val, "\n") != 0 ||
				strings.Index(token.Val, "\t") != 0 {

				re, _ := regexp.Compile(`\s+`)
				highlighted = re.ReplaceAllString(highlighted, " ")

			}

			//if SQL 'LIMIT' clause, start variable to reset newline
			if token.Val == "LIMIT" && inlineParentheses {
				clauseLimit = true
			}

		} else if clauseLimit && token.Val != "," &&
			token.Type != TokenTypeNumber &&
			token.Type != TokenTypeWhitespace {
			// Checks if we are out of the limit clause

			clauseLimit = false

		} else if token.Val == "," && !inlineParentheses {
			// Commas start a new line (unless within inline parentheses or SQL 'LIMIT' clause)
			if clauseLimit {
				newline = false
				clauseLimit = false
			} else {
				// All other cases of commas
				newline = true
			}

		} else if token.Type == TokenTypeReservedNewline {
			// Newline reserved words start a new line
			// Add a newline before the reserved word (if not already added)
			if !addedNewline {
				result += "\n" + strings.Repeat(tab, indentLevel)
			}

			// If the token may have extra whitespace
			if strings.Index(token.Val, " ") != 0 ||
				strings.Index(token.Val, "\n") != 0 ||
				strings.Index(token.Val, "\t") != 0 {

				re, _ := regexp.Compile(`\s+`)
				highlighted = re.ReplaceAllString(highlighted, " ")
			}

		} else if token.Type == TokenTypeBoundary {
			// Multiple boundary characters in a row should not have spaces between them (not including parentheses)
			if i != 0 && i < len(tokens) &&
				tokens[i-1].Type == TokenTypeBoundary {

				if token.i != 0 && token.i < len(originalTokens) &&
					originalTokens[token.i-1].Type != TokenTypeWhitespace {

					result = strings.TrimRight(result, " ")
				}
			}
		}

		// If the token shouldn't have a space before it
		if token.Val == "." || token.Val == "," || token.Val == ";" {
			result = strings.TrimRight(result, " ")
		}

		result += highlighted + " "

		// If the token shouldn't have a space after it
		if token.Val == "(" || token.Val == "." {
			result = strings.TrimRight(result, " ")
		}

		// If this is the "-" of a negative number, it shouldn't have a space after it
		if token.Val == "-" && i+1 < len(tokens) && tokens[i+1].Type == TokenTypeNumber && i != 0 {
			prev := tokens[i-1].Type
			if prev != TokenTypeQuote &&
				prev != TokenTypeBacktickQuote &&
				prev != TokenTypeWord &&
				prev != TokenTypeNumber {

				result = strings.TrimRight(result, " ")
			}
		}
	}

	// Replace tab characters with the configuration tab character
	result = strings.TrimRight(strings.Replace(result, "\t", tab, -1), " ")

	return result
}