import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useVirtualizer } from "@tanstack/react-virtual";
import type {
  DigestStatementMeta,
  LocalFingerprintGroup,
  SqlDigest,
} from "@/features/review/bulk-import/sql-digest";
import { buildBulkReportBlob } from "@/features/review/bulk-import/bulk-report";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { highlightSqlLine } from "@/shared/components/ui/code-block";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/shared/components/ui/empty";
import { Tabs, TabsList, TabsTrigger } from "@/shared/components/ui/tabs";
import { TransitionPanel } from "@/shared/components/ui/transition-panel";
import { Download, TriangleAlert } from "lucide-react";

/**
 * Virtualized fingerprint-group and statement browser (frontend PRD F5
 * deliverable: virtualized fingerprint-group list, migration contract §4). Both views render
 * only the visible window via @tanstack/react-virtual, so a 100k-statement
 * draft stays interactive and the DOM never carries the full SQL
 * (acceptance gate: never render all statements). Grouping is the local shape digest — a
 * navigation aid explicitly labelled as such; the authoritative fingerprint
 * facts remain the server's run counts and findings.
 */

const PREVIEW_MAX_CHARS = 160;
const ROW_HEIGHT = 44;

function formatBytes(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${String(bytes)} B`;
}

export interface StatementBrowserProps {
  sql: string;
  digest: SqlDigest;
  /** Authoritative fingerprint-group count once a review run exists. */
  serverGroupCount: number | null;
  /** Draft ordinal to scroll to (from finding locate), keyed by nonce. */
  locate?: { ordinal: number; nonce: number } | null;
}

export function StatementBrowser({ sql, digest, serverGroupCount, locate }: StatementBrowserProps) {
  const { t } = useTranslation();
  const [view, setView] = useState<"groups" | "statements">("groups");
  const [selectedGroup, setSelectedGroup] = useState<number | null>(null);
  const [highlightIndex, setHighlightIndex] = useState<number | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);

  const statements = useMemo(
    () =>
      selectedGroup === null
        ? digest.statements
        : digest.statements.filter((statement) => statement.group === selectedGroup),
    [digest.statements, selectedGroup],
  );

  const virtualizer = useVirtualizer({
    count: view === "groups" ? digest.groups.length : statements.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 8,
    // jsdom has no layout engine; the initial rect keeps a bounded window of
    // rows measurable (and renderable) in component tests.
    initialRect: { width: 800, height: 440 },
  });

  // Finding-driven locate: scroll to the statement ordinal and highlight it.
  // The scroll is deferred to the effect after the view-switch commit — the
  // virtualizer only knows the new item count once that render landed.
  const [pendingScroll, setPendingScroll] = useState<{ index: number; nonce: number } | null>(
    null,
  );
  const jumpNonceRef = useRef(0);
  const lastLocateNonce = useRef(0);
  const lastScrollNonce = useRef(0);
  useEffect(() => {
    if (locate === null || locate === undefined || locate.nonce === lastLocateNonce.current) return;
    lastLocateNonce.current = locate.nonce;
    const target = digest.statements.find(
      (statement) => statement.index === locate.ordinal,
    );
    if (target === undefined) return;
    setView("statements");
    setSelectedGroup(null);
    setHighlightIndex(target.index);
    setPendingScroll({ index: target.index - 1, nonce: locate.nonce });
  }, [locate, digest.statements]);

  const jumpToFirstAnomaly = (): void => {
    const anomaly = digest.statements.find((statement) => statement.anomaly);
    if (anomaly === undefined) return;
    jumpNonceRef.current += 1;
    const nonce = jumpNonceRef.current;
    setView("statements");
    setSelectedGroup(null);
    setHighlightIndex(anomaly.index);
    setPendingScroll({ index: anomaly.index - 1, nonce });
  };

  // Center the pending statement in the viewport. Setting scrollTop fires a
  // scroll event (asynchronously in browsers, synchronously under the jsdom
  // test mock), and the virtualizer follows the observed offset — deterministic
  // in both environments, unlike scrolling APIs that depend on layout timing.
  useEffect(() => {
    if (pendingScroll === null || pendingScroll.nonce === lastScrollNonce.current) return;
    lastScrollNonce.current = pendingScroll.nonce;
    const element = scrollRef.current;
    if (element === null) return;
    const centered =
      pendingScroll.index * ROW_HEIGHT - element.clientHeight / 2 + ROW_HEIGHT / 2;
    element.scrollTop = Math.max(centered, 0);
  }, [pendingScroll]);

  const downloadReport = (): void => {
    const blob = buildBulkReportBlob(digest, { serverGroupCount: serverGroupCount ?? undefined });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `yearning-bulk-report-${String(digest.statementCount)}.csv`;
    anchor.click();
    URL.revokeObjectURL(url);
  };

  // Memoized so virtualizer-driven scroll re-renders do not rebuild a
  // 100k-element wrapper array per tick.
  const rows = useMemo(
    () =>
      view === "groups"
        ? digest.groups.map((group) => ({ type: "group" as const, group }))
        : statements.map((statement) => ({ type: "statement" as const, statement })),
    [view, statements, digest.groups],
  );

  return (
    <div className="flex h-full min-h-0 flex-col gap-3" data-testid="bulk-browser">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Tabs
          value={view}
          onValueChange={(next) => {
            setView(next as "groups" | "statements");
            if (next === "groups") setSelectedGroup(null);
          }}
        >
          <TabsList>
            <TabsTrigger value="groups" data-testid="bulk-groups-tab">
              {t("precheck.bulk.browser.groupsTab", { count: digest.groupCount })}
            </TabsTrigger>
            <TabsTrigger value="statements" data-testid="bulk-statements-tab">
              {t("precheck.bulk.browser.statementsTab", { count: statements.length })}
            </TabsTrigger>
          </TabsList>
        </Tabs>
        <div className="flex items-center gap-2">
          {digest.anomalyCount > 0 && (
            <Button
              variant="outline"
              size="sm"
              onClick={jumpToFirstAnomaly}
              data-testid="bulk-jump-anomaly"
            >
              <TriangleAlert className="size-3.5" aria-hidden />
              {t("precheck.bulk.browser.jumpAnomaly", { count: digest.anomalyCount })}
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={downloadReport} data-testid="bulk-report-download">
            <Download className="size-3.5" aria-hidden />
            {t("precheck.bulk.browser.report")}
          </Button>
        </div>
      </div>

      {selectedGroup !== null && (
        <p className="text-muted-foreground text-xs">
          {t("precheck.bulk.browser.filteredGroup", {
            ordinal: selectedGroup + 1,
            count: statements.length,
          })}
        </p>
      )}

      <TransitionPanel
        activeIndex={view === "groups" ? 0 : 1}
        className="flex min-h-0 flex-1 flex-col"
        panelClassName="flex min-h-0 flex-1 flex-col"
      >
        {([0, 1] as const).map((tabIdx) =>
          rows.length === 0 ? (
            <Empty key={tabIdx}>
              <EmptyHeader>
                <EmptyTitle>{t("precheck.bulk.browser.emptyTitle")}</EmptyTitle>
                <EmptyDescription>{t("precheck.bulk.browser.emptyDescription")}</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <div
              key={tabIdx}
              ref={(node) => {
                if (node !== null) {
                  scrollRef.current = node;
                }
              }}
              className="min-h-0 flex-1 overflow-auto border-t border-border/60"
              data-testid="bulk-scroll"
            >
              <div style={{ height: virtualizer.getTotalSize(), position: "relative" }}>
                {virtualizer.getVirtualItems().map((item) => {
                  const row = rows[item.index];
                  if (row === undefined) return null;
                  return row.type === "group" ? (
                    <GroupRow
                      key={item.key}
                      group={row.group}
                      top={item.start}
                      onSelect={() => {
                        setSelectedGroup(row.group.ordinal);
                        setView("statements");
                        setHighlightIndex(null);
                      }}
                    />
                  ) : (
                    <StatementRow
                      key={item.key}
                      statement={row.statement}
                      sql={sql}
                      top={item.start}
                      highlighted={highlightIndex === row.statement.index}
                    />
                  );
                })}
              </div>
            </div>
          ),
        )}
      </TransitionPanel>

      <p className="text-muted-foreground text-xs">{t("precheck.bulk.browser.localNote")}</p>
    </div>
  );
}

function GroupRow({
  group,
  top,
  onSelect,
}: {
  group: LocalFingerprintGroup;
  top: number;
  onSelect: () => void;
}) {
  const { t } = useTranslation();
  return (
    <button
      type="button"
      onClick={onSelect}
      className="hover:bg-accent absolute inset-x-0 flex items-center gap-3 border-b border-border/60 px-3 py-2 text-left text-sm"
      style={{ transform: `translateY(${String(top)}px)`, height: ROW_HEIGHT }}
      data-testid="bulk-group-row"
      data-group-ordinal={group.ordinal + 1}
      title={group.shapeKey}
    >
      <span className="text-muted-foreground w-12 shrink-0 text-xs">#{group.ordinal + 1}</span>
      <Badge variant="outline" className="shrink-0">{group.kind}</Badge>
      <span className="w-40 shrink-0 truncate">{group.table ?? "—"}</span>
      <span className="w-24 shrink-0 text-xs">
        {t("precheck.bulk.browser.groupCount", { count: group.count })}
      </span>
      <span className="text-muted-foreground truncate text-xs">
        {t("precheck.bulk.browser.groupRange", { first: group.firstIndex, last: group.lastIndex })}
      </span>
      {group.anomalyCount > 0 && (
        <Badge variant="destructive" className="ml-auto shrink-0">
          {t("precheck.bulk.browser.anomalyBadge", { count: group.anomalyCount })}
        </Badge>
      )}
    </button>
  );
}

function StatementRow({
  statement,
  sql,
  top,
  highlighted,
}: {
  statement: DigestStatementMeta;
  sql: string;
  top: number;
  highlighted: boolean;
}) {
  const { t } = useTranslation();
  // Preview is sliced lazily for the visible row only — the digest never
  // carries statement text (memory discipline: only one persistent SQL copy).
  const preview = sql
    .slice(statement.startChar, statement.startChar + Math.min(statement.chars, PREVIEW_MAX_CHARS))
    .replace(/\s+/g, " ")
    .trim();
  return (
    <div
      className={`absolute inset-x-0 flex items-center gap-3 border-b border-border/60 px-3 py-2 text-sm ${
        highlighted ? "bg-accent ring-ring ring-1" : ""
      }`}
      style={{ transform: `translateY(${String(top)}px)`, height: ROW_HEIGHT }}
      data-testid="bulk-statement-row"
      data-statement-index={statement.index}
      data-highlighted={highlighted || undefined}
    >
      <span className="text-muted-foreground w-14 shrink-0 text-xs">#{statement.index}</span>
      <Badge variant="outline" className="shrink-0">{statement.kind}</Badge>
      <span className="w-32 shrink-0 truncate text-xs">{statement.table ?? "—"}</span>
      <span className="text-muted-foreground w-20 shrink-0 text-xs">
        {formatBytes(statement.bytes)}
      </span>
      <span className="text-muted-foreground truncate font-mono text-xs">
        {highlightSqlLine(preview)}
      </span>
      {statement.anomaly && (
        <Badge variant="destructive" className="ml-auto shrink-0">
          {t("precheck.bulk.browser.anomalyBadgeShort")}
        </Badge>
      )}
    </div>
  );
}
