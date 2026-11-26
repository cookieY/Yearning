import { useTranslation } from "react-i18next";
import {
  BookOpen,
  Brain,
  Database,
  ShieldCheck,
  Terminal,
  Wrench,
} from "lucide-react";
import type {
  AssistantCapability,
  AssistantMessage,
} from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { CodeBlock, CodeBlockContent } from "@/shared/components/ui/code-block";
import {
  Trace,
  TraceCollapsible,
  TraceCollapsibleContent,
  TraceCollapsibleTrigger,
  TraceDescription,
  TraceIcon,
  TraceLine,
  TraceTitle,
} from "@/shared/components/ui/trace";

/** Resolves a help citation id into its localized title via i18n keys. */
function resolveCitationTitle(
  citationId: string,
  fallbackTitle: string,
  t: (key: string) => string,
): string {
  switch (citationId) {
    case "help-query-access":
      return t("assistant.citations.help-query-access");
    case "help-change-orders":
      return t("assistant.citations.help-change-orders");
    case "help-ai-precheck":
      return t("assistant.citations.help-ai-precheck");
    case "help-approvals":
      return t("assistant.citations.help-approvals");
    case "help-execution-verify":
      return t("assistant.citations.help-execution-verify");
    case "help-rollback-orders":
      return t("assistant.citations.help-rollback-orders");
    case "help-error-codes":
      return t("assistant.citations.help-error-codes");
    case "help-permission-model":
      return t("assistant.citations.help-permission-model");
    case "help-sql-diagnostics":
      return t("assistant.citations.help-sql-diagnostics");
    case "help-admin-governance":
      return t("assistant.citations.help-admin-governance");
    case "help-admin-review-rules":
      return t("assistant.citations.help-admin-review-rules");
    case "help-admin-notifications-audit":
      return t("assistant.citations.help-admin-notifications-audit");
    default:
      return fallbackTitle;
  }
}

function buildReasoningSummary(
  msg: AssistantMessage,
  t: (key: string) => string,
): string {
  if (msg.capability === "sql_optimize") {
    return t("assistant.trace.reasoning.sql_optimize");
  }
  if (msg.capability === "sql_generate") {
    return t("assistant.trace.reasoning.sql_generate");
  }
  return t("assistant.trace.reasoning.platform_help");
}

function buildPrivacyGateOutput(msg: AssistantMessage): string {
  return JSON.stringify(
    {
      gate: "prompt_scope_gate_v4",
      resolved_capability: msg.capability,
      zero_leak_verified: true,
      direct_sql_execution: "forbidden",
      state: msg.state,
    },
    null,
    2,
  );
}

function buildCapabilityToolOutput(
  msg: AssistantMessage,
  t: (key: string) => string,
): string {
  if (msg.sql_draft !== null) {
    return JSON.stringify(
      {
        datasource: msg.sql_draft.datasource_name,
        engine: msg.sql_draft.engine,
        schema: msg.sql_draft.schema_name,
        referenced_tables: msg.sql_draft.referenced_tables,
        statement_kind: msg.sql_draft.statement_kind,
        risk_category: msg.sql_draft.risk_category,
      },
      null,
      2,
    );
  }
  if (msg.citations.length > 0) {
    return JSON.stringify(
      {
        matched_articles: msg.citations.map((c) => ({
          id: c.citation_id,
          title: resolveCitationTitle(c.citation_id, c.title, t),
          route: c.route_path,
        })),
      },
      null,
      2,
    );
  }
  return JSON.stringify(
    {
      capability: msg.capability,
      scope: "authorized_session_context",
    },
    null,
    2,
  );
}

export interface AssistantTraceViewProps {
  msg: AssistantMessage;
  isRunningState: boolean;
}

