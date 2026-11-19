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

// B3 operation list (operationId aligned with OpenAPI).
const (
	OpLogin                          = "login"
	OpRegister                       = "register"
	OpListAuthenticationProviders    = "listAuthenticationProviders"
	OpLoginWithLdap                  = "loginWithLdap"
	OpStartOidcLogin                 = "startOidcLogin"
	OpFinishOidcLogin                = "finishOidcLogin"
	OpLogout                         = "logout"
	OpGetCurrentUser                 = "getCurrentUser"
	OpChangeMyPassword               = "changeMyPassword"
	OpListCurrentUserFlows           = "listCurrentUserFlows"
	OpListUsers                      = "listUsers"
	OpCreateUser                     = "createUser"
	OpUpdateUser                     = "updateUser"
	OpDeleteUser                     = "deleteUser"
	OpGetUserDeletionImpact          = "getUserDeletionImpact"
	OpListFlows                      = "listFlows"
	OpCreateFlow                     = "createFlow"
	OpGetFlow                        = "getFlow"
	OpReplaceFlow                    = "replaceFlow"
	OpDeleteFlow                     = "deleteFlow"
	OpListFlowMaskingRules           = "listFlowMaskingRules"
	OpReplaceFlowMaskingRule         = "replaceFlowMaskingRule"
	OpListDatasources                = "listDatasources"
	OpCreateDatasource               = "createDatasource"
	OpGetDatasource                  = "getDatasource"
	OpReplaceDatasource              = "replaceDatasource"
	OpDeleteDatasource               = "deleteDatasource"
	OpTestDatasourceConnection       = "testDatasourceConnection"
	OpGetDatasourceCapabilities      = "getDatasourceCapabilities"
	OpListPermissionGroups           = "listPermissionGroups"
	OpCreatePermissionGroup          = "createPermissionGroup"
	OpGetPermissionGroup             = "getPermissionGroup"
	OpReplacePermissionGroup         = "replacePermissionGroup"
	OpDeletePermissionGroup          = "deletePermissionGroup"
	OpListIdentityProviders          = "listIdentityProviders"
	OpCreateIdentityProvider         = "createIdentityProvider"
	OpGetIdentityProvider            = "getIdentityProvider"
	OpReplaceIdentityProvider        = "replaceIdentityProvider"
	OpDeleteIdentityProvider         = "deleteIdentityProvider"
	OpTestIdentityProviderConnection = "testIdentityProviderConnection"

	// Review engine management surface (authorization-policy builtin-admin-management;
	// mounted in BE-B7/BE-B8, policy map completed in BE-B8-SQL-REVIEW-HARDENING).
	OpListPromptTools             = "listPromptTools"
	OpCreatePromptTool            = "createPromptTool"
	OpGetPromptTool               = "getPromptTool"
	OpReplacePromptTool           = "replacePromptTool"
	OpDeletePromptTool            = "deletePromptTool"
	OpOptimizePromptToolPrompt    = "optimizePromptToolPrompt"
	OpGeneratePromptToolTestCases = "generatePromptToolTestCases"
	OpRunPromptToolTest           = "runPromptToolTest"
	OpListAiProviders             = "listAiProviders"
	OpCreateAiProvider            = "createAiProvider"
	OpGetAiProvider               = "getAiProvider"
	OpReplaceAiProvider           = "replaceAiProvider"
	OpDeleteAiProvider            = "deleteAiProvider"
	OpTestAiProviderConn          = "testAiProviderConnection"
	OpListAiProviderModels        = "listAiProviderModels"
	OpListAiProviderRateVersions  = "listAiProviderRateVersions"
	OpCreateAiProviderRateVersion = "createAiProviderRateVersion"
	OpListAiUsageCalls            = "listAiUsageCalls"
	OpListAiUsageSummary          = "listAiUsageSummary"
	OpListKnowledgeEntries        = "listKnowledgeEntries"
	OpCreateKnowledgeEntry        = "createKnowledgeEntry"
	OpGetKnowledgeEntry           = "getKnowledgeEntry"
	OpReplaceKnowledgeEntry       = "replaceKnowledgeEntry"
	OpDeleteKnowledgeEntry        = "deleteKnowledgeEntry"
	OpEvaluateKnowledgeEntry      = "evaluateKnowledgeEntry"
	OpListRuleSets                = "listRuleSets"
	OpCreateRuleSet               = "createRuleSet"
	OpGetRuleSet                  = "getRuleSet"
	OpReplaceRuleSet              = "replaceRuleSet"
	OpDeleteRuleSet               = "deleteRuleSet"

	// Settings domain (authorization-policy builtin-admin-management;
	// route mounting completed in BE-B13-CONTRACT-ALIGNMENT).
	OpGetSettingsNamespace  = "getSettingsNamespace"
	OpReplaceSettings       = "replaceSettingsNamespace"
	OpAssessSettingsImpact  = "assessSettingsImpact"
	OpGetSettingsSchema     = "getSettingsSchema"
	OpListSettingsRevisions = "listSettingsRevisions"

	// Dashboard/announcement/audit-read/notification domains (§17.6-5 mounted in BE-B13-SERVE-SURFACE-ALIGNMENT,
	// policy map updated in sync — routes were previously unwired so the Decide gap was invisible).
	OpGetMyDashboard             = "getMyDashboard"
	OpGetOperationsDashboard     = "getOperationsDashboard"
	OpGetSystemHealthDashboard   = "getSystemHealthDashboard"
	OpGetCurrentAnnouncement     = "getCurrentAnnouncement"
	OpGetLatestRelease           = "getLatestRelease"
	OpListAnnouncementRevisions  = "listAnnouncementRevisions"
	OpCreateAnnouncementRevision = "createAnnouncementRevision"
	OpPublishAnnouncement        = "publishAnnouncementRevision"
	OpListAuditEvents            = "listAuditEvents"
	OpListNotificationChannels   = "listNotificationChannels"
	OpCreateNotificationChannel  = "createNotificationChannel"
	OpGetNotificationChannel     = "getNotificationChannel"
	OpReplaceNotificationChannel = "replaceNotificationChannel"
	OpDeleteNotificationChannel  = "deleteNotificationChannel"
	OpCreateTestDelivery         = "createNotificationTestDelivery"
	OpListNotificationDeliveries = "listNotificationDeliveries"
	// S007 notification rules surface (RCP-20261031; builtin-admin-management family).
	OpListNotificationRules   = "listNotificationRules"
	OpCreateNotificationRule  = "createNotificationRule"
	OpGetNotificationRule     = "getNotificationRule"
	OpReplaceNotificationRule = "replaceNotificationRule"
	OpDeleteNotificationRule  = "deleteNotificationRule"
	OpPreviewNotificationRule = "previewNotificationRule"

	// P101-P103 SQL performance diagnostics surface (RCP-20261108-SQL-PERFORMANCE-DIAGNOSTICS; builtin-admin-management family).
	OpGetDatasourceDiagnosticsConfig    = "getDatasourceDiagnosticsConfig"
	OpUpdateDatasourceDiagnosticsConfig = "updateDatasourceDiagnosticsConfig"
	OpListDiagnosticDatasources         = "listDiagnosticDatasources"
	OpGetDiagnosticsOverview            = "getDiagnosticsOverview"
	OpTriggerManualDiagnosticsSnapshot  = "triggerManualDiagnosticsSnapshot"
	OpGetDiagnosticStatementDetail      = "getDiagnosticStatementDetail"
	OpConfirmDiagnosticOrderLink        = "confirmDiagnosticOrderLink"
	OpDiagnoseStatementWithAi           = "diagnoseStatementWithAi"
	OpPrecheckDiagnosticOptDraft        = "precheckDiagnosticOptimizationDraft"
	OpSubmitDiagnosticOptOrder          = "submitDiagnosticOptimizationOrder"

	// Global real-time SSE domain event stream (RCP-20261118-REALTIME-SSE-BROADCAST; authenticated-session).
	OpStreamDomainEvents = "streamDomainEvents"
)

