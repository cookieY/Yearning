import { useState } from "react";
import { AnimatePresence, motion } from "motion/react";
import { useTranslation } from "react-i18next";
import { BookOpenText, Menu, Pencil, Play, Plus, Search, Trash2 } from "lucide-react";
import type {
  KnowledgeEntry,
  KnowledgeEntryEvaluation,
  KnowledgeEntryWrite,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type { ReviewInputEditorValue } from "@/features/admin/review-input-editor";
import {
  DeleteReviewInputDialog,
  type InputState,
  ReviewInputDialogFrame,
  ReviewInputStateSelect,
  StateBadge,
} from "@/features/admin/review-input-shared";
import {
  useCreateKnowledgeEntry,
  useDatasources,
  useEvaluateKnowledgeEntry,
  useKnowledgeEntries,
  useReplaceKnowledgeEntry,
} from "@/features/admin/use-admin";
import { useSession } from "@/features/auth/session-provider";
import { describeError } from "@/shared/api/error-display";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { EmptyState } from "@/shared/components/status/empty-state";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { Alert, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card } from "@/shared/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/shared/components/ui/input-group";
import { Label } from "@/shared/components/ui/label";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { Sheet, SheetContent } from "@/shared/components/ui/sheet";
import { Textarea } from "@/shared/components/ui/textarea";
import { formatTimestamp } from "@/shared/lib/timezones";
import { cn } from "@/shared/lib/utils";

interface KnowledgeFormState {
  name: string;
  state: InputState;
  purpose: string;
  scopeType: "global" | "datasource" | "table";
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
      scopeType: "global",
      datasourceId: "",
      databaseName: "",
      tableName: "",
      editor: {
        definition: { knowledge_text: "", max_severity: "critical", version: 1 },
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

/** Internal knowledge notes master-detail page (/admin/review-engine/knowledge). */
export function KnowledgeNotesPage() {
  const { t } = useTranslation();
  const session = useSession();
  const enabled = session.user?.can_access_admin === true;
  const [knowledgeSearch, setKnowledgeSearch] = useState("");
  const entriesQuery = useKnowledgeEntries(enabled, { q: knowledgeSearch.trim() || undefined });
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<KnowledgeEntry | null>(null);
  const [deleting, setDeleting] = useState<KnowledgeEntry | null>(null);
  const [evaluating, setEvaluating] = useState<KnowledgeEntry | null>(null);
  const [evaluatingId, setEvaluatingId] = useState<string | null>(null);
  const [evalResult, setEvalResult] = useState<KnowledgeEntryEvaluation | null>(null);
  const [evalErrorKey, setEvalErrorKey] = useState<string | null>(null);
  const [selectedKnowledgeId, setSelectedKnowledgeId] = useState<string | null>(null);
  const [notesSheetOpen, setNotesSheetOpen] = useState(false);
  const [knowledgeTextDraft, setKnowledgeTextDraft] = useState<string | null>(null);
  const replaceEntry = useReplaceKnowledgeEntry();
  const evaluateMutation = useEvaluateKnowledgeEntry();

  const loading = entriesQuery.isPending;
  const errored = entriesQuery.isError;
  const filtered = entriesQuery.data?.items ?? [];
  const selected = filtered.find((entry) => entry.id === selectedKnowledgeId) ?? filtered[0] ?? null;

  return (
    <PageLayout>
      <PageHeader
        title={t("admin.knowledge.title")}
        description={t("admin.knowledge.description")}
        icon={BookOpenText}
        actions={
          <Button
            onClick={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
            data-testid="review-input-create"
          >
            <Plus /> {t("admin.knowledge.createTitle")}
          </Button>
        }
      />
      <Card className="overflow-hidden">
        <div className="flex min-h-[600px]">
          <div className="hidden w-80 shrink-0 flex-col gap-4 border-e border-border p-6 lg:flex">
            <InputGroup>
              <InputGroupAddon>
                <Search aria-hidden />
              </InputGroupAddon>
              <InputGroupInput
                value={knowledgeSearch}
                onChange={(event) => { setKnowledgeSearch(event.target.value); }}
                placeholder={t("admin.knowledge.searchPlaceholder")}
                aria-label={t("admin.knowledge.searchPlaceholder")}
                data-testid="knowledge-search"
              />
            </InputGroup>
            <div className="flex min-h-5 items-center justify-between gap-2">
              <h6 className="text-base">{t("admin.knowledge.allTitle")}</h6>
              {entriesQuery.isFetching && !entriesQuery.isPending ? (
                <LoadingState variant="inline" label={t("states.refreshing")} />
              ) : null}
            </div>
            {/* Fixed height h-[728px] shows 4 full knowledge cards cleanly within ScrollArea. */}
            <ScrollArea className="h-[728px] shrink-0">
              <div className="flex flex-col gap-3">
                <AnimatePresence initial={false} mode="popLayout">
                  {loading ? (
                    <motion.div
                      key="loading"
                      initial={{ opacity: 0, y: 16 }}
                      animate={{ opacity: 1, y: 0 }}
                      exit={{ opacity: 0, y: -16 }}
                      transition={{ duration: 0.3, ease: "easeOut" }}
                    >
                      <LoadingState variant="panel" />
                    </motion.div>
                  ) : filtered.length === 0 ? (
                    <motion.div
                      key="empty"
                      initial={{ opacity: 0, y: 16 }}
                      animate={{ opacity: 1, y: 0 }}
                      exit={{ opacity: 0, y: -16 }}
                      transition={{ duration: 0.3, ease: "easeOut" }}
                    >
                      <EmptyState
                        title={knowledgeSearch.trim() === "" ? t("admin.knowledge.emptyTitle") : t("table.emptySearch")}
                        filtered={knowledgeSearch.trim() !== ""}
                        testId="knowledge-notes-empty"
                      />
                    </motion.div>
                  ) : (
                    filtered.map((entry) => (
                      <motion.div
                        key={entry.id}
                        layout
                        initial={{ opacity: 0, y: 24 }}
                        animate={{ opacity: 1, y: 0 }}
                        exit={{ opacity: 0, y: -16 }}
                        transition={{ duration: 0.3, ease: "easeOut" }}
                      >
                        <div
                          data-testid={`review-input-row-${entry.id}`}
                          onClick={() => {
                            setSelectedKnowledgeId(entry.id);
                            setKnowledgeTextDraft(null);
                          }}
                          className={cn(
                            "cursor-pointer p-4 rounded-xl transition-[scale,opacity,background-color,border-color]",
                            entry.state === "disabled"
                              ? "bg-destructive/10 border border-destructive/20"
                              : entry.state === "draft"
                                ? "bg-muted/50 border border-border"
                                : "bg-chart-2/10 border border-chart-2/20",
                            selected?.id === entry.id
                              ? "scale-100 opacity-100"
                              : "scale-95 opacity-90",
                          )}
                        >
                          <div className="flex items-center gap-2">
                            <StateBadge state={entry.state} />
                            <span className="text-muted-foreground text-xs">
                              {t(`admin.knowledge.provenance_${entry.provenance}`)}
                            </span>
                          </div>
                          <p className="mt-1.5 truncate font-medium text-primary">{entry.name}</p>
                          {entry.purpose ? (
                            <p className="text-muted-foreground truncate text-xs">{entry.purpose}</p>
                          ) : null}
                          <p className="text-muted-foreground mt-1 text-xs">
                            {entry.scope_type === "global"
                              ? t("admin.knowledge.scopeGlobal")
                              : entry.scope_type === "datasource"
                                ? t("admin.knowledge.scopeDatasource")
                                : `${entry.database_name ?? "?"}.${entry.table_name ?? "?"}`}
                          </p>
                          <div className="mt-2 flex items-center justify-between">
                            <p className="text-muted-foreground text-xs tabular-nums">
                              {formatTimestamp(entry.updated_at)}
                            </p>
                            <div className="flex items-center gap-1">
                              <PendingButton
                                type="button"
                                variant="ghost"
                                size="sm"
                                pending={evaluateMutation.isPending && evaluatingId === entry.id}
                                disabled={evaluateMutation.isPending && evaluatingId !== entry.id}
                                onClick={(event) => {
                                  event.stopPropagation();
                                  setEvalErrorKey(null);
                                  setEvaluatingId(entry.id);
                                  evaluateMutation.mutate(
                                    { id: entry.id },
                                    {
                                      onSuccess: (result) => {
                                        setEvaluatingId(null);
                                        setEvaluating(entry);
                                        setEvalResult(result);
                                        setSelectedKnowledgeId(entry.id);
                                        appToast.success(t("admin.knowledge.evaluate"), {
                                          description: entry.name,
                                        });
                                      },
                                      onError: (error) => {
                                        setEvaluatingId(null);
                                        const display = describeError(error, "evaluateKnowledgeEntry");
                                        setEvalErrorKey(display.messageKey);
                                        appToast.error(t(display.messageKey));
                                      },
                                    },
                                  );
                                }}
                                data-testid={`review-input-evaluate-${entry.id}`}
                              >
                                <Play /> {t("admin.knowledge.evaluate")}
                              </PendingButton>
                              <Button
                                type="button"
                                variant="ghost"
                                size="icon"
                                aria-label={t("common.edit")}
                                onClick={(event) => {
                                  event.stopPropagation();
                                  setEditing(entry);
                                  setDialogOpen(true);
                                }}
                                data-testid={`review-input-edit-${entry.id}`}
                              >
                                <Pencil className="size-4" aria-hidden />
                              </Button>
                              <Button
                                type="button"
                                variant="ghost"
                                size="icon"
                                aria-label={t("common.delete")}
                                onClick={(event) => {
                                  event.stopPropagation();
                                  setDeleting(entry);
                                }}
                                data-testid={`review-input-delete-${entry.id}`}
                              >
                                <Trash2 className="size-4" aria-hidden />
                              </Button>
                            </div>
                          </div>
                        </div>
                      </motion.div>
                    ))
                  )}
                </AnimatePresence>
              </div>
            </ScrollArea>
          </div>
          <div className="flex flex-1 flex-col">
            <div className="flex items-center justify-between border-b border-border px-6 py-4">
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="lg:hidden"
                  aria-label={t("admin.knowledge.listLabel")}
                  data-testid="knowledge-list-open"
                  onClick={() => { setNotesSheetOpen(true); }}
                >
                  <Menu className="size-4" aria-hidden />
                </Button>
                <h6 className="text-base">{t("admin.knowledge.editHeader")}</h6>
              </div>
            </div>
            <div className="flex flex-1 flex-col gap-4 p-6">
              {evalErrorKey !== null ? (
                <Alert variant="destructive" data-testid="knowledge-eval-error">
                  <AlertTitle>{t(evalErrorKey)}</AlertTitle>
                </Alert>
              ) : null}
              {loading ? (
                <LoadingState variant="panel" />
              ) : errored ? (
                <ErrorState error={entriesQuery.error} operationId="listKnowledgeEntries" onRetry={() => void entriesQuery.refetch()} />
              ) : selected === null ? (
                <EmptyState
                  title={knowledgeSearch.trim() === "" ? t("admin.knowledge.emptyTitle") : t("table.emptySearch")}
                  description={knowledgeSearch.trim() === "" ? t("admin.knowledge.emptyDescription") : undefined}
                  filtered={knowledgeSearch.trim() !== ""}
                />
              ) : (
                <KnowledgeDetailPanel
                  selected={selected}
                  draftText={knowledgeTextDraft}
                  onDraftChange={setKnowledgeTextDraft}
                  replaceEntry={replaceEntry}
                  onEdit={() => {
                    setEditing(selected);
                    setDialogOpen(true);
                  }}
                />
              )}
            </div>
          </div>
        </div>
        <Sheet open={notesSheetOpen} onOpenChange={setNotesSheetOpen}>
          <SheetContent side="left" className="w-80 p-0" data-testid="knowledge-notes-sheet">
            <ScrollArea className="min-h-0 flex-1">
              <div className="flex flex-col gap-4 p-6">
                <InputGroup>
                  <InputGroupAddon>
                    <Search aria-hidden />
                  </InputGroupAddon>
                  <InputGroupInput
                    value={knowledgeSearch}
                    onChange={(event) => { setKnowledgeSearch(event.target.value); }}
                    placeholder={t("admin.knowledge.searchPlaceholder")}
                    aria-label={t("admin.knowledge.searchPlaceholder")}
                  />
                </InputGroup>
                <h6 className="text-base">{t("admin.knowledge.allTitle")}</h6>
                <div className="flex flex-col gap-3">
                  {filtered.map((entry) => (
                    <div
                      key={entry.id}
                      onClick={() => { setSelectedKnowledgeId(entry.id); setNotesSheetOpen(false); }}
                      className={cn(
                        "cursor-pointer p-4 transition-colors",
                        selected?.id === entry.id
                          ? "bg-muted/70 ring-1 ring-primary/40"
                          : "hover:bg-muted/40",
                      )}
                    >
                      <p className="truncate font-medium">{entry.name}</p>
                      <p className="text-muted-foreground mt-1 text-xs tabular-nums">
                        {formatTimestamp(entry.updated_at)}
                      </p>
                    </div>
                  ))}
                </div>
              </div>
            </ScrollArea>
          </SheetContent>
        </Sheet>
      </Card>

      {dialogOpen ? (
        <KnowledgeFormDialog
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
          kind="knowledge"
          target={deleting}
          onClose={() => { setDeleting(null); }}
        />
      ) : null}

      <Dialog
        open={evaluating !== null}
        onOpenChange={(next) => { if (!next) setEvaluating(null); }}
      >
        <DialogContent data-testid="knowledge-eval-dialog">
          <DialogHeader>
            <DialogTitle>
              {t("admin.knowledge.evalTitle", { name: evaluating?.name ?? "" })}
            </DialogTitle>
            <DialogDescription>{t("admin.knowledge.evalDescription")}</DialogDescription>
          </DialogHeader>
          {evalResult !== null ? (
            <div className="flex flex-col gap-2" data-testid="knowledge-eval-result">
              <div className="flex gap-1.5">
                <Badge variant={evalResult.pass ? "default" : "destructive"}>
                  {evalResult.pass ? t("admin.knowledge.evalPass") : t("admin.knowledge.evalFail")}
                </Badge>
                <Badge variant={evalResult.schema_subset_ok ? "secondary" : "destructive"}>
                  {t("admin.knowledge.evalSchema")}
                </Badge>
                <Badge variant={evalResult.privacy_ok ? "secondary" : "destructive"}>
                  {t("admin.knowledge.evalPrivacy")}
                </Badge>
                <Badge variant={evalResult.injection_ok ? "secondary" : "destructive"}>
                  {t("admin.knowledge.evalInjection")}
                </Badge>
                <Badge variant={evalResult.severity_ok ? "secondary" : "destructive"}>
                  {t("admin.knowledge.evalSeverity")}
                </Badge>
              </div>
              {evalResult.findings !== undefined && evalResult.findings.length > 0 ? (
                <ul className="text-destructive list-disc pl-4 text-xs">
                  {evalResult.findings.map((finding) => (
                    <li key={finding}>{finding}</li>
                  ))}
                </ul>
              ) : null}
            </div>
          ) : null}
          <DialogFooter>
            <Button onClick={() => { setEvaluating(null); }}>{t("common.close")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </PageLayout>
  );
}

function KnowledgeDetailPanel({
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

  const save = () => {
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
          definition: { ...selected.definition, knowledge_text: effectiveText },
          provenance: selected.provenance,
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
            ? t("admin.knowledge.scopeGlobal")
            : selected.scope_type === "datasource"
              ? t("admin.knowledge.scopeDatasource")
              : t("admin.knowledge.scopeTable")}
        </Badge>
        <Badge variant="secondary">
          {t(`admin.knowledge.provenance_${selected.provenance}`)}
        </Badge>
        <span className="text-muted-foreground text-xs tabular-nums">
          {formatTimestamp(selected.updated_at)}
        </span>
      </div>
      {selected.purpose ? <p className="text-sm">{selected.purpose}</p> : null}
      <div className="flex flex-col gap-2">
        <Textarea
          value={effectiveText}
          onChange={(event) => { onDraftChange(event.target.value); }}
          onBlur={() => { if (dirty) save(); }}
          rows={8}
          data-testid="knowledge-notes-editor"
          aria-label={t("admin.knowledge.editHeader")}
        />
        {dirty ? (
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

function KnowledgeFormDialog({
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

  const submit = () => {
    setErrorKey(null);
    setErrorRequestId(null);
    const write: KnowledgeEntryWrite = {
      name: form.name,
      purpose: form.purpose === "" ? undefined : form.purpose,
      state: form.state,
      scope_type: form.scopeType,
      datasource_id: form.scopeType === "global" ? undefined : form.datasourceId === "" ? undefined : form.datasourceId,
      database_name: form.scopeType === "global" ? undefined : form.databaseName === "" ? undefined : form.databaseName,
      table_name: form.scopeType === "table" ? (form.tableName === "" ? undefined : form.tableName) : undefined,
      definition: form.editor.definition,
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
              value={form.scopeType}
              onValueChange={(next) => {
                if (next === null) return;
                setForm({ ...form, scopeType: next });
              }}
              items={{
                global: t("admin.knowledge.scopeGlobal"),
                datasource: t("admin.knowledge.scopeDatasource"),
                table: t("admin.knowledge.scopeTable"),
              }}
            >
              <SelectTrigger data-testid="knowledge-scope">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="global">{t("admin.knowledge.scopeGlobal")}</SelectItem>
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
