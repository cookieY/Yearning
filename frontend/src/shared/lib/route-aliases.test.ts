import { describe, expect, it } from "vitest";
import { resolveAssistantRoutePath } from "@/features/assistant/assistant-markdown";
import { ROUTE_ALIAS_MAP } from "./route-aliases";

describe("ROUTE_ALIAS_MAP", () => {
  it("maps query and approval aliases to live frontend routes rather than 404s", () => {
    expect(ROUTE_ALIAS_MAP["/approvals/queries"]).toBe("/approvals/query-access");
    expect(ROUTE_ALIAS_MAP["/query/access-requests"]).toBe("/query");
    expect(ROUTE_ALIAS_MAP["/query/grants"]).toBe("/query");

    expect(resolveAssistantRoutePath("/approvals/queries")).toBe("/approvals/query-access");
    expect(resolveAssistantRoutePath("/query/access-requests")).toBe("/query");
    expect(resolveAssistantRoutePath("/query/grants")).toBe("/query");
    expect(resolveAssistantRoutePath("/orders/ord-123")).toBe("/changes/orders/ord-123");
  });
});
