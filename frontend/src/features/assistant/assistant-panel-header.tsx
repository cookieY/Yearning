import { useTranslation } from "react-i18next";
import type {
  AssistantCapability,
  AssistantMessage,
  AssistantSession,
  SendAssistantMessageRequest,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import type { extractAuthorizedDatasources } from "./use-assistant";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import { Input } from "@/shared/components/ui/input";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/shared/components/ui/select";
import {
  BookOpen,
  Code2,
  Maximize2,
  Minimize2,
  Plus,
  Sparkles,
  Trash2,
  Wand2,
} from "lucide-react";

/**
 * Unwraps any assistant reply that was accidentally persisted as a raw or
 * truncated JSON envelope (`{"out_of_scope":false,...,"assistant_reply":"..."}`).
 */
export function unwrapAssistantReplyContent(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed.startsWith("{") || !trimmed.includes('"assistant_reply"')) {
    return raw;
  }
  try {
    const parsed = JSON.parse(trimmed) as { assistant_reply?: unknown };
    if (
      typeof parsed.assistant_reply === "string" &&
      parsed.assistant_reply.trim() !== ""
    ) {
      return parsed.assistant_reply;
    }
  } catch {
    // Fallback scanner for truncated JSON strings
    const marker = '"assistant_reply"';
    const idx = trimmed.indexOf(marker);
    if (idx >= 0) {
      const afterKey = trimmed.slice(idx + marker.length).trimStart();
      if (afterKey.startsWith(":")) {
        const afterColon = afterKey.slice(1).trimStart();
        if (afterColon.startsWith('"')) {
          const body = afterColon.slice(1);
          let out = "";
          for (let i = 0; i < body.length; i += 1) {
            const ch = body[i];
            if (ch === '"') {
              break;
            }
            if (ch === "\\" && i + 1 < body.length) {
              const next = body[i + 1];
              if (next === "n") out += "\n";
              else if (next === "r") out += "\r";
              else if (next === "t") out += "\t";
              else if (next !== undefined) out += next;
              i += 1;
              continue;
            }
            if (ch !== undefined) {
              out += ch;
            }
          }
          if (out.trim() !== "") {
            return out;
          }
        }
      }
    }
  }
  return raw;
}

export function resolveLatestConversationTitleFallback(
  messages: readonly AssistantMessage[],
  pendingUserPayload: SendAssistantMessageRequest | null,
): string {
  if (pendingUserPayload !== null && pendingUserPayload.content.trim() !== "") {
    const cleaned = pendingUserPayload.content.replace(/\s+/g, " ").trim();
    return cleaned.length > 48 ? `${cleaned.slice(0, 48)}…` : cleaned;
  }
  const lastUserMessage = [...messages]
    .reverse()
    .find((msg) => msg.role === "user" && msg.content.trim() !== "");
  if (lastUserMessage !== undefined) {
    const cleaned = lastUserMessage.content.replace(/\s+/g, " ").trim();
    return cleaned.length > 48 ? `${cleaned.slice(0, 48)}…` : cleaned;
  }
  return "";
}

export function buildAssistantSessionSelectItems({
  sessions,
  effectiveSessionId,
  currentSession,
  latestConversationTitleFallback,
  defaultTitle,
}: {
  sessions: readonly AssistantSession[];
  effectiveSessionId: string | null;
  currentSession: AssistantSession | null;
  latestConversationTitleFallback: string;
  defaultTitle: string;
}): Record<string, string> {
  const uuidPattern =
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
  const map: Record<string, string> = {};
  for (const item of sessions) {
    const isCurrent = item.id === effectiveSessionId;
    const candidateTitle = (
      isCurrent && currentSession?.title ? currentSession.title : item.title
    ).trim();
    if (
      isCurrent &&
      latestConversationTitleFallback !== "" &&
      (candidateTitle === "" ||
        uuidPattern.test(candidateTitle) ||
        candidateTitle === defaultTitle)
    ) {
      map[item.id] = latestConversationTitleFallback;
    } else if (candidateTitle !== "" && !uuidPattern.test(candidateTitle)) {
      map[item.id] = candidateTitle;
    } else if (isCurrent && latestConversationTitleFallback !== "") {
      map[item.id] = latestConversationTitleFallback;
    } else {
      map[item.id] = defaultTitle;
    }
  }
  return map;
}

type AssistantDatasourceOption = ReturnType<typeof extractAuthorizedDatasources>[number];

export interface AssistantPanelHeaderProps {
  sessions: readonly AssistantSession[];
  effectiveSessionId: string | null;
  currentSession: AssistantSession | null;
  sessionSelectItems: Record<string, string>;
  activeSessionDisplayTitle: string;
  activeIntent: AssistantCapability;
  selectedDatasourceId: string;
  schemaName: string;
  datasourceOptions: readonly AssistantDatasourceOption[];
  datasourceSelectItems: Record<string, string>;
  isCreatePending: boolean;
  isDeletePending: boolean;
  isExpanded: boolean;
  onToggleExpand?: () => void;
  onNewSession: () => void;
  onDeleteCurrentSession: () => void;
  onSelectSession: (sessionId: string) => void;
  onSelectIntent: (intent: AssistantCapability) => void;
  onSelectDatasource: (datasourceId: string) => void;
  onSchemaNameChange: (schemaName: string) => void;
}

