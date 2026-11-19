begin;

-- RCP-20261102-S008-AI-USAGE-CALLS-PROVIDER-FILTER: Adds composite index for
-- provider-scoped cursor pagination (provider_id = ? order by sent_at desc, id desc) on ai_call_usage
-- to avoid per-page sequential scan + sort. Index-only migration with no table/column changes or backfill.

create index if not exists ai_call_usage_provider_idx
    on ai_call_usage(provider_id, sent_at desc, id desc);

commit;
