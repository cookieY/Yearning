package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
)

func TestParseBaseURLTieredScheme(t *testing.T) {
	restore := addrguard.SetLoopbackBypassForTest(false)
	defer restore()

	allowedWithoutBypass := []string{
		"https://api.example.com/v1",
		"http://192.168.1.50:8000/v1",
		"http://10.20.30.40:11434/v1",
		"http://172.16.0.12:8000/v1",
	}
	for _, raw := range allowedWithoutBypass {
		if _, err := ParseBaseURL(raw); err != nil {
			t.Fatalf("expected allowed URL %q, got error: %v", raw, err)
		}
	}

	rejectedAlways := []string{
		"",
		"ftp://192.168.1.10/v1",
		"http://api.example.com/v1",
		"http://8.8.8.8:8000/v1",
		"http://169.254.169.254/latest/meta-data",
		"http://100.100.100.200/v1",
		"http://[fd00:ec2::254]/v1",
		"http://[fe80::1%eth0]:8000/v1",
		"http://127.0.0.1:8000/v1",
		"http://localhost:8000/v1",
		"http://[::1]:8000/v1",
	}
	for _, raw := range rejectedAlways {
		if u, err := ParseBaseURL(raw); err == nil {
			t.Fatalf("expected rejected URL %q when loopback disabled, got %v", raw, u)
		}
	}

	_ = addrguard.SetLoopbackBypassForTest(true)
	for _, raw := range []string{
		"http://127.0.0.1:8000/v1",
		"http://localhost:8000/v1",
		"http://[::1]:8000/v1",
	} {
		if _, err := ParseBaseURL(raw); err != nil {
			t.Fatalf("expected loopback HTTP allowed when bypass active for %q: %v", raw, err)
		}
	}
	for _, metadata := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://100.100.100.200/v1",
		"http://[fd00:ec2::254]/v1",
		"http://api.example.com/v1",
	} {
		if _, err := ParseBaseURL(metadata); err == nil {
			t.Fatalf("expected metadata/public HTTP rejected even with loopback bypass for %q", metadata)
		}
	}
}

func TestPrivacyContractHashesAndPlanHash(t *testing.T) {
	t.Parallel()

	if PrivacyContractHashDefault == PrivacyContractHashSAN {
		t.Fatal("default and san privacy contract hashes must be distinct")
	}
	if PrivacyContractHashForProfile(promptprofile.ProfileSAN) != PrivacyContractHashSAN {
		t.Fatal("san profile must resolve PrivacyContractHashSAN")
	}
	if PrivacyContractHashForProfile(promptprofile.ProfileDefault) != PrivacyContractHashDefault {
		t.Fatal("default profile must resolve PrivacyContractHashDefault")
	}

	baseEP := Endpoint{
		ProviderID: "p1", ModelName: "san", ProviderKind: "openai_compatible",
		PrivacyContractHash: PrivacyContractHashSAN, OutputSchemaHash: OutputSchemaHash,
	}
	explicitSAN := baseEP
	explicitSAN.Configuration = map[string]any{"system_prompt_profile": "san"}
	explicitDefault := baseEP
	explicitDefault.PrivacyContractHash = PrivacyContractHashDefault
	explicitDefault.Configuration = map[string]any{"system_prompt_profile": "default"}

	hAuto := PlanHash([]Endpoint{baseEP})
	hExplicitSAN := PlanHash([]Endpoint{explicitSAN})
	hExplicitDef := PlanHash([]Endpoint{explicitDefault})
	if hAuto == hExplicitSAN || hAuto == hExplicitDef || hExplicitSAN == hExplicitDef {
		t.Fatalf("PlanHash must distinguish auto vs explicit profile and profile changes: %s / %s / %s", hAuto, hExplicitSAN, hExplicitDef)
	}
}

