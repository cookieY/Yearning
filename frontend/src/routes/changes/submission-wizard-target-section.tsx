import { useTranslation } from "react-i18next";
import type {
  ChangeDraft,
  Flow,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  FlowPicker,
  FormSection,
  MetaFields,
} from "@/features/review/submission-dialogs";
import { StagePath } from "@/features/review/stage-path";
import { EmptyState } from "@/shared/components/status/empty-state";
import { LoadingState } from "@/shared/components/status/loading-state";
import { ErrorState } from "@/shared/components/status/status-components";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Label } from "@/shared/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { RefreshCw, X } from "lucide-react";

export type FlowStageItem = NonNullable<Flow["stages"]>[number];

export function resolveFlowTargetDatabase(
  flow: Flow | null,
  targetDatabase: string,
  activeDraftId: string | null,
) {
  const primaryTargetStage: FlowStageItem | null =
    flow?.stages?.find(
      (stage) =>
        stage.datasource_compatibility_mode === "mysql" ||
        stage.datasource_compatibility_mode === "postgresql",
    ) ?? null;
  const isPostgresTargetStage =
    primaryTargetStage?.datasource_compatibility_mode === "postgresql";
  const connectedPostgresDatabase = isPostgresTargetStage
    ? (primaryTargetStage.datasource_database_name?.trim() ?? "")
    : "";
  // PostgreSQL datasources bind datasource_database_name to the connection Database, never a Schema.
  const stageDefaultDatabase = !isPostgresTargetStage
    ? (primaryTargetStage?.datasource_database_name?.trim() ?? "")
    : "";
  const trimmedTargetDatabase = targetDatabase.trim();
  const effectiveTargetDatabase =
    trimmedTargetDatabase !== "" ? trimmedTargetDatabase : stageDefaultDatabase;
  const resolvedTargetDatabase =
    flow === null
      ? trimmedTargetDatabase
      : primaryTargetStage === null
        ? ""
        : effectiveTargetDatabase;
  const targetDatabaseForRequest =
    activeDraftId !== null
      ? trimmedTargetDatabase === ""
        ? null
        : trimmedTargetDatabase
      : trimmedTargetDatabase === ""
        ? undefined
        : trimmedTargetDatabase;

  return {
    primaryTargetStage,
    isPostgresTargetStage,
    connectedPostgresDatabase,
    resolvedTargetDatabase,
    targetDatabaseForRequest,
  };
}

interface TargetDatabaseFieldProps {
  primaryTargetStage: FlowStageItem | null;
  isPostgresTargetStage: boolean;
  connectedPostgresDatabase: string;
  isSubmitted: boolean;
  isRollbackDraft: boolean;
  targetDatabase: string;
  resolvedTargetDatabase: string;
  schemaOptions: string[];
  schemasRequested: boolean;
  schemasPending: boolean;
  schemasError: boolean;
  schemasSuccess: boolean;
  schemasFetching: boolean;
  onRequestSchemas: () => void;
  onRefetchSchemas: () => void;
  onUpdateTargetDatabase: (next: string) => void;
}

