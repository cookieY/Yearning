begin;

-- RCP-20260827-OSC-PARAMS (approved): Dual OSC tool support. Extends execution_kind
-- with pt_osc; updates the osc_* column group CHECK constraint to cover both OSC tools
-- (column semantics unchanged: osc_tool_version records the specific tool and version).
alter table execution_attempts drop constraint execution_attempts_execution_kind_check;
alter table execution_attempts
    add constraint execution_attempts_execution_kind_check
    check (execution_kind in ('ddl', 'dml', 'gh_ost', 'pt_osc'));

alter table execution_attempts drop constraint execution_attempt_osc_fields;
alter table execution_attempts
    add constraint execution_attempt_osc_fields check (
        (execution_kind in ('gh_ost', 'pt_osc')
            and osc_tool_version is not null
            and osc_binary_sha256 is not null
            and osc_plan_hash is not null
            and osc_plan_ciphertext is not null
            and osc_plan_nonce is not null
            and osc_plan_key_id is not null
            and osc_plan_algorithm is not null
            and osc_phase is not null
            and osc_progress_basis_points is not null
            and osc_rows_copied is not null
            and osc_residual_state is not null)
        or (execution_kind not in ('gh_ost', 'pt_osc')
            and osc_tool_version is null
            and osc_binary_sha256 is null
            and osc_plan_hash is null
            and osc_plan_ciphertext is null
            and osc_plan_nonce is null
            and osc_plan_key_id is null
            and osc_plan_algorithm is null
            and osc_phase is null
            and osc_progress_basis_points is null
            and osc_rows_copied is null
            and osc_residual_state is null
            and osc_last_heartbeat_at is null)
    );

commit;
