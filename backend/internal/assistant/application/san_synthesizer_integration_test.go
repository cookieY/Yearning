//go:build integration

package application

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func insertTestProviderEndpoint(t *testing.T, fx *assistantTestFixture, name, baseURL, modelName string, priority int, configJSON string) {
	t.Helper()
	provID := uuid.NewString()
	secEnv, err := fx.crypter.Seal(crypto.PurposePayload, []byte("sk-test-san-key"), crypto.AAD{
		DeploymentID: fx.crypter.DeploymentID(),
		Table:        "ai_providers",
		PrimaryKey:   provID,
		Column:       "secret_ciphertext",
		Purpose:      "provider_secret",
	})
	if err != nil {
		t.Fatalf("seal api key: %v", err)
	}
	if configJSON == "" {
		configJSON = "{}"
	}
	if err := fx.db.Exec(`INSERT INTO ai_providers (
		id, name, provider_kind, base_url, model_name, enabled, selection_priority,
		privacy_contract_hash, output_schema_hash, configuration,
		secret_ciphertext, secret_nonce, secret_key_id, secret_algorithm,
		version, created_by_user_id, created_at, updated_at
	) VALUES (
		?, ?, 'openai_compatible', ?, ?, true, ?,
		repeat('a', 64), repeat('b', 64), ?::jsonb,
		?, ?, ?, 'aes-256-gcm',
		1, ?, now(), now()
	)`,
		provID, name, baseURL, modelName, priority, configJSON,
		secEnv.Ciphertext, secEnv.Nonce, secEnv.KeyID, fx.userA.UserID,
	).Error; err != nil {
		t.Fatalf("insert provider %s: %v", name, err)
	}
}

func TestServices_SendMessageSANEndToEndAndFailClosed(t *testing.T) {
	ctx := context.Background()
	fx := newAssistantFixture(t)
	insertTestProviderEndpoint(t, fx, "local-san-e2e", "http://192.168.10.20:8000/v1", "san-4b", 1, `{}`)

	schemaName := "core_db"
	tableName := "warehouses"

	var seenRequests []capturedChatRequest
	overrideSQL := ""
	ps := NewProviderSynthesizer(fx.db, fx.crypter, &DeterministicSynthesizer{})
	ps.HTTPClient = &provider.HTTPClient{
		Inner: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				capReq := decodeCapturedRequest(t, req)
				seenRequests = append(seenRequests, capReq)
				sqlStr := "INSERT INTO core_db.warehouses (code, name) VALUES ('WH-HZ-01', 'HangzhouHub01')"
				if overrideSQL != "" {
					sqlStr = overrideSQL
				}
				contentJSON, _ := json.Marshal(map[string]any{
					"out_of_scope":        false,
					"resolved_capability": "sql_generate",
					"session_title":       "Warehouse Insert",
					"assistant_reply":     "Generated insert statement for core_db.warehouses.",
					"sql":                 sqlStr,
					"assumptions":         []string{"code WH-HZ-01 is unique"},
					"next_steps":          []string{"Create change order"},
					"semantic_warnings":   []string{},
				})
				bodyJSON, _ := json.Marshal(map[string]any{
					"choices": []map[string]any{{"message": map[string]any{"content": string(contentJSON)}}},
					"usage":   map[string]any{"prompt_tokens": 30, "completion_tokens": 20},
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(string(bodyJSON))),
				}, nil
			}),
		},
	}
	fx.svc.Synthesizer = ps

	sess, err := fx.svc.CreateSession(ctx, fx.userA, CreateSessionInput{
		Title:              "SAN E2E Session",
		ActiveDatasourceID: &fx.dsChangeID,
		ActiveFlowID:       &fx.changeFlowID,
		ActiveSchemaName:   &schemaName,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var streamedEvents []string
	res, err := fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Insert a warehouse row WH-HZ-01 into warehouses",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsChangeID,
		FlowID:         &fx.changeFlowID,
		SchemaName:     &schemaName,
		TargetTable:    &tableName,
		OutputLocale:   "en-US",
		OnStreamEvent: func(ev StreamEvent) {
			streamedEvents = append(streamedEvents, ev.Event)
		},
	})
	if err != nil {
		t.Fatalf("SendMessage SAN error: %v", err)
	}
	if res.SQLDraft == nil || !strings.Contains(res.SQLDraft.SQLText, "core_db.warehouses") {
		t.Fatalf("expected persisted raw SQL draft referencing core_db.warehouses, got %+v", res.SQLDraft)
	}
	if len(seenRequests) != 1 || !strings.Contains(seenRequests[0].User, "core_db") || !strings.Contains(seenRequests[0].User, "warehouses") {
		t.Fatalf("expected raw metadata & prompt sent to SAN endpoint, got %+v", seenRequests)
	}

	// Verify SAN validation failure fails closed WITHOUT falling back to DeterministicSynthesizer.
	overrideSQL = "SELECT * FROM core_db.warehouses WHERE code = :val_str_1"
	_, err = fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Query warehouses again",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsChangeID,
		FlowID:         &fx.changeFlowID,
		SchemaName:     &schemaName,
		TargetTable:    &tableName,
		OutputLocale:   "en-US",
	})
	if err == nil || !hasErrCode(err, domain.ErrSQLRestorationInvalid("").Code) {
		t.Fatalf("expected fail-closed 6004 SQL_RESTORATION_INVALID on malformed SAN SQL without DeterministicSynthesizer fallback, got %v", err)
	}

	// Verify unauthorized schema/table returned by SAN model fails closed with 6004.
	overrideSQL = "SELECT user, authentication_string FROM mysql.user"
	_, err = fx.svc.SendMessage(ctx, fx.userA, SendMessageInput{
		SessionID:      sess.ID,
		Content:        "Read warehouses",
		CapabilityHint: string(domain.CapabilitySQLGenerate),
		DatasourceID:   &fx.dsChangeID,
		FlowID:         &fx.changeFlowID,
		SchemaName:     &schemaName,
		TargetTable:    &tableName,
		OutputLocale:   "en-US",
	})
	if err == nil || !hasErrCode(err, domain.ErrSQLRestorationInvalid("").Code) {
		t.Fatalf("expected fail-closed 6004 SQL_RESTORATION_INVALID on unauthorized cross-schema SQL, got %v", err)
	}
}
