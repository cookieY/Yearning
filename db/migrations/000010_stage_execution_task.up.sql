begin;

-- B10 in-process Execution Service: adds stage_execution to async_tasks kind.
-- Pure CHECK enum extension (default inline constraint name async_tasks_kind_check);
-- existing kinds remain unchanged; execution attempt/ledger/schedule/verification tables were frozen in 000002,
-- so this migration adds zero new tables and zero new columns.
alter table async_tasks drop constraint async_tasks_kind_check;
alter table async_tasks
    add constraint async_tasks_kind_check
    check (kind in ('sql_grouping', 'ai_review', 'migration_dry_run', 'admin_connection_test',
                    'ai_provider_connection_test', 'identity_provider_conn_test',
                    'notification_test_delivery', 'stage_execution'));

commit;
