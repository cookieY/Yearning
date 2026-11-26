package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	assistantdomain "github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	diagdomain "github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
	execdomain "github.com/cookieY/YearningX/backend/internal/execution/domain"
	querydomain "github.com/cookieY/YearningX/backend/internal/query/domain"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
	reviewdomain "github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/settings"
	"github.com/cookieY/YearningX/backend/internal/tasks"
)

func TestRespondErrUnifiedCodedAndSentinelProfiles(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Load canonical error codes and operation error profiles to assert contract alignment.
	catalogRaw, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "contracts", "error-codes.json"))
	if err != nil {
		t.Fatalf("read error-codes.json: %v", err)
	}
	var catalog struct {
		BusinessErrors []struct {
			Code         int    `json:"err_code"`
			Name         string `json:"name"`
			Retryability string `json:"retryability"`
		} `json:"business_errors"`
	}
	if err := json.Unmarshal(catalogRaw, &catalog); err != nil {
		t.Fatalf("parse error-codes.json: %v", err)
	}
	codeByName := make(map[string]int, len(catalog.BusinessErrors))
	for _, entry := range catalog.BusinessErrors {
		codeByName[entry.Name] = entry.Code
	}

	profilesRaw, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "contracts", "operation-error-profiles.json"))
	if err != nil {
		t.Fatalf("read operation-error-profiles.json: %v", err)
	}
	var profilesDoc struct {
		Profiles map[string][]string `json:"profiles"`
	}
	if err := json.Unmarshal(profilesRaw, &profilesDoc); err != nil {
		t.Fatalf("parse operation-error-profiles.json: %v", err)
	}

	// Verify all 6 domain *Error types satisfy bizerr.Coded at compile time.
	var (
		_ bizerr.Coded = (*assistantdomain.Error)(nil)
		_ bizerr.Coded = (*orderdomain.Error)(nil)
		_ bizerr.Coded = (*diagdomain.Error)(nil)
		_ bizerr.Coded = (*execdomain.Error)(nil)
		_ bizerr.Coded = (*querydomain.Error)(nil)
		_ bizerr.Coded = (*reviewdomain.Error)(nil)
	)

	cases := []struct {
		name          string
		err           error
		profile       string
		contractName  string
		wantCode      int
		wantRetryable bool
		wantMetaKey   string
		wantMetaVal   string
	}{
		{
			name:         "assistant_out_of_scope",
			err:          assistantdomain.ErrOutOfScope("not db"),
			profile:      "assistant_message_send",
			contractName: "ASSISTANT_OUT_OF_SCOPE",
			wantCode:     6001,
		},
		{
			name:          "assistant_capacity_retryable",
			err:           assistantdomain.ErrCapacityExhausted("busy"),
			contractName:  "SYSTEM_CAPACITY_EXHAUSTED",
			wantCode:      1012,
			wantRetryable: true,
			wantMetaKey:   "retry_after_ms",
		},
		{
			name:         "changeorder_submission_gate_colon_reason",
			err:          orderdomain.ErrSubmissionGate("blocking_findings"),
			profile:      "draft_submit",
			contractName: "SUBMISSION_GATE_FAILED",
			wantCode:     2013,
			wantMetaKey:  "reason",
			wantMetaVal:  "blocking_findings",
		},
		{
			name:         "changeorder_not_frozen_actor",
			err:          orderdomain.ErrNotFrozenActor(),
			profile:      "order_decision",
			contractName: "NOT_FROZEN_ACTOR",
			wantCode:     3001,
		},
		{
			name:         "diagnostics_snapshot_rate_limited",
			err:          diagdomain.ErrSnapshotRateLimited(30),
			contractName: "DIAGNOSTICS_SNAPSHOT_RATE_LIMITED",
			wantCode:     7006,
			wantMetaKey:  "retry_after_seconds",
		},
		{
			name:         "execution_preflight_colon_reason_and_detail",
			err:          execdomain.ErrPreflightFailed("ddl_lock_risk"),
			profile:      "execution_start",
			contractName: "EXECUTION_PREFLIGHT_FAILED",
			wantCode:     3006,
			wantMetaKey:  "detail",
			wantMetaVal:  "ddl_lock_risk",
		},
		{
			name:         "query_cursor_invalid_with_detail",
			err:          querydomain.ErrCursorInvalid().WithDetail("cursor_owner_mismatch"),
			contractName: "QUERY_CURSOR_INVALID",
			wantCode:     4010,
			wantMetaKey:  "detail",
			wantMetaVal:  "cursor_owner_mismatch",
		},
		{
			name:         "review_provider_failed_with_detail_and_fields",
			err:          &reviewdomain.Error{Code: reviewdomain.CodeAIProviderFailed, Name: "AI_PROVIDER_FAILED", Detail: "upstream_502", Fields: map[string][]string{"model": {"unsupported"}}},
			profile:      "draft_review",
			contractName: "AI_PROVIDER_FAILED",
			wantCode:     2011,
			wantMetaKey:  "detail",
			wantMetaVal:  "upstream_502",
		},
		{
			name:         "review_sentinel_not_found",
			err:          reviewapp.ErrNotFound,
			profile:      "draft_update",
			contractName: "RESOURCE_NOT_FOUND",
			wantCode:     1002,
		},
		{
			name:         "knowledge_sentinel_validation",
			err:          knowledge.ErrValidation,
			profile:      "admin_create",
			contractName: "VALIDATION_FAILED",
			wantCode:     1001,
		},
		{
			name:         "settings_sentinel_token_invalid",
			err:          settings.ErrTokenInvalid,
			profile:      "settings_replace",
			contractName: "PRECONDITION_REQUIRED",
			wantCode:     1011,
		},
		{
			name:          "tasks_queue_full_retryable",
			err:           tasks.ErrQueueFull,
			profile:       "connection_test",
			contractName:  "SYSTEM_CAPACITY_EXHAUSTED",
			wantCode:      1012,
			wantRetryable: true,
			wantMetaKey:   "retry_after_ms",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.contractName != "" {
				if gotCatalogCode, ok := codeByName[tc.contractName]; !ok {
					t.Fatalf("contract name %q missing from error-codes.json", tc.contractName)
				} else if gotCatalogCode != tc.wantCode {
					t.Fatalf("catalog code for %q = %d, want %d", tc.contractName, gotCatalogCode, tc.wantCode)
				}
			}
			if tc.profile != "" {
				allowed, ok := profilesDoc.Profiles[tc.profile]
				if !ok {
					t.Fatalf("profile %q missing from operation-error-profiles.json", tc.profile)
				}
				found := false
				for _, name := range allowed {
					if name == tc.contractName {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("error %q not allowed by profile %q (%v)", tc.contractName, tc.profile, allowed)
				}
			}

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set(ginRequestIDKey, "req-"+tc.name)
			respondErr(c, tc.err)

			if w.Code != http.StatusOK {
				t.Fatalf("HTTP status = %d, want 200 (body=%s)", w.Code, w.Body.String())
			}
			var env struct {
				ErrCode   int            `json:"err_code"`
				Message   string         `json:"message"`
				RequestID string         `json:"request_id"`
				Retryable bool           `json:"retryable"`
				Meta      map[string]any `json:"meta"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if env.ErrCode != tc.wantCode || env.Retryable != tc.wantRetryable || env.RequestID != "req-"+tc.name || env.Message == "" {
				t.Fatalf("unexpected envelope: %+v (want code=%d retryable=%v)", env, tc.wantCode, tc.wantRetryable)
			}
			if tc.wantMetaKey != "" {
				val, ok := env.Meta[tc.wantMetaKey]
				if !ok {
					t.Fatalf("expected meta[%q] in %+v", tc.wantMetaKey, env.Meta)
				}
				if tc.wantMetaVal != "" && val != tc.wantMetaVal {
					t.Fatalf("meta[%q] = %v, want %q", tc.wantMetaKey, val, tc.wantMetaVal)
				}
			}
		})
	}

	// Unrecognized internal error must emit RFC 9457 500 Problem Details without leaking error text.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(ginRequestIDKey, "req-internal-500")
	respondErr(c, errors.New("pq: secret connection string leaked"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("internal error HTTP status = %d, want 500", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("internal error Content-Type = %q, want application/problem+json", ct)
	}
	if strings.Contains(w.Body.String(), "secret connection string") {
		t.Fatalf("internal error leaked raw message: %s", w.Body.String())
	}
}
