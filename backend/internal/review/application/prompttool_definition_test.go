package application

import (
	"strings"
	"testing"
)

// section 18.66: definition = {knowledge_text, max_severity, version} — finding template surface is retired,
// leaving validation of non-empty/bounded body, severity enum, and version.
func TestValidatePromptToolDefinitionThreeFieldModel(t *testing.T) {
	base := PromptToolDefinition{
		KnowledgeText: "require where",
		MaxSeverity:   "high",
		Version:       1,
	}
	if err := ValidatePromptToolDefinition(base); err != nil {
		t.Fatalf("valid definition rejected: %v", err)
	}
	bad := base
	bad.MaxSeverity = "severe"
	if err := ValidatePromptToolDefinition(bad); err == nil {
		t.Fatal("unknown max_severity accepted")
	}
	bad = base
	bad.MaxSeverity = ""
	if err := ValidatePromptToolDefinition(bad); err == nil {
		t.Fatal("empty max_severity accepted")
	}
	bad = base
	bad.KnowledgeText = "   "
	if err := ValidatePromptToolDefinition(bad); err == nil {
		t.Fatal("blank knowledge_text accepted")
	}
}

func TestPromptToolPreviewKeepsLegacySeverityCompatibilityOnly(t *testing.T) {
	rendered := renderPromptToolInjection("rule", nil, PromptToolDefinition{
		KnowledgeText: "require where",
		MaxSeverity:   "medium",
		Version:       1,
	})
	if !strings.Contains(rendered, "effective_severity: critical") {
		t.Fatalf("effective Critical policy missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "legacy_max_severity_compatibility: info..medium") {
		t.Fatalf("legacy compatibility metadata missing:\n%s", rendered)
	}
	if strings.Contains(rendered, "severity_ceiling") {
		t.Fatalf("legacy severity ceiling still presented as effective policy:\n%s", rendered)
	}
}
