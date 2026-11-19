import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Flow } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import "@/shared/i18n";
import { StagePath } from "./stage-path";

/**
 * The stage path shows the frozen flow chain with the server-resolved
 * datasource catalog name per stage (FlowStageView datasource_name,
 * RCP-20260904-FLOW-STAGE-DATASOURCE-NAME) — the same name order stages
 * carry after submission; the frontend never resolves names from the raw
 * datasource_id.
 */

function flow(stages: number): Flow {
  return {
    id: "4f6f1a2b-0000-4000-8000-000000000001",
    name: "\u9ed8\u8ba4\u5ba1\u6838\u6d41\u7a0b",
    flow_type: "change_review",
    enabled: true,
    status: "enabled",
    stages: Array.from({ length: stages }, (_, index) => ({
      position: index + 1,
      datasource_id: "4f6f1a2b-0000-4000-8000-00000000000" + String(index + 2),
      datasource_name: "orders-mysql-" + String(index + 1),
      approval_steps: [],
      execution_actors: [],
    })),
    version: 1,
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
  };
}

describe("StagePath", () => {
  it("renders every stage position with its datasource name in order", () => {
    render(<StagePath flow={flow(3)} />);
    expect(screen.getByTestId("stage-path-1").textContent).toContain("\u9636\u6bb5 1");
    expect(screen.getByTestId("stage-path-1").textContent).toContain("orders-mysql-1");
    expect(screen.getByTestId("stage-path-2").textContent).toContain("\u9636\u6bb5 2");
    expect(screen.getByTestId("stage-path-2").textContent).toContain("orders-mysql-2");
    expect(screen.getByTestId("stage-path-3").textContent).toContain("\u9636\u6bb5 3");
    expect(screen.getByTestId("stage-path-3").textContent).toContain("orders-mysql-3");
  });

  it("explains when no flow information exists", () => {
    render(<StagePath flow={null} />);
    expect(screen.getByText("\u6682\u65e0\u6d41\u7a0b\u4fe1\u606f")).toBeInTheDocument();
  });
});
