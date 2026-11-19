begin;

-- S008 / RCP-20261031-AI-PROVIDER-GENPARAMS-PRICING: Three tables for AI Provider reference
-- pricing and per-call usage. Rate versions are append-only (updates create a new version without rewriting
-- historical pricing facts, closing the open-ended effective_until NULL on the prior latest version within
-- the write transaction); time windows are normalized on write into same-day [start,end) rows (splitting
-- cross-midnight windows into two rows) with overlap rejected by the application service; per-HTTP-call
-- usage records one row per actual request (initial/retry/repair/failover), using nullable columns for
-- zero/positive/unknown (never coercing unknown to 0) and http_status NULL when no HTTP response was received;
-- idempotency key (run,stage,turn,call_index,sent_at) deduplicates event replays while recording genuine
-- post-crash re-executions (new sent_at). temperature is snapshotted as normalized decimal text.

create table ai_provider_rate_versions (
    id uuid primary key,
    provider_id uuid not null,
    currency char(3) not null check (currency in ('CNY', 'USD')),
    timezone text not null check (char_length(timezone) between 1 and 64),
    effective_from timestamptz not null,
    effective_until timestamptz check (effective_until is null or effective_until > effective_from),
    version bigint not null check (version >= 1),
    created_by_user_id uuid not null,
    created_at timestamptz not null,
    updated_at timestamptz not null,
    unique (provider_id, version)
);
create index ai_provider_rate_versions_lookup_idx
    on ai_provider_rate_versions(provider_id, effective_from);

create table ai_provider_rate_windows (
    id uuid primary key,
    rate_version_id uuid not null,
    start_minute smallint not null check (start_minute between 0 and 1439),
    end_minute smallint not null check (end_minute between 1 and 1440),
    input_price numeric(18, 6) not null check (input_price >= 0),
    output_price numeric(18, 6) not null check (output_price >= 0),
    cached_read_price numeric(18, 6) check (cached_read_price >= 0),
    cached_write_price numeric(18, 6) check (cached_write_price >= 0),
    check (start_minute < end_minute)
);
create index ai_provider_rate_windows_version_idx
    on ai_provider_rate_windows(rate_version_id);

create table ai_call_usage (
    id uuid primary key,
    review_run_id uuid not null,
    stage_position int not null check (stage_position >= 1),
    model_turn int not null check (model_turn >= 1),
    call_index smallint not null check (call_index >= 1),
    provider_id uuid not null,
    model_name text not null,
    sent_at timestamptz not null,
    reasoning_effort text check (reasoning_effort in ('low', 'medium', 'high')),
    max_tokens bigint check (max_tokens between 256 and 200000),
    temperature text,
    rate_version_id uuid,
    rate_window_id uuid,
    rate_source text not null check (rate_source in ('provider_version', 'global_settings', 'none')),
    currency char(3) check (currency ~ '^[A-Z]{3}$'),
    input_tokens bigint check (input_tokens >= 0),
    output_tokens bigint check (output_tokens >= 0),
    cached_tokens bigint check (cached_tokens >= 0),
    cache_write_tokens bigint check (cache_write_tokens >= 0),
    reasoning_tokens bigint check (reasoning_tokens >= 0),
    usage_reported boolean not null,
    reference_amount numeric(24, 8) check (reference_amount >= 0),
    calc_basis text not null check (calc_basis in ('full_detail', 'partial_detail', 'base_only', 'not_computable')),
    detail_note text not null default '',
    outcome text not null check (outcome in ('ok', 'schema_failed', 'error')),
    http_status smallint check (http_status between 200 and 599),
    created_at timestamptz not null
);
create index ai_call_usage_run_idx on ai_call_usage(review_run_id);
create index ai_call_usage_time_idx on ai_call_usage(sent_at);
-- Usage idempotency: call event identity (skips duplicate replays via ON CONFLICT; new re-executions
-- have a distinct sent_at and do not conflict).
create unique index ai_call_usage_event_idx
    on ai_call_usage(review_run_id, stage_position, model_turn, call_index, sent_at);

commit;
