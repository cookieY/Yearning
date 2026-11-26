import { HttpResponse, http } from "msw";
import { apiRoute } from "@/shared/mock/api-route";
import { server } from "@/test/msw/server";
import { testEnvelope } from "@/test/envelope";

export const SESSION_USER_ID = "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac";

export interface SessionStubOptions {
  id?: string;
  username?: string;
  displayName?: string;
  email?: string | null;
  admin?: boolean;
  isBuiltinAdmin?: boolean;
  canAccessAdmin?: boolean;
  authProvider?: string;
  systemTimezone?: string;
  version?: number;
  requestId?: string;
}

export function buildSessionUser(options: SessionStubOptions = {}) {
  const admin = options.admin ?? false;
  const id = options.id ?? SESSION_USER_ID;
  const username = options.username ?? "henry";
  const displayName = options.displayName ?? username;
  return {
    id,
    username,
    display_name: displayName,
    email: options.email ?? null,
    is_builtin_admin: options.isBuiltinAdmin ?? admin,
    version: options.version ?? 1,
    created_at: "2026-08-28T08:00:00Z",
    updated_at: "2026-08-28T08:00:00Z",
    can_access_admin: options.canAccessAdmin ?? admin,
    ...(options.authProvider === undefined ? {} : { auth_provider: options.authProvider }),
    ...(options.systemTimezone === undefined ? {} : { system_timezone: options.systemTimezone }),
  };
}

export function buildSessionEnvelope(options: SessionStubOptions = {}) {
  const user = buildSessionUser(options);
  return testEnvelope(user, options.requestId ?? user.id);
}

export function stubSession(adminOrOptions: boolean | SessionStubOptions = false): void {
  const options: SessionStubOptions =
    typeof adminOrOptions === "boolean" ? { admin: adminOrOptions } : adminOrOptions;
  server.use(
    http.get(apiRoute("/users/me"), () => HttpResponse.json(buildSessionEnvelope(options))),
  );
}

export function grantSession(options: SessionStubOptions = {}): void {
  stubSession({ admin: true, ...options });
}
