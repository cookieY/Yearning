// StageResultData is the terminal summary of a single stage in a review event.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ReviewEventData is the review.completed/blocked payload (review-event-data.schema.json; state is restricted to ready|blocked|partial|failed).
type FakeAdapter struct {
	Endpoint Endpoint
	// GatePayload is the submission gate result shared between events and API.
	Respond func(req Request) (body []byte, status int, err error)
	client  *http.Client
	base    string
}

// DraftStateReason is the controlled reason_code value for state_changed payloads.
func NewLocalFakeAdapter(ep Endpoint, respond func(Request) ([]byte, int, error)) *FakeAdapter {
	return &FakeAdapter{Endpoint: ep, Respond: respond, client: http.DefaultClient}
}

// gate.go implements pure SubmissionGate evaluation: deterministically applies policy decoupled from AI conclusions. Any high/critical finding or incomplete outcome blocks submission; model confidence cannot override the gate (B8 takes over workflow rules).
func (f *FakeAdapter) ValidateConfiguration(ctx context.Context) error {
	_, err := f.ReviewStructured(ctx, ProbeRequest())
	return err
}

// GateReasonCode is the controlled vocabulary for gate.reason_codes.
func (f *FakeAdapter) Health(context.Context) error { return nil }

// GateInput summarizes the terminal state across all stage snapshots of a Run.
func (f *FakeAdapter) NegotiateOutputMode(ctx context.Context) (string, error) {
	if err := f.ValidateConfiguration(ctx); err != nil {
		return "", err
	}
	return OutputModeJSONSchema, nil
}

// none indicates no findings
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
