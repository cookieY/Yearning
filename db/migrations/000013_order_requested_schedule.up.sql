begin;

-- §18.56 (Option B): Submitter requested execution time. Carried on drafts and frozen onto the order at submission;
-- display/prefill metadata only, excluded from snapshot hashes and state machines.
alter table change_drafts
    add column requested_scheduled_for timestamptz;

alter table change_orders
    add column requested_scheduled_for timestamptz;

commit;
