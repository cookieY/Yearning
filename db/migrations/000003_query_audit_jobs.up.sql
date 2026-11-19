begin;

create table query_access_requests (
    id uuid primary key,
    requester_user_id_snapshot uuid not null,
    requester_username_snapshot text not null,
    requester_display_name_snapshot text not null,
    flow_id_snapshot uuid not null,
    flow_name_snapshot text not null,
    flow_snapshot_ciphertext bytea not null,
    flow_snapshot_nonce bytea not null,
    flow_snapshot_key_id text not null,
    flow_snapshot_algorithm text not null check (flow_snapshot_algorithm = 'aes-256-gcm'),
    snapshot_hash char(64) not null check (snapshot_hash ~ '^[0-9a-f]{64}$'),
    state text not null check (state in ('access_pending', 'grant_active', 'access_rejected', 'withdrawn', 'invalid')),
    reason text not null check (length(reason) between 1 and 4096),
    requested_until timestamptz not null,
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    terminal_at timestamptz,
    constraint query_access_terminal check (
        (state = 'access_pending' and terminal_at is null)
        or (state <> 'access_pending' and terminal_at is not null)
    )
);
create index query_access_requester_idx on query_access_requests(requester_user_id_snapshot, created_at desc, id desc);
create index query_access_pending_idx on query_access_requests(state, created_at, id) where state = 'access_pending';

create table query_access_request_datasources (
    request_id uuid not null,
    datasource_id uuid not null,
    datasource_name_snapshot text not null,
    datasource_identity_fingerprint char(64) not null check (datasource_identity_fingerprint ~ '^[0-9a-f]{64}$'),
    can_query boolean not null check (can_query),
    can_export boolean not null,
    primary key (request_id, datasource_id)
);

create table query_access_approval_steps (
    id uuid primary key,
    request_id uuid not null,
    position integer not null check (position between 1 and 10),
    state text not null check (state in ('pending', 'active', 'approved', 'rejected', 'invalid')),
    decided_at timestamptz,
    unique (request_id, position)
);

create table query_access_approval_actors (
    step_id uuid not null,
    reviewer_user_id_snapshot uuid not null,
    username_snapshot text not null,
    display_name_snapshot text not null,
    primary key (step_id, reviewer_user_id_snapshot)
);

create unique index query_request_one_active_approval_step
    on query_access_approval_steps(request_id)
    where state = 'active';

create table query_access_decisions (
    id uuid primary key,
    request_id uuid not null,
    step_id uuid not null,
    reviewer_user_id_snapshot uuid not null,
    reviewer_username_snapshot text not null,
    decision text not null check (decision in ('approve', 'reject')),
    comment text not null default '' check (length(comment) <= 4096),
    occurred_at timestamptz not null,
    unique (step_id)
);

create table query_grants (
    id uuid primary key,
    query_access_request_id uuid not null unique,
    requester_user_id_snapshot uuid not null,
    requester_username_snapshot text not null,
    state text not null check (state in ('active', 'revoked', 'expired', 'relinquished')),
    expires_at timestamptz not null,
    revoked_by_user_id_snapshot uuid,
    revoked_by_username_snapshot text,
    revoke_reason text,
    revoked_at timestamptz,
    terminal_at timestamptz,
    terminal_reason text,
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    constraint query_grant_revoke_fields check (
        (state = 'revoked' and revoked_by_user_id_snapshot is not null and revoked_by_username_snapshot is not null and revoke_reason is not null and revoked_at is not null)
        or (state <> 'revoked' and revoked_by_user_id_snapshot is null and revoked_by_username_snapshot is null and revoke_reason is null and revoked_at is null)
    ),
    constraint query_grant_terminal_fields check (
        (state = 'active' and terminal_at is null and terminal_reason is null)
        or (state <> 'active' and terminal_at is not null and terminal_reason is not null)
    )
);
create index query_grants_active_requester_idx on query_grants(requester_user_id_snapshot, expires_at) where state = 'active';

create table query_grant_capabilities (
    grant_id uuid not null,
    datasource_id uuid not null,
    datasource_name_snapshot text not null,
    datasource_identity_fingerprint char(64) not null check (datasource_identity_fingerprint ~ '^[0-9a-f]{64}$'),
    can_query boolean not null check (can_query),
    can_export boolean not null,
    primary key (grant_id, datasource_id)
);

create table query_sessions (
    id uuid primary key,
    user_id_snapshot uuid not null,
    username_snapshot text not null,
    flow_id_snapshot uuid not null,
    flow_name_snapshot text not null,
    grant_id uuid,
    approval_required_snapshot boolean not null,
    state text not null check (state in ('active', 'closed', 'revoked', 'expired', 'user_deleted')),
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    last_seen_at timestamptz not null,
    closed_at timestamptz,
    close_reason text,
    constraint query_session_grant_mode check (
        (approval_required_snapshot and grant_id is not null)
        or (not approval_required_snapshot and grant_id is null)
    ),
    constraint query_session_close_pair check (
        (state = 'active' and closed_at is null and close_reason is null)
        or (state <> 'active' and closed_at is not null and close_reason is not null)
    )
);
create index query_sessions_active_user_idx on query_sessions(user_id_snapshot, last_seen_at desc) where state = 'active';

