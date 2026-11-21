// fake.go provides FakeAdapter, an in-memory Provider Port implementation for unit and integration
// tests that still enforces response size limits and strict ValidateOutput schema verification.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// FakeAdapter dispatches review requests to an in-memory callback rather than an HTTP upstream.
type FakeAdapter struct {
	Endpoint Endpoint
	Respond  func(req Request) (body []byte, status int, err error)
	client   *http.Client
	base     string
}

// NewLocalFakeAdapter constructs a FakeAdapter bound to ep and its response callback.
func NewLocalFakeAdapter(ep Endpoint, respond func(Request) ([]byte, int, error)) *FakeAdapter {
	return &FakeAdapter{Endpoint: ep, Respond: respond, client: http.DefaultClient}
}

// ValidateConfiguration runs a synthetic probe against the callback to verify structured output handling.
func (f *FakeAdapter) ValidateConfiguration(ctx context.Context) error {
	_, err := f.ReviewStructured(ctx, ProbeRequest())
	return err
}

// Health always reports healthy for an in-process FakeAdapter.
func (f *FakeAdapter) Health(context.Context) error { return nil }

// NegotiateOutputMode verifies configuration via ValidateConfiguration and returns json_schema mode.
func (f *FakeAdapter) NegotiateOutputMode(ctx context.Context) (string, error) {
	if err := f.ValidateConfiguration(ctx); err != nil {
		return "", err
	}
	return OutputModeJSONSchema, nil
}

// ReviewStructured executes the fake callback and validates the returned payload against Response schema rules.
func (f *FakeAdapter) ReviewStructured(ctx context.Context, req Request) (CallOutcome, error) {
	if f.Respond == nil {
		return CallOutcome{}, fmt.Errorf("fake provider: no responder installed")
	}
	body, status, err := f.Respond(req)
	if err != nil {
		return CallOutcome{}, err
	}
	if len(body) > MaxResponseByte {
		return CallOutcome{}, fmt.Errorf("fake provider: response exceeds 4MiB")
	}
	if status == http.StatusUnauthorized {
		return CallOutcome{}, fmt.Errorf("%w: status 401", ErrRejected)
	}
	if status < 200 || status >= 300 {
		return CallOutcome{}, fmt.Errorf("fake provider: status %d", status)
	}
	var out Response
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return CallOutcome{}, fmt.Errorf("%w: %v", ErrSchemaInvalid, err)
	}
	if err := ValidateOutput(&out, req.ReviewRunID); err != nil {
		return CallOutcome{}, fmt.Errorf("%w: %v", ErrSchemaInvalid, err)
	}
	return CallOutcome{Response: &out, Usage: Usage{InputTokens: 10, OutputTokens: 5}}, nil
}
