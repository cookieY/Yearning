import {
  Activity,
  BookOpen,
  CircleDollarSign,
  Database,
  FilePlus2,
  FileStack,
  House,
  KeyRound,
  Layers,
  Megaphone,
  Puzzle,
  ScrollText,
  Settings,
  ShieldCheck,
  Stamp,
  Terminal,
  Users,
  Workflow,
  type LucideIcon,
} from "lucide-react";

/**
 * First-level navigation information architecture. Sole authority:
 * docs/frontend/yearning-ui-design-spec.md §5.1 (2026-08-26 product decision);
 * routes follow docs/contracts/frontend-ui-migration-contract.md §2.
 *
 * Presentation-layer visibility is NOT an authorization boundary — the backend
 * remains default-deny. Fine-grained per-capability filtering (frozen-flow
 * approvers, query grants, audit visibility) consumes real server
 * capabilities from FE-F3 on; until then only the two admin-only groups are
 * filtered here.
 */
export type NavVisibility = "all" | "admin";

export interface NavItem {
  /** i18n key under nav.* */
  labelKey: string;
  to: string;
  icon: LucideIcon;
  visibility: NavVisibility;
}

export interface NavGroup {
  /** i18n key under nav.group.* */
  labelKey: string;
  items: NavItem[];
}

export const NAV_GROUPS: NavGroup[] = [
  {
    labelKey: "nav.group.workspace",
    items: [
      { labelKey: "nav.home", to: "/workspace", icon: House, visibility: "all" },
      { labelKey: "nav.myOrders", to: "/changes/mine", icon: FileStack, visibility: "all" },
      { labelKey: "nav.orderSubmit", to: "/changes/new", icon: FilePlus2, visibility: "all" },
      { labelKey: "nav.orderApprovals", to: "/approvals/changes", icon: Stamp, visibility: "all" },
    ],
  },
  {
    labelKey: "nav.group.audit",
    items: [
      { labelKey: "nav.auditRecords", to: "/records", icon: ScrollText, visibility: "all" },
    ],
  },
  {
    labelKey: "nav.group.query",
    items: [
      { labelKey: "nav.query", to: "/query", icon: Terminal, visibility: "all" },
    ],
  },
  {
    labelKey: "nav.group.admin",
    items: [
      { labelKey: "nav.admin.diagnostics", to: "/admin/diagnostics", icon: Activity, visibility: "admin" },
      { labelKey: "nav.admin.users", to: "/admin/users", icon: Users, visibility: "admin" },
      { labelKey: "nav.admin.datasources", to: "/admin/datasources", icon: Database, visibility: "admin" },
      { labelKey: "nav.admin.flows", to: "/admin/flows", icon: Workflow, visibility: "admin" },
      { labelKey: "nav.admin.permissionGroups", to: "/admin/permission-groups", icon: ShieldCheck, visibility: "admin" },
      { labelKey: "nav.admin.announcements", to: "/admin/announcements", icon: Megaphone, visibility: "admin" },
      { labelKey: "nav.admin.settings", to: "/admin/settings", icon: Settings, visibility: "admin" },
    ],
  },
  {
    labelKey: "nav.group.reviewEngine",
    items: [
      { labelKey: "nav.reviewEngine.providers", to: "/admin/review-engine/providers", icon: KeyRound, visibility: "admin" },
      { labelKey: "nav.reviewEngine.skills", to: "/admin/review-engine/skills", icon: Puzzle, visibility: "admin" },
      { labelKey: "nav.reviewEngine.knowledge", to: "/admin/review-engine/knowledge", icon: BookOpen, visibility: "admin" },
      // AI Budget moved from System Settings (owner 2026-10-31) — cost control sits next to the Provider domain.
      { labelKey: "nav.reviewEngine.aiBudget", to: "/admin/review-engine/ai-budget", icon: CircleDollarSign, visibility: "admin" },
      { labelKey: "nav.reviewEngine.ruleSets", to: "/admin/rule-sets", icon: Layers, visibility: "admin" },
    ],
  },
];

export type SessionRole = "admin" | "user";

export function filterNavGroups(groups: NavGroup[], role: SessionRole): NavGroup[] {
  return groups
    .map((group) => ({
      ...group,
      items: group.items.filter((item) => item.visibility === "all" || role === "admin"),
    }))
    // A group whose every entry is filtered out does not render its heading.
    .filter((group) => group.items.length > 0);
}
