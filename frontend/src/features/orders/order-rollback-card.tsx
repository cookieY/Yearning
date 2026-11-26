import { useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { useTranslation } from "react-i18next";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import type { ChangeOrder } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

type RollbackOrderStatus = NonNullable<ChangeOrder["rollback_status"]>;
import { OrderStateBadge } from "@/features/orders/order-state-badge";
import { SqlFileViewer } from "@/features/orders/sql-file-viewer";
import { useRecordOrderSqlCopy, useRevealOrderSql, type OrderSqlReveal } from "@/features/orders/use-approvals";
import { useCreateRollbackDraft } from "@/features/orders/use-orders";
import { describeError } from "@/shared/api/error-display";
import { writeClipboardText } from "@/shared/lib/clipboard";
import { formatTimestamp, formatWatermark } from "@/shared/lib/timezones";
import { PendingButton } from "@/shared/components/pending-button";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import {
  CheckCircle2,
  CircleAlert,
  ClipboardCopy,
  Eye,
  ExternalLink,
  RotateCcw,
  ShieldAlert,
} from "lucide-react";

const STATUS_BADGE_VARIANT: Record<
  RollbackOrderStatus,
  "default" | "secondary" | "destructive" | "outline"
> = {
  not_requested: "outline",
  pending: "secondary",
  available: "default",
  unsupported: "destructive",
  evidence_incomplete: "destructive",
  conflicted: "destructive",
  rollback_in_progress: "secondary",
  rolled_back: "secondary",
  rollback_failed: "destructive",
};

function hasCapturableRollbackSql(status: RollbackOrderStatus): boolean {
  return (
    status === "available" ||
    status === "rollback_in_progress" ||
    status === "rolled_back" ||
    status === "conflicted" ||
    status === "rollback_failed"
  );
}

export function OrderRollbackCard({ order }: { order: ChangeOrder }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const reveal = useRevealOrderSql(order.id);
  const copyAudit = useRecordOrderSqlCopy(order.id);
  const createRollbackDraft = useCreateRollbackDraft(order);

  const [revealed, setRevealed] = useState<OrderSqlReveal | null>(null);
  const [error, setError] = useState<string | null>(null);
  const autoRevealedForOrderRef = useRef<string | null>(null);

  const status: RollbackOrderStatus =
    order.rollback_status ?? (order.rollback_enabled ? "pending" : "not_requested");
  const isRollbackTicket = order.ticket_type === "rollback";
  const canRevealRollbackSql = !isRollbackTicket && hasCapturableRollbackSql(status);
  const canGenerateRollbackTicket =
    !isRollbackTicket &&
    (status === "available" || status === "rollback_failed") &&
    (order.state === "completed" || order.state === "partial_failed" || order.state === "partial_cancelled");

  useEffect(() => () => { setRevealed(null); }, []);

  // Automatically reveal the rollback SQL when viewing a completed order with
  // available rollback evidence so the user sees the rollback SQL immediately.
  useEffect(() => {
    if (!canRevealRollbackSql || order.state !== "completed") return;
    if (autoRevealedForOrderRef.current === `${order.id}:${status}` || reveal.isPending) return;
    autoRevealedForOrderRef.current = `${order.id}:${status}`;
    reveal.mutate("rollback-order-sql", {
      onSuccess: (data) => {
        setRevealed(data);
        setError(null);
      },
      onError: () => {
        // Keep silent on background auto-reveal failure; manual reveal button remains available.
      },
    });
  }, [canRevealRollbackSql, order.id, order.state, reveal, status]);

  if (!isRollbackTicket && !order.rollback_enabled && status === "not_requested") {
    return null;
  }

  const handleReveal = (): void => {
    reveal.mutate("rollback-order-sql", {
      onSuccess: (data) => {
        setRevealed(data);
        setError(null);
      },
      onError: (revealError) => {
        const display = describeError(revealError, "revealOrderSql");
        const detail = display.detail !== null ? ` ${display.detail}` : "";
        setError(
          `${t(display.messageKey)}${detail}${display.requestId !== null ? ` (${display.requestId})` : ""}`,
        );
      },
    });
  };

  const handleCopy = (): void => {
    if (revealed === null) return;
    setError(null);
    void writeClipboardText(revealed.sql).then((ok) => {
      if (!ok) {
        setError(t("orders.sql.copyFailed"));
        toast.error(t("orders.sql.copyFailed"));
        return;
      }
      toast.success(t("orders.rollback.copiedToast"));
      return copyAudit.mutateAsync(revealed.revealId).catch((auditError: unknown) => {
        const display = describeError(auditError, "recordOrderSqlCopy");
        setError(
          `${t("orders.sql.copyAuditFailed", { detail: t(display.messageKey) })}${display.requestId !== null ? ` (${display.requestId})` : ""}`,
        );
      });
    });
  };

  const handleGenerateRollbackTicket = (): void => {
    setError(null);
    createRollbackDraft.mutate(
      {},
      {
        onSuccess: (draft) => {
          toast.success(t("orders.rollback.draftCreatedToast"));
          void navigate(`/changes/drafts/${draft.id}`);
        },
        onError: (createError) => {
          const display = describeError(createError, "createRollbackDraft");
          const detail = display.detail !== null ? ` ${display.detail}` : "";
          const msg = `${t(display.messageKey)}${detail}${display.requestId !== null ? ` (${display.requestId})` : ""}`;
          setError(msg);
          toast.error(msg);
        },
      },
    );
  };

  const rollbackOrders = order.rollback_orders ?? [];

  return (
    <Card data-testid="order-rollback-card">
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <CardTitle className="flex items-center gap-2">
            <RotateCcw className="size-4" aria-hidden />
            <span>{t("orders.rollback.title")}</span>
            <Badge
              variant={STATUS_BADGE_VARIANT[status]}
              data-testid="order-rollback-status-badge"
            >
              {t(`orders.rollback.status.${status}`)}
            </Badge>
          </CardTitle>
          <CardDescription>
            {isRollbackTicket
              ? t("orders.rollback.isRollbackOrderDescription")
              : t(`orders.rollback.statusDescription.${status}`)}
          </CardDescription>
        </div>

        {!isRollbackTicket && (
          <div className="flex items-center gap-2">
            <PendingButton
              size="sm"
              pending={createRollbackDraft.isPending}
              disabled={!canGenerateRollbackTicket}
              onClick={handleGenerateRollbackTicket}
              data-testid="generate-rollback-order"
            >
              <RotateCcw className="size-3.5" aria-hidden />
              {t("orders.rollback.generateTicket")}
            </PendingButton>
          </div>
        )}
      </CardHeader>

      <CardContent className="flex flex-col gap-4 text-sm">
        {isRollbackTicket && order.source_order_id != null && (
          <div
            className="bg-muted/30 flex flex-wrap items-center justify-between gap-2 rounded-lg border px-3.5 py-2.5"
            data-testid="order-rollback-source-link-row"
          >
            <span className="text-muted-foreground text-xs">
              {t("orders.rollback.sourceOrderLabel")}
            </span>
            <Link
              to={`/changes/orders/${order.source_order_id}`}
              className="inline-flex items-center gap-1.5 font-mono text-xs font-medium underline underline-offset-4 hover:opacity-80"
              data-testid="order-rollback-source-link"
            >
              <span>{order.source_order_display_number ?? order.source_order_id}</span>
              <ExternalLink className="size-3" aria-hidden />
            </Link>
          </div>
        )}

        {order.rollback_unsupported_reason != null && order.rollback_unsupported_reason !== "" && (
          <Alert variant="destructive" data-testid="order-rollback-unsupported-alert">
            <CircleAlert className="size-4" aria-hidden />
            <AlertTitle>{t("orders.rollback.unsupportedAlertTitle")}</AlertTitle>
            <AlertDescription>{order.rollback_unsupported_reason}</AlertDescription>
          </Alert>
        )}

        {order.rollback_conflict_reason != null && order.rollback_conflict_reason !== "" && (
          <Alert variant="destructive" data-testid="order-rollback-conflict-alert">
            <CircleAlert className="size-4" aria-hidden />
            <AlertTitle>{t("orders.rollback.conflictAlertTitle")}</AlertTitle>
            <AlertDescription>{order.rollback_conflict_reason}</AlertDescription>
          </Alert>
        )}

        {status === "rolled_back" && (
          <Alert data-testid="order-rollback-completed-alert">
            <CheckCircle2 className="size-4" aria-hidden />
            <AlertTitle>{t("orders.rollback.rolledBackAlertTitle")}</AlertTitle>
            <AlertDescription>{t("orders.rollback.rolledBackAlertDescription")}</AlertDescription>
          </Alert>
        )}

        {error !== null && (
          <p role="alert" className="text-destructive text-xs" data-testid="order-rollback-error">
            {error}
          </p>
        )}

        {canRevealRollbackSql && (
          <div className="flex flex-col overflow-hidden rounded-lg border" data-testid="order-rollback-sql-section">
            {revealed === null ? (
              <div className="flex flex-wrap items-center justify-between gap-3 p-3.5">
                <p className="text-muted-foreground text-xs">
                  {t("orders.rollback.revealHint")}
                </p>
                <PendingButton
                  size="sm"
                  variant="outline"
                  pending={reveal.isPending}
                  onClick={handleReveal}
                  data-testid="reveal-rollback-sql"
                >
                  <Eye className="size-3.5" aria-hidden />
                  {t("orders.rollback.revealAction")}
                </PendingButton>
              </div>
            ) : (
              <div className="flex flex-col" data-testid="order-rollback-sql-view">
                <SqlFileViewer value={revealed.sql} data-testid="order-rollback-sql-readonly" />
                <div className="flex flex-wrap items-center gap-2 border-t border-border px-3 py-2">
                  <p className="text-muted-foreground flex min-w-0 flex-1 items-center gap-1.5 text-xs">
                    <ShieldAlert className="size-3.5 shrink-0" aria-hidden />
                    <span className="truncate">{formatWatermark(revealed.watermark)}</span>
                  </p>
                  <span className="text-muted-foreground whitespace-nowrap text-xs">
                    {t("orders.sql.validUntil", {
                      time: formatTimestamp(revealed.validUntil),
                    })}
                  </span>
                  <PendingButton
                    variant="outline"
                    size="sm"
                    pending={copyAudit.isPending}
                    onClick={handleCopy}
                    data-testid="copy-rollback-sql"
                  >
                    <ClipboardCopy className="size-3.5" aria-hidden />
                    {t("orders.rollback.copyAction")}
                  </PendingButton>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => { setRevealed(null); }}
                    data-testid="close-rollback-sql"
                  >
                    {t("orders.sql.close")}
                  </Button>
                </div>
              </div>
            )}
          </div>
        )}

        {rollbackOrders.length > 0 && (
          <div className="flex flex-col gap-2" data-testid="order-rollback-derived-orders">
            <p className="text-muted-foreground text-xs font-medium">
              {t("orders.rollback.derivedOrdersLabel")}
            </p>
            <ul className="flex flex-col gap-2">
              {rollbackOrders.map((rbOrder) => (
                <li
                  key={rbOrder.id}
                  className="bg-muted/20 flex flex-wrap items-center justify-between gap-2 rounded-lg border px-3 py-2 text-xs"
                  data-testid={`rollback-derived-order-${rbOrder.id}`}
                >
                  <Link
                    to={`/changes/orders/${rbOrder.id}`}
                    className="inline-flex items-center gap-1.5 font-mono font-medium underline underline-offset-4 hover:opacity-80"
                  >
                    <span>{rbOrder.display_number}</span>
                    <ExternalLink className="size-3" aria-hidden />
                  </Link>
                  <OrderStateBadge state={rbOrder.state} />
                </li>
              ))}
            </ul>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
