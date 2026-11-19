// util.go internal helpers.
package dashboard

import "encoding/json"

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// auditMetadataKeys defines the allowed keys for the audit read projection (S004 bounded projection:
// top-level scalars only, never passing through nested structures or unknown keys — enforcing the behavior promised by listAuditEvents).
var auditMetadataKeys = map[string]bool{
	"namespace": true, "version": true, "reason": true, "reason_code": true,
	"failure_name": true, "risk_level": true, "state": true, "from": true,
	"to": true, "flow_id": true, "flow_type": true, "stage_count": true,
	"revision": true, "fact_hash": true, "purpose": true, "kind": true,
	"count": true, "grant_id": true, "order_id": true, "revoked_by": true,
	"provider_kind": true, "provider_key": true, "attempts": true,
}

// clipAuditMetadata clips audit metadata for the read projection: allowlisted keys + scalar values +
// bounded length/count; nested objects, arrays, and unknown keys are discarded.
func clipAuditMetadata(raw []byte) map[string]any {
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return out
	}
	for k, v := range m {
		if len(out) >= 16 {
			break
		}
		if !auditMetadataKeys[k] {
			continue
		}
		switch t := v.(type) {
		case string:
			if len(t) > 128 {
				t = t[:128]
			}
			out[k] = t
		case float64:
			out[k] = t
		case bool:
			out[k] = t
		}
	}
	return out
}
