import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { openRowActions, openRowActionsAt } from "@/test/row-actions";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { HttpResponse, http } from "msw";
import { beforeAll, beforeEach, describe, expect, it } from "vitest";
import { apiRoute } from "@/shared/mock/api-route";
import { server } from "@/test/msw/server";
import { SESSION_USER_ID, stubSession } from "@/test/session";
import {
  ADMIN_FIXTURE_USER_BLOCKED_ID,
  ADMIN_FIXTURE_USER_MEMBER_ID,
  resetAdminFixture,
} from "@/shared/mock/admin-fixture";
import { SessionProvider } from "@/features/auth/session-provider";
import "@/shared/i18n";
import AdminUsersPage from "@/routes/admin/admin-users-page";
import AdminPermissionGroupsPage from "@/routes/admin/admin-permission-groups-page";
import AdminFlowsPage from "@/routes/admin/admin-flows-page";
import AdminAnnouncementsPage from "@/routes/admin/admin-announcements-page";
import AdminMigrationsPage from "@/routes/migrations/admin-migrations-page";

/**
 * FE-F10 admin-surface component tests (popup-free per the Base UI popup
 * isolation contract — no Select popups are opened here). Coverage focuses
 * on the acceptance gates: v4 model form fields (no legacy datasource-transfer or
 * SQL/executor fields on the query-flow form), deletion impact preview, append-only
 * announcements, and the migration workbench's NO-Apply rule with the
 * APPROVE-phrase gating.
 */

function renderPage(ui: React.ReactElement): void {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <SessionProvider>
        <MemoryRouter>{ui}</MemoryRouter>
      </SessionProvider>
    </QueryClientProvider>,
  );
}

beforeAll(() => {
  Object.defineProperty(Element.prototype, "hasPointerCapture", { value: () => false, configurable: true });
  Object.defineProperty(Element.prototype, "scrollIntoView", { value: () => undefined, configurable: true });
  Object.defineProperty(Element.prototype, "releasePointerCapture", { value: () => undefined, configurable: true });
  window.localStorage.setItem("yearning-mock-auth", "admin");
});

beforeEach(() => {
  resetAdminFixture();
});

