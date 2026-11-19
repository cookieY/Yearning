begin;

-- RCP-20261020-RELEASE-FEED-BACKEND (owner ruling 2026-10-20): Dashboard release banner
-- no longer fetches api.github.com directly from the browser (unauthenticated IP quota of 60/h
-- is easily exhausted). Instead, the backend fetches upstream releases/latest on startup and daily,
-- persisting a single-row snapshot read by the frontend. Failed fetches preserve the prior snapshot
-- (data:null when no snapshot exists yet).
-- current_version (owner addendum, same day): Installed reference version recorded on the first
-- successful fetch after `serve` starts; daily refreshes update only the latest release columns.
create table upstream_release (
    singleton boolean primary key default true check (singleton),
    tag_name text not null,
    release_name text not null default '',
    html_url text not null,
    body text not null default '',
    published_at timestamptz,
    fetched_at timestamptz not null,
    current_version text
);

commit;
