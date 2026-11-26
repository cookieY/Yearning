import type { HttpHandler } from "msw";
import { reviewApprovalHandlers } from "./review/handlers-approvals";
import { reviewDraftHandlers } from "./review/handlers-drafts";
import { reviewExecutionHandlers } from "./review/handlers-execution";
import { reviewOrderHandlers } from "./review/handlers-orders";
import { reviewRunHandlers } from "./review/handlers-runs";

/**
 * Stateful change-draft/review fixture backing FE-F4 mock development and E2E.
 */
export {
  FIXTURE_DATASOURCE_ID,
  FIXTURE_FLOW_ID,
  FIXTURE_OWNER_ID,
  FIXTURE_RAW_EVIDENCE_MARKER,
  FIXTURE_SQL_MARKER,
  type FixtureApprovalStep,
  type FixtureExecutionAttempt,
  type FixtureOrder,
  type FixtureOscDetails,
  type FixtureSchedule,
  type FixtureStatement,
  type FixtureStepState,
  type FixtureVerification,
  type ReviewBehavior,
} from "./review/types";
export {
  createMockEventTransport,
  fixtureRunIds,
  resetReviewFixture,
  seedFixtureOrder,
  seedFixtureSchedule,
} from "./review/state";
export { galleryStageState, galleryStepState } from "./review/seed-gallery";

export function reviewFixtureHandlers(): HttpHandler[] {
  return [
    ...reviewDraftHandlers(),
    ...reviewOrderHandlers(),
    ...reviewApprovalHandlers(),
    ...reviewExecutionHandlers(),
    ...reviewRunHandlers(),
  ];
}
