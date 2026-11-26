import { useState } from "react";
import { useTranslation } from "react-i18next";
import type {
  ChangeOrder,
  ExecutionAttempt,
  ExecutionStatement,
  OscExecutionDetails,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  ATTEMPT_TERMINAL_STATES,
  executionAttemptTone,
  ORDER_STATE_TONE_CLASS,
  statementStateTone,
} from "@/features/orders/order-state";
import {
  useCancelExecutionAttempt,
  useCreateExecutionVerification,
  useExecutionAttempt,
  useExecutionStatements,
} from "@/features/orders/use-execution";
import { useSession } from "@/features/auth/session-provider";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { PendingButton } from "@/shared/components/pending-button";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
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
import { Label } from "@/shared/components/ui/label";
import { Progress } from "@/shared/components/ui/progress";
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
import { Textarea } from "@/shared/components/ui/textarea";
import {
  CircleAlert,
  Gauge,
  ListChecks,
  RotateCw,
  ShieldQuestion,
  XCircle,
} from "lucide-react";

function StateBadge({
  state,
  labelRoot,
  tone,
}: {
  state: string;
  labelRoot: string;
  tone: (state: string) => ReturnType<typeof statementStateTone>;
}) {
  const { t } = useTranslation();
  return (
    <Badge variant="outline" className={`${ORDER_STATE_TONE_CLASS[tone(state)]} text-xs`}>
      {t(`${labelRoot}.${state}`, { defaultValue: state })}
    </Badge>
  );
}

/** Per-statement ledger (PRD F8 item 2): ordinal, kind, state, affected rows,
 * duration and sanitized error. `unknown` renders in its own high-risk
 * wording — never as not-executed (gate: Unknown is never displayed as not-executed). */
