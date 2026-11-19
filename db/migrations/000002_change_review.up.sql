begin;

create table change_drafts (
    id uuid primary key,
    owner_user_id uuid not null,
    flow_id uuid not null,
    flow_version_seen bigint not null check (flow_version_seen >= 1),
    title text not null check (length(title) between 1 and 256),
    description text not null default '' check (length(description) <= 4096),
    revision bigint not null default 1 check (revision >= 1),
    state text not null default 'draft' check (state in ('draft', 'reviewing', 'ready', 'blocked', 'partial', 'failed', 'outdated', 'submitted')),
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    updated_at timestamptz not null
);
create index change_drafts_owner_updated_idx on change_drafts(owner_user_id, updated_at desc, id desc);

create table draft_sql_payloads (
    draft_id uuid primary key,
    sql_ciphertext bytea not null,
    sql_nonce bytea not null,
    sql_key_id text not null,
    sql_algorithm text not null,
    plaintext_hmac char(64) not null check (plaintext_hmac ~ '^[0-9a-f]{64}$'),
    size_bytes integer not null check (size_bytes between 1 and 33554432),
    statement_count integer not null check (statement_count between 1 and 100000),
    updated_at timestamptz not null
);

create table review_runs (
    id uuid primary key,
    draft_id uuid not null,
    draft_revision bigint not null check (draft_revision >= 1),
    state text not null check (state in ('queued', 'running', 'ready', 'blocked', 'partial', 'failed', 'outdated')),
    flow_snapshot_hash char(64) not null check (flow_snapshot_hash ~ '^[0-9a-f]{64}$'),
    rule_config_hash char(64) not null check (rule_config_hash ~ '^[0-9a-f]{64}$'),
    tool_config_hash char(64) not null check (tool_config_hash ~ '^[0-9a-f]{64}$'),
    provider_plan_hash char(64) not null check (provider_plan_hash ~ '^[0-9a-f]{64}$'),
    fingerprint_algorithm_version text not null,
    anonymization_algorithm_version text not null,
    statement_count integer not null check (statement_count between 1 and 100000),
    fingerprint_group_count integer not null check (fingerprint_group_count between 1 and 1000),
    budget_day date not null,
    business_err_code integer check (business_err_code is null or business_err_code between 1000 and 9999),
    failure_name text,
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    started_at timestamptz,
    finished_at timestamptz,
    unique (draft_id, draft_revision, id),
    constraint review_run_finished_state check (
        (state in ('queued', 'running') and finished_at is null)
        or (state in ('ready', 'blocked', 'partial', 'failed', 'outdated') and finished_at is not null)
    ),
    constraint review_run_failure_fields check (
        (state in ('queued', 'running', 'ready', 'outdated') and business_err_code is null and failure_name is null)
        or (state in ('blocked', 'partial', 'failed') and business_err_code is not null and failure_name is not null)
    )
);
create index review_runs_draft_created_idx on review_runs(draft_id, created_at desc);

create table sql_statements (
    id uuid primary key,
    review_run_id uuid not null,
    ordinal integer not null check (ordinal between 1 and 100000),
    statement_kind text not null check (statement_kind in ('ddl', 'dml')),
    operation text not null,
    size_bytes integer not null check (size_bytes between 1 and 524288),
    algorithm_version text not null,
    dialect_profile text not null,
    fingerprint_quality text not null check (fingerprint_quality in ('exact', 'compatible_lexer')),
    fingerprint_key_id text not null,
    canonical_fingerprint_hmac char(64) not null check (canonical_fingerprint_hmac ~ '^[0-9a-f]{64}$'),
    dialect_fingerprint_hmac char(64) not null check (dialect_fingerprint_hmac ~ '^[0-9a-f]{64}$'),
    execution_shape_hmac char(64) not null check (execution_shape_hmac ~ '^[0-9a-f]{64}$'),
    typed_shape jsonb not null,
    literal_type_counts jsonb not null default '{}'::jsonb,
    tuple_count integer not null default 0 check (tuple_count >= 0),
    parameter_count integer not null default 0 check (parameter_count >= 0),
    unique (review_run_id, ordinal)
);
create index sql_statements_canonical_idx on sql_statements(review_run_id, canonical_fingerprint_hmac);
create index sql_statements_dialect_idx on sql_statements(review_run_id, dialect_fingerprint_hmac);
create index sql_statements_execution_shape_idx on sql_statements(review_run_id, execution_shape_hmac);

