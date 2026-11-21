// chain.go manages ordered AI Provider candidate failover (provider_selection).
// Failover only occurs on known-safe boundary failures (auth/input rejection or schema invalidity)
// when contract digests match, and never exceeds MaxCallsPerStep calls.
package provider

import (
	"context"
	"errors"
	"fmt"
)

// Chain holds an ordered slice of validated Endpoints and their dialed Client adapters.
type Chain struct {
	Endpoints []Endpoint
	adapters  []Client
}

// NewChain validates endpoints and dials one Client adapter per configured Provider candidate.
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

// ValidateConfiguration probes each Provider in the chain without sending review payloads.
func (c *Chain) ValidateConfiguration(ctx context.Context) error {
	var first error
	for _, a := range c.adapters {
		if err := a.ValidateConfiguration(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Result records the accepted Response along with winning Provider metadata and per-call usage logs.
type Result struct {
	Response      *Response
	ProviderID    string
	ProviderName  string
	ModelName     string
	FallbackCount int
	Usage         Usage
	Calls         []CallRecord
}

// ReviewStructured executes a prebuilt Request across the Provider chain without per-endpoint rebuilding.
func (c *Chain) ReviewStructured(ctx context.Context, req Request) (*Result, error) {
	return c.ReviewStructuredGuarded(ctx, req, nil)
}

// ReviewStructuredGuarded validates a trusted local send boundary immediately before each actual attempt,
// including fallback. A guard failure never falls through to another Provider or records an unsent attempt.
func (c *Chain) ReviewStructuredGuarded(ctx context.Context, req Request, beforeSend func(context.Context) error) (*Result, error) {
	return c.reviewStructuredChain(ctx, false, func(context.Context, Endpoint) (Request, error) {
		return req, nil
	}, beforeSend)
}

// ReviewStructuredPerEndpoint rebuilds the Request for each attempted Endpoint right before
// dispatching, allowing safe fallback across mixed prompt-profile providers ("default" <-> "san")
// while enforcing identical OutputSchemaHash and fail-closed beforeSend verification.
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
		// Only known-safe rejections after receiving a response allow falling back; unknown-boundary
		// network or timeout errors abort the chain without replaying the request.
		if errors.Is(err, ErrRejected) || errors.Is(err, ErrSchemaInvalid) {
			continue
		}
		break
	}
	if lastErr == nil {
		lastErr = ErrNoProvider
	}
	// Preserve the last attempted Provider metadata and real call records for usage accounting.
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

// PlanHash folds the configured Provider chain into a deterministic plan identity digest.
func (c *Chain) PlanHash() string { return PlanHash(c.Endpoints) }

// chainError preserves the failed candidate attribution and incurred CallRecord slices across failover.
type chainError struct {
	wrapped    error
	providerID string
	modelName  string
	fallback   int
	calls      []CallRecord
}

func (e *chainError) Error() string { return e.wrapped.Error() }
func (e *chainError) Unwrap() error { return e.wrapped }

// ChainErrorInfo extracts the last attempted candidate identity from a wrapped chain error.
func ChainErrorInfo(err error) (providerID, modelName string, fallback int, ok bool) {
	var ce *chainError
	if errors.As(err, &ce) {
		return ce.providerID, ce.modelName, ce.fallback, true
	}
	return "", "", 0, false
}

// ChainErrorCalls returns all recorded CallRecords incurred prior to chain exhaustion.
func ChainErrorCalls(err error) []CallRecord {
	var ce *chainError
	if errors.As(err, &ce) {
		return ce.calls
	}
	return nil
}
