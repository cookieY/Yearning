import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { ChangeDraft } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { useDraftEditorStore, selectDirty } from "@/features/review/draft-editor-store";
import {
  isoToRequestedFor,
  requestedForToIso,
} from "@/features/review/submission-dialogs";
import {
  saveDraftSqlRequest,
  useCreateChangeDraftMutation,
  useRevealDraftSqlMutation,
  useSaveDraftSqlMutation,
  useSubmitChangeDraftMutation,
  useUpdateChangeDraftMutation,
} from "@/features/review/use-draft-workspace";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import {
  sameDraftMetadata,
  type DraftMetadataSnapshot,
} from "./submission-wizard-summary-card";

export interface UseSubmissionDraftActionsArgs {
  mode: "create" | "draft";
  metaBound: boolean;
  activeDraftId: string | null;
  draft: ChangeDraft | null;
  selectedFlowId: string | null;
  setLocalDraftId: (id: string | null) => void;
  createTitle: string;
  createDescription: string;
  createRequestedFor: string;
  createRollbackEnabled: boolean;
  metaTitle: string;
  metaDescription: string;
  metaRequestedFor: string;
  metaRollbackEnabled: boolean;
  setMetaTitle: (val: string) => void;
  setMetaDescription: (val: string) => void;
  setMetaRequestedFor: (val: string) => void;
  setMetaRollbackEnabled: (val: boolean) => void;
  setTargetDatabase: (val: string) => void;
  targetDatabaseForRequest: string | null | undefined;
  localSqlText: string;
  createSqlRef: RefObject<string>;
  metadataEditRevisionRef: RefObject<number>;
  visibleMetadataRef: RefObject<DraftMetadataSnapshot>;
  onAdoptedSessionIds: (draftId: string) => void;
  setLoadValue: (val: { text: string; nonce: number } | null) => void;
  setStepError: (val: string | null) => void;
  lastPrecheckRunId: string | null;
  setPrecheckModalOpen: (open: boolean) => void;
  cancelReviewStartPending: () => void;
}

