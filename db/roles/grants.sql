begin;

revoke create on schema public from public;
revoke all on all tables in schema public from public;
alter default privileges for role yearning_schema_owner in schema public revoke all on tables from public;

grant usage on schema public to yearning_app, yearning_retention, yearning_migration_workbench;
grant yearning_schema_owner to yearning_migrator;

grant select on table
    legacy_migration_runs,
    migration_lineage,
    schema_migrations
to yearning_app;

grant select on table legacy_migration_runs, migration_lineage to yearning_migration_workbench;
-- NEW-DEPLOY-13: migration-review login reads users (credential hash / builtin admin flag),
-- session validation joins users, and login/logout writes auth_sessions; without these grants,
-- a contract-deployed workbench role cannot sign in. Grant only the minimum required privileges.
grant select on table users to yearning_migration_workbench;
grant select, insert, update on table auth_sessions to yearning_migration_workbench;
grant update (
    report,
    state,
    approved_manifest_hash,
    approved_by_user_id_snapshot,
    approved_by_username_snapshot,
    approved_at,
    version,
    updated_at
) on table legacy_migration_runs to yearning_migration_workbench;
grant select, insert on table audit_events, domain_events, outbox_events to yearning_migration_workbench;

grant select, insert on table
    ai_call_usage,
    ai_usage_ledger,
    announcement_revisions,
    approval_decisions,
    audit_events,
    domain_events,
    execution_verifications,
    order_comments,
    query_access_decisions,
    evidence_facts,
    evidence_raw_payloads,
    order_approval_actors,
    order_execution_actors,
    order_flow_snapshots,
    order_sql_payloads,
    query_access_approval_actors,
    query_access_request_datasources,
    review_finding_evidence,
    review_findings,
    review_run_sql_bindings,
    sql_fingerprint_groups,
    sql_statements,
    review_tool_calls
to yearning_app;

grant select, insert, update, delete on table
    admin_login_security,
    ai_assistant_context_summaries,
    ai_assistant_messages,
    ai_assistant_sessions,
    ai_assistant_sql_drafts,
    ai_daily_budget_usage,
    ai_provider_rate_versions,
    ai_provider_rate_windows,
    ai_providers,
    announcement_publication,
    async_tasks,
    auth_sessions,
    change_drafts,
    change_orders,
    dashboard_daily_metrics,
    datasource_credentials,
    datasource_diagnostics_configs,
    datasource_tls_materials,
    datasources,
    diagnostics_instance_leases,
    draft_sql_payloads,
    execution_attempts,
    execution_schedules,
    execution_statements,
    external_identities,
    flow_approval_step_actors,
    flow_approval_steps,
    flow_execution_actors,
    flow_masking_rules,
    flow_query_capabilities,
    flow_stages,
    flows,
    idempotency_records,
    identity_providers,
    knowledge_entries,
    notification_channels,
    notification_deliveries,
    notification_rules,
    order_approval_steps,
    order_rollback_evidences,
    order_stages,
    outbox_events,
    permission_group_flow_grants,
    permission_group_members,
    permission_groups,
    prompt_tools,
    query_access_approval_steps,
    query_access_requests,
    query_cursors,
    query_executions,
    query_grant_capabilities,
    query_grants,
    query_session_capabilities,
    query_sessions,
    review_identifier_mappings,
    review_runs,
    rule_set_prompt_tools,
    rule_sets,
    sql_diagnostics_ai_reports,
    sql_diagnostics_bucket_metrics,
    sql_diagnostics_order_links,
    sql_diagnostics_snapshots,
    sql_diagnostics_statements,
    stage_review_snapshots,
    system_settings,
    upstream_release,
    users
to yearning_app;

grant select, delete on table
    audit_events,
    evidence_raw_payloads,
    idempotency_records,
    query_cursors,
    review_finding_evidence,
    review_findings,
    review_run_sql_bindings,
    review_tool_calls,
    evidence_facts,
    sql_statements,
    sql_fingerprint_groups
to yearning_retention;

commit;
