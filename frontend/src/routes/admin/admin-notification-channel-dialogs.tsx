import { useState } from "react";
import { useTranslation } from "react-i18next";
import type {
  DingTalkNotificationConfig as DingTalkConfig,
  EmailNotificationConfig as EmailConfig,
  FeishuNotificationConfig as FeishuConfig,
  NotificationChannel,
  NotificationChannelKind,
  SlackNotificationConfig as SlackConfig,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useCreateNotificationChannel,
  useReplaceNotificationChannel,
} from "@/features/admin/use-admin";
import { describeError, describeErrorText } from "@/shared/api/error-display";
import { PendingButton } from "@/shared/components/pending-button";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
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
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { NumberInput } from "@/shared/components/ui/number-input";
import { PasswordInput } from "@/shared/components/ui/password-input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/components/ui/select";

interface ChannelDraft {
  kind: NotificationChannelKind;
  name: string;
  enabled: boolean;
  host: string;
  port: number;
  tlsMode: "required" | "starttls";
  username: string;
  fromAddress: string;
  secret: string;
  // DingTalk-specific: signing secret (required when robot security setting = signature mode); combined with the Webhook
  // into an {access_token, sign_secret} envelope (matching senders.SendDingTalk).
  signSecret: string;
}

const EMPTY_CHANNEL: ChannelDraft = {
  kind: "email",
  name: "",
  enabled: false,
  host: "",
  port: 465,
  tlsMode: "required",
  username: "",
  fromAddress: "",
  secret: "",
  signSecret: "",
};

// DingTalk robot credential = access_token query/path segment in the webhook URL (senders.SendDingTalk).
// Allows pasting either the full Webhook URL (extracting the access_token param) or a bare token directly.
function dingTalkToken(raw: string): string {
  const text = raw.trim();
  try {
    const url = new URL(text);
    const token = url.searchParams.get("access_token");
    if (token !== null && token !== "") return token;
  } catch {
    // Treat non-URL input as a bare access_token.
  }
  return text;
}

// DingTalk credential envelope: when signing secret is filled, use the {access_token, sign_secret} envelope (backend signed
// mode); otherwise send a bare token (compatible with existing channels and unsigned robots).
function dingTalkSecretValue(token: string, signSecret: string): string {
  const sign = signSecret.trim();
  if (sign === "") return token;
  return JSON.stringify({ access_token: token, sign_secret: sign });
}

// Feishu credential envelope: backend validateSecretValue requires JSON {webhook_url, sign_secret?}.
// Package bare Webhook URL + optional signing secret automatically, while keeping compatibility if a valid JSON envelope was pasted.
function feishuSecretValue(rawWebhook: string, rawSignSecret: string): string {
  const trimmedWebhook = rawWebhook.trim();
  if (trimmedWebhook === "") return "";
  let webhookUrl = trimmedWebhook;
  let signSecret = rawSignSecret.trim();
  if (trimmedWebhook.startsWith("{")) {
    try {
      const parsed = JSON.parse(trimmedWebhook) as { webhook_url?: unknown; sign_secret?: unknown };
      if (typeof parsed.webhook_url === "string" && parsed.webhook_url.trim() !== "") {
        webhookUrl = parsed.webhook_url.trim();
        if (signSecret === "" && typeof parsed.sign_secret === "string") {
          signSecret = parsed.sign_secret.trim();
        }
      }
    } catch {
      // Fall through and wrap raw input as webhook_url so backend validation handles malformed input uniformly.
    }
  }
  return signSecret === ""
    ? JSON.stringify({ webhook_url: webhookUrl })
    : JSON.stringify({ webhook_url: webhookUrl, sign_secret: signSecret });
}

function ChannelSecretField({
  id,
  label,
  value,
  hint,
  testId,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  hint: string;
  testId: string;
  onChange: (next: string) => void;
}) {
  return (
    <div className="flex flex-col gap-2">
      <Label htmlFor={id}>{label}</Label>
      <PasswordInput
        id={id}
        value={value}
        onChange={(event) => { onChange(event.target.value); }}
        autoComplete="new-password"
        data-testid={testId}
      />
      <p className="text-muted-foreground text-xs">{hint}</p>
    </div>
  );
}

function initialChannelDraft(editing: NotificationChannel | null): ChannelDraft {
  if (editing === null) return EMPTY_CHANNEL;
  const rawConfig = editing.kind === "email" ? editing.configuration : null;
  return {
    kind: editing.kind,
    name: editing.name,
    enabled: editing.enabled,
    host: rawConfig !== null && "host" in rawConfig && typeof rawConfig.host === "string" ? rawConfig.host : "",
    port: rawConfig !== null && "port" in rawConfig && typeof rawConfig.port === "number" ? rawConfig.port : 465,
    tlsMode: rawConfig !== null && "tls_mode" in rawConfig && rawConfig.tls_mode === "starttls" ? "starttls" : "required",
    username: rawConfig !== null && "username" in rawConfig && typeof rawConfig.username === "string" ? rawConfig.username : "",
    fromAddress: rawConfig !== null && "from_address" in rawConfig && typeof rawConfig.from_address === "string" ? rawConfig.from_address : "",
    secret: "",
    signSecret: "",
  };
}

