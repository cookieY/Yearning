import { HttpResponse } from "msw";
import type { DefaultBodyType } from "msw";
import type { Task } from "@/api/generated/client/yearningV4HTTPAPI.schemas";
import { readStoredAuthBehavior } from "@/shared/mock/auth-scenario-store";
import { effectiveCanAccessAdmin } from "@/shared/mock/identity-store";
import {
  createMockResponder,
  fixtureUuid as uuid,
  nowIsoSeconds as now,
  parseIfMatch,
} from "@/shared/mock/kit";
import {
  type PURPOSES,
  TASK_QUEUED_TO_RUNNING_MS,
  TASK_RUNNING_TO_DONE_MS,
} from "./ids";
import type { AdminWorld, FixtureAdminTask } from "./types";

declare global {
  interface Window {
    /** Test-only delay for the mock datasource list, set from Playwright. */
    __YEARNING_MOCK_LATENCY__?: number;
  }
}

export function mockLatencyMs(): number {
  const raw = typeof window === "undefined" ? 0 : window.__YEARNING_MOCK_LATENCY__;
  return typeof raw === "number" && Number.isFinite(raw) && raw > 0 ? raw : 0;
}

export const sleep = (ms: number): Promise<void> => new Promise((resolve) => setTimeout(resolve, ms));

const ADMIN_REQUEST_ID = "45454545-4545-4545-8545-454545454545";
export const {
  successEnvelope,
  businessErrorResponse: businessError,
  problem,
  unauthenticated,
  pageOf,
} = createMockResponder(ADMIN_REQUEST_ID, null);

export const world: AdminWorld = {
  datasources: new Map(),
  providers: new Map(),
  promptTools: new Map(),
  knowledgeEntries: new Map(),
  ruleSets: new Map(),
  flows: new Map(),
  settings: new Map(),
  revisions: new Map(),
  tasks: new Map(),
  rateVersions: new Map(),
  impactTokens: new Map(),
  users: new Map(),
  groups: new Map(),
  maskingRules: new Map(),
  announcementRevisions: new Map(),
  announcementPublication: {
    revision_id: null,
    version: 1,
    published_by: null,
    published_at: null,
  },
  auditEvents: [],
  identityProviders: new Map(),
  notificationChannels: new Map(),
  notificationDeliveries: [],
  notificationRules: new Map(),
  migrationRun: null,
};

export function configHash(...parts: string[]): string {
  let h1 = 0x811c9dc5;
  let h2 = 0x01000193;
  const text = parts.join("\u0000");
  for (let i = 0; i < text.length; i += 1) {
    h1 = (h1 ^ text.charCodeAt(i)) * 0x01000193;
    h2 = (h2 + text.charCodeAt(i) * (i + 7)) * 0x85ebca6b;
    h1 >>>= 0;
    h2 >>>= 0;
  }
  return (h1 >>> 0).toString(16).padStart(8, "0") + (h2 >>> 0).toString(16).padStart(8, "0");
}

export function sha256Hex(input: string): string {
  let h1 = 0x811c9dc5;
  let h2 = 0x01000193;
  for (let i = 0; i < input.length; i += 1) {
    h1 = ((h1 ^ input.charCodeAt(i)) * 16777619) >>> 0;
    h2 = ((((h2 + input.charCodeAt(i) * 31) >>> 0) ^ (h1 << 3)) >>> 0);
  }
  return `${h1.toString(16).padStart(8, "0")}${h2.toString(16).padStart(8, "0")}`.repeat(4);
}

