begin;

-- SQL Performance Diagnostics and user administrator role extension (v4.0.0, RCP-20261108-SQL-PERFORMANCE-DIAGNOSTICS):
-- 0. Adds role column ('user' | 'admin') to users, keeping role = 'admin' for the built-in super administrator;
-- 1. datasource_diagnostics_configs: Per-datasource diagnostics toggle, poll interval, Top-N budget, 6-state capability probe, and circuit breaker state;
-- 2. diagnostics_instance_leases: Single-concurrency collection mutex lease coordinated by physical instance server_identity_fingerprint;
-- 3. sql_diagnostics_snapshots: Per-collection snapshot batch header (including stats reset and gap flags);
-- 4. sql_diagnostics_statements: Controlled statement fingerprint registry and on-demand AES-256-GCM encrypted SQL templates;
-- 5. sql_diagnostics_bucket_metrics: Time-bucket monotonic delta and cumulative performance metrics;
-- 6. sql_diagnostics_ai_reports: Four-section encrypted AI diagnostic reports generated under turn-scoped in-memory redaction and optimization order bridge state;
-- 7. sql_diagnostics_order_links: Comparable before/after window bindings linking a SQL fingerprint to a change order (supporting explicit user confirmation when the fingerprint changes).

alter table users
    add column role text not null default 'user' check (role in ('user', 'admin'));

update users set role = 'admin' where is_builtin_admin = true;

alter table users
    add constraint users_builtin_admin_role check (not is_builtin_admin or role = 'admin');

create table datasource_diagnostics_configs (
    id uuid primary key,
    datasource_id uuid not null unique,
    enabled boolean not null default false,
    interval_seconds integer not null default 60 check (
        interval_seconds in (0, 60, 90, 120, 300)
    ),
    effective_interval_seconds integer not null default 60 check (
        effective_interval_seconds between 0 and 600
    ),
    top_n integer not null default 200 check (
        top_n between 50 and 500
    ),
    state text not null default 'disabled' check (
        state in ('disabled', 'active', 'degraded', 'paused_on_failure')
    ),
    last_probe_state text not null default 'collection_failed' check (
        last_probe_state in (
            'ready',
            'stats_disabled',
            'insufficient_privilege',
            'unsupported_engine',
            'no_samples',
            'collection_failed'
        )
    ),
    last_probe_at timestamptz,
    last_collected_at timestamptz,
    last_manual_snapshot_at timestamptz,
    consecutive_failures integer not null default 0 check (consecutive_failures >= 0),
    paused_reason text,
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    updated_at timestamptz not null
);

create index datasource_diagnostics_configs_enabled_idx
    on datasource_diagnostics_configs(enabled, state)
    where enabled = true;

create table diagnostics_instance_leases (
    id uuid primary key,
    server_identity_fingerprint text not null unique,
    datasource_id uuid not null,
    holder_id text not null,
    lease_expires_at timestamptz not null,
    last_collected_at timestamptz,
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    updated_at timestamptz not null
);

create index diagnostics_instance_leases_expires_idx
    on diagnostics_instance_leases(lease_expires_at);

create table sql_diagnostics_snapshots (
    id uuid primary key,
    datasource_id uuid not null,
    engine text not null check (
        engine in ('mysql', 'postgresql', 'tidb', 'oceanbase', 'polardb')
    ),
    trigger_kind text not null check (trigger_kind in ('scheduled', 'manual')),
    probe_state text not null check (
        probe_state in (
            'ready',
            'stats_disabled',
            'insufficient_privilege',
            'unsupported_engine',
            'no_samples',
            'collection_failed'
        )
    ),
    top_n integer not null check (top_n between 50 and 500),
    statement_count integer not null default 0 check (statement_count >= 0),
    duration_ms integer not null default 0 check (duration_ms >= 0),
    reset_detected boolean not null default false,
    gap_detected boolean not null default false,
    failure_reason text,
    sampled_at timestamptz not null,
    created_at timestamptz not null
);

create index sql_diagnostics_snapshots_ds_time_idx
    on sql_diagnostics_snapshots(datasource_id, sampled_at desc);

create table sql_diagnostics_statements (
    id uuid primary key,
    datasource_id uuid not null,
    engine text not null check (
        engine in ('mysql', 'postgresql', 'tidb', 'oceanbase', 'polardb')
    ),
    engine_fingerprint text not null,
    yearning_fingerprint text,
    database_or_schema text not null default '',
    template_status text not null default 'not_fetched' check (
        template_status in ('not_fetched', 'available', 'unavailable')
    ),
    template_ciphertext bytea,
    template_nonce bytea,
    template_key_id text,
    template_algorithm text check (
        template_algorithm is null or template_algorithm = 'aes-256-gcm'
    ),
    template_fetched_at timestamptz,
    latest_snapshot_id uuid,
    latest_calls_total bigint not null default 0 check (latest_calls_total >= 0),
    latest_total_exec_time_ms double precision not null default 0 check (latest_total_exec_time_ms >= 0),
    latest_rows_examined_or_shared_blks_read_total bigint not null default 0 check (latest_rows_examined_or_shared_blks_read_total >= 0),
    latest_rows_sent_or_rows_returned_total bigint not null default 0 check (latest_rows_sent_or_rows_returned_total >= 0),
    latest_lock_time_ms_or_temp_blks_written_total double precision not null default 0 check (latest_lock_time_ms_or_temp_blks_written_total >= 0),
    latest_plans_total bigint,
    latest_total_plan_time_ms double precision,
    latest_temp_blks_read_total bigint,
    latest_shared_blks_hit_total bigint,
    first_seen_at timestamptz not null,
    last_seen_at timestamptz not null,
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    updated_at timestamptz not null,
    constraint sql_diagnostics_statements_template_envelope check (
        (template_ciphertext is null and template_nonce is null and template_key_id is null and template_algorithm is null)
        or (template_ciphertext is not null and template_nonce is not null and template_key_id is not null and template_algorithm is not null)
    ),
    unique (datasource_id, engine_fingerprint)
);

