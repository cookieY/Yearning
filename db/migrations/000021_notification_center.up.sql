begin;

-- S007 / RCP-20261031-NOTIFICATION-RULES-AND-CHANNELS: Notification center four channels,
-- notification rules surface, and delivery deduplication identity rebuild. AI precheck events are
-- excluded from the subscribable vocabulary (RCP-20261031-STOP-REVIEW-BLOCKED-NOTIFICATION) and
-- pinned at the storage layer via the event_type CHECK constraint.

-- Extend channel types with Feishu (custom bot) and Slack (Incoming Webhook).
alter table notification_channels drop constraint notification_channels_kind_check;
alter table notification_channels
    add constraint notification_channels_kind_check
    check (kind in ('email', 'dingtalk', 'feishu', 'slack'));

-- Notification rules: name + enabled flag + flow scope (flow_id null = all flows) + one trigger event
-- + one target role + one channel. Target semantics dispatch by channel type (email = per-user, IM = single
-- group delivery) per PRD §7.2; no physical FK constraints (D002), with channel_id ref-links enforced by the application.
create table notification_rules (
    id uuid primary key,
    name text not null check (char_length(name) between 1 and 128),
    enabled boolean not null default false,
    flow_id uuid,
    event_type text not null check (event_type in (
        'change_order.submitted',
        'change_order.step_activated',
        'change_order.approval_decided',
        'change_order.stage_ready_for_execution',
        'change_order.rejected',
        'change_order.withdrawn',
        'change_order.voided',
        'execution.scheduled',
        'execution.schedule_cancelled',
        'execution.started',
        'execution.succeeded',
        'execution.failed',
        'execution.partial_failed',
        'execution.cancelled',
        'execution.partial_cancelled',
        'execution.result_unknown',
        'execution.blocked_datasource_unavailable',
        'execution.missed_schedule',
        'query_grant.revoked',
        'ai.budget_warning',
        'ai.budget_exhausted',
        'ai.provider_failover'
    )),
    target_kind text not null check (target_kind in (
        'order_submitter', 'stage_approvers', 'stage_executors',
        'grant_requester', 'builtin_admins'
    )),
    channel_id uuid not null,
    version bigint not null default 1 check (version >= 1),
    created_by_user_id uuid not null,
    created_at timestamptz not null,
    updated_at timestamptz not null,
    unique (name)
);
create index notification_rules_event_enabled_idx
    on notification_rules(event_type) where enabled;

-- Rebuild delivery deduplication identity: in the legacy unique key (domain_event_id, channel_id, recipient_user_id),
-- NULLs are distinct, allowing duplicate group deliveries when multiple rules match. recipient_key provides
-- a normalized target identity: user:{uuid} (individual) or group:{channel_id} (channel group target).

alter table notification_deliveries add column recipient_key text not null default '';
-- source_rule_id: Rule matched when creating the delivery plan (nullable for pre-migration rows;
-- attributes the first-writing rule when multiple rules deduplicate to the same target; explanatory only).
alter table notification_deliveries add column source_rule_id uuid;
update notification_deliveries
    set recipient_key = 'user:' || recipient_user_id::text
    where recipient_user_id is not null;
update notification_deliveries
    set recipient_key = 'group:' || notification_channel_id::text
    where recipient_user_id is null;
alter table notification_deliveries
    drop constraint notification_deliveries_domain_event_id_notification_channe_key;
alter table notification_deliveries
    add constraint notification_deliveries_recipient_key_shape
    check (recipient_key ~ '^(user:[0-9a-fA-F-]{36}|group:[0-9a-fA-F-]{36})$');
alter table notification_deliveries
    add constraint notification_deliveries_target_unique
    unique (domain_event_id, notification_channel_id, recipient_key);

-- S007 upgrade compatibility (PRD §13): Seeds compatibility rules for each existing enabled channel
-- with a configured secret, mirroring legacy hardcoded routing (submitter events + execution failure actors + grant requester)
-- so notifications are neither duplicated nor lost after upgrade. Fresh installations without existing channels seed nothing.
insert into notification_rules (id, name, enabled, flow_id, event_type, target_kind, channel_id, version, created_by_user_id, created_at, updated_at)
select gen_random_uuid(),
       'legacy-' || c.kind || '-' || left(c.id::text, 8) || '-' || e.event_type || '-submitter',
       true, null, e.event_type, 'order_submitter', c.id, 1, c.created_by_user_id, now(), now()
from notification_channels c
cross join (values
    ('change_order.submitted'),
    ('change_order.approval_decided'),
    ('change_order.rejected'),
    ('execution.failed'),
    ('execution.partial_failed'),
    ('execution.partial_cancelled'),
    ('execution.result_unknown'),
    ('execution.blocked_datasource_unavailable'),
    ('execution.missed_schedule'),
    ('execution.succeeded')
) as e(event_type)
where c.enabled and c.secret_ciphertext is not null
  and coalesce((c.configuration::jsonb -> 'events' ->> e.event_type)::boolean,
               e.event_type in ('change_order.submitted', 'change_order.approval_decided',
                                'change_order.rejected',
                                'execution.failed', 'execution.partial_failed',
                                'execution.partial_cancelled', 'execution.result_unknown',
                                'execution.blocked_datasource_unavailable',
                                'execution.missed_schedule'));

insert into notification_rules (id, name, enabled, flow_id, event_type, target_kind, channel_id, version, created_by_user_id, created_at, updated_at)
select gen_random_uuid(),
       'legacy-' || c.kind || '-' || left(c.id::text, 8) || '-' || e.event_type || '-executors',
       true, null, e.event_type, 'stage_executors', c.id, 1, c.created_by_user_id, now(), now()
from notification_channels c
cross join (values
    ('execution.failed'),
    ('execution.partial_failed'),
    ('execution.partial_cancelled'),
    ('execution.result_unknown')
) as e(event_type)
where c.enabled and c.secret_ciphertext is not null
  and coalesce((c.configuration::jsonb -> 'events' ->> e.event_type)::boolean, true);

insert into notification_rules (id, name, enabled, flow_id, event_type, target_kind, channel_id, version, created_by_user_id, created_at, updated_at)
select gen_random_uuid(),
       'legacy-' || c.kind || '-' || left(c.id::text, 8) || '-query_grant.revoked-grant',
       true, null, 'query_grant.revoked', 'grant_requester', c.id, 1, c.created_by_user_id, now(), now()
from notification_channels c
where c.enabled and c.secret_ciphertext is not null
  and coalesce((c.configuration::jsonb -> 'events' ->> 'query_grant.revoked')::boolean, true);

commit;
