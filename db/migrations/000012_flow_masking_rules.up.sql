begin;

create table flow_masking_rules (
    flow_id uuid not null,
    datasource_id uuid not null,
    sensitive_columns jsonb not null default '[]'::jsonb check (jsonb_typeof(sensitive_columns) = 'array'),
    version bigint not null default 1 check (version >= 1),
    created_at timestamptz not null,
    updated_at timestamptz not null,
    primary key (flow_id, datasource_id)
);

commit;
