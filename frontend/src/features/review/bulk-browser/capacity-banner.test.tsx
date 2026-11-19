import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import "@/shared/i18n";
import { CapacityBanner } from "@/features/review/bulk-browser/capacity-banner";
import { digestSqlText } from "@/features/review/bulk-import/sql-digest";

/**
 * Capacity summary and the contract complexity thresholds: the browser-side
 * estimate is labelled as such until a run exists, 200 unique fingerprints
 * trigger the warning, 1,000 the split guidance (sql-fingerprint.json).
 */

const SMALL = digestSqlText("SELECT 1;\nSELECT 2;\n");

function bulkSql(groups: number, perGroup = 2): string {
  const parts: string[] = [];
  for (let g = 0; g < groups; g += 1) {
    for (let i = 0; i < perGroup; i += 1) {
      parts.push(`INSERT INTO table_${String(g)} VALUES (${String(i)});\n`);
    }
  }
  return parts.join("");
}

describe("CapacityBanner", () => {
  it("renders the digest summary with the local-estimate label pre-review", () => {
    render(<CapacityBanner digest={SMALL} serverStatementCount={null} serverGroupCount={null} />);
    expect(screen.getByTestId("capacity-statements")).toHaveTextContent("2");
    expect(screen.getByTestId("capacity-groups")).toHaveTextContent(/1/);
    expect(screen.getByText("\uff08\u6d4f\u89c8\u5668\u7aef\u9884\u4f30\uff09")).toBeInTheDocument();
  });

  it("prefers the authoritative server counts once a run exists", () => {
    render(<CapacityBanner digest={SMALL} serverStatementCount={100000} serverGroupCount={2} />);
    expect(screen.getByTestId("capacity-statements")).toHaveTextContent("100000");
    expect(screen.getByTestId("capacity-groups")).toHaveTextContent(/^2$/);
    expect(screen.queryByText("\uff08\u6d4f\u89c8\u5668\u7aef\u9884\u4f30\uff09")).not.toBeInTheDocument();
  });

  it("labels only the groups figure as an estimate when the run lacks a group count", () => {
    render(<CapacityBanner digest={SMALL} serverStatementCount={7} serverGroupCount={null} />);
    expect(screen.getByTestId("capacity-statements")).toHaveTextContent("7");
    expect(screen.getByTestId("capacity-groups")).toHaveTextContent(/1/);
    expect(screen.getByText("\uff08\u6d4f\u89c8\u5668\u7aef\u9884\u4f30\uff09")).toBeInTheDocument();
  });

  it("shows no threshold banner at or below 200 groups", () => {
    const digest = digestSqlText(bulkSql(200));
    render(<CapacityBanner digest={digest} serverStatementCount={null} serverGroupCount={null} />);
    expect(screen.queryByTestId("bulk-complexity-warning")).not.toBeInTheDocument();
    expect(screen.queryByTestId("bulk-split-guidance")).not.toBeInTheDocument();
  });

  it("shows the complexity warning above 200 groups", () => {
    const digest = digestSqlText(bulkSql(201));
    render(<CapacityBanner digest={digest} serverStatementCount={null} serverGroupCount={null} />);
    expect(screen.getByTestId("bulk-complexity-warning")).toBeInTheDocument();
    expect(screen.queryByTestId("bulk-split-guidance")).not.toBeInTheDocument();
  });

  it("shows the split guidance above 1000 groups", () => {
    const digest = digestSqlText(bulkSql(1001));
    render(<CapacityBanner digest={digest} serverStatementCount={null} serverGroupCount={null} />);
    expect(screen.getByTestId("bulk-split-guidance")).toBeInTheDocument();
    expect(screen.queryByTestId("bulk-complexity-warning")).not.toBeInTheDocument();
  });

  it("applies the thresholds to the server count when present", () => {
    render(<CapacityBanner digest={SMALL} serverStatementCount={100000} serverGroupCount={1500} />);
    expect(screen.getByTestId("bulk-split-guidance")).toBeInTheDocument();
  });
});
