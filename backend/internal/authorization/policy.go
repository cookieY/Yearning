// Package authorization implements the default-deny policy engine defined by authorization-policy.json:
// every operation must match a single rule; unclassified operations are always denied.
//
// B3 covers three actor classes: anonymous authentication entry points, authenticated users, and the built-in admin management surface.
// Business-relationship actors (submitter/frozen reviewer, etc.) are implemented by subsequent Packages within their own rules;
// this package retains extension points for Actor predicates. The admin identity does not imply any business approval, execution, or query privileges.
package authorization

import (
	"github.com/cookieY/YearningX/backend/internal/identity/session"
)

// ActorClass represents an actor class recognized by the policy engine.
type ActorClass string

const (
	ActorAnonymous     ActorClass = "anonymous"
	ActorAuthenticated ActorClass = "authenticated_user"
	ActorBuiltinAdmin  ActorClass = "builtin_admin"
)

// Requirement describes the minimum actor requirement for an operation.
// Admin operations require ActorBuiltinAdmin (exact match; authentication alone is insufficient);
// authenticated operations accept any valid session (including admin, without granting business privileges).
type Requirement struct {
	MinClass ActorClass
	Exact    bool // true means the actor class must equal MinClass exactly
}

var (
	// reqAuthEntry: authentication entry point rule (authorization-policy anonymous-authentication:
	// actors are anonymous and authenticated_user). Callable by any class —
	// re-logging in or registering while holding a session is a valid path; the operation itself grants no business privileges.
	reqAuthEntry     = Requirement{MinClass: ActorAnonymous}
	reqAuthenticated = Requirement{MinClass: ActorAuthenticated}
	reqBuiltinAdmin  = Requirement{MinClass: ActorBuiltinAdmin, Exact: true}

	authEntryOps = []string{
		"login",
		"register",
		"listAuthenticationProviders",
		"loginWithLdap",
		"startOidcLogin",
		"finishOidcLogin",
	}

	// Authenticated operations: §18.71 platform operations stats, §18.83 system health, personal
	// dashboard, current announcement, and SSE stream use authenticated-session; remaining
	// management endpoints use builtin-admin-management.
	authenticatedOps = []string{
		"logout",
		"getCurrentUser",
		"changeMyPassword",
		"listCurrentUserFlows",
		"getMyDashboard",
		"getOperationsDashboard",
		"getCurrentAnnouncement",
		"getLatestRelease",
		"getSystemHealthDashboard",
		"streamDomainEvents",
	}

	// Built-in admin management surface (aligned with OpenAPI operationIds).
	builtinAdminOps = []string{
		// B3 identity, flows, datasources, permission groups, identity providers
		"listUsers", "createUser", "updateUser", "resetUserPassword", "deleteUser", "getUserDeletionImpact",
		"listFlows", "createFlow", "getFlow", "replaceFlow", "deleteFlow",
		"listFlowMaskingRules", "replaceFlowMaskingRule",
		"listDatasources", "createDatasource", "getDatasource", "replaceDatasource", "deleteDatasource",
		"testDatasourceConnection", "getDatasourceCapabilities",
		"listPermissionGroups", "createPermissionGroup", "getPermissionGroup", "replacePermissionGroup", "deletePermissionGroup",
		"listIdentityProviders", "createIdentityProvider", "getIdentityProvider", "replaceIdentityProvider",
		"deleteIdentityProvider", "testIdentityProviderConnection",

		// Review engine management surface (mounted in BE-B7/BE-B8, completed in BE-B8-SQL-REVIEW-HARDENING)
		"listPromptTools", "createPromptTool", "getPromptTool", "replacePromptTool", "deletePromptTool",
		"optimizePromptToolPrompt", "generatePromptToolTestCases", "runPromptToolTest",
		"listAiProviders", "createAiProvider", "getAiProvider", "replaceAiProvider", "deleteAiProvider",
		"testAiProviderConnection", "listAiProviderModels",
		"listAiProviderRateVersions", "createAiProviderRateVersion",
		"listAiUsageCalls", "listAiUsageSummary",
		"listKnowledgeEntries", "createKnowledgeEntry", "getKnowledgeEntry", "replaceKnowledgeEntry",
		"deleteKnowledgeEntry", "evaluateKnowledgeEntry",
		"listRuleSets", "createRuleSet", "getRuleSet", "replaceRuleSet", "deleteRuleSet",

		// Settings domain (BE-B13-CONTRACT-ALIGNMENT)
		"getSettingsNamespace", "replaceSettingsNamespace", "assessSettingsImpact",
		"getSettingsSchema", "listSettingsRevisions",

		// Announcements, audit read, and notification channels/rules (S007 / RCP-20261031)
		"listAnnouncementRevisions", "createAnnouncementRevision", "publishAnnouncementRevision", "listAuditEvents",
		"listNotificationChannels", "createNotificationChannel", "getNotificationChannel",
		"replaceNotificationChannel", "deleteNotificationChannel",
		"createNotificationTestDelivery", "listNotificationDeliveries",
		"listNotificationRules", "createNotificationRule", "getNotificationRule",
		"replaceNotificationRule", "deleteNotificationRule", "previewNotificationRule",

		// P101-P103 SQL performance diagnostics surface (RCP-20261108-SQL-PERFORMANCE-DIAGNOSTICS)
		"getDatasourceDiagnosticsConfig", "updateDatasourceDiagnosticsConfig",
		"listDiagnosticDatasources", "getDiagnosticsOverview", "triggerManualDiagnosticsSnapshot",
		"getDiagnosticStatementDetail", "confirmDiagnosticOrderLink", "diagnoseStatementWithAi",
		"precheckDiagnosticOptimizationDraft", "submitDiagnosticOptimizationOrder",
	}

	// policy is the canonical operation -> requirement mapping; unlisted operations default to deny.
	policy = func() map[string]Requirement {
		m := make(map[string]Requirement, len(authEntryOps)+len(authenticatedOps)+len(builtinAdminOps))
		register := func(ops []string, req Requirement) {
			for _, op := range ops {
				if _, dup := m[op]; dup {
					panic("authorization: duplicate policy operation " + op)
				}
				m[op] = req
			}
		}
		register(authEntryOps, reqAuthEntry)
		register(authenticatedOps, reqAuthenticated)
		register(builtinAdminOps, reqBuiltinAdmin)
		return m
	}()
)

