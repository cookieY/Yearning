package help_test

import (
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/help"
)

func TestAnswerPlatformHelp_RoleFilteredAndNoInternalDocsLeaked(t *testing.T) {
	normalUser := domain.Actor{UserID: "u-1", Username: "alice", IsBuiltinAdmin: false}
	ans, citations := help.AnswerPlatformHelp(normalUser, "How do I submit a change order and pass AI review precheck?")
	if len(citations) == 0 {
		t.Fatalf("expected non-empty citations")
	}
	if !strings.Contains(ans, "/orders/new") {
		t.Fatalf("expected route /orders/new in answer, got: %s", ans)
	}
	for _, c := range citations {
		if strings.Contains(c.SourceRef, "AGENTS.md") || strings.Contains(c.SourceRef, "security-review") || strings.Contains(c.SourceRef, "docs/development") {
			t.Fatalf("internal doc leaked in citation: %+v", c)
		}
		if c.RoutePath != nil && strings.HasPrefix(*c.RoutePath, "/admin") {
			t.Fatalf("normal user should not receive /admin route citation: %+v", c)
		}
	}

	// When a non-admin user asks about admin-only topics, /admin entries are withheld and an admin contact notice is returned.
	ansAdminTopic, citationsAdminTopic := help.AnswerPlatformHelp(normalUser, "How do I configure admin datasource governance and system settings?")
	if !strings.Contains(ansAdminTopic, "builtin_admin") {
		t.Fatalf("expected admin-only notice for normal user, got: %s", ansAdminTopic)
	}
	for _, c := range citationsAdminTopic {
		if c.RoutePath != nil && strings.HasPrefix(*c.RoutePath, "/admin") {
			t.Fatalf("normal user should not receive /admin citation: %+v", c)
		}
	}

	// Built-in administrators can receive /admin/settings and /admin/diagnostics citations.
	adminUser := domain.Actor{UserID: "u-admin", Username: "admin", IsBuiltinAdmin: true}
	_, adminCitations := help.AnswerPlatformHelp(adminUser, "How do I configure admin datasource governance and system settings?")
	foundAdmin := false
	for _, c := range adminCitations {
		if c.RoutePath != nil && *c.RoutePath == "/admin/settings" {
			foundAdmin = true
		}
	}
	if !foundAdmin {
		t.Fatalf("expected builtin_admin to receive /admin/settings citation")
	}
}

func TestIsOutOfScope(t *testing.T) {
	if !help.IsOutOfScope("What is the weather in Hangzhou today? Write a poem for me", "") {
		t.Fatalf("expected weather/poem prompt to be out of scope")
	}
	if help.IsOutOfScope("Please insert a warehouse row into table C of database A and generate SQL", "sql_generate") {
		t.Fatalf("expected SQL generation prompt to be in scope")
	}
	if help.IsOutOfScope("Please analyze and optimize this slow query SELECT * FROM orders", "sql_optimize") {
		t.Fatalf("expected SQL optimization prompt to be in scope")
	}
}