describe("gate: user deletion impact preview (P105)", () => {
  it("shows the deletion impact with blockers and disables the confirm button", async () => {
    stubSession(true);
    renderPage(<AdminUsersPage />);
    await screen.findByTestId("admin-users-table");
    await openRowActions(`admin-user-actions-${ADMIN_FIXTURE_USER_BLOCKED_ID}`);
    await userEvent.click(screen.getByTestId(`admin-user-delete-${ADMIN_FIXTURE_USER_BLOCKED_ID}`));
    const impact = await screen.findByTestId("admin-user-delete-impact");
    // §18.54: blocker codes render as translated sentences, not raw keys.
    expect(impact).toHaveTextContent("\u4ecd\u662f\u672a\u7ec8\u7ed3\u5de5\u5355\u7684\u63d0\u4ea4\u4eba");
    expect(impact).toHaveTextContent("\u6d41\u7a0b\u6a21\u677fActor\u5f15\u7528");
    expect(impact).not.toHaveTextContent("active_orders");
    expect(screen.getByTestId("admin-user-delete-confirm")).toBeDisabled();
  });

  it("creates a user only after the initial password is confirmed (§18.54)", async () => {
    stubSession(true);
    renderPage(<AdminUsersPage />);
    await screen.findByTestId("admin-users-table");
    await userEvent.click(screen.getByTestId("admin-users-create"));
    const submit = await screen.findByTestId("user-create-submit");
    expect(submit).toBeDisabled();

    await userEvent.type(screen.getByTestId("user-create-username"), "carol");
    await userEvent.type(screen.getByTestId("user-create-display"), "Carol Wu");
    await userEvent.type(screen.getByTestId("user-create-password"), "carol-password-123");
    // Confirm field empty → still disabled; mismatched → still disabled with the note.
    expect(submit).toBeDisabled();
    await userEvent.type(screen.getByTestId("user-create-confirm-password"), "different-fixture");
    expect(await screen.findByTestId("user-create-confirm-mismatch")).toBeVisible();
    expect(submit).toBeDisabled();

    await userEvent.clear(screen.getByTestId("user-create-confirm-password"));
    await userEvent.type(screen.getByTestId("user-create-confirm-password"), "carol-password-123");
    expect(screen.queryByTestId("user-create-confirm-mismatch")).toBeNull();
    expect(submit).toBeEnabled();

    // §18.54: a malformed email shows the format note and blocks submit.
    await userEvent.type(screen.getByTestId("user-create-email"), "not-an-email");
    expect(await screen.findByTestId("user-create-email-invalid")).toBeVisible();
    expect(submit).toBeDisabled();
    await userEvent.clear(screen.getByTestId("user-create-email"));
    await userEvent.type(screen.getByTestId("user-create-email"), "carol@yearning.test");
    expect(screen.queryByTestId("user-create-email-invalid")).toBeNull();
    expect(submit).toBeEnabled();

    await userEvent.click(submit);
    // The mock create handler succeeds; the dialog closes back to the table.
    await screen.findByTestId("admin-users-table");
    expect(screen.queryByTestId("user-create-submit")).toBeNull();
  });

  it("renders no role or department management anywhere (P102)", async () => {
    stubSession(true);
    renderPage(<AdminUsersPage />);
    await screen.findByTestId("admin-users-table");
    const headers = screen.getByTestId("admin-users-table").textContent;
    expect(headers).not.toContain("\u90e8\u95e8");
    expect(headers).not.toContain("\u5ba1\u8ba1\u4eba");
    expect(screen.getByTestId("admin-users-table").textContent).not.toContain("\u89d2\u8272");
  });

  it("opens edit profile from username click, validates and resets local user password, and updates version", async () => {
    const capturedResetBodies: Array<Record<string, unknown>> = [];
    const capturedPatchIfMatch: Array<string | null> = [];
    const onRequest = async ({ request }: { request: Request }) => {
      if (request.method === "POST" && request.url.endsWith(`/admin/users/${ADMIN_FIXTURE_USER_MEMBER_ID}/password`)) {
        capturedResetBodies.push((await request.clone().json()) as Record<string, unknown>);
      }
      if (request.method === "PATCH" && request.url.endsWith(`/admin/users/${ADMIN_FIXTURE_USER_MEMBER_ID}`)) {
        capturedPatchIfMatch.push(request.headers.get("If-Match"));
      }
    };
    server.events.on("request:start", onRequest);

    try {
      stubSession(true);
      renderPage(<AdminUsersPage />);
      await screen.findByTestId("admin-users-table");
      await userEvent.click(screen.getByTestId(`admin-user-profile-${ADMIN_FIXTURE_USER_MEMBER_ID}`));
      await screen.findByTestId("admin-user-edit-dialog");

      const footer = screen.getByTestId("admin-user-edit-footer");
      const resetBtn = within(footer).getByTestId("user-edit-reset-password-submit");
      expect(within(footer).getByTestId("user-edit-submit")).toBeVisible();
      expect(within(screen.getByTestId("user-edit-password-section")).queryByTestId("user-edit-reset-password-submit")).toBeNull();
      expect(resetBtn).toBeDisabled();

      // Short password shows inline min-length validation warning and keeps button disabled.
      await userEvent.type(screen.getByTestId("user-edit-new-password"), "short-pw");
      await userEvent.type(screen.getByTestId("user-edit-confirm-password"), "short-pw");
      expect(screen.getByTestId("user-edit-new-password")).toHaveAttribute("aria-invalid", "true");
      expect(await screen.findByTestId("user-edit-password-too-short")).toHaveTextContent("8/12");
      expect(resetBtn).toBeDisabled();

      // Mismatch shows hint and keeps button disabled; 12+ char clears short warning.
      await userEvent.clear(screen.getByTestId("user-edit-new-password"));
      await userEvent.clear(screen.getByTestId("user-edit-confirm-password"));
      await userEvent.type(screen.getByTestId("user-edit-new-password"), "ResetPassword-123");
      await userEvent.type(screen.getByTestId("user-edit-confirm-password"), "MismatchPassword-1");
      expect(screen.queryByTestId("user-edit-password-too-short")).toBeNull();
      expect(await screen.findByTestId("user-edit-password-mismatch")).toBeVisible();
      expect(resetBtn).toBeDisabled();

      // Valid matching password enables reset, sends POST, clears inputs, and advances currentVersion.
      await userEvent.clear(screen.getByTestId("user-edit-confirm-password"));
      await userEvent.type(screen.getByTestId("user-edit-confirm-password"), "ResetPassword-123");
      expect(resetBtn).toBeEnabled();
      await userEvent.click(resetBtn);

      await waitFor(() => {
        expect(capturedResetBodies).toEqual([{ new_password: "ResetPassword-123" }]);
        expect(screen.getByTestId("user-edit-new-password")).toHaveValue("");
        expect(screen.getByTestId("user-edit-confirm-password")).toHaveValue("");
      });

      // Subsequent profile save uses bumped version=2 without 1004 conflict.
      await userEvent.clear(screen.getByTestId("user-edit-display"));
      await userEvent.type(screen.getByTestId("user-edit-display"), "Anne Updated");
      await userEvent.click(screen.getByTestId("user-edit-submit"));
      await waitFor(() => {
        expect(capturedPatchIfMatch).toEqual(['"2"']);
        expect(screen.queryByTestId("admin-user-edit-dialog")).toBeNull();
      });
    } finally {
      server.events.removeListener("request:start", onRequest);
    }
  });

  it("shows protected password notices for builtin admin and external identity users", async () => {
    stubSession(true);
    renderPage(<AdminUsersPage />);
    await screen.findByTestId("admin-users-table");

    await userEvent.click(screen.getByRole("button", { name: "admin" }));
    await screen.findByTestId("admin-user-edit-dialog");
    expect(screen.getByTestId("user-edit-password-builtin-hint")).toBeVisible();
    expect(screen.queryByTestId("user-edit-new-password")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "\u53d6\u6d88" }));

    const externalId = "7a1a3c4d-1111-4111-8111-00000000e199";
    server.use(
      http.get(apiRoute("/admin/users"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: {
            items: [
              {
                id: externalId,
                username: "oidc-alice",
                display_name: "Alice OIDC",
                email: "alice@example.com",
                role: "user",
                auth_provider: "oidc",
                is_builtin_admin: false,
                version: 1,
                created_at: "2026-08-28T08:00:00Z",
                updated_at: "2026-08-28T08:00:00Z",
              },
            ],
            next_cursor: null,
          },
          request_id: SESSION_USER_ID,
        }),
      ),
    );

    // Re-render to observe external identity user.
    renderPage(<AdminUsersPage />);
    await userEvent.click(await screen.findByTestId(`admin-user-profile-${externalId}`));
    await screen.findByTestId("admin-user-edit-dialog");
    expect(screen.getByTestId("user-edit-password-external-hint")).toBeVisible();
    expect(screen.queryByTestId("user-edit-new-password")).toBeNull();
  });
});

