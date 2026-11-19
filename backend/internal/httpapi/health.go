package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ReadinessCheck is a named health check. Name uses only stable identifiers;
// failure details are written only to logs and never enter external responses.
type ReadinessCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// readinessCacheTTL bounds readiness check execution frequency (SEC-0044): /health/ready
// is anonymously reachable, and without caching high-frequency probes would amplify DB pings and keyring
// file reads per request. Repeated requests within TTL return cached conclusions; default k8s probe period (10s)
// is much larger than TTL, keeping traffic draining (503) and recovery semantics intact. Check failures never panic.
const readinessCacheTTL = 2 * time.Second

// Readiness aggregates all readiness checks.
// B1 wires metadata_postgresql and key_provider;
// exact_schema_version and database role self-checks are added in B2/B3 stages.
type Readiness struct {
	checks []ReadinessCheck
	logger *zap.Logger

	cacheMu    sync.Mutex
	cachedAt   time.Time
	cachedOK   bool
	cachedFail []string
}

func NewReadiness(logger *zap.Logger, checks ...ReadinessCheck) *Readiness {
	return &Readiness{checks: checks, logger: logger}
}

// HandleLive reflects only that the process event loop is responsive, without touching external dependencies.
// Implemented per api-conventions and kubernetes contracts: only liveness failure restarts the process.
func (r *Readiness) HandleLive(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "live"})
}

// HandleReady executes checks sequentially (with readinessCacheTTL result caching):
// returns 200 when all pass; returns 503 Problem Details if any fails, disclosing only
// the set of failed check names externally.
func (r *Readiness) HandleReady(c *gin.Context) {
	ok, failed := r.evaluate(c.Request.Context())
	if ok {
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
		return
	}
	WriteProblem(c, http.StatusServiceUnavailable,
		"failing checks: "+strings.Join(failed, ", "))
}

// evaluate runs checks and caches the conclusion for TTL (SEC-0044 hardening).
func (r *Readiness) evaluate(ctx context.Context) (bool, []string) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if !r.cachedAt.IsZero() && time.Since(r.cachedAt) < readinessCacheTTL {
		return r.cachedOK, r.cachedFail
	}
	failed := []string{}
	for _, check := range r.checks {
		if err := check.Check(ctx); err != nil {
			failed = append(failed, check.Name)
			if r.logger != nil {
				r.logger.Warn("readiness check failed",
					zap.String("check", check.Name),
					zap.Error(err),
				)
			}
		}
	}
	r.cachedAt = time.Now()
	r.cachedOK = len(failed) == 0
	r.cachedFail = failed
	return r.cachedOK, r.cachedFail
}
