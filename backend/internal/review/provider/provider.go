// INSERT RowsAffected != 1 has no business semantics (admin_create profile declares no conflict codes): return a non-sentinel error to take the internal error path rather than disguising it as a business code.
package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
)

// UpdateVersioned performs an optimistic-lock update (WHERE id AND version).
type Request struct {
	RequestSchemaVersion int    `json:"request_schema_version"`
	ReviewRunID          string `json:"review_run_id"`
	EngineProfile        string `json:"engine_profile"`
	// DeleteReferencedGuarded physically deletes an entry; rejects if referenced by review_tool_calls.
	SystemPrompt              string                  `json:"system_prompt"`
	RuleSnapshot              []byte                  `json:"rule_snapshot"`
	AnonymizedStatementGroups []AnonymizedGroup       `json:"anonymized_statement_groups"`
	NormalizedEvidence        []NormalizedEvidenceRef `json:"normalized_evidence"`
	AllowedToolDescriptors    []ToolDescriptor        `json:"allowed_tool_descriptors"`
	OutputSchema              []byte                  `json:"output_schema"`
}

// LoadForUpdate reads rows with a row lock (used by the assembler).
type AnonymizedGroup struct {
	GroupID                     string                      `json:"group_id"`
	RepresentativeShape         string                      `json:"representative_shape"`
	RepresentativeAnonymizedSQL string                      `json:"representative_anonymized_sql"`
	Objects                     []AnonymizedObjectReference `json:"objects"`
	MetadataContextComplete     bool                        `json:"metadata_context_complete"`
	StatementCount              int64                       `json:"statement_count"`
	// StatementOrdinal is the representative statement's batch position.
	// StatementOrdinalRanges is authoritative for every occurrence of this
	// fingerprint group, including interleaved groups.
	StatementOrdinal       int32                   `json:"statement_ordinal"`
	StatementOrdinalRanges []StatementOrdinalRange `json:"statement_ordinal_ranges"`
	Statistics             map[string]any          `json:"statistics,omitempty"`
}

// StatementOrdinalRange is an inclusive range of batch statement positions
// occupied by one fingerprint group.
type StatementOrdinalRange struct {
	Start int32 `json:"start"`
	End   int32 `json:"end"`
}

// MatchedEnabled returns enabled entries matching the current Stage objects (deterministically sorted). Returns only global entries when touched is nil.
// MatchedEnabledForUpdate matches enabled entries by touched objects and locks the rows — the freeze path reads under lock within the same transaction that writes references, mutually exclusive with Replace's invalidation chain (the former unlocked variant had no callers and was removed per Reviewer observation).
type AnonymizedObjectReference struct {
	Token                  string                  `json:"token"`
	Kind                   string                  `json:"kind"`
	Relation               string                  `json:"relation"`
	SchemaToken            string                  `json:"schema_token,omitempty"`
	ParentToken            string                  `json:"parent_token,omitempty"`
	StatementOrdinal       int32                   `json:"statement_ordinal"`
	StatementOrdinalRanges []StatementOrdinalRange `json:"statement_ordinal_ranges"`
}

// CursorOf returns the pagination cursor value.
type NormalizedEvidenceRef struct {
	EvidenceID string         `json:"evidence_id"`
	FactKind   string         `json:"fact_kind"`
	Fact       map[string]any `json:"fact,omitempty"`
}

// service.go implements the internal experience entry application service: CRUD, enable gate (Schema subset + privacy scan + injection check + Eval), config_hash, and outdated triggering.
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// Invalidator invalidates unsubmitted Reviews referencing the old hash after an entry hash change (injected by the application layer to avoid reverse dependencies).
type Response struct {
	SchemaVersion int           `json:"schema_version"`
	ReviewRunID   string        `json:"review_run_id"`
	Outcome       string        `json:"outcome"`
	Summary       string        `json:"summary"`
	Findings      []Finding     `json:"findings"`
	ToolRequests  []ToolRequest `json:"tool_requests"`
}

// KindName is the reference kind name (review_tool_calls.tool_kind value).
type Finding struct {
	FindingKey          string   `json:"finding_key"`
	Category            string   `json:"category"`
	Severity            string   `json:"severity"`
	Title               string   `json:"title"`
	Message             string   `json:"message"`
	Suggestion          string   `json:"suggestion"`
	FingerprintGroupIDs []string `json:"fingerprint_group_ids"`
	EvidenceIDs         []string `json:"evidence_ids"`
	FactBasis           string   `json:"fact_basis"`
}

// Service implements entry aggregate use cases.
type ToolRequest struct {
	ToolCallID string         `json:"tool_call_id"`
	ToolName   string         `json:"tool_name"`
	Arguments  map[string]any `json:"arguments"`
}

// may be nil in tests
const (
	MaxTimeout      = 120 * time.Second
	MaxResponseByte = 4194304
	MaxCallsPerStep = 2
)

