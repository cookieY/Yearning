begin;

create table schema_migrations (
    version bigint primary key check (version > 0),
    filename text not null unique,
    sha256 char(64) not null check (sha256 ~ '^[0-9a-f]{64}$'),
    started_at timestamptz not null,
    finished_at timestamptz,
    success boolean not null default false,
    failure_name text
);

create table legacy_migration_runs (
    id uuid primary key,
    source_schema_version text not null,
    manifest_hash char(64) check (manifest_hash is null or manifest_hash ~ '^[0-9a-f]{64}$'),
    state text not null check (state in ('planned', 'dry_run_running', 'awaiting_confirmation', 'approved', 'applying', 'verifying', 'verified', 'failed')),
    initiated_by_user_id uuid,
    report jsonb not null default '{}'::jsonb,
    approved_manifest_hash char(64) check (approved_manifest_hash is null or approved_manifest_hash ~ '^[0-9a-f]{64}$'),
    approved_by_user_id_snapshot uuid,
    approved_by_username_snapshot text,
    approved_at timestamptz,
    version bigint not null default 1 check (version >= 1),
    started_at timestamptz not null,
    updated_at timestamptz not null,
    finished_at timestamptz,
    constraint migration_run_approval_fields check (
        (state in ('approved', 'applying', 'verifying', 'verified') and approved_manifest_hash = manifest_hash and approved_by_user_id_snapshot is not null and approved_by_username_snapshot = 'admin' and approved_at is not null)
        or (state in ('planned', 'dry_run_running', 'awaiting_confirmation') and approved_manifest_hash is null and approved_by_user_id_snapshot is null and approved_by_username_snapshot is null and approved_at is null)
        or (state = 'failed' and (
            (approved_manifest_hash is null and approved_by_user_id_snapshot is null and approved_by_username_snapshot is null and approved_at is null)
            or (approved_manifest_hash = manifest_hash and approved_by_user_id_snapshot is not null and approved_by_username_snapshot = 'admin' and approved_at is not null)
        ))
    ),
    constraint migration_run_manifest_fields check (
        (state in ('planned', 'dry_run_running') and manifest_hash is null)
        or (state in ('awaiting_confirmation', 'approved', 'applying', 'verifying', 'verified') and manifest_hash is not null)
        or state = 'failed'
    ),
    constraint migration_run_terminal_time check (
        (state in ('verified', 'failed') and finished_at is not null)
        or (state not in ('verified', 'failed') and finished_at is null)
    )
);

create table migration_lineage (
    migration_run_id uuid not null,
    source_table text not null,
    source_key text not null,
    source_row_hash char(64) not null check (source_row_hash ~ '^[0-9a-f]{64}$'),
    target_resource_type text not null,
    target_resource_id uuid not null,
    imported_at timestamptz not null,
    primary key (migration_run_id, source_table, source_key, target_resource_type, target_resource_id),
    unique (migration_run_id, target_resource_type, target_resource_id)
);

