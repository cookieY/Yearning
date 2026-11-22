begin;

-- PERF-0006: Adds non-intrusive performance indexes supporting OperationsDashboard
-- statement_kind grouping, batched EXISTS DDL snapshot checks, change_orders
-- relation-filtered cursor pagination, latest execution attempt and rollback evidence
-- lookups, and personal workbench reviewer/executor actor queries.
-- Index-only migration with no table, column, or constraint changes.

create index if not exists sql_statements_kind_idx
    on sql_statements(statement_kind);

create index if not exists sql_statements_run_ddl_idx
    on sql_statements(review_run_id)
    where statement_kind = 'ddl';

create index if not exists change_orders_submitted_idx
    on change_orders(submitted_at desc, id desc);

create index if not exists execution_attempts_order_created_idx
    on execution_attempts(order_id, created_at desc);

create index if not exists order_rollback_evidences_order_created_idx
    on order_rollback_evidences(order_id, created_at desc);

create index if not exists order_approval_actors_reviewer_idx
    on order_approval_actors(reviewer_user_id_snapshot, step_id);

create index if not exists order_execution_actors_executor_idx
    on order_execution_actors(executor_user_id_snapshot, stage_id);

commit;
