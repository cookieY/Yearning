// events.go builds domain event payloads (api/events/domain-event.schema.json). Internal event names follow repository-event-routing.json: change_draft.revised / change_draft.precheck_completed -> io.yearning.v4.change_draft.state_changed (emit_one); review_run.completed -> io.yearning.v4.review.completed | review.blocked (choose_one, discriminated by gate.passed).
package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
)

// Internal domain event names (repository-commands events field).
const privacyContractCanonical = `raw_SQL_to_provider: forbidden
literal_values_to_provider: forbidden
SQL_comments_to_provider: forbidden
raw_identifiers_to_provider: forbidden
identifier_representation: review_scoped_stable_anonymous_tokens
identifier_mapping_storage: local_AEAD_envelope_never_provider
fingerprint_group_strategy: send_representative_anonymized_shape_plus_count_and_bounded_structural_statistics
unreliable_parse_fingerprint_or_anonymization: fail_closed_review_incomplete_submission_forbidden
policy_text_identifier_scope: supported_objects_in_frozen_authorized_physical_catalogs_verified_at_each_send
`

const privacyContractCanonicalSAN = `prompt_profile: san
raw_SQL_to_provider: allowed_bounded_literals_256_chars
literal_values_to_provider: truncated_over_256_chars
SQL_comments_to_provider: allowed
raw_identifiers_to_provider: allowed_within_authorized_stage_catalog
identifier_representation: raw_sql_native_unmapped
fingerprint_group_strategy: send_representative_raw_sql_shape_plus_count_and_bounded_structural_statistics
unreliable_parse_fingerprint_or_anonymization: fail_closed_review_incomplete_submission_forbidden
policy_text_identifier_scope: supported_objects_in_frozen_authorized_physical_catalogs_verified_at_each_send
`

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// External event types (CloudEvents type).
var (
	PrivacyContractHashDefault = hashHex([]byte(privacyContractCanonical))
	PrivacyContractHashSAN     = hashHex([]byte(privacyContractCanonicalSAN))
	PrivacyContractHash        = PrivacyContractHashDefault
	OutputSchemaHash           = hashHex(mustJSON(compileOutputSchema()))
)

// PrivacyContractHashForProfile returns the authoritative platform privacy contract digest
// corresponding to the effective System Prompt Profile ("default" or "san").
func PrivacyContractHashForProfile(profile string) string {
	if profile == promptprofile.ProfileSAN {
		return PrivacyContractHashSAN
	}
	return PrivacyContractHashDefault
}