describe("gate: permission-group form matches the v4 model (P101)", () => {
  it("edits a group with members and flow grants only — no datasource transfers", async () => {
    stubSession(true);
    renderPage(<AdminPermissionGroupsPage />);
    const row = await screen.findByTestId("admin-group-row-7a1a3c4d-2222-4222-8222-00000000b101");
    await openRowActionsAt(within(row).getByTestId(/admin-group-actions-/));
    await userEvent.click(screen.getByTestId(/admin-group-edit-/));
    const memberCheckboxes = await screen.findAllByTestId(/^group-member-[0-9a-f]{8}-/);
    expect(memberCheckboxes.length).toBeGreaterThan(0);
    // The legacy ddl/dml/query transfer vocabulary must not appear.
    expect(screen.queryByText(/DDL \u6570\u636e\u6e90\u6743\u9650/)).not.toBeInTheDocument();
    // The deleted legacy transfer field never appears (name assembled so
    // this source file itself stays clean for the prohibited scanner).
    expect(document.body.textContent).not.toContain(["ddl", "_source"].join(""));

    // §18.46 (same dialog instance — popup isolation contract): the member
    // list narrows by name/username and the filtered select-all unions into
    // the selection without touching hidden rows.
    await userEvent.type(screen.getByTestId("group-member-search"), "busy");
    expect(await screen.findAllByTestId(/^group-member-[0-9a-f]{8}-/)).toHaveLength(1);
    expect(screen.getByTestId("group-member-select-all-count").textContent).toBe("0/1");
    await userEvent.click(screen.getByTestId("group-member-select-all"));
    expect(screen.getByTestId("group-member-select-all-count").textContent).toBe("1/1");
    await userEvent.click(screen.getByTestId("group-member-search-clear"));
    // Anne (pre-selected before the search) keeps her state — the union
    // semantics never unselect rows the filter hid.
    expect(screen.getByTestId("group-member-7a1a3c4d-1111-4111-8111-00000000e101").getAttribute("aria-checked")).toBe("true");
    expect(screen.getByTestId("group-member-7a1a3c4d-1111-4111-8111-00000000e102").getAttribute("aria-checked")).toBe("true");

    // The flow list searches by name and its select-all flips the whole
    // filtered set (the order keyword matches several seeded catalog flows).
    await userEvent.type(screen.getByTestId("group-flow-search"), "\u8ba2\u5355");
    const visibleFlows = await screen.findAllByTestId(/^group-flow-[0-9a-f]{8}-/);
    expect(visibleFlows.length).toBeGreaterThan(1);
    const total = visibleFlows.length;
    await userEvent.click(screen.getByTestId("group-flow-select-all"));
    expect(screen.getByTestId("group-flow-select-all-count").textContent).toBe(`${String(total)}/${String(total)}`);
    await userEvent.click(screen.getByTestId("group-flow-select-all"));
    expect(screen.getByTestId("group-flow-select-all-count").textContent).toBe(`0/${String(total)}`);
  });
});

