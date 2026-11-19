import { HttpResponse, delay, http } from "msw";
import { BUSINESS_ERROR_CATALOG } from "@/api/generated/projections/business-error-catalog";
import {
  getGetLatestReleaseMockHandler,
  getGetLatestReleaseResponseLatestReleaseSuccessMock,
  getGetMyDashboardMockHandler,
  getGetMyDashboardResponseBusinessErrorResponseMock,
  getGetMyDashboardResponseMyDashboardSuccessMock,
} from "@/api/generated/mocks/dashboard/dashboard.msw";
import {
  getGetOperationsDashboardMockHandler,
  getGetOperationsDashboardResponseOperationsDashboardSuccessMock,
  getGetSystemHealthDashboardMockHandler,
  getGetSystemHealthDashboardResponseSystemHealthDashboardSuccessMock,
} from "@/api/generated/mocks/administration/administration.msw";
import type { BusinessErrorCode } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type { SetupWorker } from "msw/browser";
import type { SetupServer } from "msw/node";
import { type MockScenario } from "@/shared/mock/scenario-store";
import { authMockHandlers } from "@/shared/mock/auth-handlers";
import { reviewFixtureHandlers } from "@/shared/mock/review-fixture";
import { adminFixtureHandlers, siteFixtureHandlers } from "@/shared/mock/admin-fixture";
import { assistantFixtureHandlers } from "@/shared/mock/assistant-fixture";
import { diagnosticsFixtureHandlers } from "@/shared/mock/diagnostics-fixture";
import { queryFixtureHandlers } from "@/shared/mock/query-fixture";
import { sseMockHandlers } from "@/shared/mock/sse-mock";

/**
 * Scenario switching layered on top of the orval-generated base handlers
 * (code-generation-policy.json mock_layer): ready returns the typed success
 * envelope, blocked returns a declared business error envelope, running adds
 * realistic latency before success, error answers on the Problem Details path.
 * The demo operations share one deterministic request_id so assertions and UI
 * display stay stable across scenarios.
 */
export const DEMO_REQUEST_ID = "22222222-2222-4222-8222-222222222222";

// Deterministic dashboard payload (FE-F11 baseline determinism): the
// generated default handler drew every count from faker at request time,
// which churned the committed screenshot baseline on every regeneration and
// made visual review meaningless for the metric cards. One fixed,
// schema-valid dataset serves every dashboard response; refreshed_at matches
// the screenshot clock pin (tests/e2e/screenshots*.spec.ts).
const DASHBOARD_DATA = {
  data: {
    draft_count: 2,
    submitted_order_count: 3,
    pending_approval_count: 1,
    pending_execution_count: 1,
    blocked_review_count: 1,
    active_query_grant_count: 1,
    active_query_session_count: 1,
    refreshed_at: "2026-09-01T08:00:00Z",
  },
} as const;

function codeByName(name: string): BusinessErrorCode {
  for (const [code, entry] of Object.entries(BUSINESS_ERROR_CATALOG.errors)) {
    if (entry.name === name) return Number(code) as BusinessErrorCode;
  }
  throw new Error(`unknown business error name: ${name}`);
}

// Reference-image home fixtures (owner layout-alignment ruling 2026-09-02,
// RCP-20260828-DASHBOARD-REFERENCE surfaces): deterministic, schema-valid
// admin dashboard payloads anchored on the screenshot clock pin, so the
// trend chart and stat cards render byte-stable baselines. window_days is
// honored so the window selector provably round-trips through the client.
const FIXTURE_WINDOW_END = "2026-09-01";

function shiftDay(isoDate: string, deltaDays: number): string {
  return new Date(Date.parse(`${isoDate}T00:00:00Z`) + deltaDays * 86_400_000)
    .toISOString()
    .slice(0, 10);
}

function trendValue(dayIndex: number): number {
  // Deterministic pseudo-traffic with a weekly-ish rhythm — never random.
  return ((dayIndex * 7) % 11) + (dayIndex % 5 === 0 ? 3 : 0);
}

function fixtureWindow(days: number) {
  return {
    window_start: shiftDay(FIXTURE_WINDOW_END, -(days - 1)),
    window_end: FIXTURE_WINDOW_END,
    system_timezone: "UTC",
    refreshed_at: DASHBOARD_DATA.data.refreshed_at,
    completeness: "complete" as const,
    unavailable_metric_keys: [] as string[],
  };
}

/** Deterministic admin dashboards backing the reference-image home sections
 * (operations trend + stat cards, review quality, system health). */
