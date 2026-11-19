package application

import (
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

func TestExecutionValidityFindingLocalizedAndEvidenceBound(t *testing.T) {
	for _, locale := range []string{"zh-CN", "en-US"} {
		v := PreconditionViolation{Ordinal: 3, Object: fingerprint.ValidityObject{Kind: fingerprint.ValidityObjectTable, Token: "table_7"}, Required: fingerprint.ValidityRequirementPresent, Observed: ValidityFactAbsent, EvidenceIDs: []string{"fact-1"}}
		finding := executionValidityFinding(v, "group-2", locale)
		if finding.severity != domain.SeverityCritical || finding.groupID != "group-2" || finding.category != "correctness" || len(finding.evidence) != 1 || finding.evidence[0] != "fact-1" {
			t.Fatalf("incorrect Critical proof binding: %+v", finding)
		}
		if !strings.Contains(finding.message, "table_7") || !strings.Contains(finding.message, "3") {
			t.Fatalf("missing anonymous location: %+v", finding)
		}
		// Owner ruling 2026-10-30: the title must state the concrete defect
		// (object + observed state + statement that cannot execute), not the
		// abstract "prerequisite not met" label.
		if locale == "zh-CN" {
			if !strings.Contains(finding.title, "\u5bf9\u8c61 table_7 \u4e0d\u5b58\u5728") || !strings.Contains(finding.title, "\u7b2c 3 \u6761\u8bed\u53e5\u65e0\u6cd5\u6267\u884c") {
				t.Fatalf("Chinese title not concrete: %s", finding.title)
			}
			if !strings.Contains(finding.message, "\u4e0d\u5b58\u5728") || strings.Contains(finding.message, "absent") {
				t.Fatalf("Chinese prose not localized: %s", finding.message)
			}
		} else if !strings.Contains(finding.title, "Object table_7 does not exist") || !strings.Contains(finding.title, "statement 3 cannot execute") {
			t.Fatalf("English title not concrete: %+v", finding)
		} else if !strings.Contains(finding.message, "absent") {
			t.Fatalf("English prose not localized: %+v", finding)
		}
		finding.evidence[0] = "changed"
		if v.EvidenceIDs[0] != "fact-1" {
			t.Fatal("finding mutated input proof")
		}
	}
}
