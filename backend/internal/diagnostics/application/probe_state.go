// probe_state.go Normalization helpers for OpenAPI DiagnosticsProbeStatus values
// and dynamic-field zero-leak checks in diagnostics AI prompts.
package application

import (
	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/diagnostics/domain"
)

func normalizeProbeState(raw string) string {
	switch raw {
	case string(database.StatsProbeReady),
		string(database.StatsProbeDisabled),
		string(database.StatsProbeInsufficientPriv),
		string(database.StatsProbeUnsupportedEngine),
		string(database.StatsProbeNoSamples),
		string(database.StatsProbeCollectionFailed):
		return raw
	default:
		return string(database.StatsProbeNoSamples)
	}
}

// assertDiagnoseDynamicZeroLeak verifies zero identifier/literal/credential leaks across
// dynamic fields only (static JSON protocol keys such as "calls", "window", "engine", or
// "rows_returned" may legitimately match column names on user tables or system views).
func assertDiagnoseDynamicZeroLeak(
	priv *privacy.TurnPrivacySession,
	desensitized *DesensitizeTemplateResult,
	windowLabel string,
) error {
	if priv == nil || desensitized == nil {
		return domain.ErrAnonymizationFailed("missing_privacy_context")
	}
	for _, dynamicField := range []string{
		desensitized.AnonymizedTemplate,
		desensitized.AnonymizedSummary,
		windowLabel,
	} {
		if err := priv.AssertZeroLeak(dynamicField); err != nil {
			return err
		}
	}
	return nil
}
