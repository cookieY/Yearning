//go:build integration

package application

// RCP-20261101-EPHEMERAL-PRECHECK migration note (all coverage in this file retired):
//
// TestDatasourceConfigDriftBeforeGroupingFailsClosed legacy premise: StartRun froze flow_snapshot_hash
// into the sql_grouping task payload, and the worker re-read the datasource row and re-verified upon
// claiming — mutating database_name after start but before grouping was rejected with 2007 without
// leaving a run row, converging the draft to failed. TestDatasourceConfigDriftAfterSnapshotStopsBeforeProvider
// legacy premise: grouping and review were two separate tasks — mutating database_name after grouping
// caused the review task to converge the Run to outdated upon frozen-identity re-verification with zero Provider calls.
//
// Ephemeral precheck converges policy freezing, grouping, and stage review into the single in-memory
// boundary of StartEphemeralRun: datasource profiles are frozen into snapshot envelopes (er.Snapshots)
// during ephemeralGrouping, and review/evidence collection consume only the frozen copy, leaving no
// "task queue wait window" for re-read re-verification — mutating datasources rows after launch does
// not affect an in-flight ephemeral Run (safety is guaranteed by freezing rather than re-verification
// rejection), and the next run naturally sees the new profile. DB rows appear only upon submission
// materialization, so the premise of "leaving no run row" (task failing before vs after run row creation)
// no longer exists. Frozen target identity consistency (matchesIdentity -> ErrFactSourceIdentityChanged)
// remains continuously covered by datasource_identity_test.go and the identity_match=matched_at_freeze
// assertions in critical_validity.