describe("gate: dual flow forms match the v4 model (§3)", () => {
  it("keeps SQL, rule sets and executors OUT of the query-flow form", async () => {
    stubSession(true);
    renderPage(<AdminFlowsPage />);
    await screen.findByTestId("admin-flows-table");
    await userEvent.click(screen.getByTestId("admin-flows-create-query"));
    const capabilityCheckboxes = await screen.findAllByTestId(/flow-query-ds-/);
    expect(capabilityCheckboxes.length).toBeGreaterThan(0);
    const text = screen.getByRole("dialog").textContent;
    expect(text).not.toContain("SQL");
    expect(text).not.toContain("Rule Set");
    expect(text).not.toContain("\u6267\u884c\u4eba");
    // The "does not affect submitted orders" note is present (§3).
    expect(text).toContain("\u4e0d\u4f1a\u5f71\u54cd\u5df2\u63d0\u4ea4\u5de5\u5355");
  });

  it("exposes the sensitive-column vocabulary on the query-flow editor (Q006)", async () => {
    stubSession(true);
    renderPage(<AdminFlowsPage />);
    await screen.findByTestId("admin-flows-table");
    const queryRow = screen.getByTestId("admin-flow-row-7a1a3c4d-3333-4333-8333-00000000f002");
    await openRowActionsAt(within(queryRow).getByTestId(/admin-flow-actions-/));
    await userEvent.click(screen.getByTestId(/admin-flow-edit-/));
    const masking = await screen.findByTestId("flow-masking-rules");
    expect(masking).toHaveTextContent("\u654f\u611f\u5b57\u6bb5\u8bcd\u8868");
    const ruleInput = await within(masking).findAllByTestId(/flow-masking-rule-/);
    expect(ruleInput.some((input) => (input as HTMLTextAreaElement).value === "email, phone")).toBe(true);
  });

  // §18.64: every approval-step card and the execution-actor picker carry a
  // tri-state master toggle (all -> clear, partial -> mixed) mirroring the
  // permission-groups select-all.
  it("toggles all actors per approval step and for execution actors (§18.64)", async () => {
    stubSession(true);
    renderPage(<AdminFlowsPage />);
    await screen.findByTestId("admin-flows-table");
    await userEvent.click(screen.getByTestId("admin-flows-create-change"));
    const dialog = await screen.findByRole("dialog");
    // A fresh change flow starts with zero stages — add the first one.
    await userEvent.click(within(dialog).getByTestId("flow-add-stage"));

    const stepAll = await within(dialog).findByTestId("flow-stage-0-step-0-select-all");
    const chips = await within(dialog).findAllByTestId(/^flow-stage-0-step-0-actor-/);
    expect(chips.length).toBeGreaterThan(1);
    expect(stepAll.getAttribute("aria-checked")).toBe("false");

    await userEvent.click(stepAll);
    for (const chip of chips) {
      expect(chip.getAttribute("aria-checked")).toBe("true");
    }
    expect(within(dialog).getByTestId("flow-stage-0-step-0-select-all-count").textContent)
      .toBe(`${String(chips.length)}/${String(chips.length)}`);
    // §18.70: fully selected indicator carries data-checked (checkmark).
    const fullIndicator = stepAll.querySelector("[data-slot=checkbox-indicator]");
    expect(fullIndicator?.hasAttribute("data-checked")).toBe(true);

    // Dropping one chip moves the master checkbox to the mixed state.
    const [firstChip] = chips;
    if (firstChip === undefined) throw new Error("expected at least one actor chip");
    await userEvent.click(firstChip);
    expect(stepAll.getAttribute("aria-checked")).toBe("mixed");
    // §18.70: mixed indicator carries data-indeterminate (minus icon visibility is CSS-driven,
    // and jsdom has no layout/styles, so assert the state attribute rather than icon DOM).
    const mixedIndicator = stepAll.querySelector("[data-slot=checkbox-indicator]");
    expect(mixedIndicator?.hasAttribute("data-indeterminate")).toBe(true);
    expect(mixedIndicator?.hasAttribute("data-checked")).toBe(false);

    const execAll = within(dialog).getByTestId("flow-stage-0-exec-select-all");
    await userEvent.click(execAll);
    const execChips = within(dialog).getAllByTestId(/^flow-stage-0-exec-actor-/);
    for (const chip of execChips) {
      expect(chip.getAttribute("aria-checked")).toBe("true");
    }
    expect(within(dialog).getByTestId("flow-stage-0-exec-select-all-count").textContent)
      .toBe(`${String(execChips.length)}/${String(execChips.length)}`);
  });

  it("filters step actor capsules by search without changing selections (2026-10-31)", async () => {
    stubSession(true);
    renderPage(<AdminFlowsPage />);
    await screen.findByTestId("admin-flows-table");
    await userEvent.click(screen.getByTestId("admin-flows-create-change"));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByTestId("flow-add-stage"));
    const search = await within(dialog).findByTestId("flow-stage-0-step-0-search");
    const total = Number(
      within(dialog).getByTestId("flow-stage-0-step-0-select-all-count").textContent.split("/")[1] ?? "0",
    );
    expect(total).toBeGreaterThan(1);

    // Search filters capsules: "an" is a real substring of fixture names (Anne Zhou / Administrator, etc.),
    // matching a proper subset.
    await userEvent.type(search, "an");
    const filtered = within(dialog).getAllByTestId(/^flow-stage-0-step-0-actor-/);
    expect(filtered.length).toBeGreaterThan(0);
    expect(filtered.length).toBeLessThan(total);
    const firstChip = filtered[0] as HTMLElement;

    // Filtering only hides capsules without altering selection: check one of the filtered users.
    await userEvent.click(firstChip);
    expect(firstChip.getAttribute("aria-checked")).toBe("true");
    const clickedTestid = firstChip.getAttribute("data-testid");
    expect(clickedTestid).toBeTruthy();

    // Clear search: all capsules return, and the user checked earlier remains selected.
    await userEvent.clear(search);
    const all = within(dialog).getAllByTestId(/^flow-stage-0-step-0-actor-/);
    expect(all).toHaveLength(total);
    expect(within(dialog).getByTestId(clickedTestid as string).getAttribute("aria-checked")).toBe("true");

    // No match: render placeholder hint and zero capsules.
    await userEvent.type(search, "zzz\u65e0\u5339\u914d");
    expect(within(dialog).getByTestId("flow-stage-0-step-0")).toHaveTextContent("\u65e0\u5339\u914d\u7528\u6237");
    expect(within(dialog).queryAllByTestId(/^flow-stage-0-step-0-actor-/)).toHaveLength(0);
  });

  it("filters query-flow datasources by search without losing selections", async () => {
    stubSession(true);
    renderPage(<AdminFlowsPage />);
    await screen.findByTestId("admin-flows-table");
    await userEvent.click(screen.getByTestId("admin-flows-create-query"));
    const dialog = await screen.findByRole("dialog");
    const dsSearch = await within(dialog).findByTestId("flow-query-ds-search");
    const allItems = await within(dialog).findAllByTestId(/^flow-query-ds-[0-9a-f]/);
    expect(allItems.length).toBeGreaterThan(1);

    const firstCheckbox = allItems[0] as HTMLElement;
    await userEvent.click(firstCheckbox);
    expect(firstCheckbox.getAttribute("aria-checked")).toBe("true");
    const firstTestId = firstCheckbox.getAttribute("data-testid") ?? "";

    await userEvent.type(dsSearch, "zzz-no-match");
    expect(within(dialog).queryAllByTestId(/^flow-query-ds-[0-9a-f]/)).toHaveLength(0);

    await userEvent.clear(dsSearch);
    expect(within(dialog).getByTestId(firstTestId).getAttribute("aria-checked")).toBe("true");
  });

  it("requires a rule set on change-review flows and warns when no rule sets exist", async () => {
    server.use(
      http.get(apiRoute("/admin/rule-sets"), () =>
        HttpResponse.json({
          err_code: 0,
          message: "ok",
          data: { items: [], next_cursor: null },
          request_id: "empty-rule-sets",
        }),
      ),
    );
    stubSession(true);
    renderPage(<AdminFlowsPage />);
    await screen.findByTestId("admin-flows-table");
    await userEvent.click(screen.getByTestId("admin-flows-create-change"));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByTestId("flow-rule-set-empty-warning")).toBeInTheDocument();
    expect(within(dialog).getByTestId("flow-go-to-rule-sets")).toBeInTheDocument();
    expect(within(dialog).getByTestId("flow-form-submit")).toBeDisabled();
    expect(within(dialog).getByTestId("flow-form-invalid-hint")).toHaveTextContent("Rule Set");
  });
});

