import i18next from "@/shared/i18n";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Megaphone, Plus, UploadCloud } from "lucide-react";
import type { AnnouncementRevision } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useSession } from "@/features/auth/session-provider";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Spinner } from "@/shared/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";
import { Textarea } from "@/shared/components/ui/textarea";
import { useAnnouncementRevisions, useCreateAnnouncementRevision, usePublishAnnouncement } from "@/features/admin/use-admin";
import { useCurrentAnnouncementQuery } from "@/features/announcements/use-current-announcement";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { formatTimestamp } from "@/shared/lib/timezones";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { TableRowActions } from "@/shared/components/table-row-actions";

/**
 * Announcement Management (route /admin/announcements; UI spec §9.2 ruling keeps it
 * independent of /admin/settings/branding). S005: revisions are sanitized
 * append-only — the editor only takes title + restricted Markdown source,
 * the server sanitizes to HTML (rendered verbatim by the workspace banner);
 * publishing moves the single current pointer. No editing or deleting of
 * existing revisions exists by contract.
 */

function describeErrorText(display: { messageKey: string; requestId: string | null }): string {
  // Translated through the shared i18n bundle so users never see raw keys.
  const text = i18next.t(display.messageKey);
  return display.requestId === null ? text : `${text} (${display.requestId})`;
}

