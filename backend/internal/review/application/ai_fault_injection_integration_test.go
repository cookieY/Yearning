//go:build integration

package application

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"github.com/cookieY/YearningX/backend/internal/review/tool"
)

// TestB7DEF06InfoSeverityPersistence verifies DEF-06 using an in-process
// httptest.NewServer + real PostgreSQL persistence: an informational finding
// (severity="info") reaches ready/gate_passed=true and materializes cleanly into
// review_findings without failing review_findings_severity_check.
func TestB7DEF06InfoSeverityPersistence(t *testing.T) {
	b := newB7Fixture(t)
	t.Cleanup(addrguard.SetLoopbackBypassForTest(true))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req provider.Request
		for _, m := range body.Messages {
			if m.Role != "user" {
				continue
			}
			if idx := len("Review request JSON payload follows. Respond ONLY with a single json object matching the required JSON Schema.\n"); len(m.Content) > idx {
				_ = json.Unmarshal([]byte(m.Content[idx:]), &req)
			}
		}
		gid := ""
		if len(req.AnonymizedStatementGroups) > 0 {
			gid = req.AnonymizedStatementGroups[0].GroupID
		}
		var findings []map[string]any
		var tools []map[string]any
		summary := "Informational observation only."
		if len(req.NormalizedEvidence) == 0 {
			summary = "Collecting table metadata before finalizing observation."
			findings = []map[string]any{}
			var err error
			tools, err = scopedMetadataToolRequests(req, uuid.NewString())
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		} else {
			tools = []map[string]any{}
			findings = []map[string]any{{
				"finding_key":           "model.advisory",
				"category":              "operability",
				"severity":              "info",
				"title":                 "Table growth advisory",
				"message":               "Consider archiving historical rows periodically.",
				"suggestion":            "Schedule offline archival during maintenance window.",
				"fingerprint_group_ids": []string{gid},
				"evidence_ids":          []string{},
				"fact_basis":            "model_only",
			}}
		}
		contentObj := map[string]any{
			"schema_version": 1,
			"review_run_id":  req.ReviewRunID,
			"outcome":        "complete",
			"summary":        summary,
			"findings":       findings,
			"tool_requests":  tools,
		}
		contentRaw, _ := json.Marshal(contentObj)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]any{"content": string(contentRaw)},
			}},
			"usage": map[string]any{"prompt_tokens": 42, "completion_tokens": 24},
		})
	}))
	defer srv.Close()

	if err := b.db.Exec(`update ai_providers set base_url = ? where id = ?`, srv.URL, b.providerID).Error; err != nil {
		t.Fatalf("update provider base_url: %v", err)
	}
	hc := provider.NewHTTPClient()
	deps := AgentDeps{
		Dial: func(ep provider.Endpoint) (provider.Client, error) {
			return provider.NewOpenAIAdapter(ep, hc)
		},
		FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
			return &fakeFactSource{}, func() {}, nil
		},
	}

	sqlText := "update orders set amount = amount + 1 where id = 1;\n"
	draft := b.createDraftWithSQL(t, sqlText)
	er, runID := b.startEphemeralTerminal(t, b.owner, deps, EphemeralStartInput{
		FlowID: b.flowID, DraftID: draft.ID, SQL: []byte(sqlText),
		OutputLocale:   "en-US",
		IdempotencyKey: "def06-" + runKeySuffix(),
	})
	if er.State() != domain.RunStateReady {
		t.Fatalf("expected ready for severity=info observation, got %s (code=%s failure=%q)",
			er.State(), ephemeralRunCodeString(er), er.Failure)
	}
	b.materializeEphemeral(t, er, draft.ID, draft.Revision)

	var severities []string
	if err := b.db.Raw(`
		select rf.severity
		from review_findings rf
		join stage_review_snapshots s on s.id = rf.stage_review_id
		where s.review_run_id = ?`, runID).Scan(&severities).Error; err != nil {
		t.Fatalf("query persisted review_findings: %v", err)
	}
	if len(severities) != 1 || severities[0] != "info" {
		t.Fatalf("persisted severities=%v, want [\"info\"]", severities)
	}
}

// TestB7DEF14ProviderAnomalyBoundedStates verifies DEF-14 via in-process
// httptest.NewServer fault injection: HTTP 429, malformed JSON, and upstream
// timeouts always converge to bounded non-ready terminal states without panics
// or unbounded retry loops.
func TestB7DEF14ProviderAnomalyBoundedStates(t *testing.T) {
	cases := []struct {
		name     string
		timeout  time.Duration
		handler  func(calls *atomic.Int64) http.HandlerFunc
		maxCalls int64
	}{
		{
			name:    "http_429_rate_limit",
			timeout: 2 * time.Second,
			handler: func(calls *atomic.Int64) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`))
				}
			},
			maxCalls: 3,
		},
		{
			name:    "malformed_json_output",
			timeout: 2 * time.Second,
			handler: func(calls *atomic.Int64) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"choices": []map[string]any{{
							"finish_reason": "stop",
							"message":       map[string]any{"content": "not-valid-json{{{"},
						}},
						"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 5},
					})
				}
			},
			maxCalls: 8,
		},
		{
			name:    "timeout_exceeded",
			timeout: 80 * time.Millisecond,
			handler: func(calls *atomic.Int64) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					select {
					case <-time.After(300 * time.Millisecond):
					case <-r.Context().Done():
						return
					}
					w.WriteHeader(http.StatusGatewayTimeout)
				}
			},
			maxCalls: 3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newB7Fixture(t)
			t.Cleanup(addrguard.SetLoopbackBypassForTest(true))

			var calls atomic.Int64
			srv := httptest.NewServer(tc.handler(&calls))
			defer srv.Close()

			if err := b.db.Exec(`update ai_providers set base_url = ? where id = ?`, srv.URL, b.providerID).Error; err != nil {
				t.Fatalf("update provider base_url: %v", err)
			}
			hc := &provider.HTTPClient{Inner: &http.Client{Timeout: tc.timeout}}
			deps := AgentDeps{
				Dial: func(ep provider.Endpoint) (provider.Client, error) {
					return provider.NewOpenAIAdapter(ep, hc)
				},
				FactSourceFor: func(context.Context, string, FrozenDatasourceTarget, string) (tool.FactSource, func(), error) {
					return &fakeFactSource{}, func() {}, nil
				},
			}
			sqlText := "update orders set amount = amount + 1 where id = 1;\n"
			draft := b.createDraftWithSQL(t, sqlText)
			er, runID := b.startEphemeralTerminal(t, b.owner, deps, EphemeralStartInput{
				FlowID: b.flowID, DraftID: draft.ID, SQL: []byte(sqlText),
				IdempotencyKey: "def14-" + tc.name + "-" + runKeySuffix(),
			})
			if er.State() == domain.RunStateReady || er.State() == domain.RunStateBlocked {
				t.Fatalf("%s unexpectedly reached %s", tc.name, er.State())
			}
			if got := calls.Load(); got == 0 || got > tc.maxCalls {
				t.Fatalf("%s upstream calls=%d, want 1..%d", tc.name, got, tc.maxCalls)
			}
			b.materializeEphemeral(t, er, draft.ID, draft.Revision)
			var persistedState string
			if err := b.db.Raw(`select state from review_runs where id = ?`, runID).Scan(&persistedState).Error; err != nil {
				t.Fatalf("read persisted run state: %v", err)
			}
			if persistedState == string(domain.RunStateReady) {
				t.Fatalf("%s persisted ready state despite fault", tc.name)
			}
		})
	}
}
