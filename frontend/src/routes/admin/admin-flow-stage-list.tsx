import type { Dispatch, SetStateAction } from "react";
import { useTranslation } from "react-i18next";
import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import type { Datasource, Flow, User } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  ActorPicker,
  ApprovalStepsEditor,
} from "@/routes/admin/admin-flow-editors";
import { Button } from "@/shared/components/ui/button";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";

export interface StageDraft {
  position: number;
  datasourceId: string;
  approvalSteps: { position: number; actorIds: string[] }[];
  executionActorIds: string[];
}

export interface FlowFormState {
  name: string;
  enabled: boolean;
  ruleSetId: string;
  stages: StageDraft[];
  queryCapabilities: { datasourceId: string; canExport: boolean }[];
  approvalSteps: { position: number; actorIds: string[] }[];
}

const EMPTY_FORM: FlowFormState = {
  name: "",
  enabled: true,
  ruleSetId: "",
  stages: [],
  queryCapabilities: [],
  approvalSteps: [],
};

export function initialFlowForm(editing: Flow | null): FlowFormState {
  if (editing === null) return { ...EMPTY_FORM };
  return {
    name: editing.name,
    enabled: editing.enabled,
    ruleSetId: editing.rule_set_id ?? "",
    stages: (editing.stages ?? []).map((stage) => ({
      position: stage.position,
      datasourceId: stage.datasource_id,
      approvalSteps: stage.approval_steps.map((step) => ({
        position: step.position,
        actorIds: step.actors.map((actor) => actor.user_id),
      })),
      executionActorIds: stage.execution_actors.map((actor) => actor.user_id),
    })),
    queryCapabilities: (editing.query_capabilities ?? []).map((capability) => ({
      datasourceId: capability.datasource_id,
      canExport: capability.can_export,
    })),
    approvalSteps: (editing.approval_steps ?? []).map((step) => ({
      position: step.position,
      actorIds: step.actors.map((actor) => actor.user_id),
    })),
  };
}

