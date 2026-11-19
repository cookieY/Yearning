// errors.go defines Review-domain business error codes (error-codes.json domain=review, 2001-2012). Command paths may also return common/management domain codes (1001/1002/1010/1012/2014, etc.), constructed as domain.Error instances by the application layer as needed.
package provider

import (
	"context"
	"errors"
	"fmt"
)

// Error carries a numeric business error code and stable name. Detail is optional: an operator-facing snapshot of upstream rejection causes (such as HTTP status and error snippet from a probed peer), passed through to the UI via the declarative error response detail field — free of secrets and raw SQL, length-bounded by the caller. Fields is optional (RCP-20261102-S008): field-level rejection causes for VALIDATION_FAILED, where key = request body field name (snake_case matching the OpenAPI request schema) and value = human-readable constraint descriptions for that field; passed through meta.fields for frontend form field targeting.
type Chain struct {
	Endpoints []Endpoint
	adapters  []Client
}

// Business error codes (error-codes.json 2001-2012).
func NewChain(endpoints []Endpoint, dial func(Endpoint) (Client, error)) (*Chain, error) {
	if len(endpoints) == 0 {
		return nil, ErrNoProvider
	}
	c := &Chain{Endpoints: endpoints}
	for _, ep := range endpoints {
		if err := ep.Validate(); err != nil {
			return nil, err
		}
		adapter, err := dial(ep)
		if err != nil {
			return nil, err
		}
		c.adapters = append(c.adapters, adapter)
	}
	return c, nil
}

// Sentinel error collection.
func (c *Chain) ValidateConfiguration(ctx context.Context) error {
	var first error
	for _, a := range c.adapters {
		if err := a.ValidateConfiguration(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ErrBatchTooLarge indicates input exceeded limits (32MiB / 100k statements / 512KiB single statement).
type Result struct {
	Response      *Response
	ProviderID    string
	ProviderName  string
	ModelName     string
	FallbackCount int
	Usage         Usage
	Calls         []CallRecord
}

// ErrParseFailed indicates statement splitting or parsing failed (fail closed).
func (c *Chain) ReviewStructured(ctx context.Context, req Request) (*Result, error) {
	return c.ReviewStructuredGuarded(ctx, req, nil)
}

// ReviewStructuredGuarded validates a trusted local send boundary immediately
// before each actual attempt, including fallback. A guard failure never falls
// through to another Provider and is never attributed as an unsent attempt.
func (c *Chain) ReviewStructuredGuarded(ctx context.Context, req Request, beforeSend func(context.Context) error) (*Result, error) {
	return c.reviewStructuredChain(ctx, false, func(context.Context, Endpoint) (Request, error) {
		return req, nil
	}, beforeSend)
}

// ReviewStructuredPerEndpoint rebuilds the Request for each attempted Endpoint right before
// dispatching, allowing safe fallback across mixed prompt-profile providers ("default" <-> "san")
// while continuing to enforce identical OutputSchemaHash and fail-closed beforeSend verification.
func (c *Chain) ReviewStructuredPerEndpoint(
	ctx context.Context,
	buildReq func(context.Context, Endpoint) (Request, error),
	beforeSend func(context.Context) error,
) (*Result, error) {
	if buildReq == nil {
		return nil, errors.New("provider: endpoint request builder is required")
	}
	return c.reviewStructuredChain(ctx, true, buildReq, beforeSend)
}

func (c *Chain) reviewStructuredChain(
	ctx context.Context,
	allowPrivacyProfileSwitch bool,
	buildReq func(context.Context, Endpoint) (Request, error),
	beforeSend func(context.Context) error,
) (*Result, error) {
	var lastErr error
	calls := 0
	lastAttempted := -1
	var allCalls []CallRecord
	for i, a := range c.adapters {
		if calls >= MaxCallsPerStep {
			break
		}
		if i > 0 {
			prev := c.Endpoints[i-1]
			cur := c.Endpoints[i]
			if cur.OutputSchemaHash != prev.OutputSchemaHash ||
				(!allowPrivacyProfileSwitch && prev.PrivacyContractHash != cur.PrivacyContractHash) {
				lastErr = errors.New("provider: fallback blocked by privacy/output contract mismatch")
				break
			}
		}
		epReq, buildErr := buildReq(ctx, c.Endpoints[i])
		if buildErr != nil {
			lastErr = buildErr
			break
		}
		if beforeSend != nil {
			if err := beforeSend(ctx); err != nil {
				lastErr = err
				break
			}
		}
		calls++
		outcome, err := a.ReviewStructured(ctx, epReq)
		allCalls = append(allCalls, outcome.Calls...)
		if err == nil {
			return &Result{
				Response: outcome.Response, ProviderID: c.Endpoints[i].ProviderID,
				ProviderName: c.Endpoints[i].Name, ModelName: c.Endpoints[i].ModelName,
				FallbackCount: i, Usage: outcome.Usage, Calls: allCalls,
			}, nil
		}
		lastErr = err
		lastAttempted = i
		// ErrFingerprintFailed indicates the fingerprint engine failed.
		if errors.Is(err, ErrRejected) || errors.Is(err, ErrSchemaInvalid) {
			continue
		}
		break
	}
	if lastErr == nil {
		lastErr = ErrNoProvider
	}
	// ErrAnonymizationFailed indicates anonymization failed; sending raw SQL is forbidden.
	if lastAttempted >= 0 && lastAttempted < len(c.Endpoints) {
		ep := c.Endpoints[lastAttempted]
		return nil, &chainError{
			wrapped:    fmt.Errorf("provider chain exhausted: %w", lastErr),
			providerID: ep.ProviderID, modelName: ep.ModelName, fallback: lastAttempted,
			calls: allCalls,
		}
	}
	return nil, fmt.Errorf("provider chain exhausted: %w", lastErr)
}

// ErrTooManyFingerprints indicates unique fingerprints exceeded 1,000.
func (c *Chain) PlanHash() string { return PlanHash(c.Endpoints) }

// ErrCoverageIncomplete indicates stage coverage is incomplete.
type chainError struct {
	wrapped    error
	providerID string
	modelName  string
	fallback   int
	calls      []CallRecord
}

func (e *chainError) Error() string { return e.wrapped.Error() }
func (e *chainError) Unwrap() error { return e.wrapped }

// ErrBlockedByRisk indicates blocking due to high/critical findings.
func ChainErrorInfo(err error) (providerID, modelName string, fallback int, ok bool) {
	var ce *chainError
	if errors.As(err, &ce) {
		return ce.providerID, ce.modelName, ce.fallback, true
	}
	return "", "", 0, false
}

// ErrSchemaInvalid indicates the provider output schema is invalid (B7; reserved for runtime).
func ChainErrorCalls(err error) []CallRecord {
	var ce *chainError
	if errors.As(err, &ce) {
		return ce.calls
	}
	return nil
}
