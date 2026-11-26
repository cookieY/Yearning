begin;

update domain_events
set actor = jsonb_set(actor, '{kind}', '"user"'::jsonb, true)
where coalesce(jsonb_typeof(actor->'kind'), '') <> 'string'
   or length(btrim(coalesce(actor->>'kind', ''))) = 0;

alter table domain_events
    add constraint domain_events_actor_kind_nonempty
    check (coalesce(jsonb_typeof(actor->'kind'), '') = 'string' and length(btrim(coalesce(actor->>'kind', ''))) > 0);

commit;
