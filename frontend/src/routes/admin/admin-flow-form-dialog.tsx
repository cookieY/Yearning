import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { Database } from "lucide-react";
import type { Flow } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useCreateFlow,
  useDatasources,
  useReplaceFlow,
  useRuleSets,
  useUsers,
} from "@/features/admin/use-admin";
import { useSession } from "@/features/auth/session-provider";
import {
  ApprovalStepsEditor,
  MaskingRulesSection,
} from "@/routes/admin/admin-flow-editors";
import {
  ChangeFlowStagesSection,
  type FlowFormState,
  initialFlowForm,
} from "@/routes/admin/admin-flow-stage-list";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import { Checkbox } from "@/shared/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";

export function FlowFormDialog({
  flowType,
  editing,
  onClose,
}: {
  flowType: "change_review" | "query_access";
  editing: Flow | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const session = useSession();
  const isAdmin = session.user?.can_access_admin === true;
  const datasourcesQuery = useDatasources(isAdmin);
  const usersQuery = useUsers(isAdmin);
  const ruleSetsQuery = useRuleSets(isAdmin);
  const createFlow = useCreateFlow();
  const replaceFlow = useReplaceFlow();

  const [form, setForm] = useState<FlowFormState>(() => initialFlowForm(editing));
  const [errorText, setErrorText] = useState<string | null>(null);
  const [queryDsSearch, setQueryDsSearch] = useState("");
  const [stageDsSearchByIndex, setStageDsSearchByIndex] = useState<Record<number, string>>({});

  const datasources = useMemo(() => datasourcesQuery.data?.items ?? [], [datasourcesQuery.data]);
  const users = usersQuery.data?.items ?? [];
  const ruleSets = ruleSetsQuery.data?.items ?? [];

  // BUG-005 Option A: frontend mirror of the same-engine validation (backed by authoritative backend check).
  const engineMismatch = useMemo(() => {
    if (flowType !== "change_review") return false;
    const engines = form.stages
      .map((stage) => datasources.find((ds) => ds.id === stage.datasourceId))
      .map((ds) => (ds ? `${ds.engine}/${ds.compatibility_mode}` : ""));
    return engines.length >= 2 && engines.some((e) => e !== engines[0]);
  }, [datasources, flowType, form]);

  const valid = useMemo(() => {
    if (form.name.trim() === "") return false;
    if (flowType === "change_review") {
      if (form.ruleSetId === "") return false;
      if (engineMismatch) return false;
      return (
        form.stages.length >= 1 &&
        form.stages.every((stage) => {
          const ds = datasources.find((item) => item.id === stage.datasourceId);
          return (
            stage.datasourceId !== "" &&
            ds !== undefined &&
            ds.enabled &&
            stage.approvalSteps.length >= 1 &&
            stage.approvalSteps.length <= 10 &&
            stage.approvalSteps.every((step) => step.actorIds.length >= 1) &&
            stage.executionActorIds.length >= 1
          );
        })
      );
    }
    return (
      form.queryCapabilities.length >= 1 &&
      form.queryCapabilities.every((capability) => {
        const ds = datasources.find((item) => item.id === capability.datasourceId);
        return ds !== undefined && ds.enabled;
      }) &&
      form.approvalSteps.length >= 1 &&
      form.approvalSteps.length <= 10 &&
      form.approvalSteps.every((step) => step.actorIds.length >= 1)
    );
  }, [datasources, engineMismatch, flowType, form]);

  const missingFlowItems = useMemo(() => {
    const items: string[] = [];
    if (form.name.trim() === "") {
      items.push(t("adminFlows.column.name"));
    }
    if (flowType === "change_review") {
      if (form.ruleSetId === "") {
        items.push(t("adminFlows.ruleSet"));
      }
      if (form.stages.length < 1) {
        items.push(t("adminFlows.missingStages"));
      }
      for (const stage of form.stages) {
        const index = String(stage.position);
        const ds = datasources.find((item) => item.id === stage.datasourceId);
        if (stage.datasourceId === "" || ds === undefined) {
          items.push(t("adminFlows.missingStageDatasource", { index }));
        } else if (!ds.enabled) {
          items.push(t("adminFlows.missingStageDisabledDatasource", { index }));
        }
        const stepsValid =
          stage.approvalSteps.length >= 1 &&
          stage.approvalSteps.length <= 10 &&
          stage.approvalSteps.every((step) => step.actorIds.length >= 1);
        if (!stepsValid) {
          items.push(t("adminFlows.missingStageApprovalSteps", { index }));
        }
        if (stage.executionActorIds.length < 1) {
          items.push(t("adminFlows.missingStageExecutors", { index }));
        }
      }
    } else {
      if (form.queryCapabilities.length < 1) {
        items.push(t("adminFlows.missingQueryDatasources"));
      } else if (
        form.queryCapabilities.some((capability) => {
          const ds = datasources.find((item) => item.id === capability.datasourceId);
          return ds === undefined || !ds.enabled;
        })
      ) {
        items.push(t("adminFlows.missingQueryDisabledDatasource"));
      }
      const stepsValid =
        form.approvalSteps.length >= 1 &&
        form.approvalSteps.length <= 10 &&
        form.approvalSteps.every((step) => step.actorIds.length >= 1);
      if (!stepsValid) {
        items.push(t("adminFlows.missingQueryApprovalSteps"));
      }
    }
    return items;
  }, [datasources, flowType, form, t]);

  const submit = async () => {
    setErrorText(null);
    const body =
      flowType === "change_review"
        ? {
            name: form.name,
            flow_type: flowType,
            enabled: form.enabled,
            rule_set_id: form.ruleSetId === "" ? null : form.ruleSetId,
            stages: form.stages.map((stage) => ({
              position: stage.position,
              datasource_id: stage.datasourceId,
              approval_steps: stage.approvalSteps.map((step) => ({
                position: step.position,
                actors: step.actorIds.map((user_id) => ({ user_id })),
              })),
              execution_actors: stage.executionActorIds.map((user_id) => ({ user_id })),
            })),
          }
        : {
            name: form.name,
            flow_type: flowType,
            enabled: form.enabled,
            query_capabilities: form.queryCapabilities.map((capability) => ({
              datasource_id: capability.datasourceId,
              can_query: true as const,
              can_export: capability.canExport,
            })),
            approval_steps: form.approvalSteps.map((step) => ({
              position: step.position,
              actors: step.actorIds.map((user_id) => ({ user_id })),
            })),
          };
    try {
      if (editing === null) {
        await createFlow.mutateAsync(body);
      } else {
        await replaceFlow.mutateAsync({ flowId: editing.id, version: editing.version, body });
      }
      toast.success(editing === null ? t("common.createdToast") : t("common.savedToast"), { description: form.name });
      onClose();
    } catch (error) {
      const msg = describeErrorText(describeError(error, editing === null ? "createFlow" : "replaceFlow"));
      setErrorText(msg);
      toast.error(msg);
    }
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent
        className="max-h-[min(760px,86vh)] sm:max-w-3xl"
        viewportClassName="max-h-[calc(min(760px,86vh)-10.5rem)]"
      >
        <DialogHeader>
          <DialogTitle>
            {editing === null
              ? flowType === "change_review"
                ? t("adminFlows.createChange")
                : t("adminFlows.createQuery")
              : t("adminFlows.editTitle", { name: editing.name })}
          </DialogTitle>
          <DialogDescription>{t("adminFlows.formDescription")}</DialogDescription>
        </DialogHeader>

        <Alert>
          <AlertDescription>{t("adminFlows.noRetroactiveNote")}</AlertDescription>
        </Alert>

        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="flow-name">
              {t("adminFlows.column.name")}
              <span className="text-destructive" aria-hidden>*</span>
            </Label>
            <Input
              id="flow-name"
              value={form.name}
              onChange={(event) => { setForm({ ...form, name: event.target.value }); }}
              maxLength={128}
              aria-required="true"
              data-testid="flow-name"
            />
          </div>
          <label className="flex cursor-pointer items-center gap-2 text-sm">
            <Checkbox
              checked={form.enabled}
              onCheckedChange={(checked) => { setForm({ ...form, enabled: checked }); }}
              data-testid="flow-enabled"
            />
            {t("adminFlows.column.enabled")}
          </label>

          {flowType === "change_review" ? (
            <>
              <div className="flex flex-col gap-2">
                <Label>
                  {t("adminFlows.ruleSet")}
                  <span className="text-destructive" aria-hidden>*</span>
                </Label>
                {/* §18.59: Base UI Select (owner ruling "must use UI components"). */}
                <Select
                  value={form.ruleSetId === "" ? null : form.ruleSetId}
                  onValueChange={(value) => { setForm({ ...form, ruleSetId: value ?? "" }); }}
                  items={Object.fromEntries(ruleSets.map((ruleSet) => [ruleSet.id, ruleSet.name]))}
                >
                  <SelectTrigger
                    className="h-9 w-full text-sm"
                    aria-label={t("adminFlows.ruleSet")}
                    aria-required="true"
                    data-testid="flow-rule-set"
                  >
                    <SelectValue placeholder={t("adminFlows.noRuleSet")} />
                  </SelectTrigger>
                  <SelectContent>
                    {ruleSets.map((ruleSet) => (
                      <SelectItem key={ruleSet.id} value={ruleSet.id}>
                        {ruleSet.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground text-xs" data-testid="flow-rule-set-hint">
                  {t("adminFlows.ruleSetHint")}
                </p>
                {!ruleSetsQuery.isPending && ruleSets.length === 0 && (
                  <Alert variant="destructive" data-testid="flow-rule-set-empty-warning">
                    <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
                      <span>{t("adminFlows.ruleSetEmptyWarning")}</span>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => {
                          onClose();
                          void navigate("/admin/rule-sets");
                        }}
                        data-testid="flow-go-to-rule-sets"
                      >
                        {t("adminFlows.goToRuleSets")}
                      </Button>
                    </AlertDescription>
                  </Alert>
                )}
              </div>

              <ChangeFlowStagesSection
                stages={form.stages}
                datasources={datasources}
                users={users}
                stageDsSearchByIndex={stageDsSearchByIndex}
                setStageDsSearchByIndex={setStageDsSearchByIndex}
                setForm={setForm}
              />
            </>
          ) : (
            <>
              <div className="flex flex-col gap-2">
                <Label>{t("adminFlows.queryDatasources")}</Label>
                <p className="text-muted-foreground text-xs">{t("adminFlows.queryDatasourcesHint")}</p>
                <div className="flex flex-col gap-1.5 rounded-md border p-2">
                  <Input
                    value={queryDsSearch}
                    onChange={(event) => { setQueryDsSearch(event.target.value); }}
                    placeholder={t("adminFlows.datasourceSearch")}
                    aria-label={t("adminFlows.datasourceSearch")}
                    className="h-7 text-xs"
                    maxLength={64}
                    data-testid="flow-query-ds-search"
                  />
                  <ScrollArea viewportClassName="max-h-48">
                    <div className="flex flex-col gap-0.5 pr-3">
                      {(() => {
                        const normalizedDsQuery = queryDsSearch.trim().toLowerCase();
                        const visibleQueryDatasources =
                          normalizedDsQuery === ""
                            ? datasources
                            : datasources.filter((datasource) =>
                                datasource.name.toLowerCase().includes(normalizedDsQuery) ||
                                datasource.engine.toLowerCase().includes(normalizedDsQuery),
                              );
                        if (visibleQueryDatasources.length === 0) {
                          return (
                            <span className="text-muted-foreground px-1 py-1 text-xs">
                              {t("adminFlows.datasourceSearchEmpty")}
                            </span>
                          );
                        }
                        return visibleQueryDatasources.map((datasource) => {
                          const selected = form.queryCapabilities.find((capability) => capability.datasourceId === datasource.id);
                          return (
                            <div key={datasource.id} className="flex items-center gap-2 px-1 py-1 text-sm">
                              <Checkbox
                                checked={selected !== undefined}
                                onCheckedChange={(checked) => {
                                  setForm((current) => ({
                                    ...current,
                                    queryCapabilities: checked
                                      ? [...current.queryCapabilities, { datasourceId: datasource.id, canExport: false }]
                                      : current.queryCapabilities.filter((capability) => capability.datasourceId !== datasource.id),
                                  }));
                                }}
                                data-testid={`flow-query-ds-${datasource.id}`}
                              />
                              <Database className="size-3.5 text-muted-foreground" />
                              {datasource.name}
                              {selected !== undefined && (
                                <label className="ml-auto flex cursor-pointer items-center gap-1 text-xs">
                                  <Checkbox
                                    checked={selected.canExport}
                                    onCheckedChange={(checked) => {
                                      setForm((current) => ({
                                        ...current,
                                        queryCapabilities: current.queryCapabilities.map((capability) =>
                                          capability.datasourceId === datasource.id
                                            ? { ...capability, canExport: checked }
                                            : capability,
                                        ),
                                      }));
                                    }}
                                    data-testid={`flow-query-export-${datasource.id}`}
                                  />
                                  {t("adminFlows.canExport")}
                                </label>
                              )}
                            </div>
                          );
                        });
                      })()}
                    </div>
                  </ScrollArea>
                </div>
              </div>
              <ApprovalStepsEditor
                steps={form.approvalSteps}
                users={users}
                onChange={(steps) => { setForm({ ...form, approvalSteps: steps }); }}
                testIdPrefix="flow-query"
              />
              {editing !== null && <MaskingRulesSection flow={editing} />}
            </>
          )}

          {errorText !== null && (
            <Alert variant="destructive" data-testid="flow-form-error">
              <AlertTitle>{t("adminFlows.formFailed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
          {!valid && missingFlowItems.length > 0 && (
            <p className="text-destructive text-xs font-medium" data-testid="flow-form-invalid-hint">
              {t("adminFlows.formMissingSummary", { items: missingFlowItems.join(", ") })}
            </p>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          {engineMismatch && (
            <Alert variant="destructive" data-testid="flow-engine-mismatch">
              <AlertDescription>{t("adminFlows.engineMismatch")}</AlertDescription>
            </Alert>
          )}
          <PendingButton
            pending={createFlow.isPending || replaceFlow.isPending}
            disabled={!valid}
            onClick={() => void submit()}
            data-testid="flow-form-submit"
          >
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
