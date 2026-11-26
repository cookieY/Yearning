import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Lock, Pencil, Plus, Puzzle, Trash2 } from "lucide-react";
import type { PromptTool } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  DeleteReviewInputDialog,
  StateBadge,
} from "@/features/admin/review-input-shared";
import { renderSkillDescription } from "@/features/admin/skill-description";
import {
  convertParamDrafts,
  paramTypeKey,
  SkillFormDialog,
  stringifyParamDrafts,
} from "@/features/admin/skills-form-dialog";
import { usePromptTools } from "@/features/admin/use-admin";
import { useSession } from "@/features/auth/session-provider";
import { ListToolbar, PageHeader, PageLayout } from "@/shared/components/page-layout";
import { EmptyState } from "@/shared/components/status/empty-state";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { TableSearchInput } from "@/shared/components/table-search-input";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent } from "@/shared/components/ui/card";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/shared/components/ui/table";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { useFlipReorder } from "@/shared/lib/use-flip-reorder";

export { convertParamDrafts, paramTypeKey, stringifyParamDrafts };

/** PromptTool registry management page (/admin/review-engine/skills). */
export function SkillsManagementPage() {
  const { t } = useTranslation();
  const session = useSession();
  const enabled = session.user?.can_access_admin === true;
  const [skillSearch, setSkillSearch] = useState("");
  const skillPager = useCursorPage(20);
  const skillKeyword = skillSearch.trim();
  useEffect(() => { skillPager.reset(); }, [skillKeyword]); // eslint-disable-line react-hooks/exhaustive-deps -- reset is intentional on keyword change
  const toolsQuery = usePromptTools(enabled, {
    q: skillKeyword || undefined,
    limit: skillPager.pageSize,
    after: skillPager.after,
  });
  const toolsLoaded = !toolsQuery.isPending && !toolsQuery.isError;
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<PromptTool | null>(null);
  const [deleting, setDeleting] = useState<PromptTool | null>(null);

  const visibleSkillRows = toolsQuery.data?.items ?? [];
  const skillBodyRef = useFlipReorder<HTMLTableSectionElement>(
    JSON.stringify(visibleSkillRows.map((row) => row.id)),
  );

  return (
    <PageLayout>
      <PageHeader
        title={t("admin.skills.title")}
        description={t("admin.skills.description")}
        icon={Puzzle}
        actions={
          <Button
            onClick={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
            data-testid="review-input-create"
          >
            <Plus /> {t("admin.skills.createTitle")}
          </Button>
        }
      />
      <Card>
        <ListToolbar busy={toolsQuery.isFetching && !toolsQuery.isPending} fill>
          <TableSearchInput value={skillSearch} onChange={setSkillSearch} testId="admin-skills-search" />
        </ListToolbar>
        <CardContent className="px-0! flex flex-col gap-3">
          {toolsQuery.isPending ? (
            <LoadingState variant="table" columns={3} />
          ) : toolsQuery.isError ? (
            <ErrorState error={toolsQuery.error} operationId="listPromptTools" onRetry={() => void toolsQuery.refetch()} />
          ) : visibleSkillRows.length === 0 && skillKeyword !== "" ? (
            <EmptyState title={t("table.emptySearch")} filtered testId="admin-skills-search-empty" />
          ) : visibleSkillRows.length === 0 ? (
            <EmptyState
              title={t("admin.skills.emptyTitle")}
              description={t("admin.skills.emptyDescription")}
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("admin.reviewInput.name")}</TableHead>
                  <TableHead>{t("admin.reviewInput.state")}</TableHead>
                  <TableHead className="text-right">{t("common.actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody ref={skillBodyRef} stale={toolsQuery.isFetching && toolsQuery.isPlaceholderData}>
                {visibleSkillRows.map((row) => (
                  <TableRow key={row.id} data-row-key={row.id} data-testid={`review-input-row-${row.id}`}>
                    <TableCell className="font-semibold text-foreground">
                      <span className="flex flex-col gap-0.5">
                        <span className="flex items-center gap-1.5">
                          {row.name}
                          {row.is_builtin ? (
                            <Badge variant="outline" data-testid={`review-input-builtin-${row.id}`}>
                              <Lock /> {t("admin.skills.builtinBadge")}
                            </Badge>
                          ) : null}
                        </span>
                        {row.description ? (
                          <span
                            className="text-xs font-normal text-muted-foreground"
                            data-testid={`review-input-description-${row.id}`}
                          >
                            {renderSkillDescription(row.description, row.parameters ?? {})}
                          </span>
                        ) : null}
                      </span>
                    </TableCell>
                    <TableCell>
                      <StateBadge state={row.state} />
                    </TableCell>
                    <TableCell className="text-right">
                      <TableRowActions testId={`review-input-actions-${row.id}`}>
                        <DropdownMenuItem
                          onClick={() => {
                            setEditing(row);
                            setDialogOpen(true);
                          }}
                          data-testid={`review-input-edit-${row.id}`}
                        >
                          <Pencil /> {t("common.edit")}
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          variant="destructive"
                          onClick={() => { setDeleting(row); }}
                          disabled={row.is_builtin}
                          data-testid={`review-input-delete-${row.id}`}
                        >
                          <Trash2 /> {t("common.delete")}
                        </DropdownMenuItem>
                      </TableRowActions>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
          {toolsLoaded ? (
            <TablePagination
              busy={toolsQuery.isFetching && toolsQuery.isPlaceholderData}
              page={skillPager.pageNo}
              hasMore={toolsQuery.data.page.has_more}
              isFirst={skillPager.isFirst}
              onPrev={skillPager.goPrev}
              onJumpPage={skillPager.goToPage}
              onNext={() => { skillPager.pushCursor(toolsQuery.data.page.next_cursor); }}
              pageSize={skillPager.pageSize}
              onPageSizeChange={skillPager.setPageSize}
              rowsOnPage={toolsQuery.data.items.length}
              testIdPrefix="skills"
            />
          ) : null}
        </CardContent>
      </Card>

      {dialogOpen ? (
        <SkillFormDialog
          key={editing?.id ?? "new"}
          editing={editing}
          onClose={() => {
            setDialogOpen(false);
            setEditing(null);
          }}
        />
      ) : null}

      {deleting !== null ? (
        <DeleteReviewInputDialog
          key={deleting.id}
          kind="skills"
          target={deleting}
          onClose={() => { setDeleting(null); }}
        />
      ) : null}
    </PageLayout>
  );
}
