// GENERATED FILE — do not edit by hand.
// Source: api/contracts/error-codes.json (hash-bound in src/api/generated/contract-manifest.json)
// Regenerate with: pnpm api:generate
export const SUCCESS_ERR_CODE: number = 0;

export const BUSINESS_HTTP_STATUS: number = 200;

export interface BusinessErrorCatalogEntry {
  name: string;
  retryability: string;
  domain: string;
}

export const BUSINESS_ERROR_CATALOG: {
  errors: Record<string, BusinessErrorCatalogEntry>;
} = {
  errors: {
  "1001": {
    "name": "VALIDATION_FAILED",
    "retryability": "never",
    "domain": "common"
  },
  "1002": {
    "name": "RESOURCE_NOT_FOUND",
    "retryability": "never",
    "domain": "common"
  },
  "1003": {
    "name": "VERSION_CONFLICT",
    "retryability": "never",
    "domain": "common"
  },
  "1004": {
    "name": "CONCURRENT_MODIFICATION",
    "retryability": "never",
    "domain": "common"
  },
  "1005": {
    "name": "RESOURCE_ALREADY_EXISTS",
    "retryability": "never",
    "domain": "common"
  },
  "1006": {
    "name": "RESOURCE_REFERENCED",
    "retryability": "never",
    "domain": "common"
  },
  "1007": {
    "name": "IDEMPOTENCY_KEY_REUSED",
    "retryability": "never",
    "domain": "common"
  },
  "1008": {
    "name": "IDEMPOTENCY_IN_PROGRESS",
    "retryability": "safe",
    "domain": "common"
  },
  "1009": {
    "name": "CURSOR_EXPIRED",
    "retryability": "never",
    "domain": "common"
  },
  "1010": {
    "name": "INVALID_STATE_TRANSITION",
    "retryability": "never",
    "domain": "common"
  },
  "1011": {
    "name": "PRECONDITION_REQUIRED",
    "retryability": "never",
    "domain": "common"
  },
  "1012": {
    "name": "SYSTEM_CAPACITY_EXHAUSTED",
    "retryability": "safe",
    "domain": "common"
  },
  "1101": {
    "name": "INVALID_CREDENTIALS",
    "retryability": "never",
    "domain": "identity"
  },
  "1102": {
    "name": "ADMIN_PASSWORD_LOCKED",
    "retryability": "never",
    "domain": "identity"
  },
  "1103": {
    "name": "ADMIN_IMMUTABLE",
    "retryability": "never",
    "domain": "identity"
  },
  "1104": {
    "name": "USER_HAS_ACTIVE_ORDERS",
    "retryability": "never",
    "domain": "identity"
  },
  "1105": {
    "name": "USER_REFERENCED_BY_TEMPLATE",
    "retryability": "never",
    "domain": "identity"
  },
  "1106": {
    "name": "FLOW_REFERENCED",
    "retryability": "never",
    "domain": "management"
  },
  "1107": {
    "name": "DATASOURCE_REFERENCED",
    "retryability": "never",
    "domain": "management"
  },
  "1108": {
    "name": "FLOW_ACTOR_NODE_EMPTY",
    "retryability": "never",
    "domain": "workflow"
  },
  "1109": {
    "name": "PASSWORD_MANAGED_BY_IDENTITY_SOURCE",
    "retryability": "never",
    "domain": "identity"
  },
  "2001": {
    "name": "REVIEW_OUTDATED",
    "retryability": "never",
    "domain": "review"
  },
  "2002": {
    "name": "SQL_BATCH_TOO_LARGE",
    "retryability": "never",
    "domain": "review"
  },
  "2003": {
    "name": "SQL_PARSE_FAILED",
    "retryability": "never",
    "domain": "review"
  },
  "2004": {
    "name": "SQL_FINGERPRINT_FAILED",
    "retryability": "never",
    "domain": "review"
  },
  "2005": {
    "name": "SQL_ANONYMIZATION_FAILED",
    "retryability": "never",
    "domain": "review"
  },
  "2006": {
    "name": "TOO_MANY_FINGERPRINTS",
    "retryability": "never",
    "domain": "review"
  },
  "2007": {
    "name": "REVIEW_COVERAGE_INCOMPLETE",
    "retryability": "never",
    "domain": "review"
  },
  "2008": {
    "name": "REVIEW_BLOCKED_BY_RISK",
    "retryability": "never",
    "domain": "review"
  },
  "2009": {
    "name": "REVIEW_SCHEMA_INVALID",
    "retryability": "never",
    "domain": "review"
  },
  "2010": {
    "name": "AI_DAILY_BUDGET_EXHAUSTED",
    "retryability": "never",
    "domain": "review"
  },
  "2011": {
    "name": "AI_PROVIDER_FAILED",
    "retryability": "safe",
    "domain": "review"
  },
  "2012": {
    "name": "REVIEW_TOOL_FAILED",
    "retryability": "contextual",
    "domain": "review"
  },
  "2013": {
    "name": "SUBMISSION_GATE_FAILED",
    "retryability": "never",
    "domain": "submission"
  },
  "2014": {
    "name": "FLOW_GRANT_REVOKED",
    "retryability": "never",
    "domain": "submission"
  },
  "3001": {
    "name": "NOT_FROZEN_ACTOR",
    "retryability": "never",
    "domain": "change_order"
  },
  "3002": {
    "name": "APPROVAL_ALREADY_DECIDED",
    "retryability": "never",
    "domain": "change_order"
  },
  "3003": {
    "name": "EXECUTION_ALREADY_STARTED",
    "retryability": "never",
    "domain": "execution"
  },
  "3004": {
    "name": "EXECUTION_RETRY_FORBIDDEN",
    "retryability": "never",
    "domain": "execution"
  },
  "3005": {
    "name": "EXECUTION_RESULT_UNKNOWN",
    "retryability": "never",
    "domain": "execution"
  },
  "3006": {
    "name": "EXECUTION_PREFLIGHT_FAILED",
    "retryability": "never",
    "domain": "execution"
  },
  "3007": {
    "name": "SCHEDULE_OUT_OF_RANGE",
    "retryability": "never",
    "domain": "execution"
  },
  "3008": {
    "name": "OUTSIDE_EXECUTION_WINDOW",
    "retryability": "never",
    "domain": "execution"
  },
  "3009": {
    "name": "MISSED_SCHEDULE",
    "retryability": "never",
    "domain": "execution"
  },
  "3010": {
    "name": "DATASOURCE_UNAVAILABLE",
    "retryability": "contextual",
    "domain": "datasource"
  },
  "3011": {
    "name": "GH_OST_UNSUPPORTED",
    "retryability": "never",
    "domain": "execution"
  },
  "3012": {
    "name": "MANUAL_VERIFICATION_EVIDENCE_REQUIRED",
    "retryability": "never",
    "domain": "execution"
  },
  "4001": {
    "name": "QUERY_GRANT_REQUIRED",
    "retryability": "never",
    "domain": "query"
  },
  "4002": {
    "name": "QUERY_DATASOURCE_NOT_GRANTED",
    "retryability": "never",
    "domain": "query"
  },
  "4003": {
    "name": "QUERY_EXPORT_NOT_GRANTED",
    "retryability": "never",
    "domain": "query"
  },
  "4004": {
    "name": "QUERY_GRANT_REVOKED",
    "retryability": "never",
    "domain": "query"
  },
  "4005": {
    "name": "QUERY_GRANT_EXPIRED",
    "retryability": "never",
    "domain": "query"
  },
  "4006": {
    "name": "QUERY_SESSION_CLOSED",
    "retryability": "never",
    "domain": "query"
  },
  "4007": {
    "name": "QUERY_ONLY_SINGLE_SELECT",
    "retryability": "never",
    "domain": "query"
  },
  "4008": {
    "name": "QUERY_TIMEOUT_OUT_OF_RANGE",
    "retryability": "never",
    "domain": "query"
  },
  "4009": {
    "name": "QUERY_TIMEOUT",
    "retryability": "never",
    "domain": "query"
  },
  "4010": {
    "name": "QUERY_CURSOR_INVALID",
    "retryability": "never",
    "domain": "query"
  },
  "4011": {
    "name": "QUERY_STATEMENT_REJECTED",
    "retryability": "never",
    "domain": "query"
  },
  "5001": {
    "name": "MIGRATION_ACTIVE_WORK_EXISTS",
    "retryability": "never",
    "domain": "migration"
  },
  "5002": {
    "name": "MIGRATION_DRY_RUN_REQUIRED",
    "retryability": "never",
    "domain": "migration"
  },
  "5003": {
    "name": "MIGRATION_MAPPING_UNCONFIRMED",
    "retryability": "never",
    "domain": "migration"
  },
  "5004": {
    "name": "MIGRATION_SOURCE_UNSUPPORTED",
    "retryability": "never",
    "domain": "migration"
  },
  "5005": {
    "name": "MIGRATION_INVARIANT_FAILED",
    "retryability": "never",
    "domain": "migration"
  },
  "6001": {
    "name": "ASSISTANT_OUT_OF_SCOPE",
    "retryability": "never",
    "domain": "assistant"
  },
  "6002": {
    "name": "ASSISTANT_DATASOURCE_FORBIDDEN",
    "retryability": "never",
    "domain": "assistant"
  },
  "6003": {
    "name": "ASSISTANT_ANONYMIZATION_FAILED",
    "retryability": "never",
    "domain": "assistant"
  },
  "6004": {
    "name": "ASSISTANT_SQL_RESTORATION_INVALID",
    "retryability": "never",
    "domain": "assistant"
  },
  "6005": {
    "name": "ASSISTANT_DRAFT_REVISION_STALE",
    "retryability": "never",
    "domain": "assistant"
  },
  "6006": {
    "name": "ASSISTANT_CONTEXT_BUDGET_EXCEEDED",
    "retryability": "never",
    "domain": "assistant"
  },
  "6007": {
    "name": "ASSISTANT_STREAM_IN_PROGRESS",
    "retryability": "safe",
    "domain": "assistant"
  },
  "7001": {
    "name": "DIAGNOSTICS_DATASOURCE_FORBIDDEN",
    "retryability": "never",
    "domain": "diagnostics"
  },
  "7002": {
    "name": "DIAGNOSTICS_NOT_ENABLED",
    "retryability": "never",
    "domain": "diagnostics"
  },
  "7003": {
    "name": "DIAGNOSTICS_ENGINE_UNSUPPORTED",
    "retryability": "never",
    "domain": "diagnostics"
  },
  "7004": {
    "name": "DIAGNOSTICS_STATS_EXTENSION_DISABLED",
    "retryability": "never",
    "domain": "diagnostics"
  },
  "7005": {
    "name": "DIAGNOSTICS_INSUFFICIENT_DB_PRIVILEGES",
    "retryability": "never",
    "domain": "diagnostics"
  },
  "7006": {
    "name": "DIAGNOSTICS_SNAPSHOT_RATE_LIMITED",
    "retryability": "safe",
    "domain": "diagnostics"
  },
  "7007": {
    "name": "DIAGNOSTICS_CIRCUIT_BREAKER_OPEN",
    "retryability": "safe",
    "domain": "diagnostics"
  },
  "7008": {
    "name": "DIAGNOSTICS_AI_TEMPLATE_UNAVAILABLE",
    "retryability": "never",
    "domain": "diagnostics"
  }
},
} as const;
