import { useTranslation } from "react-i18next";
import type { ReviewFinding } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { cn } from "@/shared/lib/utils";

/**
 * Severity distribution of a finding set as a NUMERIC row (owner page
 * feedback 2026-11-04: no chart, use numeric format; round 2 on same day: remove the "Risk Distribution"
 * heading and move the row right below the "Risk Findings" header row). No heading of its own — the row
 * renders right under FindingList's title bar. One item per severity, most
 * severe first — all five always present, zeros included, so orders stay
 * comparable at a glance: severity dot + localized label + exact count.
 * The dots reuse finding-list's severity ink (--risk-* tokens, muted for
 * low/info); the label + count are plain text, so colour is never the only
 * carrier, and the finding list below remains the complete detail view.
 */
const SEVERITIES: { severity: ReviewFinding["severity"]; dot: string }[] = [
  { severity: "critical", dot: "bg-[var(--risk-critical)]" },
  { severity: "high", dot: "bg-[var(--risk-high)]" },
  { severity: "medium", dot: "bg-[var(--risk-warning)]" },
  { severity: "low", dot: "bg-[var(--muted-foreground)]" },
  { severity: "info", dot: "bg-[var(--muted-foreground)]/55" },
];

export function SeverityDistribution({ findings }: { findings: ReviewFinding[] }) {
  const { t } = useTranslation();
  const rows = SEVERITIES.map((entry) => ({
    ...entry,
    label: t(`precheck.severity.${entry.severity}`),
    count: findings.filter((finding) => finding.severity === entry.severity).length,
  }));

  return (
    <ul className="flex flex-wrap items-center gap-x-5 gap-y-2 text-sm" data-testid="severity-counts">
      {rows.map((row) => (
        <li key={row.severity} className="flex items-center gap-2">
          <span className={cn("size-2 shrink-0 rounded-full", row.dot)} aria-hidden="true" />
          <span className="text-muted-foreground">{row.label}</span>
          <span
            className="font-semibold tabular-nums"
            data-testid={`severity-count-${row.severity}`}
          >
            {row.count}
          </span>
        </li>
      ))}
    </ul>
  );
}