function StatementLedger({ attemptId, live }: { attemptId: string; live: boolean }) {
  const { t, i18n } = useTranslation();
  const statementsQuery = useExecutionStatements(attemptId, true, live);

  if (statementsQuery.isPending) return <LoadingState variant="table" columns={6} />;
  if (statementsQuery.isError) {
    return (
      <Alert variant="destructive">
        <CircleAlert aria-hidden />
        <AlertTitle>{t("orders.execution.statementsError")}</AlertTitle>
        <Button variant="outline" size="sm" className="mt-2" onClick={() => void statementsQuery.refetch()}>
          <RotateCw aria-hidden />
          {t("errors.retry")}
        </Button>
      </Alert>
    );
  }
  const items = statementsQuery.data;
  const duration = (statement: ExecutionStatement): string => {
    if (statement.started_at == null || statement.finished_at == null) return "—";
    const ms = Date.parse(statement.finished_at) - Date.parse(statement.started_at);
    return `${String(ms)} ms`;
  };
  return (
    <div className="rounded-md border" data-testid="execution-statements">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-16">#</TableHead>
            <TableHead className="w-20">{t("orders.execution.statementKind")}</TableHead>
            <TableHead>{t("orders.execution.statementState")}</TableHead>
            <TableHead className="w-28">{t("orders.execution.affectedRows")}</TableHead>
            <TableHead className="w-24">{t("orders.execution.duration")}</TableHead>
            <TableHead>{t("orders.execution.failureName")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((statement) => (
            <TableRow key={statement.id} data-testid={`execution-statement-${String(statement.ordinal)}`}>
              <TableCell className="font-mono text-xs">{String(statement.ordinal)}</TableCell>
              <TableCell>
                <Badge variant="outline" className="text-xs uppercase">
                  {statement.statement_kind}
                </Badge>
              </TableCell>
              <TableCell>
                <StateBadge state={statement.state} labelRoot="orders.statementState" tone={statementStateTone} />
              </TableCell>
              <TableCell>{statement.affected_row_count === null ? "—" : String(statement.affected_row_count)}</TableCell>
              <TableCell className="text-xs">{duration(statement)}</TableCell>
              <TableCell className="max-w-sm whitespace-normal">
                {statement.failure_name == null || statement.failure_name === "" ? (
                  <span className="text-muted-foreground text-xs">—</span>
                ) : i18n.exists(`orders.execution.failureReasons.${statement.failure_name}`) ? (
                  <div className="flex flex-col gap-0.5">
                    <span className="text-xs leading-snug text-foreground">
                      {t(`orders.execution.failureReasons.${statement.failure_name}`)}
                    </span>
                    <span className="text-muted-foreground font-mono text-[11px]">
                      {statement.failure_name}
                    </span>
                  </div>
                ) : (
                  <span className="font-mono text-xs">{statement.failure_name}</span>
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {live && <p className="border-t px-3 py-2 text-xs text-muted-foreground">{t("orders.execution.liveNote")}</p>}
    </div>
  );
}

/** OSC progress and residual surface (E006): phase, progress, rows copied and
 * the leftover-resource state a failed/cancelled gh-ost job must show. */
function OscProgress({ osc }: { osc: OscExecutionDetails }) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-2 rounded-md border p-3" data-testid="execution-osc">
      <p className="flex flex-wrap items-center gap-2 text-sm">
        <Gauge className="size-4 text-muted-foreground" aria-hidden />
        <span className="font-medium">gh-ost</span>
        <StateBadge state={osc.phase} labelRoot="orders.oscPhase" tone={executionAttemptTone} />
      </p>
      <Progress value={(osc.progress_basis_points / 100)} className="h-2" data-testid="execution-osc-progress" />
      <p className="text-xs text-muted-foreground">
        {t("orders.execution.oscRows", { rows: osc.rows_copied, percent: Math.floor(osc.progress_basis_points / 100) })}
      </p>
      <p className="flex flex-wrap items-center gap-2 text-xs">
        <span className="text-muted-foreground">{t("orders.execution.oscResidual")}</span>
        <StateBadge state={osc.residual_state} labelRoot="orders.oscResidualState" tone={statementStateTone} />
      </p>
    </div>
  );
}

/** Manual verification for result_unknown (E005): four fixed verdicts, reason
 * and database-side evidence are mandatory; only frozen executors see the
 * form, and the manual-confirmation marker persists afterwards. */
function VerificationCard({ order, attempt, onRecover, showActions }: {
  order: ChangeOrder;
  attempt: ExecutionAttempt;
  onRecover: () => void;
  showActions: boolean;
}) {
  const { t } = useTranslation();
  const session = useSession();
  const verify = useCreateExecutionVerification(attempt, order.id);
  const [result, setResult] = useState<string>("");
  const [reason, setReason] = useState("");
  const [evidence, setEvidence] = useState<Array<{ kind: string; content: string }>>([
    { kind: "database_fact", content: "" },
  ]);
  const [dialogError, setDialogError] = useState<string | null>(null);

  // The verification right follows the attempt's frozen stage, not the
  // current workflow position; the form only exists while the order itself
  // still sits in result_unknown (a first non-still_unknown verdict
  // terminalizes the order and closes the path — backend answers 1010 after).
  if (order.state !== "result_unknown" || attempt.state !== "result_unknown") return null;
  const stageOfAttempt = order.stages.find((candidate) => candidate.id === attempt.stage_id) ?? null;
  const isExecutor = stageOfAttempt?.execution_actors.some((actor) => actor.id === session.user?.id) ?? false;

  if (!showActions || !isExecutor || stageOfAttempt === null) {
    return (
      <Alert data-testid="verification-waiting">
        <ShieldQuestion className="size-4" aria-hidden />
        <AlertTitle>{t("orders.execution.verifyWaitingTitle")}</AlertTitle>
        <AlertDescription>{t("orders.execution.verifyWaitingDescription")}</AlertDescription>
      </Alert>
    );
  }

  const evidenceComplete = evidence.every((entry) => entry.content.trim() !== "");
  const canSubmit = result !== "" && reason.trim() !== "" && evidenceComplete;

  const submit = (): void => {
    verify.mutate(
      {
        result: result as "confirmed_succeeded" | "confirmed_failed" | "confirmed_partial" | "still_unknown",
        reason: reason.trim(),
        evidence: evidence.map((entry) => ({
          kind: entry.kind as "text" | "database_fact" | "external_reference",
          content: entry.content.trim(),
        })),
      },
      {
        onSuccess: () => {
          setDialogError(null);
          appToast.success(t("orders.execution.verifySubmit"));
          onRecover();
        },
        onError: (error) => {
          const display = describeError(error, "createExecutionVerification");
          const msg = `${t(display.messageKey)}${display.requestId !== null ? ` (${display.requestId})` : ""}`;
          setDialogError(msg);
          appToast.error(msg);
        },
      },
    );
  };

  return (
    <div className="flex flex-col gap-3 rounded-md border border-amber-600/40 bg-amber-600/5 p-3" data-testid="verification-form">
      <Alert className="border-amber-600/40">
        <CircleAlert className="size-4" aria-hidden />
        <AlertTitle>{t("orders.execution.verifyTitle")}</AlertTitle>
        <AlertDescription>{t("orders.execution.verifyDescription")}</AlertDescription>
      </Alert>
      <div className="flex flex-wrap gap-2" role="radiogroup" aria-label={t("orders.execution.verifyResult")} data-testid="verification-result-group">
        {(["confirmed_succeeded", "confirmed_failed", "confirmed_partial", "still_unknown"] as const).map((value) => (
          <Button
            key={value}
            type="button"
            role="radio"
            aria-checked={result === value}
            variant={result === value ? "default" : "outline"}
            onClick={() => { setResult(value); setDialogError(null); }}
            data-testid={`verification-result-${value}`}
          >
            {t(`orders.execution.verifyResult.${value}`)}
          </Button>
        ))}
      </div>
      <div className="flex flex-col gap-1">
        <Label htmlFor="verification-reason">{t("orders.execution.verifyReason")}</Label>
        <Textarea
          id="verification-reason"
          value={reason}
          onChange={(event) => { setReason(event.target.value); }}
          rows={2}
          maxLength={4096}
          data-testid="verification-reason"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label className="text-xs">{t("orders.execution.verifyEvidence")}</Label>
        {evidence.map((entry, index) => (
          <div key={index} className="flex items-center gap-2">
            <Select
              value={entry.kind}
              onValueChange={(kind) => {
                if (kind === null) return;
                setEvidence((entries) => entries.map((item, itemIndex) => (itemIndex === index ? { ...item, kind } : item)));
              }}
              items={{
                text: t("orders.execution.evidenceKind.text"),
                database_fact: t("orders.execution.evidenceKind.database_fact"),
                external_reference: t("orders.execution.evidenceKind.external_reference"),
              }}
            >
              <SelectTrigger className="w-44" data-testid={`verification-evidence-kind-${String(index)}`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="text">{t("orders.execution.evidenceKind.text")}</SelectItem>
                <SelectItem value="database_fact">{t("orders.execution.evidenceKind.database_fact")}</SelectItem>
                <SelectItem value="external_reference">{t("orders.execution.evidenceKind.external_reference")}</SelectItem>
              </SelectContent>
            </Select>
            <Input
              value={entry.content}
              onChange={(event) => {
                setEvidence((entries) => entries.map((item, itemIndex) => (itemIndex === index ? { ...item, content: event.target.value } : item)));
              }}
              placeholder={t("orders.execution.evidencePlaceholder")}
              data-testid={`verification-evidence-content-${String(index)}`}
            />
            {evidence.length > 1 && (
              <Button
                variant="ghost"
                size="icon"
                aria-label={t("orders.execution.evidenceRemove")}
                onClick={() => { setEvidence((entries) => entries.filter((_, itemIndex) => itemIndex !== index)); }}
              >
                <XCircle className="size-4" aria-hidden />
              </Button>
            )}
          </div>
        ))}
        <Button
          variant="outline"
          className="self-start"
          onClick={() => { setEvidence((entries) => [...entries, { kind: "database_fact", content: "" }]); }}
          data-testid="verification-evidence-add"
        >
          {t("orders.execution.evidenceAdd")}
        </Button>
      </div>
      {dialogError !== null && (
        <p role="alert" className="text-destructive text-sm" data-testid="verification-error">{dialogError}</p>
      )}
      <PendingButton pending={verify.isPending} onClick={submit} disabled={!canSubmit} data-testid="verification-submit">
        {t("orders.execution.verifySubmit")}
      </PendingButton>
    </div>
  );
}

/** The known attempt: progress, cancel (any frozen executor may request it),
 * statement ledger, OSC surface and the verification form. */
export function ExecutionAttemptCard({ order, attemptId, onRecover, showActions }: {
  order: ChangeOrder;
  attemptId: string;
  onRecover: () => void;
  showActions: boolean;
}) {
  const { t } = useTranslation();
  const session = useSession();
  const attemptQuery = useExecutionAttempt(attemptId, order.id, true);
  const cancel = useCancelExecutionAttempt(attemptQuery.data ?? null, order.id);
  const [cancelOpen, setCancelOpen] = useState(false);
  const [cancelReason, setCancelReason] = useState("");
  const [cancelError, setCancelError] = useState<string | null>(null);

  if (attemptQuery.isPending) {
    return (
      <Card data-testid="execution-attempt-card">
        <CardHeader className="pb-3">
          <CardTitle className="flex items-center gap-2">
            <ListChecks className="size-4" aria-hidden />
            {t("orders.execution.attemptTitle")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <LoadingState variant="table" columns={6} />
        </CardContent>
      </Card>
    );
  }
  if (attemptQuery.isError) {
    return (
      <Card data-testid="execution-attempt-card">
        <CardHeader className="pb-3">
          <CardTitle className="flex items-center gap-2">
            <ListChecks className="size-4" aria-hidden />
          {t("orders.execution.attemptTitle")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <ErrorState
            error={attemptQuery.error}
            operationId="getExecutionAttempt"
            onRetry={() => void attemptQuery.refetch()}
          />
        </CardContent>
      </Card>
    );
  }
  const attempt: ExecutionAttempt = attemptQuery.data;
  const live = !ATTEMPT_TERMINAL_STATES.has(attempt.state);
  const stageOfAttempt = order.stages.find((candidate) => candidate.id === attempt.stage_id) ?? null;
  const isExecutor = stageOfAttempt?.execution_actors.some((actor) => actor.id === session.user?.id) ?? false;

  const submitCancel = (): void => {
    cancel.mutate(cancelReason.trim(), {
      onSuccess: () => {
        setCancelOpen(false);
        setCancelReason("");
        setCancelError(null);
        appToast.info(t("orders.execution.cancel"));
      },
      onError: (error) => {
        const display = describeError(error, "cancelExecutionAttempt");
        const msg = `${t(display.messageKey)}${display.requestId !== null ? ` (${display.requestId})` : ""}`;
        setCancelError(msg);
        appToast.error(msg);
        onRecover();
      },
    });
  };

  return (
    <Card data-testid="execution-attempt-card">
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2">
          <ListChecks className="size-4" aria-hidden />
          {t("orders.execution.attemptTitle")}
          {attempt.send_boundary === "sent" && (
            <Badge variant="outline" className="text-xs">{t("orders.execution.boundarySent")}</Badge>
          )}
        </CardTitle>
        <CardDescription>
          {attempt.state === "preflight" || attempt.state === "running" || ATTEMPT_TERMINAL_STATES.has(attempt.state)
            ? t("orders.execution.preflightHashOk")
            : t("orders.execution.preflightPending")}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {attempt.osc != null && <OscProgress osc={attempt.osc} />}
        <StatementLedger attemptId={attempt.id} live={live} />
        {attempt.state === "result_unknown" && (
          <VerificationCard order={order} attempt={attempt} onRecover={onRecover} showActions={showActions} />
        )}
        {showActions && isExecutor && attempt.state === "running" && (
          <Button
            variant="outline"
            className="self-start"
            onClick={() => { setCancelOpen(true); setCancelError(null); }}
            disabled={cancel.isPending}
            data-testid="execution-cancel"
          >
            <XCircle className="size-4" aria-hidden />
            {t("orders.execution.cancel")}
          </Button>
        )}
      </CardContent>

      <Dialog open={showActions && cancelOpen} onOpenChange={(open) => { if (!open) { setCancelOpen(false); setCancelError(null); } }}>
        <DialogContent data-testid="execution-cancel-dialog">
          <DialogHeader>
            <DialogTitle>{t("orders.execution.cancelTitle")}</DialogTitle>
            <DialogDescription>{t("orders.execution.cancelDescription")}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor="execution-cancel-reason">{t("orders.execution.cancelReason")}</Label>
            <Textarea
              id="execution-cancel-reason"
              value={cancelReason}
              onChange={(event) => { setCancelReason(event.target.value); }}
              rows={3}
              maxLength={4096}
              data-testid="execution-cancel-reason"
            />
          </div>
          {cancelError !== null && (
            <p role="alert" className="text-destructive text-sm" data-testid="execution-cancel-error">{cancelError}</p>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => { setCancelOpen(false); setCancelError(null); }} data-testid="execution-cancel-cancel">
              {t("common.cancel")}
            </Button>
            <PendingButton
              variant="destructive"
              onClick={submitCancel}
              pending={cancel.isPending}
              disabled={cancelReason.trim() === ""}
              data-testid="execution-cancel-confirm"
            >
              {t("orders.execution.cancel")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
