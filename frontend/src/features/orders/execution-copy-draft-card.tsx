import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import type { ChangeOrder } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { useCopyOrderToDraft } from "@/features/orders/use-orders";
import { listCurrentUserFlows } from "@/api/generated/client/change-drafts/change-drafts";
import { describeError } from "@/shared/api/error-display";
import { unwrapApi } from "@/shared/api/mutator";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { PendingButton } from "@/shared/components/pending-button";
import { Button } from "@/shared/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/shared/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/shared/components/ui/dialog";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { Copy } from "lucide-react";

/** Order states whose execution fate is final and unretryable — the only
 * forward path is a copied draft re-running the whole review pipeline. */
const COPY_ONLY_STATES = new Set([
  "failed",
  "partial_failed",
  "cancelled",
  "partial_cancelled",
  "missed_schedule",
]);

/** Copy-only notice for unretryable execution fates (E004 gate:
 * non-not_started fates only copy to a new draft) — a fresh draft re-runs review and approval; no
 * retry and no rollback exists. */
export function CopyDraftCard({ order }: { order: ChangeOrder }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const copy = useCopyOrderToDraft(order);
  const [open, setOpen] = useState(false);
  const [flowId, setFlowId] = useState<string>("");
  const [title, setTitle] = useState(`${order.title ?? ""} · ${t("orders.execution.copySuffix")}`);
  const [error, setError] = useState<string | null>(null);
  const [flows, setFlows] = useState<Array<{ id: string; name: string }>>([]);
  const [flowsLoaded, setFlowsLoaded] = useState(false);
  const [flowsPending, setFlowsPending] = useState(false);

  if (!COPY_ONLY_STATES.has(order.state)) return null;

  const openDialog = async (): Promise<void> => {
    setOpen(true);
    setError(null);
    if (flowsLoaded) return;
    setFlowsPending(true);
    try {
      const page = await unwrapApi(listCurrentUserFlows({ flow_type: "change_review" }));
      const flowList = page.items.map((item) => ({ id: item.id, name: item.name }));
      setFlows(flowList);
      setFlowId(flowList[0]?.id ?? "");
      setFlowsLoaded(true);
    } catch {
      setFlows([]);
    } finally {
      setFlowsPending(false);
    }
  };

  const runCopy = (): void => {
    copy.mutate(
      { target_flow_id: flowId, title: title.trim() },
      {
        onSuccess: (draft) => {
          appToast.success(t("common.createdToast"), { description: title.trim() });
          void navigate(`/changes/drafts/${draft.id}`);
        },
        onError: (mutationError) => {
          const display = describeError(mutationError, "copyChangeOrderToDraft");
          const msg = `${t(display.messageKey)}${display.requestId !== null ? ` (${display.requestId})` : ""}`;
          setError(msg);
          appToast.error(msg);
        },
      },
    );
  };

  return (
    <>
      <Card data-testid="copy-draft-card">
        <CardHeader className="pb-3">
          <CardTitle className="flex items-center gap-2">
            <Copy className="size-4" aria-hidden />
            {t("orders.execution.copyTitle")}
          </CardTitle>
          <CardDescription>{t("orders.execution.copyDescription")}</CardDescription>
        </CardHeader>
        <CardContent>
          <PendingButton
            variant="outline"
            pending={flowsPending}
            onClick={() => { void openDialog(); }}
            data-testid="copy-draft-open"
          >
            {t("orders.execution.copyOpen")}
          </PendingButton>
        </CardContent>
      </Card>

      <Dialog open={open} onOpenChange={(next) => { if (!next) { setOpen(false); setError(null); } }}>
        <DialogContent data-testid="copy-draft-dialog">
          <DialogHeader>
            <DialogTitle>{t("orders.execution.copyDialogTitle")}</DialogTitle>
            <DialogDescription>{t("orders.execution.copyDialogDescription")}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <div className="flex flex-col gap-1">
              <Label htmlFor="copy-draft-flow">{t("orders.execution.copyFlow")}</Label>
              <Select
                value={flowId}
                onValueChange={(value) => { if (value !== null) setFlowId(value); }}
                items={Object.fromEntries(flows.map((flow) => [flow.id, flow.name]))}
              >
                <SelectTrigger data-testid="copy-draft-flow">
                  <SelectValue placeholder={t("orders.execution.copyFlowPlaceholder")} />
                </SelectTrigger>
                <SelectContent>
                  {flows.map((flow) => (
                    <SelectItem key={flow.id} value={flow.id}>{flow.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="copy-draft-title">{t("orders.execution.copyTitleLabel")}</Label>
              <Input
                id="copy-draft-title"
                value={title}
                onChange={(event) => { setTitle(event.target.value); }}
                maxLength={128}
                data-testid="copy-draft-title"
              />
            </div>
            {error !== null && (
              <p role="alert" className="text-destructive text-sm" data-testid="copy-draft-error">{error}</p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => { setOpen(false); setError(null); }} data-testid="copy-draft-cancel">
              {t("common.cancel")}
            </Button>
            <PendingButton
              onClick={runCopy}
              pending={copy.isPending}
              disabled={flowId === "" || title.trim() === ""}
              data-testid="copy-draft-confirm"
            >
              {t("orders.execution.copyOpen")}
            </PendingButton>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