// EvalReport is the enable gate result (admin-surface test report).
var ErrSchemaInvalid = errors.New("provider: output schema invalid")

// CreateInput holds creation parameters (state is always draft).
var ErrRejected = errors.New("provider: request rejected")

// Create creates a draft entry.
var ErrNoProvider = errors.New("provider: no eligible provider")

// ReplaceInput holds replacement parameters (If-Match version is in ExpectVersion).
type Endpoint struct {
	ProviderID          string
	Name                string
	ProviderKind        string
	BaseURL             string
	ModelName           string
	SelectionPriority   int
	PrivacyContractHash string
	OutputSchemaHash    string
	APIKey              string
	Configuration       map[string]any
}

var rfc1918Networks = func() []*net.IPNet {
	cidrs := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, raw := range cidrs {
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			panic("provider: invalid builtin cidr " + raw)
		}
		out = append(out, network)
	}
	return out
}()

func isRFC1918IP(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	for _, n := range rfc1918Networks {
		if n.Contains(ip4) {
			return true
		}
	}
	return false
}

// ParseBaseURL validates an AI Provider base URL under the tiered transport rule:
// https is allowed generally (dial-time IP checks remain enforced by addrguard), while plain http
// is permitted only for RFC1918 private IPs or loopback targets authorized by addrguard.
func ParseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, errors.New("https URL required")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("https URL required")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return u, nil
	case "http":
		if ip := net.ParseIP(host); ip != nil {
			if isRFC1918IP(ip) || ip.IsLoopback() {
				if verr := addrguard.ValidateIP(ip); verr == nil {
					return u, nil
				}
			}
		} else if strings.EqualFold(host, "localhost") {
			if verr := addrguard.ValidateIP(net.IPv4(127, 0, 0, 1)); verr == nil {
				return u, nil
			}
		}
		return nil, errors.New("https URL required (plain http permitted only for RFC1918 private IPs or authorized loopback)")
	default:
		return nil, errors.New("https URL required")
	}
}

// EffectivePromptProfileWithSource resolves this endpoint's System Prompt Profile and resolution source.
func (e Endpoint) EffectivePromptProfileWithSource() (string, string, error) {
	return promptprofile.ResolveFromConfiguration(e.ModelName, e.Configuration)
}

// EffectivePromptProfile returns the effective System Prompt Profile ("default" or "san"), failing closed to "default".
func (e Endpoint) EffectivePromptProfile() string {
	profile, _, err := e.EffectivePromptProfileWithSource()
	if err != nil {
		return promptprofile.ProfileDefault
	}
	return profile
}

