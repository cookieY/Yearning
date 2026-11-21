import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { FlaskConical, Lock, Pencil, Play, Plus, Puzzle, Sparkles, Trash2 } from "lucide-react";
import type {
  PromptTool,
  PromptToolTestCase,
  PromptToolWrite,
  PromptToolWriteParameters,
  RunPromptToolTestResponse,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type { ReviewInputEditorValue } from "@/features/admin/review-input-editor";
import {
  DeleteReviewInputDialog,
  type InputState,
  ReviewInputDialogFrame,
  ReviewInputStateSelect,
  StateBadge,
} from "@/features/admin/review-input-shared";
import { renderSkillDescription } from "@/features/admin/skill-description";
import {
  useCreatePromptTool,
  useGeneratePromptToolTestCases,
  useOptimizePromptToolPrompt,
  usePromptTools,
  useReplacePromptTool,
  useRunPromptToolTest,
} from "@/features/admin/use-admin";
import { useSession } from "@/features/auth/session-provider";
import { describeError } from "@/shared/api/error-display";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { EmptyState } from "@/shared/components/status/empty-state";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { TableSearchInput } from "@/shared/components/table-search-input";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent } from "@/shared/components/ui/card";
import { Checkbox } from "@/shared/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { Textarea } from "@/shared/components/ui/textarea";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";

// Skill description max length (consistent with OpenAPI PromptToolWrite.description maxLength in code points).
const MAX_SKILL_DESCRIPTION = 100;

interface SkillFormState {
  name: string;
  description: string;
  state: InputState;
  engine: "all" | "mysql" | "postgresql";
  paramDrafts: Record<string, string>;
  editor: ReviewInputEditorValue;
}

// Builtin skill prompts are locked, while concrete {{variable}} values are tunable as typed drafts.
export function stringifyParamDrafts(params: PromptToolWriteParameters): Record<string, string> {
  const drafts: Record<string, string> = {};
  for (const [key, value] of Object.entries(params)) {
    if (typeof value === "boolean") drafts[key] = value ? "true" : "false";
    else if (Array.isArray(value)) drafts[key] = value.join(", ");
    else drafts[key] = String(value);
  }
  return drafts;
}

export function convertParamDrafts(
  drafts: Record<string, string>,
  template: PromptToolWriteParameters,
): PromptToolWriteParameters | null {
  const out: PromptToolWriteParameters = {};
  for (const [key, raw] of Object.entries(drafts)) {
    const sample = template[key];
    if (typeof sample === "number") {
      const parsed = Number(raw);
      if (raw.trim() === "" || !Number.isInteger(parsed) || parsed < 1 || parsed > 1e15) return null;
      out[key] = parsed;
    } else if (typeof sample === "boolean") {
      out[key] = raw === "true";
    } else if (Array.isArray(sample)) {
      const items = raw.split(",").map((item) => item.trim()).filter((item) => item !== "");
      if (items.length > 16 || items.some((item) => item.length > 64)) return null;
      out[key] = items;
    } else {
      if (raw.trim() === "" || raw.length > 128) return null;
      out[key] = raw;
    }
  }
  return out;
}

export function paramTypeKey(value: number | string | boolean | string[]): string {
  if (typeof value === "number") return "paramTypeNumber";
  if (typeof value === "boolean") return "paramTypeBoolean";
  if (Array.isArray(value)) return "paramTypeStringList";
  return "paramTypeString";
}

function initialSkillForm(tool: PromptTool | null): SkillFormState {
  if (tool === null) {
    return {
      name: "",
      description: "",
      state: "draft",
      engine: "all",
      paramDrafts: {},
      editor: {
        definition: { knowledge_text: "", max_severity: "critical", version: 1 },
        parameters: {},
      },
    };
  }
  return {
    name: tool.name,
    description: tool.description,
    state: tool.state,
    engine: tool.engine,
    paramDrafts: stringifyParamDrafts(tool.parameters ?? {}),
    editor: { definition: tool.definition, parameters: tool.parameters ?? {} },
  };
}