export function ChangeFlowStagesSection({
  stages,
  datasources,
  users,
  stageDsSearchByIndex,
  setStageDsSearchByIndex,
  setForm,
}: {
  stages: StageDraft[];
  datasources: Datasource[];
  users: User[];
  stageDsSearchByIndex: Record<number, string>;
  setStageDsSearchByIndex: Dispatch<SetStateAction<Record<number, string>>>;
  setForm: Dispatch<SetStateAction<FlowFormState>>;
}) {
  const { t } = useTranslation();

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <Label>
          {t("adminFlows.stages")}
          <span className="text-destructive" aria-hidden>*</span>
        </Label>
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            setForm((current) => ({
              ...current,
              stages: [
                ...current.stages,
                {
                  position: current.stages.length + 1,
                  datasourceId: "",
                  approvalSteps: [{ position: 1, actorIds: [] }],
                  executionActorIds: [],
                },
              ],
            }));
          }}
          data-testid="flow-add-stage"
        >
          <Plus />
          {t("adminFlows.addStage")}
        </Button>
      </div>
      {stages.map((stage, stageIndex) => (
        <div key={stageIndex} className="flex flex-col gap-2 rounded-md border p-3" data-testid={`flow-stage-${String(stageIndex)}`}>
          <div className="flex items-center justify-between gap-2">
            <span className="text-sm font-medium">{t("adminFlows.stageTitle", { index: stage.position })}</span>
            <div className="flex gap-1">
              <Button
                size="icon"
                variant="ghost"
                disabled={stageIndex === 0}
                onClick={() => {
                  setForm((current) => {
                    const upper = current.stages[stageIndex - 1];
                    const lower = current.stages[stageIndex];
                    if (upper === undefined || lower === undefined) return current;
                    const nextStages = [...current.stages];
                    nextStages[stageIndex - 1] = lower;
                    nextStages[stageIndex] = upper;
                    return { ...current, stages: nextStages.map((item, index) => ({ ...item, position: index + 1 })) };
                  });
                }}
                aria-label={t("adminFlows.stageUp", { index: String(stage.position) })}
                data-testid={`flow-stage-up-${String(stageIndex)}`}
              >
                <ArrowUp />
              </Button>
              <Button
                size="icon"
                variant="ghost"
                disabled={stageIndex === stages.length - 1}
                onClick={() => {
                  setForm((current) => {
                    const upper = current.stages[stageIndex];
                    const lower = current.stages[stageIndex + 1];
                    if (upper === undefined || lower === undefined) return current;
                    const nextStages = [...current.stages];
                    nextStages[stageIndex] = lower;
                    nextStages[stageIndex + 1] = upper;
                    return { ...current, stages: nextStages.map((item, index) => ({ ...item, position: index + 1 })) };
                  });
                }}
                aria-label={t("adminFlows.stageDown", { index: String(stage.position) })}
                data-testid={`flow-stage-down-${String(stageIndex)}`}
              >
                <ArrowDown />
              </Button>
              <Button
                size="icon"
                variant="ghost"
                onClick={() => {
                  setForm((current) => ({
                    ...current,
                    stages: current.stages
                      .filter((_, index) => index !== stageIndex)
                      .map((item, index) => ({ ...item, position: index + 1 })),
                  }));
                }}
                aria-label={t("adminFlows.stageRemove", { index: String(stage.position) })}
                data-testid={`flow-stage-remove-${String(stageIndex)}`}
              >
                <Trash2 />
              </Button>
            </div>
          </div>
          <div className="flex flex-col gap-2">
            <Label>
              {t("adminFlows.stageDatasource")}
              <span className="text-destructive" aria-hidden>*</span>
            </Label>
            <Select
              value={stage.datasourceId === "" ? null : stage.datasourceId}
              onValueChange={(value) => {
                setForm((current) => {
                  const nextStages = [...current.stages];
                  nextStages[stageIndex] = { ...stage, datasourceId: value ?? "" };
                  return { ...current, stages: nextStages };
                });
              }}
              items={Object.fromEntries(datasources.map((datasource) => [datasource.id, datasource.name]))}
            >
              <SelectTrigger
                className="h-9 w-full text-sm"
                aria-label={t("adminFlows.stageDatasource", { index: String(stage.position) })}
                data-testid={`flow-stage-datasource-${String(stageIndex)}`}
              >
                <SelectValue placeholder={t("adminFlows.pickDatasource")} />
              </SelectTrigger>
              <SelectContent>
                <div className="p-1">
                  <Input
                    value={stageDsSearchByIndex[stageIndex] ?? ""}
                    onChange={(event) => {
                      const val = event.target.value;
                      setStageDsSearchByIndex((prev) => ({ ...prev, [stageIndex]: val }));
                    }}
                    onKeyDown={(event) => {
                      event.stopPropagation();
                    }}
                    placeholder={t("adminFlows.datasourceSearch")}
                    aria-label={t("adminFlows.datasourceSearch")}
                    className="h-7 text-xs"
                    maxLength={64}
                    data-testid={`flow-stage-datasource-search-${String(stageIndex)}`}
                  />
                </div>
                {(() => {
                  const stageDsQuery = (stageDsSearchByIndex[stageIndex] ?? "").trim().toLowerCase();
                  const visibleStageDatasources =
                    stageDsQuery === ""
                      ? datasources
                      : datasources.filter((datasource) =>
                          datasource.name.toLowerCase().includes(stageDsQuery) ||
                          datasource.engine.toLowerCase().includes(stageDsQuery),
                        );
                  if (visibleStageDatasources.length === 0) {
                    return (
                      <div className="text-muted-foreground px-2 py-1.5 text-xs">
                        {t("adminFlows.datasourceSearchEmpty")}
                      </div>
                    );
                  }
                  return visibleStageDatasources.map((datasource) => (
                    <SelectItem key={datasource.id} value={datasource.id}>
                      {datasource.enabled
                        ? datasource.name
                        : `${datasource.name} (${t("adminFlows.disabled")})`}
                    </SelectItem>
                  ));
                })()}
              </SelectContent>
            </Select>
            {(() => {
              const selectedDs = datasources.find((item) => item.id === stage.datasourceId);
              if (selectedDs === undefined || selectedDs.enabled) return null;
              return (
                <p
                  className="text-destructive text-xs"
                  data-testid={`flow-stage-datasource-disabled-${String(stageIndex)}`}
                >
                  {t("adminFlows.disabledDatasourceWarning", { name: selectedDs.name })}
                </p>
              );
            })()}
          </div>
          <ApprovalStepsEditor
            steps={stage.approvalSteps}
            users={users}
            onChange={(steps) => {
              setForm((current) => {
                const nextStages = [...current.stages];
                nextStages[stageIndex] = { ...stage, approvalSteps: steps };
                return { ...current, stages: nextStages };
              });
            }}
            testIdPrefix={`flow-stage-${String(stageIndex)}`}
          />
          <ActorPicker
            label={t("adminFlows.executionActors")}
            users={users}
            selected={stage.executionActorIds}
            onChange={(ids) => {
              setForm((current) => {
                const nextStages = [...current.stages];
                nextStages[stageIndex] = { ...stage, executionActorIds: ids };
                return { ...current, stages: nextStages };
              });
            }}
            testIdPrefix={`flow-stage-${String(stageIndex)}-exec`}
          />
        </div>
      ))}
    </div>
  );
}
