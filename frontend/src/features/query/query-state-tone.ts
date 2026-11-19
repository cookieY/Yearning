import type { QueryAccessRequestState, QuerySessionState, QueryGrantState } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type { StatusTone } from "@/shared/lib/status-tone";

// Presentation only: the server state remains the label and permission source.
export const QUERY_REQUEST_TONE: Record<QueryAccessRequestState, StatusTone> = {
  access_pending: "violet", grant_active: "emerald", access_rejected: "red", withdrawn: "stone", invalid: "red",
};
export const QUERY_SESSION_TONE: Record<QuerySessionState, StatusTone> = {
  active: "emerald", closed: "stone", revoked: "stone", expired: "stone", user_deleted: "stone",
};
export const QUERY_GRANT_TONE: Record<QueryGrantState, StatusTone> = {
  active: "emerald", revoked: "rose", expired: "stone", relinquished: "stone",
};
