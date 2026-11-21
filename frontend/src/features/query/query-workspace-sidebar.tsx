import { useTranslation } from "react-i18next";
import { History } from "lucide-react";
import type { QuerySession } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type { HistoryEntry } from "@/features/query/use-query-execution-tabs";
import { MetadataTree } from "@/features/query/metadata-tree";
import { Card, CardHeader } from "@/shared/components/ui/card";
import {
  CodeBlock,
  CodeBlockActions,
  CodeBlockContent,
  CodeBlockCopyButton,
  highlightSqlLine,
} from "@/shared/components/ui/code-block";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/shared/components/ui/hover-card";
import { ScrollArea } from "@/shared/components/ui/scroll-area";
import { Tabs, TabsList, TabsTrigger } from "@/shared/components/ui/tabs";
import { TransitionPanel } from "@/shared/components/ui/transition-panel";

export function QueryWorkspaceSidebar({
  leftPanel,
  onLeftPanelChange,
  session,
  datasourceId,
  effectiveHistory,
  onSelectDatasource,
  onTableSelected,
  onInspectTable,
  onSelectHistorySql,
}: {
  leftPanel: "tree" | "history";
  onLeftPanelChange: (panel: "tree" | "history") => void;
  session: QuerySession;
  datasourceId: string;
  effectiveHistory: HistoryEntry[];
  onSelectDatasource: (datasourceId: string) => void;
  onTableSelected: (schema: string, table: string, targetDatasourceId?: string) => void;
  onInspectTable: (schema: string, table: string, x: number, y: number, targetDatasourceId?: string) => void;
  onSelectHistorySql: (sql: string) => void;
}) {
  const { t } = useTranslation();

  return (
    <Card className="flex min-h-0 min-w-0 flex-col">
      <CardHeader className="min-w-0 grid-cols-[minmax(0,1fr)] pb-2">
        <Tabs
          value={leftPanel}
          onValueChange={(value) => { onLeftPanelChange(value as "tree" | "history"); }}
          className="min-w-0 w-full"
        >
          <TabsList className="grid w-full grid-cols-2">
            <TabsTrigger value="tree" className="min-w-0 text-xs" data-testid="query-left-tab-tree">
              {t("query.workspace.treeTab")}
            </TabsTrigger>
            <TabsTrigger value="history" className="min-w-0 text-xs" data-testid="query-left-tab-history">
              <History className="size-3" />
              {t("query.workspace.historyTab")}
            </TabsTrigger>
          </TabsList>
          <TransitionPanel
            activeIndex={leftPanel === "tree" ? 0 : 1}
            keepMounted
            className="min-h-0 min-w-0"
          >
            <div className="h-[calc(100vh-18rem)] min-h-0 min-w-0">
              {session.state === "active" ? (
                <MetadataTree
                  session={session}
                  activeDatasourceId={datasourceId}
                  onSelectDatasource={onSelectDatasource}
                  onTableSelected={onTableSelected}
                  onInspectTable={onInspectTable}
                />
              ) : (
                <p className="text-muted-foreground p-3 text-xs">{t("query.workspace.treeBlocked")}</p>
              )}
            </div>
            <div className="min-w-0 overflow-hidden">
              <ScrollArea className="h-[calc(100vh-18rem)] w-full min-w-0">
                <div className="w-full min-w-0 overflow-hidden">
                  {effectiveHistory.length === 0 && (
                    <p className="text-muted-foreground p-3 text-xs">{t("query.workspace.historyEmpty")}</p>
                  )}
                  {effectiveHistory.map((entry) => (
                    <HoverCard key={entry.id}>
                      <HoverCardTrigger
                        delay={150}
                        closeDelay={100}
                        render={
                          <button
                            type="button"
                            onClick={() => { onSelectHistorySql(entry.sql); }}
                            className="hover:bg-accent block w-full max-w-full truncate rounded px-2 py-1.5 text-left font-mono text-xs"
                            data-testid={`query-history-${entry.id}`}
                          />
                        }
                      >
                        <span className={entry.failed ? "text-destructive" : ""}>
                          {entry.failed ? entry.sql : highlightSqlLine(entry.sql)}
                        </span>
                      </HoverCardTrigger>
                      <HoverCardContent
                        side="right"
                        align="start"
                        className="w-96 max-w-[min(28rem,calc(100vw-2rem))]"
                        data-testid={`query-history-hover-${entry.id}`}
                      >
                        <CodeBlock size="sm">
                          <CodeBlockActions>
                            <CodeBlockCopyButton value={entry.sql} />
                          </CodeBlockActions>
                          <CodeBlockContent wrap className="max-h-60">
                            {entry.sql}
                          </CodeBlockContent>
                        </CodeBlock>
                      </HoverCardContent>
                    </HoverCard>
                  ))}
                </div>
              </ScrollArea>
            </div>
          </TransitionPanel>
        </Tabs>
      </CardHeader>
    </Card>
  );
}
