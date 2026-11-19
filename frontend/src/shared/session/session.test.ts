import { describe, expect, it } from "vitest";
import { sessionRole, sessionRoleFromCurrentUser, toSessionUser } from "./session";

const baseUser = {
  id: "0198d9cc-e65d-7b9d-a8aa-3c81945f99ac",
  username: "henry",
  email: null,
  is_builtin_admin: false,
  version: 1,
  created_at: "2026-08-28T08:00:00Z",
  updated_at: "2026-08-28T08:00:00Z",
  can_access_admin: false,
};

describe("session view mapping", () => {
  it("falls back to the username when no display name exists", () => {
    const view = toSessionUser(baseUser);
    expect(view).toEqual({ displayName: "henry", canAccessAdmin: false, migrationReview: false });
    expect(sessionRole(view)).toBe("user");
  });

  it("prefers the display name and maps the admin capability to the nav role", () => {
    const view = toSessionUser({ ...baseUser, display_name: "Henry Yee", can_access_admin: true });
    expect(view.displayName).toBe("Henry Yee");
    expect(sessionRole(view)).toBe("admin");
  });

  it("treats a missing current user as a plain user for nav filtering", () => {
    expect(sessionRoleFromCurrentUser(null)).toBe("user");
    expect(sessionRoleFromCurrentUser(baseUser)).toBe("user");
    expect(sessionRoleFromCurrentUser({ ...baseUser, can_access_admin: true })).toBe("admin");
  });
});