describe("gate: append-only announcements and single published pointer (S005)", () => {
  it("creates a revision and moves the publication pointer", async () => {
    stubSession(true);
    renderPage(<AdminAnnouncementsPage />);
    await screen.findByTestId("admin-announcements-table");
    await userEvent.click(screen.getByTestId("admin-announcements-create"));
    await userEvent.type(screen.getByTestId("announcement-title"), "\u7a97\u53e3\u53d8\u66f4");
    await userEvent.type(screen.getByTestId("announcement-markdown"), "# \u65b0\u7a97\u53e3\n\n**\u5468\u65e5** 02:00-04:00\u3002");
    await userEvent.click(screen.getByTestId("announcement-create-submit"));
    await waitFor(() => {
      expect(screen.getByTestId("admin-announcements-table").textContent).toContain("\u7a97\u53e3\u53d8\u66f4");
    });
  });
});

describe("gate: Migration UI cannot start Apply (M001)", () => {
  it("renders reconciliation, candidates and phrase-gated approval with no Apply entry", async () => {
    stubSession(true);
    renderPage(<AdminMigrationsPage />);
    await screen.findByTestId("migration-table-results");
    expect(screen.getByTestId("migration-no-apply-note")).toHaveTextContent("\u6279\u51c6\u4e0d\u4f1a\u542f\u52a8Apply");

    // Every button on the page: confirm/approve only — never Start Apply.
    const buttons = screen.getAllByRole("button");
    for (const button of buttons) {
      expect(button.textContent).not.toMatch(/\u5f00\u59cb\s*Apply|\u542f\u52a8\s*Apply|Start\s*Apply/);
    }

    // Phrase gating: the approve button stays disabled until the exact
    // APPROVE <run_uuid> phrase and all candidate confirmations exist.
    const approve = screen.getByTestId("migration-approve");
    expect(approve).toBeDisabled();
    const candidates = await screen.findAllByTestId(/migration-candidate-confirm-/);
    for (const candidate of candidates) {
      await userEvent.click(candidate);
    }
    await waitFor(() => expect(screen.getByTestId("migration-approve")).toBeDisabled());
    const phraseInput = screen.getByTestId("migration-phrase");
    await userEvent.type(phraseInput, "APPROVE wrong-phrase");
    expect(screen.getByTestId("migration-approve")).toBeDisabled();
  });
});

