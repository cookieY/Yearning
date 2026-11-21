/**
 * Canonical mapping from contract/help-doc alias paths to registered frontend routes.
 */
export const ROUTE_ALIAS_MAP: Record<string, string> = {
  "/dashboard": "/workspace",
  "/orders/new": "/changes/new",
  "/orders": "/changes/mine",
  "/changes/orders": "/changes/mine",
  "/approvals": "/approvals/changes",
  "/approvals/queries": "/approvals/query-access",
  "/query/access-requests": "/query",
  "/query/grants": "/query",
  "/admin/review-engine": "/admin/review-engine/skills",
  "/admin/review-engine/rules": "/admin/review-engine/skills",
  "/admin/review-engine/rule-sets": "/admin/rule-sets",
  "/admin/audit": "/records",
  "/admin/audit-logs": "/records",
  "/admin/notifications": "/admin/settings/notifications",
};
