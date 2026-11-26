import { useState } from "react";
import { useTranslation } from "react-i18next";
import { CircleAlert } from "lucide-react";
import type {
  KnowledgeEntry,
  KnowledgeEntryScopeType,
  KnowledgeEntryWrite,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  isAdvisorySeverity,
  type ReviewInputEditorValue,
} from "@/features/admin/review-input-editor";
import {
  type InputState,
  ReviewInputDialogFrame,
  ReviewInputStateSelect,
  StateBadge,
} from "@/features/admin/review-input-shared";
import {
  useCreateKnowledgeEntry,
  useDatasources,
  useReplaceKnowledgeEntry,
} from "@/features/admin/use-admin";
import { useSession } from "@/features/auth/session-provider";
import { describeError } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
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
import { Textarea } from "@/shared/components/ui/textarea";
import { formatTimestamp } from "@/shared/lib/timezones";

interface KnowledgeFormState {
  name: string;
  state: InputState;
  purpose: string;
  scopeType: KnowledgeEntryScopeType;
  datasourceId: string;
  databaseName: string;
  tableName: string;
  editor: ReviewInputEditorValue;
}

function initialKnowledgeForm(entry: KnowledgeEntry | null): KnowledgeFormState {
  if (entry === null) {
    return {
      name: "",
      state: "draft",
      purpose: "",
      scopeType: "datasource",
      datasourceId: "",
      databaseName: "",
      tableName: "",
      editor: {
        definition: { knowledge_text: "", max_severity: "medium", version: 1 },
        parameters: {},
      },
    };
  }
  return {
    name: entry.name,
    state: entry.state,
    purpose: entry.purpose ?? "",
    scopeType: entry.scope_type,
    datasourceId: entry.datasource_id ?? "",
    databaseName: entry.database_name ?? "",
    tableName: entry.table_name ?? "",
    editor: { definition: entry.definition, parameters: {} },
  };
}

export function KnowledgeDetailPanel({
  selected,
  draftText,
  onDraftChange,
  replaceEntry,
  onEdit,
}: {
  selected: KnowledgeEntry;
  draftText: string | null;
  onDraftChange: (next: string | null) => void;
  replaceEntry: ReturnType<typeof useReplaceKnowledgeEntry>;
  onEdit: () => void;
}) {
  const { t } = useTranslation();
  const serverText = selected.definition.knowledge_text;
  const effectiveText = draftText ?? serverText;
  const dirty = draftText !== null && draftText !== serverText;
  const validScope = selected.scope_type === "datasource" || selected.scope_type === "table";
  const validSeverity = isAdvisorySeverity(selected.definition.max_severity);
  const inlineSaveBlocked = !validScope || !validSeverity;

  const save = () => {
    if (
      (selected.scope_type !== "datasource" && selected.scope_type !== "table") ||
      !isAdvisorySeverity(selected.definition.max_severity)
    ) {
      return;
    }
    replaceEntry.mutate(
      {
        id: selected.id,
        version: selected.version,
        write: {
          name: selected.name,
          purpose: selected.purpose,
          state: selected.state,
          scope_type: selected.scope_type,
          datasource_id: selected.datasource_id,
          database_name: selected.database_name,
          table_name: selected.table_name,
          definition: {
            knowledge_text: effectiveText,
            max_severity: selected.definition.max_severity,
            version: selected.definition.version,
          },
          provenance: selected.provenance,
          source_finding_id: selected.source_finding_id ?? undefined,
        },
      },
      {
        onSuccess: () => {
          onDraftChange(null);
          appToast.success(t("common.savedToast"), { description: selected.name });
        },
        onError: (error) => {
          const display = describeError(error, "replaceKnowledgeEntry");
          appToast.error(t(display.messageKey));
        },
      },
    );
  };

  return (
    <div className="flex flex-col gap-4" data-testid="knowledge-detail">
      <div className="flex items-start justify-between gap-3">
        <h3 className="text-xl font-semibold">{selected.name}</h3>
        <Button
          variant="outline"
          size="sm"
          onClick={onEdit}
          data-testid={`review-input-edit-${selected.id}`}
        >
          {t("admin.knowledge.editTitle")}
        </Button>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <StateBadge state={selected.state} />
        <Badge variant="outline">
          {selected.scope_type === "global"
            ? t("admin.knowledge.scopeGlobalRetired")
            : selected.scope_type === "datasource"
              ? t("admin.knowledge.scopeDatasource")
              : t("admin.knowledge.scopeTable")}
        </Badge>
        <Badge variant="secondary">
          {t(`admin.reviewInput.severity_${selected.definition.max_severity}`)}
        </Badge>
        <Badge variant="secondary">
          {t(`admin.knowledge.provenance_${selected.provenance}`)}
        </Badge>
        <span className="text-muted-foreground text-xs tabular-nums">
          {formatTimestamp(selected.updated_at)}
        </span>
      </div>
      {inlineSaveBlocked ? (
        <Alert className="border-[var(--risk-warning)]/40" data-testid="knowledge-detail-legacy-notice">
          <CircleAlert aria-hidden="true" />
          <AlertTitle>{t("admin.knowledge.legacyEntryNoticeTitle")}</AlertTitle>
          <AlertDescription>{t("admin.knowledge.legacyEntryNoticeBody")}</AlertDescription>
        </Alert>
      ) : null}
      {selected.purpose ? <p className="text-sm">{selected.purpose}</p> : null}
      <div className="flex flex-col gap-2">
        <Textarea
          value={effectiveText}
          onChange={(event) => { onDraftChange(event.target.value); }}
          onBlur={() => { if (dirty && !inlineSaveBlocked) save(); }}
          disabled={inlineSaveBlocked}
          rows={8}
          data-testid="knowledge-notes-editor"
          aria-label={t("admin.knowledge.editHeader")}
        />
        {dirty && !inlineSaveBlocked ? (
          <PendingButton
            size="sm"
            pending={replaceEntry.isPending}
            onClick={save}
            data-testid="knowledge-notes-save"
          >
            {t("admin.knowledge.save")}
          </PendingButton>
        ) : null}
      </div>
    </div>
  );
}

