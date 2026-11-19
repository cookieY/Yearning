/**
 * Frontend mirror of the grouping limits in the machine contract
 * `api/contracts/sql-fingerprint.json` (grouping block). The frontend
 * repository is a separate git repo and cannot read the YearningX contract at
 * build or runtime, so the values are frozen here and asserted by unit tests
 * (bulk-constants.test.ts). The contract SHA-256 at freeze time is recorded
 * below; an approved Requirement change that edits the contract must update
 * this mirror and its test in the same change (RCP flow) — the server result
 * always remains the final judgement.
 *
 * Contract SHA-256 at freeze: 18ae95d228ca7fdbecc869af2a8eb7362d86537d19b605b9b8445632cd34b8ca
 * (algorithm_version yearning-fingerprint-v1)
 */

/** maximum_bytes — hard upload ceiling per draft (32 MiB). */
export const FINGERPRINT_MAX_INPUT_BYTES = 33554432;
/** max_statement_bytes — single statement ceiling (512 KiB). */
export const FINGERPRINT_MAX_STATEMENT_BYTES = 524288;
/** max_statements — statement count ceiling per draft. */
export const FINGERPRINT_MAX_STATEMENTS = 100000;
/** complexity_warning_unique_fingerprints — pre-review complexity warning. */
export const FINGERPRINT_COMPLEXITY_WARNING_UNIQUE = 200;
/** max_unique_fingerprints — beyond this the draft must be split. */
export const FINGERPRINT_MAX_UNIQUE = 1000;

/**
 * A draft switches from the Monaco editor to the virtualized bulk browser at
 * one max-statement size of SQL or a thousand statements — below that the
 * editor stays the primary surface (frontend PRD F5: the editor remains for
 * normal drafts; bulk display never renders full statements).
 */
export const BULK_MODE_MIN_BYTES = FINGERPRINT_MAX_STATEMENT_BYTES;
export const BULK_MODE_MIN_STATEMENTS = 1000;
