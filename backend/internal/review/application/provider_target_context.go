package application

import (
	"fmt"
	"regexp"

	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
)

var controlledServerVersion = regexp.MustCompile(`^[0-9]{1,4}(\.[0-9]{1,4}){0,3}$`)

// providerTargetContext exposes only the dialect and numeric server version
// recorded by the successful frozen identity probe. It never serializes the
// target itself or claims that the live server has been reprobed this turn.
func providerTargetContext(target FrozenDatasourceTarget) (string, error) {
	profile := fingerprint.Profile{Engine: target.Engine, Mode: target.CompatibilityMode}
	if profile.Validate() != nil || target.EngineFamily != target.CompatibilityMode || !controlledServerVersion.MatchString(target.ServerVersion) {
		return "", errFrozenDatasourceTarget
	}
	return fmt.Sprintf("\n<frozen-target-context>engine_profile=%s; server_version=%s; identity_match=matched_at_freeze</frozen-target-context>\n", profile.String(), target.ServerVersion), nil
}