export function sanitizeMarkdown(source: string): string {
  const escapeHtml = (value: string) =>
    value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  const inline = (value: string) =>
    escapeHtml(value)
      .replace(
        /\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g,
        '<a href="$2" rel="noreferrer noopener" target="_blank">$1</a>',
      )
      .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
      .replace(/`([^`]+)`/g, "<code>$1</code>");
  const headingLevel = (line: string) =>
    Math.min(3, Math.max(2, (line.match(/^#+/)?.[0].length ?? 1) + 1));
  return source
    .split(/\n\n+/)
    .map((block) => {
      const lines = block.split("\n").filter((line) => line.trim() !== "");
      if (lines.length === 0) return "";
      if (lines.every((line) => line.startsWith("#"))) {
        const level = headingLevel(lines[0] ?? "");
        const text = lines.join(" ").replace(/^#+\s*/, "");
        return `<h${String(level)}>${escapeHtml(text)}</h${String(level)}>`;
      }
      if (lines.every((line) => /^[-*]\s+/.test(line))) {
        const items = lines
          .map((line) => `<li>${inline(line.replace(/^[-*]\s+/, ""))}</li>`)
          .join("");
        return `<ul>${items}</ul>`;
      }
      if (lines.every((line) => /^\d+\.\s+/.test(line))) {
        const items = lines
          .map((line) => `<li>${inline(line.replace(/^\d+\.\s+/, ""))}</li>`)
          .join("");
        return `<ol>${items}</ol>`;
      }
      if (lines.every((line) => line.startsWith(">"))) {
        const text = lines.map((line) => line.replace(/^>\s?/, "")).join(" ");
        return `<blockquote><p>${inline(text)}</p></blockquote>`;
      }
      if (lines.every((line) => /^(-{3,}|\*{3,})$/.test(line.trim()))) {
        return "<hr />";
      }
      return `<p>${lines.map((line) => inline(line)).join("<br />")}</p>`;
    })
    .join("\n");
}

export function canonicalJson(value: unknown): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(",")}]`;
  const entries = Object.entries(value as Record<string, unknown>).sort(([a], [b]) =>
    a < b ? -1 : a > b ? 1 : 0,
  );
  return `{${entries.map(([key, entry]) => `${JSON.stringify(key)}:${canonicalJson(entry)}`).join(",")}}`;
}

export function searchOf(url: URL): string {
  const q = url.searchParams.get("q");
  return q === null ? "" : q.trim();
}

export function matchesQ(fields: (string | null | undefined)[], q: string): boolean {
  if (q === "") return true;
  const needle = q.toLowerCase();
  return fields.some((field) => (field ?? "").toLowerCase().includes(needle));
}

export function adminGuard(request?: Request): HttpResponse<DefaultBodyType> | null {
  const behavior = readStoredAuthBehavior();
  const hasSession = behavior !== "expired";
  if (!hasSession) return problem(401, "authentication required", "no active session");
  if (!effectiveCanAccessAdmin(behavior, request)) {
    return problem(403, "permission denied", "admin capability required");
  }
  return null;
}

export function requireSession(): HttpResponse<DefaultBodyType> | null {
  const hasSession = readStoredAuthBehavior() !== "expired";
  if (!hasSession) {
    return unauthenticated();
  }
  return null;
}

export function versionMismatch(request: Request, current: number): boolean {
  const got = parseIfMatch(request);
  return got !== String(current);
}

export function taskView(task: FixtureAdminTask): Task {
  return { ...task };
}

export function advanceTask(task: FixtureAdminTask, onDone: () => void, fail = false): void {
  const target = fail ? "failed" : "succeeded";
  if (task.state === "queued") {
    setTimeout(() => {
      task.state = "running";
      task.updated_at = now();
      setTimeout(() => {
        task.state = target;
        task.updated_at = now();
        onDone();
      }, TASK_RUNNING_TO_DONE_MS);
    }, TASK_QUEUED_TO_RUNNING_MS);
  }
}

export function taskResponse(
  kind: FixtureAdminTask["kind"],
  resultRef: string,
  onDone: () => void,
  fail = false,
): HttpResponse<DefaultBodyType> {
  const task: FixtureAdminTask = {
    id: uuid(),
    kind,
    state: "queued",
    progress: { completed: 0, total: 1, unit: "checks" },
    result_ref: resultRef,
    error: null,
    created_at: now(),
    updated_at: now(),
  };
  world.tasks.set(task.id, task);
  advanceTask(task, onDone, fail);
  return HttpResponse.json(successEnvelope(taskView(task)));
}

export function adminFixtureInternals() {
  return {
    datasourceCredentials: (datasourceId: string, purpose: (typeof PURPOSES)[number]) =>
      world.datasources.get(datasourceId)?.credentials[purpose],
    providerApiKey: (providerId: string) => world.providers.get(providerId)?.apiKey,
  };
}

export function adminFixtureTask(taskId: string): FixtureAdminTask | null {
  return world.tasks.get(taskId) ?? null;
}
