import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useTranslation } from "react-i18next";
import { RotateCcw } from "lucide-react";
import type { Flow } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type { MainDialogAnchor } from "@/features/review/precheck-result-dialog";
import { StagePath } from "@/features/review/stage-path";
import { MarkdownEditor } from "@/shared/components/markdown-editor";
import { PendingButton } from "@/shared/components/pending-button";
import { EmptyState } from "@/shared/components/status/empty-state";
import { TableSearchInput } from "@/shared/components/table-search-input";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { Checkbox } from "@/shared/components/ui/checkbox";
import { DateTimePicker } from "@/shared/components/ui/date-time-picker";
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

/** §18.56 Option B: datetime-local (local timezone) -> RFC3339 UTC; empty/invalid returns null. */
export function requestedForToIso(value: string): string | null {
  if (value === "") return null;
  const due = new Date(value);
  if (Number.isNaN(due.getTime())) return null;
  return due.toISOString().replace(/\.\d{3}Z$/, "Z");
}

/** RFC3339 -> datetime-local (for display/prefill); null or past timestamp returns "". */
export function isoToRequestedFor(iso: string | null | undefined): string {
  if (iso == null || iso === "") return "";
  const due = new Date(iso);
  if (Number.isNaN(due.getTime()) || due.getTime() <= Date.now()) return "";
  const pad = (n: number): string => String(n).padStart(2, "0");
  return `${String(due.getFullYear())}-${pad(due.getMonth() + 1)}-${pad(due.getDate())}T${pad(due.getHours())}:${pad(due.getMinutes())}`;
}

export interface MetaFieldsProps {
  title: string;
  description: string;
  onTitleChange: (title: string) => void;
  onDescriptionChange: (description: string) => void;
  titleError?: string | null;
  /** §18.56 Option B: requested execution time (datetime-local string, empty = unset). */
  requestedFor: string;
  onRequestedForChange: (value: string) => void;
  requestedForError?: string | null;
  rollbackEnabled: boolean;
  onRollbackEnabledChange: (checked: boolean) => void;
  isRollbackDraft?: boolean;
  sourceOrderId?: string | null;
  sourceOrderDisplayNumber?: string | null;
}

