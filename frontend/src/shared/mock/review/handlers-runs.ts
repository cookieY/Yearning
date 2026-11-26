import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import { adminFixtureTask } from "@/shared/mock/admin-fixture";
import { apiRoute } from "@/shared/mock/api-route";
import { currentIdentity } from "@/shared/mock/identity-store";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import {
  businessError,
  pageOf,
  runPublic,
  successEnvelope,
  world,
} from "./state";
import { FIXTURE_RAW_EVIDENCE_MARKER } from "./types";

export function reviewRunHandlers(): HttpHandler[] {
  return [
    http.get(apiRoute("/review-runs/:runId"), ({ params }) => {
      const run = world.runs.get(String(params.runId));
      if (run === undefined) return businessError(1002, "review run not found");
      return HttpResponse.json(successEnvelope(runPublic(run)));
    }),

    http.get(apiRoute("/review-runs/:runId/findings"), ({ params, request }) => {
      const runId = String(params.runId);
      if (!world.runs.has(runId)) return businessError(1002, "review run not found");
      const url = new URL(request.url);
      const limit = Number(url.searchParams.get("limit") ?? "50");
      const after = url.searchParams.get("after");
      const findings = world.findings.get(runId) ?? [];
      return HttpResponse.json(successEnvelope(pageOf(findings, limit, after)));
    }),

    http.get(apiRoute("/review-findings/:findingId/evidence"), ({ params }) => {
      const findingId = String(params.findingId);
      for (const findings of world.findings.values()) {
        const finding = findings.find((entry) => entry.id === findingId);
        if (finding !== undefined) {
          const evidence: Array<{
            id: string;
            source_kind: string;
            source_reference: string;
            fact_status: string;
            normalized_fact: Record<string, unknown>;
            has_raw_payload: boolean;
            raw_payload_expires_at: string | null;
            collected_at: string;
          }> = [];
          for (const id of finding.evidence_ids) {
            const entry = world.evidence.get(id);
            if (entry === undefined) continue;
            evidence.push({
              id: entry.id,
              source_kind: entry.source_kind,
              source_reference: entry.source_reference,
              fact_status: entry.fact_status,
              normalized_fact: entry.normalized_fact,
              has_raw_payload: entry.has_raw_payload,
              raw_payload_expires_at: entry.raw_payload_expires_at,
              collected_at: entry.collected_at,
            });
          }
          return HttpResponse.json(successEnvelope(evidence));
        }
      }
      return businessError(1002, "finding not found");
    }),

    http.post(apiRoute("/review-evidence/:evidenceId/raw-reveals"), ({ params }) => {
      const evidence = world.evidence.get(String(params.evidenceId));
      if (evidence === undefined) return businessError(1002, "evidence not found");
      if (!evidence.has_raw_payload || evidence.raw_payload_expires_at === null) {
        return businessError(1011, "raw payload is no longer retained");
      }
      if (evidence.raw_payload_expires_at < now()) {
        return businessError(1011, "raw payload retention window has passed");
      }
      return HttpResponse.json(
        successEnvelope({
          reveal_id: uuid(),
          content_type: "application/json",
          raw_payload: {
            tool: "built-in:mysql.table_stats",
            query: "SHOW TABLE STATUS LIKE 'orders'",
            rows: [{ name: "orders", rows: 128400, index_length: 4587520 }],
            note: FIXTURE_RAW_EVIDENCE_MARKER,
          },
          watermark: `${currentIdentity().username} · ${now()} · evidence:${evidence.id}`,
          valid_until: new Date(Date.now() + 10 * 60 * 1000).toISOString().replace(/\.\d{3}Z$/, "Z"),
        }),
      );
    }),

    http.post(apiRoute("/review-evidence/:evidenceId/raw-copy-events"), () => {
      return HttpResponse.json(successEnvelope(null));
    }),

    http.get(apiRoute("/tasks/:taskId"), ({ params }) => {
      const task = world.tasks.get(String(params.taskId));
      if (task !== undefined) return HttpResponse.json(successEnvelope({ ...task }));
      const adminTask = adminFixtureTask(String(params.taskId));
      if (adminTask !== null) return HttpResponse.json(successEnvelope({ ...adminTask }));
      return businessError(1002, "task not found");
    }),
  ];
}
