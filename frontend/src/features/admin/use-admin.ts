/**
 * Barrel module re-exporting all admin server-state hooks and types across
 * domain hook modules (datasources, AI providers/tools/pricing, review
 * inputs, identity/access, and site/system administration).
 */

export {
  type ResourceMutationOptions,
  useResourceMutation,
} from "@/features/admin/use-admin-mutation";
export * from "@/features/admin/use-admin-datasources";
export * from "@/features/admin/use-admin-ai";
export * from "@/features/admin/use-admin-review";
export * from "@/features/admin/use-admin-identity";
export * from "@/features/admin/use-admin-system";
