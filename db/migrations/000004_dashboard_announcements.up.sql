begin;

create table announcement_revisions (
    id uuid primary key,
    revision_number bigint not null unique check (revision_number >= 1),
    title text not null check (length(title) between 1 and 200),
    markdown_source text not null check (length(markdown_source) between 1 and 20000),
    sanitized_html text not null check (length(sanitized_html) between 1 and 100000),
    content_sha256 char(64) not null check (content_sha256 ~ '^[0-9a-f]{64}$'),
    sanitizer_policy_version text not null,
    created_by_user_id_snapshot uuid not null,
    created_by_username_snapshot text not null,
    created_at timestamptz not null
);

create table announcement_publication (
    singleton boolean primary key default true check (singleton),
    announcement_revision_id uuid not null,
    published_by_user_id_snapshot uuid not null,
    published_by_username_snapshot text not null,
    published_at timestamptz not null,
    version bigint not null default 1 check (version >= 1),
    updated_at timestamptz not null
);

create table dashboard_daily_metrics (
    id uuid primary key,
    metric_day date not null,
    metric_key text not null check (metric_key in (
        'change_order_total', 'ddl_statement_total', 'dml_statement_total',
        'approval_duration_ms', 'execution_outcome_total', 'review_outcome_total',
        'finding_total', 'provider_call_outcome_total', 'review_duration_ms',
        'ai_token_total', 'ai_cost_minor', 'fingerprint_coverage_ratio'
    )),
    scope_kind text not null check (scope_kind in ('global', 'user', 'datasource', 'provider')),
    scope_id uuid,
    dimension_key text not null default '',
    metric_payload jsonb not null,
    source_event_sequence bigint not null check (source_event_sequence >= 0),
    version bigint not null default 1 check (version >= 1),
    updated_at timestamptz not null,
    constraint dashboard_metric_scope check (
        (scope_kind = 'global' and scope_id is null)
        or (scope_kind <> 'global' and scope_id is not null)
    )
);
create unique index dashboard_daily_metrics_natural_unique
    on dashboard_daily_metrics(metric_day, metric_key, scope_kind, scope_id, dimension_key) nulls not distinct;
create index dashboard_daily_metrics_lookup_idx
    on dashboard_daily_metrics(scope_kind, scope_id, metric_day desc, metric_key);

commit;
