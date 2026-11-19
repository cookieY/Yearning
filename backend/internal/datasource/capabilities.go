// capabilities.go datasource capability matrix (api/contracts/datasource-capabilities.json).
// Unsupported capabilities explicitly return Unsupported; capabilities are resolved from engine + mode + version constraints,
// and must never be claimed by name alone.
package datasource

import (
	"fmt"
	"strconv"
	"strings"
)

// Capability is a capability identifier.
const (
	CapConnectionTest = "connection_test"
	CapMetadata       = "metadata"
	CapAIReview       = "ai_review"
	CapQuery          = "query"
	CapDmlExecution   = "dml_execution"
	CapMultiDdl       = "multi_ddl_execution"
	CapGhOst          = "gh_ost"
)

// capabilityTarget is a structured copy of a contract targets entry.
type capabilityTarget struct {
	Target       string
	Engine       string
	Deployment   string
	Mode         string
	Constraint   string // "5.7.x" / "14.x" / "latest_stable_at_release"
	Capabilities []string
}

// CapabilityMatrix matches datasource-capabilities.json (10 targets).
var CapabilityMatrix = []capabilityTarget{
	{Target: "mysql-5.7", Engine: "mysql", Deployment: "native", Mode: "mysql", Constraint: "5.7.x",
		Capabilities: []string{CapConnectionTest, CapMetadata, CapAIReview, CapQuery, CapDmlExecution, CapMultiDdl, CapGhOst}},
	{Target: "mysql-8.0", Engine: "mysql", Deployment: "native", Mode: "mysql", Constraint: "8.0.x",
		Capabilities: []string{CapConnectionTest, CapMetadata, CapAIReview, CapQuery, CapDmlExecution, CapMultiDdl, CapGhOst}},
	{Target: "postgresql-14", Engine: "postgresql", Deployment: "native", Mode: "postgresql", Constraint: "14.x",
		Capabilities: []string{CapConnectionTest, CapMetadata, CapAIReview, CapQuery, CapDmlExecution, CapMultiDdl}},
	{Target: "postgresql-15", Engine: "postgresql", Deployment: "native", Mode: "postgresql", Constraint: "15.x",
		Capabilities: []string{CapConnectionTest, CapMetadata, CapAIReview, CapQuery, CapDmlExecution, CapMultiDdl}},
	{Target: "postgresql-16", Engine: "postgresql", Deployment: "native", Mode: "postgresql", Constraint: "16.x",
		Capabilities: []string{CapConnectionTest, CapMetadata, CapAIReview, CapQuery, CapDmlExecution, CapMultiDdl}},
	{Target: "postgresql-17", Engine: "postgresql", Deployment: "native", Mode: "postgresql", Constraint: "17.x",
		Capabilities: []string{CapConnectionTest, CapMetadata, CapAIReview, CapQuery, CapDmlExecution, CapMultiDdl}},
	{Target: "tidb-mysql", Engine: "tidb", Deployment: "cloud_or_self_hosted", Mode: "mysql", Constraint: "latest_stable_at_release",
		Capabilities: []string{CapConnectionTest, CapMetadata, CapAIReview, CapQuery, CapDmlExecution, CapMultiDdl}},
	{Target: "oceanbase-mysql", Engine: "oceanbase", Deployment: "cloud_or_self_hosted", Mode: "mysql", Constraint: "latest_stable_at_release",
		Capabilities: []string{CapConnectionTest, CapMetadata, CapAIReview, CapQuery, CapDmlExecution, CapMultiDdl}},
	{Target: "polardb-mysql", Engine: "polardb", Deployment: "cloud", Mode: "mysql", Constraint: "latest_stable_at_release",
		Capabilities: []string{CapConnectionTest, CapMetadata, CapAIReview, CapQuery, CapDmlExecution, CapMultiDdl}},
	{Target: "polardb-postgresql", Engine: "polardb", Deployment: "cloud", Mode: "postgresql", Constraint: "latest_stable_at_release",
		Capabilities: []string{CapConnectionTest, CapMetadata, CapAIReview, CapQuery, CapDmlExecution, CapMultiDdl}},
}

// requiredCapabilities is the set of required capability keys in the capability matrix.
var requiredCapabilities = []string{CapConnectionTest, CapMetadata, CapAIReview, CapQuery, CapDmlExecution, CapMultiDdl}

// ErrUnsupportedCapability indicates the capability matrix cannot resolve the current datasource.
var ErrUnsupportedCapability = fmt.Errorf("datasource: capability target unsupported")

// ConstraintMatches reports whether the detected version satisfies the constraint.
// "N.M.x" requires matching major and minor versions; "latest_stable_at_release" accepts any stable version of that engine.
func ConstraintMatches(constraint, detectedVersion string) bool {
	if constraint == "latest_stable_at_release" {
		return true
	}
	prefix := strings.TrimSuffix(constraint, ".x")
	if prefix == constraint {
		return constraint == detectedVersion
	}
	return strings.HasPrefix(normalizeVersion(detectedVersion), prefix+".")
}

func normalizeVersion(v string) string {
	// Strip common suffixes (such as "-log" or "+build") before comparing.
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	return v
}

// ResolveCapabilities resolves capabilities from engine/mode and the detected version.
// Returns ErrUnsupportedCapability (explicit Unsupported) when no target matches.
func ResolveCapabilities(engine, mode, detectedVersion string) (string, map[string]bool, error) {
	candidates := 0
	for _, t := range CapabilityMatrix {
		if t.Engine == engine && t.Mode == mode {
			candidates++
			if ConstraintMatches(t.Constraint, detectedVersion) {
				caps := map[string]bool{}
				for _, c := range requiredCapabilities {
					caps[c] = false
				}
				for _, c := range t.Capabilities {
					caps[c] = true
				}
				return t.Target, caps, nil
			}
		}
	}
	if candidates == 0 {
		return "", nil, ErrUnsupportedCapability
	}
	// Engine is supported but version is outside the frozen matrix: return nil capabilities and an error.
	// §18.76 supplement: Detail carries the detected version and frozen target so operators can diagnose it immediately
	// (e.g. mysql 8.4.11 is not in the mysql-5.7/8.0 frozen matrix).
	return "", nil, fmt.Errorf("%w: engine %s mode %s detected version %s is not in the frozen capability matrix",
		ErrUnsupportedCapability, engine, mode, detectedVersion)
}

// MajorMinor extracts the "N.M" major and minor version numbers.
func MajorMinor(version string) (int, int) {
	parts := strings.Split(normalizeVersion(version), ".")
	if len(parts) < 2 {
		return 0, 0
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	return major, minor
}
