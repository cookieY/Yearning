import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import { apiRoute } from "@/shared/mock/api-route";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import { migrationRunView } from "./helpers";
import {
  adminGuard,
  businessError,
  pageOf,
  successEnvelope,
  versionMismatch,
  world,
} from "./state";
import type { FixtureNotificationRule } from "./types";

export function adminRulesMigrationsHandlers(): HttpHandler[] {
  return [
    http.get(apiRoute("/admin/notification-rules"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = [...world.notificationRules.values()].sort((a, b) =>
        a.created_at.localeCompare(b.created_at),
      );
      return HttpResponse.json(
        successEnvelope(pageOf(items, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.post(apiRoute("/admin/notification-rules"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.name !== "string" || body.name.trim() === "") {
        return businessError(1001, "validation failed");
      }
      const rawTpl = typeof body.message_template === "string" ? body.message_template.trim() : "";
      const rule: FixtureNotificationRule = {
        id: uuid(),
        name: body.name,
        enabled: body.enabled === undefined ? true : (body.enabled as boolean),
        flow_id: (body.flow_id as string | null | undefined) ?? null,
        event_type: body.event_type as FixtureNotificationRule["event_type"],
        target_kind: body.target_kind as FixtureNotificationRule["target_kind"],
        channel_id: body.channel_id as string,
        message_template: rawTpl === "" ? null : rawTpl,
        version: 1,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      };
      world.notificationRules.set(rule.id, rule);
      return HttpResponse.json(successEnvelope(rule));
    }),

    http.post(apiRoute("/admin/notification-rules/preview"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as { rule?: { message_template?: string | null } } | null;
      const customTpl = typeof body?.rule?.message_template === "string" ? body.rule.message_template.trim() : "";
      const sampleVars: Record<string, string> = {
        order_display_number: "YR-PREVIEW",
        order_no: "YR-PREVIEW",
        order_id: "00000000-0000-4000-8000-000000000001",
        order_title: "orders_index_change",
        submitter: "alice",
        submitter_username: "alice",
        submitted_at: "2026-11-10T09:00:00Z",
        order_state: "stage_approval_active",
        flow_name: "prod-standard-flow",
        stage_name: "prod-primary",
        datasource_name: "prod-mysql-01",
        approvers: "bob, carol",
        approved_by: "bob",
        approved_at: "2026-11-10T09:05:00Z",
        executors: "dba_ops",
        executed_by: "dba_ops",
        executed_at: "2026-11-10T09:10:03Z",
        execution_started_at: "2026-11-10T09:10:00Z",
        reason_code: "REVIEWED",
        order_url: "/changes/orders/00000000-0000-4000-8000-000000000001",
      };
      const rendered = customTpl === ""
        ? "\u5de5\u5355 YR-PREVIEW \u6267\u884c\u5931\u8d25\u3002"
        : customTpl.replace(/\{\{\s*([a-z_]+)\s*\}\}/g, (_, k: string) => sampleVars[k] ?? "-");
      return HttpResponse.json(successEnvelope({
        targets: [{ recipient_key: "group:preview", recipient_user_id: null, masked_address: "op***@corp.test" }],
        message_preview: rendered,
      }));
    }),

    http.get(apiRoute("/admin/notification-rules/:ruleId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const rule = world.notificationRules.get(String(params.ruleId));
      if (rule === undefined) return businessError(1002, "rule not found");
      return HttpResponse.json(successEnvelope(rule));
    }),

    http.put(apiRoute("/admin/notification-rules/:ruleId"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const existing = world.notificationRules.get(String(params.ruleId));
      if (existing === undefined) return businessError(1002, "rule not found");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "validation failed");
      const rawTpl = typeof body.message_template === "string" ? body.message_template.trim() : "";
      const updated: FixtureNotificationRule = {
        ...existing,
        name: body.name as string,
        enabled: body.enabled as boolean,
        flow_id: (body.flow_id as string | null | undefined) ?? null,
        event_type: body.event_type as FixtureNotificationRule["event_type"],
        target_kind: body.target_kind as FixtureNotificationRule["target_kind"],
        channel_id: body.channel_id as string,
        message_template: rawTpl === "" ? null : rawTpl,
        version: existing.version + 1,
        updated_at: new Date().toISOString(),
      };
      world.notificationRules.set(updated.id, updated);
      return HttpResponse.json(successEnvelope(updated));
    }),

    http.delete(apiRoute("/admin/notification-rules/:ruleId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      if (world.notificationRules.delete(String(params.ruleId))) {
        return HttpResponse.json(successEnvelope(null));
      }
      return businessError(1002, "rule not found");
    }),

    http.get(apiRoute("/admin/notification-deliveries"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = [...world.notificationDeliveries].sort((a, b) =>
        b.created_at.localeCompare(a.created_at),
      );
      return HttpResponse.json(
        successEnvelope(pageOf(items, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.get(apiRoute("/admin/migrations"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = world.migrationRun === null ? [] : [migrationRunView(world.migrationRun)];
      return HttpResponse.json(
        successEnvelope(pageOf(items, url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.get(apiRoute("/admin/migrations/:runId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      if (world.migrationRun === null || world.migrationRun.id !== String(params.runId)) {
        return businessError(1002, "migration run not found");
      }
      return HttpResponse.json(successEnvelope(migrationRunView(world.migrationRun)));
    }),

    http.put(apiRoute("/admin/migrations/:runId/candidate-mappings/:candidateId/confirmation"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const run = world.migrationRun;
      if (run === null || run.id !== String(params.runId)) {
        return businessError(1002, "migration run not found");
      }
      if (run.state !== "awaiting_confirmation") return businessError(1010, "not awaiting confirmation");
      if (versionMismatch(request, run.version)) return businessError(1004, "version mismatch");
      const candidate = run.candidates.find((row) => row.candidate_id === String(params.candidateId));
      if (candidate === undefined) return businessError(1002, "candidate not found");
      const body = (await request.json().catch(() => null)) as {
        confirmed?: boolean;
        target_definition_hash?: string;
        comment?: string;
      } | null;
      if (
        body === null || typeof body.confirmed !== "boolean" ||
        body.target_definition_hash !== candidate.target_definition_hash
      ) {
        return businessError(1001, "confirmed and matching target_definition_hash required");
      }
      candidate.confirmed = body.confirmed;
      candidate.confirmed_by = body.confirmed ? "admin" : null;
      candidate.confirmed_at = body.confirmed ? now() : null;
      run.version += 1;
      run.updated_at = now();
      return HttpResponse.json(successEnvelope(migrationRunView(run)));
    }),

    http.post(apiRoute("/admin/migrations/:runId/approval"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const run = world.migrationRun;
      if (run === null || run.id !== String(params.runId)) {
        return businessError(1002, "migration run not found");
      }
      if (versionMismatch(request, run.version)) return businessError(1004, "version mismatch");
      if (run.state === "planned" || run.state === "dry_run_running") {
        return businessError(5002, "dry run required before approval");
      }
      if (run.state !== "awaiting_confirmation") return businessError(1010, "not awaiting confirmation");
      const body = (await request.json().catch(() => null)) as {
        manifest_hash?: string;
        confirmation_phrase?: string;
      } | null;
      if (body?.confirmation_phrase !== `APPROVE ${run.id}`) {
        return businessError(1001, "confirmation phrase mismatch");
      }
      if (run.manifest_hash === null || body.manifest_hash !== run.manifest_hash) {
        return businessError(1001, "manifest hash mismatch");
      }
      if (!run.candidates.every((candidate) => candidate.confirmed)) {
        return businessError(5003, "every candidate must be confirmed first");
      }
      run.state = "approved";
      run.updated_at = now();
      return HttpResponse.json(successEnvelope(migrationRunView(run)));
    }),
  ];
}
