/**
 * Stateful browser MSW fixture backing SQL Performance Diagnostics
 * (`/api/v4/admin/diagnostics/*` and `/api/v4/admin/datasources/:id/diagnostics-config`).
 * Anchored on the shared mock catalog datasources so overview charts, drill-down
 * statement diagnostics, AI optimization reports, and change-order links all work
 * end to end in mock dev and the public pure-frontend interactive demo.
 */
export { resetDiagnosticsFixture } from "./diagnostics/state";
export { diagnosticsFixtureHandlers } from "./diagnostics/handlers";
