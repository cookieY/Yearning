import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ReviewFinding } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import "@/shared/i18n";
import { FindingList } from "./finding-list";

/**
 * The finding list is the structured AI output surface: severity filter,
 * per-finding context (stage, category), suggestion, evidence affordance and
 * the quote-based SQL locate. Only contract fields render — the gate
 * "do not display chain-of-thought" means no reasoning channel exists at all.
 */

const EVIDENCE_ID = "4f6f1a2b-0000-4000-8000-00000000be01";

function finding(overrides: Partial<ReviewFinding> = {}): ReviewFinding {
  return {
    id: "4f6f1a2b-0000-4000-8000-00000000f101",
    stage_position: 1,
    fingerprint_group_id: null,
    category: "performance",
    severity: "medium",
    title: "Missing index",
    message: "Statement `UPDATE orders SET status = 1` cannot hit an index.",
    suggestion: "Add an index.",
    model_confidence: 0.9,
    evidence_ids: [EVIDENCE_ID],
    ...overrides,
  };
}

const FINDINGS = [
  finding(),
  finding({
    id: "4f6f1a2b-0000-4000-8000-00000000f102",
    severity: "critical",
    category: "safety",
    title: "DROP TABLE",
    message: "High-risk operation.",
    evidence_ids: [],
  }),
];

function ready(findings: ReviewFinding[] = FINDINGS) {
  return { status: "ready" as const, findings };
}

function incomplete(findings: ReviewFinding[] = FINDINGS) {
  return { status: "incomplete" as const, findings };
}

