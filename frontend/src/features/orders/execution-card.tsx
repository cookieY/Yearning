import { useEffect, useRef, useState } from "react";
import { DateTimePicker } from "@/shared/components/ui/date-time-picker";
import { useTranslation } from "react-i18next";
import type {
  ChangeOrder,
  OscOverrides,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  frozenExecutorStageFor,
  scheduledStageFor,
} from "@/features/orders/order-state";
import {
  useCancelExecutionSchedule,
  useCreateExecutionAttempt,
  useCreateExecutionSchedule,
} from "@/features/orders/use-execution";
import { useSession } from "@/features/auth/session-provider";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { PendingButton } from "@/shared/components/pending-button";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { NumberInput } from "@/shared/components/ui/number-input";
import { Label } from "@/shared/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/shared/components/ui/radio-group";
import { formatTimestamp } from "@/shared/lib/timezones";
import {
  CalendarClock,
  CircleAlert,
  Fingerprint,
  Play,
  XCircle,
} from "lucide-react";
import { ExecutionAttemptCard } from "./execution-attempt-card";
import { CopyDraftCard } from "./execution-copy-draft-card";

/**
 * Execution workspace (frontend PRD F8 items 1–3, W006, E001–E007). The
 * executor area renders only for the frozen executors of the stage currently
 * waiting for execution — approval never auto-executes, and admin confers no
 * execution right. Once an attempt exists (its id comes from the creation
 * response; the contract has no list-attempts read), the workspace shows
 * preflight/progress, the per-statement ledger, OSC progress and the
 * unknown-result verification form. There is deliberately no retry button:
 * a stage whose prior attempt crossed the send boundary can only restart as
 * a copied draft (E004), and there is no rollback entry anywhere (E003).
 */

/** Executor affordances for the stage waiting in execution_pending (W006).
 * Shows the frozen SQL hash both the approval and this execution are bound
 * to (gate: approved and executed SQL Hashes are displayed consistently), the DML/DDL semantics note, the
 * execute confirm and the deferred-schedule form. */