create table query_session_capabilities (
    session_id uuid not null,
    datasource_id uuid not null,
    datasource_name_snapshot text not null,
    datasource_identity_fingerprint char(64) not null check (datasource_identity_fingerprint ~ '^[0-9a-f]{64}$'),
    state text not null check (state in ('active', 'datasource_unavailable', 'identity_changed')),
    can_query boolean not null check (can_query),
    can_export boolean not null,
    primary key (session_id, datasource_id)
);

create table query_executions (
    id uuid primary key,
    session_id uuid not null,
    user_id_snapshot uuid not null,
    datasource_id uuid not null,
    query_payload_ciphertext bytea not null,
    query_payload_nonce bytea not null,
    query_payload_key_id text not null,
    query_payload_algorithm text not null check (query_payload_algorithm = 'aes-256-gcm'),
    sql_hmac char(64) not null check (sql_hmac ~ '^[0-9a-f]{64}$'),
    fingerprint_hmac char(64) not null check (fingerprint_hmac ~ '^[0-9a-f]{64}$'),
    state text not null check (state in ('running', 'succeeded', 'failed', 'cancelled', 'timed_out', 'result_unknown')),
    timeout_ms integer not null check (timeout_ms between 1 and 300000),
    elapsed_ms integer check (elapsed_ms is null or elapsed_ms >= 0),
    streamed_row_count bigint not null default 0 check (streamed_row_count >= 0),
    business_err_code integer check (business_err_code is null or business_err_code between 1000 and 9999),
    failure_name text,
    created_at timestamptz not null,
    finished_at timestamptz,
    constraint query_execution_time_consistency check (
        (state = 'running' and finished_at is null and elapsed_ms is null)
        or (state <> 'running' and finished_at is not null and elapsed_ms is not null)
    ),
    constraint query_execution_failure_fields check (
        (state = 'succeeded' and business_err_code is null and failure_name is null)
        or (state in ('failed', 'cancelled', 'timed_out', 'result_unknown') and business_err_code is not null and failure_name is not null)
        or (state = 'running' and business_err_code is null and failure_name is null)
    )
);
create index query_executions_session_idx on query_executions(session_id, created_at desc, id desc);
create index query_executions_running_idx on query_executions(state, id) where state = 'running';

create table query_cursors (
    id uuid primary key,
    execution_id uuid not null,
    cursor_token_hash char(64) not null unique check (cursor_token_hash ~ '^[0-9a-f]{64}$'),
    owner_instance_id text not null check (length(owner_instance_id) between 1 and 128),
    fence_token bigint not null default 1 check (fence_token >= 1),
    purpose text not null check (purpose in ('display', 'export')),
    cursor_state_ciphertext bytea not null,
    cursor_state_nonce bytea not null,
    cursor_state_key_id text not null,
    cursor_state_algorithm text not null check (cursor_state_algorithm = 'aes-256-gcm'),
    next_page_number bigint not null check (next_page_number >= 1),
    created_at timestamptz not null,
    expires_at timestamptz not null,
    absolute_expires_at timestamptz not null,
    consumed_at timestamptz,
    constraint query_cursor_expiry check (
        expires_at > created_at
        and expires_at <= absolute_expires_at
        and absolute_expires_at = created_at + interval '6 hours'
    )
);
create index query_cursors_expiry_idx on query_cursors(expires_at) where consumed_at is null;
create index query_cursors_owner_idx on query_cursors(owner_instance_id, consumed_at, expires_at);

create table audit_events (
    id uuid primary key,
    event_type text not null,
    actor_kind text not null check (actor_kind in ('user', 'system', 'worker', 'cli')),
    actor_id uuid,
    actor_username_snapshot text,
    resource_type text not null,
    resource_id uuid,
    action text not null,
    outcome text not null check (outcome in ('succeeded', 'denied', 'failed')),
    request_id uuid,
    source_ip inet,
    user_agent text,
    metadata jsonb not null default '{}'::jsonb,
    occurred_at timestamptz not null,
    expires_at timestamptz not null,
    constraint audit_retention_exact check (expires_at = occurred_at + interval '90 days')
);
create index audit_events_time_idx on audit_events(occurred_at desc, id desc);
create index audit_events_expiry_idx on audit_events(expires_at);
create index audit_events_resource_idx on audit_events(resource_type, resource_id, occurred_at desc);

create table domain_events (
    id uuid primary key,
    event_type text not null,
    subject text not null,
    sequence bigint not null check (sequence >= 1),
    correlation_id uuid not null,
    causation_id uuid,
    actor jsonb not null,
    event_data jsonb not null,
    occurred_at timestamptz not null,
    unique (subject, sequence)
);
create index domain_events_subject_idx on domain_events(subject, sequence);

