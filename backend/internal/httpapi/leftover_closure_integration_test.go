//go:build integration

// leftover_closure_integration_test.go BE-B13-LEFTOVER-CLOSURE (D005):
// AAD sealing of query_access_requests.flow_snapshot is bound to row primary key --
// decrypts with the right PK (row ID); legacy state constant "pending" or another row ID must fail authentication.
// Covers runtime sealing path (not migration backfill) by creating a request via HTTP and asserting Open on DB ciphertext.
package httpapi

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

func TestLeftoverClosureAADRowKeySealing(t *testing.T) {
	fx := newB11Fixture(t)
	code, body, raw := fx.requester.doWithHeaders("POST", "/api/v4/query-access-requests", map[string]any{
		"flow_id":         fx.qFlowID,
		"datasource_ids":  []string{fx.qDsID},
		"requested_until": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano),
		"reason":          "aad row key closure",
	}, map[string]string{
		"Idempotency-Key": "aad-" + uuid.NewString()[:12],
		"X-CSRF-Token":    fx.requester.cookies["yearning_csrf"],
	})
	if code != 200 || errCode(body) != 0 {
		t.Fatalf("create access request: %d %s", code, raw)
	}
	reqID := body["data"].(map[string]any)["id"].(string)

	var row struct {
		Ciphertext []byte
		Nonce      []byte
		KeyID      string
		Algorithm  string
	}
	if reqID == "" {
		t.Fatal("empty request id")
	}
	if err := fx.db.Raw(`select flow_snapshot_ciphertext as ciphertext,
		flow_snapshot_nonce as nonce, flow_snapshot_key_id as key_id,
		flow_snapshot_algorithm as algorithm
		from query_access_requests where id = ?`, reqID).Scan(&row).Error; err != nil {
		t.Fatalf("load sealed row: %v", err)
	}
	env := crypto.Envelope{
		Ciphertext: row.Ciphertext, Nonce: row.Nonce,
		KeyID: row.KeyID, Algorithm: row.Algorithm,
	}
	aad := func(pk string) crypto.AAD {
		return crypto.AAD{
			DeploymentID: fx.svc.Crypter.DeploymentID(),
			Table:        "query_access_requests", PrimaryKey: pk,
			Column: "flow_snapshot", Purpose: crypto.PurposePayload,
		}
	}
	if _, err := fx.svc.Crypter.Open(crypto.PurposePayload, env, aad(reqID)); err != nil {
		t.Fatalf("open with row primary key must succeed: %v", err)
	}
	for _, wrongPK := range []string{"pending", "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac", reqID + "x"} {
		if _, err := fx.svc.Crypter.Open(crypto.PurposePayload, env, aad(wrongPK)); err == nil {
			t.Fatalf("open with wrong primary key %q must fail (AAD not row-bound)", wrongPK)
		}
	}
}
