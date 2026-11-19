// GENERATED FILE — do not edit by hand.
// Source: api/contracts/state-machines.json (hash-bound in src/api/generated/contract-manifest.json)
// Regenerate with: pnpm api:generate
export interface StateMachineProjection {
  initial: string;
  states: string[];
  terminal: string[];
}

export const STATE_DISPLAY_PROJECTION: {
  machines: Record<string, StateMachineProjection>;
} = {
  machines: {
  "legacy_migration_run": {
    "initial": "planned",
    "states": [
      "planned",
      "dry_run_running",
      "awaiting_confirmation",
      "approved",
      "applying",
      "verifying",
      "verified",
      "failed"
    ],
    "terminal": [
      "verified",
      "failed"
    ]
  },
  "change_draft": {
    "initial": "draft",
    "states": [
      "draft",
      "reviewing",
      "ready",
      "blocked",
      "partial",
      "failed",
      "outdated",
      "submitted"
    ],
    "terminal": [
      "submitted"
    ]
  },
  "change_order": {
    "initial": "submitted",
    "states": [
      "submitted",
      "stage_approval_active",
      "stage_execution_pending",
      "scheduled",
      "running",
      "completed",
      "rejected",
      "withdrawn",
      "withdrawn_after_partial_execution",
      "voided",
      "failed",
      "partial_failed",
      "cancelled",
      "partial_cancelled",
      "result_unknown",
      "blocked_datasource_unavailable",
      "missed_schedule",
      "invalid"
    ],
    "terminal": [
      "completed",
      "rejected",
      "withdrawn",
      "withdrawn_after_partial_execution",
      "voided",
      "failed",
      "partial_failed",
      "cancelled",
      "partial_cancelled",
      "missed_schedule"
    ]
  },
  "query_access_request": {
    "initial": "access_pending",
    "states": [
      "access_pending",
      "grant_active",
      "access_rejected",
      "withdrawn",
      "invalid"
    ],
    "terminal": [
      "grant_active",
      "access_rejected",
      "withdrawn",
      "invalid"
    ]
  },
  "query_grant": {
    "initial": "active",
    "states": [
      "active",
      "revoked",
      "expired",
      "relinquished"
    ],
    "terminal": [
      "revoked",
      "expired",
      "relinquished"
    ]
  },
  "query_session": {
    "initial": "active",
    "states": [
      "active",
      "closed",
      "revoked",
      "expired",
      "user_deleted"
    ],
    "terminal": [
      "closed",
      "revoked",
      "expired",
      "user_deleted"
    ]
  },
  "query_session_datasource": {
    "initial": "active",
    "states": [
      "active",
      "datasource_unavailable",
      "identity_changed"
    ],
    "terminal": [
      "identity_changed"
    ]
  }
},
} as const;
