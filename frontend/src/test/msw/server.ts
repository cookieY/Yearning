import { setupServer } from "msw/node";
import { baseHandlers } from "@/shared/mock/scenarios";

// Generated MSW base handlers (orval mocks output) registered as the base set;
// scenario overrides are layered on top per code-generation-policy.json
// (mock_layer: msw shared by vitest and playwright).
export const server = setupServer(...baseHandlers());
