import { lazy } from "react";
import { createBrowserRouter, Navigate, Outlet } from "react-router";
import { ROUTE_ALIAS_MAP } from "@/shared/lib/route-aliases";
import { BlankLayout } from "@/app/layouts/blank-layout";
import { FullLayout } from "@/app/layouts/full-layout";
import {
  RedirectIfAuthenticated,
  RequireAdminCapability,
  RequireSession,
} from "@/app/router/guards";
import { Loadable } from "@/app/router/loadable";

// Route-level code splitting is a machine gate (migration contract §8):
// every page is lazy and the admin bundle must not enter the first screen.
const LoginPage = lazy(() => import("@/routes/login/login-page"));

// The workspace home replaced the previous dashboard wholesale (owner ruling
// 2026-10-23); the /workspace/preview A/B route was retired with it.
const WorkspaceHomePage = lazy(() => import("@/routes/workspace/workspace-home-page"));
const ProfilePage = lazy(() => import("@/routes/profile/profile-page"));
const AuditRecordsPage = lazy(() => import("@/routes/records/audit-records-page"));
const QueryEntryPage = lazy(() => import("@/routes/query/query-entry-page"));
const QueryWorkspacePage = lazy(() => import("@/routes/query/query-workspace-page"));
const AdminUsersPage = lazy(() => import("@/routes/admin/admin-users-page"));
const AdminPermissionGroupsPage = lazy(() => import("@/routes/admin/admin-permission-groups-page"));
const AdminFlowsPage = lazy(() => import("@/routes/admin/admin-flows-page"));
const AdminAnnouncementsPage = lazy(() => import("@/routes/admin/admin-announcements-page"));
const AdminIdentityProvidersPage = lazy(() => import("@/routes/admin/admin-identity-providers-page"));
const AdminSettingsNamespacePage = lazy(() => import("@/routes/admin/admin-settings-page"));
const AdminDatasourcesPage = lazy(() => import("@/routes/admin/admin-datasources-page"));
const AdminDiagnosticsPage = lazy(() => import("@/routes/admin/admin-diagnostics-page"));
const AdminProvidersPage = lazy(() => import("@/routes/admin/admin-providers-page"));
const AdminAiBudgetPage = lazy(() => import("@/routes/admin/admin-ai-budget-page"));
const AdminRuleSetsPage = lazy(() => import("@/routes/admin/admin-rule-sets-page"));
const AdminReviewSkillsPage = lazy(() => import("@/routes/admin/admin-review-skills-page"));
const AdminReviewKnowledgePage = lazy(() => import("@/routes/admin/admin-review-knowledge-page"));
const ChangesNewPage = lazy(() => import("@/routes/changes/changes-new-page"));
const DraftWorkspacePage = lazy(() => import("@/routes/changes/draft-workspace-page"));
const MinePage = lazy(() => import("@/routes/changes/mine-page"));
const OrderDetailPage = lazy(() => import("@/routes/changes/order-detail-page"));
const ApprovalsWorkspacePage = lazy(() =>
  import("@/routes/approvals/approval-queue-page").then((mod) => ({
    default: mod.ApprovalsWorkspacePage,
  })),
);
const NotFoundPage = lazy(() => import("@/routes/not-found"));
const ForbiddenPage = lazy(() => import("@/routes/forbidden"));