create table sql_fingerprint_groups (
    id uuid primary key,
    review_run_id uuid not null,
    canonical_fingerprint_hmac char(64) not null check (canonical_fingerprint_hmac ~ '^[0-9a-f]{64}$'),
    dialect_fingerprint_hmac char(64) not null check (dialect_fingerprint_hmac ~ '^[0-9a-f]{64}$'),
    algorithm_version text not null,
    fingerprint_key_id text not null,
    typed_shape jsonb not null,
    in_value_count_max integer not null default 0 check (in_value_count_max >= 0),
    values_row_count_max integer not null default 0 check (values_row_count_max >= 0),
    statement_count integer not null check (statement_count >= 1),
    representative_statement_id uuid not null,
    unique (review_run_id, canonical_fingerprint_hmac, dialect_fingerprint_hmac)
);

create table stage_review_snapshots (
    id uuid primary key,
    review_run_id uuid not null,
    stage_position integer not null check (stage_position >= 1),
    datasource_id uuid not null,
    datasource_identity_fingerprint char(64) not null check (datasource_identity_fingerprint ~ '^[0-9a-f]{64}$'),
    state text not null check (state in ('pending', 'running', 'passed', 'blocked', 'partial', 'failed')),
    highest_severity text not null check (highest_severity in ('none', 'low', 'medium', 'high', 'critical')),
    gate_passed boolean not null,
    gate_reason_codes text[] not null default '{}',
    normalized_context_ciphertext bytea not null,
    normalized_context_nonce bytea not null,
    normalized_context_key_id text not null,
    normalized_context_algorithm text not null check (normalized_context_algorithm = 'aes-256-gcm'),
    snapshot_hash char(64) not null check (snapshot_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz not null,
    finished_at timestamptz,
    unique (review_run_id, stage_position),
    constraint stage_review_gate_consistency check (
        (state = 'passed' and gate_passed and highest_severity not in ('high', 'critical'))
        or (state <> 'passed' and not gate_passed)
    ),
    constraint stage_review_finished_state check (
        (state in ('pending', 'running') and finished_at is null)
        or (state in ('passed', 'blocked', 'partial', 'failed') and finished_at is not null)
    )
);

create table evidence_facts (
    id uuid primary key,
    stage_review_id uuid not null,
    source_kind text not null,
    source_ref text not null,
    normalized_fact_ciphertext bytea not null,
    normalized_fact_nonce bytea not null,
    normalized_fact_key_id text not null,
    normalized_fact_algorithm text not null check (normalized_fact_algorithm = 'aes-256-gcm'),
    fact_hash char(64) not null check (fact_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz not null,
    unique (stage_review_id, fact_hash)
);

create table evidence_raw_payloads (
    id uuid primary key,
    evidence_fact_id uuid not null,
    raw_payload_ciphertext bytea not null,
    raw_payload_nonce bytea not null,
    raw_payload_key_id text not null,
    raw_payload_algorithm text not null check (raw_payload_algorithm = 'aes-256-gcm'),
    payload_hash char(64) not null check (payload_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz not null,
    expires_at timestamptz not null,
    constraint raw_evidence_retention check (expires_at = created_at + interval '7 days')
);
create index evidence_raw_expiry_idx on evidence_raw_payloads(expires_at);

create table review_findings (
    id uuid primary key,
    stage_review_id uuid not null,
    fingerprint_group_id uuid,
    category text not null,
    severity text not null check (severity in ('low', 'medium', 'high', 'critical')),
    title text not null,
    message text not null,
    suggestion text,
    model_confidence numeric(5,4) check (model_confidence is null or model_confidence between 0 and 1),
    created_at timestamptz not null
);

create table review_finding_evidence (
    finding_id uuid not null,
    evidence_fact_id uuid not null,
    primary key (finding_id, evidence_fact_id)
);

create table ai_usage_ledger (
    id uuid primary key,
    review_run_id uuid not null,
    provider_id uuid not null,
    model_name text not null,
    attempt_number smallint not null check (attempt_number between 1 and 3),
    input_token_count bigint not null check (input_token_count >= 0),
    output_token_count bigint not null check (output_token_count >= 0),
    amount_minor bigint not null check (amount_minor >= 0),
    currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
    budget_day date not null,
    outcome text not null check (outcome in ('succeeded', 'transient_failed', 'schema_failed', 'permanent_failed')),
    created_at timestamptz not null
);
create index ai_usage_budget_idx on ai_usage_ledger(budget_day, currency);

create table change_orders (
    id uuid primary key,
    display_number text not null unique check (display_number ~ '^YR-[0-9]{8}-[0-9]{6}$'),
    submitter_user_id_snapshot uuid not null,
    submitter_username_snapshot text not null,
    submitter_display_name_snapshot text not null,
    title text not null,
    description text not null default '',
    state text not null check (state in (
        'submitted', 'stage_approval_active', 'stage_execution_pending', 'scheduled', 'running',
        'completed', 'rejected', 'withdrawn', 'withdrawn_after_partial_execution', 'voided',
        'failed', 'partial_failed', 'cancelled', 'partial_cancelled', 'result_unknown',
        'blocked_datasource_unavailable', 'missed_schedule', 'invalid'
    )),
    current_stage_position integer check (current_stage_position is null or current_stage_position >= 1),
    sql_hmac char(64) not null check (sql_hmac ~ '^[0-9a-f]{64}$'),
    snapshot_hash char(64) not null check (snapshot_hash ~ '^[0-9a-f]{64}$'),
    manually_verified boolean not null default false,
    legacy_snapshot_completeness text check (legacy_snapshot_completeness in ('complete', 'partial', 'minimal')),
    version bigint not null default 1 check (version >= 1),
    submitted_at timestamptz not null,
    terminal_at timestamptz,
    constraint order_terminal_time check (
        (state in ('submitted', 'stage_approval_active', 'stage_execution_pending', 'scheduled', 'running', 'result_unknown', 'blocked_datasource_unavailable') and terminal_at is null)
        or (state not in ('submitted', 'stage_approval_active', 'stage_execution_pending', 'scheduled', 'running', 'result_unknown', 'blocked_datasource_unavailable') and terminal_at is not null)
    )
);
create index change_orders_submitter_idx on change_orders(submitter_user_id_snapshot, submitted_at desc, id desc);
create index change_orders_state_idx on change_orders(state, submitted_at desc, id desc);

create table order_flow_snapshots (
    order_id uuid primary key,
    flow_snapshot_ciphertext bytea not null,
    flow_snapshot_nonce bytea not null,
    flow_snapshot_key_id text not null,
    flow_snapshot_algorithm text not null check (flow_snapshot_algorithm = 'aes-256-gcm'),
    snapshot_hash char(64) not null check (snapshot_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz not null
);

create table order_sql_payloads (
    order_id uuid primary key,
    sql_ciphertext bytea not null,
    sql_nonce bytea not null,
    sql_key_id text not null,
    sql_algorithm text not null,
    plaintext_hmac char(64) not null check (plaintext_hmac ~ '^[0-9a-f]{64}$'),
    size_bytes integer not null check (size_bytes between 1 and 33554432),
    statement_count integer not null check (statement_count between 1 and 100000),
    created_at timestamptz not null
);

create table order_stages (
    id uuid primary key,
    order_id uuid not null,
    position integer not null check (position >= 1),
    datasource_id uuid not null,
    datasource_name_snapshot text not null,
    datasource_engine_snapshot text not null,
    datasource_identity_fingerprint char(64) not null check (datasource_identity_fingerprint ~ '^[0-9a-f]{64}$'),
    stage_review_snapshot_id uuid not null,
    review_snapshot_hash char(64) not null check (review_snapshot_hash ~ '^[0-9a-f]{64}$'),
    review_gate_passed boolean not null check (review_gate_passed),
    target_snapshot_ciphertext bytea not null,
    target_snapshot_nonce bytea not null,
    target_snapshot_key_id text not null,
    target_snapshot_algorithm text not null check (target_snapshot_algorithm = 'aes-256-gcm'),
    state text not null check (state in ('pending', 'approval_active', 'execution_pending', 'scheduled', 'running', 'succeeded', 'failed', 'partial_failed', 'cancelled', 'partial_cancelled', 'result_unknown', 'skipped')),
    version bigint not null default 1 check (version >= 1),
    activated_at timestamptz,
    terminal_at timestamptz,
    unique (order_id, position),
    constraint order_stage_time_consistency check (
        (state = 'pending' and activated_at is null and terminal_at is null)
        or (state in ('approval_active', 'execution_pending', 'scheduled', 'running', 'result_unknown') and activated_at is not null and terminal_at is null)
        or (state in ('succeeded', 'failed', 'partial_failed', 'cancelled', 'partial_cancelled') and activated_at is not null and terminal_at is not null)
        or (state = 'skipped' and activated_at is null and terminal_at is not null)
    )
);
create index order_stages_active_idx on order_stages(state, id) where state in ('approval_active', 'execution_pending', 'scheduled', 'running', 'result_unknown');
create unique index order_one_live_stage
    on order_stages(order_id)
    where state in ('approval_active', 'execution_pending', 'scheduled', 'running', 'result_unknown');

create table order_approval_steps (
    id uuid primary key,
    stage_id uuid not null,
    position integer not null check (position between 1 and 10),
    state text not null check (state in ('pending', 'active', 'approved', 'rejected', 'skipped', 'invalid')),
    decided_at timestamptz,
    unique (stage_id, position),
    constraint order_approval_step_decision_time check (
        (state in ('pending', 'active') and decided_at is null)
        or (state in ('approved', 'rejected', 'skipped', 'invalid') and decided_at is not null)
    )
);

create table order_approval_actors (
    step_id uuid not null,
    reviewer_user_id_snapshot uuid not null,
    username_snapshot text not null,
    display_name_snapshot text not null,
    primary key (step_id, reviewer_user_id_snapshot)
);

create unique index order_one_active_approval_step
    on order_approval_steps(stage_id)
    where state = 'active';

create table approval_decisions (
    id uuid primary key,
    order_id uuid not null,
    stage_id uuid not null,
    step_id uuid not null,
    reviewer_user_id_snapshot uuid not null,
    reviewer_username_snapshot text not null,
    decision text not null check (decision in ('approve', 'reject')),
    comment text not null default '' check (length(comment) <= 4096),
    occurred_at timestamptz not null,
    unique (step_id, reviewer_user_id_snapshot)
);

create unique index approval_step_single_effective_decision
    on approval_decisions(step_id);

create table order_execution_actors (
    stage_id uuid not null,
    executor_user_id_snapshot uuid not null,
    username_snapshot text not null,
    display_name_snapshot text not null,
    primary key (stage_id, executor_user_id_snapshot)
);

create table execution_attempts (
    id uuid primary key,
    order_id uuid not null,
    stage_id uuid not null,
    initiated_by_user_id_snapshot uuid not null,
    initiated_by_username_snapshot text not null,
    execution_kind text not null check (execution_kind in ('ddl', 'dml', 'gh_ost')),
    state text not null check (state in ('created', 'preflight', 'running', 'cancelling', 'succeeded', 'failed', 'partial_failed', 'cancelled', 'partial_cancelled', 'result_unknown')),
    send_boundary text not null check (send_boundary in ('not_started', 'sending', 'sent')),
    fence_token bigint not null check (fence_token >= 1),
    lease_owner text,
    lease_expires_at timestamptz,
    started_at timestamptz,
    finished_at timestamptz,
    business_err_code integer check (business_err_code is null or business_err_code between 1000 and 9999),
    failure_name text,
    osc_tool_version text,
    osc_binary_sha256 char(64) check (osc_binary_sha256 is null or osc_binary_sha256 ~ '^[0-9a-f]{64}$'),
    osc_plan_hash char(64) check (osc_plan_hash is null or osc_plan_hash ~ '^[0-9a-f]{64}$'),
    osc_plan_ciphertext bytea,
    osc_plan_nonce bytea,
    osc_plan_key_id text,
    osc_plan_algorithm text check (osc_plan_algorithm is null or osc_plan_algorithm = 'aes-256-gcm'),
    osc_phase text check (osc_phase is null or osc_phase in ('planned', 'preflight', 'copying', 'postponed_cutover', 'cutover', 'cleanup', 'succeeded', 'failed', 'cancelled', 'result_unknown')),
    osc_progress_basis_points integer check (osc_progress_basis_points is null or osc_progress_basis_points between 0 and 10000),
    osc_rows_copied bigint check (osc_rows_copied is null or osc_rows_copied >= 0),
    osc_residual_state text check (osc_residual_state is null or osc_residual_state in ('not_checked', 'none', 'detected', 'cleanup_required', 'cleaned')),
    osc_last_heartbeat_at timestamptz,
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    constraint execution_lease_pair check ((lease_owner is null) = (lease_expires_at is null)),
    constraint execution_attempt_time_consistency check (
        (state = 'created' and started_at is null and finished_at is null)
        or (state in ('preflight', 'running', 'cancelling') and started_at is not null and finished_at is null)
        or (state in ('succeeded', 'failed', 'partial_failed', 'cancelled', 'partial_cancelled', 'result_unknown') and started_at is not null and finished_at is not null)
    ),
    constraint execution_attempt_failure_fields check (
        (state in ('created', 'preflight', 'running', 'cancelling', 'succeeded') and business_err_code is null and failure_name is null)
        or (state in ('failed', 'partial_failed', 'cancelled', 'partial_cancelled', 'result_unknown') and business_err_code is not null and failure_name is not null)
    ),
    constraint execution_attempt_osc_fields check (
        (execution_kind = 'gh_ost'
            and osc_tool_version is not null
            and osc_binary_sha256 is not null
            and osc_plan_hash is not null
            and osc_plan_ciphertext is not null
            and osc_plan_nonce is not null
            and osc_plan_key_id is not null
            and osc_plan_algorithm is not null
            and osc_phase is not null
            and osc_progress_basis_points is not null
            and osc_rows_copied is not null
            and osc_residual_state is not null)
        or (execution_kind <> 'gh_ost'
            and osc_tool_version is null
            and osc_binary_sha256 is null
            and osc_plan_hash is null
            and osc_plan_ciphertext is null
            and osc_plan_nonce is null
            and osc_plan_key_id is null
            and osc_plan_algorithm is null
            and osc_phase is null
            and osc_progress_basis_points is null
            and osc_rows_copied is null
            and osc_residual_state is null
            and osc_last_heartbeat_at is null)
    )
);
create unique index execution_stage_one_non_not_started_attempt
    on execution_attempts(stage_id)
    where send_boundary <> 'not_started';
create unique index execution_stage_one_active_attempt
    on execution_attempts(stage_id)
    where state in ('created', 'preflight', 'running', 'cancelling', 'result_unknown');

create table execution_statements (
    id uuid primary key,
    attempt_id uuid not null,
    ordinal integer not null check (ordinal between 1 and 100000),
    statement_kind text not null check (statement_kind in ('ddl', 'dml')),
    state text not null check (state in ('not_started', 'sent', 'succeeded', 'failed', 'cancelled', 'unknown', 'skipped')),
    affected_row_count bigint check (affected_row_count is null or affected_row_count >= 0),
    started_at timestamptz,
    finished_at timestamptz,
    business_err_code integer check (business_err_code is null or business_err_code between 1000 and 9999),
    failure_name text,
    error_detail_ciphertext bytea,
    error_nonce bytea,
    error_key_id text,
    error_algorithm text check (error_algorithm is null or error_algorithm = 'aes-256-gcm'),
    unique (attempt_id, ordinal),
    constraint execution_error_envelope check (
        (error_detail_ciphertext is null and error_nonce is null and error_key_id is null and error_algorithm is null)
        or (error_detail_ciphertext is not null and error_nonce is not null and error_key_id is not null and error_algorithm is not null)
    ),
    constraint execution_failure_fields check (
        (state not in ('failed', 'unknown') and business_err_code is null and failure_name is null and error_detail_ciphertext is null)
        or (state in ('failed', 'unknown') and business_err_code is not null and failure_name is not null)
    )
);

create table execution_schedules (
    id uuid primary key,
    order_id uuid not null,
    stage_id uuid not null,
    scheduled_by_user_id_snapshot uuid not null,
    scheduled_by_username_snapshot text not null,
    scheduled_for timestamptz not null,
    state text not null check (state in ('scheduled', 'running', 'cancelled', 'missed', 'blocked')),
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    updated_at timestamptz not null
);
create unique index execution_stage_one_live_schedule
    on execution_schedules(stage_id) where state = 'scheduled';
create index execution_schedules_due_idx
    on execution_schedules(scheduled_for, id) where state = 'scheduled';

create table execution_verifications (
    id uuid primary key,
    attempt_id uuid not null,
    verified_by_user_id_snapshot uuid not null,
    verified_by_username_snapshot text not null,
    result text not null check (result in ('confirmed_succeeded', 'confirmed_failed', 'confirmed_partial', 'still_unknown')),
    reason_ciphertext bytea not null,
    reason_nonce bytea not null,
    reason_key_id text not null,
    reason_algorithm text not null check (reason_algorithm = 'aes-256-gcm'),
    evidence_bundle_ciphertext bytea not null,
    evidence_nonce bytea not null,
    evidence_key_id text not null,
    evidence_algorithm text not null check (evidence_algorithm = 'aes-256-gcm'),
    occurred_at timestamptz not null
);

create table order_comments (
    id uuid primary key,
    order_id uuid not null,
    author_user_id_snapshot uuid not null,
    author_username_snapshot text not null,
    content text not null check (length(content) between 1 and 4096),
    occurred_at timestamptz not null
);

commit;
