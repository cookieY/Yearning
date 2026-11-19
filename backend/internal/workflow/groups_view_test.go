package workflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Member and grant collections of an empty permission group are required arrays in OpenAPI and must serialize as [] rather than null;
// regression test: empty groups previously returned null and crashed the /admin/permission-groups page.
func TestNewGroupViewEmptySetsMarshalAsArrays(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	view := newGroupView(PermissionGroupModel{
		ID: "group-1", Name: "empty", Enabled: true, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	})
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, want := range []string{`"member_user_ids":[]`, `"granted_flow_ids":[]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %s in %s", want, body)
		}
	}
	if strings.Contains(body, "null") {
		t.Fatalf("unexpected null in %s", body)
	}
}