export const appRouter = createBrowserRouter([
  {
    path: "/login",
    element: (
      <BlankLayout>
        <RedirectIfAuthenticated>{Loadable(LoginPage)}</RedirectIfAuthenticated>
      </BlankLayout>
    ),
  },
  {
    path: "/",
    element: (
      <RequireSession>
        <FullLayout />
      </RequireSession>
    ),
    children: [
      { index: true, element: <Navigate to="/workspace" replace /> },
      { path: "workspace", element: Loadable(WorkspaceHomePage) },
      {
        path: "changes/new",
        element: Loadable(ChangesNewPage),
      },
      {
        path: "changes/drafts/:draftId",
        element: Loadable(DraftWorkspacePage),
      },
      {
        path: "changes/mine",
        element: Loadable(MinePage),
      },
      {
        path: "changes/orders/:orderId",
        element: Loadable(OrderDetailPage),
      },
      {
        element: Loadable(ApprovalsWorkspacePage),
        children: [
          { path: "approvals/changes", element: null },
          { path: "approvals/query-access", element: null },
        ],
      },
      {
        // owner ruling 2026-10-31 (self-review/self-execute button split): the order detail entry from the approval queue
        // uses approval context — the detail page shows only Approve/Reject accordingly; /changes/orders/:id from My Orders
        // shows only Withdraw/Void (mutually exclusive when one user holds both roles).
        path: "approvals/orders/:orderId",
        element: Loadable(OrderDetailPage),
      },
      {
        path: "records",
        element: Loadable(AuditRecordsPage),
      },
      {
        path: "profile",
        element: Loadable(ProfilePage),
      },
      {
        path: "query",
        element: Loadable(QueryEntryPage),
      },
      {
        path: "query/sessions/:sessionId",
        element: Loadable(QueryWorkspacePage),
      },
      {
        path: "diagnostics",
        element: <Navigate to="/admin/diagnostics" replace />,
      },
      {
        // The settings family splits per namespace (migration contract §2).
        path: "admin/settings",
        element: <Navigate to="/admin/settings/general" replace />,
      },
      {
        // Preserve redirect from old AI Budget path (so bookmarks and external links do not break).
        path: "admin/settings/ai-budget",
        element: <Navigate to="/admin/review-engine/ai-budget" replace />,
      },
      {
        // Admin surfaces exist only behind the server-declared
        // `can_access_admin` capability (GET /users/me) — the guard is a
        // presentation boundary, the backend stays the authorization decision.
        element: (
          <RequireAdminCapability>
            <Outlet />
          </RequireAdminCapability>
        ),
        children: [
          { path: "admin/users", element: Loadable(AdminUsersPage) },
          { path: "admin/datasources", element: Loadable(AdminDatasourcesPage) },
          { path: "admin/diagnostics", element: Loadable(AdminDiagnosticsPage) },
          { path: "admin/review-engine/skills", element: Loadable(AdminReviewSkillsPage) },
          { path: "admin/review-engine/knowledge", element: Loadable(AdminReviewKnowledgePage) },
          { path: "admin/review-engine/providers", element: Loadable(AdminProvidersPage) },
          // AI Budget moved to Review Engine group (owner 2026-10-31: awkward to operate under System Settings);
          // backend settings namespace API is unchanged, only the page route moved.
          { path: "admin/review-engine/ai-budget", element: Loadable(AdminAiBudgetPage) },
          { path: "admin/rule-sets", element: Loadable(AdminRuleSetsPage) },
          { path: "admin/settings/:namespace", element: Loadable(AdminSettingsNamespacePage) },
          { path: "admin/permission-groups", element: Loadable(AdminPermissionGroupsPage) },
          { path: "admin/flows", element: Loadable(AdminFlowsPage) },
          { path: "admin/announcements", element: Loadable(AdminAnnouncementsPage) },
          // Legacy LDAP/OIDC settings rebuilt as the identity provider surface (migration contract §2).
          { path: "admin/identity-providers", element: Loadable(AdminIdentityProvidersPage) },
        ],
      },
      // Alias redirects for contract/help knowledge-base routes so links never 404.
      ...Object.entries(ROUTE_ALIAS_MAP).map(([alias, target]) => ({
        path: alias.slice(1),
        element: <Navigate to={target} replace />,
      })),
    ],
  },
  { path: "/403", element: Loadable(ForbiddenPage) },
  { path: "*", element: <Navigate to="/404" replace /> },
  { path: "/404", element: Loadable(NotFoundPage) },
]);