describe("FindingList", () => {
  it("renders structured findings with severity, category and stage, and the numeric distribution under the title bar", () => {
    render(<FindingList state={ready()} onOpenEvidence={() => {}} />);
    expect(screen.getAllByTestId("finding-item")).toHaveLength(2);
    // Critical appears twice: the distribution row's label and the finding badge.
    expect(screen.getAllByText("\u4e25\u91cd")).toHaveLength(2);
    expect(screen.getAllByText("\u9636\u6bb5 1").length).toBe(2);
    // Distribution row sits inside the list, all five severities with counts.
    expect(screen.getByTestId("severity-count-critical")).toHaveTextContent("1");
    expect(screen.getByTestId("severity-count-info")).toHaveTextContent("0");
  });

  it("filters by severity through the select", async () => {
    const user = userEvent.setup();
    render(<FindingList state={ready()} onOpenEvidence={() => {}} />);
    await user.click(screen.getByLabelText("\u7b5b\u9009\u4e25\u91cd\u7ea7\u522b"));
    await user.click(await screen.findByRole("option", { name: "\u4e25\u91cd" }));
    expect(screen.getAllByTestId("finding-item")).toHaveLength(1);
    expect(screen.getByText("DROP TABLE")).toBeInTheDocument();
  });

  it("filters info findings independently", async () => {
    const user = userEvent.setup();
    render(
      <FindingList
        state={ready([
          finding({
            id: "4f6f1a2b-0000-4000-8000-00000000f103",
            severity: "info",
            title: "Execution hint",
            evidence_ids: [],
          }),
          finding(),
        ])}
        onOpenEvidence={() => {}}
      />,
    );
    await user.click(screen.getByLabelText("\u7b5b\u9009\u4e25\u91cd\u7ea7\u522b"));
    await user.click(await screen.findByRole("option", { name: "\u4fe1\u606f" }));
    expect(screen.getAllByTestId("finding-item")).toHaveLength(1);
    expect(screen.getByText("Execution hint")).toBeInTheDocument();
  });

  it("shows an empty state when the filter matches nothing", async () => {
    const user = userEvent.setup();
    render(<FindingList state={ready()} onOpenEvidence={() => {}} />);
    await user.click(screen.getByLabelText("\u7b5b\u9009\u4e25\u91cd\u7ea7\u522b"));
    await user.click(await screen.findByRole("option", { name: "\u4f4e" }));
    expect(screen.getByTestId("findings-empty")).toBeInTheDocument();
  });

  it("offers the evidence affordance only when evidence is bound", () => {
    render(<FindingList state={ready()} onOpenEvidence={() => {}} />);
    expect(screen.getAllByRole("button", { name: "\u67e5\u770b\u5ba1\u6838\u8bc1\u636e" })).toHaveLength(1);
  });

  it("offers SQL locating only for findings quoting a snippet present in the editor", () => {
    const onLocate = vi.fn();
    render(
      <FindingList state={ready()} onOpenEvidence={() => {}} onLocate={onLocate} />,
    );
    const locateButtons = screen.getAllByRole("button", { name: "\u5728SQL\u4e2d\u5b9a\u4f4d" });
    // Only the medium finding quotes a snippet; the critical one does not.
    expect(locateButtons).toHaveLength(1);
    locateButtons[0]?.click();
    expect(onLocate).toHaveBeenCalledWith(
      expect.objectContaining({ id: "4f6f1a2b-0000-4000-8000-00000000f101" }),
    );
  });

  it("never renders a reasoning channel or model transcript", () => {
    render(<FindingList state={ready()} onOpenEvidence={() => {}} />);
    const text = document.body.textContent;
    expect(text).not.toMatch(/\u601d\u7ef4\u94fe|\u601d\u8003\u8fc7\u7a0b|chain of thought|reasoning_content/i);
    expect(document.querySelector("[data-testid='reasoning']")).toBeNull();
  });

  it("shows a waiting state while the review is queued or running", () => {
    render(<FindingList state={{ status: "waiting" }} onOpenEvidence={() => {}} />);

    const waiting = screen.getByTestId("findings-waiting");
    expect(waiting).toHaveAttribute("role", "status");
    expect(waiting).toHaveTextContent("AI \u9884\u5ba1\u8fdb\u884c\u4e2d");
    expect(waiting).toHaveTextContent("\u98ce\u9669\u53d1\u73b0\u5c06\u5728\u6240\u6709\u5ba1\u6838\u9636\u6bb5\u5b8c\u6210\u540e\u663e\u793a");
    expect(screen.queryByTestId("findings-empty")).not.toBeInTheDocument();
    expect(screen.queryByTestId("findings-empty-all")).not.toBeInTheDocument();
  });

  it("shows loading until the terminal review findings request succeeds", () => {
    render(<FindingList state={{ status: "loading" }} onOpenEvidence={() => {}} />);

    const loading = screen.getByTestId("findings-loading");
    expect(loading).toHaveTextContent("\u6b63\u5728\u52a0\u8f7d\u98ce\u9669\u53d1\u73b0");
    expect(loading.querySelector('[role="status"]')).not.toBeNull();
    expect(screen.queryByTestId("findings-empty")).not.toBeInTheDocument();
    expect(screen.queryByTestId("findings-empty-all")).not.toBeInTheDocument();
  });

  it("shows an actionable error instead of an empty result", async () => {
    const user = userEvent.setup();
    const onRetry = vi.fn();
    render(
      <FindingList
        state={{ status: "error", error: new Error("findings request failed") }}
        onOpenEvidence={() => {}}
        onRetry={onRetry}
      />,
    );

    expect(screen.getByTestId("findings-error")).toHaveTextContent("\u8bf7\u6c42\u5931\u8d25");
    expect(screen.queryByTestId("findings-empty")).not.toBeInTheDocument();
    expect(screen.queryByTestId("findings-empty-all")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "\u91cd\u8bd5" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it("only reports no risks after a successful review result", () => {
    render(<FindingList state={ready([])} onOpenEvidence={() => {}} />);

    expect(screen.getByTestId("findings-empty-all")).toHaveTextContent("\u672c\u6b21\u9884\u5ba1\u672a\u53d1\u73b0\u98ce\u9669");
    expect(screen.queryByTestId("findings-empty")).not.toBeInTheDocument();
    expect(screen.queryByTestId("finding-item")).not.toBeInTheDocument();
  });

  it("never reports no risks when an incomplete review returns an empty list", () => {
    render(<FindingList state={incomplete([])} onOpenEvidence={() => {}} />);

    const incompleteNotice = screen.getByTestId("findings-incomplete");
    expect(incompleteNotice).toHaveAttribute("role", "alert");
    expect(incompleteNotice).toHaveTextContent("\u5ba1\u6838\u672a\u5b8c\u6574\u5b8c\u6210");
    expect(screen.queryByTestId("findings-empty-all")).not.toBeInTheDocument();
  });

  it("keeps available findings readable when the review is incomplete", () => {
    render(<FindingList state={incomplete()} onOpenEvidence={() => {}} />);

    expect(screen.getByTestId("findings-incomplete")).toHaveTextContent("\u5ba1\u6838\u672a\u5b8c\u6574\u5b8c\u6210");
    expect(screen.getAllByTestId("finding-item")).toHaveLength(2);
    expect(screen.queryByTestId("findings-empty-all")).not.toBeInTheDocument();
  });

  it("keeps no-run and stale results distinct from an empty review", () => {
    const idle = render(
      <FindingList state={{ status: "idle" }} onOpenEvidence={() => {}} />,
    );
    expect(screen.getByTestId("findings-idle")).toHaveTextContent("\u5c1a\u672a\u751f\u6210\u98ce\u9669\u53d1\u73b0");
    expect(screen.queryByTestId("findings-empty-all")).not.toBeInTheDocument();
    idle.unmount();

    render(<FindingList state={{ status: "stale" }} onOpenEvidence={() => {}} />);
    // RCP-20261101: staleness copy = SQL modified (local comparison, not stale draft version).
    expect(screen.getByTestId("findings-stale")).toHaveTextContent("SQL\u5df2\u4fee\u6539");
    expect(screen.queryByTestId("findings-empty-all")).not.toBeInTheDocument();
  });

  it("renders backtick spans as inline code without literal backticks", () => {
    render(<FindingList state={ready()} onOpenEvidence={() => {}} />);
    const code = screen.getByTestId("finding-inline-code");
    expect(code).toHaveTextContent("UPDATE orders SET status = 1");
    // The markdown backticks are a rendering convention, not user-visible text.
    const allText = screen
      .getAllByTestId("finding-item")
      .map((item) => item.textContent)
      .join("");
    expect(allText).not.toContain("`");
  });

  it("keeps the suggestion visible as a labelled block with normal contrast", () => {
    render(<FindingList state={ready()} onOpenEvidence={() => {}} />);
    const suggestions = screen.getAllByTestId("finding-suggestion");
    expect(suggestions).toHaveLength(2);
    for (const suggestion of suggestions) {
      expect(suggestion).toHaveTextContent("\u4fee\u6539\u5efa\u8bae");
      // The block must not rely on the muted (near-disabled) foreground.
      expect(suggestion.className).not.toContain("text-muted-foreground");
    }
    expect(suggestions[0]).toHaveTextContent("Add an index.");
  });

  it("merges same-group findings into one card led by the highest severity", () => {
    const groupId = "9b0f6e21-0000-4000-8000-00000000a001";
    render(
      <FindingList
        state={ready([
          finding({
            id: "4f6f1a2b-0000-4000-8000-00000000f201",
            severity: "medium",
            category: "operability",
            title: "Target table does not exist, DROP TABLE will fail",
            message: "Current metadata snapshot shows demo.x does not exist; statement omits IF EXISTS.",
            suggestion: "Use DROP TABLE IF EXISTS x for idempotent removal.",
            fingerprint_group_id: groupId,
          }),
          finding({
            id: "4f6f1a2b-0000-4000-8000-00000000f202",
            severity: "critical",
            category: "correctness",
            title: "Object x does not exist, statement #1 cannot execute",
            message: "Object x in statement #1 requires existence, proven absent.",
            suggestion: "Verify target object and statement order first.",
            fingerprint_group_id: groupId,
          }),
        ])}
        onOpenEvidence={() => {}}
      />,
    );
    // One merged card, headline severity = highest of the group.
    const cards = screen.getAllByTestId("finding-item");
    expect(cards).toHaveLength(1);
    expect(cards[0]).toHaveAttribute("data-severity", "critical");
    expect(screen.getByText("Object x does not exist, statement #1 cannot execute")).toBeInTheDocument();
    // The lower-severity finding stays fully present as a secondary sub-item:
    // its own severity, title and advice visible, message one expand away.
    const subs = screen.getAllByTestId("finding-sub-item");
    expect(subs).toHaveLength(1);
    expect(subs[0]).toHaveAttribute("data-severity", "medium");
    expect(subs[0]).toHaveTextContent("Target table does not exist, DROP TABLE will fail");
    expect(subs[0]).toHaveTextContent("Use DROP TABLE IF EXISTS x for idempotent removal.");
    expect(screen.getByTestId("finding-sub-detail")).toHaveTextContent("demo.x");
  });

  it("falls back to a flat card when the filter hides the other group member", async () => {
    const groupId = "9b0f6e21-0000-4000-8000-00000000a002";
    const user = userEvent.setup();
    render(
      <FindingList
        state={ready([
          finding({ id: "4f6f1a2b-0000-4000-8000-00000000f211", fingerprint_group_id: groupId }),
          finding({
            id: "4f6f1a2b-0000-4000-8000-00000000f212",
            severity: "critical",
            fingerprint_group_id: groupId,
          }),
        ])}
        onOpenEvidence={() => {}}
      />,
    );
    await user.click(screen.getByLabelText("\u7b5b\u9009\u4e25\u91cd\u7ea7\u522b"));
    await user.click(await screen.findByRole("option", { name: "\u4e25\u91cd" }));
    expect(screen.getAllByTestId("finding-item")).toHaveLength(1);
    expect(screen.queryByTestId("finding-sub-item")).not.toBeInTheDocument();
  });

  it("opens the whole group's evidence through the single card button", () => {
    const groupId = "9b0f6e21-0000-4000-8000-00000000a003";
    const onOpenEvidence = vi.fn();
    render(
      <FindingList
        state={ready([
          finding({
            id: "4f6f1a2b-0000-4000-8000-00000000f221",
            severity: "critical",
            title: "Object x does not exist, statement #1 cannot execute",
            evidence_ids: [],
            fingerprint_group_id: groupId,
          }),
          finding({
            id: "4f6f1a2b-0000-4000-8000-00000000f222",
            severity: "medium",
            title: "Target table does not exist, DROP TABLE will fail",
            evidence_ids: [EVIDENCE_ID],
            fingerprint_group_id: groupId,
          }),
        ])}
        onOpenEvidence={onOpenEvidence}
      />,
    );
    // Single evidence button inside the merged card: still shown when the primary item has no evidence but a sub-item does, carrying
    // the finding list for the whole group at once (owner ruling 2026-10-31: same-group evidence lives under one button).
    const buttons = screen.getAllByRole("button", { name: "\u67e5\u770b\u5ba1\u6838\u8bc1\u636e" });
    expect(buttons).toHaveLength(1);
    buttons[0]?.click();
    expect(onOpenEvidence).toHaveBeenCalledWith([
      expect.objectContaining({ id: "4f6f1a2b-0000-4000-8000-00000000f221" }),
      expect.objectContaining({ id: "4f6f1a2b-0000-4000-8000-00000000f222" }),
    ]);
    // Sub-item details no longer have their own evidence buttons.
    expect(screen.getAllByTestId("finding-sub-item")).toHaveLength(1);
  });

  it("upgrades long code spans to a copyable code block", async () => {
    const longSql = `ALTER TABLE orders ADD COLUMN backup_reason varchar(255) NOT NULL DEFAULT '' COMMENT 'manual backup justification for this change batch'`;
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    render(
      <FindingList
        state={ready([
          finding({
            id: "4f6f1a2b-0000-4000-8000-00000000f301",
            title: "Add column to large table",
            message: `Suggested statement \`${longSql}\` requires table-lock evaluation.`,
            suggestion: null,
          }),
        ])}
        onOpenEvidence={() => {}}
      />,
    );
    const block = screen.getByTestId("finding-block-code");
    expect(block).toHaveTextContent(longSql);
    // Native click (same pattern as the locate test): userEvent's pointer
    // clipping check mis-fires on buttons nested in overflow-hidden cards.
    screen.getByTestId("finding-copy-code").click();
    expect(writeText).toHaveBeenCalledWith(longSql);
    // Copy feedback is announced to assistive technology.
    expect(await screen.findByText("\u4ee3\u7801\u5df2\u590d\u5236")).toBeInTheDocument();
  });
});