create table users (
    id uuid primary key,
    username text not null,
    password_hash text,
    display_name text not null,
    email text,
    is_builtin_admin boolean not null default false,
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    updated_at timestamptz not null,
    constraint users_username_format check (username ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'),
    constraint users_admin_identity check (not is_builtin_admin or username = 'admin'),
    constraint users_email_length check (email is null or length(email) <= 320)
);

create unique index users_username_ci_unique on users (lower(username));
create unique index users_single_builtin_admin on users ((is_builtin_admin)) where is_builtin_admin;

create table identity_providers (
    id uuid primary key,
    provider_key text not null unique,
    provider_kind text not null check (provider_kind in ('ldap', 'oidc')),
    display_name text not null check (length(display_name) between 1 and 64),
    enabled boolean not null default false,
    configuration jsonb not null,
    configuration_hash char(64) not null check (configuration_hash ~ '^[0-9a-f]{64}$'),
    secret_ciphertext bytea,
    secret_nonce bytea,
    secret_key_id text,
    secret_algorithm text check (secret_algorithm is null or secret_algorithm = 'aes-256-gcm'),
    version bigint not null default 1 check (version >= 1),
    created_by_user_id uuid not null,
    created_at timestamptz not null,
    updated_at timestamptz not null,
    constraint identity_provider_key_format check (provider_key ~ '^[a-z][a-z0-9-]{0,62}[a-z0-9]$|^[a-z]$'),
    constraint identity_provider_secret_envelope check (
        (secret_ciphertext is null and secret_nonce is null and secret_key_id is null and secret_algorithm is null)
        or (secret_ciphertext is not null and secret_nonce is not null and secret_key_id is not null and secret_algorithm is not null)
    ),
    constraint oidc_provider_requires_secret check (provider_kind <> 'oidc' or secret_ciphertext is not null)
);
create unique index identity_providers_single_ldap on identity_providers(provider_kind) where provider_kind = 'ldap';

create table external_identities (
    id uuid primary key,
    user_id uuid not null,
    provider_kind text not null check (provider_kind in ('oidc', 'ldap')),
    provider_key text not null,
    external_subject text not null,
    created_at timestamptz not null,
    unique (provider_kind, provider_key, external_subject),
    constraint external_identity_cannot_bind_admin check (external_subject <> 'admin')
);

create table auth_sessions (
    id uuid primary key,
    user_id uuid not null,
    token_hash char(64) not null unique check (token_hash ~ '^[0-9a-f]{64}$'),
    csrf_hash char(64) not null check (csrf_hash ~ '^[0-9a-f]{64}$'),
    created_at timestamptz not null,
    last_seen_at timestamptz not null,
    expires_at timestamptz not null,
    revoked_at timestamptz,
    revoke_reason text,
    constraint auth_session_expiry check (expires_at > created_at),
    constraint auth_session_revoke_pair check ((revoked_at is null) = (revoke_reason is null))
);
create index auth_sessions_active_user_idx on auth_sessions(user_id, expires_at) where revoked_at is null;

create table admin_login_security (
    user_id uuid primary key,
    failed_attempts smallint not null default 0 check (failed_attempts between 0 and 5),
    locked_at timestamptz,
    updated_at timestamptz not null,
    constraint admin_lock_consistency check ((failed_attempts = 5) = (locked_at is not null))
);

create table system_settings (
    namespace text primary key check (namespace in ('general', 'query', 'execution', 'ai_budget', 'branding')),
    schema_version integer not null check (schema_version >= 1),
    settings jsonb not null,
    version bigint not null default 1 check (version >= 1),
    updated_by_user_id uuid not null,
    created_at timestamptz not null,
    updated_at timestamptz not null
);

create table notification_channels (
    id uuid primary key,
    kind text not null check (kind in ('email', 'dingtalk')),
    name text not null,
    enabled boolean not null default false,
    configuration jsonb not null default '{}'::jsonb,
    secret_ciphertext bytea,
    secret_nonce bytea,
    secret_key_id text,
    secret_algorithm text check (secret_algorithm is null or secret_algorithm = 'aes-256-gcm'),
    version bigint not null default 1 check (version >= 1),
    created_by_user_id uuid not null,
    created_at timestamptz not null,
    updated_at timestamptz not null,
    unique (kind, name),
    constraint notification_secret_envelope check (
        (secret_ciphertext is null and secret_nonce is null and secret_key_id is null and secret_algorithm is null)
        or (secret_ciphertext is not null and secret_nonce is not null and secret_key_id is not null and secret_algorithm is not null)
    )
);

create table ai_providers (
    id uuid primary key,
    name text not null unique,
    provider_kind text not null,
    base_url text not null,
    model_name text not null,
    enabled boolean not null default false,
    selection_priority smallint not null check (selection_priority between 1 and 100),
    privacy_contract_hash char(64) not null check (privacy_contract_hash ~ '^[0-9a-f]{64}$'),
    output_schema_hash char(64) not null check (output_schema_hash ~ '^[0-9a-f]{64}$'),
    configuration jsonb not null default '{}'::jsonb,
    secret_ciphertext bytea not null,
    secret_nonce bytea not null,
    secret_key_id text not null,
    secret_algorithm text not null check (secret_algorithm = 'aes-256-gcm'),
    version bigint not null default 1 check (version >= 1),
    created_by_user_id uuid not null,
    created_at timestamptz not null,
    updated_at timestamptz not null,
    unique (selection_priority)
);

create table datasources (
    id uuid primary key,
    name text not null,
    engine text not null check (engine in ('mysql', 'postgresql', 'tidb', 'oceanbase', 'polardb')),
    compatibility_mode text not null check (compatibility_mode in ('mysql', 'postgresql')),
    deployment_kind text not null default 'native' check (deployment_kind in ('native', 'cloud')),
    host text not null,
    port integer not null check (port between 1 and 65535),
    database_name text,
    version_constraint text not null,
    enabled boolean not null default true,
    identity_fingerprint char(64) check (identity_fingerprint is null or identity_fingerprint ~ '^[0-9a-f]{64}$'),
    metadata jsonb not null default '{}'::jsonb,
    version bigint not null default 1 check (version >= 1),
    created_by_user_id uuid not null,
    created_at timestamptz not null,
    updated_at timestamptz not null,
    unique (name),
    constraint datasource_engine_mode check (
        (engine in ('mysql', 'tidb', 'oceanbase') and compatibility_mode = 'mysql')
        or (engine = 'postgresql' and compatibility_mode = 'postgresql')
        or (engine = 'polardb')
    )
);

create table datasource_credentials (
    id uuid primary key,
    datasource_id uuid not null,
    purpose text not null check (purpose in ('review', 'query', 'execution')),
    credential_ciphertext bytea not null,
    credential_nonce bytea not null,
    credential_key_id text not null,
    credential_algorithm text not null check (credential_algorithm = 'aes-256-gcm'),
    enabled boolean not null default true,
    created_at timestamptz not null,
    updated_at timestamptz not null
);
create unique index datasource_one_active_credential_per_purpose
    on datasource_credentials(datasource_id, purpose) where enabled;

create table datasource_tls_materials (
    id uuid primary key,
    datasource_id uuid not null,
    purpose text not null check (purpose in ('review', 'query', 'execution')),
    verify_server boolean not null default true,
    server_name text,
    material_ciphertext bytea,
    material_nonce bytea,
    material_key_id text,
    material_algorithm text check (material_algorithm is null or material_algorithm = 'aes-256-gcm'),
    created_at timestamptz not null,
    updated_at timestamptz not null,
    unique (datasource_id, purpose),
    constraint tls_envelope_complete check (
        (material_ciphertext is null and material_nonce is null and material_key_id is null and material_algorithm is null)
        or (material_ciphertext is not null and material_nonce is not null and material_key_id is not null and material_algorithm is not null)
    )
);

create table rule_sets (
    id uuid primary key,
    name text not null unique,
    enabled boolean not null default true,
    rules jsonb not null,
    config_hash char(64) not null check (config_hash ~ '^[0-9a-f]{64}$'),
    version bigint not null default 1 check (version >= 1),
    created_by_user_id uuid not null,
    created_at timestamptz not null,
    updated_at timestamptz not null
);

create table prompt_tools (
    id uuid primary key,
    name text not null unique,
    state text not null check (state in ('draft', 'enabled', 'disabled')),
    definition jsonb not null,
    config_hash char(64) not null check (config_hash ~ '^[0-9a-f]{64}$'),
    version bigint not null default 1 check (version >= 1),
    created_by_user_id uuid not null,
    created_at timestamptz not null,
    updated_at timestamptz not null
);

create table rule_set_prompt_tools (
    rule_set_id uuid not null,
    prompt_tool_id uuid not null,
    primary key (rule_set_id, prompt_tool_id)
);

create table flows (
    id uuid primary key,
    name text not null unique,
    flow_type text not null check (flow_type in ('change_review', 'query_access')),
    status text not null default 'enabled' check (status in ('enabled', 'disabled', 'invalid')),
    rule_set_id uuid,
    version bigint not null default 1 check (version >= 1),
    created_by_user_id uuid not null,
    created_at timestamptz not null,
    updated_at timestamptz not null,
    constraint flow_rule_type check (
        (flow_type = 'change_review' and rule_set_id is not null)
        or (flow_type = 'query_access' and rule_set_id is null)
    )
);

create table flow_stages (
    id uuid primary key,
    flow_id uuid not null,
    position integer not null check (position >= 1),
    datasource_id uuid not null,
    schema_mapping_ciphertext bytea not null,
    schema_mapping_nonce bytea not null,
    schema_mapping_key_id text not null,
    schema_mapping_algorithm text not null check (schema_mapping_algorithm = 'aes-256-gcm'),
    unique (flow_id, position)
);

create table flow_approval_steps (
    id uuid primary key,
    flow_id uuid not null,
    stage_id uuid,
    position integer not null check (position between 1 and 10),
    unique (flow_id, stage_id, position)
);

create unique index flow_query_approval_position_unique
    on flow_approval_steps(flow_id, position) where stage_id is null;

create table flow_approval_step_actors (
    step_id uuid not null,
    user_id uuid not null,
    primary key (step_id, user_id)
);

create table flow_execution_actors (
    stage_id uuid not null,
    user_id uuid not null,
    primary key (stage_id, user_id)
);

create table flow_query_capabilities (
    flow_id uuid not null,
    datasource_id uuid not null,
    can_query boolean not null default true check (can_query),
    can_export boolean not null default false,
    primary key (flow_id, datasource_id)
);

create table permission_groups (
    id uuid primary key,
    name text not null unique,
    enabled boolean not null default true,
    version bigint not null default 1 check (version >= 1),
    created_by_user_id uuid not null,
    created_at timestamptz not null,
    updated_at timestamptz not null
);

create table permission_group_members (
    permission_group_id uuid not null,
    user_id uuid not null,
    primary key (permission_group_id, user_id)
);

create table permission_group_flow_grants (
    permission_group_id uuid not null,
    flow_id uuid not null,
    primary key (permission_group_id, flow_id)
);

commit;