export default function AdminAnnouncementsPage() {
  const { t } = useTranslation();
  const session = useSession();
  const isAdmin = session.user?.can_access_admin === true;
  const pager = useCursorPage(20);
  const revisionsQuery = useAnnouncementRevisions(isAdmin, { limit: pager.pageSize, after: pager.after });
  // §18.82: capture before the exhaustive ternary so narrowing does not leak into pagination guards.
  const revisionsLoaded = !revisionsQuery.isPending && !revisionsQuery.isError;
  const currentQuery = useCurrentAnnouncementQuery(isAdmin);
  const createRevision = useCreateAnnouncementRevision();
  const publish = usePublishAnnouncement();
  const [createOpen, setCreateOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [markdown, setMarkdown] = useState("");
  const [errorText, setErrorText] = useState<string | null>(null);

  const revisions = revisionsQuery.data?.items ?? [];
  // Announcement revisions only paginate without filtering; tween row position changes when the row set changes.
  const revisionsBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(revisions.map((revision) => revision.id)),
  );
  const revisionsPage = revisionsQuery.data?.page;
  const current = currentQuery.data ?? null;

  const submitCreate = async () => {
    setErrorText(null);
    try {
      await createRevision.mutateAsync({ title, markdown_source: markdown });
      setTitle("");
      setMarkdown("");
      setCreateOpen(false);
      appToast.success(t("adminAnnouncements.create"));
    } catch (error) {
      const errText = describeErrorText(describeError(error, "createAnnouncementRevision"));
      setErrorText(errText);
      appToast.error(errText);
    }
  };

  return (
    <PageLayout data-testid="admin-announcements-page">
      <PageHeader
        title={t("adminAnnouncements.title")}
        description={t("adminAnnouncements.description")}
        icon={Megaphone}
        actions={
          <Button onClick={() => { setCreateOpen(true); }} data-testid="admin-announcements-create">
            <Plus />
            {t("adminAnnouncements.create")}
          </Button>
        }
      />

      {current !== null && (
        <Card data-testid="admin-announcements-current">
          <CardHeader className="pb-2">
            <CardTitle className="flex items-center gap-2">
              <Megaphone className="size-4" />
              {t("adminAnnouncements.current")}
            </CardTitle>
            <CardDescription>
              {t("adminAnnouncements.currentMeta", {
                revision: current.revision.revision_number,
                at: formatTimestamp(current.published_at),
              })}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <ScrollArea className="h-44" viewportClassName="h-44 pr-3">
              <div
                className="prose prose-sm dark:prose-invert max-w-none pr-1 text-sm"
                dangerouslySetInnerHTML={{ __html: current.revision.sanitized_html }}
              />
            </ScrollArea>
          </CardContent>
        </Card>
      )}

      {revisionsQuery.isPending && <LoadingState variant="table" columns={5} />}
      {revisionsQuery.error !== null && (
        <ErrorState error={revisionsQuery.error} operationId="listAnnouncementRevisions" onRetry={() => void revisionsQuery.refetch()} />
      )}

      {!revisionsQuery.isPending && revisionsQuery.error === null && (
        <Card>
          <CardHeader className="border-b pb-3">
            <CardTitle>{t("adminAnnouncements.revisions")}</CardTitle>
            <CardDescription>{t("adminAnnouncements.revisionsDescription")}</CardDescription>
          </CardHeader>
          <CardContent className="px-0!">
            {revisions.length === 0 ? (
              <EmptyState title={t("adminAnnouncements.empty")} testId="admin-announcements-empty" />
            ) : (
              <>
              <Table data-testid="admin-announcements-table">
                <TableHeader>
                  <TableRow>
                    <TableHead>#</TableHead>
                    <TableHead>{t("adminAnnouncements.column.title")}</TableHead>
                    <TableHead>{t("adminAnnouncements.column.createdAt")}</TableHead>
                    <TableHead>{t("adminAnnouncements.column.hash")}</TableHead>
                    <TableHead />
                  </TableRow>
                </TableHeader>
                <TableBody ref={revisionsBodyRef} stale={revisionsQuery.isFetching && revisionsQuery.isPlaceholderData}>
                  {revisions.map((revision: AnnouncementRevision) => (
                    <TableRow key={revision.id} data-testid={`admin-announcement-row-${revision.id}`} data-row-key={revision.id}>
                      <TableCell className="text-base font-semibold tabular-nums text-foreground">
                        {revision.revision_number}
                      </TableCell>
                      <TableCell className="font-semibold text-foreground">{revision.title}</TableCell>
                      <TableCell className="text-muted-foreground">
                        {formatTimestamp(revision.created_at)}
                      </TableCell>
                      <TableCell className="font-mono text-xs">
                        {revision.content_sha256.slice(0, 12)}…
                      </TableCell>
                      <TableCell className="text-right">
                        <TableRowActions testId={`admin-announcement-actions-${revision.id}`}>
                          <DropdownMenuItem
                            disabled={
                              current?.revision.id === revision.id ||
                              publish.isPending
                            }
                            aria-busy={publish.isPending || undefined}
                            onClick={() => {
                              setErrorText(null);
                              publish.mutate(
                                {
                                  body: { announcement_revision_id: revision.id },
                                  publicationVersion: current?.version ?? 1,
                                },
                                {
                                  onSuccess: () => {
                                    appToast.success(t("adminAnnouncements.published"), {
                                      description: revision.title,
                                    });
                                  },
                                  onError: (error) => {
                                    const errText = describeErrorText(
                                      describeError(error, "publishAnnouncementRevision"),
                                    );
                                    setErrorText(errText);
                                    appToast.error(errText);
                                  },
                                },
                              );
                            }}
                            data-testid={`admin-announcement-publish-${revision.id}`}
                          >
                            {publish.isPending ? <Spinner aria-hidden /> : <UploadCloud />}
                            {current?.revision.id === revision.id ? t("adminAnnouncements.published") : t("adminAnnouncements.publish")}
                          </DropdownMenuItem>
                        </TableRowActions>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              </>
            )}
      {revisionsLoaded ? (
        <TablePagination
          busy={revisionsQuery.isFetching && revisionsQuery.isPlaceholderData}
          page={pager.pageNo}
          hasMore={revisionsPage?.has_more ?? false}
          isFirst={pager.isFirst}
          onPrev={pager.goPrev}
          onJumpPage={pager.goToPage}
          onNext={() => {
            pager.pushCursor(revisionsPage?.next_cursor);
          }}
          pageSize={pager.pageSize}
          onPageSizeChange={pager.setPageSize}
          rowsOnPage={revisions.length}
          testIdPrefix="announcements"
        />
      ) : null}
          </CardContent>
        </Card>
      )}

      {errorText !== null && (
        <Alert variant="destructive" data-testid="admin-announcements-error">
          <AlertTitle>{t("adminAnnouncements.operationFailed")}</AlertTitle>
          <AlertDescription>{errorText}</AlertDescription>
        </Alert>
      )}

      <Dialog open={createOpen} onOpenChange={(next) => { if (!next) setCreateOpen(false); }}>
        <DialogContent className="sm:max-w-2xl" data-testid="admin-announcement-create-dialog">
          <DialogHeader>
            <DialogTitle>{t("adminAnnouncements.createTitle")}</DialogTitle>
            <DialogDescription>{t("adminAnnouncements.createDescription")}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <div className="flex flex-col gap-2">
              <Label htmlFor="announcement-title">{t("adminAnnouncements.column.title")}</Label>
              <Input
                id="announcement-title"
                value={title}
                onChange={(event) => { setTitle(event.target.value); }}
                maxLength={200}
                data-testid="announcement-title"
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="announcement-markdown">{t("adminAnnouncements.markdown")}</Label>
              <Textarea
                id="announcement-markdown"
                value={markdown}
                onChange={(event) => { setMarkdown(event.target.value); }}
                className="min-h-64"
                maxLength={20000}
                data-testid="announcement-markdown"
              />
              <p className="text-muted-foreground text-xs">{t("adminAnnouncements.markdownHint")}</p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => { setCreateOpen(false); }}>
              {t("common.cancel")}
            </Button>
            <PendingButton
              pending={createRevision.isPending}
              disabled={title.trim() === "" || markdown.trim() === ""}
              onClick={() => void submitCreate()}
              data-testid="announcement-create-submit"
            >
              {t("adminAnnouncements.create")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </PageLayout>
  );
}