export function KnowledgeFormDialog({
  editing,
  onClose,
}: {
  editing: KnowledgeEntry | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const createEntry = useCreateKnowledgeEntry();
  const replaceEntry = useReplaceKnowledgeEntry();
  const session = useSession();
  const datasourcesQuery = useDatasources(session.user?.can_access_admin === true);
  const [form, setForm] = useState<KnowledgeFormState>(() => initialKnowledgeForm(editing));
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const [errorRequestId, setErrorRequestId] = useState<string | null>(null);

  const onWriteError = (operationId: string) => (error: unknown) => {
    const display = describeError(error, operationId);
    setErrorKey(display.messageKey);
    setErrorRequestId(display.requestId);
    appToast.error(t(display.messageKey));
  };

  const mutation = editing === null ? createEntry : replaceEntry;
  const legacyNeedsFix =
    form.scopeType === "global" || !isAdvisorySeverity(form.editor.definition.max_severity);

  const submit = () => {
    setErrorKey(null);
    setErrorRequestId(null);
    if (
      (form.scopeType !== "datasource" && form.scopeType !== "table") ||
      !isAdvisorySeverity(form.editor.definition.max_severity)
    ) {
      return;
    }
    const write: KnowledgeEntryWrite = {
      name: form.name,
      purpose: form.purpose === "" ? undefined : form.purpose,
      state: form.state,
      scope_type: form.scopeType,
      datasource_id: form.datasourceId === "" ? undefined : form.datasourceId,
      database_name: form.databaseName === "" ? undefined : form.databaseName,
      table_name: form.scopeType === "table" ? (form.tableName === "" ? undefined : form.tableName) : undefined,
      definition: {
        knowledge_text: form.editor.definition.knowledge_text,
        max_severity: form.editor.definition.max_severity,
        version: form.editor.definition.version,
      },
      provenance: editing !== null ? editing.provenance : "manual",
      source_finding_id: editing !== null ? (editing.source_finding_id ?? undefined) : undefined,
    };
    if (editing === null) {
      createEntry.mutate(write, {
        onSuccess: () => {
          appToast.success(t("common.save"), { description: write.name });
          onClose();
        },
        onError: onWriteError("createKnowledgeEntry"),
      });
    } else {
      replaceEntry.mutate(
        { id: editing.id, version: editing.version, write },
        {
          onSuccess: () => {
            appToast.success(t("common.save"), { description: write.name });
            onClose();
          },
          onError: onWriteError("replaceKnowledgeEntry"),
        },
      );
    }
  };

  return (
    <ReviewInputDialogFrame
      title={t(editing === null ? "admin.knowledge.createTitle" : "admin.knowledge.editTitle")}
      configHash={editing?.config_hash ?? ""}
      editorMode="knowledge"
      submitDisabled={legacyNeedsFix}
      notice={
        form.scopeType === "global" ? (
          <Alert variant="destructive" data-testid="knowledge-legacy-global-warning">
            <CircleAlert aria-hidden="true" />
            <AlertTitle>{t("admin.knowledge.legacyGlobalTitle")}</AlertTitle>
            <AlertDescription>{t("admin.knowledge.legacyGlobalHint")}</AlertDescription>
          </Alert>
        ) : undefined
      }
      fields={
        <>
          <div className="flex flex-col gap-1">
            <Label htmlFor="review-input-name">{t("admin.reviewInput.name")}</Label>
            <Input
              id="review-input-name"
              value={form.name}
              onChange={(event) => { setForm({ ...form, name: event.target.value }); }}
              data-testid="review-input-name"
            />
          </div>
          <ReviewInputStateSelect
            value={form.state}
            onChange={(state) => { setForm({ ...form, state }); }}
          />
          <div className="flex flex-col gap-1">
            <Label>{t("admin.knowledge.scope")}</Label>
            <Select
              value={form.scopeType === "global" ? undefined : form.scopeType}
              onValueChange={(next) => {
                if (next === "datasource" || next === "table") {
                  setForm({ ...form, scopeType: next });
                }
              }}
              items={{
                datasource: t("admin.knowledge.scopeDatasource"),
                table: t("admin.knowledge.scopeTable"),
              }}
            >
              <SelectTrigger data-testid="knowledge-scope">
                <SelectValue>
                  {form.scopeType === "datasource"
                    ? t("admin.knowledge.scopeDatasource")
                    : form.scopeType === "table"
                      ? t("admin.knowledge.scopeTable")
                      : t("admin.knowledge.scopeGlobalRetired")}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="datasource">{t("admin.knowledge.scopeDatasource")}</SelectItem>
                <SelectItem value="table">{t("admin.knowledge.scopeTable")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="knowledge-purpose">{t("admin.knowledge.purpose")}</Label>
            <Input
              id="knowledge-purpose"
              value={form.purpose}
              onChange={(event) => { setForm({ ...form, purpose: event.target.value }); }}
              data-testid="knowledge-purpose"
            />
          </div>
          {form.scopeType !== "global" ? (
            <>
              <div className="flex flex-col gap-1">
                <Label>{t("admin.knowledge.datasource")}</Label>
                <Select
                  value={form.datasourceId}
                  onValueChange={(next) => {
                    if (next === null) return;
                    setForm({ ...form, datasourceId: next });
                  }}
                >
                  <SelectTrigger data-testid="knowledge-datasource">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(datasourcesQuery.data?.items ?? []).map((datasource: { id: string; name: string }) => (
                      <SelectItem key={datasource.id} value={datasource.id}>
                        {datasource.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="knowledge-database">{t("admin.knowledge.databaseName")}</Label>
                <Input
                  id="knowledge-database"
                  value={form.databaseName}
                  onChange={(event) => { setForm({ ...form, databaseName: event.target.value }); }}
                  data-testid="knowledge-database"
                />
              </div>
            </>
          ) : null}
          {form.scopeType === "table" ? (
            <div className="flex flex-col gap-1">
              <Label htmlFor="knowledge-table">{t("admin.knowledge.tableName")}</Label>
              <Input
                id="knowledge-table"
                value={form.tableName}
                onChange={(event) => { setForm({ ...form, tableName: event.target.value }); }}
                data-testid="knowledge-table"
              />
            </div>
          ) : null}
        </>
      }
      editorValue={form.editor}
      onEditorChange={(editor) => { setForm({ ...form, editor }); }}
      errorKey={errorKey}
      errorRequestId={errorRequestId}
      pending={mutation.isPending}
      onSubmit={submit}
      onClose={onClose}
    />
  );
}
