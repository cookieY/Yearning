import { useTranslation } from "react-i18next";
import type { SettingsValue } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type { DraftSetter } from "@/routes/admin/admin-settings-execution-fields";
import { Alert, AlertDescription } from "@/shared/components/ui/alert";
import { Checkbox } from "@/shared/components/ui/checkbox";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { NumberInput } from "@/shared/components/ui/number-input";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import { timezoneOptions } from "@/shared/lib/timezones";
import { DEFAULT_PRODUCT_NAME } from "@/shared/lib/branding";

/** F-SET-001: structural timezone validation (matching backend structural rules — non-empty segments, character set,
 * not "Local", length; whether the IANA zone name actually exists is authoritatively decided and surfaced by the server). */
export function timezoneStructureOk(value: string): boolean {
  if (value === "" || value.length > 64 || value === "Local") return false;
  if (value.split("/").some((segment) => segment === "")) return false;
  return /^[A-Za-z0-9_+/-]+$/.test(value);
}

/** `SettingsValue` is a five-namespace union type; the general panel guard only reads
 * the timezone field when namespace==="general", safely narrowing via the `in` operator. */
export function generalTimezoneOf(draft: SettingsValue): string {
  return "system_timezone" in draft ? draft.system_timezone : "";
}

export function GeneralFields({ draft, setDraft }: { draft: SettingsValue; setDraft: DraftSetter }) {
  const { t } = useTranslation();
  if (!("registration_enabled" in draft)) return null;
  return (
    <>
      <div className="flex items-center gap-2">
        <Checkbox
          id="settings-general-registration"
          checked={draft.registration_enabled}
          onCheckedChange={(checked) => { setDraft({ ...draft, registration_enabled: checked }); }}
          data-testid="settings-general-registration"
        />
        <Label htmlFor="settings-general-registration" className="cursor-pointer">
          {t("adminSettings.general.registration")}
        </Label>
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-general-timezone">{t("adminSettings.general.timezone")}</Label>
        {/* Owner feedback 2026-11-05: the timezone is picked, not typed. */}
        <Select
          value={draft.system_timezone}
          onValueChange={(value) => {
            if (typeof value !== "string") return;
            setDraft({ ...draft, system_timezone: value });
          }}
        >
          <SelectTrigger
            id="settings-general-timezone"
            className="w-72"
            aria-invalid={!timezoneStructureOk(draft.system_timezone)}
            data-testid="settings-general-timezone"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent alignItemWithTrigger={false}>
            <ScrollArea viewportClassName="max-h-60">
              {timezoneOptions(draft.system_timezone).map((zone) => (
                <SelectItem key={zone} value={zone}>
                  {zone}
                </SelectItem>
              ))}
            </ScrollArea>
          </SelectContent>
        </Select>
        {!timezoneStructureOk(draft.system_timezone) && (
          <p className="text-destructive text-xs" data-testid="settings-general-timezone-error">
            {t("adminSettings.general.timezoneInvalid")}
          </p>
        )}
      </div>
    </>
  );
}

export function QueryFields({ draft, setDraft }: { draft: SettingsValue; setDraft: DraftSetter }) {
  const { t } = useTranslation();
  if (!("approval_enabled" in draft)) return null;
  return (
    <>
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-2">
          <Checkbox
            id="settings-query-approval"
            checked={draft.approval_enabled}
            onCheckedChange={(checked) => { setDraft({ ...draft, approval_enabled: checked }); }}
            data-testid="settings-query-approval"
          />
          <Label htmlFor="settings-query-approval" className="cursor-pointer">
            {t("adminSettings.query.approval")}
          </Label>
        </div>
        <p className="text-muted-foreground text-xs">{t("adminSettings.query.approvalHint")}</p>
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-query-timeout">{t("adminSettings.query.timeout")}</Label>
        <NumberInput
          id="settings-query-timeout"
          min={1}
          max={300000}
          value={draft.default_timeout_ms}
          onChange={(event) => { setDraft({ ...draft, default_timeout_ms: Number(event.target.value) }); }}
          data-testid="settings-query-timeout"
        />
        <p className="text-muted-foreground text-xs">{t("adminSettings.query.timeoutHint")}</p>
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-query-duration">{t("adminSettings.query.maxDuration")}</Label>
        <NumberInput
          id="settings-query-duration"
          min={1}
          max={525600}
          value={draft.maximum_approved_access_duration_minutes}
          onChange={(event) => { setDraft({ ...draft, maximum_approved_access_duration_minutes: Number(event.target.value) }); }}
          data-testid="settings-query-duration"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-query-max-rows">{t("adminSettings.query.maxResultRows")}</Label>
        <NumberInput
          id="settings-query-max-rows"
          min={1}
          max={100000}
          value={draft.max_result_rows}
          onChange={(event) => { setDraft({ ...draft, max_result_rows: Number(event.target.value) }); }}
          data-testid="settings-query-max-rows"
        />
        <p className="text-muted-foreground text-xs">{t("adminSettings.query.maxResultRowsHint")}</p>
      </div>
      <Alert>
        <AlertDescription data-testid="settings-query-no-limit">{t("adminSettings.query.noLimitNote")}</AlertDescription>
      </Alert>
    </>
  );
}

export function BrandingFields({ draft, setDraft }: { draft: SettingsValue; setDraft: DraftSetter }) {
  const { t } = useTranslation();
  if (!("product_name" in draft)) return null;
  return (
    <>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-branding-name">{t("adminSettings.branding.productName")}</Label>
        <Input
          id="settings-branding-name"
          value={draft.product_name}
          placeholder={DEFAULT_PRODUCT_NAME}
          onChange={(event) => { setDraft({ ...draft, product_name: event.target.value }); }}
          maxLength={64}
          data-testid="settings-branding-name"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor="settings-branding-description">{t("adminSettings.branding.loginDescription")}</Label>
        <Input
          id="settings-branding-description"
          value={draft.login_description}
          onChange={(event) => { setDraft({ ...draft, login_description: event.target.value }); }}
          maxLength={500}
          data-testid="settings-branding-description"
        />
      </div>
      <p className="text-muted-foreground text-xs">{t("adminSettings.branding.assetsNote")}</p>
    </>
  );
}