var (
	// reqAuthEntry: authentication entry point rule (authorization-policy anonymous-authentication:
	// actors are anonymous and authenticated_user). Callable by any class —
	// re-logging in or registering while holding a session is a valid path; the operation itself grants no business privileges.
	reqAuthEntry     = Requirement{MinClass: ActorAnonymous}
	reqAuthenticated = Requirement{MinClass: ActorAuthenticated}
	reqBuiltinAdmin  = Requirement{MinClass: ActorBuiltinAdmin, Exact: true}
)

// policy is the canonical operation -> requirement mapping; unlisted operations default to deny.
var policy = map[string]Requirement{
	OpLogin:                          reqAuthEntry,
	OpRegister:                       reqAuthEntry,
	OpListAuthenticationProviders:    reqAuthEntry,
	OpLoginWithLdap:                  reqAuthEntry,
	OpStartOidcLogin:                 reqAuthEntry,
	OpFinishOidcLogin:                reqAuthEntry,
	OpLogout:                         reqAuthenticated,
	OpGetCurrentUser:                 reqAuthenticated,
	OpChangeMyPassword:               reqAuthenticated,
	OpListCurrentUserFlows:           reqAuthenticated,
	OpListUsers:                      reqBuiltinAdmin,
	OpCreateUser:                     reqBuiltinAdmin,
	OpUpdateUser:                     reqBuiltinAdmin,
	OpDeleteUser:                     reqBuiltinAdmin,
	OpGetUserDeletionImpact:          reqBuiltinAdmin,
	OpListFlows:                      reqBuiltinAdmin,
	OpCreateFlow:                     reqBuiltinAdmin,
	OpGetFlow:                        reqBuiltinAdmin,
	OpReplaceFlow:                    reqBuiltinAdmin,
	OpDeleteFlow:                     reqBuiltinAdmin,
	OpListFlowMaskingRules:           reqBuiltinAdmin,
	OpReplaceFlowMaskingRule:         reqBuiltinAdmin,
	OpListDatasources:                reqBuiltinAdmin,
	OpCreateDatasource:               reqBuiltinAdmin,
	OpGetDatasource:                  reqBuiltinAdmin,
	OpReplaceDatasource:              reqBuiltinAdmin,
	OpDeleteDatasource:               reqBuiltinAdmin,
	OpTestDatasourceConnection:       reqBuiltinAdmin,
	OpGetDatasourceCapabilities:      reqBuiltinAdmin,
	OpListPermissionGroups:           reqBuiltinAdmin,
	OpCreatePermissionGroup:          reqBuiltinAdmin,
	OpGetPermissionGroup:             reqBuiltinAdmin,
	OpReplacePermissionGroup:         reqBuiltinAdmin,
	OpDeletePermissionGroup:          reqBuiltinAdmin,
	OpListIdentityProviders:          reqBuiltinAdmin,
	OpCreateIdentityProvider:         reqBuiltinAdmin,
	OpGetIdentityProvider:            reqBuiltinAdmin,
	OpReplaceIdentityProvider:        reqBuiltinAdmin,
	OpDeleteIdentityProvider:         reqBuiltinAdmin,
	OpTestIdentityProviderConnection: reqBuiltinAdmin,

	OpListPromptTools:             reqBuiltinAdmin,
	OpCreatePromptTool:            reqBuiltinAdmin,
	OpGetPromptTool:               reqBuiltinAdmin,
	OpReplacePromptTool:           reqBuiltinAdmin,
	OpDeletePromptTool:            reqBuiltinAdmin,
	OpOptimizePromptToolPrompt:    reqBuiltinAdmin,
	OpGeneratePromptToolTestCases: reqBuiltinAdmin,
	OpRunPromptToolTest:           reqBuiltinAdmin,
	OpListAiProviders:             reqBuiltinAdmin,
	OpCreateAiProvider:            reqBuiltinAdmin,
	OpGetAiProvider:               reqBuiltinAdmin,
	OpReplaceAiProvider:           reqBuiltinAdmin,
	OpDeleteAiProvider:            reqBuiltinAdmin,
	OpTestAiProviderConn:          reqBuiltinAdmin,
	OpListAiProviderModels:        reqBuiltinAdmin,
	OpListAiProviderRateVersions:  reqBuiltinAdmin,
	OpCreateAiProviderRateVersion: reqBuiltinAdmin,
	OpListAiUsageCalls:            reqBuiltinAdmin,
	OpListAiUsageSummary:          reqBuiltinAdmin,
	OpListKnowledgeEntries:        reqBuiltinAdmin,
	OpCreateKnowledgeEntry:        reqBuiltinAdmin,
	OpGetKnowledgeEntry:           reqBuiltinAdmin,
	OpReplaceKnowledgeEntry:       reqBuiltinAdmin,
	OpDeleteKnowledgeEntry:        reqBuiltinAdmin,
	OpEvaluateKnowledgeEntry:      reqBuiltinAdmin,
	OpListRuleSets:                reqBuiltinAdmin,
	OpCreateRuleSet:               reqBuiltinAdmin,
	OpGetRuleSet:                  reqBuiltinAdmin,
	OpReplaceRuleSet:              reqBuiltinAdmin,
	OpDeleteRuleSet:               reqBuiltinAdmin,

	OpGetSettingsNamespace:  reqBuiltinAdmin,
	OpReplaceSettings:       reqBuiltinAdmin,
	OpAssessSettingsImpact:  reqBuiltinAdmin,
	OpGetSettingsSchema:     reqBuiltinAdmin,
	OpListSettingsRevisions: reqBuiltinAdmin,

	// Dashboard/announcement/audit-read/notification domains: §18.71 platform operations stats, §18.83 system health, personal dashboard,
	// and current announcement use authenticated-session; remaining management endpoints use builtin-admin-management.
	OpGetMyDashboard:             reqAuthenticated,
	OpGetOperationsDashboard:     reqAuthenticated,
	OpGetCurrentAnnouncement:     reqAuthenticated,
	OpGetLatestRelease:           reqAuthenticated,
	OpGetSystemHealthDashboard:   reqAuthenticated,
	OpListAnnouncementRevisions:  reqBuiltinAdmin,
	OpCreateAnnouncementRevision: reqBuiltinAdmin,
	OpPublishAnnouncement:        reqBuiltinAdmin,
	OpListAuditEvents:            reqBuiltinAdmin,
	OpListNotificationChannels:   reqBuiltinAdmin,
	OpCreateNotificationChannel:  reqBuiltinAdmin,
	OpGetNotificationChannel:     reqBuiltinAdmin,
	OpReplaceNotificationChannel: reqBuiltinAdmin,
	OpDeleteNotificationChannel:  reqBuiltinAdmin,
	OpCreateTestDelivery:         reqBuiltinAdmin,
	OpListNotificationDeliveries: reqBuiltinAdmin,
	OpListNotificationRules:      reqBuiltinAdmin,
	OpCreateNotificationRule:     reqBuiltinAdmin,
	OpGetNotificationRule:        reqBuiltinAdmin,
	OpReplaceNotificationRule:    reqBuiltinAdmin,
	OpDeleteNotificationRule:     reqBuiltinAdmin,
	OpPreviewNotificationRule:    reqBuiltinAdmin,

	OpGetDatasourceDiagnosticsConfig:    reqBuiltinAdmin,
	OpUpdateDatasourceDiagnosticsConfig: reqBuiltinAdmin,
	OpListDiagnosticDatasources:         reqBuiltinAdmin,
	OpGetDiagnosticsOverview:            reqBuiltinAdmin,
	OpTriggerManualDiagnosticsSnapshot:  reqBuiltinAdmin,
	OpGetDiagnosticStatementDetail:      reqBuiltinAdmin,
	OpConfirmDiagnosticOrderLink:        reqBuiltinAdmin,
	OpDiagnoseStatementWithAi:           reqBuiltinAdmin,
	OpPrecheckDiagnosticOptDraft:        reqBuiltinAdmin,
	OpSubmitDiagnosticOptOrder:          reqBuiltinAdmin,

	OpStreamDomainEvents: reqAuthenticated,
}

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
