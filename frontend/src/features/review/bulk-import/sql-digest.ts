import {
  FINGERPRINT_MAX_STATEMENT_BYTES,
} from "@/features/review/bulk-constants";

/**
 * Streaming client-side SQL digest (frontend PRD F5 deliverable: incremental file reading and
 * digest). The scanner is a pure character-level state machine fed decoded
 * chunks: it splits statements at semicolons that sit outside strings, quoted
 * identifiers and comments, and derives per-statement metadata without ever
 * retaining statement text. Memory discipline (acceptance gate: never duplicate SQL copies):
 * the only SQL-sized allocation is the caller's own source text; the digest
 * keeps bounded metadata (shape keys are capped, no literals, no statement
 * bodies) and previews are sliced lazily by the browser for visible rows
 * only.
 *
 * This is a navigation aid, NOT the backend fingerprint: grouping uses a
 * local shape key, while the authoritative fingerprint groups are the ones
 * the backend reports (run.fingerprint_group_count, findings with
 * fingerprint_group_id). The server result remains the final judgement.
 */

export type StatementKind =
  | "SELECT"
  | "INSERT"
  | "UPDATE"
  | "DELETE"
  | "ALTER"
  | "CREATE"
  | "DROP"
  | "TRUNCATE"
  | "REPLACE"
  | "RENAME"
  | "OTHER";

export interface DigestStatementMeta {
  /** 1-based ordinal. */
  readonly index: number;
  /** Byte offsets — used against size limits. */
  readonly startByte: number;
  readonly bytes: number;
  /** UTF-16 code-unit offsets — used for lazy preview slicing. */
  readonly startChar: number;
  readonly chars: number;
  readonly kind: StatementKind;
  readonly table: string | null;
  readonly hasWhere: boolean;
  /** True when the statement exceeded the single-statement byte limit. */
  readonly oversized: boolean;
  /** True when a terminating `;` was found (final statement may not have one). */
  readonly terminated: boolean;
  /** Local shape-group ordinal (navigation grouping, not the backend HMAC). */
  readonly group: number;
  readonly shapeKey: string;
  /** No-WHERE DML or oversized — surfaced separately so aggregation cannot hide it. */
  readonly anomaly: boolean;
}

export interface LocalFingerprintGroup {
  readonly ordinal: number;
  readonly shapeKey: string;
  readonly kind: StatementKind;
  readonly table: string | null;
  readonly count: number;
  readonly firstIndex: number;
  readonly lastIndex: number;
  readonly anomalyCount: number;
}

export interface SqlDigest {
  readonly sizeBytes: number;
  readonly statementCount: number;
  readonly statements: readonly DigestStatementMeta[];
  readonly groupCount: number;
  readonly groups: readonly LocalFingerprintGroup[];
  /** Distinct target tables (capped for display; tableCount is the true count). */
  readonly tables: readonly string[];
  readonly tableCount: number;
  /** Terminated statements / statement count — 1 means every statement ended cleanly. */
  readonly coverageRatio: number;
  readonly maxStatementBytes: number;
  readonly anomalyCount: number;
  /** Last statement has no terminator — the server may fail closed on this. */
  readonly truncated: boolean;
  /** Decoder emitted U+FFFD — the source is not valid UTF-8. */
  readonly decodeError: boolean;
}

export const SHAPE_KEY_MAX_CHARS = 240;
const TABLES_DISPLAY_CAP = 200;
const RAW_HEAD_MAX_CHARS = 200;
const QUOTE_BUFFER_MAX_CHARS = 128;

const KIND_KEYWORDS: ReadonlySet<string> = new Set([
  "SELECT",
  "INSERT",
  "UPDATE",
  "DELETE",
  "ALTER",
  "CREATE",
  "DROP",
  "TRUNCATE",
  "REPLACE",
  "RENAME",
]);

