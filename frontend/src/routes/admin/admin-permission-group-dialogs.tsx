import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Pencil, Trash2 } from "lucide-react";
import type { Flow, PermissionGroup, User } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useCreatePermissionGroup,
  useDeletePermissionGroup,
  useReplacePermissionGroup,
} from "@/features/admin/use-admin";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast as toast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { StatusBadge } from "@/shared/components/status/status-badge";
import { TableRowActions } from "@/shared/components/table-row-actions";
import { TableSearchInput } from "@/shared/components/table-search-input";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Checkbox } from "@/shared/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { DropdownMenuItem } from "@/shared/components/ui/dropdown-menu";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { TableCell, TableRow } from "@/shared/components/ui/table";

interface GroupFormState {
  name: string;
  enabled: boolean;
  memberIds: string[];
  flowIds: string[];
}

export function GroupRow({
  group,
  userById,
  flowById,
  onEdit,
}: {
  group: PermissionGroup;
  userById: Map<string, User>;
  flowById: Map<string, Flow>;
  onEdit: () => void;
}) {
  const { t } = useTranslation();
  const deleteGroup = useDeletePermissionGroup();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [errorText, setErrorText] = useState<string | null>(null);

  return (
    <>
      <TableRow data-testid={`admin-group-row-${group.id}`} data-row-key={group.id}>
        <TableCell className="font-semibold text-foreground">{group.name}</TableCell>
        <TableCell>
          <StatusBadge tone={group.enabled ? "emerald" : "stone"}>
            {group.enabled ? t("adminGroups.enabled") : t("adminGroups.disabled")}
          </StatusBadge>
        </TableCell>
        <TableCell className="text-muted-foreground max-w-56 truncate text-xs">
          {group.member_user_ids
            .map((id) => userById.get(id)?.username ?? id)
            .join(", ") || "—"}
        </TableCell>
        <TableCell className="text-muted-foreground max-w-56 truncate text-xs">
          {group.granted_flow_ids
            .map((id) => flowById.get(id)?.name ?? id)
            .join(", ") || "—"}
        </TableCell>
        <TableCell className="text-right">
          <TableRowActions testId={`admin-group-actions-${group.id}`}>
            <DropdownMenuItem onClick={onEdit} data-testid={`admin-group-edit-${group.id}`}>
              <Pencil /> {t("common.edit")}
            </DropdownMenuItem>
            <DropdownMenuItem
              variant="destructive"
              onClick={() => {
                setErrorText(null);
                setConfirmOpen(true);
              }}
              data-testid={`admin-group-delete-${group.id}`}
            >
              <Trash2 /> {t("common.delete")}
            </DropdownMenuItem>
          </TableRowActions>
        </TableCell>
      </TableRow>

      <Dialog open={confirmOpen} onOpenChange={(next) => { if (!next) setConfirmOpen(false); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("adminGroups.delete.title", { name: group.name })}</DialogTitle>
            <DialogDescription>{t("adminGroups.delete.description")}</DialogDescription>
          </DialogHeader>
          {errorText !== null && (
            <Alert variant="destructive">
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => { setConfirmOpen(false); }}>
              {t("common.cancel")}
            </Button>
            <PendingButton
              variant="destructive"
              pending={deleteGroup.isPending}
              onClick={() => {
                deleteGroup.mutate(
                  { groupId: group.id, version: group.version },
                  {
                    onSuccess: () => {
                      toast.success(t("common.delete"), { description: group.name });
                      setConfirmOpen(false);
                    },
                    onError: (error) => {
                      const msg = describeErrorText(describeError(error, "deletePermissionGroup"));
                      setErrorText(msg);
                      toast.error(msg);
                    },
                  },
                );
              }}
              data-testid="admin-group-delete-confirm"
            >
              {t("adminGroups.delete.confirm")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

export function GroupFormDialog({
  editing,
  users,
  flows,
  onClose,
}: {
  editing: PermissionGroup | null;
  users: User[];
  flows: Flow[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const createGroup = useCreatePermissionGroup();
  const replaceGroup = useReplacePermissionGroup();
  const [form, setForm] = useState<GroupFormState>(() =>
    editing === null
      ? { name: "", enabled: true, memberIds: [], flowIds: [] }
      : {
          name: editing.name,
          enabled: editing.enabled,
          memberIds: [...editing.member_user_ids],
          flowIds: [...editing.granted_flow_ids],
        },
  );
  const [errorText, setErrorText] = useState<string | null>(null);
  const [memberSearch, setMemberSearch] = useState("");
  const [flowSearch, setFlowSearch] = useState("");

  const toggle = (list: "memberIds" | "flowIds", id: string) => {
    setForm((current) => ({
      ...current,
      [list]: current[list].includes(id) ? current[list].filter((item) => item !== id) : [...current[list], id],
    }));
  };

  // Select-all applies to the FILTERED rows only (§18.46): checking it unions
  // the visible ids into the selection, unchecking removes exactly the
  // visible ones — rows hidden by the search keep their state untouched.
  const toggleAll = (list: "memberIds" | "flowIds", visibleIds: string[]) => {
    setForm((current) => {
      const selected = new Set(current[list]);
      const allVisibleSelected = visibleIds.length > 0 && visibleIds.every((id) => selected.has(id));
      for (const id of visibleIds) {
        if (allVisibleSelected) selected.delete(id);
        else selected.add(id);
      }
      return { ...current, [list]: [...selected] };
    });
  };

  const memberKeyword = memberSearch.trim().toLowerCase();
  const visibleUsers =
    memberKeyword === ""
      ? users
      : users.filter(
          (user) =>
            (user.display_name ?? "").toLowerCase().includes(memberKeyword) ||
            user.username.toLowerCase().includes(memberKeyword),
        );
  const flowKeyword = flowSearch.trim().toLowerCase();
  const visibleFlows =
    flowKeyword === "" ? flows : flows.filter((flow) => flow.name.toLowerCase().includes(flowKeyword));
  const visibleMemberIds = visibleUsers.map((user) => user.id);
  const visibleFlowIds = visibleFlows.map((flow) => flow.id);
  const selectedVisibleMembers = visibleMemberIds.filter((id) => form.memberIds.includes(id)).length;
  const selectedVisibleFlows = visibleFlowIds.filter((id) => form.flowIds.includes(id)).length;

  const submit = async () => {
    setErrorText(null);
    const body = {
      name: form.name,
      enabled: form.enabled,
      member_user_ids: form.memberIds,
      granted_flow_ids: form.flowIds,
    };
    try {
      if (editing === null) {
        await createGroup.mutateAsync(body);
      } else {
        await replaceGroup.mutateAsync({ groupId: editing.id, version: editing.version, body });
      }
      toast.success(editing === null ? t("common.createdToast") : t("common.savedToast"), { description: form.name });
      onClose();
    } catch (error) {
      const msg = describeErrorText(describeError(error, editing === null ? "createPermissionGroup" : "replacePermissionGroup"));
      setErrorText(msg);
      toast.error(msg);
    }
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent
        className="max-h-[min(760px,86vh)] sm:max-w-2xl"
        viewportClassName="max-h-[calc(min(760px,86vh)-10.5rem)]"
      >
        <DialogHeader>
          <DialogTitle>{editing === null ? t("adminGroups.createTitle") : t("adminGroups.editTitle", { name: editing.name })}</DialogTitle>
          <DialogDescription>{t("adminGroups.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-2">
            <Label htmlFor="group-name">{t("adminGroups.column.name")}</Label>
            <Input
              id="group-name"
              value={form.name}
              onChange={(event) => { setForm({ ...form, name: event.target.value }); }}
              maxLength={128}
              data-testid="group-name"
            />
          </div>
          <label className="flex cursor-pointer items-center gap-2 text-sm">
            <Checkbox
              checked={form.enabled}
              onCheckedChange={(checked) => { setForm({ ...form, enabled: checked }); }}
              data-testid="group-enabled"
            />
            {t("adminGroups.column.enabled")}
          </label>

          <div className="flex flex-col gap-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <Label>{t("adminGroups.members")}</Label>
              <TableSearchInput
                value={memberSearch}
                onChange={setMemberSearch}
                placeholder={t("adminGroups.memberSearchPlaceholder")}
                testId="group-member-search"
              />
            </div>
            <label className="flex cursor-pointer items-center gap-2 border-b pb-2 text-sm" data-testid="group-member-select-all-row">
              <Checkbox
                checked={visibleMemberIds.length > 0 && selectedVisibleMembers === visibleMemberIds.length}
                indeterminate={selectedVisibleMembers > 0 && selectedVisibleMembers < visibleMemberIds.length}
                onCheckedChange={() => { toggleAll("memberIds", visibleMemberIds); }}
                data-testid="group-member-select-all"
              />
              {t("adminGroups.selectAll")}
              <span className="text-muted-foreground text-xs tabular-nums" data-testid="group-member-select-all-count">
                {String(selectedVisibleMembers)}/{String(visibleMemberIds.length)}
              </span>
            </label>
            {/* Unified scrollbars (owner 2026-11-03): member/flow selection lists use ScrollArea
                overlay thumb; height cap sits on the viewport (see scroll-area.tsx comment). */}
            <ScrollArea className="rounded-md border p-2" viewportClassName="max-h-48">
              <div className="pr-3">
              {visibleUsers.length === 0 && (
                <p className="text-muted-foreground p-2 text-xs">{t("table.emptySearch")}</p>
              )}
              {visibleUsers.map((user) => (
                <label key={user.id} className="flex cursor-pointer items-center gap-2 px-1 py-1 text-sm">
                  <Checkbox
                    checked={form.memberIds.includes(user.id)}
                    onCheckedChange={() => { toggle("memberIds", user.id); }}
                    data-testid={`group-member-${user.id}`}
                  />
                  {user.display_name}
                  <span className="text-muted-foreground font-mono text-xs">{user.username}</span>
                </label>
              ))}
              </div>
            </ScrollArea>
          </div>

          <div className="flex flex-col gap-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <Label>{t("adminGroups.flows")}</Label>
              <TableSearchInput
                value={flowSearch}
                onChange={setFlowSearch}
                placeholder={t("adminGroups.flowSearchPlaceholder")}
                testId="group-flow-search"
              />
            </div>
            <p className="text-muted-foreground text-xs">{t("adminGroups.flowsHint")}</p>
            <label className="flex cursor-pointer items-center gap-2 border-b pb-2 text-sm" data-testid="group-flow-select-all-row">
              <Checkbox
                checked={visibleFlowIds.length > 0 && selectedVisibleFlows === visibleFlowIds.length}
                indeterminate={selectedVisibleFlows > 0 && selectedVisibleFlows < visibleFlowIds.length}
                onCheckedChange={() => { toggleAll("flowIds", visibleFlowIds); }}
                data-testid="group-flow-select-all"
              />
              {t("adminGroups.selectAll")}
              <span className="text-muted-foreground text-xs tabular-nums" data-testid="group-flow-select-all-count">
                {String(selectedVisibleFlows)}/{String(visibleFlowIds.length)}
              </span>
            </label>
            <ScrollArea className="rounded-md border p-2" viewportClassName="max-h-48">
              <div className="pr-3">
              {visibleFlows.length === 0 && (
                <p className="text-muted-foreground p-2 text-xs">
                  {flowKeyword !== "" ? t("table.emptySearch") : t("adminGroups.noFlows")}
                </p>
              )}
              {visibleFlows.map((flow) => (
                <label key={flow.id} className="flex cursor-pointer items-center gap-2 px-1 py-1 text-sm">
                  <Checkbox
                    checked={form.flowIds.includes(flow.id)}
                    onCheckedChange={() => { toggle("flowIds", flow.id); }}
                    data-testid={`group-flow-${flow.id}`}
                  />
                  {flow.name}
                  <Badge variant="outline">
                    {flow.flow_type === "change_review" ? t("adminGroups.flowKind.change") : t("adminGroups.flowKind.query")}
                  </Badge>
                </label>
              ))}
              </div>
            </ScrollArea>
          </div>

          {errorText !== null && (
            <Alert variant="destructive" data-testid="group-form-error">
              <AlertTitle>{t("adminGroups.formFailed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            pending={createGroup.isPending || replaceGroup.isPending}
            disabled={form.name.trim() === ""}
            onClick={() => void submit()}
            data-testid="group-form-submit"
          >
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