create index sql_diagnostics_statements_ds_last_seen_idx
    on sql_diagnostics_statements(datasource_id, last_seen_at desc);

create table sql_diagnostics_bucket_metrics (
    id uuid primary key,
    datasource_id uuid not null,
    statement_id uuid not null,
    snapshot_id uuid not null,
    bucket_start timestamptz not null,
    bucket_end timestamptz not null,
    coverage_mode text not null check (
        coverage_mode in (
            'cumulative_only',
            'window_delta',
            'reset_or_gap',
            'unobserved_below_top_n'
        )
    ),
    calls_total bigint not null default 0 check (calls_total >= 0),
    total_exec_time_ms double precision not null default 0 check (total_exec_time_ms >= 0),
    rows_examined_or_shared_blks_read_total bigint not null default 0 check (rows_examined_or_shared_blks_read_total >= 0),
    rows_sent_or_rows_returned_total bigint not null default 0 check (rows_sent_or_rows_returned_total >= 0),
    lock_time_ms_or_temp_blks_written_total double precision not null default 0 check (lock_time_ms_or_temp_blks_written_total >= 0),
    delta_calls bigint not null default 0 check (delta_calls >= 0),
    delta_exec_time_ms double precision not null default 0 check (delta_exec_time_ms >= 0),
    avg_exec_time_ms double precision not null default 0 check (avg_exec_time_ms >= 0),
    delta_rows_examined_or_shared_blks_read bigint not null default 0 check (delta_rows_examined_or_shared_blks_read >= 0),
    delta_rows_sent_or_rows_returned bigint not null default 0 check (delta_rows_sent_or_rows_returned >= 0),
    delta_lock_time_ms_or_temp_blks_written double precision not null default 0 check (delta_lock_time_ms_or_temp_blks_written >= 0),
    delta_plans bigint,
    delta_plan_time_ms double precision,
    delta_temp_blks_read bigint,
    delta_shared_blks_hit bigint,
    reset_detected boolean not null default false,
    first_observed boolean not null default false,
    created_at timestamptz not null,
    unique (snapshot_id, statement_id)
);

create index sql_diagnostics_bucket_metrics_ds_bucket_idx
    on sql_diagnostics_bucket_metrics(datasource_id, bucket_end desc);

create index sql_diagnostics_bucket_metrics_stmt_bucket_idx
    on sql_diagnostics_bucket_metrics(statement_id, bucket_end asc);

create table sql_diagnostics_ai_reports (
    id uuid primary key,
    datasource_id uuid not null,
    statement_id uuid not null,
    user_id uuid not null,
    window_start timestamptz not null,
    window_end timestamptz not null,
    coverage_mode text not null check (
        coverage_mode in (
            'cumulative_only',
            'window_delta',
            'reset_or_gap',
            'unobserved_below_top_n'
        )
    ),
    report_ciphertext bytea not null,
    report_nonce bytea not null,
    report_key_id text not null,
    report_algorithm text not null check (report_algorithm = 'aes-256-gcm'),
    candidate_sql_ciphertext bytea,
    candidate_sql_nonce bytea,
    candidate_sql_key_id text,
    candidate_sql_algorithm text check (
        candidate_sql_algorithm is null or candidate_sql_algorithm = 'aes-256-gcm'
    ),
    candidate_sql_hmac bytea,
    candidate_sql_hmac_key_id text,
    candidate_statement_kind text check (
        candidate_statement_kind is null or candidate_statement_kind in ('ddl', 'dml')
    ),
    submission_state text not null default 'draft_ready' check (
        submission_state in ('draft_ready', 'prechecked', 'submitted')
    ),
    flow_id uuid,
    ephemeral_run_id uuid,
    submitted_order_id uuid,
    submitted_order_display_no text,
    expires_at timestamptz not null,
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    updated_at timestamptz not null,
    constraint sql_diagnostics_ai_reports_candidate_envelope check (
        (candidate_sql_ciphertext is null and candidate_sql_nonce is null and candidate_sql_key_id is null and candidate_sql_algorithm is null)
        or (candidate_sql_ciphertext is not null and candidate_sql_nonce is not null and candidate_sql_key_id is not null and candidate_sql_algorithm is not null)
    )
);

create index sql_diagnostics_ai_reports_stmt_idx
    on sql_diagnostics_ai_reports(statement_id, created_at desc);

create index sql_diagnostics_ai_reports_expires_idx
    on sql_diagnostics_ai_reports(expires_at);

create table sql_diagnostics_order_links (
    id uuid primary key,
    datasource_id uuid not null,
    statement_id uuid not null,
    target_statement_id uuid,
    change_order_id uuid not null,
    order_display_no text not null,
    relation_kind text not null check (
        relation_kind in (
            'same_fingerprint',
            'rewritten_fingerprint_confirmed',
            'index_or_schema_change'
        )
    ),
    order_executed_at timestamptz,
    confirmed_by_user_id uuid not null,
    created_at timestamptz not null,
    unique (statement_id, change_order_id)
);

create index sql_diagnostics_order_links_stmt_idx
    on sql_diagnostics_order_links(datasource_id, statement_id);

commit;
