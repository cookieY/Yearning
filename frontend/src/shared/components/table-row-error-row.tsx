import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertCircle, Check, Copy, X } from "lucide-react";
import { Button } from "@/shared/components/ui/button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { writeClipboardText } from "@/shared/lib/clipboard";
import { TableCell, TableRow } from "@/shared/components/ui/table";

export interface TableRowErrorRowProps {
  /** Number of columns in the parent table so the sub-row spans the full width. */
  colSpan: number;
  /** Localized primary error summary. */
  summary: string;
  /** Optional raw technical detail (e.g. probe error or upstream reason). */
  detail?: string | null;
  /** Optional dismiss handler; renders a trailing close button when provided. */
  onClose?: () => void;
  testId?: string;
  detailTestId?: string;
  copyTestId?: string;
  closeTestId?: string;
}

/**
 * Full-width diagnostic sub-row rendered beneath a table row when a row-level
 * action (such as connection probing) fails. Keeps the trailing actions column
 * compact while displaying the complete wrapping error message with one-click copy.
 */
export function TableRowErrorRow({
  colSpan,
  summary,
  detail,
  onClose,
  testId,
  detailTestId,
  copyTestId,
  closeTestId,
}: TableRowErrorRowProps) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const normalizedDetail =
    detail !== undefined && detail !== null && detail.trim() !== "" ? detail : null;
  const copyText = normalizedDetail !== null ? `${summary}\n${normalizedDetail}` : summary;

  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => {
      setCopied(false);
    }, 2000);
    return () => {
      window.clearTimeout(timer);
    };
  }, [copied]);

  const handleCopy = (): void => {
    void writeClipboardText(copyText).then((ok) => {
      if (ok) {
        setCopied(true);
      } else {
        setCopied(false);
        appToast.error(t("common.copyFailed"));
      }
    });
  };

  return (
    <TableRow className="bg-muted/25 hover:bg-muted/25 h-auto" data-testid={testId}>
      <TableCell colSpan={colSpan} className="px-5 py-2 whitespace-normal">
        <div
          role="alert"
          className="flex items-start justify-between gap-3 rounded-md border border-destructive/25 bg-destructive/5 px-3 py-2 text-left"
        >
          <div className="flex min-w-0 flex-1 items-start gap-2">
            <AlertCircle className="mt-0.5 size-4 shrink-0 text-destructive" aria-hidden />
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <p className="[overflow-wrap:anywhere] break-all text-xs leading-5 font-medium text-destructive">
                {summary}
              </p>
              {normalizedDetail !== null ? (
                <pre
                  className="max-h-40 max-w-full overflow-y-auto font-mono text-xs leading-5 [overflow-wrap:anywhere] break-all whitespace-pre-wrap text-destructive/90 select-text"
                  data-testid={detailTestId}
                >
                  {normalizedDetail}
                </pre>
              ) : null}
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-6 gap-1 px-2 text-xs text-muted-foreground hover:text-foreground"
              onClick={handleCopy}
              data-testid={copyTestId}
            >
              {copied ? <Check className="size-3" aria-hidden /> : <Copy className="size-3" aria-hidden />}
              {copied ? t("common.copied") : t("common.copy")}
            </Button>
            {onClose !== undefined ? (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="size-6 text-muted-foreground hover:text-foreground"
                onClick={onClose}
                aria-label={t("common.close")}
                data-testid={closeTestId}
              >
                <X className="size-3.5" aria-hidden />
              </Button>
            ) : null}
          </div>
        </div>
      </TableCell>
    </TableRow>
  );
}
