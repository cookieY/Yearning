// Package provider defines the structured AI model Provider Port, request/response schemas,
// endpoint validation, SSRF-guarded HTTP client, and per-call usage types.
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

// Request is the ReviewStructured input payload sent to an AI Provider adapter.
type Request struct {
	RequestSchemaVersion      int                     `json:"request_schema_version"`
	ReviewRunID               string                  `json:"review_run_id"`
	EngineProfile             string                  `json:"engine_profile"`
	SystemPrompt              string                  `json:"system_prompt"`
	RuleSnapshot              []byte                  `json:"rule_snapshot"`
	AnonymizedStatementGroups []AnonymizedGroup       `json:"anonymized_statement_groups"`
	NormalizedEvidence        []NormalizedEvidenceRef `json:"normalized_evidence"`
	AllowedToolDescriptors    []ToolDescriptor        `json:"allowed_tool_descriptors"`
	OutputSchema              []byte                  `json:"output_schema"`
}

// AnonymizedGroup summarizes one fingerprint group of SQL statements for model review.
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

// AnonymizedObjectReference describes an anonymous object relationship referenced by a statement group.
type AnonymizedObjectReference struct {
	Token                  string                  `json:"token"`
	Kind                   string                  `json:"kind"`
	Relation               string                  `json:"relation"`
	SchemaToken            string                  `json:"schema_token,omitempty"`
	ParentToken            string                  `json:"parent_token,omitempty"`
	StatementOrdinal       int32                   `json:"statement_ordinal"`
	StatementOrdinalRanges []StatementOrdinalRange `json:"statement_ordinal_ranges"`
	OrdinalRangesTruncated bool                    `json:"-"`
}

// NormalizedEvidenceRef carries a bounded, normalized tool evidence fact into the model context.
type NormalizedEvidenceRef struct {
	EvidenceID string         `json:"evidence_id"`
	FactKind   string         `json:"fact_kind"`
	Fact       map[string]any `json:"fact,omitempty"`
}

// ToolDescriptor declares a built-in fact tool and its parameter JSON schema to the model.
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

// Response is the validated structured output returned by an AI Provider adapter.
type Response struct {
	SchemaVersion int           `json:"schema_version"`
	ReviewRunID   string        `json:"review_run_id"`
	Outcome       string        `json:"outcome"`
	Summary       string        `json:"summary"`
	Findings      []Finding     `json:"findings"`
	ToolRequests  []ToolRequest `json:"tool_requests"`
}

// Finding is a structured review finding emitted within Response.
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

// ToolRequest is a validated follow-up fact tool request emitted by the model.
type ToolRequest struct {
	ToolCallID string         `json:"tool_call_id"`
	ToolName   string         `json:"tool_name"`
	Arguments  map[string]any `json:"arguments"`
}

// Provider call safety bounds enforced across adapters and failovers.
const (
	MaxTimeout      = 120 * time.Second
	MaxResponseByte = 4194304
	MaxCallsPerStep = 2
)

// Provider error sentinels used for failover eligibility checks.
var ErrSchemaInvalid = errors.New("provider: output schema invalid")
var ErrRejected = errors.New("provider: request rejected")
var ErrNoProvider = errors.New("provider: no eligible provider")

// Endpoint holds decrypted runtime configuration for a single AI Provider candidate.
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
		port := 80
		if p := u.Port(); p != "" {
			parsed, perr := strconv.Atoi(p)
			if perr != nil || parsed < 1 || parsed > 65535 {
				return nil, errors.New("https URL required (invalid port)")
			}
			port = parsed
		}
		if ip := net.ParseIP(host); ip != nil {
			if isRFC1918IP(ip) || ip.IsLoopback() {
				if verr := addrguard.ValidateIPFor(addrguard.PurposeAIProvider, ip); verr == nil {
					if serr := addrguard.CheckSelfTargetFor(addrguard.PurposeAIProvider, ip, port, ""); serr == nil {
						return u, nil
					}
				}
			}
		} else if strings.EqualFold(host, "localhost") {
			loop := net.IPv4(127, 0, 0, 1)
			if verr := addrguard.ValidateIPFor(addrguard.PurposeAIProvider, loop); verr == nil {
				if serr := addrguard.CheckSelfTargetFor(addrguard.PurposeAIProvider, loop, port, ""); serr == nil {
					return u, nil
				}
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

// PlanHash hashes the ordered provider chain, prompt profiles, contract digests, and generation parameters.
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

// GenerationParamsCanonical serializes configured max_tokens, reasoning_effort, and temperature in fixed key order.
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

// Validate checks that required Endpoint identifiers, contract hashes, and base URL are valid.
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

// CallOutcome aggregates the decoded Response, token totals, and real HTTP CallRecords for one call.
type CallOutcome struct {
	Response *Response
	Usage    Usage
	Calls    []CallRecord
}

// UsageDetail captures nullable tri-state token counts reported by upstream providers.
type UsageDetail struct {
	InputTokens      *int64
	OutputTokens     *int64
	CachedTokens     *int64
	CacheWriteTokens *int64
	ReasoningTokens  *int64
}

// CallRecord captures per-HTTP-attempt metadata, generation parameters, outcome, and usage detail.
type CallRecord struct {
	ProviderID      string
	ModelName       string
	SentAt          time.Time
	HTTPStatus      int
	Outcome         string // ok | schema_failed | error
	Detail          *UsageDetail
	ReasoningEffort string
	MaxTokens       int
	Temperature     *float64
}

// Controlled CallRecord outcome values.
const (
	CallOutcomeOK           = "ok"
	CallOutcomeSchemaFailed = "schema_failed"
	CallOutcomeError        = "error"
)

// Client defines the AI Provider adapter interface.
type Client interface {
	ValidateConfiguration(ctx context.Context) error
	NegotiateOutputMode(ctx context.Context) (string, error)
	ReviewStructured(ctx context.Context, req Request) (CallOutcome, error)
	Health(ctx context.Context) error
}

// HTTPClient wraps a redirect-disabled, addrguard-dialing HTTP client bounded by MaxTimeout.
type HTTPClient struct {
	Inner *http.Client
}

// NewHTTPClient constructs an SSRF-guarded HTTPClient for AI Provider requests.
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{Inner: &http.Client{
		Timeout:       MaxTimeout,
		CheckRedirect: addrguard.RedirectPolicy(0, false),
		Transport: &http.Transport{
			DialContext:         addrguard.DialFuncFor(addrguard.PurposeAIProvider, MaxTimeout),
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

var now = time.Now

// ProbeRunID is the deterministic synthetic Run ID used for provider health probes.
const ProbeRunID = "00000000-0000-4000-8000-000000000000"

// ProbeRequest builds a minimal anonymized probe request to validate structured output support.
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
