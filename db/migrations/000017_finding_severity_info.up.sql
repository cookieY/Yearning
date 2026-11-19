begin;

-- F12 #009 (BE-B13-F12-CONTRACT-ALIGNMENT, owner ruling A): Machine contract
-- api/contracts/ai-review-output.schema.json includes info in the severity enum, and input
-- max_severity downward-closure semantics (info..max) also expect info-level findings (non-blocking
-- informational severity); this CHECK was the sole outlier. Expands CHECK to include info while keeping
-- stage highest_severity at none..critical (info does not elevate stage highest_severity).
alter table review_findings
    drop constraint review_findings_severity_check;
alter table review_findings
    add constraint review_findings_severity_check
    check (severity in ('info', 'low', 'medium', 'high', 'critical'));

commit;
