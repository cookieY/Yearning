import type { HttpHandler } from "msw";
import { queryAccessHandlers } from "./query/handlers-access";
import { querySessionHandlers } from "./query/handlers-sessions";

/**
 * Stateful MSW fixture for the query domain (FE-F10): query flows, access
 * requests, grants, sessions, metadata, executions and cursor pages.
 */
export {
  QUERY_FIXTURE_DS_MYSQL_ID,
  QUERY_FIXTURE_DS_PG_ID,
  QUERY_FIXTURE_FLOW_ID,
  QUERY_FIXTURE_OTHER_USER_ID,
  QUERY_FIXTURE_SESSION_USER_ID,
  type QueryScenario,
} from "./query/state";
export {
  queryFlowsCatalogPage,
  resetQueryFixture,
  seedQueryScenario,
} from "./query/seed";

export function queryFixtureHandlers(): HttpHandler[] {
  return [...queryAccessHandlers(), ...querySessionHandlers()];
}