/** Title / description / requested execution time / rollback-enabled fields of the Title & Description section in both modes. */
export function MetaFields({
  title,
  description,
  onTitleChange,
  onDescriptionChange,
  requestedFor,
  onRequestedForChange,
  titleError,
  requestedForError,
  rollbackEnabled,
  onRollbackEnabledChange,
  isRollbackDraft = false,
  sourceOrderId = null,
  sourceOrderDisplayNumber = null,
}: MetaFieldsProps) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-y-4">
      {isRollbackDraft && (
        <div
          className="bg-muted/40 flex flex-wrap items-center justify-between gap-2 rounded-lg border px-3.5 py-2.5 text-sm"
          data-testid="wizard-rollback-draft-banner"
        >
          <span className="flex items-center gap-2 font-medium">
            <RotateCcw className="size-4 shrink-0" aria-hidden />
            <Badge variant="outline">{t("orders.rollback.ticketBadge")}</Badge>
            <span>{t("precheck.wizard.rollbackDraftBanner")}</span>
          </span>
          {sourceOrderId != null && (
            <Link
              to={`/changes/orders/${sourceOrderId}`}
              className="text-foreground font-mono text-xs underline underline-offset-4 hover:opacity-80"
              data-testid="wizard-rollback-source-order-link"
            >
              {sourceOrderDisplayNumber ?? sourceOrderId}
            </Link>
          )}
        </div>
      )}
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="draft-title-input" className="mb-1">
          {t("precheck.new.dialog.titleLabel")}
          <span className="text-destructive" aria-hidden>*</span>
        </Label>
        <Input
          id="draft-title-input"
          value={title}
          onChange={(event) => { onTitleChange(event.target.value); }}
          maxLength={256}
          placeholder={t("precheck.wizard.titlePlaceholder")}
          aria-invalid={titleError != null}
          className="rounded-lg text-sm md:text-sm"
          data-testid="draft-title-input"
        />
        {titleError != null && (
          <p className="text-destructive text-xs" data-testid="wizard-title-error">{titleError}</p>
        )}
      </div>
      <div className="grid gap-4 md:grid-cols-2 md:items-start">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="draft-requested-for" className="text-sm font-semibold">
            {t("precheck.wizard.requestedScheduleLabel")}
          </Label>
          <DateTimePicker
            id="draft-requested-for"
            value={requestedFor}
            onChange={onRequestedForChange}
            enforceFuture
            ariaLabel={t("precheck.wizard.requestedScheduleLabel")}
            placeholder={t("precheck.wizard.requestedSchedulePlaceholder")}
            testId="draft-requested-for"
            className="w-full"
            inputClassName="h-10 w-full text-sm"
          />
          <p className="text-muted-foreground text-[13px]" data-testid="draft-requested-for-hint">
            {t("precheck.wizard.requestedScheduleHint")}
          </p>
          {requestedForError != null && (
            <p className="text-destructive text-xs" data-testid="wizard-requested-for-error">{requestedForError}</p>
          )}
        </div>
        <div className="flex flex-col gap-1.5">
          <div className="flex items-center gap-2.5">
            <Checkbox
              id="draft-rollback-enabled"
              checked={isRollbackDraft ? false : rollbackEnabled}
              disabled={isRollbackDraft}
              onCheckedChange={(checked) => {
                if (!isRollbackDraft) {
                  onRollbackEnabledChange(checked);
                }
              }}
              data-testid="wizard-rollback-enabled"
            />
            <Label htmlFor="draft-rollback-enabled" className="cursor-pointer text-sm font-semibold">
              {t("precheck.wizard.rollbackEnabledLabel")}
            </Label>
          </div>
          <p className="text-muted-foreground pl-6 text-[13px]" data-testid="draft-rollback-enabled-hint">
            {isRollbackDraft
              ? t("precheck.wizard.rollbackDisabledOnRollbackDraftHint")
              : t("precheck.wizard.rollbackEnabledHint")}
          </p>
        </div>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="draft-description-input" className="text-sm font-semibold">
          {t("precheck.new.dialog.descriptionLabel")}
        </Label>
        <MarkdownEditor
          id="draft-description-input"
          value={description}
          onChange={onDescriptionChange}
          maxLength={4096}
          placeholder={t("editor.placeholder")}
          ariaLabel={t("precheck.new.dialog.descriptionLabel")}
          data-testid="draft-description-input"
        />
      </div>
    </div>
  );
}

export interface FlowPickerProps {
  flows: Flow[];
  selectedFlowId: string | null;
  onSelect: (flowId: string) => void;
}

/** Selectable flow cards in create mode showing each flow's frozen stage chain and datasources. */
export function FlowPicker({ flows, selectedFlowId, onSelect }: FlowPickerProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const keyword = query.trim().toLowerCase();
  const visible = keyword === "" ? flows : flows.filter((flow) => flow.name.toLowerCase().includes(keyword));
  return (
    <div className="flex flex-col gap-3" data-testid="wizard-flow-picker">
      <div className="max-w-64">
        <TableSearchInput
          value={query}
          onChange={setQuery}
          placeholder={t("precheck.wizard.searchPlaceholder")}
          testId="wizard-flow-search"
        />
      </div>
      {/* Height cap sits on the viewport (placing max-h on the root fails inside an auto-height chain). */}
      {visible.length === 0 ? (
        <div className="rounded-lg border border-dashed">
          <EmptyState
            filtered
            title={t("precheck.wizard.searchEmpty")}
            testId="wizard-flow-search-empty"
          />
        </div>
      ) : (
        <ScrollArea className="max-h-[380px]" viewportClassName="max-h-[380px]">
          <div className="grid gap-3 pb-px sm:grid-cols-2 xl:grid-cols-3">
            {visible.map((flow) => {
              const selected = flow.id === selectedFlowId;
              return (
                <button
                  key={flow.id}
                  type="button"
                  onClick={() => { onSelect(flow.id); }}
                  aria-pressed={selected}
                  className={
                    selected
                      ? "flex flex-col gap-2 rounded-xl border border-foreground/70 bg-foreground/[0.04] p-4 text-left ring-1 ring-foreground/20"
                      : "hover:border-foreground/30 flex flex-col gap-2 rounded-xl border p-4 text-left transition-colors"
                  }
                  data-testid={`use-flow-${flow.id}`}
                >
                  <span className="flex items-center justify-between gap-2 text-sm font-semibold">
                    {flow.name}
                    <span
                      aria-hidden
                      className={
                        selected
                          ? "size-4 shrink-0 rounded-full border-[5px] border-foreground"
                          : "size-4 shrink-0 rounded-full border-2 border-foreground/25"
                      }
                    />
                  </span>
                  <StagePath flow={flow} />
                </button>
              );
            })}
          </div>
        </ScrollArea>
      )}
    </div>
  );
}