function TargetDatabaseField({
  primaryTargetStage,
  isPostgresTargetStage,
  connectedPostgresDatabase,
  isSubmitted,
  isRollbackDraft,
  targetDatabase,
  resolvedTargetDatabase,
  schemaOptions,
  schemasRequested,
  schemasPending,
  schemasError,
  schemasSuccess,
  schemasFetching,
  onRequestSchemas,
  onRefetchSchemas,
  onUpdateTargetDatabase,
}: TargetDatabaseFieldProps) {
  const { t } = useTranslation();
  if (primaryTargetStage === null) return null;
  const fieldLabel = t(
    isPostgresTargetStage
      ? "precheck.wizard.targetSchemaLabel"
      : "precheck.wizard.targetDatabaseLabel",
  );
  const fieldHint = isPostgresTargetStage
    ? connectedPostgresDatabase !== ""
      ? t("precheck.wizard.targetSchemaHintWithDb", { database: connectedPostgresDatabase })
      : t("precheck.wizard.targetSchemaHint")
    : t("precheck.wizard.targetDatabaseHint");
  const canEditTarget = !isSubmitted && !isRollbackDraft;
  const itemsMap = Object.fromEntries(schemaOptions.map((name) => [name, name]));
  return (
    <div className="mt-4 flex flex-col gap-1.5" data-testid="wizard-target-database-section">
      <div className="flex flex-wrap items-center gap-2">
        <Label htmlFor="wizard-target-database">
          {fieldLabel}
        </Label>
        {isPostgresTargetStage && (
          <Badge
            variant="secondary"
            className="text-xs font-normal"
            data-testid="wizard-pg-schema-notice"
          >
            {t("precheck.wizard.targetSchemaScopeBadge")}
          </Badge>
        )}
      </div>
      <div className="flex items-center gap-2">
        <Select
          modal={false}
          value={resolvedTargetDatabase === "" ? null : resolvedTargetDatabase}
          items={itemsMap}
          disabled={!canEditTarget}
          onOpenChange={(open) => {
            if (open) {
              onRequestSchemas();
            }
          }}
          onValueChange={(value) => {
            onUpdateTargetDatabase(value ?? "");
          }}
        >
          <SelectTrigger
            id="wizard-target-database"
            className="h-9 w-full font-mono text-sm sm:max-w-sm"
            aria-label={fieldLabel}
            data-testid="wizard-target-database"
          >
            <SelectValue
              placeholder={t(
                isPostgresTargetStage
                  ? "precheck.wizard.targetSchemaPlaceholder"
                  : "precheck.wizard.targetDatabasePlaceholder",
              )}
            />
          </SelectTrigger>
          <SelectContent>
            {schemasRequested && schemasPending && (
              <div
                className="text-muted-foreground px-2 py-3 text-center text-xs"
                data-testid="wizard-target-database-loading"
              >
                {t(
                  isPostgresTargetStage
                    ? "precheck.wizard.targetSchemaLoading"
                    : "precheck.wizard.targetDatabaseLoading",
                )}
              </div>
            )}
            {schemasRequested && schemasError && (
              <div
                className="text-destructive px-2 py-3 text-center text-xs"
                data-testid="wizard-target-database-error"
              >
                {t(
                  isPostgresTargetStage
                    ? "precheck.wizard.targetSchemaError"
                    : "precheck.wizard.targetDatabaseError",
                )}
              </div>
            )}
            {schemasRequested && schemasSuccess && schemaOptions.length === 0 && (
              <div
                className="text-muted-foreground px-2 py-3 text-center text-xs"
                data-testid="wizard-target-database-empty"
              >
                {t(
                  isPostgresTargetStage
                    ? "precheck.wizard.targetSchemaEmpty"
                    : "precheck.wizard.targetDatabaseEmpty",
                )}
              </div>
            )}
            {schemaOptions.map((name) => (
              <SelectItem
                key={name}
                value={name}
                data-testid={`wizard-target-database-option-${name}`}
              >
                {name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {canEditTarget && targetDatabase.trim() !== "" && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-9 shrink-0 gap-1 px-2 text-xs"
            onClick={() => {
              onUpdateTargetDatabase("");
            }}
            data-testid="wizard-target-database-clear"
          >
            <X className="size-3" aria-hidden />
            {t("precheck.wizard.targetDatabaseClear")}
          </Button>
        )}
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-9 shrink-0 gap-1 px-2 text-xs"
          disabled={schemasFetching}
          onClick={onRefetchSchemas}
          data-testid="wizard-target-database-refresh"
        >
          <RefreshCw
            className={`size-3 ${schemasFetching ? "animate-spin" : ""}`}
            aria-hidden
          />
          {t("precheck.wizard.targetDatabaseRefresh")}
        </Button>
      </div>
      <p className="text-muted-foreground text-xs" data-testid="wizard-target-database-hint">
        {fieldHint}
      </p>
    </div>
  );
}

export interface SubmissionWizardFlowSectionProps extends TargetDatabaseFieldProps {
  mode: "create" | "draft";
  adopted: boolean;
  flow: Flow | null;
  draft: ChangeDraft | null;
  flows: Flow[];
  flowsPending: boolean;
  flowsError: boolean;
  flowsErrorDetail: unknown;
  flowsSuccess: boolean;
  selectedFlowId: string | null;
  onRetryFlows: () => void;
  onSelectFlow: (flowId: string) => void;
}

export function SubmissionWizardFlowSection({
  mode,
  adopted,
  flow,
  draft,
  flows,
  flowsPending,
  flowsError,
  flowsErrorDetail,
  flowsSuccess,
  selectedFlowId,
  onRetryFlows,
  onSelectFlow,
  ...targetProps
}: SubmissionWizardFlowSectionProps) {
  const { t } = useTranslation();

  if (mode === "draft" || adopted) {
    return (
      <FormSection
        title={t("precheck.wizard.section.flow")}
        hint={t("precheck.wizard.section.flowFrozenHint")}
      >
        <div className="rounded-xl border p-4" data-testid="wizard-flow-frozen">
          <p className="text-sm font-semibold">{flow?.name ?? draft?.flow_id}</p>
          <div className="mt-2">
            <StagePath flow={flow} />
          </div>
        </div>
        <TargetDatabaseField {...targetProps} />
      </FormSection>
    );
  }

  return (
    <FormSection
      title={t("precheck.wizard.section.flow")}
      hint={t("precheck.wizard.section.flowHint")}
    >
      {flowsPending && <LoadingState variant="table" columns={3} />}
      {flowsError && (
        <ErrorState
          error={flowsErrorDetail}
          operationId="listCurrentUserFlows"
          onRetry={onRetryFlows}
        />
      )}
      {flowsSuccess && flows.length === 0 && (
        <div className="rounded-lg border">
          <EmptyState
            title={t("precheck.new.empty")}
            testId="wizard-flow-empty"
          />
        </div>
      )}
      {flows.length > 0 && (
        <FlowPicker
          flows={flows}
          selectedFlowId={selectedFlowId}
          onSelect={onSelectFlow}
        />
      )}
      <TargetDatabaseField {...targetProps} />
    </FormSection>
  );
}

export interface SubmissionWizardMetaSectionProps {
  metaBound: boolean;
  metaTitle: string;
  createTitle: string;
  metaDescription: string;
  createDescription: string;
  metaRequestedFor: string;
  createRequestedFor: string;
  requestedForError: string | null;
  metaRollbackEnabled: boolean;
  createRollbackEnabled: boolean;
  stepError: string | null;
  draft: ChangeDraft | null;
  onTitleChange: (value: string) => void;
  onDescriptionChange: (value: string) => void;
  onRequestedForChange: (value: string) => void;
  onRollbackEnabledChange: (checked: boolean) => void;
}

export function SubmissionWizardMetaSection({
  metaBound,
  metaTitle,
  createTitle,
  metaDescription,
  createDescription,
  metaRequestedFor,
  createRequestedFor,
  requestedForError,
  metaRollbackEnabled,
  createRollbackEnabled,
  stepError,
  draft,
  onTitleChange,
  onDescriptionChange,
  onRequestedForChange,
  onRollbackEnabledChange,
}: SubmissionWizardMetaSectionProps) {
  const { t } = useTranslation();

  return (
    <FormSection
      title={t("precheck.wizard.section.meta")}
      hint={t("precheck.wizard.section.metaHint")}
    >
      <MetaFields
        title={metaBound ? metaTitle : createTitle}
        description={metaBound ? metaDescription : createDescription}
        onTitleChange={onTitleChange}
        onDescriptionChange={onDescriptionChange}
        requestedFor={metaBound ? metaRequestedFor : createRequestedFor}
        onRequestedForChange={onRequestedForChange}
        requestedForError={requestedForError}
        titleError={stepError}
        rollbackEnabled={metaBound ? metaRollbackEnabled : createRollbackEnabled}
        onRollbackEnabledChange={onRollbackEnabledChange}
        isRollbackDraft={draft?.ticket_type === "rollback"}
        sourceOrderId={draft?.source_order_id ?? null}
        sourceOrderDisplayNumber={draft?.source_order_display_number ?? null}
      />
    </FormSection>
  );
}
