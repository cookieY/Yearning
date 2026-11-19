import { create } from "zustand";
import type { DraftState } from "@/api/generated/client/yearningV4HTTPAPI.schemas";

/**
 * Pure client-side editor state for one draft workspace session (frontend
 * PRD §4: Zustand for draft editing and cross-component client state; server
 * state stays in the Query layer). The SQL plaintext lives in memory only —
 * never in web storage, URLs or logs (migration contract §6). The store
 * tracks the delta between the editor text and the last saved revision so
 * the workspace can void a prior review result the instant the SQL changes
 * (PRD F4: mark outdated locally as soon as SQL changes) instead of waiting for the PUT.
 */

interface DraftEditorState {
  draftId: string | null;
  /** Mirror of the server-side draft state from the latest query/mutation. */
  serverState: DraftState | null;
  /** Last draft row version acknowledged by a mutation or the draft query.
   * This is the If-Match target for every draft mutation (sql replace,
   * metadata update, review run, submission) — §18.74: the backend compares
   * the row `version`, never the SQL content `revision` (those diverge once
   * a review run completes, which bumps version only). */
  savedVersion: number | null;
  /** Editor text as of that version (memory only). */
  savedSql: string | null;
  /** Current editor text (memory only). */
  sql: string;
  attach: (draft: { id: string; state: DraftState; version: number; sql?: string | null }) => void;
  setSql: (sql: string) => void;
  markSaved: (version: number, state: DraftState, sql: string) => void;
  markServerState: (state: DraftState) => void;
  /** Monotonic version sync from query refreshes: a completed review run
   * bumps the row version server-side (§18.74), so any refetch carrying a
   * higher version must lift the store — never regress it (in-flight PUT
   * responses remain authoritative via markSaved). */
  markVersioned: (version: number, state: DraftState) => void;
  reset: () => void;
}

const initial = {
  draftId: null,
  serverState: null,
  savedVersion: null,
  savedSql: null,
  sql: "",
} as const;

export const useDraftEditorStore = create<DraftEditorState>((set) => ({
  ...initial,
  attach: ({ id, state, version, sql }) => { set({
      draftId: id,
      serverState: state,
      savedVersion: version,
      savedSql: typeof sql === "string" ? sql : null,
      sql: typeof sql === "string" ? sql : "",
    }); },
  setSql: (sql) => { set({ sql }); },
  /** Acknowledge the revision that was saved without replacing what the user
   * typed after the request started. Equal text becomes clean; later edits
   * remain dirty and can be saved by the next explicit action. */
  markSaved: (version, state, sql) => { set((current) =>
    current.savedVersion !== null && version < current.savedVersion
      ? current
      : { savedVersion: version, serverState: state, savedSql: sql }); },
  markServerState: (state) => { set({ serverState: state }); },
  markVersioned: (version, state) => { set((current) =>
    current.savedVersion !== null && version <= current.savedVersion
      ? current
      : { savedVersion: version, serverState: state }); },
  reset: () => { set({ ...initial }); },
}));

export function selectDirty(state: DraftEditorState): boolean {
  if (state.draftId === null) return false;
  // An attached draft whose saved SQL is unknown (never revealed in this
  // session) counts as clean until the user edits — there is no delta to
  // prove otherwise.
  if (state.savedSql === null) return state.sql !== "";
  return state.sql !== state.savedSql;
}
