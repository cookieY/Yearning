import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import { apiRoute } from "@/shared/mock/api-route";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import { deletionImpactOf, userView } from "./helpers";
import {
  adminGuard,
  businessError,
  matchesQ,
  pageOf,
  searchOf,
  successEnvelope,
  versionMismatch,
  world,
} from "./state";
import type { FixturePermissionGroup, FixtureUser } from "./types";

export function adminUsersGroupsHandlers(): HttpHandler[] {
  return [
    http.get(apiRoute("/admin/users"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const q = searchOf(url);
      const kind = url.searchParams.get("kind");
      const items = [...world.users.values()]
        .filter((user) => kind === null || (kind === "builtin_admin") === user.is_builtin_admin)
        .filter((user) => matchesQ([user.username, user.display_name, user.email], q))
        .sort((a, b) => a.created_at.localeCompare(b.created_at));
      return HttpResponse.json(
        successEnvelope(pageOf(items.map(userView), url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.post(apiRoute("/admin/users"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as {
        username?: string;
        display_name?: string;
        email?: string | null;
        password?: string;
      } | null;
      if (
        body === null || typeof body.username !== "string" || body.username.trim() === "" ||
        body.username.length > 64 || typeof body.display_name !== "string" ||
        body.display_name.trim() === "" || body.display_name.length > 128 ||
        typeof body.password !== "string" || body.password.length < 12 || body.password.length > 1024 ||
        (body.email !== null && body.email !== undefined && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(body.email))
      ) {
        return businessError(1001, "validation failed");
      }
      const requestedUsername = body.username ?? "";
      const exists = [...world.users.values()].some(
        (user) => user.username.toLowerCase() === requestedUsername.toLowerCase(),
      );
      if (exists) return businessError(1001, "username already exists");
      const user: FixtureUser = {
        id: uuid(),
        username: body.username,
        display_name: body.display_name,
        email: body.email ?? null,
        is_builtin_admin: false,
        auth_provider: "local",
        version: 1,
        created_at: now(),
        updated_at: now(),
        activeOrderCount: 0,
        templateReferenceCount: 0,
      };
      world.users.set(user.id, user);
      return HttpResponse.json(successEnvelope(userView(user)));
    }),

    http.get(apiRoute("/admin/users/:userId/deletion-impact"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const user = world.users.get(String(params.userId));
      if (user === undefined) return businessError(1002, "user not found");
      return HttpResponse.json(successEnvelope(deletionImpactOf(user)));
    }),

    http.patch(apiRoute("/admin/users/:userId"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const user = world.users.get(String(params.userId));
      if (user === undefined) return businessError(1002, "user not found");
      if (versionMismatch(request, user.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as {
        display_name?: string;
        email?: string | null;
      } | null;
      if (body === null || (body.display_name === undefined && body.email === undefined)) {
        return businessError(1001, "nothing to update");
      }
      if (body.email !== null && body.email !== undefined && !/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(body.email)) {
        return businessError(1001, "email invalid");
      }
      if (body.display_name !== undefined) {
        if (body.display_name.trim() === "" || body.display_name.length > 128) {
          return businessError(1001, "display_name invalid");
        }
        user.display_name = body.display_name;
      }
      if (body.email !== undefined) user.email = body.email;
      user.version += 1;
      user.updated_at = now();
      return HttpResponse.json(successEnvelope(userView(user)));
    }),

    http.post(apiRoute("/admin/users/:userId/password"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const user = world.users.get(String(params.userId));
      if (user === undefined) return businessError(1002, "user not found");
      if (user.is_builtin_admin) return businessError(1103, "builtin admin immutable");
      if ((user.auth_provider ?? "local") !== "local") {
        return businessError(1109, "password managed by identity source");
      }
      const body = (await request.json().catch(() => null)) as {
        new_password?: unknown;
      } | null;
      if (
        body === null ||
        typeof body.new_password !== "string" ||
        body.new_password.length < 12 ||
        body.new_password.length > 1024
      ) {
        return businessError(1001, "new_password must be 12 to 1024 characters");
      }
      user.version += 1;
      user.updated_at = now();
      return HttpResponse.json(successEnvelope(null));
    }),

    http.delete(apiRoute("/admin/users/:userId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const user = world.users.get(String(params.userId));
      if (user === undefined) return businessError(1002, "user not found");
      if (user.is_builtin_admin) return businessError(1103, "builtin admin immutable");
      if (versionMismatch(request, user.version)) return businessError(1004, "version mismatch");
      if (user.activeOrderCount > 0) return businessError(1104, "user has active orders");
      if (user.templateReferenceCount > 0) {
        return businessError(1105, "user referenced by template");
      }
      world.users.delete(user.id);
      for (const group of world.groups.values()) {
        group.member_user_ids = group.member_user_ids.filter((id) => id !== user.id);
      }
      return HttpResponse.json(successEnvelope(null));
    }),

    http.get(apiRoute("/admin/permission-groups"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const enabled = url.searchParams.get("enabled");
      const memberUserId = url.searchParams.get("member_user_id");
      const flowId = url.searchParams.get("flow_id");
      const items = [...world.groups.values()]
        .filter((group) => enabled === null || (enabled === "true") === group.enabled)
        .filter((group) => memberUserId === null || group.member_user_ids.includes(memberUserId))
        .filter((group) => flowId === null || group.granted_flow_ids.includes(flowId))
        .filter((group) => matchesQ([group.name], searchOf(url)))
        .sort((a, b) => a.created_at.localeCompare(b.created_at));
      return HttpResponse.json(
        successEnvelope(pageOf(items, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.post(apiRoute("/admin/permission-groups"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as {
        name?: string;
        enabled?: boolean;
        member_user_ids?: string[];
        granted_flow_ids?: string[];
      } | null;
      if (
        body === null || typeof body.name !== "string" || body.name.trim() === "" ||
        body.name.length > 128 || typeof body.enabled !== "boolean" ||
        !Array.isArray(body.member_user_ids) || !Array.isArray(body.granted_flow_ids)
      ) {
        return businessError(1001, "validation failed");
      }
      const group: FixturePermissionGroup = {
        id: uuid(),
        name: body.name,
        enabled: body.enabled,
        member_user_ids: [...new Set(body.member_user_ids)],
        granted_flow_ids: [...new Set(body.granted_flow_ids)],
        version: 1,
        created_at: now(),
        updated_at: now(),
      };
      world.groups.set(group.id, group);
      return HttpResponse.json(successEnvelope(group));
    }),

    http.get(apiRoute("/admin/permission-groups/:groupId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const group = world.groups.get(String(params.groupId));
      if (group === undefined) return businessError(1002, "group not found");
      return HttpResponse.json(successEnvelope(group));
    }),

    http.put(apiRoute("/admin/permission-groups/:groupId"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const group = world.groups.get(String(params.groupId));
      if (group === undefined) return businessError(1002, "group not found");
      if (versionMismatch(request, group.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as {
        name?: string;
        enabled?: boolean;
        member_user_ids?: string[];
        granted_flow_ids?: string[];
      } | null;
      if (
        body === null || typeof body.name !== "string" || body.name.trim() === "" ||
        body.name.length > 128 || typeof body.enabled !== "boolean" ||
        !Array.isArray(body.member_user_ids) || !Array.isArray(body.granted_flow_ids)
      ) {
        return businessError(1001, "validation failed");
      }
      group.name = body.name;
      group.enabled = body.enabled;
      group.member_user_ids = [...new Set(body.member_user_ids)];
      group.granted_flow_ids = [...new Set(body.granted_flow_ids)];
      group.version += 1;
      group.updated_at = now();
      return HttpResponse.json(successEnvelope(group));
    }),

    http.delete(apiRoute("/admin/permission-groups/:groupId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const group = world.groups.get(String(params.groupId));
      if (group === undefined) return businessError(1002, "group not found");
      if (versionMismatch(request, group.version)) return businessError(1004, "version mismatch");
      world.groups.delete(group.id);
      return HttpResponse.json(successEnvelope(null));
    }),
  ];
}