func TestOpenAIAdapterSANProbeAndWireElision(t *testing.T) {
	t.Parallel()

	var captured []map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		captured = append(captured, payload)
		_ = json.NewEncoder(w).Encode(validChatBody())
	}))
	defer srv.Close()

	adapter, err := NewOpenAIAdapter(Endpoint{
		ProviderID:          "p-san",
		Name:                "san-local",
		ProviderKind:        "openai_compatible",
		BaseURL:             srv.URL,
		ModelName:           "san-4b",
		PrivacyContractHash: PrivacyContractHashSAN,
		OutputSchemaHash:    OutputSchemaHash,
		APIKey:              "k",
		Configuration:       map[string]any{ConfigKeyOutputMode: OutputModeJSONObject},
	}, &HTTPClient{Inner: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}

	if err := adapter.ValidateConfiguration(context.Background()); err != nil {
		t.Fatalf("SAN ValidateConfiguration failed: %v", err)
	}
	longLiteral := strings.Repeat("A", 300)
	req := Request{
		RequestSchemaVersion: 1,
		ReviewRunID:          ProbeRunID,
		EngineProfile:        "mysql/mysql",
		SystemPrompt:         promptprofile.BuildSANReviewProbePrompt(),
		RuleSnapshot:         []byte("rule-bytes"),
		OutputSchema:         []byte("schema-bytes"),
		AllowedToolDescriptors: []ToolDescriptor{{
			Name: "schema_metadata", Kind: "builtin_database_fact", Description: "long desc",
		}},
		AnonymizedStatementGroups: []AnonymizedGroup{{
			GroupID:                     "11111111-1111-4111-8111-111111111111",
			RepresentativeShape:         "INSERT",
			RepresentativeAnonymizedSQL: "INSERT INTO shop.orders VALUES ('" + longLiteral + "');",
			StatementCount:              1,
			StatementOrdinal:            1,
			StatementOrdinalRanges:      []StatementOrdinalRange{{Start: 1, End: 1}},
		}},
	}
	if _, err := adapter.ReviewStructured(context.Background(), req); err != nil {
		t.Fatalf("SAN ReviewStructured failed: %v", err)
	}
	if len(captured) != 2 {
		t.Fatalf("expected 2 requests (probe + review), got %d", len(captured))
	}
	for idx, body := range captured {
		msgs := body["messages"].([]any)
		sysContent := msgs[0].(map[string]any)["content"].(string)
		userContent := msgs[1].(map[string]any)["content"].(string)
		if sysContent != promptprofile.BuildSANReviewProbePrompt() {
			t.Fatalf("req %d system prompt mismatch:\n%s", idx, sysContent)
		}
		if strings.Contains(sysContent, "OUTPUT FORMAT") {
			t.Fatalf("req %d unexpectedly injected jsonObjectPromptSuffix on SAN", idx)
		}
		_, jsonPart, ok := strings.Cut(userContent, "\n")
		if !ok {
			t.Fatalf("req %d missing user JSON payload separator", idx)
		}
		var wire map[string]any
		if err := json.Unmarshal([]byte(jsonPart), &wire); err != nil {
			t.Fatalf("req %d invalid user JSON: %v", idx, err)
		}
		for _, elided := range []string{"system_prompt", "rule_snapshot", "output_schema"} {
			if _, present := wire[elided]; present {
				t.Fatalf("req %d wire JSON must elide %q", idx, elided)
			}
		}
		tools, ok := wire["allowed_tool_descriptors"].([]any)
		if !ok || len(tools) != 0 {
			t.Fatalf("req %d expected empty allowed_tool_descriptors, got %#v", idx, wire["allowed_tool_descriptors"])
		}
		if idx == 1 {
			groups := wire["anonymized_statement_groups"].([]any)
			sql := groups[0].(map[string]any)["representative_anonymized_sql"].(string)
			if !strings.Contains(sql, "...<truncated:300_chars>") {
				t.Fatalf("req %d oversized SQL literal not truncated: %q", idx, sql)
			}
		}
	}
}

