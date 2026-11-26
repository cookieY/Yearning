import { useState } from "react";
import { AnimatePresence, motion } from "motion/react";
import { useTranslation } from "react-i18next";
import { BookOpenText, Menu, Pencil, Play, Plus, Search, Trash2 } from "lucide-react";
import type {
  KnowledgeEntry,
  KnowledgeEntryEvaluation,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  KnowledgeDetailPanel,
  KnowledgeFormDialog,
} from "@/features/admin/knowledge-notes-sections";
import {
  DeleteReviewInputDialog,
  StateBadge,
} from "@/features/admin/review-input-shared";
import {
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
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/shared/components/ui/input-group";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Sheet, SheetContent } from "@/shared/components/ui/sheet";
import { formatTimestamp } from "@/shared/lib/timezones";
import { cn } from "@/shared/lib/utils";

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
                            <Badge variant="secondary">
                              {t(`admin.reviewInput.severity_${entry.definition.max_severity}`)}
                            </Badge>
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
                              ? t("admin.knowledge.scopeGlobalRetired")
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
