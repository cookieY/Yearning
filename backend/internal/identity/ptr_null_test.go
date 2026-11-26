package identity

import (
	"testing"
)

func TestLoginAuditEmptyMetadataStaysNilForSQLNull(t *testing.T) {
	svc := &Service{}
	uid := "u-1"
	in := svc.loginAudit("user", &uid, "alice", "auth.login", "succeeded", RequestMeta{
		RequestID: "",
		SourceIP:  "",
		UserAgent: "",
	})
	if in.RequestID != nil {
		t.Fatalf("expected empty RequestID to be nil (SQL NULL), got %q", *in.RequestID)
	}
	if in.SourceIP != nil {
		t.Fatalf("expected empty SourceIP to be nil (SQL NULL), got %q", *in.SourceIP)
	}
	if in.UserAgent != nil {
		t.Fatalf("expected empty UserAgent to be nil (SQL NULL), got %q", *in.UserAgent)
	}

	inFilled := svc.loginAudit("user", &uid, "alice", "auth.login", "succeeded", RequestMeta{
		RequestID: "req-1",
		SourceIP:  "127.0.0.1",
		UserAgent: "test-agent",
	})
	if inFilled.RequestID == nil || *inFilled.RequestID != "req-1" {
		t.Fatalf("expected RequestID=req-1, got %v", inFilled.RequestID)
	}
	if inFilled.SourceIP == nil || *inFilled.SourceIP != "127.0.0.1" {
		t.Fatalf("expected SourceIP=127.0.0.1, got %v", inFilled.SourceIP)
	}
	if inFilled.UserAgent == nil || *inFilled.UserAgent != "test-agent" {
		t.Fatalf("expected UserAgent=test-agent, got %v", inFilled.UserAgent)
	}
}