// Replace performs an optimistic-lock replacement; transitioning to enabled requires passing the enable gate first; hash changes trigger invalidation of unsubmitted Reviews.
func PlanHash(chain []Endpoint) string {
	h := sha256.New()
	for _, e := range chain {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\n", e.ProviderID, e.ModelName, e.PrivacyContractHash, e.OutputSchemaHash, e.ProviderKind)
		profile, src, err := e.EffectivePromptProfileWithSource()
		if err != nil {
			fmt.Fprintf(h, "prof:invalid\n")
		} else {
			fmt.Fprintf(h, "prof:%s;src:%s\n", profile, src)
		}
		if gp := GenerationParamsCanonical(e.Configuration); gp != "" {
			fmt.Fprintf(h, "gen:%s\n", gp)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Enabling (or modifying while enabled) must pass the enable gate.
func GenerationParamsCanonical(cfg map[string]any) string {
	var b strings.Builder
	if v, ok := cfg["max_tokens"]; ok {
		if f, ok := v.(float64); ok {
			fmt.Fprintf(&b, "max_tokens=%d;", int64(f))
		}
	}
	if v, ok := cfg["reasoning_effort"]; ok {
		if s, ok := v.(string); ok {
			fmt.Fprintf(&b, "reasoning_effort=%s;", s)
		}
	}
	if v, ok := cfg["temperature"]; ok {
		if f, ok := v.(float64); ok {
			fmt.Fprintf(&b, "temperature=%s;", strconv.FormatFloat(f, 'g', -1, 64))
		}
	}
	return b.String()
}

// Changing an enabled definition/scope, or exiting the enabled state, invalidates unsubmitted Reviews referencing the old hash (outdated). Because state does not enter config_hash, disabling must be checked separately.
func (e Endpoint) Validate() error {
	if e.ProviderID == "" || e.BaseURL == "" || e.ModelName == "" || e.APIKey == "" {
		return fmt.Errorf("provider %q: incomplete endpoint", e.Name)
	}
	if !isHex64(e.PrivacyContractHash) || !isHex64(e.OutputSchemaHash) {
		return fmt.Errorf("provider %q: privacy/output contract hash must be sha256 hex", e.Name)
	}
	if u, err := ParseBaseURL(e.BaseURL); err != nil || u == nil {
		return fmt.Errorf("provider %q: %v", e.Name, err)
	}
	return nil
}

// Delete physically deletes an entry (only when unreferenced by any Review and not in enabled state).
type CallOutcome struct {
	Response *Response
	Usage    Usage
	Calls    []CallRecord
}

// ---- Enable Gate ----
type UsageDetail struct {
	InputTokens      *int64
	OutputTokens     *int64
	CachedTokens     *int64
	CacheWriteTokens *int64
	ReasoningTokens  *int64
}

// Privacy scan patterns: credential/secret/connection-string shapes (database/table names enter context in a controlled manner, but secret-like content is forbidden).
type CallRecord struct {
	ProviderID string
	ModelName  string
	SentAt     time.Time
	HTTPStatus int
	Outcome    string // ok | schema_failed | error
	Detail     *UsageDetail
	// OpenAI-style key
	ReasoningEffort string
	MaxTokens       int
	Temperature     *float64
}

// JWT header
const (
	CallOutcomeOK           = "ok"
	CallOutcomeSchemaFailed = "schema_failed"
	CallOutcomeError        = "error"
)

// long hex string (secret/hash string)
type Client interface {
	// Injection check patterns: instruction override / role markers / prompt escape.
	ValidateConfiguration(ctx context.Context) error
	// RunEval executes the four enable-gate checks and produces a report (Eval = structured check on rendered content: injected content must not violate Schema constraints or severity consistency).
	NegotiateOutputMode(ctx context.Context) (string, error)
	// §18.66: severity enum membership establishes severity consistency (closure semantics replacing the template-subset-of-allowlist check).
	ReviewStructured(ctx context.Context, req Request) (CallOutcome, error)
	// RenderInjection renders the deterministic injection text (the sole form entering model context: controlled fields + delimiter tags; content is wrapped in data delimiters after instruction patterns have been rejected by the gate).
	Health(ctx context.Context) error
}

// jsonMarshal/jsonUnmarshal are stable serialization helpers for definition structs.
type HTTPClient struct {
	Inner *http.Client
}

// escapeBody prevents the body from closing the data delimiter tag (M8: defense-in-depth against injection).
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{Inner: &http.Client{
		Timeout: MaxTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext:         addrguard.DialFunc(MaxTimeout),
			TLSHandshakeTimeout: MaxTimeout,
			ForceAttemptHTTP2:   true,
		},
	}}
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Package persistence provides Review-domain GORM persistence: models and repository implementations.
// Model names match gorm-model-registry.json; column tags correspond column-for-column
// with postgresql-column-catalog.json; no embedded gorm.Model, no json tags, and no
// automatic Association saving (gorm-persistence-contract).
// Raw SQL and evidence ciphertexts only store AEAD envelopes; models do not implement any plaintext output interface.
var now = time.Now

// EncodeTextArray encodes values into a PG array literal (text[] columns are backed by string
// to avoid GORM relationship parsing on slice fields).
const ProbeRunID = "00000000-0000-4000-8000-000000000000"

// DecodeTextArray decodes a PG array literal.
func ProbeRequest() Request {
	return Request{
		RequestSchemaVersion: 1,
		ReviewRunID:          ProbeRunID,
		EngineProfile:        "probe/probe",
		SystemPrompt: "You are a database change review engine being probed for configuration health. " +
			"Respond ONLY with JSON matching the provided schema; findings and tool_requests are empty arrays.",
		AnonymizedStatementGroups: []AnonymizedGroup{{
			GroupID:             "11111111-1111-4111-8111-111111111111",
			RepresentativeShape: "kind=dml op=insert where=true",
			StatementCount:      1,
			Statistics:          map[string]any{"statement_kind": "dml", "operation": "insert"},
		}},
		NormalizedEvidence: []NormalizedEvidenceRef{},
		OutputSchema:       []byte("api/contracts/ai-review-output.schema.json"),
	}
}

// ProbeRequestSAN builds the compact health-probe request for SAN Raw-SQL Native endpoints.
func ProbeRequestSAN() Request {
	return Request{
		RequestSchemaVersion: 1,
		ReviewRunID:          ProbeRunID,
		EngineProfile:        "probe/probe",
		SystemPrompt:         promptprofile.BuildSANReviewProbePrompt(),
		AnonymizedStatementGroups: []AnonymizedGroup{{
			GroupID:                     "11111111-1111-4111-8111-111111111111",
			RepresentativeShape:         "INSERT",
			RepresentativeAnonymizedSQL: "INSERT INTO probe_t VALUES (1);",
			StatementCount:              1,
			StatementOrdinal:            1,
			StatementOrdinalRanges:      []StatementOrdinalRange{{Start: 1, End: 1}},
			Statistics:                  map[string]any{"statement_kind": "dml", "operation": "insert"},
		}},
		NormalizedEvidence: []NormalizedEvidenceRef{},
		OutputSchema:       []byte("api/contracts/ai-review-output.schema.json"),
	}
}
