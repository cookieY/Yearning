import { useEffect, useState } from "react";
import { AnimatePresence, motion } from "motion/react";
import { useTranslation } from "react-i18next";
import { AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/shared/components/ui/alert-dialog";
import { Menu } from "lucide-react";
import { Sheet, SheetContent } from "@/shared/components/ui/sheet";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Textarea } from "@/shared/components/ui/textarea";
import { cn } from "@/shared/lib/utils";
import { BookOpenText, Pencil, Puzzle, Plus, Trash2, Play, Lock, Search, Sparkles, FlaskConical } from "lucide-react";
import { Checkbox } from "@/shared/components/ui/checkbox";
import type {
  KnowledgeEntry,
  KnowledgeEntryEvaluation,
  KnowledgeEntryWrite,
  PromptTool,
  PromptToolTestCase,
  PromptToolWrite,
  PromptToolWriteParameters,
  RunPromptToolTestResponse,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useCreateKnowledgeEntry,
  useCreatePromptTool,
  useDatasources,
  useDeleteKnowledgeEntry,
  useDeletePromptTool,
  useEvaluateKnowledgeEntry,
  useGeneratePromptToolTestCases,
  useKnowledgeEntries,
  useOptimizePromptToolPrompt,
  usePromptTools,
  useReplaceKnowledgeEntry,
  useReplacePromptTool,
  useRunPromptToolTest,
} from "@/features/admin/use-admin";
import {
  ReviewInputDefinitionEditor,
  type ReviewInputEditorValue,
} from "@/features/admin/review-input-editor";
import { renderSkillDescription } from "@/features/admin/skill-description";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { formatTimestamp } from "@/shared/lib/timezones";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { useSession } from "@/features/auth/session-provider";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { TableSearchInput } from "@/shared/components/table-search-input";
import { Card, CardContent } from "@/shared/components/ui/card";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/shared/components/ui/input-group";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";

/**
 * Shared lifecycle page for the two governed review inputs (migration
 * contract §9.2 review-engine entries): skills (/admin/review-engine/skills,
 * PromptTool) and internal experience (/admin/review-engine/knowledge,
 * KnowledgeEntry). Both follow the identical Draft/Enabled/Disabled
 * lifecycle with an immutable config_hash per save; every save re-hashes the
 * definition, and a changed hash makes every unsubmitted review that
 * references the old hash outdated while submitted orders keep their frozen
 * snapshot. Binding to flows/rule sets happens elsewhere (flow editor F10 /
 * rule-set page) — this surface renders reverse references only.
 *
 * B13 alignment: PromptTool views carry is_builtin — built-in rows render a
 * lock badge, their definition face (name/engine/definition/parameter keys)
 * is locked in the editor and only the state stays toggleable, and delete is
 * disabled with an inline explanation (the backend refuses both). The skills
 * Eval gate runs at save time (RCP-20260831 ruling 4): enabling a skill
 * evaluates inline inside create/replace, a failing gate blocks the save and
 * renders the business error in place — the page offers no separate Eval
 * entry or result column (knowledge entries keep their declared evaluations
 * endpoint).
 */

const STATE_ORDER = ["draft", "enabled", "disabled"] as const;
type InputState = (typeof STATE_ORDER)[number];

interface ReviewInputFormState {
  name: string;
  description: string;
  state: InputState;
  engine: "all" | "mysql" | "postgresql";
  purpose: string;
  scopeType: "global" | "datasource" | "table";
  datasourceId: string;
  databaseName: string;
  tableName: string;
  /** §18.81: string draft of tunable parameter values (key set and types follow the row; only values are editable). */
  paramDrafts: Record<string, string>;
  editor: ReviewInputEditorValue;
}

// §18.80: skill description max length (consistent with OpenAPI PromptToolWrite.description maxLength,
// counted in Unicode code points).
const MAX_SKILL_DESCRIPTION = 100;

// §18.81 (owner ruling): builtin skill prompts are locked, but concrete values of {{variable}}
// in prompts are open for tuning. Drafts are held as strings and restored to the row value types on submit —
// types and key sets are guaranteed not to drift by construction, with backend key-set + type checks as fallback (B8 governance triad).
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

