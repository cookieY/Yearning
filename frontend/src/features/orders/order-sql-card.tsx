import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useRecordOrderSqlCopy, useRevealOrderSql, type OrderSqlReveal } from "@/features/orders/use-approvals";
import { SqlFileViewer } from "@/features/orders/sql-file-viewer";
import { describeError } from "@/shared/api/error-display";
import { formatTimestamp, formatWatermark } from "@/shared/lib/timezones";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { PendingButton } from "@/shared/components/pending-button";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { ClipboardCopy, Eye, FileCode2, ShieldAlert } from "lucide-react";

/**
 * Frozen SQL Revision viewer (PRD F7 item 1 — the reviewer judges the exact
 * SQL the submission froze). The plaintext never lives in the contract
 * surface (the order carries sql_hash only): every view is an explicit,
 * per-view authorized reveal (sensitive_reveal profile), watermarked with
 * the viewer and server time, valid for five minutes. Plaintext stays in
 * component memory only — closing the viewer or unmounting wipes it (no web
 * storage, no telemetry); copying goes through the audited copy-event API
 * before the clipboard write, mirroring the draft-side reveal contract.
 */
export function OrderSqlCard({ orderId, embedded = false }: { orderId: string; embedded?: boolean }) {
  const { t } = useTranslation();
  const reveal = useRevealOrderSql(orderId);
  const copyAudit = useRecordOrderSqlCopy(orderId);
  const [revealed, setRevealed] = useState<OrderSqlReveal | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Plaintext lives in component memory only: unmount wipes the reveal.
  useEffect(() => () => { setRevealed(null); }, []);

  const revealNow = (): void => {
    reveal.mutate("review-order-sql", {
      onSuccess: (data) => {
        setRevealed(data);
        setError(null);
      },
      onError: (revealError) => {
        const display = describeError(revealError, "revealOrderSql");
        setError(
          `${t(display.messageKey)}${display.requestId !== null ? ` (${display.requestId})` : ""}`,
        );
      },
    });
  };

  const closeReveal = (): void => {
    setRevealed(null);
  };

  const copyRevealed = (): void => {
    if (revealed === null) return;
    setError(null);
    // SEC-0035: the clipboard write is issued synchronously inside the click
    // handler — awaiting anything before it would let Chromium's transient
    // activation expire and silently drop the copy. The audit request fires
    // only after the plaintext actually reached the clipboard, and every
    // failure surfaces through the inline error alert (nothing is swallowed):
    // an unrecorded audit must never pass silently after a real copy.
    void navigator.clipboard
      .writeText(revealed.sql)
      .then(() => {
        appToast.success(t("orders.sql.copy"));
        return copyAudit.mutateAsync(revealed.revealId).catch((auditError: unknown) => {
          const display = describeError(auditError, "recordOrderSqlCopy");
          const msg = `${t("orders.sql.copyAuditFailed", { detail: t(display.messageKey) })}${display.requestId !== null ? ` (${display.requestId})` : ""}`;
          setError(msg);
          appToast.error(msg);
        });
      })
      .catch(() => {
        setError(t("orders.sql.copyFailed"));
        appToast.error(t("orders.sql.copyFailed"));
      });
  };

  const body = (
    <>
      {revealed === null ? (
        <>
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2">
              <FileCode2 className="size-4" aria-hidden />
              {t("orders.sql.title")}
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3 text-sm">
            {error !== null && (
              <p role="alert" className="text-destructive text-xs" data-testid="order-sql-error">
                {error}
              </p>
            )}
            <div className="flex items-center gap-3">
              <PendingButton
                size="sm"
                variant="outline"
                onClick={revealNow}
                pending={reveal.isPending}
                data-testid="reveal-order-sql"
              >
                <Eye className="size-3.5" aria-hidden />
                {t("orders.sql.reveal")}
              </PendingButton>
              <p className="text-muted-foreground text-xs">{t("orders.sql.revealNote")}</p>
            </div>
          </CardContent>
        </>
      ) : (
        /* collapsible-08 "Expandable Code" silhouette (owner ruling
           2026-10-31, second round): the card itself becomes the block —
           flush mono header bar → numbered code → show-more trigger — with
           the audit row (watermark / validity / copy / close) as the bottom
           bar. No nested box, no padded inset. */
        <div className="flex flex-col" data-testid="order-sql-view">
          <SqlFileViewer value={revealed.sql} data-testid="order-sql-readonly" />
          {error !== null && (
            <p
              role="alert"
              className="border-t border-border px-3 py-2 text-xs text-destructive"
              data-testid="order-sql-error"
            >
              {error}
            </p>
          )}
          <div className="flex flex-wrap items-center gap-2 border-t border-border px-3 py-2">
            <p className="text-muted-foreground flex min-w-0 flex-1 items-center gap-1.5 text-xs">
              <ShieldAlert className="size-3.5 shrink-0" aria-hidden />
              <span className="truncate">{formatWatermark(revealed.watermark)}</span>
            </p>
            <span className="text-muted-foreground whitespace-nowrap text-xs">
              {t("orders.sql.validUntil", { time: formatTimestamp(revealed.validUntil) })}
            </span>
            <PendingButton
              variant="outline"
              size="sm"
              pending={copyAudit.isPending}
              onClick={copyRevealed}
              data-testid="copy-order-sql"
            >
              <ClipboardCopy className="size-3.5" aria-hidden />
              {t("orders.sql.copy")}
            </PendingButton>
            <Button variant="outline" size="sm" onClick={closeReveal} data-testid="close-order-sql">
              {t("orders.sql.close")}
            </Button>
          </div>
        </div>
      )}
    </>
  );

  // Embedded in a merged card the section carries no surface of its own: the
  // revealed viewer stays flush (it is the block), the idle state pads itself.
  if (embedded) {
    return (
      <section
        data-testid="order-sql-card"
        className={
          revealed === null
            ? "flex flex-col gap-(--card-spacing) py-(--card-spacing) [--card-spacing:--spacing(4)]"
            : "flex flex-col"
        }
      >
        {body}
      </section>
    );
  }
  return (
    <Card
      data-testid="order-sql-card"
      className={revealed === null ? undefined : "gap-0 py-0"}
    >
      {body}
    </Card>
  );
}
