begin;

-- §18.61 (owner ruling): Persist connection test purpose on the datasource -- table "Test Connection"
-- selects stored credentials by this column, while purpose changes are made in the edit form. Defaults to review;
-- configuration column only, excluded from credentials, snapshots, and fingerprints.
alter table datasources
    add column test_purpose text not null default 'review'
        check (test_purpose in ('review', 'query', 'execution'));

commit;
