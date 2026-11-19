begin;

-- S007 notification rule custom template support (RCP-20261110-NOTIFICATION-RULE-TEMPLATES):
-- Optional per-rule message_template using allowlisted {{placeholder}} variables;
-- NULL means the rule uses the built-in default template for its event_type.
alter table notification_rules
    add column message_template text check (message_template is null or char_length(message_template) <= 2000);

commit;
