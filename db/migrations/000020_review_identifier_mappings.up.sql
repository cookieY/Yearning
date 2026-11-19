begin;

-- 000020_review_identifier_mappings NEW-AI-01: Local AEAD envelope storage for
-- Review Run anonymized identifier mappings (wires privacy/envelope.go SealMapping/OpenMapping).
-- Contract (api/contracts/ai-review-runtime-contract.json input_privacy
-- .identifier_mapping_storage=local_AEAD_envelope_never_provider;
-- docs/contracts/ai-sql-review-accuracy-and-security-contract.md §3.3
-- encrypted_and_bound_to_review_run, identifier_mapping_lifetime=
-- review_and_frozen_order_retention):
--   - Envelope columns store privacy.SealMapping output (AES-256-GCM ciphertext of
--     "kind\ttoken\toriginal" lines); AAD binds deployment ID, table name
--     review_identifier_mappings (privacy.MappingTable), primary key
--     "{review_run_id}#{statement_idx}", and column identifier_mapping
--     (privacy.MappingColumn) -- cross-Run or cross-record substitution is rejected;
--   - Run-level mappings persist a single row with sentinel statement_idx=1; the review Runner
--     idempotently reseals after stage facts persist (crash replay restores stored mappings before extending);
--   - Plaintext mappings and raw identifiers never enter Provider requests, traces, or logs
--     (model_must_never_receive: identifier_mapping); authorized read-side reveal is covered by
--     RCP-20261022-IDENTIFIER-MAPPING-REVEAL.
create table review_identifier_mappings (
    id uuid primary key,
    review_run_id uuid not null,
    statement_idx int not null check (statement_idx >= 1),
    identifier_mapping_ciphertext bytea not null,
    identifier_mapping_nonce bytea not null,
    identifier_mapping_key_id text not null,
    identifier_mapping_algorithm text not null check (identifier_mapping_algorithm = 'aes-256-gcm'),
    mapping_entry_count int not null check (mapping_entry_count >= 0),
    created_at timestamptz not null,
    constraint review_identifier_mappings_run_statement_key unique (review_run_id, statement_idx)
);

create index review_identifier_mappings_run_idx on review_identifier_mappings(review_run_id);

commit;