// Classify returns the actor class for the current session identity.
func Classify(id *session.Identity) ActorClass {
	switch {
	case id == nil:
		return ActorAnonymous
	case id.HasAdminAccess():
		return ActorBuiltinAdmin
	default:
		return ActorAuthenticated
	}
}

// Decide determines whether identity is permitted to execute operation.
// Unclassified operations return false (default deny; build-time tests guarantee zero omissions).
func Decide(operation string, id *session.Identity) bool {
	req, ok := policy[operation]
	if !ok {
		return false
	}
	class := Classify(id)
	if req.Exact {
		return class == req.MinClass
	}
	switch req.MinClass {
	case ActorAnonymous:
		return true
	case ActorAuthenticated:
		return class == ActorAuthenticated || class == ActorBuiltinAdmin
	default:
		return class == ActorBuiltinAdmin
	}
}

// IsAnonymousOperation reports whether the operation targets anonymous actors (login/registration, etc.).
// Mutating requests for such operations carry no session CSRF token (no session exists yet);
// protection relies on SameSite cookies and backend authentication itself.
func IsAnonymousOperation(operation string) bool {
	req, ok := policy[operation]
	return ok && req.MinClass == ActorAnonymous && !req.Exact
}

// KnownOperations returns the set of classified operations (used for tests and build-time coverage checks).
func KnownOperations() map[string]Requirement {
	out := make(map[string]Requirement, len(policy))
	for k, v := range policy {
		out[k] = v
	}
	return out
}

// IsMutating reports whether the operation mutates state (CSRF verification scope).
func IsMutating(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

// businessRelationshipOps lists every mounted OpenAPI operationId whose authorization
// requires an authenticated session at the HTTP boundary and actor-relationship enforcement
// in the application layer (default-deny outside this allowlist).
var businessRelationshipOps = map[string]struct{}{
	// B6 review domain
	"listChangeDrafts":            {},
	"createChangeDraft":           {},
	"getChangeDraft":              {},
	"updateChangeDraft":           {},
	"deleteChangeDraft":           {},
	"replaceDraftSql":             {},
	"revealDraftSql":              {},
	"runDraftReview":              {},
	"getReviewRun":                {},
	"listReviewRunFindings":       {},
	"revealIdentifierMapping":     {},
	"listReviewFindingEvidence":   {},
	"revealRawReviewEvidence":     {},
	"recordRawReviewEvidenceCopy": {},
	"getTask":                     {},

	// B9 change-order domain
	"submitChangeDraft":        {},
	"listChangeOrders":         {},
	"getChangeOrder":           {},
	"decideChangeOrder":        {},
	"withdrawChangeOrder":      {},
	"voidChangeOrder":          {},
	"copyChangeOrderToDraft":   {},
	"createRollbackDraft":      {},
	"listChangeOrderTimeline":  {},
	"listChangeOrderComments":  {},
	"createChangeOrderComment": {},
	"listOrderReviewFindings":  {},
	"revealOrderSql":           {},
	"recordOrderSqlCopy":       {},

	// B10 execution domain
	"createExecutionAttempt":         {},
	"getExecutionAttempt":            {},
	"listExecutionAttemptStatements": {},
	"cancelExecutionAttempt":         {},
	"createExecutionVerification":    {},

	// B11 query & execution schedule domain
	"listQueryAccessRequests":        {},
	"createQueryAccessRequest":       {},
	"getQueryAccessRequest":          {},
	"decideQueryAccess":              {},
	"withdrawQueryAccessRequest":     {},
	"listQueryGrants":                {},
	"getQueryGrant":                  {},
	"revokeQueryGrant":               {},
	"relinquishQueryGrant":           {},
	"createQueryGrantRenewalRequest": {},
	"listQueryOrders":                {},
	"listQuerySessions":              {},
	"createQuerySession":             {},
	"getQuerySession":                {},
	"closeQuerySession":              {},
	"listQuerySessionSchemas":        {},
	"listQuerySessionTables":         {},
	"listQuerySessionColumns":        {},
	"listMyFlowDatasourceSchemas":    {},
	"listMyFlowDatasourceTables":     {},
	"listMyFlowDatasourceColumns":    {},
	"executeSelect":                  {},
	"fetchQueryResultPage":           {},
	"createExecutionSchedule":        {},
	"cancelExecutionSchedule":        {},

	// AI Assistant domain
	"listAssistantSessions":        {},
	"createAssistantSession":       {},
	"getAssistantSession":          {},
	"deleteAssistantSession":       {},
	"sendAssistantMessage":         {},
	"cancelAssistantMessageStream": {},
	"precheckAssistantSqlDraft":    {},
	"submitAssistantSqlDraftOrder": {},
}

// IsBusinessRelationshipOperation reports whether op is registered as a business-relationship
// operation whose fine-grained actor eligibility is enforced by the application layer.
func IsBusinessRelationshipOperation(op string) bool {
	_, ok := businessRelationshipOps[op]
	return ok
}
