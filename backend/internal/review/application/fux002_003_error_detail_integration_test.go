//go:build integration

// fux002_003_error_detail_integration_test.go v6.4 acceptance findings F-UX-002/
// F-UX-003 error visibility regression: provider unique conflicts distinguish Detail by axis (1005),
// and StartRun without SQL returns a Detail naming the missing SQL cause (1001). Both pass verbatim
// to the UI via meta.detail in MapReviewError.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// probeOKStub is a conformant probe stub (same shape as existing b7 test cases).
func probeOKStub(ep provider.Endpoint) (provider.Client, error) {
	return provider.NewLocalFakeAdapter(ep, func(req provider.Request) ([]byte, int, error) {
		raw, _ := json.Marshal(map[string]any{
			"schema_version": 1, "review_run_id": req.ReviewRunID, "outcome": "complete",
			"summary": "Connection test.", "findings": []any{}, "tool_requests": []any{},
		})
		return raw, 200, nil
	}), nil
}

// TestB7ProviderUniqueConflictDetailAxis F-UX-002: 1005 Detail on collisions against the
// selection_priority and name unique axes must name the colliding field rather than sharing a generic
// "resource with same identity already exists" error.
func TestB7ProviderUniqueConflictDetailAxis(t *testing.T) {
	b := newB7Fixture(t)
	origDial := probeDial
	probeDial = probeOKStub
	defer func() { probeDial = origDial }()

	// Seed provider: name='primary', selection_priority=1 (see seed).
	// Collide on the priority axis.
	_, err := b.svc.CreateAIProvider(context.Background(), b.userID, CreateAIProviderInput{
		Name: "other-vendor", ProviderKind: "openai_compatible",
		BaseURL: "https://other.example.com", ModelName: "m", Enabled: false,
		SelectionPriority: 1, APIKey: "k",
	})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != 1005 {
		t.Fatalf("priority collision: want 1005, got %v", err)
	}
	if !strings.Contains(de.Detail, "selection_priority=1") {
		t.Fatalf("priority collision detail: %q", de.Detail)
	}

	// Collide on the name axis (non-conflicting priority).
	_, err = b.svc.CreateAIProvider(context.Background(), b.userID, CreateAIProviderInput{
		Name: "primary", ProviderKind: "openai_compatible",
		BaseURL: "https://name.example.com", ModelName: "m", Enabled: false,
		SelectionPriority: 9, APIKey: "k",
	})
	if !errors.As(err, &de) || de.Code != 1005 {
		t.Fatalf("name collision: want 1005, got %v", err)
	}
	if !strings.Contains(de.Detail, "name") || strings.Contains(de.Detail, "selection_priority=") {
		t.Fatalf("name collision detail: %q", de.Detail)
	}

	// Replacing without colliding on either axis (keeping its own values) is unaffected by the Detail path.
	view, err := b.svc.CreateAIProvider(context.Background(), b.userID, CreateAIProviderInput{
		Name: "clean-vendor", ProviderKind: "openai_compatible",
		BaseURL: "https://clean.example.com", ModelName: "m", Enabled: false,
		SelectionPriority: 5, APIKey: "k",
	})
	if err != nil {
		t.Fatalf("clean create: %v", err)
	}
	if _, err := b.svc.ReplaceAIProvider(context.Background(), view.ID, ReplaceAIProviderInput{
		ExpectVersion: view.Version, Name: view.Name, ProviderKind: "openai_compatible",
		BaseURL: view.BaseURL, ModelName: view.ModelName, Enabled: false,
		SelectionPriority: 5,
	}); err != nil {
		t.Fatalf("self replace: %v", err)
	}
}

// TestStartRunWithoutSQLStatesCause F-UX-003: starting an ephemeral precheck without SQL
// returns 1001 with Detail stating that SQL is missing rather than a generic "invalid request parameters".
func TestStartRunWithoutSQLStatesCause(t *testing.T) {
	f := newFixture(t)
	view, err := f.svc.CreateDraft(context.Background(), f.owner, CreateDraftInput{
		FlowID: f.flowID, Title: "no-sql", Description: "",
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	_, err = f.svc.StartEphemeralRun(context.Background(), f.owner, &AgentRunner{}, EphemeralStartInput{
		FlowID: f.flowID, DraftID: view.ID, SQL: nil, IdempotencyKey: "fux003-no-sql",
	})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != 1001 {
		t.Fatalf("start ephemeral run without sql: want 1001, got %v", err)
	}
	if !strings.Contains(de.Detail, "SQL must not be empty") {
		t.Fatalf("missing-sql detail: %q", de.Detail)
	}
}