/** One continuous-form card section wrapping title, optional header action, body, and hint caption. */
export function FormSection({
  title,
  hint,
  action,
  children,
  last = false,
}: {
  title: string;
  hint: string;
  action?: ReactNode;
  children: ReactNode;
  last?: boolean;
}) {
  return (
    <Card className="[--card-spacing:--spacing(4)]" data-last={last ? "true" : undefined}>
      <CardHeader>
        <CardTitle className="flex items-center justify-between gap-3">
          <h5 className="text-base font-semibold">{title}</h5>
          {action}
        </CardTitle>
      </CardHeader>
      <CardContent>
        {children}
        <small className="text-muted-foreground mt-2 block text-xs">{hint}</small>
      </CardContent>
    </Card>
  );
}

/** Key-value row rendered inside the sticky summary sidebar card. */
export function SummaryRow({
  label,
  value,
  testId,
  muted = false,
}: {
  label: string;
  value: ReactNode;
  testId?: string;
  muted?: boolean;
}) {
  return (
    <div className="flex items-baseline justify-between gap-3 text-sm">
      <dt className="text-muted-foreground flex-none text-[13px]">{label}</dt>
      <dd
        className={
          muted
            ? "text-muted-foreground min-w-0 truncate text-right"
            : "min-w-0 truncate text-right font-medium"
        }
        data-testid={testId}
      >
        {value}
      </dd>
    </div>
  );
}

export interface SubmitConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mainDialogAnchor: MainDialogAnchor | null;
  flow: Flow | null;
  fallbackFlowId?: string;
  gatePassed: boolean;
  submitError: string | null;
  submitting: boolean;
  hasPresentedRun: boolean;
  onReopenPrecheck: () => void;
  onConfirmSubmit: () => void;
}

/** Confirmation modal summarizing the frozen flow and gate verdict before submitting an immutable change order. */
export function SubmitConfirmDialog({
  open,
  onOpenChange,
  mainDialogAnchor,
  flow,
  fallbackFlowId,
  gatePassed,
  submitError,
  submitting,
  hasPresentedRun,
  onReopenPrecheck,
  onConfirmSubmit,
}: SubmitConfirmDialogProps) {
  const { t } = useTranslation();

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        onOpenChange(nextOpen);
        if (!nextOpen && hasPresentedRun) {
          onReopenPrecheck();
        }
      }}
    >
      <DialogContent
        style={
          mainDialogAnchor !== null
            ? {
                left: `${String(mainDialogAnchor.left)}px`,
                maxWidth: `min(24rem, ${String(mainDialogAnchor.maxWidth)}px)`,
              }
            : undefined
        }
        data-testid="submit-confirm-dialog"
      >
        <DialogHeader>
          <DialogTitle>{t("precheck.submit.confirmTitle")}</DialogTitle>
          <DialogDescription>{t("precheck.submit.confirmDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3 text-sm">
          <div>
            <p className="text-muted-foreground text-xs">{t("precheck.wizard.summary.flow")}</p>
            <p className="mt-1">{flow?.name ?? fallbackFlowId}</p>
            <div className="mt-1">
              <StagePath flow={flow} />
            </div>
          </div>
          <div>
            <p className="text-muted-foreground text-xs">{t("precheck.submit.confirmGate")}</p>
            <p className="mt-1" data-testid="submit-confirm-gate">
              {gatePassed
                ? t("precheck.submit.confirmGatePassed")
                : t("precheck.submit.confirmGateFailed")}
            </p>
          </div>
          {submitError !== null && (
            <p role="alert" className="text-destructive text-sm" data-testid="submit-confirm-error">
              {submitError}
            </p>
          )}
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false);
              if (hasPresentedRun) {
                onReopenPrecheck();
              }
            }}
            data-testid="submit-confirm-cancel"
          >
            {t("common.cancel")}
          </Button>
          <PendingButton
            pending={submitting}
            onClick={onConfirmSubmit}
            data-testid="submit-confirm-accept"
          >
            {t("precheck.submit.confirmAction")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
