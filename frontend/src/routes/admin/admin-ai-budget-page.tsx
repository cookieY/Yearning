import { useState } from "react";
import { useTranslation } from "react-i18next";
import { AnimatePresence, motion } from "motion/react";
import { CircleDollarSign, History, ShieldAlert } from "lucide-react";
import { Checkbox } from "@/shared/components/ui/checkbox";
import { ChartContainer, type ChartConfig } from "@/shared/components/ui/chart";
import { PolarAngleAxis, RadialBar, RadialBarChart } from "recharts";
import type {
  AiBudgetSettings,
  SettingsValue,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import {
  useAiUsageSummaryToday,
  useAssessSettingsImpact,
  useReplaceSettings,
  useSettingsNamespace,
  useSettingsRevisions,
} from "@/features/admin/use-admin";
import { useSession } from "@/features/auth/session-provider";
import { BusinessError } from "@/shared/api/mutator";
import { useCursorPage } from "@/shared/lib/cursor-page";
import { formatTimestamp } from "@/shared/lib/timezones";
import { TablePagination } from "@/shared/components/ui/table-pagination";
import { describeError } from "@/shared/api/error-display";
import { appToast } from "@/shared/components/shadcn-dashboard/sonner/sonner-08";
import { PageHeader, PageLayout } from "@/shared/components/page-layout";
import { PendingButton } from "@/shared/components/pending-button";
import { ErrorState } from "@/shared/components/status/status-components";
import { LoadingState } from "@/shared/components/status/loading-state";
import { EmptyState } from "@/shared/components/status/empty-state";
import { Alert, AlertDescription, AlertTitle } from "@/shared/components/ui/alert";
import { Badge } from "@/shared/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/shared/components/ui/card";
import { NumberInput } from "@/shared/components/ui/number-input";
import { Label } from "@/shared/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/shared/components/ui/table";

/**
 * AI Budget settings namespace (route /admin/review-engine/ai-budget — moved from
 * /admin/settings/ai-budget, owner 2026-10-31: sits more naturally next to the Review Engine Provider domain;
 * backend settings namespace API is unchanged).
 * R008 boundary made visible: only a global daily AI cost budget and a
 * provider cost alert threshold exist — no per-order token or cost budget
 * is configurable anywhere, and the page states that boundary explicitly.
 *
 * High-impact changes (toggling `enforced`) require a single-use server
 * impact_token on PUT. Rather than exposing a manual impact-assessment ceremony,
 * saving directly handles a PRECONDITION_REQUIRED (1011) response by fetching a
 * fresh impact token once and retrying the save transparently (matching
 * AdminSettingsNamespacePage). Low-impact changes save in a single PUT.
 */

const CURRENCIES = ["USD", "CNY", "EUR", "JPY", "GBP"] as const;

function formFor(settings: AiBudgetSettings): AiBudgetSettings {
  return { ...settings };
}

/** Formats integer minor currency units (1/100 of major unit) as a concise major-unit input string. */
function minorToMajorInput(minor: number): string {
  if (!Number.isFinite(minor)) return "0";
  const rounded = Math.round(minor);
  const sign = rounded < 0 ? "-" : "";
  const abs = Math.abs(rounded);
  const whole = Math.floor(abs / 100);
  const frac = abs % 100;
  if (frac === 0) return `${sign}${String(whole)}`;
  const fracStr = String(frac).padStart(2, "0").replace(/0+$/, "");
  return `${sign}${String(whole)}.${fracStr}`;
}

/** Parses a major-unit input string into bounded integer minor units (1/100 of major unit), or null when empty/invalid. */
function parseMajorToMinor(raw: string): number | null {
  const trimmed = raw.trim();
  if (trimmed === "") return null;
  const parsed = Number(trimmed);
  if (!Number.isFinite(parsed)) return null;
  return Math.min(Math.round(parsed * 100), Number.MAX_SAFE_INTEGER);
}

/** Today's usage ring chart (owner 2026-10-31 round 3, reference image: thick-stroke progress ring + centered
 * "Used / Total"). Data = S008 reference pricing daily summary (major currency units) vs
 * daily budget (minor currency units -> major currency units); no matching currency summary = honestly counted as 0. */
function BudgetUsageRing({
  usedMajor,
  budgetMajor,
  currency,
}: {
  usedMajor: number;
  budgetMajor: number;
  currency: string;
}) {
  const { t } = useTranslation();
  const budget = Math.max(budgetMajor, 0);
  const used = Math.max(usedMajor, 0);
  const pct = budget > 0 ? used / budget : 0;
  const chartConfig = { used: { color: "var(--primary)" } } satisfies ChartConfig;
  const fmt = (n: number) => (n >= 100 ? n.toFixed(0) : n.toFixed(2));
  // recharts v3: ring radii are chart-level pixel numbers (innerRadius/outerRadius accept numbers only),
  // and a fixed 300px square container keeps the ring/stroke width stable (stroke ~= outer - inner = 34px)
  // while filling the full right-column height without overflowing narrow mobile or split desktop viewports.
  // ChartContainer's default aspect-video (16:9) stretched the svg into a rectangle (pushing the centered overlay text up
  // and covering the caption). Inline style wins unconditionally, pinning 300x300; initialDimension matches
  // the same value to avoid first-measure flicker.
  return (
    <div
      className="flex h-full w-full flex-col items-center justify-center gap-4 rounded-xl border border-border/60 bg-muted/20 px-1 py-5 sm:p-6"
      data-testid="ai-budget-usage-ring"
    >
      <div className="relative size-[300px]">
        <ChartContainer
          config={chartConfig}
          className="size-[300px]"
          style={{ width: 300, height: 300, aspectRatio: "auto" }}
          initialDimension={{ width: 300, height: 300 }}
        >
          <RadialBarChart
            data={[{ used: budget > 0 ? Math.min(used, budget) : 0 }]}
            startAngle={90}
            endAngle={-270}
            innerRadius={114}
            outerRadius={148}
          >
            <PolarAngleAxis
              type="number"
              domain={[0, budget > 0 ? budget : 1]}
              tick={false}
              axisLine={false}
            />
            <RadialBar
              dataKey="used"
              background={{ className: "fill-muted" }}
              cornerRadius={17}
              className="fill-primary"
            />
          </RadialBarChart>
        </ChartContainer>
        <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center gap-1.5 px-8 text-center">
          <p className="text-3xl font-semibold tabular-nums text-foreground">
            {fmt(used)} / {fmt(budget)}
          </p>
          <p className="text-muted-foreground text-sm">
            {t("admin.aiBudget.usageLabel", { currency })} · {(pct * 100).toFixed(0)}%
          </p>
        </div>
      </div>
      <p className="text-muted-foreground max-w-sm text-center text-xs">{t("admin.aiBudget.usageHint")}</p>
    </div>
  );
}

function AiBudgetPanel({
  current,
  saving,
  onSave,
}: {
  current: {
    namespace: string;
    schema_version: number;
    settings: SettingsValue;
    version: number;
    updated_by: string;
    updated_at: string;
  };
  saving: boolean;
  onSave: (settings: AiBudgetSettings) => Promise<boolean>;
}) {
  const { t } = useTranslation();
  // Keyed by the saved version in the parent: the form re-initializes from
  // the aggregate only when a save actually lands, never mid-edit.
  const [form, setForm] = useState<AiBudgetSettings>(() => formFor(current.settings as AiBudgetSettings));
  // Keep raw major-unit draft strings for the three currency fields so typing decimals (e.g. "1.05")
  // never loses intermediate ".0" characters while converting to integer minor units for the API contract.
  const [inputPriceText, setInputPriceText] = useState<string>(() =>
    minorToMajorInput((current.settings as AiBudgetSettings).input_price_per_million_minor),
  );
  const [outputPriceText, setOutputPriceText] = useState<string>(() =>
    minorToMajorInput((current.settings as AiBudgetSettings).output_price_per_million_minor),
  );
  const [dailyBudgetText, setDailyBudgetText] = useState<string>(() =>
    minorToMajorInput((current.settings as AiBudgetSettings).daily_budget_minor),
  );
  // Today's reference usage (S008 summary); counted only when currency matches the budget, otherwise 0.
  // reference_amount is a fixed-point decimal string in major currency units (do not mix with the minor-unit
  // daily_budget_minor: the ring chart displays major units, dividing the budget side by 100 separately).
  const usageQuery = useAiUsageSummaryToday(true);
  const usageRow = usageQuery.data?.find((row) => row.currency === form.currency) ?? null;
  const usedMajor =
    usageRow !== null && usageRow.reference_amount !== "" ? Number(usageRow.reference_amount) : 0;

  const dirty = JSON.stringify(form) !== JSON.stringify(current.settings);

  return (
    <div className="flex flex-col gap-4">
      <Alert data-testid="ai-budget-r008-note">
        <ShieldAlert />
        <AlertTitle>{t("admin.aiBudget.r008Title")}</AlertTitle>
        <AlertDescription>{t("admin.aiBudget.r008Body")}</AlertDescription>
      </Alert>
      <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
        <div className="flex items-center gap-2 md:col-span-2">
          <Checkbox
            id="ai-budget-enforced"
            checked={form.enforced}
            onCheckedChange={(checked) => { setForm({ ...form, enforced: checked }); }}
            data-testid="ai-budget-enforced"
          />
          <Label htmlFor="ai-budget-enforced" className="cursor-pointer">
            {t("admin.aiBudget.enforced")}
          </Label>
          <p className="text-muted-foreground text-xs">{t("admin.aiBudget.enforcedHint")}</p>
        </div>
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-1">
            <Label>{t("admin.aiBudget.currency")}</Label>
            <Select
              value={form.currency}
              onValueChange={(next) => {
                if (next === "" || next === null) return;
                setForm({ ...form, currency: next });
              }}
            >
              <SelectTrigger data-testid="ai-budget-currency">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CURRENCIES.map((currency) => (
                  <SelectItem key={currency} value={currency}>
                    {currency}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="ai-budget-alert">{t("admin.aiBudget.alertThreshold")}</Label>
            <NumberInput
              id="ai-budget-alert"
              min={1}
              max={100}
              value={form.alert_threshold_percent}
              onChange={(event) => {
                setForm({ ...form, alert_threshold_percent: Number(event.target.value) });
              }}
              data-testid="ai-budget-alert"
            />
            <p className="text-muted-foreground text-xs">{t("admin.aiBudget.alertHint")}</p>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="ai-budget-input-price">
              {t("admin.aiBudget.inputPrice", { currency: form.currency })}
            </Label>
            <NumberInput
              id="ai-budget-input-price"
              min={0}
              step={0.01}
              value={inputPriceText}
              onChange={(event) => {
                const raw = event.target.value;
                setInputPriceText(raw);
                const minor = parseMajorToMinor(raw);
                setForm((prev) => ({
                  ...prev,
                  input_price_per_million_minor: minor !== null && minor >= 0 ? minor : 0,
                }));
              }}
              onBlur={() => {
                setInputPriceText(minorToMajorInput(form.input_price_per_million_minor));
              }}
              data-testid="ai-budget-input-price"
            />
            <p className="text-muted-foreground text-xs">{t("admin.aiBudget.inputPriceHint")}</p>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="ai-budget-output-price">
              {t("admin.aiBudget.outputPrice", { currency: form.currency })}
            </Label>
            <NumberInput
              id="ai-budget-output-price"
              min={0}
              step={0.01}
              value={outputPriceText}
              onChange={(event) => {
                const raw = event.target.value;
                setOutputPriceText(raw);
                const minor = parseMajorToMinor(raw);
                setForm((prev) => ({
                  ...prev,
                  output_price_per_million_minor: minor !== null && minor >= 0 ? minor : 0,
                }));
              }}
              onBlur={() => {
                setOutputPriceText(minorToMajorInput(form.output_price_per_million_minor));
              }}
              data-testid="ai-budget-output-price"
            />
            <p className="text-muted-foreground text-xs">{t("admin.aiBudget.outputPriceHint")}</p>
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="ai-budget-daily">
              {t("admin.aiBudget.dailyBudget", { currency: form.currency })}
            </Label>
            <NumberInput
              id="ai-budget-daily"
              min={0.01}
              step={0.01}
              value={dailyBudgetText}
              onChange={(event) => {
                const raw = event.target.value;
                setDailyBudgetText(raw);
                const minor = parseMajorToMinor(raw);
                if (minor !== null && minor >= 1) {
                  setForm((prev) => ({ ...prev, daily_budget_minor: minor }));
                }
              }}
              onBlur={() => {
                const minor = parseMajorToMinor(dailyBudgetText);
                if (minor !== null && minor < 1) {
                  setForm((prev) => ({ ...prev, daily_budget_minor: 1 }));
                  setDailyBudgetText(minorToMajorInput(1));
                } else {
                  setDailyBudgetText(minorToMajorInput(form.daily_budget_minor));
                }
              }}
              data-testid="ai-budget-daily"
            />
            <p className="text-muted-foreground text-xs">{t("admin.aiBudget.minorUnitsHint")}</p>
          </div>
        </div>

        {/* Full-height right-column slot: enlarged usage ring aligned with all five left-column fields. */}
        <div className="flex h-full items-stretch">
          <BudgetUsageRing
            usedMajor={usedMajor}
            budgetMajor={form.daily_budget_minor / 100}
            currency={form.currency}
          />
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <PendingButton
          onClick={() => { void onSave(form); }}
          pending={saving}
          disabled={!dirty || saving}
          data-testid="ai-budget-save"
        >
          {t("common.save")}
        </PendingButton>
      </div>
    </div>
  );
}

export default function AdminAiBudgetPage() {
  const { t } = useTranslation();
  const session = useSession();
  const enabled = session.user?.can_access_admin === true;
  const namespace = "ai-budget" as const;
  const query = useSettingsNamespace(namespace, enabled);
  // §18.82: revision history uses backend cursor pagination (default 20 rows/page).
  const revisionsPager = useCursorPage(20);
  const revisionsQuery = useSettingsRevisions(namespace, enabled, {
    limit: revisionsPager.pageSize,
    after: revisionsPager.after,
  });
  // §18.82: capture before the exhaustive ternary so narrowing does not leak into pagination guards.
  const revisionsLoaded = !revisionsQuery.isPending && !revisionsQuery.isError;

  const assessMutation = useAssessSettingsImpact();
  const replaceMutation = useReplaceSettings();
  const saving = replaceMutation.isPending || assessMutation.isPending;
  const [savedTick, setSavedTick] = useState(0);
  const [saveErrorKey, setSaveErrorKey] = useState<string | null>(null);
  const [saveErrorRequestId, setSaveErrorRequestId] = useState<string | null>(null);

  const save = async (settings: AiBudgetSettings): Promise<boolean> => {
    setSaveErrorKey(null);
    setSaveErrorRequestId(null);
    if (query.data === undefined) return false;
    const version = query.data.version;
    const commit = async (impactToken: string | null): Promise<void> => {
      await replaceMutation.mutateAsync({
        namespace,
        version,
        settings,
        impactToken,
      });
    };
    try {
      await commit(null);
    } catch (error) {
      // High-impact changes (toggling `enforced`) need a one-time impact token.
      // Transparently run one assess -> retry cycle so the user saves in one click.
      if (error instanceof BusinessError && error.err_code === 1011) {
        let phase: "assessSettingsImpact" | "replaceSettingsNamespace" = "assessSettingsImpact";
        try {
          const assessment = await assessMutation.mutateAsync({ namespace, settings });
          phase = "replaceSettingsNamespace";
          await commit(assessment.impact_token);
        } catch (retryError) {
          const display = describeError(retryError, phase);
          setSaveErrorKey(display.messageKey);
          setSaveErrorRequestId(display.requestId);
          appToast.error(t(display.messageKey));
          return false;
        }
      } else {
        const display = describeError(error, "replaceSettingsNamespace");
        setSaveErrorKey(display.messageKey);
        setSaveErrorRequestId(display.requestId);
        appToast.error(t(display.messageKey));
        return false;
      }
    }
    setSavedTick((tick) => tick + 1);
    appToast.success(t("common.save"));
    return true;
  };

  return (
    <PageLayout data-testid="admin-settings-ai-budget">
      <PageHeader
        title={t("admin.aiBudget.title")}
        description={t("admin.aiBudget.description")}
        icon={CircleDollarSign}
        actions={
          query.data !== undefined ? (
            <Badge variant="outline" data-testid="ai-budget-version">
              {t("admin.aiBudget.version", { version: query.data.version })}
            </Badge>
          ) : undefined
        }
      />
      {/* Same animation as the four settings-namespace pages (owner ruling 2026-10-31 round 2: consistent switching
          across all six settings tabs) — AnimatePresence fade/rise, including exit. */}
      <AnimatePresence mode="wait">
        <motion.div
          key="ai-budget"
          initial={{ opacity: 0, y: 10 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: -6 }}
          transition={{ duration: 0.16, ease: "easeOut" }}
          className="flex flex-col gap-5"
        >
      <Card>
        {query.isPending ? (
          <CardContent>
            <LoadingState variant="panel" />
          </CardContent>
        ) : query.isError ? (
          <CardContent>
            <ErrorState
              error={query.error}
              operationId="getSettingsNamespace"
              onRetry={() => void query.refetch()}
            />
          </CardContent>
        ) : (
          <CardContent className="flex flex-col gap-4">
            <AiBudgetPanel
              key={query.data.version}
              current={query.data}
              saving={saving}
              onSave={save}
            />
            {saveErrorKey !== null ? (
              <Alert variant="destructive" data-testid="ai-budget-error">
                <AlertTitle>{t(saveErrorKey)}</AlertTitle>
                {saveErrorRequestId !== null ? (
                  <AlertDescription>{`request_id: ${saveErrorRequestId}`}</AlertDescription>
                ) : null}
              </Alert>
            ) : null}
            {savedTick > 0 && saveErrorKey === null ? (
              <p className="text-[var(--risk-safe)]" data-testid="ai-budget-saved">
                {t("admin.aiBudget.saved")}
              </p>
            ) : null}
          </CardContent>
        )}
      </Card>
      <Card>
        <CardHeader className="border-b">
          <CardTitle className="flex items-center gap-2">
            <History className="size-4" /> {t("admin.aiBudget.revisionsTitle")}
          </CardTitle>
          <CardDescription>{t("admin.aiBudget.revisionsDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="px-0!">
          {revisionsQuery.isPending ? (
            <LoadingState variant="table" columns={4} />
          ) : revisionsQuery.isError ? (
            <ErrorState
              error={revisionsQuery.error}
              operationId="listSettingsRevisions"
              onRetry={() => void revisionsQuery.refetch()}
            />
          ) : revisionsQuery.data.items.length === 0 ? (
            <EmptyState title={t("admin.aiBudget.revisionsEmpty")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("admin.aiBudget.revisionVersion")}</TableHead>
                  <TableHead>{t("admin.aiBudget.revisionChangedBy")}</TableHead>
                  <TableHead>{t("admin.aiBudget.revisionChangedAt")}</TableHead>
                  <TableHead>{t("admin.aiBudget.revisionPaths")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody stale={revisionsQuery.isFetching && revisionsQuery.isPlaceholderData}>
                {revisionsQuery.data.items.map((revision) => (
                  <TableRow key={revision.version} data-testid={`ai-budget-revision-${String(revision.version)}`}>
                    <TableCell className="font-semibold tabular-nums text-foreground">v{revision.version}</TableCell>
                    <TableCell className="font-mono">{revision.changed_by}</TableCell>
                    <TableCell>{formatTimestamp(revision.changed_at)}</TableCell>
                    <TableCell className="max-w-72 truncate font-mono">{revision.changed_paths.join(", ")}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
          {revisionsLoaded ? (
            <TablePagination
              busy={revisionsQuery.isFetching && revisionsQuery.isPlaceholderData}
              page={revisionsPager.pageNo}
              hasMore={revisionsQuery.data.page.has_more}
              isFirst={revisionsPager.isFirst}
              onPrev={revisionsPager.goPrev}
          onJumpPage={revisionsPager.goToPage}
              onNext={() => { revisionsPager.pushCursor(revisionsQuery.data.page.next_cursor); }}
              pageSize={revisionsPager.pageSize}
              onPageSizeChange={revisionsPager.setPageSize}
              rowsOnPage={revisionsQuery.data.items.length}
              testIdPrefix="ai-budget-revisions"
            />
          ) : null}
        </CardContent>
      </Card>
      </motion.div>
      </AnimatePresence>
    </PageLayout>
  );
}

