import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Check, ClipboardCopy } from "lucide-react";
import { Button } from "@/shared/components/ui/button";
import {
  highlightSqlLine,
  highlightSqlText,
} from "@/shared/components/ui/code-block";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { splitBacktickSegments } from "@/shared/components/inline-code-text";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { writeClipboardText } from "@/shared/lib/clipboard";

/**
 * Finding rich text (owner ruling 2026-10-30, readability pass):
 *
 * The review contract has exactly three free-text fields per finding
 * (title / message / suggestion); there is no structured fact/impact split
 * and none is invented here. What IS contractual is the backtick convention:
 * the review system prompt requires code snippets and technical identifiers
 * to be wrapped in backticks, so backtick-delimited spans are rendered as real
 * inline code instead of literal markdown. Everything outside the spans is
 * plain prose; unmatched backticks fall back to literal text — no guessing
 * beyond the convention.
 *
 * Long or multi-line spans (candidate SQL, full statements) upgrade to a
 * scrollable code block with an explicit copy action.
 */

/** Spans at or above this length render as a code block instead of inline. */
const BLOCK_CODE_MIN_LENGTH = 96;

export function FindingText({ text }: { text: string }) {
  const segments = splitBacktickSegments(text);
  return (
    <>
      {segments.map((segment, index) =>
        segment.kind === "code" ? (
          isBlockCode(segment.value) ? (
            <FindingCodeBlock key={index} code={segment.value} />
          ) : (
            <code
              key={index}
              className="bg-muted rounded px-1 py-0.5 font-mono text-[0.875em] [overflow-wrap:anywhere]"
              data-testid="finding-inline-code"
            >
              {highlightSqlLine(segment.value)}
            </code>
          )
        ) : (
          <span key={index}>{segment.value}</span>
        ),
      )}
    </>
  );
}

function isBlockCode(value: string): boolean {
  return value.includes("\n") || value.length >= BLOCK_CODE_MIN_LENGTH;
}

function FindingCodeBlock({ code }: { code: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);

  const copy = () => {
    // Clipboard write fires synchronously inside the click handler
    // (transient activation; the audit-free variant of the SEC-0035 rule —
    // this is the user's own finding text, not a revealed raw payload).
    void writeClipboardText(code).then((ok) => {
      if (ok) {
        setCopied(true);
        window.setTimeout(() => { setCopied(false); }, 2000);
      } else {
        setCopied(false);
        appToast.error(t("common.copyFailed"));
      }
    });
  };

  return (
    <span className="my-1 block">
      <span className="bg-muted flex items-start justify-between gap-2 rounded-md p-2 pr-1">
        {/* Unified scrollbars (owner 2026-11-03): horizontal scrolling of SQL snippets uses ScrollArea overlay
            thumb (was native overflow-x-auto). whitespace-pre keeps lines unwrapped. */}
        <ScrollArea className="min-w-0 flex-1">
          <code
            className="block font-mono text-xs whitespace-pre"
            data-testid="finding-block-code"
          >
            {highlightSqlText(code)}
          </code>
        </ScrollArea>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={t("precheck.findings.copyCode")}
          onClick={copy}
          data-testid="finding-copy-code"
          className="shrink-0"
        >
          {copied ? (
            <Check className="size-3.5" aria-hidden />
          ) : (
            <ClipboardCopy className="size-3.5" aria-hidden />
          )}
        </Button>
      </span>
      <span role="status" aria-live="polite" className="sr-only">
        {copied ? t("precheck.findings.copyCodeDone") : ""}
      </span>
    </span>
  );
}
