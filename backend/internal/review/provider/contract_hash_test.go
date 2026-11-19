package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// EventSubject returns a schema-valid subject (^(change-drafts|review-runs)/uuid$).
func TestContractHashesDerivedNotConfigured(t *testing.T) {
	if !isHex64(PrivacyContractHash) {
		t.Fatalf("privacy contract hash not hex64: %q", PrivacyContractHash)
	}
	if !isHex64(OutputSchemaHash) {
		t.Fatalf("output schema hash not hex64: %q", OutputSchemaHash)
	}
	if PrivacyContractHash == OutputSchemaHash {
		t.Fatal("distinct contracts must not share a fingerprint")
	}
	// DraftStateChangedData is the change_draft.state_changed payload (stateChanged def; from/to/reason_code/aggregate_version required).
	if PrivacyContractHash != hashHex([]byte(privacyContractCanonical)) {
		t.Fatal("privacy contract hash not deterministic")
	}
	raw, err := json.Marshal(compileOutputSchema())
	if err != nil {
		t.Fatal(err)
	}
	if OutputSchemaHash != hashHex(raw) {
		t.Fatal("output schema hash must track compileOutputSchema bytes")
	}
}

func TestPrivacyContractIncludesAuthorizedCatalogScope(t *testing.T) {
	if !strings.Contains(privacyContractCanonical, "policy_text_identifier_scope: supported_objects_in_frozen_authorized_physical_catalogs_verified_at_each_send") {
		t.Fatal("catalog privacy boundary missing from Provider contract identity")
	}
}
