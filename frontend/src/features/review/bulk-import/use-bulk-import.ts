import { useCallback, useEffect, useRef, useState } from "react";
import {
  FINGERPRINT_MAX_INPUT_BYTES,
  FINGERPRINT_MAX_STATEMENTS,
  FINGERPRINT_MAX_STATEMENT_BYTES,
} from "@/features/review/bulk-constants";
import {
  digestSqlFile,
  type DigestProgress,
  type SqlDigest,
} from "@/features/review/bulk-import/sql-digest";

/**
 * Client-side pre-validation state machine for bulk SQL import (frontend PRD
 * F5 item 7: warn and validate 32 MiB file cap and 512 KiB single-statement cap before upload;
 * server result remains the final judgement). Reading is incremental and cancellable; the hook produces
 * the digest plus the single in-memory SQL copy the page uploads.
 */

export type ImportBlockKey =
  | "fileTooLarge"
  | "statementTooLarge"
  | "tooManyStatements";

export interface ImportBlock {
  readonly key: ImportBlockKey;
  /** Offending ordinals (first few) and the total count where applicable. */
  readonly samples?: readonly number[];
  readonly count?: number;
  readonly limit: number;
  readonly actual: number;
}

export type ImportPhase =
  | { kind: "idle" }
  | { kind: "reading"; bytes: number; totalBytes: number }
  | { kind: "ready"; digest: SqlDigest }
  | { kind: "blocked"; digest: SqlDigest | null; blocks: readonly ImportBlock[] }
  | { kind: "cancelled" }
  | { kind: "error"; message: string };

export interface BulkImportState {
  readonly phase: ImportPhase;
  readonly fileName: string | null;
  start: (file: File) => Promise<void>;
  cancel: () => void;
  reset: () => void;
  /** Returns the assembled SQL text of the last successful digest once. */
  takeText: () => string | null;
}

export function useBulkImport(): BulkImportState {
  const [phase, setPhase] = useState<ImportPhase>({ kind: "idle" });
  const [fileName, setFileName] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const runRef = useRef(0);
  const textRef = useRef<string | null>(null);

  useEffect(() => () => { abortRef.current?.abort(); }, []);

  const cancel = useCallback(() => {
    abortRef.current?.abort();
  }, []);

  const reset = useCallback(() => {
    runRef.current += 1;
    abortRef.current?.abort();
    abortRef.current = null;
    textRef.current = null;
    setPhase({ kind: "idle" });
    setFileName(null);
  }, []);

  const takeText = useCallback((): string | null => {
    const text = textRef.current;
    textRef.current = null;
    return text;
  }, []);

  const start = useCallback(async (file: File) => {
    runRef.current += 1;
    const run = runRef.current;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    textRef.current = null;
    setFileName(file.name);

    if (file.size > FINGERPRINT_MAX_INPUT_BYTES) {
      setPhase({
        kind: "blocked",
        digest: null,
        blocks: [{
          key: "fileTooLarge",
          limit: FINGERPRINT_MAX_INPUT_BYTES,
          actual: file.size,
        }],
      });
      return;
    }

    const onProgress = (progress: DigestProgress): void => {
      if (runRef.current !== run) return;
      setPhase({ kind: "reading", bytes: progress.bytes, totalBytes: progress.totalBytes });
    };

    try {
      const { digest, text } = await digestSqlFile(file, {
        signal: controller.signal,
        onProgress,
      });
      if (runRef.current !== run) return;

      const blocks: ImportBlock[] = [];
      if (digest.statementCount > FINGERPRINT_MAX_STATEMENTS) {
        blocks.push({
          key: "tooManyStatements",
          limit: FINGERPRINT_MAX_STATEMENTS,
          actual: digest.statementCount,
        });
      }
      const oversized = digest.statements
        .filter((statement) => statement.oversized)
        .map((statement) => statement.index);
      if (oversized.length > 0) {
        blocks.push({
          key: "statementTooLarge",
          samples: oversized.slice(0, 5),
          count: oversized.length,
          limit: FINGERPRINT_MAX_STATEMENT_BYTES,
          actual: digest.maxStatementBytes,
        });
      }
      if (blocks.length > 0) {
        setPhase({ kind: "blocked", digest, blocks });
        return;
      }
      setPhase({ kind: "ready", digest });
      textRef.current = text;
    } catch (error) {
      if (runRef.current !== run) return;
      if (error instanceof DOMException && error.name === "AbortError") {
        setPhase({ kind: "cancelled" });
        return;
      }
      setPhase({
        kind: "error",
        message: error instanceof Error ? error.message : String(error),
      });
    }
  }, []);

  return { phase, fileName, start, cancel, reset, takeText };
}
