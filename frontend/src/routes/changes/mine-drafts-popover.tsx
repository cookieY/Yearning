import { useTranslation } from "react-i18next";
import type { ChangeDraft } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { PendingButton } from "@/shared/components/pending-button";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Popover, PopoverContent, PopoverTrigger } from "@/shared/components/ui/popover";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { formatTimestamp } from "@/shared/lib/timezones";
import { FileStack, Inbox, Trash2 } from "lucide-react";

function DraftRow({
  draft,
  onSelect,
  onDelete,
}: {
  draft: ChangeDraft;
  onSelect: (draft: ChangeDraft) => void;
  onDelete: (draft: ChangeDraft) => void;
}) {
  const { t } = useTranslation();
  return (
    <div
      role="button"
      tabIndex={0}
      onClick={() => { onSelect(draft); }}
      onKeyDown={(event) => {
        if (event.key === "Enter") onSelect(draft);
      }}
      className="group hover:bg-muted/40 focus-visible:bg-muted/40 flex w-full cursor-pointer items-center gap-3 px-5 py-3 text-left outline-none transition-colors duration-150 ease-out"
      data-testid="mine-draft-row"
    >
      <div className="relative shrink-0">
        <span
          className="bg-primary ring-popover absolute top-0 right-0 size-2.5 rounded-full ring-2"
          aria-hidden
        />
        <span className="bg-secondary text-secondary-foreground flex size-10 items-center justify-center rounded-full">
          <FileStack className="size-4" aria-hidden />
        </span>
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between gap-2">
          <span className="text-foreground truncate text-sm font-semibold">
            {draft.title}
          </span>
          <span className="text-muted-foreground shrink-0 text-xs">
            {draft.updated_at.slice(0, 10)}
          </span>
        </div>
        <div className="mt-0.5 flex items-center justify-between gap-2">
          <span className="text-muted-foreground truncate text-xs">
            {t("orders.list.draftsUpdated", { time: formatTimestamp(draft.updated_at) })}
          </span>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            className="text-muted-foreground hover:text-destructive size-6 shrink-0"
            aria-label={t("orders.list.draftsDelete")}
            onClick={(event) => {
              event.stopPropagation();
              onDelete(draft);
            }}
            data-testid={`delete-draft-${draft.id}`}
          >
            <Trash2 className="size-3.5" aria-hidden />
          </Button>
        </div>
      </div>
    </div>
  );
}

export function DeleteDraftDialog({
  pendingDelete,
  isPending,
  onCancel,
  onConfirm,
}: {
  pendingDelete: ChangeDraft | null;
  isPending: boolean;
  onCancel: () => void;
  onConfirm: (draft: ChangeDraft) => void;
}) {
  const { t } = useTranslation();
  return (
    <Dialog
      open={pendingDelete !== null}
      onOpenChange={(open) => {
        if (!open) onCancel();
      }}
    >
      <DialogContent data-testid="delete-draft-dialog">
        <DialogHeader>
          <DialogTitle>{t("orders.list.draftsDeleteTitle")}</DialogTitle>
          <DialogDescription>
            {t("orders.list.draftsDeleteDescription")}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={onCancel}
            data-testid="delete-draft-cancel"
          >
            {t("common.cancel")}
          </Button>
          <PendingButton
            variant="destructive"
            pending={isPending}
            onClick={() => {
              if (pendingDelete !== null) onConfirm(pendingDelete);
            }}
            data-testid="delete-draft-accept"
          >
            {t("orders.list.draftsDeleteAction")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function MineDraftsPopover({
  open,
  onOpenChange,
  drafts,
  onSelectDraft,
  onRequestDelete,
  onNewDraft,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  drafts: ChangeDraft[];
  onSelectDraft: (draft: ChangeDraft) => void;
  onRequestDelete: (draft: ChangeDraft) => void;
  onNewDraft: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger
        render={
          <Button
            type="button"
            variant="outline"
            size="icon"
            className="relative shrink-0"
            aria-label={t("orders.list.draftsCard")}
            data-testid="mine-drafts-trigger"
          />
        }
      >
        <Inbox className="size-4" aria-hidden />
        {drafts.length > 0 && (
          <span
            className="bg-primary text-primary-foreground ring-card absolute -top-1.5 -right-1.5 flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-[10px] leading-none font-semibold tabular-nums ring-2"
            data-testid="mine-drafts-count"
          >
            {drafts.length}
          </span>
        )}
      </PopoverTrigger>
      <PopoverContent
        align="end"
        sideOffset={8}
        className="w-[360px] rounded-xl p-0"
        data-testid="mine-drafts-popover"
      >
        <div className="flex items-center justify-between border-b border-border px-5 py-3.5">
          <h3 className="text-base font-semibold text-foreground">
            {t("orders.list.draftsCard")}
          </h3>
          <Badge
            variant="secondary"
            className="rounded-full px-2.5 py-0.5 text-xs font-semibold"
            data-testid="mine-drafts-badge"
          >
            {t("orders.list.draftsBadgeCount", {
              total: drafts.length,
            })}
          </Badge>
        </div>
        <ScrollArea viewportClassName="max-h-80">
          {drafts.length === 0 ? (
            <div
              className="px-5 py-8 text-center text-xs text-muted-foreground"
              data-testid="mine-drafts-empty"
            >
              {t("orders.list.draftsEmpty")}
            </div>
          ) : (
            <div className="divide-y divide-border">
              {drafts.map((draft) => (
                <DraftRow
                  key={draft.id}
                  draft={draft}
                  onSelect={onSelectDraft}
                  onDelete={onRequestDelete}
                />
              ))}
            </div>
          )}
        </ScrollArea>
        <div className="border-t border-border px-5 py-3.5">
          <Button
            variant="secondary"
            className="w-full"
            onClick={onNewDraft}
            data-testid="mine-drafts-new"
          >
            {t("orders.list.newDraftAction")}
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}
