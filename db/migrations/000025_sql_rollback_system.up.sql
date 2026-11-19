begin;

-- Unified MySQL and PostgreSQL SQL rollback system (DML transaction snapshot / RETURNING capture +
-- DDL metadata snapshot and inverse DDL + rollback evidence AEAD envelope + one-click rollback order pipeline):
-- 1. Extends change_drafts / change_orders with rollback toggle (default true, false for rollback orders),
--    order kind (normal / rollback), source order and evidence ref-links, and order rollback lifecycle state;
-- 2. Persists order_rollback_evidences captured during protected execution attempts (table schema, primary keys,
--    before/after images, and inverse rollback SQL list sealed inside an AES-256-GCM envelope with AAD bound to
--    order_rollback_evidences and evidence ID), never persisting plaintext business rows or rollback SQL.

alter table change_drafts
    add column rollback_enabled boolean not null default true;

alter table change_drafts
    add column ticket_type text not null default 'normal' check (ticket_type in ('normal', 'rollback'));

alter table change_drafts
    add column source_order_id uuid;

alter table change_drafts
    add column rollback_evidence_id uuid;

alter table change_orders
    add column rollback_enabled boolean not null default true;

alter table change_orders
    add column ticket_type text not null default 'normal' check (ticket_type in ('normal', 'rollback'));

alter table change_orders
    add column source_order_id uuid;

alter table change_orders
    add column rollback_evidence_id uuid;

alter table change_orders
    add column rollback_status text not null default 'not_requested' check (
        rollback_status in (
            'not_requested',
            'pending',
            'available',
            'unsupported',
            'evidence_incomplete',
            'conflicted',
            'rollback_in_progress',
            'rolled_back',
            'rollback_failed'
        )
    );

create index change_orders_source_order_idx
    on change_orders(source_order_id)
    where source_order_id is not null;

create table order_rollback_evidences (
    id uuid primary key,
    order_id uuid not null,
    stage_id uuid not null,
    attempt_id uuid not null,
    datasource_id uuid not null,
    database_engine text not null check (database_engine in ('mysql', 'postgresql')),
    database_version text not null,
    schema_name text not null,
    operation_type text not null check (operation_type in ('dml', 'ddl')),
    rollback_status text not null check (
        rollback_status in (
            'pending',
            'available',
            'unsupported',
            'evidence_incomplete',
            'conflicted',
            'rollback_in_progress',
            'rolled_back',
            'rollback_failed'
        )
    ),
    unsupported_reason text,
    conflict_reason text,
    affected_rows bigint not null default 0 check (affected_rows >= 0),
    rollback_payload_ciphertext bytea not null,
    rollback_payload_nonce bytea not null,
    rollback_payload_key_id text not null,
    rollback_payload_algorithm text not null check (rollback_payload_algorithm = 'aes-256-gcm'),
    rollback_sql_hmac text not null,
    created_at timestamptz not null,
    updated_at timestamptz not null,
    unique (attempt_id)
);

create index order_rollback_evidences_order_idx
    on order_rollback_evidences(order_id);

create index order_rollback_evidences_stage_idx
    on order_rollback_evidences(stage_id);

commit;
