begin;

-- 000006_knowledge_entries R010 internal experience (knowledge base) entries.
-- Lifecycle and invariants align with prompt_tools (PRD 8.8/9.3.2): entries referenced by a Review
-- cannot be physically deleted (enforced at the application layer); each save computes an immutable config_hash;
-- when a Run starts, it matches scope against SQL-referenced objects and freezes the hash snapshot of referenced entries.
create table knowledge_entries (
    id uuid primary key,
    name text not null unique,
    purpose text not null default '',
    state text not null check (state in ('draft', 'enabled', 'disabled')),
    scope_type text not null check (scope_type in ('global', 'datasource', 'table')),
    datasource_id uuid,
    database_name text,
    table_name text,
    definition jsonb not null,
    config_hash char(64) not null check (config_hash ~ '^[0-9a-f]{64}$'),
    version bigint not null default 1 check (version >= 1),
    provenance text not null check (provenance in ('manual', 'finding_conversion')),
    source_finding_id uuid,
    created_by_user_id uuid not null,
    created_at timestamptz not null,
    updated_at timestamptz not null,
    constraint knowledge_scope_fields check (
        (scope_type = 'global' and datasource_id is null and database_name is null and table_name is null)
        or (scope_type = 'datasource' and datasource_id is not null and database_name is not null and table_name is null)
        or (scope_type = 'table' and datasource_id is not null and database_name is not null and table_name is not null)
    ),
    constraint knowledge_provenance_fields check (
        (provenance = 'finding_conversion' and source_finding_id is not null)
        or (provenance = 'manual' and source_finding_id is null)
    )
);

create index knowledge_entries_scope_idx on knowledge_entries(state, scope_type);

commit;
