begin;

-- 000008_review_run_sql_bindings B8 Submission Gate completeness:
-- complete_ai_precheck preconditions require the review to bind the exact draft revision
-- and SQL hash (repository-commands). TaskInput freezes sql_hmac at launch, and this migration
-- persists the binding in an insert-only fact table (domain insert-only fact pattern; no column ALTERs
-- so the validator derives column coverage from CREATE TABLE).
-- Rows are immutable: bindings are created only in the grouping transaction and read-only during convergence and submission recheck.
create table review_run_sql_bindings (
    review_run_id uuid primary key,
    sql_payload_hmac char(64) not null check (sql_payload_hmac ~ '^[0-9a-f]{64}$'),
    created_at timestamptz not null
);

commit;