export function AssistantPanelHeader({
  sessions,
  effectiveSessionId,
  currentSession,
  sessionSelectItems,
  activeSessionDisplayTitle,
  activeIntent,
  selectedDatasourceId,
  schemaName,
  datasourceOptions,
  datasourceSelectItems,
  isCreatePending,
  isDeletePending,
  isExpanded,
  onToggleExpand,
  onNewSession,
  onDeleteCurrentSession,
  onSelectSession,
  onSelectIntent,
  onSelectDatasource,
  onSchemaNameChange,
}: AssistantPanelHeaderProps) {
  const { t } = useTranslation();
  return (
    <div className="border-border flex flex-col gap-2 border-b px-3.5 py-2.5">
      <div className="flex items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <Sparkles className="text-primary size-4 shrink-0" aria-hidden="true" />
          <span className="text-foreground truncate text-sm font-semibold">
            {t("assistant.panelTitle")}
          </span>
          <Badge variant="outline" className="shrink-0 text-[11px]">
            {t("assistant.readOnlyGuardBadge")}
          </Badge>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onNewSession}
            disabled={isCreatePending}
            data-testid="assistant-new-session-button"
          >
            <Plus data-icon="inline-start" aria-hidden="true" />
            {t("assistant.newSession")}
          </Button>
          {currentSession !== null && (
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              onClick={onDeleteCurrentSession}
              disabled={isDeletePending}
              aria-label={t("assistant.deleteSession")}
              data-testid="assistant-delete-session-button"
            >
              <Trash2 aria-hidden="true" />
            </Button>
          )}
          {onToggleExpand !== undefined && (
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              onClick={onToggleExpand}
              aria-label={
                isExpanded
                  ? t("assistant.collapseWindow")
                  : t("assistant.expandWindow")
              }
              title={
                isExpanded
                  ? t("assistant.collapseWindow")
                  : t("assistant.expandWindow")
              }
              data-testid="assistant-expand-toggle"
            >
              {isExpanded ? (
                <Minimize2 aria-hidden="true" />
              ) : (
                <Maximize2 aria-hidden="true" />
              )}
            </Button>
          )}
        </div>
      </div>

      {sessions.length > 0 && (
        <div className="flex items-center gap-2">
          <Select
            items={sessionSelectItems}
            value={effectiveSessionId ?? ""}
            onValueChange={(val) => {
              if (val) onSelectSession(val);
            }}
          >
            <SelectTrigger
              className="h-8 flex-1 text-xs"
              aria-label={t("assistant.historySelectLabel")}
              data-testid="assistant-session-select"
            >
              <SelectValue>{activeSessionDisplayTitle}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {sessions.map((item) => (
                  <SelectItem key={item.id} value={item.id}>
                    {sessionSelectItems[item.id] ?? item.title}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
      )}

      {/* Intent selector chips */}
      <div
        className="flex flex-wrap items-center gap-1.5"
        role="group"
        aria-label={t("assistant.panelTitle")}
      >
        <Button
          type="button"
          variant={activeIntent === "platform_help" ? "default" : "outline"}
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={() => { onSelectIntent("platform_help"); }}
          data-testid="assistant-intent-platform_help"
        >
          <BookOpen data-icon="inline-start" aria-hidden="true" />
          {t("assistant.intents.platform_help")}
        </Button>
        <Button
          type="button"
          variant={activeIntent === "sql_optimize" ? "default" : "outline"}
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={() => { onSelectIntent("sql_optimize"); }}
          data-testid="assistant-intent-sql_optimize"
        >
          <Wand2 data-icon="inline-start" aria-hidden="true" />
          {t("assistant.intents.sql_optimize")}
        </Button>
        <Button
          type="button"
          variant={activeIntent === "sql_generate" ? "default" : "outline"}
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={() => { onSelectIntent("sql_generate"); }}
          data-testid="assistant-intent-sql_generate"
        >
          <Code2 data-icon="inline-start" aria-hidden="true" />
          {t("assistant.intents.sql_generate")}
        </Button>
      </div>

      {/* Target Datasource & Schema context bar for SQL intents */}
      {(activeIntent === "sql_generate" || activeIntent === "sql_optimize") && (
        <div className="grid grid-cols-2 gap-2 pt-0.5">
          <Select
            items={datasourceSelectItems}
            value={
              selectedDatasourceId ||
              currentSession?.active_datasource_id ||
              ""
            }
            onValueChange={(val) => {
              onSelectDatasource(val ?? "");
            }}
          >
            <SelectTrigger
              className="h-8 text-xs"
              aria-label={t("assistant.targetDatasourceLabel")}
              data-testid="assistant-datasource-select"
            >
              <SelectValue
                placeholder={t("assistant.targetDatasourcePlaceholder")}
              />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {datasourceOptions.map((ds) => (
                  <SelectItem key={ds.datasourceId} value={ds.datasourceId}>
                    {ds.datasourceName}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>

          <Input
            value={schemaName || (currentSession?.active_schema_name ?? "")}
            onChange={(event) => {
              onSchemaNameChange(event.target.value);
            }}
            placeholder={t("assistant.targetSchemaPlaceholder")}
            aria-label={t("assistant.targetSchemaLabel")}
            className="h-8 text-xs"
            data-testid="assistant-schema-input"
          />
        </div>
      )}
    </div>
  );
}
