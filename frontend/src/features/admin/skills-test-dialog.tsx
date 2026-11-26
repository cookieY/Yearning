import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Play, Sparkles } from "lucide-react";
import type {
  PromptToolTestCase,
  PromptToolWriteParameters,
  RunPromptToolTestResponse,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useGeneratePromptToolTestCases,
  useRunPromptToolTest,
} from "@/features/admin/use-admin";
import { describeError } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Alert, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Textarea } from "@/shared/components/ui/textarea";

export function SkillTestDialog({
  name,
  description,
  engine,
  parameters,
  knowledgeText,
  onClose,
}: {
  name: string;
  description: string;
  engine: "all" | "mysql" | "postgresql";
  parameters: PromptToolWriteParameters;
  knowledgeText: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const generateCases = useGeneratePromptToolTestCases();
  const runTest = useRunPromptToolTest();
  const [cases, setCases] = useState<PromptToolTestCase[]>([]);
  const [testResult, setTestResult] = useState<RunPromptToolTestResponse | null>(null);

  const handleGenerate = () => {
    generateCases.mutate(
      {
        name: name.trim() || undefined,
        description: description.trim() || undefined,
        engine,
        parameters,
        knowledge_text: knowledgeText,
      },
      {
        onSuccess: (res) => {
          setCases(res.cases);
          setTestResult(null);
        },
        onError: (err) => {
          const display = describeError(err, "generatePromptToolTestCases");
          appToast.error(t(display.messageKey));
        },
      },
    );
  };

  const handleRunTest = () => {
    runTest.mutate(
      {
        name: name.trim() || undefined,
        description: description.trim() || undefined,
        engine,
        parameters,
        knowledge_text: knowledgeText,
        cases,
      },
      {
        onSuccess: (res) => {
          setTestResult(res);
        },
        onError: (err) => {
          const display = describeError(err, "runPromptToolTest");
          appToast.error(t(display.messageKey));
        },
      },
    );
  };

  const resultById = new Map((testResult?.results ?? []).map((item) => [item.id, item]));

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className="sm:max-w-2xl" data-testid="skill-test-dialog">
        <DialogHeader>
          <DialogTitle>{t("admin.skills.testDialogTitle")}</DialogTitle>
          <DialogDescription>{t("admin.skills.testDialogDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <PendingButton
              size="sm"
              variant="outline"
              pending={generateCases.isPending}
              onClick={handleGenerate}
              data-testid="skill-test-generate"
            >
              <Sparkles className="size-3.5" aria-hidden="true" />
              {generateCases.isPending
                ? t("admin.skills.generateTestCasesPending")
                : t("admin.skills.generateTestCases")}
            </PendingButton>
            <PendingButton
              size="sm"
              pending={runTest.isPending}
              disabled={cases.length === 0}
              onClick={handleRunTest}
              data-testid="skill-test-run"
            >
              <Play className="size-3.5" aria-hidden="true" />
              {runTest.isPending
                ? t("admin.skills.runTestPending")
                : t("admin.skills.runTest")}
            </PendingButton>
          </div>
          {testResult !== null ? (
            <Alert data-testid="skill-test-summary">
              <AlertTitle className="flex flex-wrap items-center gap-2">
                <span>
                  {t("admin.skills.testSummaryStats", {
                    matched: testResult.matched_count,
                    total: testResult.total_count,
                  })}
                </span>
                <Badge variant={testResult.passed ? "default" : "destructive"} data-testid="skill-test-summary-badge">
                  {t(testResult.passed ? "admin.skills.testSummaryPassed" : "admin.skills.testSummaryFailed")}
                </Badge>
              </AlertTitle>
            </Alert>
          ) : null}
          {cases.length === 0 ? (
            <div className="text-muted-foreground rounded-lg border border-dashed p-6 text-center text-xs" data-testid="skill-test-empty">
              {t("admin.skills.testEmptyHint")}
            </div>
          ) : (
            <div className="flex flex-col gap-2.5" data-testid="skill-test-cases">
              {cases.map((tc, index) => {
                const verdict = resultById.get(tc.id);
                const caseNumber = String(index + 1);
                return (
                  <div
                    key={tc.id}
                    className="flex flex-col gap-2 rounded-lg border p-3"
                    data-testid={`skill-test-case-${caseNumber}`}
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-mono text-xs font-medium">#{caseNumber}</span>
                        <Badge variant={tc.expected_compliant ? "secondary" : "outline"} data-testid={`skill-test-expected-${caseNumber}`}>
                          {t(tc.expected_compliant ? "admin.skills.expectedCompliant" : "admin.skills.expectedNonCompliant")}
                        </Badge>
                      </div>
                      {verdict !== undefined ? (
                        <div className="flex flex-wrap items-center gap-1.5">
                          <Badge variant="outline" data-testid={`skill-test-actual-${caseNumber}`}>
                            {t(verdict.actual_compliant ? "admin.skills.actualCompliant" : "admin.skills.actualNonCompliant")}
                          </Badge>
                          <Badge
                            variant={verdict.matched_expectation ? "default" : "destructive"}
                            data-testid={`skill-test-match-${caseNumber}`}
                          >
                            {t(verdict.matched_expectation ? "admin.skills.matchedExpectation" : "admin.skills.mismatchedExpectation")}
                          </Badge>
                        </div>
                      ) : null}
                    </div>
                    <Textarea
                      rows={2}
                      className="font-mono text-xs"
                      value={tc.sql}
                      onChange={(event) => {
                        const nextSql = event.target.value;
                        setCases((prev) =>
                          prev.map((item, idx) => (idx === index ? { ...item, sql: nextSql } : item)),
                        );
                      }}
                      data-testid={`skill-test-sql-${caseNumber}`}
                    />
                    <p className="text-muted-foreground text-xs">
                      <span className="font-medium">{t("admin.skills.testCaseRationale")}: </span>
                      {tc.rationale}
                    </p>
                    {verdict !== undefined ? (
                      <p className="text-xs" data-testid={`skill-test-finding-${caseNumber}`}>
                        <span className="font-medium">{t("admin.skills.testCaseFinding")}: </span>
                        {verdict.finding_summary}
                      </p>
                    ) : null}
                  </div>
                );
              })}
            </div>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
