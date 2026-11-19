//go:build integration

package application

// TestStageCommitRollsBackWhenIdentifierMappingFails removed under RCP-20261101.
// Legacy premise: the review stage of the DB task chain wrote the terminal stage watermark, evidence,
// findings, and the review_identifier_mappings envelope within a single transaction — the test corrupted
// the persisted mapping envelope after the final Provider response so the stage commit transaction failed,
// verifying zero residual rows (stage failed, evidence/findings rolled back). In ephemeral precheck,
// stage commit is an in-memory write via ephemeralCommitStage (no DB transaction to fail, and mappings
// live in er.Mapper memory for the lifetime of the Run), while all-or-nothing atomicity of the
// submission materialization transaction is covered by the equivalently migrated
// TestGroupingMapperAndRowsRollBackWithRunTransaction (injecting a fingerprint group insertion failure
// during materialization -> Run / mapping / statements / groups / snapshots all roll back).