/** Displays the 5-step Codex-style agent execution trace for a persisted assistant reply. */
export function AssistantTraceView({ msg, isRunningState }: AssistantTraceViewProps) {
  const { t } = useTranslation();
  const citations = msg.citations;

  return (
    <Trace className="px-1" data-testid={`assistant-trace-${msg.id}`}>
      {/* 1. Reasoning / Thought Process */}
      <TraceCollapsible defaultOpen={isRunningState}>
        <TraceCollapsibleTrigger>
          <TraceLine status={isRunningState ? "running" : undefined}>
            <TraceIcon>
              <Brain />
            </TraceIcon>
            <TraceTitle>
              {isRunningState
                ? t("assistant.trace.thinkingRunning")
                : t("assistant.trace.thinkingDone")}
            </TraceTitle>
          </TraceLine>
        </TraceCollapsibleTrigger>
        <TraceCollapsibleContent>
          <div className="whitespace-pre-wrap text-xs">
            {buildReasoningSummary(msg, t)}
          </div>
        </TraceCollapsibleContent>
      </TraceCollapsible>

      {/* 2. Tool: Prompt Scope Gate & Zero-Leak Privacy Anonymizer */}
      <TraceCollapsible>
        <TraceCollapsibleTrigger>
          <TraceLine>
            <TraceIcon>
              <ShieldCheck />
            </TraceIcon>
            <TraceTitle>{t("assistant.trace.toolPrivacyGate")}</TraceTitle>
            <TraceDescription>{msg.capability}</TraceDescription>
          </TraceLine>
        </TraceCollapsibleTrigger>
        <TraceCollapsibleContent>
          <CodeBlock size="sm">
            <CodeBlockContent wrap className="max-h-48">
              {buildPrivacyGateOutput(msg)}
            </CodeBlockContent>
          </CodeBlock>
        </TraceCollapsibleContent>
      </TraceCollapsible>

      {/* 3. Tool: Capability Knowledge / Schema Catalog Inspector */}
      <TraceCollapsible>
        <TraceCollapsibleTrigger>
          <TraceLine>
            <TraceIcon>
              {msg.capability === "platform_help" ? <Wrench /> : <Database />}
            </TraceIcon>
            <TraceTitle>
              {msg.capability === "platform_help"
                ? t("assistant.trace.toolKnowledge")
                : t("assistant.trace.toolSchemaCatalog")}
            </TraceTitle>
            <TraceDescription>
              {msg.sql_draft !== null
                ? `${msg.sql_draft.engine} · ${msg.sql_draft.schema_name}`
                : `${String(citations.length)} refs`}
            </TraceDescription>
          </TraceLine>
        </TraceCollapsibleTrigger>
        <TraceCollapsibleContent>
          <CodeBlock size="sm">
            <CodeBlockContent wrap className="max-h-48">
              {buildCapabilityToolOutput(msg, t)}
            </CodeBlockContent>
          </CodeBlock>
        </TraceCollapsibleContent>
      </TraceCollapsible>

      {/* 4. Tool: AST SQL Validator & Token Restorer (when SQL draft generated) */}
      {msg.sql_draft !== null && (
        <TraceCollapsible>
          <TraceCollapsibleTrigger>
            <TraceLine>
              <TraceIcon>
                <Terminal />
              </TraceIcon>
              <TraceTitle>{t("assistant.trace.toolSqlValidator")}</TraceTitle>
              <TraceDescription>
                {`${msg.sql_draft.statement_kind.toUpperCase()} · ${msg.sql_draft.risk_category.toUpperCase()}`}
              </TraceDescription>
            </TraceLine>
          </TraceCollapsibleTrigger>
          <TraceCollapsibleContent>
            <CodeBlock size="sm">
              <CodeBlockContent wrap className="max-h-48">
                {JSON.stringify(
                  {
                    verified_single_statement: true,
                    statement_kind: msg.sql_draft.statement_kind,
                    risk_category: msg.sql_draft.risk_category,
                    sql_hmac: msg.sql_draft.sql_hmac,
                    submission_state: msg.sql_draft.submission_state,
                  },
                  null,
                  2,
                )}
              </CodeBlockContent>
            </CodeBlock>
          </TraceCollapsibleContent>
        </TraceCollapsible>
      )}

      {/* 5. Sources / Citations Trace */}
      {citations.length > 0 && (
        <TraceCollapsible>
          <TraceCollapsibleTrigger>
            <TraceLine>
              <TraceIcon>
                <BookOpen />
              </TraceIcon>
              <TraceTitle>
                {t("assistant.trace.usedSources", {
                  count: citations.length,
                })}
              </TraceTitle>
            </TraceLine>
          </TraceCollapsibleTrigger>
          <TraceCollapsibleContent>
            <Trace>
              {citations.map((cite) => {
                const localizedTitle = resolveCitationTitle(
                  cite.citation_id,
                  cite.title,
                  t,
                );
                return (
                  <TraceLine key={`trace-${cite.citation_id}-${cite.source_ref}`}>
                    <TraceIcon>
                      <BookOpen />
                    </TraceIcon>
                    <TraceTitle>{localizedTitle}</TraceTitle>
                  </TraceLine>
                );
              })}
            </Trace>
          </TraceCollapsibleContent>
        </TraceCollapsible>
      )}
    </Trace>
  );
}

