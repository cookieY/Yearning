import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Ban, CalendarClock, ChevronRight, Clock, Database, FilePlus2 } from "lucide-react";
import type { Flow, QueryAccessRequest, QueryGrant } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import {
  businessErrCodeByName,
  describeError,
  describeErrorText,
} from "@/shared/api/error-display";
import { BusinessError } from "@/shared/api/mutator";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { Checkbox } from "@/shared/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/components/ui/dialog";
import { DatePicker } from "@/shared/components/ui/date-picker";
import { Label } from "@/shared/components/ui/label";
import { Textarea } from "@/shared/components/ui/textarea";
import {
  useCreateAccessRequest,
  useCreateSession,
  useMyAccessRequests,
  useMyGrants,
  useMyQueryFlows,
  useMySessions,
  useRelinquishGrant,
} from "@/features/query/use-query-domain";

/**
 * Query Entry (route /query; migration contract §2, UI spec §7.5).
 *
 * Flow cards are the primary entry. Whether entering needs a grant is
 * SERVER-driven: the grant-less attempt succeeds when query approval is
 * disabled (Q002 direct path) and answers QUERY_GRANT_REQUIRED otherwise,
 * which the page converts into the Request Access guidance — the frontend never
 * reads the admin-only query settings namespace to guess the mode.
 *
 * Grant matching: the request view carries no flow id (the backend keeps
 * flow_id_snapshot internal), so an active grant is attached to a flow by
 * construction — its approved datasource set is a subset of the flow's
 * capabilities. The subset rule is derived purely from server facts and is
 * recorded as a presentation boundary in the migration contract §17.
 */

import { formatTimestamp } from "@/shared/lib/timezones";

function formatExpiry(iso: string | null | undefined): string {
  return formatTimestamp(iso);
}

interface FlowGrantMatch {
  grant: QueryGrant;
  request: QueryAccessRequest;
}

function matchGrantForFlow(flow: Flow, requests: QueryAccessRequest[], grants: QueryGrant[]): FlowGrantMatch | null {
  const grantById = new Map(grants.map((grant) => [grant.id, grant]));
  const capabilityIds = new Set((flow.query_capabilities ?? []).map((capability) => capability.datasource_id));
  const candidates: FlowGrantMatch[] = [];
  for (const request of requests) {
    if (request.state !== "grant_active" || request.grant_id === null || request.grant_id === undefined) continue;
    const grant = grantById.get(request.grant_id);
    if (grant === undefined || grant.state !== "active") continue;
    const subset = request.datasource_ids.every((id) => capabilityIds.has(id));
    if (subset) candidates.push({ grant, request });
  }
  if (candidates.length === 0) return null;
  return candidates.reduce((latest, current) =>
    current.grant.created_at > latest.grant.created_at ? current : latest,
  );
}

function matchPendingRequestForFlow(flow: Flow, requests: QueryAccessRequest[]): QueryAccessRequest | null {
  const capabilityIds = new Set((flow.query_capabilities ?? []).map((capability) => capability.datasource_id));
  const candidates: QueryAccessRequest[] = [];
  for (const request of requests) {
    if (request.state !== "access_pending") continue;
    const subset = request.datasource_ids.length > 0 && request.datasource_ids.every((id) => capabilityIds.has(id));
    if (subset) candidates.push(request);
  }
  if (candidates.length === 0) return null;
  return candidates.reduce((latest, current) =>
    current.created_at > latest.created_at ? current : latest,
  );
}

