// GENERATED FILE — do not edit by hand.
// Source: api/contracts/operation-error-profiles.json (hash-bound in src/api/generated/contract-manifest.json)
// Regenerate with: pnpm api:generate
export interface OperationErrorProfile {
  err_codes: number[];
  operations: string[];
}

export const OPERATION_ERROR_PROFILES: {
  profiles: Record<string, OperationErrorProfile>;
  semantics: {
    unlistedBusinessErrorIsContractViolation: boolean;
  };
} = {
  profiles: {
  "no_business_error": {
    "err_codes": [],
    "operations": [
      "logout",
      "listAuthenticationProviders",
      "startOidcLogin",
      "finishOidcLogin",
      "getCurrentUser",
      "getCurrentAnnouncement",
      "getLatestRelease",
      "streamDomainEvents"
    ]
  },
  "validated_list": {
    "err_codes": [
      1001
    ],
    "operations": [
      "listAiProviderRateVersions",
      "listAiProviders",
      "listAiUsageCalls",
      "listAiUsageSummary",
      "listAnnouncementRevisions",
      "listAuditEvents",
      "listChangeDrafts",
      "listChangeOrders",
      "listCurrentUserFlows",
      "listDatasources",
      "listFlowMaskingRules",
      "listFlows",
      "listIdentityProviders",
      "listKnowledgeEntries",
      "listLegacyMigrationRuns",
      "listNotificationChannels",
      "listNotificationDeliveries",
      "listNotificationRules",
      "listPermissionGroups",
      "listPromptTools",
      "listQueryAccessRequests",
      "listQueryGrants",
      "listQueryOrders",
      "listQuerySessions",
      "listRuleSets",
      "listUsers",
      "listAssistantSessions",
      "listDiagnosticDatasources"
    ]
  },
  "resource_read": {
    "err_codes": [
      1001,
      1002
    ],
    "operations": [
      "getUserDeletionImpact",
      "getFlow",
      "getLegacyMigrationRun",
      "getIdentityProvider",
      "getDatasource",
      "getDatasourceCapabilities",
      "getPermissionGroup",
      "getRuleSet",
      "getPromptTool",
      "getAiProvider",
      "getSettingsNamespace",
      "getSettingsSchema",
      "listSettingsRevisions",
      "getNotificationChannel",
      "getChangeDraft",
      "getReviewRun",
      "listReviewRunFindings",
      "getChangeOrder",
      "listOrderReviewFindings",
      "listReviewFindingEvidence",
      "listChangeOrderComments",
      "listChangeOrderTimeline",
      "getExecutionAttempt",
      "listExecutionAttemptStatements",
      "getQueryAccessRequest",
      "getQueryGrant",
      "getQuerySession",
      "getMyDashboard",
      "getOperationsDashboard",
      "getSystemHealthDashboard",
      "getKnowledgeEntry",
      "getNotificationRule",
      "getAssistantSession"
    ]
  },
  "auth_login": {
    "err_codes": [
      1001,
      1101,
      1102
    ],
    "operations": [
      "login"
    ]
  },
  "auth_external_login": {
    "err_codes": [
      1001,
      1101,
      1002
    ],
    "operations": [
      "loginWithLdap"
    ]
  },
  "auth_register": {
    "err_codes": [
      1001,
      1005
    ],
    "operations": [
      "register"
    ]
  },
  "password_change": {
    "err_codes": [
      1001,
      1101,
      1109
    ],
    "operations": [
      "changeMyPassword"
    ]
  },
  "admin_create": {
    "err_codes": [
      1001,
      1005
    ],
    "operations": [
      "createUser",
      "createIdentityProvider",
      "createDatasource",
      "createPermissionGroup",
      "createRuleSet",
      "createPromptTool",
      "createNotificationChannel",
      "createAnnouncementRevision",
      "createKnowledgeEntry",
      "createNotificationRule",
      "previewNotificationRule"
    ]
  },
  "admin_replace": {
    "err_codes": [
      1001,
      1002,
      1003,
      1004,
      1005
    ],
    "operations": [
      "updateUser",
      "replaceIdentityProvider",
      "replaceDatasource",
      "replacePermissionGroup",
      "replaceRuleSet",
      "replacePromptTool",
      "replaceNotificationChannel",
      "replaceKnowledgeEntry",
      "replaceFlowMaskingRule",
      "replaceNotificationRule"
    ]
  },
  "admin_delete": {
    "err_codes": [
      1001,
      1002,
      1006,
      1004
    ],
    "operations": [
      "deleteIdentityProvider",
      "deletePermissionGroup",
      "deleteRuleSet",
      "deletePromptTool",
      "deleteAiProvider",
      "deleteNotificationChannel",
      "deleteKnowledgeEntry",
      "deleteNotificationRule"
    ]
  },
  "user_delete": {
    "err_codes": [
      1001,
      1002,
      1103,
      1104,
      1105,
      1004
    ],
    "operations": [
      "deleteUser"
    ]
  },
  "flow_write": {
    "err_codes": [
      1001,
      1002,
      1003,
      1004,
      1108,
      1107
    ],
    "operations": [
      "createFlow",
      "replaceFlow"
    ]
  },
  "flow_delete": {
    "err_codes": [
      1001,
      1002,
      1106,
      1004
    ],
    "operations": [
      "deleteFlow"
    ]
  },
  "datasource_delete": {
    "err_codes": [
      1001,
      1002,
      1107,
      1004
    ],
    "operations": [
      "deleteDatasource"
    ]
  },
  "connection_test": {
    "err_codes": [
      1001,
      1002,
      3010,
      1012
    ],
    "operations": [
      "testIdentityProviderConnection",
      "testDatasourceConnection",
      "createNotificationTestDelivery",
      "evaluateKnowledgeEntry"
    ]
  },
  "settings_replace": {
    "err_codes": [
      1001,
      1002,
      1003,
      1004,
      1011
    ],
    "operations": [
      "replaceSettingsNamespace",
      "assessSettingsImpact"
    ]
  },
  "announcement_publish": {
    "err_codes": [
      1001,
      1002,
      1003,
      1004
    ],
    "operations": [
      "publishAnnouncementRevision"
    ]
  },
  "migration_confirm": {
    "err_codes": [
      1001,
      1002,
      1003,
      1004,
      5003,
      5005
    ],
    "operations": [
      "confirmLegacyMigrationCandidate"
    ]
  },
  "migration_approve": {
    "err_codes": [
      1001,
      1002,
      1003,
      1004,
      5002,
      5003,
      5005
    ],
    "operations": [
      "approveLegacyMigrationRun"
    ]
  },
  "draft_create": {
    "err_codes": [
      1001,
      1002,
      2014,
      1005
    ],
    "operations": [
      "createChangeDraft"
    ]
  },
  "draft_update": {
    "err_codes": [
      1001,
      1002,
      1003,
      1004,
      1010,
      2014
    ],
    "operations": [
      "updateChangeDraft"
    ]
  },
  "draft_sql_replace": {
    "err_codes": [
      1001,
      1002,
      1003,
      1004,
      1010,
      2014,
      2002,
      2003
    ],
    "operations": [
      "replaceDraftSql"
    ]
  },
  "draft_delete": {
    "err_codes": [
      1002,
      1010,
      1004
    ],
    "operations": [
      "deleteChangeDraft"
    ]
  },
  "sensitive_reveal": {
    "err_codes": [
      1001,
      1002,
      1011
    ],
    "operations": [
      "revealDraftSql",
      "revealOrderSql",
      "revealRawReviewEvidence",
      "recordOrderSqlCopy",
      "recordRawReviewEvidenceCopy",
      "revealIdentifierMapping"
    ]
  },
  "draft_review": {
    "err_codes": [
      1001,
      1002,
      1010,
      1012,
      2001,
      2002,
      2003,
      2004,
      2005,
      2006,
      2007,
      2009,
      2010,
      2011,
      2012,
      1007,
      1008,
      1003,
      1004
    ],
    "operations": [
      "runDraftReview"
    ]
  },
  "draft_submit": {
    "err_codes": [
      1001,
      1002,
      1010,
      2001,
      2007,
      2008,
      2013,
      2014,
      1004,
      1007,
      1008
    ],
    "operations": [
      "submitChangeDraft"
    ]
  },
  "order_comment": {
    "err_codes": [
      1001,
      1002,
      1010,
      1007,
      1008
    ],
    "operations": [
      "createChangeOrderComment"
    ]
  },
  "order_decision": {
    "err_codes": [
      1001,
      1002,
      1010,
      1004,
      3001,
      3002,
      1007,
      1008
    ],
    "operations": [
      "decideChangeOrder"
    ]
  },
  "execution_start": {
    "err_codes": [
      1001,
      1002,
      1010,
      1004,
      1012,
      3001,
      3003,
      3004,
      3006,
      3010,
      3011,
      1007,
      1008
    ],
    "operations": [
      "createExecutionAttempt"
    ]
  },
  "execution_schedule": {
    "err_codes": [
      1001,
      1002,
      1010,
      1004,
      3001,
      3003,
      3007,
      3008,
      3010,
      1007,
      1008
    ],
    "operations": [
      "createExecutionSchedule",
      "cancelExecutionSchedule"
    ]
  },
  "execution_cancel": {
    "err_codes": [
      1001,
      1002,
      1010,
      1004,
      3001,
      3005,
      1007,
      1008
    ],
    "operations": [
      "cancelExecutionAttempt"
    ]
  },
  "execution_verify": {
    "err_codes": [
      1001,
      1002,
      1010,
      1004,
      3001,
      3012,
      1007,
      1008
    ],
    "operations": [
      "createExecutionVerification"
    ]
  },
  "order_lifecycle": {
    "err_codes": [
      1001,
      1002,
      1010,
      1004,
      3005,
      1007,
      1008
    ],
    "operations": [
      "withdrawChangeOrder",
      "voidChangeOrder"
    ]
  },
  "query_access_create": {
    "err_codes": [
      1001,
      1002,
      2014,
      1005,
      3010,
      1007,
      1008
    ],
    "operations": [
      "createQueryAccessRequest"
    ]
  },
  "query_access_decide": {
    "err_codes": [
      1001,
      1002,
      1010,
      1004,
      3001,
      3002,
      1007,
      1008
    ],
    "operations": [
      "decideQueryAccess"
    ]
  },
  "query_access_withdraw": {
    "err_codes": [
      1002,
      1010,
      1004,
      1007,
      1008
    ],
    "operations": [
      "withdrawQueryAccessRequest"
    ]
  },
  "query_grant_revoke": {
    "err_codes": [
      1001,
      1002,
      1010,
      1004,
      3001,
      4004,
      4005,
      1007,
      1008
    ],
    "operations": [
      "revokeQueryGrant"
    ]
  },
  "query_grant_relinquish": {
    "err_codes": [
      1002,
      1010,
      1004,
      4004,
      4005,
      1007,
      1008
    ],
    "operations": [
      "relinquishQueryGrant"
    ]
  },
  "query_grant_renew": {
    "err_codes": [
      1001,
      1002,
      2014,
      4004,
      4005,
      3010,
      1007,
      1008
    ],
    "operations": [
      "createQueryGrantRenewalRequest"
    ]
  },
  "query_session_create": {
    "err_codes": [
      1001,
      1002,
      4001,
      4002,
      4004,
      4005,
      3010
    ],
    "operations": [
      "createQuerySession"
    ]
  },
  "query_session_close": {
    "err_codes": [
      1002,
      1010,
      1004,
      4006,
      1007,
      1008
    ],
    "operations": [
      "closeQuerySession"
    ]
  },
  "query_metadata": {
    "err_codes": [
      1001,
      1002,
      1012,
      3010,
      4002,
      4004,
      4005,
      4006
    ],
    "operations": [
      "listQuerySessionSchemas",
      "listQuerySessionTables",
      "listQuerySessionColumns"
    ]
  },
  "query_execute": {
    "err_codes": [
      1001,
      1002,
      1012,
      3010,
      4002,
      4004,
      4005,
      4006,
      4007,
      4008,
      4009,
      4011
    ],
    "operations": [
      "executeSelect"
    ]
  },
  "query_page": {
    "err_codes": [
      1001,
      1002,
      1012,
      1009,
      4010,
      4003,
      4004,
      4005,
      4006
    ],
    "operations": [
      "fetchQueryResultPage"
    ]
  },
  "task_read": {
    "err_codes": [
      1001,
      1002
    ],
    "operations": [
      "getTask"
    ]
  },
  "draft_copy": {
    "err_codes": [
      1001,
      1002,
      2014,
      1005,
      1007,
      1008
    ],
    "operations": [
      "copyChangeOrderToDraft",
      "createRollbackDraft"
    ]
  },
  "ai_provider_create": {
    "err_codes": [
      1001,
      1005,
      2011
    ],
    "operations": [
      "createAiProvider"
    ]
  },
  "ai_provider_replace": {
    "err_codes": [
      1001,
      1002,
      1003,
      1004,
      1005,
      2011
    ],
    "operations": [
      "replaceAiProvider"
    ]
  },
  "ai_provider_connection_test": {
    "err_codes": [
      1002,
      2011
    ],
    "operations": [
      "testAiProviderConnection"
    ]
  },
  "ai_provider_model_catalog": {
    "err_codes": [
      1001,
      1002,
      2011
    ],
    "operations": [
      "listAiProviderModels",
      "optimizePromptToolPrompt",
      "generatePromptToolTestCases",
      "runPromptToolTest"
    ]
  },
  "submitter_metadata": {
    "err_codes": [
      1001,
      1002,
      2014,
      3010
    ],
    "operations": [
      "listMyFlowDatasourceSchemas",
      "listMyFlowDatasourceTables",
      "listMyFlowDatasourceColumns"
    ]
  },
  "ai_rate_version_create": {
    "err_codes": [
      1001,
      1002,
      1005
    ],
    "operations": [
      "createAiProviderRateVersion"
    ]
  },
  "assistant_session_create": {
    "err_codes": [
      1001,
      1002,
      6002,
      1012
    ],
    "operations": [
      "createAssistantSession"
    ]
  },
  "assistant_session_lifecycle": {
    "err_codes": [
      1001,
      1002,
      1010,
      1004
    ],
    "operations": [
      "deleteAssistantSession",
      "cancelAssistantMessageStream"
    ]
  },
  "assistant_message_send": {
    "err_codes": [
      1001,
      1002,
      6001,
      6002,
      6003,
      6004,
      6006,
      6007,
      2010,
      2011,
      1012
    ],
    "operations": [
      "sendAssistantMessage"
    ]
  },
  "assistant_draft_precheck": {
    "err_codes": [
      1001,
      1002,
      6002,
      6005,
      2014,
      2003,
      2004,
      2005,
      2010,
      2011,
      1012
    ],
    "operations": [
      "precheckAssistantSqlDraft"
    ]
  },
  "assistant_draft_submit_order": {
    "err_codes": [
      1001,
      1002,
      6002,
      6005,
      2001,
      2007,
      2008,
      2013,
      2014,
      1004,
      1007,
      1008
    ],
    "operations": [
      "submitAssistantSqlDraftOrder"
    ]
  },
  "diagnostics_config_update": {
    "err_codes": [
      1001,
      1002,
      1003,
      1004,
      7001,
      7003,
      7004,
      7005
    ],
    "operations": [
      "updateDatasourceDiagnosticsConfig"
    ]
  },
  "diagnostics_read": {
    "err_codes": [
      1001,
      1002,
      7001,
      7002
    ],
    "operations": [
      "getDatasourceDiagnosticsConfig",
      "getDiagnosticsOverview",
      "getDiagnosticStatementDetail"
    ]
  },
  "diagnostics_snapshot_trigger": {
    "err_codes": [
      1001,
      1002,
      7001,
      7002,
      7003,
      7004,
      7005,
      7006,
      7007,
      1007,
      1008,
      1012
    ],
    "operations": [
      "triggerManualDiagnosticsSnapshot"
    ]
  },
  "diagnostics_order_link_confirm": {
    "err_codes": [
      1001,
      1002,
      7001,
      7002,
      1004
    ],
    "operations": [
      "confirmDiagnosticOrderLink"
    ]
  },
  "diagnostics_ai_diagnose": {
    "err_codes": [
      1001,
      1002,
      7001,
      7002,
      7008,
      2003,
      2004,
      2005,
      2010,
      2011,
      1012
    ],
    "operations": [
      "diagnoseStatementWithAi"
    ]
  },
  "diagnostics_draft_precheck": {
    "err_codes": [
      1001,
      1002,
      7001,
      2014,
      2003,
      2004,
      2005,
      2010,
      2011,
      1012
    ],
    "operations": [
      "precheckDiagnosticOptimizationDraft"
    ]
  },
  "diagnostics_draft_submit_order": {
    "err_codes": [
      1001,
      1002,
      7001,
      2001,
      2007,
      2008,
      2013,
      2014,
      1004,
      1007,
      1008,
      1012
    ],
    "operations": [
      "submitDiagnosticOptimizationOrder"
    ]
  }
},
  semantics: {
    unlistedBusinessErrorIsContractViolation: true,
  },
} as const;
