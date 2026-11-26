import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReviewRun, ReviewStageResultState } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import i18n from "@/shared/i18n";
import { knownFailureKey, ReviewStatusCard } from "./review-status-card";
import { extractReviewSummaries, ReviewSummarySection } from "./review-summary-section";
import type { RunPhase } from "./run-state";

/**
 * The review status card renders the observable run truth: phase badge with
 * icon+text, per-stage results, statement/fingerprint counts and the known
 * failure copy. Text is i18n-only — raw state names and failure codes never
 * reach the screen (migration contract §1).
 */

function runFixture(overrides: Partial<ReviewRun> = {}): ReviewRun {
  return {
    id: "4f6f1a2b-0000-4000-8000-00000000aa01",
    draft_id: "4f6f1a2b-0000-4000-8000-00000000aa02",
    draft_revision: 1,
    state: "ready",
    statement_count: 12,
    fingerprint_group_count: 3,
    stage_results: [
      {
        stage_position: 1,
        state: "passed",
        highest_severity: "medium",
        gate_passed: true,
        finding_count: 1,
        evidence_count: 1,
        snapshot_hash: "snap-1",
        summary: "\u5ba1\u6838\u5b8c\u6210\u3002",
        summary_locale: "zh-CN",
      },
    ],
    gate: { passed: true, reason_codes: [] },
    failure_code: null,
    version: 3,
    created_at: "2026-08-30T00:00:00Z",
    started_at: "2026-08-30T00:00:01Z",
    finished_at: "2026-08-30T00:00:02Z",
    ...overrides,
  };
}

function renderCard(
  phase: RunPhase,
  run: ReviewRun | null,
  query: {
    hasRunId?: boolean;
    isPending?: boolean;
    isError?: boolean;
    expired?: boolean;
    error?: unknown;
    onRetry?: () => void;
  } = {},
) {
  return render(
    <ReviewStatusCard
      phase={phase}
      run={run}
      hasRunId={query.hasRunId ?? (run !== null || phase !== "idle")}
      isPending={query.isPending ?? false}
      isError={query.isError ?? false}
      expired={query.expired}
      error={query.error}
      onRetry={query.onRetry}
    />,
  );
}

