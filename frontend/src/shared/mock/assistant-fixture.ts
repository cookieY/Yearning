/**
 * Stateful browser MSW fixture backing the Database AI Assistant panel
 * (`/api/v4/ai-assistant/*`) for local mock development and the public
 * pure-frontend interactive demo. Supports realistic SSE streaming frames
 * (`progress`, `tool`, `reasoning`, `delta`, `done`) plus SQL draft precheck
 * and change-order handoff.
 */
export { resetAssistantFixture } from "./assistant/state";
export { assistantFixtureHandlers } from "./assistant/handlers";
