// prompttool_classify.go Skill category classification, SQL comment stripping,
// and generated test case post-validation for Prompt Tools AI authoring.
package application

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	negatedDDLRe        = regexp.MustCompile(`(?i)(?:non[-\s]+|not\s+|excluding\s+|except\s+|\x{975e}\s*)ddl\b`)
	negatedDMLRe        = regexp.MustCompile(`(?i)(?:non[-\s]+|not\s+|excluding\s+|except\s+|\x{975e}\s*)dml\b`)
	referentialActionRe = regexp.MustCompile(`(?i)\bon\s+(?:update|delete)\b`)
	ddlChineseActionRe  = regexp.MustCompile(`(?:\x{5220}\x{9664}|\x{66f4}\x{65b0})\s*(?:\x{8868}\x{7ed3}\x{6784}|\x{8868}|\x{5217}|\x{5b57}\x{6bb5}|\x{7d22}\x{5f15}|\x{5916}\x{952e}|\x{7ea6}\x{675f}|\x{89c6}\x{56fe})`)
	nameDMLRe           = regexp.MustCompile(`(?i)\bdml\b`)
	nameDDLRe           = regexp.MustCompile(`(?i)\b(?:ddl|truncate|drop)\b`)

	dmlStrongPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bdml\b`),
		regexp.MustCompile(`(?i)\bdml_[a-z0-9_]*`),
		regexp.MustCompile(`(?i)\bunbounded_mutation\b`),
		regexp.MustCompile(`(?i)\binsert_has_columns\b`),
		regexp.MustCompile(`(?i)\bvalues_row_count_max\b`),
		regexp.MustCompile(`(?i)\bwhere_literal_null\b`),
		regexp.MustCompile(`(?i)\binsert_has_null_literal\b`),
		regexp.MustCompile(`(?i)\bin_value_count_max\b`),
		regexp.MustCompile(`(?i)\bupdat(?:e|es|ing)\b`),
		regexp.MustCompile(`(?i)\bdelet(?:e|es|ing)\b`),
		regexp.MustCompile(`(?i)\binsert(?:s|ing)?\b`),
		regexp.MustCompile(`(?i)\breplace\s+into\b`),
		regexp.MustCompile(`(?i)\bwhere\s+clause\b`),
	}
	dmlStrongSubstrings = []string{
		"where\u6761\u4ef6",        // where condition
		"where \u6761\u4ef6",       // where condition
		"\u5b50\u67e5\u8be2",       // subquery
		"\u6279\u91cf\u884c\u6570", // batch rows
		"\u66f4\u65b0",             // update
		"\u5220\u9664",             // delete
		"\u63d2\u5165",             // insert
		"\u5199\u5165",             // write
	}

	ddlStrongPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bddl\b`),
		regexp.MustCompile(`(?i)\bddl_[a-z0-9_]*`),
		regexp.MustCompile(`(?i)\bcreate\s+table\b`),
		regexp.MustCompile(`(?i)\balter\s+table\b`),
		regexp.MustCompile(`(?i)\bdrop\s+table\b`),
		regexp.MustCompile(`(?i)\bdrop\s+database\b`),
		regexp.MustCompile(`(?i)\btruncate\b`),
		regexp.MustCompile(`(?i)\brename\s+table\b`),
		regexp.MustCompile(`(?i)\bcreate\s+view\b`),
		regexp.MustCompile(`(?i)\bcreate\s+(?:unique\s+)?index\b`),
	}
	ddlStrongSubstrings = []string{
		"\u5efa\u8868",             // create table
		"\u5220\u8868",             // drop table
		"\u5220\u9664\u8868",       // drop table
		"\u4fee\u6539\u8868",       // alter table
		"\u5220\u9664\u5217",       // drop column
		"\u8868\u7ed3\u6784",       // table structure
		"\u8868\u540d",             // table name
		"\u8868\u6ce8\u91ca",       // table comment
		"\u5217\u6ce8\u91ca",       // column comment
		"\u5b57\u6bb5\u957f\u5ea6", // column length
		"\u5217\u4f4d\u7f6e",       // column position
		"\u5b57\u7b26\u96c6",       // charset
		"\u6392\u5e8f\u89c4\u5219", // collation
		"\u5206\u533a",             // partition
		"\u89c6\u56fe",             // view
	}

	// Weak DDL schema-property cues can appear inside DML rules as filtering/indexing advice
	// (e.g., "filter UPDATE by primary key or index"), so they carry lower weight than explicit
	// target statement types.
	ddlWeakPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bprimary\s+key\b`),
		regexp.MustCompile(`(?i)\bforeign\s+key\b`),
		regexp.MustCompile(`(?i)\bauto_increment\b`),
		regexp.MustCompile(`(?i)\bunsigned\b`),
		regexp.MustCompile(`(?i)\bcharset\b`),
		regexp.MustCompile(`(?i)\bcollat(?:e|ion)\b`),
		regexp.MustCompile(`(?i)\bpartition\b`),
	}
	ddlWeakSubstrings = []string{
		"\u4e3b\u952e",       // primary key
		"\u5916\u952e",       // foreign key
		"\u7d22\u5f15",       // index
		"\u5217\u7c7b\u578b", // column type
		"\u9ed8\u8ba4\u503c", // default value
		"\u53ef\u7a7a",       // nullable
		"\u81ea\u589e",       // auto increment
	}
)

func scoreSkillCategoryText(text string, multiplier int) (dmlScore, ddlScore int) {
	if strings.TrimSpace(text) == "" {
		return 0, 0
	}
	lower := strings.ToLower(text)
	// Strip negated category mentions (e.g. "non-DDL", "not DDL"), DDL FK clauses ("ON UPDATE CASCADE"),
	// and Chinese DDL table/column actions ("drop table", "drop column") before DML matching.
	forDML := ddlChineseActionRe.ReplaceAllString(
		referentialActionRe.ReplaceAllString(negatedDMLRe.ReplaceAllString(lower, " "), " "),
		" ",
	)
	forDDL := negatedDDLRe.ReplaceAllString(lower, " ")

	for _, re := range dmlStrongPatterns {
		if re.MatchString(forDML) {
			dmlScore += 4 * multiplier
		}
	}
	for _, sub := range dmlStrongSubstrings {
		if strings.Contains(forDML, sub) {
			dmlScore += 4 * multiplier
		}
	}
	for _, re := range ddlStrongPatterns {
		if re.MatchString(forDDL) {
			ddlScore += 4 * multiplier
		}
	}
	for _, sub := range ddlStrongSubstrings {
		if strings.Contains(forDDL, sub) {
			ddlScore += 4 * multiplier
		}
	}
	for _, re := range ddlWeakPatterns {
		if re.MatchString(forDDL) {
			ddlScore += 1 * multiplier
		}
	}
	for _, sub := range ddlWeakSubstrings {
		if strings.Contains(forDDL, sub) {
			ddlScore += 1 * multiplier
		}
	}
	return dmlScore, ddlScore
}

func classifySkillCategory(name, description, ruleText string) skillStatementCategory {
	combined := strings.ToLower(name + " " + description + " " + ruleText)
	if strings.Contains(combined, "ddl-implicit-type-conversion") || strings.Contains(combined, "ddlimplicittypeconversion") {
		return skillCategoryDML
	}
	lowerName := strings.ToLower(strings.TrimSpace(name))
	nameTokens := strings.NewReplacer("-", " ", "_", " ", ".", " ").Replace(lowerName)
	if nameDMLRe.MatchString(negatedDMLRe.ReplaceAllString(nameTokens, " ")) {
		return skillCategoryDML
	}
	if nameDDLRe.MatchString(negatedDDLRe.ReplaceAllString(nameTokens, " ")) {
		return skillCategoryDDL
	}
	// Weight author-specified description and skill name higher than LLM-refined rule prose so
	// auxiliary schema terms ("primary key", "index", "non-DDL") cannot flip a DML rule into DDL.
	nameDML, nameDDL := scoreSkillCategoryText(nameTokens, 2)
	descDML, descDDL := scoreSkillCategoryText(description, 3)
	ruleDML, ruleDDL := scoreSkillCategoryText(ruleText, 1)
	if nameDML+descDML+ruleDML > nameDDL+descDDL+ruleDDL {
		return skillCategoryDML
	}
	return skillCategoryDDL
}

func stripLeadingSQLComments(sqlText string) string {
	s := sqlText
	for {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "--") || strings.HasPrefix(s, "#") {
			idx := strings.IndexByte(s, '\n')
			if idx < 0 {
				return ""
			}
			s = s[idx+1:]
			continue
		}
		if strings.HasPrefix(s, "/*") {
			end := strings.Index(s[2:], "*/")
			if end < 0 {
				return ""
			}
			s = s[2+end+2:]
			continue
		}
		return s
	}
}

func isASCIIWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func hasSQLKeywordPrefix(upper, kw string) bool {
	if !strings.HasPrefix(upper, kw) {
		return false
	}
	if len(upper) == len(kw) {
		return true
	}
	return isASCIIWhitespace(upper[len(kw)])
}

func hasTwoWordSQLKeywordPrefix(upper, first, second string) bool {
	if !hasSQLKeywordPrefix(upper, first) || len(upper) <= len(first) {
		return false
	}
	rest := strings.TrimLeft(upper[len(first):], " \t\n\r")
	return hasSQLKeywordPrefix(rest, second)
}

func isStatementValidForCategory(sqlText string, category skillStatementCategory) bool {
	upper := strings.ToUpper(stripLeadingSQLComments(sqlText))
	if upper == "" {
		return false
	}
	// Standalone SELECT or read-only / session statements are strictly forbidden in skill tests.
	if strings.HasPrefix(upper, "SELECT*") {
		return false
	}
	for _, forbiddenKeyword := range []string{
		"SELECT", "WITH", "SHOW", "EXPLAIN", "DESC", "DESCRIBE",
		"USE", "SET", "BEGIN", "COMMIT", "ROLLBACK",
	} {
		if hasSQLKeywordPrefix(upper, forbiddenKeyword) {
			return false
		}
	}
	if category == skillCategoryDDL {
		for _, ddlKeyword := range []string{
			"CREATE", "ALTER", "DROP", "TRUNCATE", "RENAME",
		} {
			if hasSQLKeywordPrefix(upper, ddlKeyword) {
				return true
			}
		}
		return hasTwoWordSQLKeywordPrefix(upper, "COMMENT", "ON")
	}
	for _, dmlKeyword := range []string{
		"INSERT", "UPDATE", "DELETE", "REPLACE",
	} {
		if hasSQLKeywordPrefix(upper, dmlKeyword) {
			return true
		}
	}
	return false
}

type generatedTestCaseStats struct {
	rawCount          int
	validCompliant    int
	validNonCompliant int
	rejectedCount     int
}

func normalizeAndFilterGeneratedTestCases(
	cases []PromptToolTestCase,
	category skillStatementCategory,
) ([]PromptToolTestCase, generatedTestCaseStats, bool) {
	stats := generatedTestCaseStats{rawCount: len(cases)}
	var compliant, nonCompliant []PromptToolTestCase
	for _, c := range cases {
		sqlText := strings.TrimSpace(c.SQL)
		if !isStatementValidForCategory(sqlText, category) {
			stats.rejectedCount++
			continue
		}
		rationale := strings.TrimSpace(c.Rationale)
		if rationale == "" {
			if c.ExpectedCompliant {
				rationale = "Complies with the skill rule."
			} else {
				rationale = "Violates the skill rule."
			}
		}
		item := PromptToolTestCase{
			SQL:               sqlText,
			ExpectedCompliant: c.ExpectedCompliant,
			Rationale:         rationale,
		}
		if c.ExpectedCompliant {
			if len(compliant) < 5 {
				compliant = append(compliant, item)
			}
		} else {
			if len(nonCompliant) < 5 {
				nonCompliant = append(nonCompliant, item)
			}
		}
	}
	stats.validCompliant = len(compliant)
	stats.validNonCompliant = len(nonCompliant)
	if len(compliant) != 5 || len(nonCompliant) != 5 {
		return nil, stats, false
	}
	out := make([]PromptToolTestCase, 0, 10)
	for i := 0; i < 5; i++ {
		c := compliant[i]
		c.ID = fmt.Sprintf("case-%d", i+1)
		out = append(out, c)
	}
	for i := 0; i < 5; i++ {
		c := nonCompliant[i]
		c.ID = fmt.Sprintf("case-%d", i+6)
		out = append(out, c)
	}
	return out, stats, true
}
