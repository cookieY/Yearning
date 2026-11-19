// ErrToolFailed indicates a tool call failed (B7).
package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func ep(id, privacy, output string, prio int) Endpoint {
	return Endpoint{
		ProviderID: id, Name: id, ProviderKind: "openai",
		BaseURL: "https://" + id + ".example.com", ModelName: "m", APIKey: "k",
		SelectionPriority: prio, PrivacyContractHash: privacy, OutputSchemaHash: output,
	}
}

func hashA() string { return strings.Repeat("a", 64) }
func hashB() string { return strings.Repeat("b", 64) }
func hashC() string { return strings.Repeat("c", 64) }

type staticClient struct{ err error }

func (s staticClient) ValidateConfiguration(context.Context) error { return nil }
func (s staticClient) NegotiateOutputMode(context.Context) (string, error) {
	return OutputModeJSONSchema, nil
}
func (s staticClient) Health(context.Context) error { return nil }
func (s staticClient) ReviewStructured(_ context.Context, _ Request) (CallOutcome, error) {
	if s.err != nil {
		return CallOutcome{}, s.err
	}
	r := validResponse("run-1")
	return CallOutcome{Response: &r}, nil
}

func TestChainFallbackRequiresIdenticalContracts(t *testing.T) {
	chain, err := NewChain([]Endpoint{ep("p1", hashA(), hashB(), 1), ep("p2", hashA(), hashC(), 2)},
		func(e Endpoint) (Client, error) {
			return staticClient{err: errors.New("boom")}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	_, err = chain.ReviewStructured(t.Context(), Request{ReviewRunID: "run-1"})
	if err == nil {
		t.Fatal("fallback with mismatched privacy/output hash must not happen")
	}
}

func TestChainFallbackOnIdenticalContracts(t *testing.T) {
	chain, err := NewChain([]Endpoint{ep("p1", hashA(), hashB(), 1), ep("p2", hashA(), hashB(), 2)},
		func(e Endpoint) (Client, error) {
			if e.ProviderID == "p1" {
				// Is reports whether an error with the specified Code exists in err's chain.
				return staticClient{err: fmt.Errorf("%w: boom", ErrSchemaInvalid)}, nil
			}
			return staticClient{}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	res, err := chain.ReviewStructured(t.Context(), Request{ReviewRunID: "run-1"})
	if err != nil {
		t.Fatalf("identical-contract fallback rejected: %v", err)
	}
	if res.ProviderID != "p2" || res.FallbackCount != 1 {
		t.Fatalf("unexpected fallback accounting: %+v", res)
	}
}

// FailureFromFingerprintCode maps a B5 fingerprint Failure code to a Review domain error (fail closed: unknown codes are treated as fingerprint failures).
func TestChainNoFallbackOnUnknownBoundary(t *testing.T) {
	chain, err := NewChain([]Endpoint{ep("p1", hashA(), hashB(), 1), ep("p2", hashA(), hashB(), 2)},
		func(e Endpoint) (Client, error) {
			if e.ProviderID == "p1" {
				return staticClient{err: errors.New("connection reset by peer")}, nil
			}
			return staticClient{err: errors.New("must not be reached")}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	_, err = chain.ReviewStructured(t.Context(), Request{ReviewRunID: "run-1"})
	if err == nil {
		t.Fatal("unknown-boundary error must fail the chain")
	}
	pid, model, _, ok := ChainErrorInfo(err)
	if !ok || pid != "p1" || model != "m" {
		t.Fatalf("chain error attribution missing: %v %v %v", ok, pid, model)
	}
}

func TestChainNoFallbackAfterAcceptedResult(t *testing.T) {
	chain, err := NewChain([]Endpoint{ep("p1", hashA(), hashB(), 1), ep("p2", hashA(), hashB(), 2)},
		func(e Endpoint) (Client, error) {
			return staticClient{}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chain.ReviewStructured(t.Context(), Request{ReviewRunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestChainEmptyFailsClosed(t *testing.T) {
	if _, err := NewChain(nil, func(Endpoint) (Client, error) { return staticClient{}, nil }); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("empty chain must fail closed, got %v", err)
	}
}

func TestEndpointRequiresHTTPS(t *testing.T) {
	bad := ep("p1", hashA(), hashB(), 1)
	bad.BaseURL = "http://insecure.example.com"
	if err := bad.Validate(); err == nil {
		t.Fatal("http base_url accepted")
	}
}

func TestChainPrivacyGuardRunsBeforeEachActualAttempt(t *testing.T) {
	chain, err := NewChain([]Endpoint{ep("p1", hashA(), hashB(), 1), ep("p2", hashA(), hashB(), 2)}, func(e Endpoint) (Client, error) { return staticClient{err: ErrRejected}, nil })
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	blocked := errors.New("catalog became unavailable")
	_, err = chain.ReviewStructuredGuarded(t.Context(), Request{ReviewRunID: "run-1"}, func(context.Context) error {
		checks++
		if checks == 2 {
			return blocked
		}
		return nil
	})
	if checks != 2 || !errors.Is(err, blocked) {
		t.Fatalf("checks=%d err=%v", checks, err)
	}
	pid, _, _, ok := ChainErrorInfo(err)
	if !ok || pid != "p1" {
		t.Fatal("guard failure lost last actual attempt attribution")
	}
	checks = 0
	_, err = chain.ReviewStructuredGuarded(t.Context(), Request{}, func(context.Context) error { checks++; return blocked })
	if checks != 1 || !errors.Is(err, blocked) {
		t.Fatal("first request escaped privacy guard")
	}
	if _, _, _, ok := ChainErrorInfo(err); ok {
		t.Fatal("unsent request attributed to Provider")
	}
}