export function ChannelFormDialog({
  editing,
  onClose,
}: {
  editing: NotificationChannel | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const createChannel = useCreateNotificationChannel();
  const replaceChannel = useReplaceNotificationChannel();
  const [draft, setDraft] = useState<ChannelDraft>(() => initialChannelDraft(editing));
  const [errorText, setErrorText] = useState<string | null>(null);

  const kindLabels: Record<NotificationChannelKind, string> = {
    email: t("adminNotifications.kind.email"),
    dingtalk: t("adminNotifications.kind.dingtalk"),
    feishu: t("adminNotifications.kind.feishu"),
    slack: t("adminNotifications.kind.slack"),
  };
  const tlsLabels: Record<"required" | "starttls", string> = {
    required: t("adminNotifications.tlsMode.required"),
    starttls: t("adminNotifications.tlsMode.starttls"),
  };

  const submit = async () => {
    setErrorText(null);
    // DingTalk: input accepts either a full Webhook URL or a bare token, normalized on submit to
    // access_token per backend contract; if a signing secret is provided, assemble an {access_token, sign_secret} envelope.
    const dingToken = draft.kind === "dingtalk" ? dingTalkToken(draft.secret) : "";
    const secretValue =
      draft.kind === "dingtalk"
        ? dingToken === ""
          ? ""
          : dingTalkSecretValue(dingToken, draft.signSecret)
        : draft.kind === "feishu"
          ? feishuSecretValue(draft.secret, draft.signSecret)
          : draft.kind === "slack"
            ? draft.secret.trim()
            : draft.secret;
    const configuration: EmailConfig | DingTalkConfig | FeishuConfig | SlackConfig =
      draft.kind === "dingtalk"
        ? { webhook_host: "oapi.dingtalk.com" }
        : draft.kind === "feishu"
          ? {}
          : draft.kind === "slack"
            ? {}
            : {
                host: draft.host,
                port: draft.port,
                tls_mode: draft.tlsMode,
                username: draft.username,
                from_address: draft.fromAddress,
              };
    if (editing === null && secretValue === "") {
      setErrorText(
        draft.kind === "email"
          ? t("adminNotifications.smtpPasswordRequired")
          : t("adminNotifications.webhookRequired"),
      );
      return;
    }
    if (
      editing !== null &&
      (draft.kind === "dingtalk" || draft.kind === "feishu") &&
      secretValue === "" &&
      draft.signSecret.trim() !== ""
    ) {
      setErrorText(t("adminNotifications.webhookRequired"));
      return;
    }
    try {
      if (editing === null) {
        await createChannel.mutateAsync({
          kind: draft.kind,
          name: draft.name,
          enabled: draft.enabled,
          configuration,
          secret: { value: secretValue },
        });
      } else {
        await replaceChannel.mutateAsync({
          channelId: editing.id,
          version: editing.version,
          body: {
            kind: draft.kind,
            name: draft.name,
            enabled: draft.enabled,
            configuration,
            secret: secretValue === "" ? undefined : { value: secretValue },
          },
        });
      }
      appToast.success(t("common.save"), { description: draft.name });
      onClose();
    } catch (error) {
      const errText = describeErrorText(
        describeError(error, editing === null ? "createNotificationChannel" : "replaceNotificationChannel"),
      );
      setErrorText(errText);
      appToast.error(errText);
    }
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent
        className="max-h-[min(760px,86vh)] sm:max-w-2xl"
        viewportClassName="max-h-[calc(min(760px,86vh)-10.5rem)]"
      >
        <DialogHeader>
          <DialogTitle>{editing === null ? t("adminNotifications.createTitle") : t("adminNotifications.editTitle", { name: editing.name })}</DialogTitle>
          <DialogDescription>{t("adminNotifications.formDescription")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-2">
            <Label htmlFor="channel-kind">{t("adminNotifications.column.kind")}</Label>
            <Select
              value={draft.kind}
              disabled={editing !== null}
              items={kindLabels}
              onValueChange={(value) => {
                if (value !== "email" && value !== "dingtalk" && value !== "feishu" && value !== "slack") return;
                setDraft({ ...draft, kind: value, secret: "", signSecret: "" });
              }}
            >
              <SelectTrigger
                id="channel-kind"
                className="w-full"
                data-testid="channel-kind"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="email">{kindLabels.email}</SelectItem>
                <SelectItem value="dingtalk">{kindLabels.dingtalk}</SelectItem>
                <SelectItem value="feishu">{kindLabels.feishu}</SelectItem>
                <SelectItem value="slack">{kindLabels.slack}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor="channel-name">{t("adminNotifications.column.name")}</Label>
            <Input id="channel-name" value={draft.name} onChange={(event) => { setDraft({ ...draft, name: event.target.value }); }} maxLength={128} data-testid="channel-name" />
          </div>
          <div className="flex items-center gap-2">
            <Checkbox
              id="channel-enabled"
              checked={draft.enabled}
              onCheckedChange={(checked) => { setDraft({ ...draft, enabled: checked }); }}
              data-testid="channel-enabled"
            />
            <Label htmlFor="channel-enabled" className="cursor-pointer">
              {t("adminNotifications.column.enabled")}
            </Label>
          </div>
          {draft.kind === "email" && (
            <>
              <div className="flex flex-col gap-2">
                <Label htmlFor="channel-host">{t("adminNotifications.field.host")}</Label>
                <Input
                  id="channel-host"
                  value={draft.host}
                  placeholder={t("adminNotifications.field.hostPlaceholder")}
                  onChange={(event) => { setDraft({ ...draft, host: event.target.value }); }}
                  data-testid="channel-host"
                />
                <p className="text-muted-foreground text-xs">{t("adminNotifications.field.hostHint")}</p>
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="channel-port">{t("adminNotifications.field.port")}</Label>
                <NumberInput id="channel-port" min={1} max={65535} value={draft.port} onChange={(event) => { setDraft({ ...draft, port: Number(event.target.value) }); }} data-testid="channel-port" />
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="channel-tls">{t("adminNotifications.field.tls")}</Label>
                <Select
                  value={draft.tlsMode}
                  items={tlsLabels}
                  onValueChange={(value) => {
                    if (value !== "required" && value !== "starttls") return;
                    setDraft({ ...draft, tlsMode: value });
                  }}
                >
                  <SelectTrigger
                    id="channel-tls"
                    className="w-full"
                    data-testid="channel-tls"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="required">{tlsLabels.required}</SelectItem>
                    <SelectItem value="starttls">{tlsLabels.starttls}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="channel-username">{t("adminNotifications.field.username")}</Label>
                <Input
                  id="channel-username"
                  value={draft.username}
                  placeholder={t("adminNotifications.field.usernamePlaceholder")}
                  onChange={(event) => { setDraft({ ...draft, username: event.target.value }); }}
                  data-testid="channel-username"
                />
                <p className="text-muted-foreground text-xs">{t("adminNotifications.field.usernameHint")}</p>
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="channel-from">{t("adminNotifications.field.from")}</Label>
                <Input
                  id="channel-from"
                  type="email"
                  value={draft.fromAddress}
                  placeholder={t("adminNotifications.field.fromPlaceholder")}
                  onChange={(event) => { setDraft({ ...draft, fromAddress: event.target.value }); }}
                  data-testid="channel-from"
                />
                <p className="text-muted-foreground text-xs">{t("adminNotifications.field.fromHint")}</p>
              </div>
            </>
          )}
          {draft.kind === "dingtalk" || draft.kind === "feishu" ? (
            // DingTalk/Feishu credential = robot Webhook URL + optional signing secret,
            // rendered as two separate inputs and wrapped into the backend envelope on submit.
            <>
              <ChannelSecretField
                id="channel-webhook"
                label={t("adminNotifications.field.webhook")}
                value={draft.secret}
                onChange={(secret) => { setDraft({ ...draft, secret }); }}
                testId="channel-webhook"
                hint={t(
                  draft.kind === "feishu"
                    ? editing === null ? "adminNotifications.field.feishuWebhookCreateHint" : "adminNotifications.field.feishuWebhookEditHint"
                    : editing === null ? "adminNotifications.field.webhookCreateHint" : "adminNotifications.field.webhookEditHint",
                )}
              />
              <ChannelSecretField
                id="channel-sign-secret"
                label={t(draft.kind === "feishu" ? "adminNotifications.field.feishuSignSecret" : "adminNotifications.field.signSecret")}
                value={draft.signSecret}
                onChange={(signSecret) => { setDraft({ ...draft, signSecret }); }}
                testId="channel-sign-secret"
                hint={t(draft.kind === "feishu" ? "adminNotifications.field.feishuSignSecretHint" : "adminNotifications.field.signSecretHint")}
              />
            </>
          ) : (
            <ChannelSecretField
              id="channel-secret"
              label={t(draft.kind === "slack" ? "adminNotifications.field.webhook" : "adminNotifications.field.smtpPassword")}
              value={draft.secret}
              onChange={(secret) => { setDraft({ ...draft, secret }); }}
              testId="channel-secret"
              hint={t(
                draft.kind === "slack"
                  ? editing === null ? "adminNotifications.field.slackWebhookCreateHint" : "adminNotifications.field.slackWebhookEditHint"
                  : editing === null ? "adminNotifications.field.smtpPasswordCreateHint" : "adminNotifications.field.smtpPasswordEditHint",
              )}
            />
          )}
          {errorText !== null && (
            <Alert variant="destructive" data-testid="channel-form-error">
              <AlertTitle>{t("adminNotifications.formFailed")}</AlertTitle>
              <AlertDescription>{errorText}</AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <PendingButton
            pending={createChannel.isPending || replaceChannel.isPending}
            disabled={draft.name.trim() === ""}
            onClick={() => void submit()}
            data-testid="channel-form-submit"
          >
            {t("common.save")}
          </PendingButton>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
