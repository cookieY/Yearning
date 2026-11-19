begin;

-- RCP-20260826-01 (approved): Auxiliary indexes for orphan sweep.
-- The application role retains select+insert on review fact tables; after an uncommitted draft is deleted,
-- retention sweeps unreachable child rows via anti-joins in dependency order. Two indexes support sweep probes:
-- review_findings by stage, and review_finding_evidence by fact.

create index review_findings_stage_idx on review_findings(stage_review_id);
create index review_finding_evidence_fact_idx on review_finding_evidence(evidence_fact_id);

commit;