function emptyForm(): ReviewInputFormState {
  return {
    name: "",
    description: "",
    state: "draft",
    engine: "all",
    purpose: "",
    scopeType: "global",
    datasourceId: "",
    databaseName: "",
    tableName: "",
    paramDrafts: {},
    editor: {
      definition: {
        knowledge_text: "",
        max_severity: "critical",
        version: 1,
      },
      parameters: {},
    },
  };
}

function formForTool(tool: PromptTool): ReviewInputFormState {
  return {
    name: tool.name,
    description: tool.description,
    state: tool.state,
    engine: tool.engine,
    purpose: "",
    scopeType: "global",
    datasourceId: "",
    databaseName: "",
    tableName: "",
    paramDrafts: stringifyParamDrafts(tool.parameters ?? {}),
    editor: { definition: tool.definition, parameters: tool.parameters ?? {} },
  };
}

function formForEntry(entry: KnowledgeEntry): ReviewInputFormState {
  return {
    name: entry.name,
    description: "",
    state: entry.state,
    engine: "all",
    purpose: entry.purpose ?? "",
    scopeType: entry.scope_type,
    datasourceId: entry.datasource_id ?? "",
    databaseName: "",
    tableName: "",
    paramDrafts: {},
    editor: { definition: entry.definition, parameters: {} },
  };
}

function StateBadge({ state }: { state: string }) {
  const { t } = useTranslation();
  const tone = state === "enabled" ? "emerald" : state === "draft" ? "amber" : "stone";
  return (
    <StatusBadge tone={tone} data-testid="review-input-state">
      {t(`admin.reviewInput.state_${state}`)}
    </StatusBadge>
  );
}