func TestChainReviewStructuredPerEndpointMixedProfiles(t *testing.T) {
	t.Parallel()

	var seenPayloads []string
	chain, err := NewChain([]Endpoint{
		ep("p-san", PrivacyContractHashSAN, OutputSchemaHash, 1),
		ep("p-def", PrivacyContractHashDefault, OutputSchemaHash, 2),
	}, func(e Endpoint) (Client, error) {
		return perEndpointCaptureClient{
			ep: e,
			fn: func(req Request) (CallOutcome, error) {
				seenPayloads = append(seenPayloads, req.SystemPrompt)
				if e.ProviderID == "p-san" {
					return CallOutcome{}, ErrRejected
				}
				r := validResponse(req.ReviewRunID)
				return CallOutcome{Response: &r}, nil
			},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Static chain call must still reject mismatched PrivacyContractHash fallback.
	if _, err := chain.ReviewStructured(context.Background(), Request{ReviewRunID: "run-1"}); err == nil {
		t.Fatal("static ReviewStructured must block fallback when PrivacyContractHash differs")
	}

	seenPayloads = nil
	res, err := chain.ReviewStructuredPerEndpoint(context.Background(), func(_ context.Context, e Endpoint) (Request, error) {
		if e.PrivacyContractHash == PrivacyContractHashSAN {
			return Request{ReviewRunID: "run-1", SystemPrompt: "RAW_SECRET_TABLE"}, nil
		}
		return Request{ReviewRunID: "run-1", SystemPrompt: "ANON_TABLE_1"}, nil
	}, nil)
	if err != nil {
		t.Fatalf("per-endpoint mixed fallback failed: %v", err)
	}
	if res.ProviderID != "p-def" || len(seenPayloads) != 2 {
		t.Fatalf("unexpected fallback result=%+v payloads=%v", res, seenPayloads)
	}
	if seenPayloads[0] != "RAW_SECRET_TABLE" || seenPayloads[1] != "ANON_TABLE_1" {
		t.Fatalf("mixed fallback leaked wrong payload: %v", seenPayloads)
	}
}

type perEndpointCaptureClient struct {
	ep Endpoint
	fn func(Request) (CallOutcome, error)
}

func (c perEndpointCaptureClient) ValidateConfiguration(context.Context) error { return nil }
func (c perEndpointCaptureClient) NegotiateOutputMode(context.Context) (string, error) {
	return OutputModeJSONSchema, nil
}
func (c perEndpointCaptureClient) Health(context.Context) error { return nil }
func (c perEndpointCaptureClient) ReviewStructured(_ context.Context, req Request) (CallOutcome, error) {
	return c.fn(req)
}

func TestNewHTTPClient_BlocksSchemeDowngradeRedirect(t *testing.T) {
	restore := addrguard.SetLoopbackBypassForTest(true)
	defer restore()

	var httpTargetHits atomic.Int32
	plainTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpTargetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer plainTarget.Close()

	tlsOrigin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plainTarget.URL+"/v1/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer tlsOrigin.Close()

	hc := NewHTTPClient()
	tr := hc.Inner.Transport.(*http.Transport)
	tr.TLSClientConfig = tlsOrigin.Client().Transport.(*http.Transport).TLSClientConfig.Clone()

	adapter, err := NewOpenAIAdapter(Endpoint{
		ProviderID:          "p-redir",
		Name:                "redir-origin",
		ProviderKind:        "openai_compatible",
		BaseURL:             tlsOrigin.URL + "/v1",
		ModelName:           "gpt-4.1",
		PrivacyContractHash: PrivacyContractHashDefault,
		OutputSchemaHash:    OutputSchemaHash,
		APIKey:              "k",
	}, hc)
	if err != nil {
		t.Fatalf("NewOpenAIAdapter error: %v", err)
	}

	if err := adapter.Health(context.Background()); err == nil {
		t.Fatal("expected 307 redirect response to fail health check")
	}
	if got := httpTargetHits.Load(); got != 0 {
		t.Fatalf("expected NewHTTPClient to block scheme-downgrade redirect, but HTTP target received %d hits", got)
	}
}
