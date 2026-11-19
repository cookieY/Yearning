begin;

-- RCP-20261101-EPHEMERAL-PRECHECK: Ephemeral precheck (zero DB persistence); drafts no longer
-- carry a review state machine. Normalizes existing uncommitted draft states (reviewing/ready/blocked/
-- partial/failed/outdated) to draft while keeping submitted unchanged.
update change_drafts set state = 'draft'
 where state in ('reviewing', 'ready', 'blocked', 'partial', 'failed', 'outdated');

alter table change_drafts drop constraint change_drafts_state_check;
alter table change_drafts add constraint change_drafts_state_check
    check (state in ('draft', 'submitted'));

commit;