export function useSubmissionDraftActions(args: UseSubmissionDraftActionsArgs) {
  const {
    mode, metaBound, activeDraftId, draft, selectedFlowId, setLocalDraftId,
    createTitle, createDescription, createRequestedFor, createRollbackEnabled,
    metaTitle, metaDescription, metaRequestedFor, metaRollbackEnabled,
    setMetaTitle, setMetaDescription, setMetaRequestedFor, setMetaRollbackEnabled,
    setTargetDatabase, targetDatabaseForRequest, localSqlText, createSqlRef,
    metadataEditRevisionRef, visibleMetadataRef, onAdoptedSessionIds,
    setLoadValue, setStepError, lastPrecheckRunId, setPrecheckModalOpen, cancelReviewStartPending,
  } = args;
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const store = useDraftEditorStore();

  const adoptedDraftIdRef = useRef<string | null>(null);
  // Whether both halves of adoption (POST shell creation + SQL PUT) have completed: an adoption whose PUT failed
  // is an "incomplete action", so subsequent acting operations finish it first (completing what the user already initiated,
  // not saving new edits behind their back — owner ruling 2026-10-31).
  const adoptionSqlCompleteRef = useRef(false);
  const [adoptionPending, setAdoptionPending] = useState(false);
  const ensureDraftPromiseRef = useRef<Promise<string | null> | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [confirmSubmit, setConfirmSubmit] = useState(false);

  const showActionError = useCallback((error: unknown, operationId: string) => {
    const msg = describeErrorText(describeError(error, operationId), { detailSeparator: " " });
    setActionError(msg);
    appToast.error(msg);
  }, []);

  const saveMutation = useSaveDraftSqlMutation({
    onSuccess: (savedDraft, { sql }) => {
      store.markSaved(savedDraft.version, savedDraft.state, sql);
      setActionError(null);
      appToast.success(t("common.savedToast"));
    },
    onError: (error) => { showActionError(error, "replaceDraftSql"); },
  });

  const editorIsCurrent = useCallback((expectedSql: string): boolean => {
    const current = useDraftEditorStore.getState();
    return current.draftId !== null
      && current.sql === expectedSql
      && !selectDirty(current);
  }, []);

  const savedSqlMatchesEditor = useCallback((savedSql: string): boolean => {
    const current = useDraftEditorStore.getState();
    return current.savedSql === savedSql && editorIsCurrent(savedSql);
  }, [editorIsCurrent]);

  const metadataIsCurrent = useCallback(
    (snapshot: DraftMetadataSnapshot, revision: number): boolean =>
      metadataEditRevisionRef.current === revision
      && sameDraftMetadata(visibleMetadataRef.current, snapshot),
    [metadataEditRevisionRef, visibleMetadataRef],
  );

  const createMutation = useCreateChangeDraftMutation({
    onError: (error) => { showActionError(error, "createChangeDraft"); },
  });

  /**
   * Implicit-draft adoption (owner ruling 2026-10-24): create the server
   * submission envelope — silently, in place — on the first acting
   * operation. The flow + title gate stays (the envelope cannot exist
   * without them) but it surfaces as a near-field error at the moment of
   * the action, never as a mandatory user step. Local SQL travels with the
   * adoption so the editor never blanks.
   */
  const ensureDraft = useCallback(async (): Promise<string | null> => {
    if (ensureDraftPromiseRef.current !== null) return ensureDraftPromiseRef.current;
    const needsAdoption = activeDraftId === null && adoptedDraftIdRef.current === null;
    if (needsAdoption) {
      // Keep validation synchronous: rejection must set the error and clear pending in the same synchronous
      // batch as the click (validation errors must not appear as "in progress"), and must never leave a settled promise
      // that would cause the next click to be swallowed by deduplication.
      const rejectDraft = (message: string): null => {
        setStepError(message);
        cancelReviewStartPending();
        return null;
      };
      const requestedIssue = (value: string): string | null => {
        if (value === "") return null;
        if (requestedForToIso(value) === null) return t("precheck.wizard.requestedScheduleInvalid");
        if (new Date(value).getTime() <= Date.now()) return t("precheck.wizard.requestedSchedulePast");
        return null;
      };
      if (selectedFlowId === null) {
        return rejectDraft(t("precheck.wizard.error.needFlow"));
      }
      if (createTitle.trim() === "") {
        return rejectDraft(t("precheck.wizard.error.needTitle"));
      }
      const issue = requestedIssue(createRequestedFor);
      if (issue !== null) {
        return rejectDraft(issue);
      }
    }
    const completion = (async (): Promise<string | null> => {
      // Complete partial adoption: when the SQL PUT of a prior adoption did not land (after POST authoritatively created the shell),
      // finish it first so the current acting operation faces saved SQL consistent with the editor; if the retry still
      // fails, abort the current operation without creating a second draft.
      const adoptedDraftId = adoptedDraftIdRef.current;
      if (adoptedDraftId !== null && !adoptionSqlCompleteRef.current) {
        const current = useDraftEditorStore.getState();
        if (current.sql !== "" && current.savedVersion !== null) {
          try {
            const saved = await saveDraftSqlRequest(adoptedDraftId, current.sql, current.savedVersion);
            store.markSaved(saved.version, saved.state, current.sql);
            void queryClient.invalidateQueries({ queryKey: ["change-draft", adoptedDraftId] });
          } catch (error) {
            showActionError(error, "replaceDraftSql");
            return null;
          }
        }
        adoptionSqlCompleteRef.current = true;
      }
      if (activeDraftId !== null) return activeDraftId;
      if (adoptedDraftId !== null) return adoptedDraftId;
      setStepError(null);
      setAdoptionPending(true);
      const metadataAtCreate = visibleMetadataRef.current;
      let created: ChangeDraft;
      try {
        created = await createMutation.mutateAsync({
          flow_id: selectedFlowId as string,
          title: metadataAtCreate.title,
          description: metadataAtCreate.description,
          requested_scheduled_for: metadataAtCreate.requested_scheduled_for ?? undefined,
          rollback_enabled: metadataAtCreate.rollback_enabled,
          target_database: metadataAtCreate.target_database ?? undefined,
        });
      } catch (error) {
        showActionError(error, "createChangeDraft");
        return null;
      }

      // The POST is now authoritative: keep this page attached to the created
      // row even if the following SQL PUT fails. Retrying the first action must
      // update this draft, never create an orphan replacement.
      const sqlAtCreate = createSqlRef.current;
      adoptedDraftIdRef.current = created.id;
      onAdoptedSessionIds(created.id);
      const metadataAtAdoption = visibleMetadataRef.current;
      setMetaTitle(metadataAtAdoption.title);
      setMetaDescription(metadataAtAdoption.description ?? "");
      setMetaRequestedFor(isoToRequestedFor(metadataAtAdoption.requested_scheduled_for));
      setMetaRollbackEnabled(metadataAtAdoption.rollback_enabled);
      setTargetDatabase(metadataAtAdoption.target_database ?? "");
      store.attach({
        id: created.id,
        state: created.state,
        version: created.version,
        sql: null,
      });
      if (sqlAtCreate !== "") store.setSql(sqlAtCreate);
      setLocalDraftId(created.id);
      setLoadValue({ text: sqlAtCreate, nonce: Date.now() });

      if (sqlAtCreate !== "") {
        try {
          const saved = await saveDraftSqlRequest(created.id, sqlAtCreate, created.version);
          store.markSaved(saved.version, saved.state, sqlAtCreate);
          void queryClient.invalidateQueries({ queryKey: ["change-draft", created.id] });
        } catch (error) {
          showActionError(error, "replaceDraftSql");
          return null;
        }
      }
      adoptionSqlCompleteRef.current = true;

      // The create or SQL PUT can span user typing. markSaved acknowledges only
      // the submitted text, so any later input remains visible and dirty.
      const sqlAfterSave = createSqlRef.current;
      if (sqlAfterSave !== sqlAtCreate) store.setSql(sqlAfterSave);
      return created.id;
    })();
    ensureDraftPromiseRef.current = completion;
    try {
      return await completion;
    } finally {
      if (ensureDraftPromiseRef.current === completion) {
        ensureDraftPromiseRef.current = null;
        setAdoptionPending(false);
      }
    }
  }, [
    activeDraftId,
    cancelReviewStartPending,
    createMutation,
    createRequestedFor,
    createSqlRef,
    createTitle,
    onAdoptedSessionIds,
    queryClient,
    selectedFlowId,
    setLoadValue,
    setLocalDraftId,
    setMetaDescription,
    setMetaRequestedFor,
    setMetaRollbackEnabled,
    setMetaTitle,
    setStepError,
    setTargetDatabase,
    showActionError,
    store,
    t,
    visibleMetadataRef,
  ]);

  const revealMutation = useRevealDraftSqlMutation(activeDraftId, {
    onSuccess: (payload) => {
      if (draft === null) return;
      store.attach({
        id: draft.id,
        state: draft.state,
        version: draft.version,
        sql: payload.sql,
      });
      setLoadValue({ text: payload.sql, nonce: Date.now() });
      setActionError(null);
    },
    onError: (error) => { showActionError(error, "revealDraftSql"); },
  });

  // Auto-load the saved SQL on draft entry (owner ruling 2026-11-01: the
  // read-SQL threshold was over-design — display it directly; precheck must
  // always have the editor text, bulk drafts included).
  const autoRevealedRef = useRef<string | null>(null);
  useEffect(() => {
    if (mode !== "draft" || draft === null) return;
    if (!draft.has_sql) return;
    const storeState = useDraftEditorStore.getState();
    if (storeState.draftId !== draft.id || storeState.savedSql !== null) return;
    if (autoRevealedRef.current === draft.id || revealMutation.isPending) return;
    autoRevealedRef.current = draft.id;
    revealMutation.mutate();
  }, [mode, draft, revealMutation]);

  const metaSaveMutation = useUpdateChangeDraftMutation({
    onSuccess: () => {
      setActionError(null);
    },
    onError: (error) => { showActionError(error, "updateChangeDraft"); },
  });

  const submitMutation = useSubmitChangeDraftMutation({
    onSuccess: (created) => {
      store.markServerState("submitted");
      setSubmitError(null);
      setConfirmSubmit(false);
      appToast.success(t("common.createdToast"), {
        description: created.display_number,
      });
      // F6 flow: submission lands on the immutable order detail (the order
      // id is returned by the submission response itself).
      void navigate(`/changes/orders/${created.id}`);
    },
    onError: (error) => {
      const msg = describeErrorText(describeError(error, "submitChangeDraft"), { detailSeparator: " " });
      setSubmitError(msg);
      appToast.error(msg);
    },
  });

  const handleConfirmSubmit = (): void => {
    submitMutation.mutate({
      body: {
        flow_id: (draft?.flow_id ?? selectedFlowId) as string,
        draft_id: activeDraftId ?? undefined,
        sql: localSqlText,
        run_id: lastPrecheckRunId as string,
        target_database: targetDatabaseForRequest,
        metadata: mode === "create"
          ? {
              title: (metaBound ? metaTitle : createTitle).trim(),
              description: (metaBound ? metaDescription : createDescription) === ""
                ? undefined
                : (metaBound ? metaDescription : createDescription),
              requested_scheduled_for:
                requestedForToIso(metaBound ? metaRequestedFor : createRequestedFor) ?? undefined,
              rollback_enabled: metaBound ? metaRollbackEnabled : createRollbackEnabled,
            }
          : undefined,
      },
      beforeSubmit:
        metaBound && activeDraftId !== null
          ? async () => {
              await metaSaveMutation.mutateAsync({
                draftId: activeDraftId,
                body: visibleMetadataRef.current,
              });
            }
          : undefined,
    });
  };

  // ---- Save and Return: the single secondary action (owner ruling 2026-10-24,
  // Save Info folded away) — persist SQL then metadata, then leave. ----
  const saveDraftAndLeave = (): void => {
    const leave = () => {
      appToast.success(t("common.savedToast"));
      void navigate("/changes/mine");
    };
    const metadataSnapshot = visibleMetadataRef.current;
    const metadataRevision = metadataEditRevisionRef.current;
    const metadataUnchanged = () => metadataIsCurrent(metadataSnapshot, metadataRevision);
    if (mode === "create" && (activeDraftId === null || !adoptionSqlCompleteRef.current)) {
      void (async () => {
        const adoptedId = await ensureDraft();
        if (adoptedId === null) return;
        const current = useDraftEditorStore.getState();
        if (!selectDirty(current)) {
          if (metadataUnchanged()) leave();
          return;
        }
        saveMutation.mutate({ draftId: adoptedId, sql: current.sql }, {
          onSuccess: (_saved, variables) => {
            if (savedSqlMatchesEditor(variables.sql) && metadataUnchanged()) leave();
          },
        });
      })();
      return;
    }
    if (metaBound && metaTitle.trim() === "") {
      setStepError(t("precheck.wizard.error.needTitle"));
      return;
    }
    const current = useDraftEditorStore.getState();
    if (selectDirty(current)) {
      saveMutation.mutate({ draftId: activeDraftId as string, sql: current.sql }, {
        onSuccess: (_saved, variables) => {
          if (!savedSqlMatchesEditor(variables.sql)) return;
          if (metaBound) {
            metaSaveMutation.mutate({ draftId: activeDraftId as string, body: metadataSnapshot }, {
              onSuccess: () => {
                if (editorIsCurrent(variables.sql) && metadataUnchanged()) leave();
              },
            });
          } else {
            leave();
          }
        },
      });
      return;
    }
    if (metaBound) {
      metaSaveMutation.mutate({ draftId: activeDraftId as string, body: metadataSnapshot }, {
        onSuccess: () => {
          if (editorIsCurrent(current.sql) && metadataUnchanged()) leave();
        },
      });
    } else {
      leave();
    }
  };

  const handleAttemptSubmit = (): void => {
    if (mode === "create") {
      if (selectedFlowId === null && draft?.flow_id == null) {
        setStepError(t("precheck.wizard.error.needFlow"));
        return;
      }
      if ((metaBound ? metaTitle : createTitle).trim() === "") {
        setStepError(t("precheck.wizard.error.needTitle"));
        return;
      }
      const requestedFor = metaBound ? metaRequestedFor : createRequestedFor;
      if (requestedFor !== "") {
        if (requestedForToIso(requestedFor) === null) {
          setStepError(t("precheck.wizard.requestedScheduleInvalid"));
          return;
        }
        if (new Date(requestedFor).getTime() <= Date.now()) {
          setStepError(t("precheck.wizard.requestedSchedulePast"));
          return;
        }
      }
    }
    setStepError(null);
    setSubmitError(null);
    setPrecheckModalOpen(false);
    setConfirmSubmit(true);
  };

  return {
    adoptionPending,
    actionError,
    submitError,
    confirmSubmit,
    setConfirmSubmit,
    saveMutationPending: saveMutation.isPending,
    createMutationPending: createMutation.isPending,
    metaSaveMutationPending: metaSaveMutation.isPending,
    submitMutationPending: submitMutation.isPending,
    submitMutationSuccess: submitMutation.isSuccess,
    saveDraftAndLeave,
    handleConfirmSubmit,
    handleAttemptSubmit,
  };
}
