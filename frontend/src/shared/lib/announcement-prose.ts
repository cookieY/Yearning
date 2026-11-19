import { cn } from "@/shared/lib/utils";

/**
 * Rendered-announcement typography. The announcement body arrives as
 * server-sanitized HTML (the revision records its `sanitizer_policy_version`),
 * so the client owns the rhythm but never the markup. No typography plugin is
 * a dependency here, therefore the scale is expressed with descendant
 * utilities that resolve to the same tokens as the surrounding cards — the
 * previous `prose` classes had no plugin behind them and did nothing.
 */
export const ANNOUNCEMENT_PROSE = cn(
  "text-muted-foreground text-sm leading-relaxed",
  "[&_h1]:text-foreground [&_h1]:mt-4 [&_h1]:text-base [&_h1]:font-semibold [&_h1:first-child]:mt-0",
  "[&_h2]:text-foreground [&_h2]:mt-4 [&_h2]:text-[15px] [&_h2]:font-semibold [&_h2:first-child]:mt-0",
  "[&_h3]:text-foreground [&_h3]:mt-3 [&_h3]:text-sm [&_h3]:font-semibold [&_h3:first-child]:mt-0",
  "[&_p]:mt-2 [&_p:first-child]:mt-0",
  "[&_strong]:text-foreground [&_strong]:font-medium",
  "[&_em]:italic",
  "[&_a]:text-chart-brand [&_a]:font-medium [&_a]:underline [&_a]:underline-offset-2",
  "[&_ul]:mt-2 [&_ul]:list-disc [&_ul]:space-y-1 [&_ul]:pl-5",
  "[&_ol]:mt-2 [&_ol]:list-decimal [&_ol]:space-y-1 [&_ol]:pl-5",
  "[&_li]:marker:text-muted-foreground/60",
  "[&_code]:bg-muted [&_code]:text-foreground [&_code]:rounded [&_code]:px-1 [&_code]:py-0.5 [&_code]:font-mono [&_code]:text-[12px]",
  "[&_pre]:bg-muted/40 [&_pre]:border-border [&_pre]:mt-2 [&_pre]:overflow-x-auto [&_pre]:rounded-lg [&_pre]:border [&_pre]:p-3 [&_pre_code]:bg-transparent [&_pre_code]:p-0",
  "[&_blockquote]:border-chart-brand [&_blockquote]:mt-2 [&_blockquote]:border-l-2 [&_blockquote]:pl-3 [&_blockquote]:italic",
  "[&_hr]:border-border [&_hr]:my-3",
  "[&_table]:mt-2 [&_table]:w-full [&_table]:text-[13px]",
  "[&_th]:text-foreground [&_th]:border-border [&_th]:border-b [&_th]:py-1 [&_th]:text-left [&_th]:font-medium",
  "[&_td]:border-border [&_td]:border-b [&_td]:py-1",
  "[&_img]:mt-2 [&_img]:max-w-full [&_img]:rounded-lg",
);