describe("ReviewStatusCard", () => {
  beforeEach(async () => {
    await i18n.changeLanguage("zh-CN");
  });

  it("shows counts and stage results for a finished run", () => {
    renderCard("ready", runFixture());
    expect(screen.getByTestId("review-counts").textContent).toContain("12");
    expect(screen.getByTestId("review-counts").textContent).toContain("3");
    expect(screen.getByRole("cell", { name: "\u9636\u6bb5 1" })).toBeInTheDocument();
    expect(screen.getByText("\u901a\u8fc7")).toBeInTheDocument();
  });

  it("presents the queued/running phases without run data regressions", () => {
    renderCard("running", null);
    expect(screen.getByText("\u9884\u5ba1\u4e2d")).toBeInTheDocument();
  });

  it("renders known failure codes as i18n copy, never the raw code", () => {
    renderCard(
      "failed",
      runFixture({
        state: "failed",
        failure_code: "provider_unavailable",
        gate: { passed: false, reason_codes: ["stage_review_failed"] },
        stage_results: [
          {
            stage_position: 1,
            state: "failed",
            highest_severity: "none",
            gate_passed: false,
            finding_count: 0,
            evidence_count: 0,
            snapshot_hash: "snap-x",
            summary: null,
            summary_locale: null,
          },
        ],
      }),
    );
    expect(screen.queryByTestId("review-failure")).not.toBeInTheDocument();
    expect(screen.queryByTestId("review-summary")).not.toBeInTheDocument();
    expect(i18n.t(`precheck.failure.${knownFailureKey("provider_unavailable")}`)).toContain(
      "AI\u670d\u52a1\u6682\u4e0d\u53ef\u7528",
    );
    expect(screen.queryByText("provider_unavailable")).not.toBeInTheDocument();
  });

  it("uses safe generic copy for unknown failure codes", () => {
    renderCard(
      "failed",
      runFixture({ state: "failed", failure_code: "brand_new_failure" }),
    );
    expect(screen.queryByTestId("review-failure")).not.toBeInTheDocument();
    expect(knownFailureKey("brand_new_failure")).toBe("generic");
    expect(i18n.t(`precheck.failure.${knownFailureKey("brand_new_failure")}`)).toContain("\u9884\u5ba1\u5931\u8d25");
    expect(screen.queryByText("brand_new_failure")).not.toBeInTheDocument();
  });

  it("explains that no run exists yet instead of fabricating one", () => {
    renderCard("idle", null);
    expect(screen.getByText(/\u5c1a\u672a\u8fd0\u884c\u9884\u5ba1/)).toBeInTheDocument();
  });

  it("shows run loading instead of claiming that no precheck exists", () => {
    renderCard("idle", null, { hasRunId: true, isPending: true });
    expect(screen.getByTestId("review-status-loading")).toHaveTextContent("\u6b63\u5728\u8bfb\u53d6\u5ba1\u6838\u72b6\u6001");
    expect(screen.queryByText(/\u5c1a\u672a\u8fd0\u884c\u9884\u5ba1/)).not.toBeInTheDocument();
  });

  it("shows a retryable run read error instead of claiming that no precheck exists", () => {
    const onRetry = vi.fn();
    renderCard("idle", null, {
      hasRunId: true,
      isError: true,
      error: new Error("review run unavailable"),
      onRetry,
    });
    expect(screen.getByTestId("review-status-error")).toHaveTextContent("\u8bf7\u6c42\u5931\u8d25");
    expect(screen.queryByText(/\u5c1a\u672a\u8fd0\u884c\u9884\u5ba1/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "\u91cd\u8bd5" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it("shows expired notice instead of generic error when an ephemeral run expired", () => {
    renderCard("idle", null, {
      hasRunId: true,
      expired: true,
      isError: true,
      error: new Error("RESOURCE_NOT_FOUND"),
    });
    expect(screen.getByTestId("review-status-expired")).toHaveTextContent("\u9884\u5ba1\u7ed3\u679c\u5df2\u8fc7\u671f");
    expect(screen.queryByTestId("review-status-error")).not.toBeInTheDocument();
  });

  it("shows the AI usage as a Context hover card: total on the button, exact figures on hover", async () => {
    renderCard(
      "ready",
      runFixture({
        usage: {
          provider_name: "DeepSeek",
          model_name: "deepseek-chat",
          input_tokens: 6896,
          output_tokens: 1583,
          amount_minor: 9,
          currency: "CNY",
        },
      }),
    );
    // The trigger carries only the abbreviated total.
    const trigger = screen.getByTestId("review-usage");
    expect(trigger).toHaveTextContent("8.5k tokens");
    expect(screen.queryByTestId("review-usage-card")).not.toBeInTheDocument();

    await userEvent.hover(trigger);
    const card = await screen.findByTestId("review-usage-card");
    expect(card).toHaveTextContent("deepseek-chat");
    expect(screen.getByTestId("review-usage-provider")).toHaveTextContent("DeepSeek");
    // Exact (not abbreviated) figures, with their share of the total.
    expect(screen.getByTestId("review-usage-input")).toHaveTextContent(/6,896.*81%/);
    expect(screen.getByTestId("review-usage-output")).toHaveTextContent(/1,583.*19%/);
    expect(screen.getByRole("progressbar", { name: "\u8f93\u5165\u5360\u6bd4" })).toHaveAttribute("aria-valuenow", "81");
  });

  it("shows no usage control while the run has no usage yet", () => {
    renderCard("ready", runFixture({ usage: null }));
    expect(screen.queryByTestId("review-usage")).not.toBeInTheDocument();
  });

  it("falls back to pending copy for an unrecognized stage state", () => {
    // Generated type union matches STAGE_BADGE; the fallback branch guards against runtime drift,
    // so construct a state outside the union via explicit cast.
    renderCard(
      "ready",
      runFixture({
        stage_results: [
          {
            stage_position: 1,
            state: "outdated" as unknown as ReviewStageResultState,
            highest_severity: "none",
            gate_passed: false,
            finding_count: 0,
            evidence_count: 0,
            snapshot_hash: "snap-o",
            summary: null,
            summary_locale: null,
          },
        ],
      }),
    );
    expect(screen.getByText("\u7b49\u5f85")).toBeInTheDocument();
  });

  it("renders budget and timeout failure copy from the known set", () => {
    expect(i18n.t(`precheck.failure.${knownFailureKey("budget_exhausted")}`)).toContain("\u5f53\u65e5AI\u9884\u7b97\u5df2\u8017\u5c3d");
    expect(i18n.t(`precheck.failure.${knownFailureKey("timed_out")}`)).toContain("\u9884\u5ba1\u8d85\u65f6");
    for (const [code, expected] of [
      ["AI_PROVIDER_FAILED", "AI\u670d\u52a1\u6682\u4e0d\u53ef\u7528"],
      ["REVIEW_COVERAGE_INCOMPLETE", "\u9636\u6bb5\u5143\u6570\u636e\u6216\u7b56\u7565\u8986\u76d6\u4e0d\u5b8c\u6574"],
      ["REVIEW_SCHEMA_INVALID", "AI\u6a21\u578b\u8fd4\u56de\u683c\u5f0f\u4e0d\u7b26\u5408\u5ba1\u6838Schema\u8981\u6c42"],
      ["SQL_PARSE_FAILED", "SQL\u8bed\u6cd5\u89e3\u6790\u5931\u8d25"],
      ["SQL_FINGERPRINT_FAILED", "SQL\u6307\u7eb9\u6216\u8131\u654f\u5206\u6790\u5931\u8d25"],
      ["SQL_BATCH_TOO_LARGE", "SQL\u6279\u6b21\u5927\u5c0f\u6216\u6307\u7eb9\u7ec4\u6570\u91cf\u8d85\u51fa\u9884\u5ba1\u4e0a\u9650"],
    ] as const) {
      expect(i18n.t(`precheck.failure.${knownFailureKey(code)}`)).toContain(expected);
      const view = renderCard("failed", runFixture({ state: "failed", failure_code: code }));
      expect(screen.queryByTestId("review-failure")).not.toBeInTheDocument();
      view.unmount();
    }
  });

  it("shows the accepted summary verbatim while translating only its label", async () => {
    const summary = "\u5df2\u63a5\u53d7\u7684\u4e2d\u6587\u5ba1\u6838\u7ed3\u8bba\u3002";
    const run = runFixture({
      stage_results: [
        {
          stage_position: 1,
          state: "passed",
          highest_severity: "none",
          gate_passed: true,
          finding_count: 0,
          evidence_count: 1,
          snapshot_hash: "snap-summary",
          summary,
          summary_locale: "zh-CN",
        },
      ] as ReviewRun["stage_results"],
    });
    render(<ReviewSummarySection summaries={extractReviewSummaries(run)} />);

    expect(screen.getByText("AI \u5ba1\u6838\u7ed3\u8bba")).toBeInTheDocument();
    expect(screen.getByText(summary)).toHaveAttribute("lang", "zh-CN");

    await i18n.changeLanguage("en-US");

    expect(screen.getByText("AI Review Summary")).toBeInTheDocument();
    expect(screen.getByText(summary)).toHaveAttribute("lang", "zh-CN");
  });

  it("renders Chinese and English frozen summaries independently of UI language", async () => {
    const zhSummary = "\u4e2d\u6587\u51bb\u7ed3\u5ba1\u6838\u7ed3\u8bba\u3002";
    const enSummary = "English frozen review summary.";
    const run = runFixture({
      stage_results: [
        {
          stage_position: 1,
          state: "passed",
          highest_severity: "none",
          gate_passed: true,
          finding_count: 0,
          evidence_count: 1,
          snapshot_hash: "snap-summary-zh",
          summary: zhSummary,
          summary_locale: "zh-CN",
        },
        {
          stage_position: 2,
          state: "passed",
          highest_severity: "none",
          gate_passed: true,
          finding_count: 0,
          evidence_count: 1,
          snapshot_hash: "snap-summary-en",
          summary: enSummary,
          summary_locale: "en-US",
        },
      ],
    });
    render(<ReviewSummarySection summaries={extractReviewSummaries(run)} />);

    expect(screen.getByText(zhSummary)).toHaveAttribute("lang", "zh-CN");
    expect(screen.getByText(enSummary)).toHaveAttribute("lang", "en-US");

    await i18n.changeLanguage("en-US");

    expect(screen.getByText(zhSummary)).toHaveAttribute("lang", "zh-CN");
    expect(screen.getByText(enSummary)).toHaveAttribute("lang", "en-US");
  });

  it("does not fabricate a summary for a stage that has not completed", () => {
    const run = runFixture({
      state: "running",
      stage_results: [
        {
          stage_position: 1,
          state: "running",
          highest_severity: "none",
          gate_passed: false,
          finding_count: 0,
          evidence_count: 0,
          snapshot_hash: "snap-running",
          summary: null,
          summary_locale: null,
        },
      ] as ReviewRun["stage_results"],
    });
    render(<ReviewSummarySection summaries={extractReviewSummaries(run)} />);

    expect(screen.queryByTestId("review-summary")).not.toBeInTheDocument();
  });

  it("ignores a malformed stage response with no summary property", () => {
    const malformedStage = {
      stage_position: 1,
      state: "passed",
      highest_severity: "none",
      gate_passed: true,
      finding_count: 0,
      evidence_count: 1,
      snapshot_hash: "snap-malformed",
    };
    const run = runFixture({
      stage_results: [malformedStage] as unknown as ReviewRun["stage_results"],
    });
    expect(extractReviewSummaries(run)).toHaveLength(0);
    renderCard("ready", run);

    expect(screen.queryByTestId("review-summary")).not.toBeInTheDocument();
    expect(screen.getByRole("cell", { name: "\u9636\u6bb5 1" })).toBeInTheDocument();
  });

  it("maps lowercase coverage_incomplete and specific 2007/2008 diagnostic failure codes without leaking raw codes", () => {
    for (const [code, expected] of [
      ["coverage_incomplete", "\u9636\u6bb5\u5143\u6570\u636e\u6216\u7b56\u7565\u8986\u76d6\u4e0d\u5b8c\u6574"],
      ["risk_blocked", "\u53d1\u73b0\u963b\u65ad\u63d0\u4ea4\u7684\u9ad8\u5371\u6216\u4e25\u91cd\u98ce\u9669"],
      ["sql_schema_out_of_scope", "SQL \u4e2d\u663e\u5f0f\u6307\u5b9a\u7684\u5e93\u540d\u6216 Schema"],
      ["datasource_database_required", "\u76ee\u6807\u6570\u636e\u6e90\u672a\u914d\u7f6e\u6709\u6548\u7684\u9ed8\u8ba4\u6570\u636e\u5e93"],
      ["postgres_search_path_unavailable", "search_path"],
      ["datasource_identity_unverified", "\u5c1a\u672a\u5b8c\u6210\u5b9e\u4f8b\u8eab\u4efd\u6821\u9a8c"],
      ["datasource_identity_changed", "\u5b9e\u4f8b\u8eab\u4efd\u6307\u7eb9\u5df2\u53d1\u751f\u53d8\u5316"],
      ["review_credential_missing", "\u672a\u914d\u7f6e\u53ef\u7528\u7684\u53ea\u8bfb\u51ed\u636e"],
      ["datasource_unreachable", "\u65e0\u6cd5\u8fde\u63a5\u76ee\u6807\u6570\u636e\u6e90"],
      ["catalog_schema_missing", "\u4e0d\u5b58\u5728\u6307\u5b9a\u7684\u6570\u636e\u5e93\u6216 Schema"],
      ["catalog_permission_denied", "SELECT \u6216 USAGE \u6743\u9650"],
      ["catalog_too_large", "\u8d85\u51fa\u9690\u79c1\u76ee\u5f55\u4fdd\u62a4\u4e0a\u9650"],
      ["catalog_read_failed", "\u8bfb\u53d6\u76ee\u6807\u6570\u636e\u5e93\u5143\u6570\u636e\u76ee\u5f55\u8d85\u65f6\u6216\u5931\u8d25"],
      ["catalog_validation_failed", "\u76ee\u6807\u6570\u636e\u5e93\u5143\u6570\u636e\u76ee\u5f55\u7ed3\u6784\u5f02\u5e38"],
      ["policy_privacy_conflict", "\u4e0e\u9690\u79c1\u6620\u5c04\u51b2\u7a81"],
      ["policy_parameter_invalid", "\u672a\u5b9a\u4e49\u6216\u975e\u6cd5\u7684\u53c2\u6570\u5360\u4f4d\u7b26"],
      ["rule_exceeds_context_window", "\u8d85\u51fa\u5f53\u524d AI \u63d0\u4f9b\u5546\u7684\u4e0a\u4e0b\u6587\u7a97\u53e3\u4e0a\u9650"],
    ] as const) {
      const translated = i18n.t(`precheck.failure.${knownFailureKey(code)}`);
      expect(translated).toContain(expected);
      expect(translated).not.toContain(code);
    }
  });
});