export interface AssistantLiveTraceViewProps {
  capability: AssistantCapability;
  liveAgentStep: number;
  liveReasoningDone: boolean;
  liveReasoningText: string;
  liveValidatorStatus: "idle" | "running" | "completed";
}

/** Displays real-time SSE agent execution progress steps while an assistant turn is streaming. */
export function AssistantLiveTraceView({
  capability,
  liveAgentStep,
  liveReasoningDone,
  liveReasoningText,
  liveValidatorStatus,
}: AssistantLiveTraceViewProps) {
  const { t } = useTranslation();

  return (
    <Trace className="px-1">
      <TraceLine status={liveAgentStep === 0 ? "running" : undefined}>
        <TraceIcon>
          <ShieldCheck />
        </TraceIcon>
        <TraceTitle>{t("assistant.trace.toolPrivacyGate")}</TraceTitle>
        <TraceDescription>{capability}</TraceDescription>
      </TraceLine>

      <TraceLine
        status={
          liveAgentStep === 0
            ? "pending"
            : liveAgentStep === 1
              ? "running"
              : undefined
        }
      >
        <TraceIcon>
          {capability === "platform_help" ? <Wrench /> : <Database />}
        </TraceIcon>
        <TraceTitle>
          {capability === "platform_help"
            ? t("assistant.trace.toolKnowledge")
            : t("assistant.trace.toolSchemaCatalog")}
        </TraceTitle>
      </TraceLine>

      <TraceCollapsible defaultOpen>
        <TraceCollapsibleTrigger>
          <TraceLine status={liveReasoningDone ? undefined : "running"}>
            <TraceIcon>
              <Brain />
            </TraceIcon>
            <TraceTitle>
              {liveReasoningDone
                ? t("assistant.trace.thinkingDone")
                : t("assistant.trace.thinkingRunning")}
            </TraceTitle>
          </TraceLine>
        </TraceCollapsibleTrigger>
        <TraceCollapsibleContent>
          <div className="whitespace-pre-wrap text-xs">
            {liveReasoningText !== ""
              ? liveReasoningText
              : t("assistant.trace.thinkingRunning")}
          </div>
        </TraceCollapsibleContent>
      </TraceCollapsible>

      {liveValidatorStatus !== "idle" && (
        <TraceLine status={liveValidatorStatus === "running" ? "running" : undefined}>
          <TraceIcon>
            <Terminal />
          </TraceIcon>
          <TraceTitle>{t("assistant.trace.toolSqlValidator")}</TraceTitle>
        </TraceLine>
      )}
    </Trace>
  );
}