/** UX-006: today in local timezone (YYYY-MM-DD), used by the date picker to disable past dates. */
function todayLocal(): string {
  const now = new Date();
  return `${String(now.getFullYear())}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
}

export default function QueryEntryPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const session = useSession();
  const authenticated = session.status === "authenticated";
  const flowsQuery = useMyQueryFlows(authenticated);
  const requestsQuery = useMyAccessRequests(authenticated);
  const grantsQuery = useMyGrants(authenticated);
  const sessionsQuery = useMySessions(authenticated);
  const createSession = useCreateSession();

  const [requestFlow, setRequestFlow] = useState<Flow | null>(null);
  const [terminateTarget, setTerminateTarget] = useState<{ flow: Flow; grant: QueryGrant } | null>(null);
  const [entryError, setEntryError] = useState<string | null>(null);
  const [needsAccessHint, setNeedsAccessHint] = useState<string | null>(null);
  // §18.49 (owner ruling): the apply affordance is server-driven — it only
  // appears for flows the server answered QUERY_GRANT_REQUIRED for. With
  // query approval disabled (Q002 direct path) it never shows.
  const [needsAccessFlows, setNeedsAccessFlows] = useState<Set<string>>(new Set());
  const [enteringFlowId, setEnteringFlowId] = useState<string | null>(null);

  const flows = flowsQuery.data ?? [];
  const currentUserId = session.user?.id;
  const requests = useMemo(
    () =>
      currentUserId === undefined
        ? requestsQuery.data.items
        : requestsQuery.data.items.filter((request) => request.requester_user_id === currentUserId),
    [currentUserId, requestsQuery.data.items],
  );
  const sessions = (sessionsQuery.data.items).filter((row) => row.state === "active");
  const matches = useMemo(
    () => new Map(
      (flowsQuery.data ?? []).map((flow) => [
        flow.id,
        matchGrantForFlow(flow, requests, grantsQuery.data ?? []),
      ]),
    ),
    [flowsQuery.data, grantsQuery.data, requests],
  );
  const pendingByFlow = useMemo(
    () => new Map(
      (flowsQuery.data ?? []).map((flow) => [
        flow.id,
        matchPendingRequestForFlow(flow, requests),
      ]),
    ),
    [flowsQuery.data, requests],
  );

  const enter = async (flow: Flow, match: FlowGrantMatch | null): Promise<void> => {
    setEntryError(null);
    setNeedsAccessHint(null);
    setEnteringFlowId(flow.id);
    try {
      // owner ruling 2026-10-31: the same datasource within the query validity window = the same session — leaving
      // and re-entering the page reuses the active session instead of opening a new one (so the "Active Sessions" list
      // no longer accumulates duplicates). Explicit "Exit Query" is the audited terminal action; a new session can only be
      // created after the current one ends. Matching rule: same eligibility (same grant for approval flows; null grant for direct entry)
      // + same datasource set.
      const wanted =
        match === null
          ? (flow.query_capabilities ?? []).map((capability) => capability.datasource_id)
          : [...match.request.datasource_ids];
      const signature = (ids: string[]) => [...ids].sort().join(",");
      const expectedGrant = match === null ? null : match.grant.id;
      const existing = sessions.find(
        (row) =>
          (expectedGrant === null ? row.grant_id == null : row.grant_id === expectedGrant) &&
          signature(row.capabilities.map((capability) => capability.datasource_id)) ===
            signature(wanted),
      );
      if (existing !== undefined) {
        void navigate(`/query/sessions/${existing.id}`);
        return;
      }
      const created = await createSession.mutateAsync({
        flow_id: flow.id,
        grant_id: match === null ? null : match.grant.id,
        datasource_ids:
          match === null
            ? (flow.query_capabilities ?? []).map((capability) => capability.datasource_id)
            : [...match.request.datasource_ids],
      });
      void navigate(`/query/sessions/${created.id}`);
    } catch (error) {
      if (
        error instanceof BusinessError &&
        error.err_code === businessErrCodeByName("QUERY_GRANT_REQUIRED")
      ) {
        setNeedsAccessHint(flow.name);
        setNeedsAccessFlows((current) => new Set(current).add(flow.id));
        return;
      }
      setEntryError(describeErrorText(describeError(error, "createQuerySession")));
    } finally {
      setEnteringFlowId(null);
    }
  };

  const anyLoading = flowsQuery.isPending || requestsQuery.isPending || grantsQuery.isPending;
  const loadError = flowsQuery.error ?? requestsQuery.error ?? grantsQuery.error;

  return (
    <PageLayout data-testid="query-entry">
      <PageHeader
        icon={Database}
        title={t("query.entry.title")}
        description={t("query.entry.description")}
      />
      {entryError !== null && (
        <Alert variant="destructive" data-testid="query-entry-error">
          <AlertTitle>{t("query.entry.failed")}</AlertTitle>
          <AlertDescription>{entryError}</AlertDescription>
        </Alert>
      )}
      {needsAccessHint !== null && (
        <Alert data-testid="query-needs-access-hint">
          <AlertTitle>{t("query.entry.needsAccessTitle")}</AlertTitle>
          <AlertDescription>{t("query.entry.needsAccessDescription", { name: needsAccessHint })}</AlertDescription>
        </Alert>
      )}
      {requests.some((request) => request.state === "access_pending") && (
        <Alert data-testid="query-pending-hint">
          <AlertTitle>{t("query.entry.pendingTitle")}</AlertTitle>
          <AlertDescription>{t("query.entry.pendingDescription")}</AlertDescription>
        </Alert>
      )}

      {loadError !== null && (
        <ErrorState
          error={loadError}
          operationId="listCurrentUserFlows"
          onRetry={() => {
            void flowsQuery.refetch();
            void requestsQuery.refetch();
            void grantsQuery.refetch();
          }}
        />
      )}
      {loadError === null && anyLoading && <LoadingState variant="page" />}

      {loadError === null && !anyLoading && (
        <>
          {flows.length === 0 && sessions.length === 0 && (
            <div className="rounded-xl border">
              <EmptyState
                title={t("query.entry.emptyTitle")}
                description={t("query.entry.emptyDescription")}
                testId="query-entry-empty"
              />
            </div>
          )}

          {flows.length > 0 && (
            <Card>
              <CardHeader className="pb-3">
                <CardTitle>{t("query.entry.flowsCard")}</CardTitle>
                <CardDescription>{t("query.entry.flowsDescription")}</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-2">
                {flows.map((flow) => {
                  const match = matches.get(flow.id) ?? null;
                  const pending = pendingByFlow.get(flow.id) ?? null;
                  const requiresAccess =
                    needsAccessFlows.has(flow.id) ||
                    (match === null &&
                      flow.flow_type === "query_access" &&
                      (flow.approval_steps?.length ?? 0) > 0);
                  return (
                    <div
                      key={flow.id}
                      className="flex flex-row items-center justify-between gap-3 rounded-lg border px-3 py-2"
                      data-testid={`query-flow-${flow.id}`}
                    >
                      <div className="flex min-w-0 flex-col gap-1">
                        <span className="truncate text-sm font-medium">{flow.name}</span>
                        <span className="text-muted-foreground flex flex-wrap items-center gap-1 text-xs">
                          <Database className="size-3" />
                          {(flow.query_capabilities ?? []).length}
                          {t("query.entry.datasourceCount")}
                          {(flow.query_capabilities ?? []).some((capability) => capability.can_export) && (
                            <Badge variant="secondary">{t("query.entry.exportable")}</Badge>
                          )}
                        </span>
                        {match !== null && (
                          <span className="flex items-center gap-1 text-xs" data-testid={`query-flow-expiry-${flow.id}`}>
                            <CalendarClock className="size-3" />
                            {t("query.entry.grantExpires", { time: formatExpiry(match.grant.expires_at) })}
                          </span>
                        )}
                      </div>
                      <div className="flex shrink-0 flex-row gap-2">
                        {/* owner ruling 2026-10-31 round 6: hide "Enter Query" when the user has no eligibility.
                            Query flows with approval steps require approval — show Enter Query only when an active grant matches,
                            otherwise show only Request Access; flows without approval steps remain direct-entry
                            paths (Q002). §18.49 server-driven handling remains as a fallback (grant expiring mid-flight
                            -> 4001 -> needsAccessFlows). */}
                        {match === null && pending !== null ? (
                          <Button
                            size="sm"
                            variant="outline"
                            disabled
                            data-testid={`query-flow-pending-${flow.id}`}
                          >
                            <Clock />
                            {t("query.request.state.access_pending")}
                          </Button>
                        ) : requiresAccess ? (
                          <Button size="sm" variant="outline" onClick={() => { setRequestFlow(flow); }} data-testid={`query-flow-apply-${flow.id}`}>
                            <FilePlus2 />
                            {t("query.entry.applyAccess")}
                          </Button>
                        ) : (
                          <>
                            {match !== null && (
                              <Button
                                size="sm"
                                variant="destructive"
                                onClick={() => { setTerminateTarget({ flow, grant: match.grant }); }}
                                data-testid={`query-flow-terminate-${flow.id}`}
                              >
                                <Ban />
                                {t("query.entry.terminate")}
                              </Button>
                            )}
                            <PendingButton
                              size="sm"
                              pending={createSession.isPending && enteringFlowId === flow.id}
                              disabled={createSession.isPending && enteringFlowId !== flow.id}
                              onClick={() => { void enter(flow, match); }}
                              data-testid={`query-flow-enter-${flow.id}`}
                            >
                              {t("query.entry.enter")}
                              <ChevronRight />
                            </PendingButton>
                          </>
                        )}
                      </div>
                    </div>
                  );
                })}
              </CardContent>
            </Card>
          )}

          {sessions.length > 0 && (
            <Card>
              <CardHeader className="pb-3">
                <CardTitle>{t("query.entry.sessionsCard")}</CardTitle>
                <CardDescription>{t("query.entry.sessionsDescription")}</CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-2">
                {sessions.map((row) => (
                  <button
                    key={row.id}
                    type="button"
                    onClick={() => { void navigate(`/query/sessions/${row.id}`); }}
                    className="flex flex-row items-center justify-between gap-3 rounded-lg border px-3 py-2 text-left hover:bg-accent"
                    data-testid={`query-session-${row.id}`}
                  >
                    <span className="text-muted-foreground truncate font-mono text-xs">{row.id}</span>
                    <span className="flex shrink-0 items-center gap-2">
                      <Badge variant="secondary">{t(`query.session.state.${row.state}`)}</Badge>
                      <span className="text-muted-foreground text-xs">{formatExpiry(row.created_at)}</span>
                      <ChevronRight className="size-4" />
                    </span>
                  </button>
                ))}
              </CardContent>
            </Card>
          )}
        </>
      )}

      <AccessRequestDialog flow={requestFlow} onClose={() => { setRequestFlow(null); }} />
      <TerminateGrantDialog target={terminateTarget} onClose={() => { setTerminateTarget(null); }} />
    </PageLayout>
  );
}

function TerminateGrantDialog({
  target,
  onClose,
}: {
  target: { flow: Flow; grant: QueryGrant } | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const relinquish = useRelinquishGrant();
  const [reason, setReason] = useState("");
  const [errorText, setErrorText] = useState<string | null>(null);

  const close = () => {
    setReason("");
    setErrorText(null);
    onClose();
  };

  const submit = async () => {
    if (target === null) return;
    setErrorText(null);
    try {
      await relinquish.mutateAsync({
        grantId: target.grant.id,
        version: target.grant.version,
        reason: reason.trim() === "" ? t("query.entry.terminateDefaultReason") : reason.trim(),
      });
      toast.success(t("query.entry.terminateSubmitted"));
      close();
    } catch (error) {
      setErrorText(describeErrorText(describeError(error, "relinquishQueryGrant")));
    }
  };

  return (
    <Dialog open={target !== null} onOpenChange={(open) => { if (!open) close(); }}>
      <DialogContent data-testid="query-terminate-dialog">
        <DialogHeader>
          <DialogTitle>{t("query.entry.terminateTitle", { name: target?.flow.name ?? "" })}</DialogTitle>
          <DialogDescription>{t("query.entry.terminateDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-2">
          <Label htmlFor="query-terminate-reason">{t("query.entry.terminateReason")}</Label>
          <Textarea
            id="query-terminate-reason"
            value={reason}
            onChange={(event) => { setReason(event.target.value); }}
            maxLength={4096}
            data-testid="query-terminate-reason"
          />
          {errorText !== null && (
            <Alert variant="destructive" data-testid="query-terminate-error">
              <AlertTitle>{t("query.entry.terminateFailed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={close}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            variant="destructive"
            pending={relinquish.isPending}
            onClick={() => void submit()}
            data-testid="query-terminate-confirm"
          >
            {t("query.entry.terminateConfirm")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function AccessRequestDialog({ flow, onClose }: { flow: Flow | null; onClose: () => void }) {
  const { t } = useTranslation();
  const createRequest = useCreateAccessRequest();
  const [reason, setReason] = useState("");
  const [until, setUntil] = useState("");
  const [untilTime, setUntilTime] = useState("23:59");
  const [selected, setSelected] = useState<string[]>([]);
  const [errorText, setErrorText] = useState<string | null>(null);

  const capabilities = flow?.query_capabilities ?? [];
  const valid = reason.trim() !== "" && until !== "" && selected.length > 0;

  const toggle = (datasourceId: string) => {
    setSelected((current) =>
      current.includes(datasourceId)
        ? current.filter((id) => id !== datasourceId)
        : [...current, datasourceId],
    );
  };

  const close = () => {
    setReason("");
    setUntil("");
    setUntilTime("23:59");
    setSelected([]);
    setErrorText(null);
    onClose();
  };

  const submit = async () => {
    if (flow === null) return;
    setErrorText(null);
    try {
      await createRequest.mutateAsync({
        flow_id: flow.id,
        datasource_ids: selected,
        // OBS-009 ruling (RCP): send wall-clock time (Z marker) at minute granularity, re-anchored server-side
        // in general.system_timezone so semantics stay consistent across multi-timezone teams.
        requested_until: `${until}T${untilTime}:00Z`,
        reason,
      });
      toast.success(t("query.apply.submitted"));
      close();
    } catch (error) {
      const msg = describeErrorText(describeError(error, "createQueryAccessRequest"));
      setErrorText(msg);
      toast.error(msg);
    }
  };

  return (
    <Dialog open={flow !== null} onOpenChange={(open) => { if (!open) close(); }}>
      <DialogContent data-testid="query-access-request-dialog">
        <DialogHeader>
          <DialogTitle>{t("query.apply.title", { name: flow?.name ?? "" })}</DialogTitle>
          <DialogDescription>{t("query.apply.description")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label>{t("query.apply.datasources")}</Label>
            <p className="text-muted-foreground text-xs">{t("query.apply.datasourcesHint")}</p>
            <div className="flex flex-col gap-2">
              {capabilities.map((capability) => (
                <div
                  key={capability.datasource_id}
                  className="flex flex-row items-center gap-3 rounded-lg border px-3 py-2 text-sm"
                >
                  <Checkbox
                    id={`query-apply-ds-${capability.datasource_id}`}
                    checked={selected.includes(capability.datasource_id)}
                    onCheckedChange={() => { toggle(capability.datasource_id); }}
                    data-testid={`query-apply-ds-${capability.datasource_id}`}
                  />
                  <Label
                    htmlFor={`query-apply-ds-${capability.datasource_id}`}
                    className="min-w-0 flex-1 cursor-pointer justify-between gap-3 text-xs font-normal"
                  >
                    <span className="truncate font-mono">{capability.datasource_name}</span>
                    {capability.can_export && <Badge variant="secondary">{t("query.entry.exportable")}</Badge>}
                  </Label>
                </div>
              ))}
            </div>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="query-apply-until">{t("query.apply.until")}</Label>
            <DatePicker
              value={until}
              onChange={(next) => { setUntil(next); }}
              time={untilTime}
              onTimeChange={(next) => { setUntilTime(next); }}
              timeLabel={t("query.apply.untilTime")}
              timeId="query-apply-until-time"
              timeTestId="query-apply-until-time"
              testId="query-apply-until"
              ariaLabel={t("query.apply.until")}
              disableBefore={todayLocal()}
              className="w-full"
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="query-apply-reason">{t("query.apply.reason")}</Label>
            <Textarea
              id="query-apply-reason"
              value={reason}
              onChange={(event) => { setReason(event.target.value); }}
              maxLength={4096}
              data-testid="query-apply-reason"
            />
          </div>
          {errorText !== null && (
            <Alert variant="destructive" data-testid="query-apply-error">
              <AlertTitle>{t("query.apply.failed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={close}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            onClick={() => { void submit(); }}
            pending={createRequest.isPending}
            disabled={!valid}
            data-testid="query-apply-submit"
          >
            {t("query.apply.submit")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