describe("migration-mode router swap (§8)", () => {
  it("normal mode never registers /admin/migrations; migration mode registers only its three surfaces", async () => {
    const { appRouter } = await import("@/app/router/router");
    const normalPaths = JSON.stringify(appRouter.routes);
    expect(normalPaths).not.toContain("admin/migrations");

    const { buildMigrationRouter } = await import("@/app/router/migration-router");
    const migrationPaths = JSON.stringify(buildMigrationRouter().routes);
    expect(migrationPaths).toContain("admin/migrations");
    expect(migrationPaths).toContain("login");
    expect(migrationPaths).not.toContain("changes/mine");

    // Session-level detection: the migration me payload carries the
    // capability; the normal payload does not.
    const { toSessionUser } = await import("@/shared/session/session");
    const normal = toSessionUser({
      id: SESSION_USER_ID,
      username: "henry",
      is_builtin_admin: false,
      version: 1,
      created_at: "2026-08-28T08:00:00Z",
      updated_at: "2026-08-28T08:00:00Z",
      can_access_admin: false,
    });
    expect(normal.migrationReview).toBe(false);
    const migration = toSessionUser({
      id: SESSION_USER_ID,
      username: "admin",
      is_builtin_admin: true,
      version: 1,
      created_at: "2026-08-28T08:00:00Z",
      updated_at: "2026-08-28T08:00:00Z",
      can_access_admin: true,
      // The migration review server's me view (divergence recorded §17).
      capabilities: ["migration_review"],
    } as never);
    expect(migration.migrationReview).toBe(true);
      });
});