function adminDashboardHandlers() {
  return [
    getGetOperationsDashboardMockHandler((info) => {
      const requested = Number(new URL(info.request.url).searchParams.get("window_days"));
      const days = Number.isFinite(requested) ? Math.min(Math.max(requested, 1), 90) : 30;
      return getGetOperationsDashboardResponseOperationsDashboardSuccessMock({
        request_id: DEMO_REQUEST_ID,
        data: {
          meta: fixtureWindow(days),
          change_order_total: 47,
          ddl_statement_total: 12,
          dml_statement_total: 35,
          completed_total: 41,
          failed_total: 3,
          partial_failed_total: 2,
          query_execution_total: 23981,
          user_total: 12,
          datasource_total: 5,
          approval_duration_p50_ms: 18_000,
          approval_duration_p95_ms: 96_000,
          order_trend: Array.from({ length: days }, (_, i) => ({
            day: shiftDay(FIXTURE_WINDOW_END, i - (days - 1)),
            value: trendValue(i),
          })),
          // Platform 30-day window state distribution (RCP-20261023-HOME-METRICS-ALIGNMENT):
          // all 18 keys, summing to change_order_total=47 — 41 completed +
          // 3 failed + 2 partial_failed + 1 running, derived from the same source as the 41/46 success rate.
          state_distribution: {
            submitted: 0,
            stage_approval_active: 0,
            stage_execution_pending: 0,
            scheduled: 0,
            running: 1,
            completed: 41,
            rejected: 0,
            withdrawn: 0,
            withdrawn_after_partial_execution: 0,
            voided: 0,
            failed: 3,
            partial_failed: 2,
            cancelled: 0,
            partial_cancelled: 0,
            result_unknown: 0,
            blocked_datasource_unavailable: 0,
            missed_schedule: 0,
            invalid: 0,
          },
        },
      });
    }),
    getGetSystemHealthDashboardMockHandler(
      getGetSystemHealthDashboardResponseSystemHealthDashboardSuccessMock({
        request_id: DEMO_REQUEST_ID,
        data: {
          checked_at: DASHBOARD_DATA.data.refreshed_at,
          components: (
            ["postgresql", "scheduler", "outbox", "notification", "ai_provider", "datasource"] as const
          ).map((component) => ({
            component,
            status: "healthy" as const,
            checked_at: DASHBOARD_DATA.data.refreshed_at,
          })),
        },
      }),
    ),
  ];
}

export function scenarioHandlers(scenario: MockScenario) {
  switch (scenario) {
    case "ready":
      return [
        getGetMyDashboardMockHandler(
          getGetMyDashboardResponseMyDashboardSuccessMock({
            request_id: DEMO_REQUEST_ID,
            ...DASHBOARD_DATA,
          }),
        ),
      ];
    case "blocked":
      return [
        getGetMyDashboardMockHandler(
          getGetMyDashboardResponseBusinessErrorResponseMock({
            err_code: codeByName("RESOURCE_NOT_FOUND"),
            message: "resource not found",
            data: null,
            request_id: DEMO_REQUEST_ID,
            retryable: false,
            meta: undefined,
          }),
        ),
      ];
    case "running":
      return [
        getGetMyDashboardMockHandler(async () => {
          await delay(1200);
          return getGetMyDashboardResponseMyDashboardSuccessMock({
            request_id: DEMO_REQUEST_ID,
            ...DASHBOARD_DATA,
          });
        }),
      ];
    case "error":
      // Transport failures bypass the typed envelope factories entirely and
      // answer on the Problem Details path, exactly as the real backend would.
      return [
        http.get(
          "*/dashboard/me",
          () =>
            HttpResponse.json(
              {
                type: "about:blank",
                title: "service_unavailable",
                status: 503,
                detail: "mocked transport failure",
                request_id: DEMO_REQUEST_ID,
              },
              { status: 503, headers: { "Content-Type": "application/problem+json" } },
            ),
        ),
      ];
    default:
      // review-* scenarios: no dashboard override — the stateful review
      // fixture reads its behavior from the scenario store directly.
      return [];
  }
}

// Deterministic release-snapshot fixture backing the home release banner
// (RCP-20261020-RELEASE-FEED-BACKEND): the banner reads the backend's
// /releases/latest envelope, so the mock layer mirrors that endpoint. The
// fixture's html_url deliberately stays same-origin — the browser-storage
// gates forbid absolute transport URL literals in src.
const RELEASE_SNAPSHOT_FIXTURE = {
  tag_name: "v4.1.0",
  name: "Yearning v4.1.0",
  published_at: "2026-09-01T08:00:00Z" as string | null,
  body: "工单趋势与首页服务状态优化，新增数据源TLS状态展示；修复查询导出在超宽结果下的截断问题，并提升大批量执行的稳定性。",
  html_url: "/mock/github-releases/latest/v4.1.0",
  fetched_at: "2026-10-20T00:00:00Z",
  // Equal to tag_name: the default mock world is up-to-date — no "update
  // now" tag renders and the screenshot baselines stay byte-stable. The
  // mismatch path is covered by an endpoint override in the unit tests.
  current_version: "v4.1.0" as string | null,
};

/** Base set: the deterministic dashboard handler plus the hand-written
 * authentication handlers that simulate the cookie session, the stateful
 * change-draft/review fixture backing the FE-F4 workspace, and the stateful
 * admin fixture backing the FE-F9 review-admin management pages. */
export function baseHandlers() {
  return [
    getGetMyDashboardMockHandler(
      getGetMyDashboardResponseMyDashboardSuccessMock({
        request_id: DEMO_REQUEST_ID,
        ...DASHBOARD_DATA,
      }),
    ),
    ...adminDashboardHandlers(),
    getGetLatestReleaseMockHandler(
      getGetLatestReleaseResponseLatestReleaseSuccessMock({
        request_id: DEMO_REQUEST_ID,
        data: { ...RELEASE_SNAPSHOT_FIXTURE },
      }),
    ),
    ...authMockHandlers(),
    ...reviewFixtureHandlers(),
    ...adminFixtureHandlers(),
    ...assistantFixtureHandlers(),
    ...diagnosticsFixtureHandlers(),
    ...queryFixtureHandlers(),
    ...siteFixtureHandlers(),
    ...sseMockHandlers(),
  ];
}

/** Vitest: apply a scenario as an override on the shared node server. */
export function applyScenarioToServer(server: SetupServer, scenario: MockScenario) {
  server.use(...scenarioHandlers(scenario));
}

/** Browser dev/e2e: apply a scenario as an override on the shared worker. */
export function applyScenarioToWorker(worker: SetupWorker, scenario: MockScenario) {
  worker.use(...scenarioHandlers(scenario));
}

