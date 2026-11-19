---
id: help-permission-model
title: Platform Role & Permission Model (Who Can Access What)
source_ref: docs/contracts/authorization-and-permission-contract.md
route_path: /dashboard
admin_only: false
keywords: permission, role, access, who can, admin, builtin_admin, user, permission group, flow, grant, authorize, separation of duties, datasource, forbidden, 6002, 4001, workspace, announcement, ldap, ldaps, starttls, oidc, sso, identity provider, login, session, 50
summary: YearningX separates Business Plane permissions (Query /query and Change Orders /orders/new, governed by Permission Groups + Flows) from Management Plane permissions (/admin/* including SQL Diagnostics /admin/diagnostics, governed solely by the builtin_admin / admin role).
---

## Steps
- Check your role: Normal Users (`user`) access `/workspace` (`/dashboard`), `/query`, `/changes/*` (`/orders/new`), `/approvals`, and `/assistant`; Administrators (`admin` / `builtin_admin`) also access all `/admin/*` pages directly.
- For `/query` and `/orders/new`, verify that your Permission Group (`/admin/permission-groups`) has been granted the corresponding `query_access` or `change_review` flow (`/admin/flows`).
- In `/assistant`, select a datasource where you have `query_access` (for `SELECT` generation) or `change_review` (for DML/DDL generation).

## Detailed Guide
- **2 Platform Roles (`user` vs. `admin` / `builtin_admin`)**:
  - **Normal User (`user`)**: Can access Personal Workspace (`/workspace`, contract `/dashboard`), Query Workspace (`/query`), Change Orders (`/changes/new` `/orders/new`, `/changes/mine`, `/changes/orders/:id`), Approvals (`/approvals`), and Database AI Assistant (`/assistant`).
  - **Administrator (`admin` / `builtin_admin`)**: Directly accesses all `/admin/*` governance modules (Users, Permission Groups, Flows, Datasources, SQL Diagnostics `/admin/diagnostics`, Review Engine, Announcements, Audit Logs, and System Settings) **without needing Permission Group or Flow grants for `/admin/*`**. The built-in administrator (`is_builtin_admin = true`) is protected from deletion or demotion.
- **How Datasource & Flow Authorization Works (Business Plane)**:
  - Users never receive raw database passwords. Instead, administrators configure **Datasources** (`/admin/datasources`), bind them to stages in **Approval Flows** (`/admin/flows`), and grant flows to users via **Permission Groups** (`/admin/permission-groups`):
    1. **Change Review Flow (`change_review`)**: Grants permission to submit DML/DDL change orders (and generate DML/DDL SQL drafts in `/assistant`) on the datasources mapped in that flow.
    2. **Query Access Flow (`query_access`)**: Lets users request or hold active **Query Sessions / Query Grants** for read-only queries (`can_query`) and optional data export (`can_export`) in `/query`.
- **Database AI Assistant (`/assistant`) Capabilities & Boundaries**:
  - Supports 3 capabilities: **Platform Usage Help (`platform_help`)**, **Natural Language SQL Generation (`sql_generate`)**, and **SQL Analysis & Optimization (`sql_optimize`)**.
  - **Read vs. Write Permission Enforcement (`6002`)**:
    - If you only hold a read-only Query Session/Grant on a datasource (without a `change_review` flow grant), `/assistant` allows generating and optimizing **`SELECT` queries only**, and rejects `INSERT` / `UPDATE` / `DELETE` / DDL generation with `6002 ASSISTANT_DATASOURCE_FORBIDDEN`.
    - If you hold `change_review` flow permission on the datasource, `/assistant` allows generating `SELECT`, DML, and reversible DDL drafts.
  - **Session Quota & Zero-Leak Privacy**: Each user can maintain up to **`50` active AI Assistant sessions** (`1012` if exceeded; deleting a session immediately erases its encrypted messages and drafts). Real table names, column names, comments, and literals are anonymized before calling the LLM and restored locally.
- **Personal Workspace (`/workspace`), Announcements, SSO Login & External `yearning` Agent Skill**:
  - **Workspace Home (`/workspace`, contract `/dashboard`)**: Displays Platform Operations metrics (`GET /admin/dashboard/operations`, where `state_distribution` and completion rate share the same 30-day window and denominator) and the 6-component System Health status dots (`GET /admin/dashboard/system-health` on the announcement card)—both readable by **all authenticated users**—alongside your personal counters, recent orders, and active announcements (`/admin/announcements`).
  - **Authentication & Enterprise SSO (Local, LDAP & OIDC)**:
    - **Configuration Entry**: Platform administrators (`builtin_admin`) configure and enable **LDAP (Singleton)** and **OIDC Providers** in **System Settings -> General (`/admin/settings/general`)** (also accessible via `/admin/identity-providers`).
    - **Login Page (`/login`) Layout**: Enabled OIDC providers are displayed as top Single Sign-On buttons (`Sign in with <Display Name>`) above the divider; when both Local Account and LDAP are enabled, the credential form displays a **`[Local Account | LDAP]` segmented tab switcher** at the top of the username/password form. Local accounts use Argon2id password hashing with consecutive failed-attempt lockout (`1008 ACCOUNT_LOCKED`).
    - **Just-In-Time (JIT) User Provisioning**: First-time LDAP or OIDC users are automatically provisioned on sign-in with the standard `user` role (`auth_provider = ldap | oidc`), and administrators grant them business permissions by assigning them to **Permission Groups** (`/admin/permission-groups`).
  - **External AI Coding Assistant Integration (`yearning` Skill)**: External AI coding assistants interact with YearningX by calling `/api/v4/*` HTTP APIs directly via the bundled **`yearning` Agent Skill** (`skills/yearning/SKILL.md` and `skills/yearning/scripts/yearning_api.py`; no standalone MCP server is used). The skill enforces the user's authenticated session, mandatory `flow_id` / `datasource_id` / `schema_name` discovery before running SQL prechecks (`precheck`) or read-only queries (`query-run`), audited SQL reveal (`order-sql`), and full approval/execution/rollback governance.
