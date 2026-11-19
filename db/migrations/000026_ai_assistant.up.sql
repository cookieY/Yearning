begin;

-- Database AI Assistant (v4.0.0 controlled platform help, natural-language SQL generation, and SQL optimization):
-- 1. ai_assistant_sessions: Persists user session metadata and encrypted session titles (30-day default retention,
--    up to 50 active sessions per user and 100 messages per session);
-- 2. ai_assistant_messages: Persists AES-256-GCM encrypted message bodies, source ref-links, and status;
-- 3. ai_assistant_sql_drafts: Persists dialect-AST-validated and structurally restored encrypted SQL drafts,
--    sql_hmac, key assumptions, non-equivalence warnings, validation suggestions, and review/order bindings;
-- 4. ai_assistant_context_summaries: Persists encrypted redacted context summaries generated near the token budget
--    (containing no redaction mapping tables and invalidated on message mutation or permission change).
-- Per-turn in-memory redaction mappings (TurnPrivacySession) are never persisted.

create table ai_assistant_sessions (
    id uuid primary key,
    user_id uuid not null,
    title_ciphertext bytea not null,
    title_nonce bytea not null,
    title_key_id text not null,
    title_algorithm text not null check (title_algorithm = 'aes-256-gcm'),
    state text not null check (state in ('active', 'deleted', 'expired')),
    active_datasource_id uuid,
    active_flow_id uuid,
    active_schema_name text,
    selected_draft_id uuid,
    compacted_through_seq bigint not null default 0 check (compacted_through_seq >= 0),
    expires_at timestamptz not null,
    deleted_at timestamptz,
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    updated_at timestamptz not null
);

create index ai_assistant_sessions_owner_active_idx
    on ai_assistant_sessions(user_id, updated_at desc)
    where state = 'active' and deleted_at is null;

create index ai_assistant_sessions_expires_idx
    on ai_assistant_sessions(expires_at)
    where state = 'active' and deleted_at is null;

create table ai_assistant_messages (
    id uuid primary key,
    session_id uuid not null,
    user_id uuid not null,
    sequence bigint not null check (sequence > 0 and sequence <= 100),
    role text not null check (role in ('user', 'assistant', 'system')),
    capability text not null check (
        capability in ('platform_help', 'sql_generate', 'sql_optimize', 'out_of_scope')
    ),
    state text not null check (
        state in ('streaming', 'completed', 'cancelled', 'failed')
    ),
    content_ciphertext bytea not null,
    content_nonce bytea not null,
    content_key_id text not null,
    content_algorithm text not null check (content_algorithm = 'aes-256-gcm'),
    sql_draft_id uuid,
    failure_code integer,
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    updated_at timestamptz not null,
    unique (session_id, sequence)
);

create index ai_assistant_messages_session_seq_idx
    on ai_assistant_messages(session_id, sequence);

create table ai_assistant_sql_drafts (
    id uuid primary key,
    session_id uuid not null,
    message_id uuid not null,
    user_id uuid not null,
    revision bigint not null check (revision > 0),
    datasource_id uuid not null,
    flow_id uuid,
    engine text not null check (
        engine in ('mysql', 'postgresql', 'tidb', 'oceanbase', 'polardb')
    ),
    schema_name text not null,
    statement_kind text not null check (
        statement_kind in (
            'select',
            'insert',
            'update',
            'delete',
            'create_table',
            'alter_table',
            'create_index',
            'drop_index'
        )
    ),
    risk_category text not null check (
        risk_category in ('read_only', 'dml', 'ddl')
    ),
    sql_ciphertext bytea not null,
    sql_nonce bytea not null,
    sql_key_id text not null,
    sql_algorithm text not null check (sql_algorithm = 'aes-256-gcm'),
    sql_hmac bytea not null,
    sql_hmac_key_id text not null,
    submission_state text not null check (
        submission_state in (
            'draft',
            'precheck_running',
            'precheck_ready',
            'precheck_blocked',
            'order_submitted',
            'stale'
        )
    ),
    ephemeral_run_id uuid,
    submitted_order_id uuid,
    submitted_order_display_no text,
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    updated_at timestamptz not null
);

create index ai_assistant_sql_drafts_session_idx
    on ai_assistant_sql_drafts(session_id, revision desc);

create index ai_assistant_sql_drafts_message_idx
    on ai_assistant_sql_drafts(message_id);

create table ai_assistant_context_summaries (
    id uuid primary key,
    session_id uuid not null,
    covered_from_seq bigint not null check (covered_from_seq > 0),
    covered_through_seq bigint not null check (covered_through_seq >= covered_from_seq),
    source_version bigint not null check (source_version >= 1),
    summary_ciphertext bytea not null,
    summary_nonce bytea not null,
    summary_key_id text not null,
    summary_algorithm text not null check (summary_algorithm = 'aes-256-gcm'),
    invalidated boolean not null default false,
    created_at timestamptz not null
);

create index ai_assistant_context_summaries_active_idx
    on ai_assistant_context_summaries(session_id, source_version desc)
    where invalidated = false;

commit;
