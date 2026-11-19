begin;

-- F12 #006 (BE-B13-F12-CONTRACT-ALIGNMENT): Contract ChangeOrder.stages[].execution_actors
-- ref-links the full User shape (is_builtin_admin/version/created_at/updated_at required),
-- whereas the frozen snapshot table only stored id/username/display_name. Adds snapshot columns per W004:
-- frozen alongside username/display_name at submission so reads do not join the live users table (user deletion
-- must not alter historical order views, W008).
alter table order_execution_actors
    add column is_builtin_admin_snapshot boolean not null default false,
    add column user_version_snapshot bigint not null default 1,
    add column user_created_at_snapshot timestamptz not null default '1970-01-01T00:00:00Z',
    add column user_updated_at_snapshot timestamptz not null default '1970-01-01T00:00:00Z';

-- Backfill existing rows (orphan snapshots whose user was deleted retain conservative defaults per W008
-- rather than failing on join).
update order_execution_actors ea
set is_builtin_admin_snapshot = u.is_builtin_admin,
    user_version_snapshot = u.version,
    user_created_at_snapshot = u.created_at,
    user_updated_at_snapshot = u.updated_at
from users u
where u.id = ea.executor_user_id_snapshot;

commit;
