import { HttpResponse, http } from "msw";
import type { HttpHandler } from "msw";
import { apiRoute } from "@/shared/mock/api-route";
import { fixtureUuid as uuid, nowIsoSeconds as now } from "@/shared/mock/kit";
import {
  ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
  TASK_RUNNING_TO_DONE_MS,
} from "./ids";
import {
  adminGuard,
  advanceTask,
  businessError,
  pageOf,
  successEnvelope,
  versionMismatch,
  world,
} from "./state";
import type {
  FixtureAdminTask,
  FixtureNotificationChannel,
  FixtureNotificationDelivery,
} from "./types";

export function adminChannelsRatesHandlers(): HttpHandler[] {
  return [
    http.get(apiRoute("/admin/notification-channels"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const url = new URL(request.url);
      const items = [...world.notificationChannels.values()].sort((a, b) =>
        a.created_at.localeCompare(b.created_at),
      );
      return HttpResponse.json(
        successEnvelope(pageOf(items.map(({ secret: _secret, ...view }) => view), url.searchParams.get("limit") === null ? null : Number(url.searchParams.get("limit")), url.searchParams.get("after"))),
      );
    }),

    http.post(apiRoute("/admin/notification-channels"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null || typeof body.name !== "string" || body.name.trim() === "") {
        return businessError(1001, "validation failed");
      }
      const secret = body.secret as { value?: string } | null | undefined;
      if (secret !== null && secret !== undefined && secret.value === "") {
        return businessError(1001, "secret value must not be empty");
      }
      if (secret?.value === undefined) return businessError(1001, "secret required on create");
      const channel: FixtureNotificationChannel = {
        id: uuid(),
        kind: body.kind as "email" | "dingtalk",
        name: body.name,
        enabled: body.enabled === undefined ? true : (body.enabled as boolean),
        configuration: body.configuration as FixtureNotificationChannel["configuration"],
        secret_configured: true,
        version: 1,
        created_at: now(),
        updated_at: now(),
        secret: secret.value,
      };
      world.notificationChannels.set(channel.id, channel);
      const { secret: _secret, ...view } = channel;
      return HttpResponse.json(successEnvelope(view));
    }),

    http.get(apiRoute("/admin/notification-channels/:channelId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const channel = world.notificationChannels.get(String(params.channelId));
      if (channel === undefined) return businessError(1002, "channel not found");
      const { secret: _secret, ...view } = channel;
      return HttpResponse.json(successEnvelope(view));
    }),

    http.put(apiRoute("/admin/notification-channels/:channelId"), async ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const channel = world.notificationChannels.get(String(params.channelId));
      if (channel === undefined) return businessError(1002, "channel not found");
      if (versionMismatch(request, channel.version)) return businessError(1004, "version mismatch");
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "body required");
      const secret = body.secret as { value: string | null } | null | undefined;
      if (secret !== null && secret !== undefined && secret.value === "") {
        return businessError(1001, "secret value must not be empty");
      }
      const clearing = secret === null || secret?.value === null;
      if (clearing && channel.enabled) {
        return businessError(1001, "clear secret requires disabled channel");
      }
      channel.name = body.name === undefined ? channel.name : (body.name as string);
      channel.enabled = body.enabled === undefined ? channel.enabled : (body.enabled as boolean);
      channel.configuration = (body.configuration ?? channel.configuration) as FixtureNotificationChannel["configuration"];
      if (secret !== undefined && secret !== null) {
        if (secret.value !== null) {
          channel.secret = secret.value;
          channel.secret_configured = true;
        } else {
          channel.secret = null;
          channel.secret_configured = false;
        }
      }
      channel.version += 1;
      channel.updated_at = now();
      const { secret: _secret, ...view } = channel;
      return HttpResponse.json(successEnvelope(view));
    }),

    http.delete(apiRoute("/admin/notification-channels/:channelId"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const channel = world.notificationChannels.get(String(params.channelId));
      if (channel === undefined) return businessError(1002, "channel not found");
      if (versionMismatch(request, channel.version)) return businessError(1004, "version mismatch");
      world.notificationChannels.delete(channel.id);
      return HttpResponse.json(successEnvelope(null));
    }),

    http.post(apiRoute("/admin/notification-channels/:channelId/test-deliveries"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const channel = world.notificationChannels.get(String(params.channelId));
      if (channel === undefined) return businessError(1002, "channel not found");
      const delivery: FixtureNotificationDelivery = {
        id: uuid(),
        domain_event_id: uuid(),
        event_type: "notification_test_delivery",
        notification_channel_id: channel.id,
        channel_kind: channel.kind,
        channel_name: channel.name,
        recipient_user_id: null,
        masked_target: "group:" + channel.id.slice(0, 8),
        state: "queued",
        delivery_attempt_count: 0,
        next_attempt_at: now(),
        last_error_code: null,
        created_at: now(),
        updated_at: now(),
      };
      world.notificationDeliveries.unshift(delivery);
      setTimeout(() => {
        delivery.delivery_attempt_count = 1;
        if (channel.enabled) {
          delivery.state = "succeeded";
        } else {
          delivery.state = "dead_letter";
          delivery.last_error_code = "channel_disabled";
        }
        delivery.updated_at = now();
      }, TASK_RUNNING_TO_DONE_MS);
      const task: FixtureAdminTask = {
        id: uuid(),
        kind: "notification_test_delivery",
        state: "queued",
        progress: { completed: 0, total: 1, unit: "deliveries" },
        result_ref: null,
        error: null,
        created_at: now(),
        updated_at: now(),
      };
      world.tasks.set(task.id, task);
      advanceTask(task, () => {
        task.progress.completed = 1;
        if (!channel.enabled) {
          task.error = {
            type: "about:blank",
            title: "CHANNEL_DISABLED",
            status: 1001,
            detail: "channel disabled",
            request_id: "",
          };
        }
      }, !channel.enabled);
      return HttpResponse.json(successEnvelope({
        task_id: task.id, channel_id: channel.id,
      }));
    }),

    http.get(apiRoute("/admin/ai-providers/:providerId/rate-versions"), ({ request, params }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const pid = String(params.providerId);
      const rows = [...world.rateVersions.values()]
        .filter((v) => v.provider_id === pid)
        .sort((a, b) => String(b.effective_from).localeCompare(String(a.effective_from)));
      return HttpResponse.json(successEnvelope(rows));
    }),

    http.post(apiRoute("/admin/ai-providers/:providerId/rate-versions"), async ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const body = (await request.json().catch(() => null)) as Record<string, unknown> | null;
      if (body === null) return businessError(1001, "rate version body invalid");
      if (typeof body.currency !== "string" || (body.currency !== "CNY" && body.currency !== "USD")) {
        return businessError(1001, "VALIDATION_FAILED", {
          detail: "currency must be CNY or USD",
          fields: { currency: ["must be CNY or USD"] },
        });
      }
      if (typeof body.timezone !== "string" || body.timezone === "") {
        return businessError(1001, "VALIDATION_FAILED", {
          detail: "timezone must be a valid IANA name",
          fields: { timezone: ["must be a valid IANA name"] },
        });
      }
      if (!Array.isArray(body.windows) || body.windows.length === 0) {
        return businessError(1001, "VALIDATION_FAILED", {
          detail: "windows must contain 1..24 entries",
          fields: { windows: ["must contain 1..24 entries"] },
        });
      }
      const pid = String(new URL(request.url).pathname.split("/")[5]);
      const provider = world.providers.get(pid);
      if (provider === undefined) return businessError(1002, "provider not found");
      const budget = world.settings.get("ai-budget") as { currency?: string; enforced?: boolean } | undefined;
      if (budget?.enforced && budget.currency !== body.currency) {
        return businessError(1001, "RATE_CURRENCY_MISMATCH");
      }
      const createdAt = new Date().toISOString();
      for (const prev of world.rateVersions.values()) {
        if (prev.provider_id === pid && prev.effective_until == null) {
          prev.effective_until = body.effective_from ?? createdAt;
        }
      }
      const id = uuid();
      const rawWindows = body.windows as Array<Record<string, unknown>>;
      const windows = rawWindows.map((win) => ({ id: uuid(), ...win }));
      const restBody: Record<string, unknown> = { ...body };
      if (restBody.effective_until == null) {
        delete restBody.effective_until;
      }
      const row = {
        id,
        provider_id: pid,
        ...restBody,
        windows,
        version: world.rateVersions.size + 1,
        created_at: createdAt,
      };
      world.rateVersions.set(id, row);
      return HttpResponse.json(successEnvelope(row));
    }),

    http.get(apiRoute("/admin/ai-usage/calls"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      const providerFilter = new URL(request.url).searchParams.get("provider_id");
      const createdAt = now();
      const rows = [
        {
          id: "0b1e2f3a-0000-4000-8000-00000000d001",
          review_run_id: "0b1e2f3a-0000-4000-8000-000000000001",
          stage_position: 1, model_turn: 1, call_index: 1,
          provider_id: ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
          model_name: "glm-4.6", sent_at: createdAt,
          reasoning_effort: "medium", max_tokens: 4096, temperature: "0.7",
          rate_source: "global_settings",
          currency: "CNY", input_tokens: 1200, output_tokens: 380,
          reasoning_tokens: 210,
          usage_reported: true, reference_amount: "0.00632100",
          calc_basis: "partial_detail",
          detail_note: "Computed from global fallback rates without cache detail breakdown.",
          outcome: "ok",
          created_at: createdAt,
        },
        {
          id: "0b1e2f3a-0000-4000-8000-00000000d002",
          review_run_id: "0b1e2f3a-0000-4000-8000-000000000001",
          stage_position: 1, model_turn: 2, call_index: 1,
          provider_id: ADMIN_FIXTURE_PROVIDER_PRIMARY_ID,
          model_name: "glm-4.6", sent_at: createdAt,
          rate_source: "none",
          usage_reported: false,
          calc_basis: "not_computable",
          detail_note: "Upstream did not report token counts and no active provider rate exists.",
          outcome: "ok",
          created_at: createdAt,
        },
      ];
      const filtered = providerFilter === null
        ? rows
        : rows.filter((row) => row.provider_id === providerFilter);
      return HttpResponse.json(successEnvelope({
        items: filtered,
        page: { next_cursor: null, has_more: false },
      }));
    }),

    http.get(apiRoute("/admin/ai-usage/summary"), ({ request }) => {
      const guard = adminGuard(request);
      if (guard !== null) return guard;
      return HttpResponse.json(successEnvelope([
        {
          currency: "USD",
          calls: 3,
          reference_amount: "3.67100000",
          computable_calls: 3,
          input_tokens_known: 12000,
          input_tokens_cache_known: 8000,
          cached_tokens_known: 4000,
          cache_detail_samples: 2,
        },
      ]));
    }),
  ];
}
