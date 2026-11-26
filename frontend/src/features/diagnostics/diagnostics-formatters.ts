export type TopMetricKey = "total_exec_time_ms" | "avg_exec_time_ms" | "calls";

export function formatReadableSqlLabel(options: {
  templatePreview?: string | null;
  databaseOrSchema?: string | null;
  engineFingerprint?: string | null;
  fallbackIndex?: number;
  maxLength?: number;
}): string {
  const maxLen = options.maxLength ?? 36;
  const rawSql = (options.templatePreview ?? "")
    .replace(/[`"]/g, "")
    .replace(/\s+/g, " ")
    .trim();

  if (rawSql !== "") {
    if (rawSql.length <= maxLen) {
      return rawSql;
    }
    const selectMatch =
      /^(SELECT)\s+.+?\s+FROM\s+([a-zA-Z0-9_$.]+)(?:\s+(WHERE\s+.+|\bJOIN\s+[a-zA-Z0-9_$.]+|\bGROUP\s+BY\s+.+|\bORDER\s+BY\s+.+))?/i.exec(
        rawSql,
      );
    if (selectMatch?.[1] && selectMatch[2]) {
      const verb = selectMatch[1].toUpperCase();
      const table = selectMatch[2];
      const clause = selectMatch[3]?.trim() ?? "";
      const compact = clause ? `${verb} ${table} · ${clause}` : `${verb} ${table}`;
      return compact.length > maxLen
        ? `${compact.slice(0, Math.max(12, maxLen - 1))}…`
        : compact;
    }
    return `${rawSql.slice(0, Math.max(12, maxLen - 1))}…`;
  }

  const schema = (options.databaseOrSchema ?? "").trim();
  const idxLabel =
    options.fallbackIndex !== undefined ? `#${String(options.fallbackIndex + 1)}` : "";
  if (schema !== "") {
    return idxLabel ? `${schema} · SQL ${idxLabel}` : `${schema} · SQL`;
  }
  return idxLabel ? `SQL ${idxLabel}` : "SQL Statement";
}

export function formatAxisSqlLabel(options: {
  templatePreview?: string | null;
  databaseOrSchema?: string | null;
  index: number;
  maxLength?: number;
}): string {
  const maxLen = options.maxLength ?? 22;
  const rankPrefix = `#${String(options.index + 1)} `;
  const rawSql = (options.templatePreview ?? "")
    .replace(/[`"]/g, "")
    .replace(/\s+/g, " ")
    .trim();

  if (rawSql !== "") {
    const statementMatch =
      /^(?:(SELECT)\s+.+?\s+FROM|(INSERT)(?:\s+IGNORE)?\s+INTO|(UPDATE)|(DELETE)\s+FROM|(CREATE)\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?|(ALTER)\s+TABLE|(DROP)\s+TABLE(?:\s+IF\s+EXISTS)?|(SET)|(SHOW))\s+([a-zA-Z0-9_$.]+)/i.exec(
        rawSql,
      );
    const verb = statementMatch?.slice(1, 10).find(Boolean)?.toUpperCase();
    const target = statementMatch?.[10];
    const summary = verb && target ? `${verb} ${target}` : rawSql.toUpperCase();
    const full = `${rankPrefix}${summary}`;
    return full.length > maxLen ? `${full.slice(0, maxLen - 1)}…` : full;
  }

  const schema = (options.databaseOrSchema ?? "").trim();
  const fallback = schema !== "" ? `${rankPrefix}${schema}` : `${rankPrefix}SQL`;
  return fallback.length > maxLen ? `${fallback.slice(0, maxLen - 1)}…` : fallback;
}

export function formatShortTime(iso: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toISOString().slice(11, 16);
}

export function heatmapToneClass(ratio: number, unobserved: boolean): string {
  if (unobserved) return "bg-muted/40 text-muted-foreground";
  if (ratio >= 0.75) return "bg-primary/80 text-primary-foreground";
  if (ratio >= 0.45) return "bg-primary/50 text-foreground";
  if (ratio >= 0.15) return "bg-primary/25 text-foreground";
  if (ratio > 0) return "bg-primary/10 text-foreground";
  return "bg-muted/30 text-muted-foreground";
}
