import { useTranslation } from "react-i18next";
import type {
  ExecutionWindowDaysOfWeekItem,
  OscOverrides,
  SettingsValue,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { Button } from "@/shared/components/ui/button";
import { Checkbox } from "@/shared/components/ui/checkbox";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { NumberInput } from "@/shared/components/ui/number-input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";

export type DraftSetter = (next: SettingsValue) => void;

const WEEKDAYS: readonly ExecutionWindowDaysOfWeekItem[] = [
  "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday",
];

type OscStringKey = "max_load" | "critical_load";
type OscNumericKey =
  | "chunk_size"
  | "dml_batch_size"
  | "max_lag"
  | "nice_ratio"
  | "cut_over_lock_timeout_seconds"
  | "retries"
  | "critical_load_interval_millis"
  | "critical_load_hibernate_seconds"
  | "heartbeat_interval_millis"
  | "exponential_backoff_max_interval";
type OscBooleanKey =
  | "exact_rowcount"
  | "concurrent_rowcount"
  | "ok_to_drop_table"
  | "timestamp_old_table"
  | "cut_over_exponential_backoff"
  | "attempt_instant_ddl";

export function ExecutionFields({ draft, setDraft }: { draft: SettingsValue; setDraft: DraftSetter }) {
  const { t } = useTranslation();
  if (!("restriction_enabled" in draft)) return null;

  /** Strip empty values: all keys on OscOverrides are optional, so empty strings are omitted (pattern would reject "").
   * exec captures the `in`-narrowed ExecutionSettings (parameter narrowing does not persist across closures). */
  const exec = draft;
  function setOscStringDefault(key: OscStringKey, raw: string): void {
    const trimmed = raw.trim();
    const previous: OscOverrides = exec.osc.defaults;
    const { [key]: _removed, ...rest } = previous;
    const base: OscOverrides = rest;
    if (trimmed !== "") base[key] = trimmed;
    setDraft({ ...exec, osc: { ...exec.osc, defaults: base } });
  }
  function setOscNumericDefault(key: OscNumericKey, raw: string): void {
    const trimmed = raw.trim();
    const previous: OscOverrides = exec.osc.defaults;
    const { [key]: _removed, ...rest } = previous;
    const base: OscOverrides = rest;
    const parsed = Number(trimmed);
    if (trimmed !== "" && !Number.isNaN(parsed)) base[key] = parsed;
    setDraft({ ...exec, osc: { ...exec.osc, defaults: base } });
  }
  function setOscBooleanDefault(key: OscBooleanKey, next: boolean | undefined): void {
    const previous: OscOverrides = exec.osc.defaults;
    const { [key]: _removed, ...rest } = previous;
    const base: OscOverrides = rest;
    if (next !== undefined) base[key] = next;
    setDraft({ ...exec, osc: { ...exec.osc, defaults: base } });
  }
  return (
    <>
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-2">
          <Checkbox
            id="settings-execution-restriction"
            checked={draft.restriction_enabled}
            onCheckedChange={(checked) => { setDraft({ ...draft, restriction_enabled: checked }); }}
            data-testid="settings-execution-restriction"
          />
          <Label htmlFor="settings-execution-restriction" className="cursor-pointer">
            {t("adminSettings.execution.restriction")}
          </Label>
        </div>
        <p className="text-muted-foreground text-xs">{t("adminSettings.execution.restrictionHint")}</p>
      </div>
      <div className="flex flex-col gap-2">
        <Label>{t("adminSettings.execution.windows")}</Label>
        <p className="text-muted-foreground text-xs">{t("adminSettings.execution.windowsHint")}</p>
        <div className="flex flex-col gap-1">
          {draft.windows.map((window, index) => (
            <div key={index} className="flex items-center gap-2 text-xs">
              <Input
                value={window.start_local_time}
                onChange={(event) => {
                  const windows = [...draft.windows];
                  windows[index] = { ...window, start_local_time: event.target.value };
                  setDraft({ ...draft, windows });
                }}
                className="h-8 font-mono text-xs"
                placeholder="HH:MM"
              />
              <span>→</span>
              <Input
                value={window.end_local_time}
                onChange={(event) => {
                  const windows = [...draft.windows];
                  windows[index] = { ...window, end_local_time: event.target.value };
                  setDraft({ ...draft, windows });
                }}
                className="h-8 font-mono text-xs"
                placeholder="HH:MM"
              />
              {/* Owner feedback 2026-11-05: weekdays are picked from the seven
                  fixed values (multi-select), not typed as a comma list. */}
              <Select
                multiple
                value={window.days_of_week}
                onValueChange={(next) => {
                  const picked = new Set<string>(next);
                  const windows = [...draft.windows];
                  windows[index] = {
                    ...window,
                    days_of_week: WEEKDAYS.filter((day) => picked.has(day)),
                  };
                  setDraft({ ...draft, windows });
                }}
              >
                <SelectTrigger
                  className="h-8 w-56 text-xs"
                  aria-label={t("adminSettings.execution.weekdays")}
                  data-testid={`settings-execution-weekdays-${String(index)}`}
                >
                  <SelectValue placeholder={t("adminSettings.execution.weekdays")}>
                    {(selected: string[]) =>
                      selected.length === 0
                        ? t("adminSettings.execution.weekdays")
                        : WEEKDAYS.filter((day) => selected.includes(day))
                            .map((day) => t(`adminSettings.execution.weekday.${day}`))
                            .join(t("adminSettings.execution.weekdaySeparator"))
                    }
                  </SelectValue>
                </SelectTrigger>
                <SelectContent alignItemWithTrigger={false}>
                  {WEEKDAYS.map((day) => (
                    <SelectItem key={day} value={day}>
                      {t(`adminSettings.execution.weekday.${day}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button
                size="icon"
                variant="ghost"
                onClick={() => { setDraft({ ...draft, windows: draft.windows.filter((_, i) => i !== index) }); }}
              >
                ×
              </Button>
            </div>
          ))}
          {draft.windows.length < 14 && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setDraft({
                  ...draft,
                  windows: [
                    ...draft.windows,
                    { days_of_week: ["monday"] as ExecutionWindowDaysOfWeekItem[], start_local_time: "02:00", end_local_time: "06:00" },
                  ],
                });
              }}
              data-testid="settings-execution-add-window"
            >
              {t("adminSettings.execution.addWindow")}
            </Button>
          )}
        </div>
      </div>
      <div className="flex flex-col gap-2">
        <Label>{t("adminSettings.execution.oscTool")}</Label>
        {/* §18.59: Base UI Select + expand typed parameter inputs when gh-ost is selected
            (five OscOverrides keys declared in contract; empty values are omitted). */}
        <Select
          value={draft.osc.tool}
          onValueChange={(value) => { setDraft({ ...draft, osc: { ...draft.osc, tool: value ?? "none" } }); }}
          items={{ none: "none", "gh-ost": "gh-ost" }}
        >
          <SelectTrigger
            className="h-9 w-40 text-sm"
            aria-label={t("adminSettings.execution.oscTool")}
            data-testid="settings-execution-osc"
          >
            <SelectValue placeholder="none" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">none</SelectItem>
            <SelectItem value="gh-ost">gh-ost</SelectItem>
          </SelectContent>
        </Select>
        <p className="text-muted-foreground text-xs">{t("adminSettings.execution.oscHint")}</p>
        {draft.osc.tool === "gh-ost" && (
          <div className="mt-1 flex flex-col gap-4 rounded-md border p-3" data-testid="settings-osc-defaults">
            <p className="text-muted-foreground text-xs">{t("adminSettings.execution.oscDefaultsHint")}</p>

            <div className="flex flex-col gap-2">
              <p className="text-xs font-medium text-foreground/80">{t("adminSettings.execution.oscSectionLoad")}</p>
              <div className="grid gap-3 sm:grid-cols-2">
                {([
                  { key: "max_load", labelKey: "adminSettings.execution.oscMaxLoad", placeholder: "Threads_running=50", testId: "settings-osc-max-load" },
                  { key: "critical_load", labelKey: "adminSettings.execution.oscCriticalLoad", placeholder: "Threads_running=100", testId: "settings-osc-critical-load" },
                ] as const).map((field) => (
                  <OscTextField
                    key={field.key}
                    label={t(field.labelKey)}
                    placeholder={field.placeholder}
                    value={draft.osc.defaults[field.key]}
                    onChange={(next) => { setOscStringDefault(field.key, next); }}
                    testId={field.testId}
                  />
                ))}
                {([
                  { key: "critical_load_interval_millis", labelKey: "adminSettings.execution.oscCriticalLoadInterval", testId: "settings-osc-critical-load-interval" },
                  { key: "critical_load_hibernate_seconds", labelKey: "adminSettings.execution.oscCriticalLoadHibernate", testId: "settings-osc-critical-load-hibernate" },
                ] as const).map((field) => (
                  <OscNumberField
                    key={field.key}
                    label={t(field.labelKey)}
                    value={draft.osc.defaults[field.key]}
                    onChange={(next) => { setOscNumericDefault(field.key, next); }}
                    testId={field.testId}
                  />
                ))}
              </div>
            </div>

            <div className="flex flex-col gap-2">
              <p className="text-xs font-medium text-foreground/80">{t("adminSettings.execution.oscSectionCopy")}</p>
              <div className="grid gap-3 sm:grid-cols-2">
                {([
                  { key: "chunk_size", labelKey: "adminSettings.execution.oscChunkSize", testId: "settings-osc-chunk-size" },
                  { key: "dml_batch_size", labelKey: "adminSettings.execution.oscDmlBatchSize", testId: "settings-osc-dml-batch-size" },
                  { key: "max_lag", labelKey: "adminSettings.execution.oscMaxLag", testId: "settings-osc-max-lag" },
                  { key: "nice_ratio", labelKey: "adminSettings.execution.oscNiceRatio", testId: "settings-osc-nice-ratio" },
                ] as const).map((field) => (
                  <OscNumberField
                    key={field.key}
                    label={t(field.labelKey)}
                    value={draft.osc.defaults[field.key]}
                    onChange={(next) => { setOscNumericDefault(field.key, next); }}
                    testId={field.testId}
                  />
                ))}
              </div>
            </div>

            <div className="flex flex-col gap-2">
              <p className="text-xs font-medium text-foreground/80">{t("adminSettings.execution.oscSectionCutover")}</p>
              <div className="grid gap-3 sm:grid-cols-2">
                {([
                  { key: "cut_over_lock_timeout_seconds", labelKey: "adminSettings.execution.oscCutOverTimeout", testId: "settings-osc-cut-over-timeout" },
                  { key: "retries", labelKey: "adminSettings.execution.oscRetries", testId: "settings-osc-retries" },
                  { key: "heartbeat_interval_millis", labelKey: "adminSettings.execution.oscHeartbeatInterval", testId: "settings-osc-heartbeat-interval" },
                  { key: "exponential_backoff_max_interval", labelKey: "adminSettings.execution.oscBackoffMaxInterval", testId: "settings-osc-backoff-max-interval" },
                ] as const).map((field) => (
                  <OscNumberField
                    key={field.key}
                    label={t(field.labelKey)}
                    value={draft.osc.defaults[field.key]}
                    onChange={(next) => { setOscNumericDefault(field.key, next); }}
                    testId={field.testId}
                  />
                ))}
              </div>
            </div>

            <div className="flex flex-col gap-2">
              <p className="text-xs font-medium text-foreground/80">{t("adminSettings.execution.oscSectionSwitches")}</p>
              <div className="grid gap-3 sm:grid-cols-2">
                {([
                  { key: "exact_rowcount", labelKey: "adminSettings.execution.oscExactRowcount", testId: "settings-osc-exact-rowcount" },
                  { key: "concurrent_rowcount", labelKey: "adminSettings.execution.oscConcurrentRowcount", testId: "settings-osc-concurrent-rowcount" },
                  { key: "ok_to_drop_table", labelKey: "adminSettings.execution.oscOkToDropTable", testId: "settings-osc-ok-to-drop-table" },
                  { key: "timestamp_old_table", labelKey: "adminSettings.execution.oscTimestampOldTable", testId: "settings-osc-timestamp-old-table" },
                  { key: "cut_over_exponential_backoff", labelKey: "adminSettings.execution.oscCutOverExpBackoff", testId: "settings-osc-cut-over-exp-backoff" },
                  { key: "attempt_instant_ddl", labelKey: "adminSettings.execution.oscAttemptInstantDdl", testId: "settings-osc-attempt-instant-ddl" },
                ] as const).map((field) => (
                  <OscBooleanField
                    key={field.key}
                    label={t(field.labelKey)}
                    value={draft.osc.defaults[field.key]}
                    onChange={(next) => { setOscBooleanDefault(field.key, next); }}
                    testId={field.testId}
                  />
                ))}
              </div>
            </div>
          </div>
        )}
      </div>
    </>
  );
}

function OscTextField({ label, placeholder, value, onChange, testId }: {
  label: string; placeholder: string; value: string | undefined; onChange: (next: string) => void; testId: string;
}) {
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={testId} className="text-xs">{label}</Label>
      <Input id={testId} value={value ?? ""} placeholder={placeholder} onChange={(event) => { onChange(event.target.value); }} className="h-8 font-mono text-xs" data-testid={testId} />
    </div>
  );
}

function OscNumberField({ label, value, onChange, testId }: {
  label: string; value: number | undefined; onChange: (next: string) => void; testId: string;
}) {
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={testId} className="text-xs">{label}</Label>
      <NumberInput id={testId} value={value === undefined ? "" : String(value)} onChange={(event) => { onChange(event.target.value); }} className="h-8 text-xs" data-testid={testId} />
    </div>
  );
}

function OscBooleanField({ label, value, onChange, testId }: {
  label: string; value: boolean | undefined; onChange: (next: boolean | undefined) => void; testId: string;
}) {
  const { t } = useTranslation();
  const strVal = value === true ? "true" : value === false ? "false" : "default";
  return (
    <div className="flex flex-col gap-1">
      <Label className="text-xs">{label}</Label>
      <Select
        value={strVal}
        onValueChange={(next) => {
          if (next === "true") onChange(true);
          else if (next === "false") onChange(false);
          else onChange(undefined);
        }}
        items={{
          default: t("adminSettings.execution.oscTriStateDefault"),
          true: t("adminSettings.execution.oscTriStateEnabled"),
          false: t("adminSettings.execution.oscTriStateDisabled"),
        }}
      >
        <SelectTrigger className="h-8 text-xs" aria-label={label} data-testid={testId}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="default">{t("adminSettings.execution.oscTriStateDefault")}</SelectItem>
          <SelectItem value="true">{t("adminSettings.execution.oscTriStateEnabled")}</SelectItem>
          <SelectItem value="false">{t("adminSettings.execution.oscTriStateDisabled")}</SelectItem>
        </SelectContent>
      </Select>
    </div>
  );
}