const TABLE_PATTERNS: readonly RegExp[] = [
  /(?:INSERT|REPLACE)\s+(?:IGNORE\s+)?INTO\s+([^\s(,]+)/i,
  /UPDATE\s+([^\s(,]+)/i,
  /DELETE\s+FROM\s+([^\s(,]+)/i,
  /ALTER\s+TABLE\s+([^\s(,]+)/i,
  /CREATE\s+(?:TEMPORARY\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([^\s(,]+)/i,
  /DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([^\s(,]+)/i,
  /TRUNCATE(?:\s+TABLE)?\s+([^\s(,]+)/i,
];

/** Normalizes a captured table reference: strips quoting and trailing punctuation. */
function cleanTable(raw: string): string {
  return raw.replace(/[`"']/g, "").replace(/[.,;:]+$/, "").trim();
}

function extractTable(rawHead: string): string | null {
  for (const pattern of TABLE_PATTERNS) {
    const match = pattern.exec(rawHead);
    if (match !== null) {
      const table = cleanTable(match[1] ?? "");
      if (table !== "") return table;
    }
  }
  return null;
}

/** UTF-8 byte length of one code point (surrogate pairs resolved by caller). */
function utf8Bytes(code: number): number {
  if (code < 0x80) return 1;
  if (code < 0x800) return 2;
  if (code >= 0xd800 && code <= 0xdfff) return 4;
  if (code < 0x10000) return 3;
  return 4;
}

interface PendingStatement {
  index: number;
  startByte: number;
  startChar: number;
  bytes: number;
  chars: number;
  shapeKey: string;
  shapeLength: number;
  rawHead: string;
  prevShapeChar: string;
  prevRawChar: string;
  /** True while a kept identifier digit run is in progress. */
  digitRunKept: boolean;
  /** Current uppercase letter run for exact keyword detection (unbounded scan). */
  keywordRun: string;
  /** True once the exact keyword WHERE has been observed at a token boundary. */
  whereSeen: boolean;
}

interface GroupAccumulator {
  ordinal: number;
  shapeKey: string;
  kind: StatementKind;
  table: string | null;
  count: number;
  firstIndex: number;
  lastIndex: number;
  anomalyCount: number;
}

export class SqlDigestScanner {
  private readonly statements: DigestStatementMeta[] = [];
  private readonly shapeGroups = new Map<string, GroupAccumulator>();
  private byteCursor = 0;
  private charCursor = 0;
  private decodeError = false;
  /** Quote/comment context inside the current statement. */
  private quote: "'" | '"' | "`" | null = null;
  private lineComment = false;
  private blockComment = false;
  private escapeNext = false;
  private prevChar = "";
  private current: PendingStatement | null = null;
  private quoteBuffer = "";

  /** Feeds one decoded text fragment; call finish() after the last chunk. */
  push(text: string): void {
    for (let i = 0; i < text.length; i += 1) {
      let code = text.charCodeAt(i);
      // Swallow the low surrogate of a pair; bytes/chars are charged once.
      if (code >= 0xdc00 && code <= 0xdfff) continue;
      let char = text.charAt(i);
      if (code >= 0xd800 && code <= 0xdbff && i + 1 < text.length) {
        const low = text.charCodeAt(i + 1);
        if (low >= 0xdc00 && low <= 0xdfff) {
          char += text.charAt(i + 1);
          code = 0x10000 + ((code - 0xd800) << 10) + (low - 0xdc00);
        }
      }
      if (code === 0xfffd) this.decodeError = true;
      this.consume(char);
      this.byteCursor += utf8Bytes(code);
      this.charCursor += char.length;
    }
  }

  /** Finalizes the digest; the scanner must not be reused afterwards. */
  finish(): SqlDigest {
    if (this.current !== null) this.closeStatement(false);
    const statements = this.statements;
    let maxStatementBytes = 0;
    let anomalyCount = 0;
    let terminatedCount = 0;
    const tables = new Set<string>();
    for (const statement of statements) {
      if (statement.bytes > maxStatementBytes) maxStatementBytes = statement.bytes;
      if (statement.anomaly) anomalyCount += 1;
      if (statement.terminated) terminatedCount += 1;
      if (statement.table !== null) tables.add(statement.table);
    }
    const sortedTables = [...tables].sort((a, b) => a.localeCompare(b));
    return {
      sizeBytes: this.byteCursor,
      statementCount: statements.length,
      statements,
      groupCount: this.shapeGroups.size,
      groups: [...this.shapeGroups.values()].sort((a, b) => a.ordinal - b.ordinal),
      tables: sortedTables.slice(0, TABLES_DISPLAY_CAP),
      tableCount: sortedTables.length,
      coverageRatio: statements.length === 0 ? 1 : terminatedCount / statements.length,
      maxStatementBytes,
      anomalyCount,
      truncated:
        statements.length > 0 &&
        (statements[statements.length - 1]?.terminated === false),
      decodeError: this.decodeError,
    };
  }

  private consume(char: string): void {
    const charge = (): void => {
      if (this.current === null) return;
      this.current.bytes += utf8Bytes(char.codePointAt(0) ?? 0);
      this.current.chars += char.length;
    };

    if (this.lineComment) {
      charge();
      if (char === "\n" || char === "\r") this.lineComment = false;
      return;
    }
    if (this.blockComment) {
      charge();
      if (char === "/" && this.prevChar === "*") {
        this.blockComment = false;
        this.prevChar = "";
      } else {
        this.prevChar = char;
      }
      return;
    }
    if (this.quote !== null) {
      charge();
      if (this.escapeNext) {
        this.escapeNext = false;
        this.appendQuoteBuffer(char);
        return;
      }
      if (char === this.quote) {
        this.flushQuoteBuffer();
        this.quote = null;
        this.prevChar = "";
        if (this.current !== null) this.endKeywordRun(this.current);
        return;
      }
      if (this.quote === "'" && char === "\\") {
        this.escapeNext = true;
        return;
      }
      this.appendQuoteBuffer(char);
      return;
    }

    if (char === "'" || char === '"' || char === "`") {
      this.ensureStatement();
      charge();
      this.endKeywordRun(this.current);
      this.quote = char;
      this.quoteBuffer = "";
      this.appendShape("?");
      if (this.current !== null) this.current.prevShapeChar = "?";
      return;
    }
    if (char === "-" && this.prevChar === "-") {
      this.enterLineComment();
      return;
    }
    if (char === "#") {
      this.enterLineComment();
      return;
    }
    if (char === "*" && this.prevChar === "/") {
      this.enterBlockComment();
      return;
    }
    this.prevChar = char;

    if (char === ";") {
      if (this.current !== null) this.closeStatement(true);
      return;
    }

    if (this.current === null) {
      if (char.trim() === "") return;
      this.ensureStatement();
    }
    const statement = this.current;
    if (statement === null) return;
    statement.bytes += utf8Bytes(char.codePointAt(0) ?? 0);
    statement.chars += char.length;

    if (statement.rawHead.length < RAW_HEAD_MAX_CHARS) {
      statement.rawHead += char;
    }

    if (/\s/.test(char)) {
      if (statement.prevShapeChar !== "" && statement.prevShapeChar !== " ") {
        this.appendShape(" ");
        statement.prevShapeChar = " ";
      }
      this.endKeywordRun(statement);
      statement.prevRawChar = char;
      statement.digitRunKept = false;
      return;
    }
    if (/[0-9]/.test(char)) {
      this.endKeywordRun(statement);
      // A digit run only survives verbatim when it STARTED as part of an
      // identifier (table_2 ≠ table_3); value numbers collapse into one
      // placeholder regardless of length (id = 123 → ?, not ?23).
      const startsIdentifierRun = /[A-Za-z_$]/.test(statement.prevRawChar);
      const keep = startsIdentifierRun || (/[0-9]/.test(statement.prevRawChar) && statement.digitRunKept);
      statement.digitRunKept = keep;
      if (keep) {
        this.appendShape(char);
        statement.prevShapeChar = char;
      } else if (statement.prevShapeChar !== "?") {
        this.appendShape("?");
        statement.prevShapeChar = "?";
      }
      statement.prevRawChar = char;
      return;
    }
    statement.digitRunKept = false;
    const upper = char.toUpperCase();
    if (/[A-Z_]/.test(upper)) {
      // Unbounded keyword detection: the shape key truncates at 240 chars,
      // but a WHERE beyond the truncation still blocks no-WHERE DML. Exact
      // token match — the run ends at every non-identifier character (see
      // endKeywordRun call below and the quote branches), so identifiers
      // like SOMEWHERE or WHEREX never match.
      statement.keywordRun = (statement.keywordRun + upper).slice(-16);
    } else {
      this.endKeywordRun(statement);
    }
    this.appendShape(upper);
    statement.prevShapeChar = upper;
    statement.prevRawChar = char;
  }

  private endKeywordRun(statement: PendingStatement | null): void {
    if (statement === null) return;
    if (statement.keywordRun === "WHERE") statement.whereSeen = true;
    statement.keywordRun = "";
  }

  /**
   * Comment starters are consumed as normal characters before their second
   * character reveals the comment, so a pending statement may hold a trailing
   * "-", "/" or "/*" fragment. Trim that fragment and drop the statement when
   * it has no other content — comments are not structural.
   */
  private trimTrailingCommentStarters(statement: PendingStatement): void {
    const trimmedShape = statement.shapeKey.replace(/[-/*#]+\s*$/, "");
    const trimmedHead = statement.rawHead.replace(/[-/*#]+\s*$/, "");
    if (trimmedShape.length !== statement.shapeKey.length) {
      statement.shapeKey = trimmedShape;
      statement.rawHead = trimmedHead;
      statement.prevShapeChar = trimmedShape.endsWith(" ") ? " " : (trimmedShape.slice(-1) || "");
    }
  }

  private enterLineComment(): void {
    if (this.current !== null) {
      this.trimTrailingCommentStarters(this.current);
      if (this.current.shapeKey === "") this.current = null;
    }
    this.lineComment = true;
    this.prevChar = "";
  }

  private enterBlockComment(): void {
    if (this.current !== null) {
      this.trimTrailingCommentStarters(this.current);
      if (this.current.shapeKey === "") this.current = null;
    }
    this.blockComment = true;
    this.prevChar = "";
  }

  /** Opens a statement at the first significant character. */
  private ensureStatement(): void {
    if (this.current !== null) return;
    this.current = {
      index: this.statements.length + 1,
      startByte: this.byteCursor,
      startChar: this.charCursor,
      bytes: 0,
      chars: 0,
      shapeKey: "",
      shapeLength: 0,
      rawHead: "",
      prevShapeChar: "",
      prevRawChar: "",
      digitRunKept: false,
      keywordRun: "",
      whereSeen: false,
    };
  }

  private appendQuoteBuffer(char: string): void {
    if (this.quoteBuffer.length < QUOTE_BUFFER_MAX_CHARS) {
      this.quoteBuffer += char;
    }
  }

  /** Quoted identifier content feeds the raw head with its quotes so capture
   * patterns can span separator characters inside the name; literal content
   * becomes a placeholder. */
  private flushQuoteBuffer(): void {
    if (this.current === null) return;
    if (this.quote === "`" || this.quote === '"') {
      if (this.current.rawHead.length < RAW_HEAD_MAX_CHARS) {
        this.current.rawHead += this.quote + this.quoteBuffer + this.quote;
      }
    } else if (this.current.rawHead.length < RAW_HEAD_MAX_CHARS) {
      this.current.rawHead += "?";
    }
    this.quoteBuffer = "";
  }

  private appendShape(token: string): void {
    if (this.current === null) return;
    if (this.current.shapeLength >= SHAPE_KEY_MAX_CHARS) return;
    this.current.shapeKey += token;
    this.current.shapeLength += token.length;
  }

  private closeStatement(terminated: boolean): void {
    const statement = this.current;
    this.current = null;
    this.quote = null;
    this.lineComment = false;
    this.blockComment = false;
    this.escapeNext = false;
    this.prevChar = "";
    if (statement === null) return;
    if (statement.keywordRun === "WHERE") statement.whereSeen = true;
    const table = extractTable(statement.rawHead);
    const hasWhere = statement.whereSeen;
    const firstToken = /^[A-Z]+/.exec(statement.shapeKey)?.[0] ?? "";
    const kind = KIND_KEYWORDS.has(firstToken) ? (firstToken as StatementKind) : "OTHER";
    const oversized = statement.bytes > FINGERPRINT_MAX_STATEMENT_BYTES;
    const noWhereDml = (kind === "UPDATE" || kind === "DELETE") && !hasWhere;
    const anomaly = oversized || noWhereDml;
    const existing = this.shapeGroups.get(statement.shapeKey);
    let groupOrdinal: number;
    if (existing !== undefined) {
      groupOrdinal = existing.ordinal;
      existing.count += 1;
      existing.lastIndex = statement.index;
      if (anomaly) existing.anomalyCount += 1;
    } else {
      groupOrdinal = this.shapeGroups.size;
      this.shapeGroups.set(statement.shapeKey, {
        ordinal: groupOrdinal,
        shapeKey: statement.shapeKey,
        kind,
        table,
        count: 1,
        firstIndex: statement.index,
        lastIndex: statement.index,
        anomalyCount: anomaly ? 1 : 0,
      });
    }
    const meta: DigestStatementMeta = {
      index: statement.index,
      startByte: statement.startByte,
      bytes: statement.bytes,
      startChar: statement.startChar,
      chars: statement.chars,
      kind,
      table,
      hasWhere,
      oversized,
      terminated,
      group: groupOrdinal,
      shapeKey: statement.shapeKey,
      anomaly,
    };
    this.statements.push(meta);
  }
}

/** Digests a complete in-memory SQL text in one pass. */
export function digestSqlText(sql: string): SqlDigest {
  const scanner = new SqlDigestScanner();
  scanner.push(sql);
  return scanner.finish();
}

export interface DigestProgress {
  readonly bytes: number;
  readonly totalBytes: number;
}

export interface DigestFileResult {
  readonly digest: SqlDigest;
  /** The decoded full text — the single persistent SQL copy for upload. */
  readonly text: string;
}

/**
 * Incrementally reads a File in byte chunks (frontend PRD F5: incremental reading), feeds
 * the scanner, reports progress and honours cancellation. Chunks are decoded
 * with a streaming TextDecoder so multi-byte characters crossing chunk
 * boundaries stay intact; the accumulated text is joined exactly once at the
 * end so the source never lives in memory twice.
 */
export async function digestSqlFile(
  file: File,
  options: {
    signal?: AbortSignal;
    chunkBytes?: number;
    onProgress?: (progress: DigestProgress) => void;
  } = {},
): Promise<DigestFileResult> {
  const chunkBytes = options.chunkBytes ?? 1024 * 1024;
  const decoder = new TextDecoder("utf-8", { fatal: false });
  const scanner = new SqlDigestScanner();
  const parts: string[] = [];
  for (let offset = 0; offset < file.size; offset += chunkBytes) {
    if (options.signal?.aborted) throw new DOMException("aborted", "AbortError");
    const chunk = await file.slice(offset, offset + chunkBytes).arrayBuffer();
    if (options.signal?.aborted) throw new DOMException("aborted", "AbortError");
    const text = decoder.decode(chunk, { stream: true });
    parts.push(text);
    scanner.push(text);
    options.onProgress?.({
      bytes: Math.min(offset + chunkBytes, file.size),
      totalBytes: file.size,
    });
    // Yield to the event loop between chunks so the page stays interactive
    // while a large file is being read (exit criterion: main thread is never blocked for long periods).
    await new Promise<void>((resolve) => { setTimeout(resolve, 0); });
  }
  const tail = decoder.decode();
  if (tail !== "") {
    parts.push(tail);
    scanner.push(tail);
  }
  return { digest: scanner.finish(), text: parts.join("") };
}
