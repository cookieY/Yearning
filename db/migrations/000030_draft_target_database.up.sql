begin;

alter table change_drafts
    add column if not exists target_database text
    check (target_database is null or (char_length(target_database) between 1 and 128));

commit;
