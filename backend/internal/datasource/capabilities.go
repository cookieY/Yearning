// capabilities.go datasource capability matrix (api/contracts/datasource-capabilities.json).
// Unsupported capabilities explicitly return Unsupported; capabilities are resolved from engine + mode + version constraints,
// and must never be claimed by name alone.
package datasource

import (
	"encoding/json"
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
	Constraint   string // ">=5.7" / ">=14" / "latest_stable_at_release"
	Capabilities []string
}

// CapabilityMatrix matches datasource-capabilities.json (6 targets).
var CapabilityMatrix = []capabilityTarget{
	{Target: "mysql", Engine: "mysql", Deployment: "native", Mode: "mysql", Constraint: ">=5.7",
		Capabilities: []string{CapConnectionTest, CapMetadata, CapAIReview, CapQuery, CapDmlExecution, CapMultiDdl, CapGhOst}},
	{Target: "postgresql", Engine: "postgresql", Deployment: "native", Mode: "postgresql", Constraint: ">=14",
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
// ">=N" / ">=N.M" requires detected major.minor >= floor; "N.M.x" requires matching major.minor;
// "latest_stable_at_release" accepts any stable version of that engine.
func ConstraintMatches(constraint, detectedVersion string) bool {
	if constraint == "latest_stable_at_release" {
		return true
	}
	if strings.HasPrefix(constraint, ">=") {
		minMajor, minMinor, minOK := parseMajorMinor(strings.TrimPrefix(constraint, ">="))
		detMajor, detMinor, detOK := parseMajorMinor(detectedVersion)
		if !minOK || !detOK {
			return false
		}
		return detMajor > minMajor || (detMajor == minMajor && detMinor >= minMinor)
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

func parseMajorMinor(version string) (int, int, bool) {
	norm := strings.TrimSpace(normalizeVersion(strings.TrimSpace(version)))
	if norm == "" {
		return 0, 0, false
	}
	parts := strings.Split(norm, ".")
	if parts[0] == "" {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return 0, 0, false
	}
	minor := 0
	for i, seg := range parts[1:] {
		if seg == "" {
			return 0, 0, false
		}
		v, err := strconv.Atoi(seg)
		if err != nil || v < 0 {
			return 0, 0, false
		}
		if i == 0 {
			minor = v
		}
	}
	return major, minor, true
}

// DetectEngineFlavor identifies the runtime database flavor and verification tier from engine,
// compatibility mode, and raw server version() banner without relying on user-declared labels alone.
func DetectEngineFlavor(engine, mode, rawBanner string) (flavor, tier, notice string) {
	eng := strings.ToLower(strings.TrimSpace(engine))
	md := strings.ToLower(strings.TrimSpace(mode))
	banner := strings.ToLower(strings.TrimSpace(rawBanner))

	if md == "postgresql" {
		if eng == "polardb" || strings.Contains(banner, "polardb") {
			return "polardb_postgresql", "class_b", "PolarDB PostgreSQL compatibility flavor detected (Class B verification tier)"
		}
		return "postgresql_native", "class_a", ""
	}

	switch {
	case strings.Contains(banner, "mariadb"):
		return "mariadb", "class_b", "MariaDB compatibility banner detected under MySQL mode; gh-ost disabled (Class B verification tier)"
	case eng == "tidb" || strings.Contains(banner, "tidb"):
		return "tidb", "class_b", "TiDB compatibility flavor detected; gh-ost disabled (Class B verification tier)"
	case eng == "oceanbase" || strings.Contains(banner, "oceanbase"):
		return "oceanbase", "class_b", "OceanBase compatibility flavor detected; gh-ost disabled (Class B verification tier)"
	case eng == "polardb" || strings.Contains(banner, "polardb"):
		return "polardb_mysql", "class_b", "PolarDB MySQL compatibility flavor detected; gh-ost disabled (Class B verification tier)"
	}

	if banner != "" {
		if major, minor, ok := parseMajorMinor(banner); ok {
			if major == 5 && minor == 7 {
				return "mysql_native", "class_b", "MySQL 5.7 Class B verification tier (non-CTE catalog pruning active)"
			}
			if major > 8 || (major == 8 && minor >= 4) {
				return "mysql_native", "class_b", "MySQL 8.4+ Class B verification tier"
			}
		}
	}
	return "mysql_native", "class_a", ""
}

// ResolveCapabilitiesWithProbe resolves capabilities from engine, mode, detected version, and raw server banner,
// failing closed on gh_ost whenever runtime banner detection identifies a non-native MySQL flavor.
func ResolveCapabilitiesWithProbe(engine, mode, detectedVersion, rawBanner string) (
	target string,
	caps map[string]bool,
	flavor, tier, notice string,
	err error,
) {
	constraints := make([]string, 0, 2)
	for _, t := range CapabilityMatrix {
		if t.Engine == engine && t.Mode == mode {
			constraints = append(constraints, t.Constraint)
			if ConstraintMatches(t.Constraint, detectedVersion) {
				caps := map[string]bool{}
				for _, c := range requiredCapabilities {
					caps[c] = false
				}
				for _, c := range t.Capabilities {
					caps[c] = true
				}
				flavor, tier, notice = DetectEngineFlavor(engine, mode, rawBanner)
				if mode == "mysql" && flavor != "mysql_native" {
					caps[CapGhOst] = false
				}
				return t.Target, caps, flavor, tier, notice, nil
			}
		}
	}
	if len(constraints) == 0 {
		return "", nil, "", "", "", ErrUnsupportedCapability
	}
	// Engine is supported but detected version falls below the supported minimum floor (e.g. mysql 5.6.51 < >=5.7).
	return "", nil, "", "", "", fmt.Errorf("%w: engine %s mode %s detected version %s does not satisfy supported version constraint %s",
		ErrUnsupportedCapability, engine, mode, detectedVersion, strings.Join(constraints, ", "))
}

// ResolveCapabilities resolves capabilities from engine/mode and the detected version.
// Returns ErrUnsupportedCapability (explicit Unsupported) when no target matches.
func ResolveCapabilities(engine, mode, detectedVersion string) (string, map[string]bool, error) {
	target, caps, _, _, _, err := ResolveCapabilitiesWithProbe(engine, mode, detectedVersion, "")
	return target, caps, err
}

// DefaultVersionConstraint returns the built-in capability matrix version constraint
// for the given engine and compatibility mode, or empty string if unsupported.
func DefaultVersionConstraint(engine, mode string) string {
	for _, t := range CapabilityMatrix {
		if t.Engine == engine && t.Mode == mode {
			return t.Constraint
		}
	}
	return ""
}

// MajorMinor extracts the "N.M" major and minor version numbers.
func MajorMinor(version string) (int, int) {
	major, minor, ok := parseMajorMinor(version)
	if !ok {
		return 0, 0
	}
	return major, minor
}

// buildProbePayloads marshals persistent datasource metadata and async probe result_ref JSON.
func buildProbePayloads(target string, durationMS int64, detectedVersion, flavor, tier, notice string) (metaBytes, resultRefBytes []byte, err error) {
	metaBytes, err = json.Marshal(struct {
		DetectedVersion         string `json:"detected_version"`
		CapabilityTarget        string `json:"capability_target"`
		DetectedFlavor          string `json:"detected_flavor,omitempty"`
		VerificationTier        string `json:"verification_tier,omitempty"`
		CompatibilityModeNotice string `json:"compatibility_mode_notice,omitempty"`
	}{
		DetectedVersion:         detectedVersion,
		CapabilityTarget:        target,
		DetectedFlavor:          flavor,
		VerificationTier:        tier,
		CompatibilityModeNotice: notice,
	})
	if err != nil {
		return nil, nil, err
	}
	resultRefBytes, err = json.Marshal(struct {
		Stages                  []string `json:"stages"`
		DurationMS              int64    `json:"duration_ms"`
		DetectedVersion         string   `json:"detected_version"`
		CapabilityTarget        string   `json:"capability_target"`
		DetectedFlavor          string   `json:"detected_flavor,omitempty"`
		VerificationTier        string   `json:"verification_tier,omitempty"`
		CompatibilityModeNotice string   `json:"compatibility_mode_notice,omitempty"`
	}{
		Stages:                  []string{"dial", "authenticate", "probe"},
		DurationMS:              durationMS,
		DetectedVersion:         detectedVersion,
		CapabilityTarget:        target,
		DetectedFlavor:          flavor,
		VerificationTier:        tier,
		CompatibilityModeNotice: notice,
	})
	if err != nil {
		return nil, nil, err
	}
	return metaBytes, resultRefBytes, nil
}
