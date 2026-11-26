import { keepPreviousData, useMutation, useQuery } from "@tanstack/react-query";
import {
  createIdentityProvider,
  createPermissionGroup,
  createUser,
  deleteIdentityProvider,
  deletePermissionGroup,
  deleteUser,
  getUserDeletionImpact,
  listIdentityProviders,
  listPermissionGroups,
  listUsers,
  replaceIdentityProvider,
  replacePermissionGroup,
  resetUserPassword,
  testIdentityProviderConnection,
  updateUser,
} from "@/api/generated/client/administration/administration";
import type {
  CreateIdentityProviderRequest,
  CreateUserRequest,
  PermissionGroupWrite,
  ReplaceIdentityProviderRequest,
  ResetUserPasswordRequest,
  UpdateUserRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { PAGE_LIMIT, useResourceMutation } from "@/features/admin/use-admin-mutation";
import { ifMatchHeader, unwrapApi } from "@/shared/api/mutator";
import { normalizePage } from "@/shared/lib/cursor-page";

// ---- users ---------------------------------------------------------------

export function useUsers(enabled: boolean, paging?: {
  limit?: number;
  after?: string;
  q?: string;
  /** RCP-20261105 per-column filter: kind narrows by account type, ANDed with q. */
  kind?: "builtin_admin" | "local_user";
}) {
  return useQuery({
    queryKey: ["admin", "users", paging?.after ?? "", paging?.limit ?? 0, paging?.q ?? "", paging?.kind ?? ""],
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listUsers({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
            q: paging?.q || undefined,
            kind: paging?.kind,
          }),
        ),
      ),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useCreateUser() {
  return useResourceMutation({
    mutationFn: (body: CreateUserRequest) => unwrapApi(createUser(body)),
    invalidateKeys: [["admin", "users"]],
    when: "settled",
  });
}

export function useUpdateUser() {
  return useResourceMutation({
    mutationFn: (input: { userId: string; version: number; body: UpdateUserRequest }) =>
      unwrapApi(updateUser(input.userId, input.body, ifMatchHeader(input.version))),
    invalidateKeys: [["admin", "users"]],
    when: "settled",
  });
}

export function useResetUserPassword() {
  return useResourceMutation({
    mutationFn: async (input: { userId: string; body: ResetUserPasswordRequest }) => {
      await resetUserPassword(input.userId, input.body);
    },
    invalidateKeys: [["admin", "users"]],
    when: "settled",
  });
}

export function useDeleteUser() {
  return useResourceMutation({
    mutationFn: async (input: { userId: string; version: number }) => {
      await deleteUser(input.userId, ifMatchHeader(input.version));
    },
    invalidateKeys: [["admin", "users"]],
    when: "settled",
  });
}

export function useDeletionImpact(userId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["admin", "users", userId, "deletion-impact"],
    queryFn: () => unwrapApi(getUserDeletionImpact(userId)),
    enabled: enabled && userId !== "",
  });
}

// ---- permission groups ---------------------------------------------------

export function usePermissionGroups(enabled: boolean, paging?: {
  limit?: number;
  after?: string;
  q?: string;
  /** RCP-20261105 per-column filters: enabled / member / granted flow, all narrowing and ANDed together. */
  enabled?: boolean;
  memberUserId?: string;
  flowId?: string;
}) {
  return useQuery({
    queryKey: [
      "admin", "permission-groups", paging?.after ?? "", paging?.limit ?? 0, paging?.q ?? "",
      paging?.enabled === undefined ? "" : String(paging.enabled), paging?.memberUserId ?? "", paging?.flowId ?? "",
    ],
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listPermissionGroups({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
            q: paging?.q || undefined,
            enabled: paging?.enabled,
            member_user_id: paging?.memberUserId,
            flow_id: paging?.flowId,
          }),
        ),
      ),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useCreatePermissionGroup() {
  return useResourceMutation({
    mutationFn: (body: PermissionGroupWrite) => unwrapApi(createPermissionGroup(body)),
    invalidateKeys: [["admin", "permission-groups"]],
    when: "settled",
  });
}

export function useReplacePermissionGroup() {
  return useResourceMutation({
    mutationFn: (input: { groupId: string; version: number; body: PermissionGroupWrite }) =>
      unwrapApi(replacePermissionGroup(input.groupId, input.body, ifMatchHeader(input.version))),
    invalidateKeys: [["admin", "permission-groups"]],
    when: "settled",
  });
}

export function useDeletePermissionGroup() {
  return useResourceMutation({
    mutationFn: async (input: { groupId: string; version: number }) => {
      await deletePermissionGroup(input.groupId, ifMatchHeader(input.version));
    },
    invalidateKeys: [["admin", "permission-groups"]],
    when: "settled",
  });
}

// ---- identity providers ------------------------------------------------------

export function useIdentityProviders(enabled: boolean, paging?: { limit: number; after?: string }) {
  return useQuery({
    queryKey: ["admin", "identity-providers", paging?.after ?? "", paging?.limit ?? 0],
    queryFn: async () =>
      normalizePage(
        await unwrapApi(
          listIdentityProviders({
            limit: paging?.limit ?? PAGE_LIMIT,
            after: paging?.after || undefined,
          }),
        ),
      ),
    enabled,
    placeholderData: keepPreviousData,
  });
}

export function useCreateIdentityProvider() {
  return useResourceMutation({
    mutationFn: (body: CreateIdentityProviderRequest) => unwrapApi(createIdentityProvider(body)),
    invalidateKeys: [["admin", "identity-providers"]],
    when: "settled",
  });
}

export function useReplaceIdentityProvider() {
  return useResourceMutation({
    mutationFn: (input: { providerId: string; version: number; body: ReplaceIdentityProviderRequest }) =>
      unwrapApi(replaceIdentityProvider(input.providerId, input.body, ifMatchHeader(input.version))),
    invalidateKeys: [["admin", "identity-providers"]],
    when: "settled",
  });
}

export function useDeleteIdentityProvider() {
  return useResourceMutation({
    mutationFn: async (input: { providerId: string; version: number }) => {
      await deleteIdentityProvider(input.providerId, ifMatchHeader(input.version));
    },
    invalidateKeys: [["admin", "identity-providers"]],
    when: "settled",
  });
}

export function useTestIdentityProviderConnection() {
  return useMutation({
    mutationFn: (providerId: string) => unwrapApi(testIdentityProviderConnection(providerId)),
  });
}
