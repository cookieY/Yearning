begin;

-- 000007_review_tool_calls B7 runtime tool call records and review input ref-links.
-- Three tool_kind categories: builtin_database_fact rows record read-only evidence tool invocations
-- (success/failure/duration/error code, excluding payload -- normalized facts live in evidence_facts);
-- prompt_tool and knowledge_entry rows freeze deterministically injected ref-links
-- (input_ref_id+config_hash) for plaintext queries checking "outdated on change" and
-- "physical deletion forbidden when referenced".
create table review_tool_calls (
    id uuid primary key,
    review_run_id uuid not null,
    stage_review_id uuid,
    tool_kind text not null check (tool_kind in ('builtin_database_fact', 'prompt_tool', 'knowledge_entry')),
    tool_name text not null,
    input_ref_id uuid,
    config_hash char(64),
    success boolean not null,
    error_code int,
    started_at timestamptz not null,
    finished_at timestamptz not null,
    constraint review_tool_call_ref_fields check (
        (tool_kind = 'builtin_database_fact' and input_ref_id is null and config_hash is null)
        or (tool_kind in ('prompt_tool', 'knowledge_entry') and input_ref_id is not null and config_hash ~ '^[0-9a-f]{64}$')
    )
);

create index review_tool_calls_run_idx on review_tool_calls(review_run_id);
create index review_tool_calls_ref_idx on review_tool_calls(tool_kind, input_ref_id, config_hash);

commit;