export function ReviewInputListPage({ kind }: { kind: "skills" | "knowledge" }) {
  const { t } = useTranslation();
  const session = useSession();
  const enabled = session.user?.can_access_admin === true;
  const [skillSearch, setSkillSearch] = useState("");
  const [knowledgeSearch, setKnowledgeSearch] = useState("");
  // §18.82: skill table uses backend cursor pagination (default 20 rows/page); search changes reset to page 1.
  const skillPager = useCursorPage(20);
  const skillKeyword = skillSearch.trim();
  useEffect(() => { skillPager.reset(); }, [skillKeyword]); // eslint-disable-line react-hooks/exhaustive-deps -- reset is intentional on keyword change
  // §18.73: skill/knowledge search uses server-side q (name substring).
  const toolsQuery = usePromptTools(enabled && kind === "skills", {
    q: skillKeyword || undefined,
    limit: skillPager.pageSize,
    after: skillPager.after,
  });
  // Capture before the exhaustive ternary below so discriminated-union narrowing does not leak into pagination guards (§18.82).
  const toolsLoaded = !toolsQuery.isPending && !toolsQuery.isError;
  const entriesQuery = useKnowledgeEntries(enabled && kind === "knowledge", { q: knowledgeSearch.trim() || undefined });
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<PromptTool | KnowledgeEntry | null>(null);
  const [deleting, setDeleting] = useState<PromptTool | KnowledgeEntry | null>(null);
  const [evaluating, setEvaluating] = useState<KnowledgeEntry | null>(null);
  const [evaluatingId, setEvaluatingId] = useState<string | null>(null);
  const [evalResult, setEvalResult] = useState<KnowledgeEntryEvaluation | null>(null);
  const [evalErrorKey, setEvalErrorKey] = useState<string | null>(null);
  const [selectedKnowledgeId, setSelectedKnowledgeId] = useState<string | null>(null);
  const [notesSheetOpen, setNotesSheetOpen] = useState(false);
  const [knowledgeTextDraft, setKnowledgeTextDraft] = useState<string | null>(null);
  const replaceEntry = useReplaceKnowledgeEntry();
  const evaluateMutation = useEvaluateKnowledgeEntry();

  const rows = kind === "skills" ? (toolsQuery.data?.items ?? []) : (entriesQuery.data?.items ?? []);
  const loading = kind === "skills" ? toolsQuery.isPending : entriesQuery.isPending;
  const errored = kind === "skills" ? toolsQuery.isError : entriesQuery.isError;
  const refetch = kind === "skills" ? toolsQuery.refetch : entriesQuery.refetch;

  const isSkills = kind === "skills";
  // §18.73: render directly after server-side q filtering; empty-state distinction reads skillSearch directly.
  const visibleSkillRows = rows;
  // Tween the position delta of surviving skill rows when server-side q narrows the result set; same on page turns.
  const skillBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(visibleSkillRows.map((row) => row.id)),
  );

  // Internal knowledge renders as the Notes-app master-detail (owner ruling 2026-09-02,
  // frozen template apps/notes): entry cards on the left, the experience
  // detail on the right. Skills keep the table view. All CRUD/evaluate
  // machinery is shared with the table branch below.
  if (!isSkills) {
    // §18.73: knowledgeSearch is consumed by server-side q; client no longer filters.
    const entries = entriesQuery.data?.items ?? [];
    const filtered = entries;
    const selected =
      filtered.find((entry) => entry.id === selectedKnowledgeId) ?? filtered[0] ?? null;
    const formatDate = (value: string): string => formatTimestamp(value);
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
              {/* Exit animation (animated-list-02, owner ruling 2026-10-24): rows
                  slide up in and fade up out as the filter/selection changes;
                  layout tweens the survivors' reflow. Durations sit inside the
                  motion discipline (<=500ms, named curves) and only
                  opacity/transform move. */}
              {/* Unified scrollbars (owner 2026-11-03): item list uses ScrollArea overlay
                thumb. Fixed height (owner 2026-11-05): h-[728px] = 4 full cards
                (4x173px, including purpose line) + 3x12px gaps, showing 4 knowledge items in full
                while the rest scroll inside the list. */}
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
                          {formatDate(entry.updated_at)}
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
                  <ErrorState error={entriesQuery.error} operationId="listKnowledgeEntries" onRetry={() => void refetch()} />
                ) : selected === null ? (
                  <EmptyState
                    title={
                      knowledgeSearch.trim() === ""
                        ? t("admin.knowledge.emptyTitle")
                        : t("table.emptySearch")
                    }
                    description={
                      knowledgeSearch.trim() === ""
                        ? t("admin.knowledge.emptyDescription")
                        : undefined
                    }
                    filtered={knowledgeSearch.trim() !== ""}
                  />
                ) : (
                  <div className="flex flex-col gap-4" data-testid="knowledge-detail">
                    <div className="flex items-start justify-between gap-3">
                      <h3 className="text-xl font-semibold">{selected.name}</h3>
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => {
                          setEditing(selected);
                          setDialogOpen(true);
                        }}
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
                        {formatDate(selected.updated_at)}
                      </span>
                    </div>
                    {selected.purpose ? (
                      <p className="text-sm">{selected.purpose}</p>
                    ) : null}
                    {(() => {
                      const serverText = selected.definition.knowledge_text;
                      const draftText = knowledgeTextDraft ?? serverText;
                      const dirty = knowledgeTextDraft !== null && knowledgeTextDraft !== serverText;
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
                              definition: { ...selected.definition, knowledge_text: draftText },
                              provenance: selected.provenance,
                            },
                          },
                          {
                            onSuccess: () => {
                              setKnowledgeTextDraft(null);
                              appToast.success(t("common.savedToast"), {
                                description: selected.name,
                              });
                            },
                            onError: (error) => {
                              const display = describeError(error, "replaceKnowledgeEntry");
                              appToast.error(t(display.messageKey));
                            },
                          },
                        );
                      };
                      return (
                        <div className="flex flex-col gap-2">
                          <Textarea
                            value={draftText}
                            onChange={(event) => { setKnowledgeTextDraft(event.target.value); }}
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
                      );
                    })()}
                  </div>
                )}
              </div>
            </div>
          </div>
          {/* Unified scrollbars (owner 2026-11-03): notes Sheet scrolls with ScrollArea overlay thumb;
              gap-4/p-6 moved into the content layer so the panel itself no longer scrolls (was native overflow-y-auto). */}
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
                      {formatDate(entry.updated_at)}
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
          <ReviewInputDialog
            key={editing?.id ?? "new"}
            kind="knowledge"
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
        <ListToolbar
          busy={toolsQuery.isFetching && !toolsQuery.isPending}
          fill
        >
          <TableSearchInput value={skillSearch} onChange={setSkillSearch} testId="admin-skills-search" />
        </ListToolbar>
        <CardContent className="px-0! flex flex-col gap-3">
          {evalErrorKey !== null ? (
            <Alert variant="destructive" data-testid="knowledge-eval-error">
              <AlertTitle>{t(evalErrorKey)}</AlertTitle>
            </Alert>
          ) : null}
          {loading ? (
            <LoadingState variant="table" columns={3} />
          ) : errored ? (
            <ErrorState error={toolsQuery.error} operationId="listPromptTools" onRetry={() => void refetch()} />
          ) : visibleSkillRows.length === 0 && skillSearch.trim() !== "" ? (
            <EmptyState
              title={t("table.emptySearch")}
              filtered
              testId="admin-skills-search-empty"
            />
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
                {visibleSkillRows.map((row) => {
                  const id = row.id;
                  const name = row.name;
                  const state = row.state;
                  
                  const builtin = (row as PromptTool).is_builtin;
                  const description = (row as PromptTool).description;
                  return (
                    <TableRow key={id} data-row-key={id} data-testid={`review-input-row-${id}`}>
                      <TableCell className="font-semibold text-foreground">
                        {/* §18.80: show skill description in muted text below the name so users understand
                            each skill without opening the edit dialog; {{param}} placeholders
                            in descriptions render dynamically from current row values (addendum 1). */}
                        <span className="flex flex-col gap-0.5">
                          <span className="flex items-center gap-1.5">
                            {name}
                            {builtin ? (
                              <Badge variant="outline" data-testid={`review-input-builtin-${id}`}>
                                <Lock /> {t("admin.skills.builtinBadge")}
                              </Badge>
                            ) : null}
                          </span>
                          {description ? (
                            <span
                              className="text-xs font-normal text-muted-foreground"
                              data-testid={`review-input-description-${id}`}
                            >
                              {renderSkillDescription(description, (row as PromptTool).parameters ?? {})}
                            </span>
                          ) : null}
                        </span>
                      </TableCell>
                      <TableCell>
                        <StateBadge state={state} />
                      </TableCell>
                      <TableCell className="text-right">
                        <TableRowActions testId={`review-input-actions-${id}`}>
                          <DropdownMenuItem
                            onClick={() => {
                              setEditing(row);
                              setDialogOpen(true);
                            }}
                            data-testid={`review-input-edit-${id}`}
                          >
                            <Pencil /> {t("common.edit")}
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            variant="destructive"
                            onClick={() => { setDeleting(row); }}
                            disabled={builtin}
                            data-testid={`review-input-delete-${id}`}
                          >
                            <Trash2 /> {t("common.delete")}
                          </DropdownMenuItem>
                        </TableRowActions>
                      </TableCell>
                    </TableRow>
                  );
                })}
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
        <ReviewInputDialog
          key={editing?.id ?? "new"}
          kind={kind}
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
          kind={kind}
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

function ReviewInputDialog({
  kind,
  editing,
  onClose,
}: {
  kind: "skills" | "knowledge";
  editing: PromptTool | KnowledgeEntry | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const isSkills = kind === "skills";
  // Built-in skills are system-owned (B13 is_builtin): the definition face
  // arrives locked and the submit can only carry a state change.
  const editingBuiltin = isSkills && editing !== null && (editing as PromptTool).is_builtin;
  const createTool = useCreatePromptTool();
  const replaceTool = useReplacePromptTool();
  const optimizePrompt = useOptimizePromptToolPrompt();
  const createEntry = useCreateKnowledgeEntry();
  const replaceEntry = useReplaceKnowledgeEntry();
  const session = useSession();
  const datasourcesQuery = useDatasources(!isSkills && session.user?.can_access_admin === true);
  // Keyed per open by the parent: state initializes from the edited row.
  const [form, setForm] = useState<ReviewInputFormState>(() =>
    editing === null
      ? emptyForm()
      : isSkills
        ? formForTool(editing as PromptTool)
        : formForEntry(editing as KnowledgeEntry),
  );
  // §18.81: inline parameters (keys displayed in stable order; builtin and user rows share the same rule — values are tunable).
  const rowParams: PromptToolWriteParameters =
    isSkills && editing !== null ? ((editing as PromptTool).parameters ?? {}) : {};
  const [paramInvalid, setParamInvalid] = useState(false);
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const [errorRequestId, setErrorRequestId] = useState<string | null>(null);
  const [skillTestOpen, setSkillTestOpen] = useState(false);

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

  const mutation = isSkills
    ? editing === null
      ? createTool
      : replaceTool
    : editing === null
      ? createEntry
      : replaceEntry;

  const submit = () => {
    setErrorKey(null);
    setErrorRequestId(null);
    // max_severity is a retained v1 wire field. Preserve historical stored
    // values on unrelated edits; effective mandatory-rule severity remains
    // Critical in the backend regardless of this compatibility metadata.
    const definition = form.editor.definition;
    if (isSkills) {
      // §18.81: restore parameter values to their inline types before submitting; intercept invalid values inline without sending a request.
      const params = convertParamDrafts(form.paramDrafts, rowParams);
      if (params === null) {
        setParamInvalid(true);
        return;
      }
      setParamInvalid(false);
      const write: PromptToolWrite = {
        name: form.name,
        // §18.80: full replacement semantics — empty string clears description; backend ignores payload description on builtin rows.
        description: form.description,
        state: form.state,
        engine: form.engine,
        parameters: params,
        definition,
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
    } else {
      const write: KnowledgeEntryWrite = {
        name: form.name,
        purpose: form.purpose === "" ? undefined : form.purpose,
        state: form.state,
        scope_type: form.scopeType,
        datasource_id: form.scopeType === "global" ? undefined : form.datasourceId === "" ? undefined : form.datasourceId,
        database_name: form.databaseName === "" ? undefined : form.databaseName,
        table_name: form.tableName === "" ? undefined : form.tableName,
        definition,
        provenance: editing !== null && "provenance" in editing ? editing.provenance : "manual",
        source_finding_id:
          editing !== null && "source_finding_id" in editing
            ? (editing).source_finding_id ?? undefined
            : undefined,
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
    }
  };

  const onWriteError = (operationId: string) => (error: unknown) => {
    const display = describeError(error, operationId);
    setErrorKey(display.messageKey);
    setErrorRequestId(display.requestId);
    appToast.error(t(display.messageKey));
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {editing === null
              ? t(isSkills ? "admin.skills.createTitle" : "admin.knowledge.createTitle")
              : t(isSkills ? "admin.skills.editTitle" : "admin.knowledge.editTitle")}
          </DialogTitle>
          <DialogDescription>{t("admin.reviewInput.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          {/* Owner ruling: hash and invalidation impact notice belong to the editing context — moved from the list page
              into the dialog; hash is visible only in edit mode (not yet generated during creation). */}
          <Alert className="border-[var(--risk-warning)]/40" data-testid="review-input-outdated-note">
            <AlertTitle>{t("admin.reviewInput.outdatedNoteTitle")}</AlertTitle>
            <AlertDescription>{t("admin.reviewInput.outdatedNoteBody")}</AlertDescription>
          </Alert>
          {editing !== null && editing.config_hash !== "" ? (
            <div className="flex flex-col gap-1">
              <Label>{t("admin.reviewInput.configHash")}</Label>
              <p className="break-all font-mono text-xs" data-testid="review-input-hash">
                {editing.config_hash}
              </p>
            </div>
          ) : null}
          {editingBuiltin ? (
            <Alert data-testid="review-input-builtin-notice">
              <AlertTitle>{t("admin.skills.builtinDialogTitle")}</AlertTitle>
              <AlertDescription>{t("admin.skills.builtinDialogBody")}</AlertDescription>
            </Alert>
          ) : null}
          {isSkills ? (
            <p className="text-muted-foreground text-xs" data-testid="skills-eval-gate-hint">
              {t("admin.skills.evalGateHint")}
            </p>
          ) : null}
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
            {isSkills ? (
              <>
                {/* §18.80: full-width name row -> full-width skill description row -> status/engine distributed
                    horizontally as a single section. */}
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
              </>
            ) : (
              <div className="flex flex-col gap-1">
                <Label htmlFor="review-input-name">{t("admin.reviewInput.name")}</Label>
                <Input
                  id="review-input-name"
                  value={form.name}
                  onChange={(event) => { setForm({ ...form, name: event.target.value }); }}
                  data-testid="review-input-name"
                />
              </div>
            )}
            <div className="flex flex-col gap-1">
              <Label>{t("admin.reviewInput.state")}</Label>
              <Select
                value={form.state}
                onValueChange={(next) => {
                  if (next === null) return;
                  setForm({ ...form, state: next });
                }}
                items={Object.fromEntries(
                  STATE_ORDER.map((state) => [state, t(`admin.reviewInput.state_${state}`)]),
                )}
              >
                <SelectTrigger data-testid="review-input-state-select">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {STATE_ORDER.map((state) => (
                    <SelectItem key={state} value={state}>
                      {t(`admin.reviewInput.state_${state}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {isSkills ? (
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
            ) : (
              <>
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
            )}
          </div>
          {isSkills && Object.keys(rowParams).length > 0 ? (
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
          ) : null}
          <ReviewInputDefinitionEditor
            value={form.editor}
            onChange={(next) => { setForm({ ...form, editor: next }); }}
            disabled={editingBuiltin}
            headerAction={
              isSkills ? (
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
              ) : undefined
            }
          />
          {errorKey !== null ? (
            <Alert variant="destructive" data-testid="review-input-error">
              <AlertTitle>{t(errorKey)}</AlertTitle>
              <AlertDescription>
                {errorRequestId !== null ? `request_id: ${errorRequestId}` : null}
              </AlertDescription>
            </Alert>
          ) : null}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton pending={mutation.isPending} onClick={submit} data-testid="review-input-submit">
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
        {isSkills && skillTestOpen ? (
          <SkillTestDialog
            name={form.name}
            description={form.description}
            engine={form.engine}
            parameters={convertParamDrafts(form.paramDrafts, rowParams) ?? {}}
            knowledgeText={form.editor.definition.knowledge_text}
            onClose={() => { setSkillTestOpen(false); }}
          />
        ) : null}
      </DialogContent>
    </Dialog>
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

  const resultById = new Map(
    (testResult?.results ?? []).map((item) => [item.id, item]),
  );

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

function DeleteReviewInputDialog({
  kind,
  target,
  onClose,
}: {
  kind: "skills" | "knowledge";
  target: PromptTool | KnowledgeEntry;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const isSkills = kind === "skills";
  const deleteTool = useDeletePromptTool();
  const deleteEntry = useDeleteKnowledgeEntry();
  const [errorKey, setErrorKey] = useState<string | null>(null);
  const mutation = isSkills ? deleteTool : deleteEntry;
  return (
    <AlertDialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("admin.reviewInput.deleteTitle")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("admin.reviewInput.deleteDescription", { name: target.name })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {errorKey !== null ? (
          <Alert variant="destructive" data-testid="review-input-delete-error">
            <AlertTitle>{t(errorKey)}</AlertTitle>
            <AlertDescription>{t("admin.reviewInput.deleteErrorHint")}</AlertDescription>
          </Alert>
        ) : null}
        <AlertDialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            variant="destructive"
            pending={mutation.isPending}
            onClick={() => { mutation.mutate(
                { id: target.id, version: target.version },
                {
                  onSuccess: () => {
                    appToast.success(t("common.delete"), { description: target.name });
                    onClose();
                  },
                  onError: (error) => {
                    const display = describeError(error, isSkills ? "deletePromptTool" : "deleteKnowledgeEntry");
                    setErrorKey(display.messageKey);
                    appToast.error(t(display.messageKey));
                  },
                },
              ); }
            }
            data-testid="review-input-delete-confirm"
          >
            {t("common.delete")}
          </PendingButton>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
