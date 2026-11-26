begin;

-- 000032_knowledge_nonblocking_scope retires global internal experience scope (RCP-20261126-KNOWLEDGE-ASSISTANT-SYNC):
-- 1) Transition non-outdated review runs on unsubmitted drafts referencing enabled global entries to outdated.
-- 2) Disable all legacy enabled global knowledge entries (preserving historical rows and CHECK compatibility).
-- 3) Add partial index knowledge_entries_enabled_ds_idx for constant-query datasource/table lookups.

update review_runs r
set state = 'outdated',
    version = r.version + 1,
    finished_at = coalesce(r.finished_at, now()),
    business_err_code = null,
    failure_name = null
from change_drafts d
where d.id = r.draft_id
  and d.state <> 'submitted'
  and r.state <> 'outdated'
  and exists (
      select 1
      from review_tool_calls c
      join knowledge_entries k on k.id = c.input_ref_id
      where c.review_run_id = r.id
        and c.tool_kind = 'knowledge_entry'
        and k.scope_type = 'global'
        and k.state = 'enabled'
  );

update knowledge_entries
set state = 'disabled',
    version = version + 1,
    updated_at = now()
where scope_type = 'global'
  and state = 'enabled';

create index if not exists knowledge_entries_enabled_ds_idx
    on knowledge_entries (datasource_id, scope_type)
    where state = 'enabled';

commit;