/** PromptTool registry management page (/admin/review-engine/skills). */
export function SkillsManagementPage() {
  const { t } = useTranslation();
  const session = useSession();
  const enabled = session.user?.can_access_admin === true;
  const [skillSearch, setSkillSearch] = useState("");
  const skillPager = useCursorPage(20);
  const skillKeyword = skillSearch.trim();
  useEffect(() => { skillPager.reset(); }, [skillKeyword]); // eslint-disable-line react-hooks/exhaustive-deps -- reset is intentional on keyword change
  const toolsQuery = usePromptTools(enabled, {
    q: skillKeyword || undefined,
    limit: skillPager.pageSize,
    after: skillPager.after,
  });
  const toolsLoaded = !toolsQuery.isPending && !toolsQuery.isError;
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<PromptTool | null>(null);
  const [deleting, setDeleting] = useState<PromptTool | null>(null);

  const visibleSkillRows = toolsQuery.data?.items ?? [];
  const skillBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(visibleSkillRows.map((row) => row.id)),
  );

  return (
    <PageLayout>
      <PageHeader
        title={t("admin.skills.title")}
        description={t("admin.skills.description")}
        icon={Puzzle}
        actions={
          <Button
            onClick={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
            data-testid="review-input-create"
          >
            <Plus /> {t("admin.skills.createTitle")}
          </Button>
        }
      />
      <Card>
        <ListToolbar busy={toolsQuery.isFetching && !toolsQuery.isPending} fill>
          <TableSearchInput value={skillSearch} onChange={setSkillSearch} testId="admin-skills-search" />
        </ListToolbar>
        <CardContent className="px-0! flex flex-col gap-3">
          {toolsQuery.isPending ? (
            <LoadingState variant="table" columns={3} />
          ) : toolsQuery.isError ? (
            <ErrorState error={toolsQuery.error} operationId="listPromptTools" onRetry={() => void toolsQuery.refetch()} />
          ) : visibleSkillRows.length === 0 && skillKeyword !== "" ? (
            <EmptyState title={t("table.emptySearch")} filtered testId="admin-skills-search-empty" />
          ) : visibleSkillRows.length === 0 ? (
            <EmptyState
              title={t("admin.skills.emptyTitle")}
              description={t("admin.skills.emptyDescription")}
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("admin.reviewInput.name")}</TableHead>
                  <TableHead>{t("admin.reviewInput.state")}</TableHead>
                  <TableHead className="text-right">{t("common.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody ref={skillBodyRef} stale={toolsQuery.isFetching && toolsQuery.isPlaceholderData}>
                {visibleSkillRows.map((row) => (
                  <TableRow key={row.id} data-row-key={row.id} data-testid={`review-input-row-${row.id}`}>
                    <TableCell className="font-semibold text-foreground">
                      <span className="flex flex-col gap-0.5">
                        <span className="flex items-center gap-1.5">
                          {row.name}
                          {row.is_builtin ? (
                            <Badge variant="outline" data-testid={`review-input-builtin-${row.id}`}>
                              <Lock /> {t("admin.skills.builtinBadge")}
                            </Badge>
                          ) : null}
                        </span>
                        {row.description ? (
                          <span
                            className="text-xs font-normal text-muted-foreground"
                            data-testid={`review-input-description-${row.id}`}
                          >
                            {renderSkillDescription(row.description, row.parameters ?? {})}
                          </span>
                        ) : null}
                      </span>
                    </TableCell>
                    <TableCell>
                      <StateBadge state={row.state} />
                    </TableCell>
                    <TableCell className="text-right">
                      <TableRowActions testId={`review-input-actions-${row.id}`}>
                        <DropdownMenuItem
                          onClick={() => {
                            setEditing(row);
                            setDialogOpen(true);
                          }}
                          data-testid={`review-input-edit-${row.id}`}
                        >
                          <Pencil /> {t("common.edit")}
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          variant="destructive"
                          onClick={() => { setDeleting(row); }}
                          disabled={row.is_builtin}
                          data-testid={`review-input-delete-${row.id}`}
                        >
                          <Trash2 /> {t("common.delete")}
                        </DropdownMenuItem>
                      </TableRowActions>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
          {toolsLoaded ? (
            <TablePagination
              busy={toolsQuery.isFetching && toolsQuery.isPlaceholderData}
              page={skillPager.pageNo}
              hasMore={toolsQuery.data.page.has_more}
              isFirst={skillPager.isFirst}
              onPrev={skillPager.goPrev}
              onJumpPage={skillPager.goToPage}
              onNext={() => { skillPager.pushCursor(toolsQuery.data.page.next_cursor); }}
              pageSize={skillPager.pageSize}
              onPageSizeChange={skillPager.setPageSize}
              rowsOnPage={toolsQuery.data.items.length}
              testIdPrefix="skills"
            />
          ) : null}
        </CardContent>
      </Card>

      {dialogOpen ? (
        <SkillFormDialog
          key={editing?.id ?? "new"}
          editing={editing}
          onClose={() => {
            setDialogOpen(false);
            setEditing(null);
          }}
        />
      ) : null}

      {deleting !== null ? (
        <DeleteReviewInputDialog
          key={deleting.id}
          kind="skills"
          target={deleting}
          onClose={() => { setDeleting(null); }}
        />
      ) : null}
    </PageLayout>
  );
}

function SkillFormDialog({
  editing,
  onClose,
}: {
  editing: PromptTool | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const editingBuiltin = editing !== null && editing.is_builtin;
  const createTool = useCreatePromptTool();
  const replaceTool = useReplacePromptTool();
  const optimizePrompt = useOptimizePromptToolPrompt();
  const [form, setForm] = useState<SkillFormState>(() => initialSkillForm(editing));
  const rowParams: PromptToolWriteParameters = editing !== null ? (editing.parameters ?? {}) : {};
  const [paramInvalid, setParamInvalid] = useState(false);
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const [errorRequestId, setErrorRequestId] = useState<string | null>(null);
  const [skillTestOpen, setSkillTestOpen] = useState(false);

  const onWriteError = (operationId: string) => (error: unknown) => {
    const display = describeError(error, operationId);
    setErrorKey(display.messageKey);
    setErrorRequestId(display.requestId);
    appToast.error(t(display.messageKey));
  };

  const handleOptimizePrompt = () => {
    const seedText =
      form.editor.definition.knowledge_text.trim() ||
      form.description.trim() ||
      form.name.trim();
    const currentParams = convertParamDrafts(form.paramDrafts, rowParams) ?? {};
    optimizePrompt.mutate(
      {
        name: form.name.trim() || undefined,
        description: form.description.trim() || undefined,
        engine: form.engine,
        parameters: currentParams,
        knowledge_text: seedText,
      },
      {
        onSuccess: (res) => {
          setForm((prev) => ({
            ...prev,
            editor: {
              ...prev.editor,
              definition: {
                ...prev.editor.definition,
                knowledge_text: res.knowledge_text,
              },
            },
          }));
          appToast.success(t("admin.skills.optimizePrompt"));
        },
        onError: onWriteError("optimizePromptToolPrompt"),
      },
    );
  };

  const mutation = editing === null ? createTool : replaceTool;

  const submit = () => {
    setErrorKey(null);
    setErrorRequestId(null);
    const params = convertParamDrafts(form.paramDrafts, rowParams);
    if (params === null) {
      setParamInvalid(true);
      return;
    }
    setParamInvalid(false);
    const write: PromptToolWrite = {
      name: form.name,
      description: form.description,
      state: form.state,
      engine: form.engine,
      parameters: params,
      definition: form.editor.definition,
    };
    if (editing === null) {
      createTool.mutate(write, {
        onSuccess: () => {
          appToast.success(t("common.save"), { description: write.name });
          onClose();
        },
        onError: onWriteError("createPromptTool"),
      });
    } else {
      replaceTool.mutate(
        { id: editing.id, version: editing.version, write },
        {
          onSuccess: () => {
            appToast.success(t("common.save"), { description: write.name });
            onClose();
          },
          onError: onWriteError("replacePromptTool"),
        },
      );
    }
  };

  return (
    <ReviewInputDialogFrame
      title={t(editing === null ? "admin.skills.createTitle" : "admin.skills.editTitle")}
      configHash={editing?.config_hash ?? ""}
      notice={
        <>
          {editingBuiltin ? (
            <Alert data-testid="review-input-builtin-notice">
              <AlertTitle>{t("admin.skills.builtinDialogTitle")}</AlertTitle>
              <AlertDescription>{t("admin.skills.builtinDialogBody")}</AlertDescription>
            </Alert>
          ) : null}
          <p className="text-muted-foreground text-xs" data-testid="skills-eval-gate-hint">
            {t("admin.skills.evalGateHint")}
          </p>
        </>
      }
      fields={
        <>
          <div className="flex flex-col gap-1 md:col-span-2">
            <Label htmlFor="review-input-name">{t("admin.reviewInput.name")}</Label>
            <Input
              id="review-input-name"
              value={form.name}
              onChange={(event) => { setForm({ ...form, name: event.target.value }); }}
              disabled={editingBuiltin}
              data-testid="review-input-name"
            />
          </div>
          <div className="flex flex-col gap-1 md:col-span-2">
            <Label htmlFor="review-input-description">{t("admin.skills.descriptionLabel")}</Label>
            <Input
              id="review-input-description"
              value={form.description}
              maxLength={MAX_SKILL_DESCRIPTION}
              onChange={(event) => { setForm({ ...form, description: event.target.value }); }}
              placeholder={t("admin.skills.descriptionPlaceholder")}
              disabled={editingBuiltin}
              data-testid="review-input-description"
            />
            <p className="text-muted-foreground text-xs" data-testid="review-input-description-count">
              {t("admin.skills.descriptionCount", { current: Array.from(form.description).length, max: MAX_SKILL_DESCRIPTION })}
            </p>
          </div>
          <ReviewInputStateSelect
            value={form.state}
            onChange={(state) => { setForm({ ...form, state }); }}
          />
          <div className="flex flex-col gap-1">
            <Label>{t("admin.skills.engine")}</Label>
            <Select
              value={form.engine}
              onValueChange={(next) => {
                if (next === null) return;
                setForm({ ...form, engine: next });
              }}
            >
              <SelectTrigger data-testid="review-input-engine" disabled={editingBuiltin}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">all</SelectItem>
                <SelectItem value="mysql">mysql</SelectItem>
                <SelectItem value="postgresql">postgresql</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </>
      }
      extraSection={
        Object.keys(rowParams).length > 0 ? (
          <div className="flex flex-col gap-2 rounded-lg border p-3" data-testid="review-input-params">
            <div className="flex flex-col gap-0.5">
              <Label>{t("admin.skills.parametersTitle")}</Label>
              <p className="text-muted-foreground text-xs">{t("admin.skills.parametersHint")}</p>
            </div>
            {Object.entries(rowParams).map(([key, sample]) => (
              <div key={key} className="flex flex-col gap-1">
                <Label htmlFor={`review-input-param-${key}`} className="font-mono text-xs">
                  {key} · {t(`admin.skills.${paramTypeKey(sample)}`)}
                </Label>
                {typeof sample === "boolean" ? (
                  <Checkbox
                    id={`review-input-param-${key}`}
                    checked={form.paramDrafts[key] === "true"}
                    onCheckedChange={(checked) => {
                      setParamInvalid(false);
                      setForm({ ...form, paramDrafts: { ...form.paramDrafts, [key]: checked ? "true" : "false" } });
                    }}
                    data-testid={`review-input-param-${key}`}
                  />
                ) : (
                  <Input
                    id={`review-input-param-${key}`}
                    value={form.paramDrafts[key] ?? ""}
                    type={typeof sample === "number" ? "number" : "text"}
                    min={typeof sample === "number" ? 1 : undefined}
                    step={typeof sample === "number" ? 1 : undefined}
                    onChange={(event) => {
                      setParamInvalid(false);
                      setForm({ ...form, paramDrafts: { ...form.paramDrafts, [key]: event.target.value } });
                    }}
                    data-testid={`review-input-param-${key}`}
                  />
                )}
              </div>
            ))}
            {paramInvalid ? (
              <p className="text-destructive text-xs" data-testid="review-input-param-error">
                {t("admin.skills.parametersInvalid")}
              </p>
            ) : null}
          </div>
        ) : null
      }
      editorValue={form.editor}
      onEditorChange={(editor) => { setForm({ ...form, editor }); }}
      editorDisabled={editingBuiltin}
      editorHeaderAction={
        <div className="flex flex-wrap items-center gap-2">
          {!editingBuiltin ? (
            <PendingButton
              size="xs"
              variant="outline"
              pending={optimizePrompt.isPending}
              disabled={
                form.editor.definition.knowledge_text.trim() === "" &&
                form.description.trim() === "" &&
                form.name.trim() === ""
              }
              onClick={handleOptimizePrompt}
              data-testid="skill-optimize-prompt"
            >
              <Sparkles className="size-3.5" aria-hidden="true" />
              {optimizePrompt.isPending
                ? t("admin.skills.optimizePromptPending")
                : t("admin.skills.optimizePrompt")}
            </PendingButton>
          ) : null}
          <Button
            type="button"
            size="xs"
            variant="outline"
            disabled={form.editor.definition.knowledge_text.trim() === ""}
            onClick={() => { setSkillTestOpen(true); }}
            data-testid="skill-test-open"
          >
            <FlaskConical className="size-3.5" aria-hidden="true" />
            {t("admin.skills.testSkill")}
          </Button>
        </div>
      }
      errorKey={errorKey}
      errorRequestId={errorRequestId}
      pending={mutation.isPending}
      onSubmit={submit}
      onClose={onClose}
      trailing={
        skillTestOpen ? (
          <SkillTestDialog
            name={form.name}
            description={form.description}
            engine={form.engine}
            parameters={convertParamDrafts(form.paramDrafts, rowParams) ?? {}}
            knowledgeText={form.editor.definition.knowledge_text}
            onClose={() => { setSkillTestOpen(false); }}
          />
        ) : null
      }
    />
  );
}

function SkillTestDialog({
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