function ExecutionActionCard({ order, onAttemptCreated }: {
  order: ChangeOrder;
  onAttemptCreated: (attemptId: string) => void;
}) {
  const { t } = useTranslation();
  const session = useSession();
  const stage = frozenExecutorStageFor(order, session.user?.id);
  const create = useCreateExecutionAttempt(order, onAttemptCreated);
  const schedule = useCreateExecutionSchedule(order);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [ddlMode, setDdlMode] = useState<"gh_ost" | "online_ddl">("gh_ost");
  const [oscExpanded, setOscExpanded] = useState(false);
  const [oscOverrides, setOscOverrides] = useState<OscOverrides>({});
  const [scheduledFor, setScheduledFor] = useState("");
  const [scheduleNote, setScheduleNote] = useState<string | null>(null);
  // The 5-minute / 30-day schedule window depends on wall-clock time, so it
  // is derived inside the change handler (impure work stays out of render).
  const [dueInRange, setDueInRange] = useState(false);
  // §18.56 Option B: prefill submitter requested time (once, guarded by ref; once the user edits,
  // touched is set, the prefill hint disappears, and input is no longer overwritten).
  const [prefilled, setPrefilled] = useState(false);
  const [touched, setTouched] = useState(false);
  const prefillAppliedRef = useRef(false);
  /* eslint-disable react-hooks/set-state-in-effect -- §18.56 one-time prefill: ref guard
     ensures it runs at most once per order with no feedback loop (react-compiler cascade false positive). */
  useEffect(() => {
    if (prefillAppliedRef.current || order.requested_scheduled_for == null) return;
    prefillAppliedRef.current = true;
    const requested = new Date(order.requested_scheduled_for);
    const lead = requested.getTime() - Date.now();
    if (Number.isNaN(requested.getTime()) || lead <= 5 * 60 * 1000 || lead >= 30 * 24 * 3600 * 1000) {
      return;
    }
    const pad = (n: number): string => String(n).padStart(2, "0");
    setScheduledFor(
      `${String(requested.getFullYear())}-${pad(requested.getMonth() + 1)}-${pad(requested.getDate())}T${pad(requested.getHours())}:${pad(requested.getMinutes())}`,
    );
    setDueInRange(true);
    setPrefilled(true);
  }, [order.requested_scheduled_for]);
  /* eslint-enable react-hooks/set-state-in-effect */
  const applyScheduledFor = (value: string): void => {
    setTouched(true);
    setScheduledFor(value);
    setScheduleNote(null);
    if (value === "") {
      setDueInRange(false);
      return;
    }
    const lead = new Date(value).getTime() - Date.now();
    // UX-004: show an explicit hint when less than 5 minutes ahead instead of silently disabling the button.
    setScheduleNote(
      !Number.isNaN(lead) && lead >= 0 && lead <= 5 * 60 * 1000
        ? t("orders.execution.scheduleLeadHint")
        : null,
    );
    setDueInRange(!Number.isNaN(lead) && lead > 5 * 60 * 1000 && lead < 30 * 24 * 3600 * 1000);
  };

  if (stage === null) return null;

  // The datetime-local value is interpreted as local time; the wire format is
  // RFC3339 UTC (ScheduleRequest Timestamp).
  const scheduleValue = (): string | null => {
    if (scheduledFor === "") return null;
    const due = new Date(scheduledFor);
    if (Number.isNaN(due.getTime())) return null;
    return due.toISOString().replace(/\.\d{3}Z$/, "Z");
  };
  const dueIso = scheduleValue();

  const setOscStringOverride = (key: "max_load" | "critical_load", raw: string): void => {
    const trimmed = raw.trim();
    const { [key]: _removed, ...rest } = oscOverrides;
    const next: OscOverrides = rest;
    if (trimmed !== "") next[key] = trimmed;
    setOscOverrides(next);
  };

  const setOscNumberOverride = (
    key:
      | "chunk_size"
      | "dml_batch_size"
      | "max_lag"
      | "nice_ratio"
      | "cut_over_lock_timeout_seconds"
      | "retries",
    raw: string,
  ): void => {
    const trimmed = raw.trim();
    const { [key]: _removed, ...rest } = oscOverrides;
    const next: OscOverrides = rest;
    const parsed = Number(trimmed);
    if (trimmed !== "" && !Number.isNaN(parsed)) next[key] = parsed;
    setOscOverrides(next);
  };

  const hasDdl = order.has_ddl === true || order.osc_choice_available === true;

  const runExecute = (): void => {
    const body: { ddl_execution_mode?: "gh_ost" | "online_ddl"; osc_overrides?: OscOverrides } = {};
    if (order.osc_choice_available === true && ddlMode === "online_ddl") {
      body.ddl_execution_mode = "online_ddl";
    } else if (order.osc_choice_available === true && Object.keys(oscOverrides).length > 0) {
      body.osc_overrides = oscOverrides;
    }
    create.mutate(
      body,
      {
        onSuccess: () => {
          setConfirmOpen(false);
          setActionError(null);
          appToast.info(t("orders.execution.start"));
        },
        onError: (error) => {
          const display = describeError(error, "createExecutionAttempt");
          // BUG-006: meta.detail carries the preflight refusal reason (e.g. sql_changed_after_review),
          // appended verbatim so the executor no longer sees only the generic "preflight check failed" copy.
          const detail = display.detail !== null ? ` ${display.detail}` : "";
          const msg = `${t(display.messageKey)}${detail}${display.requestId !== null ? ` (${display.requestId})` : ""}`;
          setActionError(msg);
          appToast.error(msg);
        },
      },
    );
  };

  const runSchedule = (): void => {
    if (dueIso === null) return;
    schedule.mutate(dueIso, {
      onSuccess: (created) => {
        const note = t("orders.execution.scheduleCreated", { time: formatTimestamp(created.scheduled_for) });
        setScheduleNote(note);
        setScheduledFor("");
        setActionError(null);
        appToast.success(note);
      },
      onError: (error) => {
        const display = describeError(error, "createExecutionSchedule");
        const detail = display.detail !== null ? ` ${display.detail}` : "";
        const msg = `${t(display.messageKey)}${detail}${display.requestId !== null ? ` (${display.requestId})` : ""}`;
        setActionError(msg);
        appToast.error(msg);
      },
    });
  };

  const pending = create.isPending || schedule.isPending;

  return (
    <Card data-testid="execution-action-card">
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2">
          <Play className="size-4" aria-hidden />
          {t("orders.execution.actionTitle")}
        </CardTitle>
        <CardDescription>{t("orders.execution.actionNote")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <p className="flex flex-wrap items-center gap-2 text-sm" data-testid="execution-hash-line">
          <Fingerprint className="size-4 text-muted-foreground" aria-hidden />
          <span className="text-muted-foreground">{t("orders.execution.sqlHash")}</span>
          <span className="font-mono text-xs">{order.sql_hash}</span>
          <Badge variant="outline" className="text-xs" data-testid="execution-hash-consistent">
            {t("orders.execution.hashConsistent")}
          </Badge>
        </p>
        <p className="text-muted-foreground text-xs">
          {hasDdl ? t("orders.execution.semanticsNote") : t("orders.execution.dmlSemanticsNote")}
        </p>
        {order.osc_choice_available === true && (
          <div className="flex flex-col gap-2 rounded-md border p-3" data-testid="execution-ddl-mode-section">
            <Label className="text-xs font-medium">{t("orders.execution.ddlModeLabel")}</Label>
            <RadioGroup
              value={ddlMode}
              onValueChange={(value: unknown) => {
                if (value === "gh_ost" || value === "online_ddl") {
                  setDdlMode(value);
                }
              }}
              className="grid gap-2 sm:grid-cols-2"
              data-testid="execution-ddl-mode-radiogroup"
            >
              <label
                htmlFor="ddl-mode-gh-ost"
                className="flex cursor-pointer items-start gap-2.5 rounded-md border p-2.5 text-xs transition-colors hover:bg-muted/40 has-[[data-checked]]:border-primary has-[[data-checked]]:bg-primary/5"
              >
                <RadioGroupItem id="ddl-mode-gh-ost" value="gh_ost" className="mt-0.5" data-testid="execution-ddl-mode-gh-ost" />
                <div className="flex flex-col gap-0.5">
                  <span className="font-medium text-foreground">{t("orders.execution.ddlModeGhOst")}</span>
                  <span className="text-muted-foreground">{t("orders.execution.ddlModeGhOstHint")}</span>
                </div>
              </label>
              <label
                htmlFor="ddl-mode-online-ddl"
                className="flex cursor-pointer items-start gap-2.5 rounded-md border p-2.5 text-xs transition-colors hover:bg-muted/40 has-[[data-checked]]:border-primary has-[[data-checked]]:bg-primary/5"
              >
                <RadioGroupItem id="ddl-mode-online-ddl" value="online_ddl" className="mt-0.5" data-testid="execution-ddl-mode-online-ddl" />
                <div className="flex flex-col gap-0.5">
                  <span className="font-medium text-foreground">{t("orders.execution.ddlModeOnlineDdl")}</span>
                  <span className="text-muted-foreground">{t("orders.execution.ddlModeOnlineDdlHint")}</span>
                </div>
              </label>
            </RadioGroup>
          </div>
        )}
        <div className="flex flex-wrap items-end gap-3">
          <Button onClick={() => { setConfirmOpen(true); setActionError(null); }} disabled={pending} data-testid="execution-start">
            <Play className="size-4" aria-hidden />
            {t("orders.execution.start")}
          </Button>
          <div className="flex flex-col gap-1">
            <Label htmlFor="execution-schedule-at" className="text-xs">
              {t("orders.execution.scheduleLabel")}
            </Label>
            <div className="flex items-center gap-2">
              <DateTimePicker
                id="execution-schedule-at"
                value={scheduledFor}
                onChange={applyScheduledFor}
                enforceFuture
                ariaLabel={t("orders.execution.scheduleLabel")}
                placeholder={t("orders.execution.schedulePlaceholder")}
                testId="execution-schedule-input"
              />
              <PendingButton
                variant="outline"
                onClick={runSchedule}
                pending={schedule.isPending}
                disabled={pending || !dueInRange}
                data-testid="execution-schedule-submit"
              >
                <CalendarClock className="size-4" aria-hidden />
                {t("orders.execution.scheduleSubmit")}
              </PendingButton>
            </div>
            {scheduleNote !== null && (
              <p className="text-xs text-muted-foreground" data-testid="execution-schedule-note">{scheduleNote}</p>
            )}
            {prefilled && !touched && scheduleNote === null && (
              <p className="text-xs text-muted-foreground" data-testid="execution-schedule-prefilled">
                {t("orders.execution.schedulePrefilled")}
              </p>
            )}
          </div>
        </div>
        {actionError !== null && (
          <p role="alert" className="text-destructive text-sm" data-testid="execution-action-error">
            {actionError}
          </p>
        )}
      </CardContent>

      <Dialog open={confirmOpen} onOpenChange={(open) => { if (!open) { setConfirmOpen(false); setActionError(null); } }}>
        <DialogContent className="sm:max-w-xl" data-testid="execution-confirm-dialog">
          <DialogHeader>
            <DialogTitle>{t("orders.execution.confirmTitle")}</DialogTitle>
            <DialogDescription>{t("orders.execution.confirmDescription")}</DialogDescription>
          </DialogHeader>
          {hasDdl && (
            <Alert className="border-amber-600/40" data-testid="execution-ddl-warning">
              <CircleAlert className="size-4" aria-hidden />
              <AlertTitle>{t("orders.execution.ddlWarningTitle")}</AlertTitle>
              <AlertDescription>{t("orders.execution.ddlWarningDescription")}</AlertDescription>
            </Alert>
          )}
          {order.osc_choice_available === true && ddlMode === "gh_ost" && (
          <div className="flex flex-col gap-2 rounded-md border p-3" data-testid="execution-osc-overrides-box">
            <button
              type="button"
              className="flex w-full cursor-pointer items-center justify-between text-left text-xs font-medium text-foreground/90 no-underline outline-none hover:text-foreground focus:no-underline focus-visible:no-underline"
              onClick={() => { setOscExpanded((prev) => !prev); }}
              data-testid="execution-osc-overrides-toggle"
            >
              <span>{t("orders.execution.oscOverridesToggle")}</span>
              <span className="text-muted-foreground">{oscExpanded ? "▾" : "▸"}</span>
            </button>
            {oscExpanded && (
              <div className="mt-1 flex flex-col gap-2" data-testid="execution-osc-overrides-fields">
                <p className="text-muted-foreground text-xs">{t("orders.execution.oscOverridesHint")}</p>
                <div className="grid gap-2 sm:grid-cols-2">
                  {([
                    { key: "max_load", labelKey: "adminSettings.execution.oscMaxLoad", placeholder: "Threads_running=50", testId: "exec-osc-max-load" },
                    { key: "critical_load", labelKey: "adminSettings.execution.oscCriticalLoad", placeholder: "Threads_running=100", testId: "exec-osc-critical-load" },
                  ] as const).map((field) => (
                    <div key={field.key} className="flex flex-col gap-1">
                      <Label htmlFor={field.testId} className="text-xs">{t(field.labelKey)}</Label>
                      <Input
                        id={field.testId}
                        value={oscOverrides[field.key] ?? ""}
                        placeholder={field.placeholder}
                        onChange={(e) => { setOscStringOverride(field.key, e.target.value); }}
                        className="h-8 font-mono text-xs"
                        data-testid={field.testId}
                      />
                    </div>
                  ))}
                  {([
                    { key: "chunk_size", labelKey: "adminSettings.execution.oscChunkSize", testId: "exec-osc-chunk-size" },
                    { key: "dml_batch_size", labelKey: "adminSettings.execution.oscDmlBatchSize", testId: "exec-osc-dml-batch-size" },
                    { key: "max_lag", labelKey: "adminSettings.execution.oscMaxLag", testId: "exec-osc-max-lag" },
                    { key: "nice_ratio", labelKey: "adminSettings.execution.oscNiceRatio", testId: "exec-osc-nice-ratio" },
                    { key: "cut_over_lock_timeout_seconds", labelKey: "adminSettings.execution.oscCutOverTimeout", testId: "exec-osc-cut-over-timeout" },
                    { key: "retries", labelKey: "adminSettings.execution.oscRetries", testId: "exec-osc-retries" },
                  ] as const).map((field) => {
                    const numVal = oscOverrides[field.key];
                    return (
                      <div key={field.key} className="flex flex-col gap-1">
                        <Label htmlFor={field.testId} className="text-xs">{t(field.labelKey)}</Label>
                        <NumberInput
                          id={field.testId}
                          value={numVal === undefined ? "" : String(numVal)}
                          onChange={(e) => { setOscNumberOverride(field.key, e.target.value); }}
                          className="h-8 text-xs"
                          data-testid={field.testId}
                        />
                      </div>
                    );
                  })}
                </div>
              </div>
            )}
          </div>
          )}
          {actionError !== null && (
            <p role="alert" className="text-destructive text-sm">{actionError}</p>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => { setConfirmOpen(false); setActionError(null); }} data-testid="execution-confirm-cancel">
              {t("common.cancel")}
            </Button>
            <PendingButton pending={create.isPending} onClick={runExecute} data-testid="execution-confirm-run">
              {t("orders.execution.start")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

/** Scheduled-state card (owner ruling 2026-10-30): while the order sits in
 * `scheduled`, its frozen executors see the planned time and the cancel
 * affordance. Any frozen executor may cancel (not just the booker); the
 * order returns to stage_execution_pending for a fresh schedule or an
 * immediate run. Both the schedule and its cancellation live in the
 * database, so a page refresh — or a server crash plus restart — keeps the
 * booking intact until due time. */
function ScheduledCard({ order }: { order: ChangeOrder }) {
  const { t } = useTranslation();
  const session = useSession();
  const stage = scheduledStageFor(order, session.user?.id);
  const cancel = useCancelExecutionSchedule(order);
  const [cancelOpen, setCancelOpen] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

  if (stage === null) return null;
  // Live-row gate (Review P3): the backend guarantees active_schedule present
  // iff a state='scheduled' row exists. In the preflight-blocked corner
  // (blockDueScheduleOnly) the order stays `scheduled` with the row `blocked`
  // and no active_schedule — the cancel command would deterministically fail
  // 1004 there, so the affordance must not render on order.state alone.
  // Captured into a local so the narrowing reads as one guard; the planned
  // time below is then non-null by construction (ExecutionSchedule contract).
  const activeSchedule = order.active_schedule;
  if (activeSchedule == null) return null;

  const runCancel = (): void => {
    cancel.mutate(undefined, {
      onSuccess: () => {
        setCancelOpen(false);
        setActionError(null);
        appToast.info(t("orders.execution.cancelSchedule"));
      },
      onError: (error) => {
        const display = describeError(error, "cancelExecutionSchedule");
        const msg = `${t(display.messageKey)}${display.requestId !== null ? ` (${display.requestId})` : ""}`;
        setActionError(msg);
        appToast.error(msg);
        setCancelOpen(false);
      },
    });
  };

  const plannedFor = activeSchedule.scheduled_for;

  return (
    <Card data-testid="execution-scheduled-card">
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2">
          <CalendarClock className="size-4" aria-hidden />
          {t("orders.execution.scheduledTitle")}
        </CardTitle>
        <CardDescription>
          {t("orders.execution.scheduledNote", { time: formatTimestamp(plannedFor) })}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <p className="text-muted-foreground text-xs">{t("orders.execution.scheduledCancelNote")}</p>
        <Button
          variant="outline"
          className="self-start"
          onClick={() => { setCancelOpen(true); setActionError(null); }}
          disabled={cancel.isPending}
          data-testid="execution-schedule-cancel"
        >
          <XCircle className="size-4" aria-hidden />
          {t("orders.execution.cancelSchedule")}
        </Button>
        {actionError !== null && (
          <p role="alert" className="text-destructive text-sm" data-testid="execution-schedule-cancel-error">
            {actionError}
          </p>
        )}
      </CardContent>

      <Dialog open={cancelOpen} onOpenChange={(open) => { if (!open) { setCancelOpen(false); setActionError(null); } }}>
        <DialogContent data-testid="execution-schedule-cancel-dialog">
          <DialogHeader>
            <DialogTitle>{t("orders.execution.cancelScheduleTitle")}</DialogTitle>
            <DialogDescription>{t("orders.execution.cancelScheduleDescription")}</DialogDescription>
          </DialogHeader>
          {actionError !== null && (
            <p role="alert" className="text-destructive text-sm">{actionError}</p>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => { setCancelOpen(false); setActionError(null); }} data-testid="execution-schedule-cancel-cancel">
              {t("common.cancel")}
            </Button>
            <PendingButton
              variant="destructive"
              onClick={runCancel}
              pending={cancel.isPending}
              data-testid="execution-schedule-cancel-confirm"
            >
              {t("orders.execution.cancelSchedule")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}


export function ExecutionWorkspaceCard({ order, attemptId, onAttemptCreated, onRecover, showActions = true }: {
  order: ChangeOrder;
  attemptId: string | null;
  onAttemptCreated: (attemptId: string) => void;
  onRecover: () => void;
  showActions?: boolean;
}) {
  return (
    // Every child returns null for most order states; an empty wrapper would
    // still be a flex item of the page column and add a second 30px gap.
    // Conditional mounting by order.state also resets ExecutionActionCard's
    // transient schedule confirmation hint when a schedule is cancelled.
    <div className="flex flex-col gap-[30px] empty:hidden">
      {showActions && order.state === "stage_execution_pending" && (
        <ExecutionActionCard order={order} onAttemptCreated={onAttemptCreated} />
      )}
      {showActions && order.state === "scheduled" && (
        <ScheduledCard order={order} />
      )}
      {attemptId !== null && (
        <ExecutionAttemptCard order={order} attemptId={attemptId} onRecover={onRecover} showActions={showActions} />
      )}
      <CopyDraftCard order={order} />
    </div>
  );
}