func TestEmbeddedMarkdownKnowledgeBaseAndTopKRetrieval(t *testing.T) {
	normalUser := domain.Actor{UserID: "u-1", Username: "alice", IsBuiltinAdmin: false}
	adminUser := domain.Actor{UserID: "u-admin", Username: "admin", IsBuiltinAdmin: true}

	normalArticles := help.VisibleArticles(normalUser)
	adminArticles := help.VisibleArticles(adminUser)
	if len(normalArticles) != 8 {
		t.Fatalf("expected 8 non-admin embedded articles, got %d", len(normalArticles))
	}
	if len(adminArticles) != 12 {
		t.Fatalf("expected 12 total embedded articles for builtin_admin, got %d", len(adminArticles))
	}

	// Verify Top-K retrieval ranks rollback and execution verification articles at the top
	rollbackRanked := help.RetrieveRelevantArticles(
		normalUser,
		"How do I generate a rollback ticket and what does conflicted status mean?",
		"",
		3,
	)
	if len(rollbackRanked) == 0 || rollbackRanked[0].ID != "help-rollback-orders" || !rollbackRanked[0].PrimaryMatch {
		t.Fatalf("expected help-rollback-orders as top primary match, got %+v", rollbackRanked[0])
	}
	primaryIDs := help.PrimaryMatchedArticleIDs(rollbackRanked)
	if len(primaryIDs) == 0 || len(primaryIDs) > 3 {
		t.Fatalf("expected between 1 and 3 primary matched IDs, got %v", primaryIDs)
	}

	// Verify system prompt includes Global Catalog Index for all visible articles and Detailed Guide for primary matches
	sysPrompt := help.BuildScopeGateSystemPrompt(domain.CapabilityPlatformHelp, rollbackRanked, false)
	if !strings.Contains(sysPrompt, "Global Module Catalog Index") {
		t.Fatalf("expected Global Module Catalog Index in system prompt")
	}
	if !strings.Contains(sysPrompt, "Detailed Documentation for Top-Matched Platform Guides") {
		t.Fatalf("expected Detailed Documentation section in system prompt")
	}
	if !strings.Contains(sysPrompt, "evidence_incomplete") {
		t.Fatalf("expected detailed rollback FAQ content in system prompt")
	}

	// Verify admin-only review rules and notifications/audit articles are retrieved for builtin_admin
	adminRanked := help.RetrieveRelevantArticles(
		adminUser,
		"Where do I configure webhook notifications and watermarked SQL reveal audit logs?",
		"",
		2,
	)
	if len(adminRanked) == 0 || adminRanked[0].ID != "help-admin-notifications-audit" {
		t.Fatalf("expected help-admin-notifications-audit as top match for admin, got %+v", adminRanked[0])
	}

	// Verify LDAP and OIDC configuration query ranks help-admin-governance and includes detailed LDAP/OIDC guide
	idpRanked := help.RetrieveRelevantArticles(
		adminUser,
		"How do I configure LDAP ldaps starttls and OIDC Single Sign-On in System Settings?",
		"",
		2,
	)
	if len(idpRanked) == 0 || idpRanked[0].ID != "help-admin-governance" {
		t.Fatalf("expected help-admin-governance as top match for LDAP/OIDC config, got %+v", idpRanked[0])
	}
	idpPrompt := help.BuildScopeGateSystemPrompt(domain.CapabilityPlatformHelp, idpRanked, false)
	if !strings.Contains(idpPrompt, "/admin/settings/general") || !strings.Contains(idpPrompt, "user_filter") || !strings.Contains(idpPrompt, "issuer_url") {
		t.Fatalf("expected detailed LDAP and OIDC configuration guide in system prompt")
	}
}

func TestToPromptProfileArticles_FallbacksAndEnglishFields(t *testing.T) {
	articles := []help.Article{
		{
			ID:              "help-en",
			Title:           "Fallback Title Unused",
			TitleEN:         "AI Provider Setup",
			RoutePath:       "/admin/ai-providers",
			SummaryZH:       "Fallback Summary Unused",
			SummaryEN:       "Configure OpenAI-compatible AI providers.",
			StepsZH:         []string{"Unused step"},
			StepsEN:         []string{"Open Providers page.", "Save config."},
			DetailedGuideEN: "  Detailed guide for operators.  ",
			PrimaryMatch:    true,
		},
		{
			ID:           "help-fallback",
			Title:        "Fallback Title Used",
			RoutePath:    "/orders",
			SummaryZH:    "  Fallback Summary Used  ",
			StepsZH:      []string{"Fallback Step 1", "Fallback Step 2"},
			PrimaryMatch: false,
		},
	}

	converted := help.ToPromptProfileArticles(articles)
	if len(converted) != 2 {
		t.Fatalf("expected 2 converted articles, got %d", len(converted))
	}
	if converted[0].Title != "AI Provider Setup" ||
		converted[0].Summary != "Configure OpenAI-compatible AI providers." ||
		len(converted[0].Steps) != 2 ||
		converted[0].Steps[0] != "Open Providers page." ||
		converted[0].DetailedGuide != "Detailed guide for operators." ||
		!converted[0].PrimaryMatch {
		t.Fatalf("unexpected primary English article conversion: %+v", converted[0])
	}
	// Mutating caller Steps must not alias the converted output.
	articles[0].StepsEN[0] = "Mutated"
	if converted[0].Steps[0] != "Open Providers page." {
		t.Fatalf("Steps slice aliased input slice: %+v", converted[0].Steps)
	}

	if converted[1].Title != "Fallback Title Used" ||
		converted[1].Summary != "Fallback Summary Used" ||
		len(converted[1].Steps) != 2 ||
		converted[1].Steps[1] != "Fallback Step 2" ||
		converted[1].PrimaryMatch {
		t.Fatalf("unexpected fallback article conversion: %+v", converted[1])
	}
}