create table outbox_events (
    id uuid primary key,
    event_type text not null,
    subject text not null,
    payload jsonb not null,
    occurred_at timestamptz not null,
    available_at timestamptz not null,
    published_at timestamptz,
    delivery_attempts integer not null default 0 check (delivery_attempts >= 0),
    last_failure_name text,
    lease_owner text,
    lease_expires_at timestamptz,
    constraint outbox_lease_pair check ((lease_owner is null) = (lease_expires_at is null))
);
create index outbox_ready_idx on outbox_events(available_at, id) where published_at is null;

create table async_tasks (
    id uuid primary key,
    kind text not null check (kind in ('sql_grouping', 'ai_review', 'migration_dry_run', 'admin_connection_test', 'ai_provider_connection_test', 'identity_provider_connection_test', 'notification_test_delivery')),
    state text not null check (state in ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    aggregate_type text,
    aggregate_id uuid,
    input jsonb not null,
    progress_completed bigint not null default 0 check (progress_completed >= 0),
    progress_total bigint not null default 0 check (progress_total >= 0),
    progress_unit text not null,
    result_ref text,
    business_err_code integer check (business_err_code is null or business_err_code between 1000 and 9999),
    failure_name text,
    lease_owner text,
    lease_expires_at timestamptz,
    fence_token bigint not null default 1 check (fence_token >= 1),
    created_at timestamptz not null,
    updated_at timestamptz not null,
    finished_at timestamptz,
    constraint async_task_lease_pair check ((lease_owner is null) = (lease_expires_at is null)),
    constraint async_task_progress_consistency check (progress_completed <= progress_total),
    constraint async_task_terminal_fields check (
        (state in ('queued', 'running') and finished_at is null and business_err_code is null and failure_name is null)
        or (state = 'succeeded' and finished_at is not null and business_err_code is null and failure_name is null)
        or (state in ('failed', 'cancelled') and finished_at is not null and business_err_code is not null and failure_name is not null)
    )
);
create index async_tasks_claim_idx on async_tasks(state, created_at, id) where state in ('queued', 'running');

create table idempotency_records (
    id uuid primary key,
    requesting_user_id uuid not null,
    route_template text not null,
    aggregate_id uuid,
    idempotency_key text not null check (length(idempotency_key) between 1 and 128),
    request_hash char(64) not null check (request_hash ~ '^[0-9a-f]{64}$'),
    state text not null check (state in ('in_progress', 'completed')),
    response_http_status integer check (response_http_status = 200),
    response_headers jsonb,
    response_body_ciphertext bytea,
    response_body_nonce bytea,
    response_key_id text,
    response_body_algorithm text check (response_body_algorithm is null or response_body_algorithm = 'aes-256-gcm'),
    created_at timestamptz not null,
    expires_at timestamptz not null,
    unique nulls not distinct (requesting_user_id, route_template, aggregate_id, idempotency_key),
    constraint idempotency_expiry check (expires_at = created_at + interval '24 hours'),
    constraint idempotency_response_envelope check (
        (state = 'in_progress' and response_http_status is null and response_body_ciphertext is null and response_body_nonce is null and response_key_id is null and response_body_algorithm is null)
        or (state = 'completed' and response_http_status = 200 and response_body_ciphertext is not null and response_body_nonce is not null and response_key_id is not null and response_body_algorithm is not null)
    )
);
create index idempotency_expiry_idx on idempotency_records(expires_at);

create table notification_deliveries (
    id uuid primary key,
    domain_event_id uuid not null,
    notification_channel_id uuid not null,
    recipient_user_id uuid,
    recipient_address_ciphertext bytea not null,
    recipient_address_nonce bytea not null,
    recipient_address_key_id text not null,
    recipient_address_algorithm text not null check (recipient_address_algorithm = 'aes-256-gcm'),
    state text not null check (state in ('queued', 'sending', 'succeeded', 'failed', 'dead_letter')),
    delivery_attempt_count smallint not null default 0 check (delivery_attempt_count between 0 and 5),
    next_attempt_at timestamptz,
    last_failure_name text,
    created_at timestamptz not null,
    updated_at timestamptz not null,
    unique (domain_event_id, notification_channel_id, recipient_user_id)
);
create index notification_delivery_ready_idx
    on notification_deliveries(next_attempt_at, id) where state in ('queued', 'failed') and delivery_attempt_count < 5;

create table ai_daily_budget_usage (
    budget_day date not null,
    currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
    amount_minor bigint not null default 0 check (amount_minor >= 0),
    reserved_minor bigint not null default 0 check (reserved_minor >= 0),
    version bigint not null default 1 check (version >= 1),
    updated_at timestamptz not null,
    primary key (budget_day, currency)
);

commit;
