import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { ReviewFinding, ReviewEvidence } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  listReviewFindingEvidence,
  recordRawReviewEvidenceCopy,
  revealRawReviewEvidence,
} from "@/api/generated/client/change-orders/change-orders";
import { Button } from "@/shared/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/shared/components/ui/sheet";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { ClipboardCopy, Eye, ShieldAlert } from "lucide-react";
import { describeError } from "@/shared/api/error-display";
import { writeClipboardText } from "@/shared/lib/clipboard";
import { unwrapApi } from "@/shared/api/mutator";
import { formatWatermark } from "@/shared/lib/timezones";

/**
 * Evidence sheet (migration contract §4 item 6, §6 sensitive-data boundary):
 * normalized evidence always renders; the Raw Payload is a separate explicit
 * reveal that is re-authorized per view, watermarked server-side, kept in
 * memory only (no web storage, no telemetry) and copied solely through the
 * copy-audit API. The 7-day retention countdown is shown while the raw
 * payload exists; after expiry only normalized evidence remains.
 */

interface EvidenceSheetProps {
  /**
   * One or more findings whose evidence renders together (owner ruling
   * 2026-10-31: a merged same-group card exposes ONE evidence button that
   * carries the whole group). With multiple findings each group is headed
   * by its finding title.
   */
  findings: ReviewFinding[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

interface RevealedRaw {
  text: string;
  revealId: string;
  watermark: string;
}

export function EvidenceSheet({ findings, open, onOpenChange }: EvidenceSheetProps) {
  const { t } = useTranslation();

  const findingIds = findings.map((finding) => finding.id);
  const evidenceQuery = useQuery({
    queryKey: ["review-finding-evidence", findingIds.join(",")],
    queryFn: async () =>
      Promise.all(
        findingIds.map((id) => unwrapApi(listReviewFindingEvidence(id))),
      ),
    enabled: open && findingIds.length > 0,
  });
  // Findings stay in call order; a finding may carry zero evidence rows.
  const evidenceGroups: Array<{ finding: ReviewFinding; evidence: ReviewEvidence[] }> =
    evidenceQuery.data != null
      ? findings.map((finding, index) => ({
          finding,
          evidence: evidenceQuery.data[index] ?? [],
        }))
      : [];

  const [revealed, setRevealed] = useState<Record<string, RevealedRaw>>({});
  // SEC-0035: copy-flow errors (clipboard write or copy-audit) surface inline
  // — a copy whose audit failed to record must never pass silently.
  const [copyError, setCopyError] = useState<string | null>(null);

  // Plaintext lives in component memory only: closing the sheet wipes every
  // revealed raw payload (migration contract §6); unmount does the same via
  // the cleanup-only effect below.
  const handleOpenChange = (next: boolean) => {
    if (!next) setRevealed({});
    onOpenChange(next);
  };
  useEffect(() => () => {
    setRevealed({});
  }, []);

  const revealMutation = useMutation({
    mutationFn: (evidenceId: string) =>
      unwrapApi(revealRawReviewEvidence(evidenceId, { purpose: "review-evidence" })),
    onSuccess: (payload, evidenceId) => {
      setRevealed((previous) => ({
        ...previous,
        [evidenceId]: {
          text: JSON.stringify(payload.raw_payload, null, 2),
          revealId: payload.reveal_id,
          watermark: payload.watermark,
        },
      }));
    },
  });

  const copyMutation = useMutation({
    mutationFn: (input: { evidenceId: string; revealId: string }) =>
      recordRawReviewEvidenceCopy(input.evidenceId, { source_reveal_id: input.revealId }),
  });

  const copyRevealed = (evidenceId: string) => {
    const revealedRaw = revealed[evidenceId];
    if (revealedRaw === undefined) return;
    setCopyError(null);
    // SEC-0035: the clipboard write is issued synchronously inside the click
    // handler (migration contract §6) — awaiting anything before it lets
    // Chromium's transient activation expire and silently drop the copy. The
    // audit request fires only after the plaintext actually reached the
    // clipboard, and every failure surfaces inline (nothing is swallowed):
    // an unrecorded audit must never pass silently after a real copy.
    void writeClipboardText(revealedRaw.text).then((ok) => {
      if (!ok) {
        setCopyError(t("precheck.evidence.copyFailed"));
        return;
      }
      return copyMutation
        .mutateAsync({ evidenceId, revealId: revealedRaw.revealId })
        .catch((auditError: unknown) => {
          const display = describeError(auditError, "recordRawReviewEvidenceCopy");
          setCopyError(
            `${t("precheck.evidence.copyAuditFailed", { detail: t(display.messageKey) })}${display.requestId !== null ? ` (${display.requestId})` : ""}`,
          );
        });
    });
  };

  // Each query/mutation is judged by its own operation error profile.
  const errorDisplay =
    revealMutation.error !== null
      ? describeError(revealMutation.error, "revealRawReviewEvidence")
      : evidenceQuery.error !== null
        ? describeError(evidenceQuery.error, "listReviewFindingEvidence")
        : null;

  return (
    <Sheet open={open} onOpenChange={handleOpenChange}>
      {/* Unified scrollbars (owner 2026-11-03): both Sheet panel and JSON snippet scrolling use
          ScrollArea overlay thumb (previously native overflow bars). */}
      <SheetContent className="sm:max-w-lg" data-testid="evidence-sheet">
        <ScrollArea className="min-h-0 flex-1">
          <div>
        <SheetHeader>
          <SheetTitle>{t("precheck.evidence.title")}</SheetTitle>
          <SheetDescription>{t("precheck.evidence.description")}</SheetDescription>
        </SheetHeader>
        <div className="space-y-4 px-5 pr-6 pb-8" data-testid="evidence-content">
          {errorDisplay !== null && (
            <p role="alert" className="text-destructive text-sm">
              {t(errorDisplay.messageKey)}
              {errorDisplay.requestId !== null ? ` (${errorDisplay.requestId})` : ""}
            </p>
          )}
          {copyError !== null && (
            <p role="alert" className="text-destructive text-sm" data-testid="evidence-copy-error">
              {copyError}
            </p>
          )}
          {evidenceGroups.length > 0 &&
            evidenceGroups.every((group) => group.evidence.length === 0) &&
            evidenceQuery.isSuccess && (
              <p className="text-muted-foreground text-sm">{t("precheck.evidence.empty")}</p>
            )}
          {evidenceGroups.map((group) => (
            <div key={group.finding.id} className="space-y-3">
              {evidenceGroups.length > 1 && (
                <p
                  className="text-muted-foreground border-b pb-1 text-xs font-medium"
                  data-testid="evidence-finding-header"
                >
                  {group.finding.title}
                </p>
              )}
              {group.evidence.map((entry) => {
                const revealedRaw = revealed[entry.id];
                const expiresAt = entry.raw_payload_expires_at;
                return (
                  <section
                    key={entry.id}
                    className="rounded-md border p-3 text-sm"
                    data-testid="evidence-item"
                  >
                    <header className="flex flex-wrap items-center justify-between gap-2">
                      <span className="font-medium">{entry.source_reference}</span>
                      <span className="text-muted-foreground text-xs">
                        {t(`precheck.evidence.fact.${entry.fact_status}`)}
                      </span>
                    </header>
                    <ScrollArea className="bg-muted mt-2 rounded-md">
                      <pre className="p-2 text-xs whitespace-pre">
                        {JSON.stringify(entry.normalized_fact, null, 2)}
                      </pre>
                    </ScrollArea>
                    {entry.has_raw_payload && expiresAt != null && (
                      <footer className="mt-2 space-y-2">
                        <p className="text-muted-foreground text-xs">
                          {t("precheck.evidence.retention", {
                            days: remainingDays(expiresAt),
                          })}
                        </p>
                        {revealedRaw === undefined ? (
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => { revealMutation.mutate(entry.id); }}
                            disabled={revealMutation.isPending}
                            data-testid={`reveal-raw-${entry.id}`}
                          >
                            <Eye className="size-3.5" aria-hidden />
                            {t("precheck.evidence.reveal")}
                          </Button>
                        ) : (
                          <div className="space-y-2" data-testid={`raw-view-${entry.id}`}>
                            <p className="text-muted-foreground flex items-center gap-1 text-xs">
                              <ShieldAlert className="size-3.5" aria-hidden />
                              {formatWatermark(revealedRaw.watermark)}
                            </p>
                            <ScrollArea className="bg-muted rounded-md">
                              <pre className="p-2 text-xs whitespace-pre">
                                {revealedRaw.text}
                              </pre>
                            </ScrollArea>
                            <Button
                              variant="outline"
                              size="sm"
                              onClick={() => {
                                copyRevealed(entry.id);
                              }}
                              data-testid={`copy-raw-${entry.id}`}
                            >
                              <ClipboardCopy className="size-3.5" aria-hidden />
                              {t("precheck.evidence.copy")}
                            </Button>
                          </div>
                        )}
                      </footer>
                    )}
                  </section>
                );
              })}
            </div>
          ))}
        </div>
          </div>
        </ScrollArea>
      </SheetContent>
    </Sheet>
  );
}

function remainingDays(expiresAt: string): number {
  const diff = new Date(expiresAt).getTime() - Date.now();
  return Math.max(0, Math.ceil(diff / (24 * 3600 * 1000)));
}
