/**
 * Pure SQL inspection helpers for the Query Workspace:
 * - statement splitting outside strings, quoted identifiers, and comments
 * - extracting the primary target schema/table from SELECT statements
 * - resolving the effective schema_name when SQL explicitly qualifies schema.table
 * - formatting deduplicated result tab labels ("schema.table", "schema.table 2", ...)
 */

export interface QueryStatementTarget {
  schema: string | null;
  table: string | null;
}

/**
 * Splits a SQL batch into individual top-level statements on semicolons that
 * sit outside string literals ('...', "..."), quoted identifiers (`...`), and
 * comments (-- ..., # ..., /* ... *\/). Trailing whitespace/comment-only
 * segments after the final semicolon are dropped.
 */
export function splitSqlStatements(sql: string): string[] {
  const statements: string[] = [];
  let start = 0;
  let i = 0;
  const len = sql.length;

  const pushIfMeaningful = (raw: string) => {
    const stripped = raw
      .replace(/--[^\n]*/g, "")
      .replace(/#[^\n]*/g, "")
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .trim();
    if (stripped !== "") {
      statements.push(raw.trim());
    }
  };

  while (i < len) {
    const ch = sql[i];
    const next = sql[i + 1];
    if (ch === "-" && next === "-") {
      i += 2;
      while (i < len && sql[i] !== "\n") i += 1;
      continue;
    }
    if (ch === "#") {
      i += 1;
      while (i < len && sql[i] !== "\n") i += 1;
      continue;
    }
    if (ch === "/" && next === "*") {
      i += 2;
      while (i < len && !(sql[i] === "*" && sql[i + 1] === "/")) i += 1;
      i += 2;
      continue;
    }
    if (ch === "'" || ch === '"' || ch === "`") {
      const quote = ch;
      i += 1;
      while (i < len) {
        if (sql[i] === "\\" && quote !== "`") {
          i += 2;
          continue;
        }
        if (sql[i] === quote) {
          if (sql[i + 1] === quote) {
            i += 2;
            continue;
          }
          i += 1;
          break;
        }
        i += 1;
      }
      continue;
    }
    if (ch === ";") {
      pushIfMeaningful(sql.slice(start, i));
      start = i + 1;
    }
    i += 1;
  }
  pushIfMeaningful(sql.slice(start));
  return statements;
}

interface SqlToken {
  kind: "word" | "quoted" | "symbol";
  value: string;
  depth: number;
}

function tokenizeSqlForTarget(sql: string): SqlToken[] {
  const tokens: SqlToken[] = [];
  let i = 0;
  let depth = 0;
  const len = sql.length;

  while (i < len) {
    const ch = sql[i];
    const next = sql[i + 1];

    if (ch === undefined || /\s/.test(ch)) {
      i += 1;
      continue;
    }
    // Line comments (-- ... or # ...)
    if ((ch === "-" && next === "-") || ch === "#") {
      i += ch === "#" ? 1 : 2;
      while (i < len && sql[i] !== "\n") i += 1;
      continue;
    }
    // Block comments (/* ... */)
    if (ch === "/" && next === "*") {
      i += 2;
      while (i < len && !(sql[i] === "*" && sql[i + 1] === "/")) i += 1;
      i += 2;
      continue;
    }
    // Single-quoted string literals are skipped so keywords inside strings never match.
    if (ch === "'") {
      i += 1;
      while (i < len) {
        if (sql[i] === "\\") {
          i += 2;
          continue;
        }
        if (sql[i] === "'") {
          if (sql[i + 1] === "'") {
            i += 2;
            continue;
          }
          i += 1;
          break;
        }
        i += 1;
      }
      continue;
    }
    // Quoted identifiers (`...` or "...")
    if (ch === "`" || ch === '"') {
      const quote = ch;
      i += 1;
      let ident = "";
      while (i < len) {
        if (sql[i] === quote) {
          if (sql[i + 1] === quote) {
            ident += quote;
            i += 2;
            continue;
          }
          i += 1;
          break;
        }
        ident += sql[i] ?? "";
        i += 1;
      }
      if (ident !== "") {
        tokens.push({ kind: "quoted", value: ident, depth });
      }
      continue;
    }
    if (ch === "(") {
      tokens.push({ kind: "symbol", value: "(", depth });
      depth += 1;
      i += 1;
      continue;
    }
    if (ch === ")") {
      depth = Math.max(0, depth - 1);
      tokens.push({ kind: "symbol", value: ")", depth });
      i += 1;
      continue;
    }
    if (ch === "." || ch === "," || ch === ";") {
      tokens.push({ kind: "symbol", value: ch, depth });
      i += 1;
      continue;
    }
    if (/[A-Za-z0-9_$]/.test(ch)) {
      const start = i;
      i += 1;
      while (i < len && /[A-Za-z0-9_$]/.test(sql[i] ?? "")) {
        i += 1;
      }
      tokens.push({ kind: "word", value: sql.slice(start, i), depth });
      continue;
    }
    i += 1;
  }
  return tokens;
}

function readTargetAfterFrom(tokens: SqlToken[], fromIndex: number): QueryStatementTarget | null {
  const first = tokens[fromIndex + 1];
  if (first === undefined || first.kind === "symbol") {
    return null;
  }
  // Skip optional PostgreSQL ONLY keyword: FROM ONLY schema.table
  let idx = fromIndex + 1;
  if (first.kind === "word" && first.value.toLowerCase() === "only") {
    idx += 1;
  }
  const ident1 = tokens[idx];
  if (ident1 === undefined || ident1.kind === "symbol") {
    return null;
  }
  const dot = tokens[idx + 1];
  const ident2 = tokens[idx + 2];
  if (dot !== undefined && dot.kind === "symbol" && dot.value === "." && ident2 !== undefined && ident2.kind !== "symbol") {
    return { schema: ident1.value, table: ident2.value };
  }
  return { schema: null, table: ident1.value };
}

/**
 * Extracts the primary target schema and table from a SELECT statement.
 * Prefers the first top-level (paren depth 0) FROM table reference so CTEs
 * and scalar subqueries in SELECT lists resolve to the outer queried table;
 * falls back to nested FROM clauses for derived-table queries.
 */
export function extractQueryTarget(sql: string): QueryStatementTarget {
  const tokens = tokenizeSqlForTarget(sql);
  let fallback: QueryStatementTarget | null = null;

  for (let i = 0; i < tokens.length; i += 1) {
    const tok = tokens[i];
    if (tok === undefined || tok.kind !== "word" || tok.value.toLowerCase() !== "from") {
      continue;
    }
    const target = readTargetAfterFrom(tokens, i);
    if (target === null) continue;
    if (tok.depth === 0) {
      return target;
    }
    if (fallback === null || (fallback.schema === null && target.schema !== null)) {
      fallback = target;
    }
  }
  return fallback ?? { schema: null, table: null };
}

/**
 * Resolves the effective schema_name for executing a statement: uses the
 * explicitly selected schema from the toolbar dropdown when present, or falls
 * back to the explicit `schema.table` qualifier embedded in the SQL statement.
 */
export function resolveStatementSchema(sql: string, selectedSchema: string): string {
  const trimmed = selectedSchema.trim();
  if (trimmed !== "") {
    return trimmed;
  }
  return extractQueryTarget(sql).schema ?? "";
}

/**
 * Builds display labels for query result tabs based on each statement's target
 * `schema.table`. Duplicate targets receive sequential numeric suffixes:
 * e.g. ["demo.accounts", "demo.accounts 2", "demo.accounts 3"].
 */
export function buildResultTabLabels(
  items: ReadonlyArray<{ sql: string; schemaName: string }>,
  fallbackLabel: (index: number) => string,
): string[] {
  const counts = new Map<string, number>();
  return items.map((item, index) => {
    const target = extractQueryTarget(item.sql);
    let base: string;
    if (target.schema !== null && target.table !== null) {
      base = `${target.schema}.${target.table}`;
    } else if (target.table !== null && item.schemaName.trim() !== "") {
      base = `${item.schemaName.trim()}.${target.table}`;
    } else if (target.table !== null) {
      base = target.table;
    } else {
      base = fallbackLabel(index);
    }

    const key = base.toLowerCase();
    const seen = (counts.get(key) ?? 0) + 1;
    counts.set(key, seen);
    return seen === 1 ? base : `${base} ${String(seen)}`;
  });
}

export interface GrantSummaryRef {
  id: string;
  state: string;
  expires_at?: string | null;
  created_at: string;
}

/** Picks the workspace's active grant, preferring the session's bound grant ID then latest expiry. */
export function pickWorkspaceActiveGrant<T extends GrantSummaryRef>(
  grants: readonly T[],
  sessionGrantId: string | null | undefined,
): T | null {
  const active = grants.filter((grant) => grant.state === "active");
  if (active.length === 0) return null;
  if (sessionGrantId !== null && sessionGrantId !== undefined) {
    const bound = active.find((grant) => grant.id === sessionGrantId);
    if (bound !== undefined) return bound;
  }
  return active.reduce((latest, current) => {
    const currentExp = current.expires_at ?? "";
    const latestExp = latest.expires_at ?? "";
    if (currentExp !== latestExp) {
      return currentExp > latestExp ? current : latest;
    }
    return current.created_at > latest.created_at ? current : latest;
  });
}

export interface GrantDominanceRef extends GrantSummaryRef {
  requester_user_id: string;
}

export interface RequestDominanceRef {
  grant_id?: string | null;
  flow_id?: string;
  datasource_ids: readonly string[];
}

/** Returns true when `primary` supersedes `candidate` (same requester & flow, superset datasources, later expiry). */
export function grantDominatesCandidate(
  primary: GrantDominanceRef,
  candidate: GrantDominanceRef,
  requestByGrantId: Map<string, RequestDominanceRef>,
): boolean {
  if (primary.id === candidate.id || primary.requester_user_id !== candidate.requester_user_id) {
    return false;
  }
  const primaryReq = requestByGrantId.get(primary.id);
  const candidateReq = requestByGrantId.get(candidate.id);
  if (
    primaryReq === undefined ||
    candidateReq === undefined ||
    primaryReq.flow_id === undefined ||
    primaryReq.flow_id === "" ||
    primaryReq.flow_id !== candidateReq.flow_id ||
    primaryReq.datasource_ids.length === 0 ||
    candidateReq.datasource_ids.length === 0
  ) {
    return false;
  }
  const primaryDS = new Set(primaryReq.datasource_ids);
  if (!candidateReq.datasource_ids.every((id) => primaryDS.has(id))) {
    return false;
  }
  const pExp = primary.expires_at ?? "";
  const cExp = candidate.expires_at ?? "";
  if (pExp !== cExp) return pExp > cExp;
  if (primary.created_at !== candidate.created_at) return primary.created_at > candidate.created_at;
  return primary.id > candidate.id;
}

export interface FlowMatchRef {
  id: string;
  query_capabilities?: readonly { datasource_id: string }[];
}

export interface RequestMatchRef extends RequestDominanceRef {
  state: string;
  created_at: string;
}

export function requestMatchesQueryFlow(
  flow: FlowMatchRef,
  request: RequestDominanceRef,
  capabilityIds: Set<string>,
): boolean {
  if (request.flow_id !== undefined && request.flow_id !== "") {
    return request.flow_id === flow.id;
  }
  return request.datasource_ids.length > 0 && request.datasource_ids.every((id) => capabilityIds.has(id));
}

/** Matches a flow's primary active grant and any superseded active grants in the same renewal chain. */
export function matchGrantForQueryFlow<
  TGrant extends GrantDominanceRef & { state: string },
  TReq extends RequestMatchRef,
>(
  flow: FlowMatchRef,
  requests: readonly TReq[],
  grants: readonly TGrant[],
): { grant: TGrant; request: TReq; superseded: TGrant[] } | null {
  const grantById = new Map(grants.map((grant) => [grant.id, grant]));
  const capabilityIds = new Set((flow.query_capabilities ?? []).map((cap) => cap.datasource_id));
  const candidates: { grant: TGrant; request: TReq }[] = [];
  for (const request of requests) {
    if (request.state !== "grant_active" || request.grant_id === null || request.grant_id === undefined) {
      continue;
    }
    const grant = grantById.get(request.grant_id);
    if (grant === undefined || grant.state !== "active") continue;
    if (requestMatchesQueryFlow(flow, request, capabilityIds)) candidates.push({ grant, request });
  }
  if (candidates.length === 0) return null;
  const requestByGrantId = new Map<string, RequestDominanceRef>(
    candidates.map((cand) => [cand.grant.id, cand.request]),
  );
  const primary = candidates.reduce((latest, current) => {
    const currentExp = current.grant.expires_at ?? "";
    const latestExp = latest.grant.expires_at ?? "";
    if (currentExp !== latestExp) {
      return currentExp > latestExp ? current : latest;
    }
    return current.grant.created_at > latest.grant.created_at ? current : latest;
  });
  const superseded = candidates
    .filter((cand) => grantDominatesCandidate(primary.grant, cand.grant, requestByGrantId))
    .map((cand) => cand.grant);
  return { grant: primary.grant, request: primary.request, superseded };
}
