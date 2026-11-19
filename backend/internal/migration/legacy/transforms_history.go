package legacy

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/crypto"
)

// Historical domain transformations: only terminal business records are migrated; active, unknown, or
// unadjudicated states block Apply. The execution chain (draft -> review_run -> stage_review_snapshot ->
// order_stage -> execution_attempt) is constructed only for tickets that actually executed; review source
// is tagged as legacy (rule-based review) without generating any AI facts.

// TransformSQLOrder transforms a single legacy SQL ticket. ok=false indicates quarantine; blocking=true blocks Apply.
func (tc *TransformContext) TransformSQLOrder(row Row, ev *StatusEvidence) (ok bool, blocking bool, err error) {
	key := rowString(row["id"])
	workID := strings.TrimSpace(rowString(row["work_id"]))
	if workID == "" {
		tc.quarantine("core_sql_orders", key, "empty work_id")
		return false, false, nil
	}
	status, hasStatus := rowInt64(row["status"])
	if !hasStatus {
		tc.quarantine("core_sql_orders", key, "status missing")
		return false, true, nil
	}
	outcome := ev.ClassifySQLOrder(status)
	switch outcome.Kind {
	case StatusActive, StatusUnknown:
		tc.quarantine("core_sql_orders", key, outcome.Reason)
		return false, true, nil
	case StatusAmbiguous:
		resolved, state := tc.resolveAmbiguousSQL(status, row)
		if !resolved {
			tc.quarantine("core_sql_orders", key, outcome.Reason+"; row evidence inconclusive")
			return false, true, nil
		}
		outcome = StatusOutcome{Kind: StatusMapped, State: state}
	}
	state := outcome.State
	if state == "cancelled" {
		state = "withdrawn" // Unexecuted cancellations converge to withdrawn semantics
	}
	submittedAt, err := tc.parseLegacyTime(row["date"], "date")
	if err != nil {
		tc.quarantine("core_sql_orders", key, "date invalid: "+err.Error())
		return false, false, nil
	}
	username := strings.TrimSpace(rowString(row["username"]))
	uid, resolved := tc.Users[strings.ToLower(username)]
	if !resolved {
		tc.quarantine("core_sql_orders", key, "submitter unresolved")
		return false, false, nil
	}
	execKind := "dml"
	orderType, hasType := rowInt64(row["type"])
	if !hasType || orderType < 0 || orderType > 1 {
		tc.quarantine("core_sql_orders", key, fmt.Sprintf("type %v outside contract", row["type"]))
		return false, true, nil
	}
	if orderType == 0 {
		execKind = "ddl"
	}
	orderID := tc.detID("change_orders", workID)
	tc.Orders[workID] = orderID
	displayName := fallbackName(rowString(row["real_name"]), username)

	sqlText := rowString(row["sql"])
	sealed, err := tc.sealBytes("order-sql", tc.aad("order_sql_payloads", orderID.String(), "sql_ciphertext", crypto.PurposePayload), []byte(sqlText))
	if err != nil {
		return false, false, err
	}
	snapshot := map[string]any{
		"text": rowString(row["text"]), "assigned": rowString(row["assigned"]),
		"current_step": rowString(row["current_step"]), "relevant": rowString(row["relevant"]),
		"backup": rowString(row["backup"]), "osc_info": rowString(row["osc_info"]),
		"file": rowString(row["file"]), "data_base": rowString(row["data_base"]),
		"table": rowString(row["table"]), "source": rowString(row["source"]),
		"idc": rowString(row["idc"]), "source_id": rowString(row["source_id"]),
		"delay": rowString(row["delay"]),
	}
	snapSealed, err := tc.sealJSON("order-snapshot", tc.aad("order_flow_snapshots", orderID.String(), "flow_snapshot_ciphertext", crypto.PurposePayload), snapshot)
	if err != nil {
		return false, false, err
	}
	tc.OrderSnapshots[workID] = snapshot

	terminalAt := terminalTime(tc, row)
	completeness := "partial"
	tc.addWrite("change_orders", ChangeOrderModel{
		ID: orderID.String(), DisplayNumber: legacyDisplayNumber(workID, submittedAt, tc.Timezone),
		SubmitterUserIDSnapshot: uid.String(), SubmitterUsernameSnapshot: username,
		SubmitterDisplayNameSnapshot: displayName,
		Title:                        "Legacy order " + workID, Description: "",
		State: state, SQLHMAC: tc.Crypter.DomainHMAC("order-sql", []byte(sqlText)),
		SnapshotHash:     tc.hash64("order-snapshot", []byte(mustJSON(snapshot))),
		ManuallyVerified: true, LegacySnapshotCompleteness: &completeness,
		SubmittedAt: submittedAt, TerminalAt: terminalAt,
	})
	tc.addWrite("order_sql_payloads", OrderSQLPayloadModel{
		OrderID:       orderID.String(),
		SQLCiphertext: sealed.Ciphertext, SQLNonce: sealed.Nonce,
		SQLKeyID: sealed.KeyID, SQLAlgorithm: sealed.Algorithm,
		PlaintextHMAC: tc.Crypter.DomainHMAC("order-sql", []byte(sqlText)),
		SizeBytes:     int32(len(sqlText)), StatementCount: 1, CreatedAt: tc.Now,
	})
	tc.addWrite("order_flow_snapshots", OrderFlowSnapshotModel{
		OrderID:                orderID.String(),
		FlowSnapshotCiphertext: snapSealed.Ciphertext, FlowSnapshotNonce: snapSealed.Nonce,
		FlowSnapshotKeyID: snapSealed.KeyID, FlowSnapshotAlgorithm: snapSealed.Algorithm,
		SnapshotHash: tc.hash64("order-snapshot", []byte(mustJSON(snapshot))), CreatedAt: tc.Now,
	})

	// Build the execution chain only for terminal states that actually executed (completed/failed).
	if state == "completed" || state == "failed" {
		if err := tc.buildExecutionChain(workID, orderID, uid, username, execKind, state, sqlText, submittedAt, terminalAt); err != nil {
			return false, false, err
		}
	}
	return true, false, nil
}

// buildExecutionChain constructs the complete historical execution chain satisfying foreign-key and logical references.
// review_runs are tagged with the legacy rule-review source and produce neither review_findings nor ai_usage_ledger rows.
func (tc *TransformContext) buildExecutionChain(workID string, orderID, uid uuid.UUID, username, execKind, state, sqlText string, submittedAt time.Time, terminalAt *time.Time) error {
	flowID := tc.materializedFlowFor(workID)
	draftID := tc.detID("change_drafts", workID)
	tc.addWrite("change_drafts", ChangeDraftModel{
		ID: draftID.String(), OwnerUserID: uid.String(), FlowID: flowID.String(),
		FlowVersionSeen: 1, Revision: 1, Title: "Legacy order " + workID,
		State: "submitted", CreatedAt: submittedAt, UpdatedAt: *terminalAt,
	})
	draftSealed, err := tc.sealBytes("order-sql", tc.aad("draft_sql_payloads", draftID.String(), "sql_ciphertext", crypto.PurposePayload), []byte(sqlText))
	if err != nil {
		return err
	}
	tc.addWrite("draft_sql_payloads", DraftSQLPayloadModel{
		DraftID:       draftID.String(),
		SQLCiphertext: draftSealed.Ciphertext, SQLNonce: draftSealed.Nonce,
		SQLKeyID: draftSealed.KeyID, SQLAlgorithm: draftSealed.Algorithm,
		PlaintextHMAC: tc.Crypter.DomainHMAC("order-sql", []byte(sqlText)),
		SizeBytes:     int32(len(sqlText)), StatementCount: 1, UpdatedAt: tc.Now,
	})
	reviewRunID := tc.detID("review_runs", workID)
	tc.addWrite("review_runs", ReviewRunModel{
		ID: reviewRunID.String(), DraftID: draftID.String(), DraftRevision: 1,
		State:                         "ready",
		FlowSnapshotHash:              tc.hash64("flow-snapshot", []byte(workID)),
		RuleConfigHash:                tc.hash64("legacy-rules", []byte(tc.legacyAuditRoleHash)),
		ToolConfigHash:                tc.hash64("legacy-tools", []byte("legacy-engine:v1")),
		ProviderPlanHash:              tc.hash64("legacy-provider", []byte("legacy:none")),
		FingerprintAlgorithmVersion:   "legacy:v1",
		AnonymizationAlgorithmVersion: "legacy:v1",
		StatementCount:                1, FingerprintGroupCount: 1,
		BudgetDay: sourceWallClock(submittedAt, tc.Timezone).Format("2006-01-02"),
		CreatedAt: submittedAt, StartedAt: &submittedAt, FinishedAt: terminalAt,
	})
	stageSnapshotID := tc.detID("stage_review_snapshots", workID)
	dsFingerprint := tc.hash64("datasource-identity", []byte("legacy:"+workID))
	// AAD mirrors the runtime writer semantics in fakerunner: PrimaryKey = reviewRunID#stagePosition
	// (the composite locator key for this table) rather than the snapshot row's own UUID.
	contextSealed, err := tc.sealJSON("review-context", tc.aad("stage_review_snapshots", reviewRunID.String()+"#1", "normalized_context_ciphertext", crypto.PurposePayload), map[string]any{"origin": "legacy_rule_engine", "ai_review": false})
	if err != nil {
		return err
	}
	tc.addWrite("stage_review_snapshots", StageReviewSnapshotModel{
		ID: stageSnapshotID.String(), ReviewRunID: reviewRunID.String(),
		StagePosition: 1, DatasourceID: tc.legacyStageDatasource(workID).String(),
		DatasourceIdentityFingerprint: dsFingerprint,
		State:                         "passed", HighestSeverity: "none", GatePassed: true,
		NormalizedContextCiphertext: contextSealed.Ciphertext,
		NormalizedContextNonce:      contextSealed.Nonce,
		NormalizedContextKeyID:      contextSealed.KeyID,
		NormalizedContextAlgorithm:  contextSealed.Algorithm,
		SnapshotHash:                tc.hash64("stage-review", []byte(workID)),
		CreatedAt:                   submittedAt, FinishedAt: terminalAt,
	})
	stageID := tc.detID("order_stages", workID)
	stageState := "succeeded"
	if state == "failed" {
		stageState = "failed"
	}
	// order_stages.target_snapshot is sealed with its own table AAD: the runtime reader
	// openTargetSnapshot decrypts with (order_stages, rowID, target_snapshot_ciphertext,
	// payload); previously reusing the stage_review_snapshots AAD ciphertext here left a residual
	// AAD asymmetry (R1 Medium-2) that broke the cross-column ciphertext substitution invariant.
	targetSealed, err := tc.sealJSON("order-stage-target", tc.aad("order_stages", stageID.String(), "target_snapshot_ciphertext", crypto.PurposePayload), map[string]any{"origin": "legacy_rule_engine", "ai_review": false})
	if err != nil {
		return err
	}
	tc.addWrite("order_stages", OrderStageModel{
		ID: stageID.String(), OrderID: orderID.String(), Position: 1,
		DatasourceID:           tc.legacyStageDatasource(workID).String(),
		DatasourceNameSnapshot: "legacy", DatasourceEngineSnapshot: "legacy",
		DatasourceIdentityFingerprint: dsFingerprint,
		StageReviewSnapshotID:         stageSnapshotID.String(),
		ReviewSnapshotHash:            tc.hash64("stage-review", []byte(workID)),
		ReviewGatePassed:              true,
		TargetSnapshotCiphertext:      targetSealed.Ciphertext,
		TargetSnapshotNonce:           targetSealed.Nonce,
		TargetSnapshotKeyID:           targetSealed.KeyID,
		TargetSnapshotAlgorithm:       targetSealed.Algorithm,
		State:                         stageState, ActivatedAt: terminalAt, TerminalAt: terminalAt,
	})
	attemptID := tc.detID("execution_attempts", workID)
	attemptState := "succeeded"
	var attemptBizCode *int
	var attemptFailure *string
	if state == "failed" {
		attemptState = "failed"
		code := 2600
		name := "legacy_execution_error"
		attemptBizCode, attemptFailure = &code, &name
	}
	tc.addWrite("execution_attempts", ExecutionAttemptModel{
		ID: attemptID.String(), OrderID: orderID.String(), StageID: stageID.String(),
		InitiatedByUserIDSnapshot: uid.String(), InitiatedByUsernameSnapshot: username,
		ExecutionKind: execKind, State: attemptState, SendBoundary: "sent", FenceToken: 1,
		BusinessErrCode: attemptBizCode, FailureName: attemptFailure,
		StartedAt: terminalAt, FinishedAt: terminalAt, CreatedAt: tc.Now,
	})
	tc.attemptOfOrder[workID] = attemptID
	// Historical schedule record for delayed tasks (created only for executed tickets).
	return nil
}

// materializedFlowFor returns the confirmed candidate flow for the ticket's datasource,
// or creates a deterministic inactive legacy placeholder flow if no candidate was materialized.
func (tc *TransformContext) materializedFlowFor(workID string) uuid.UUID {
	if sourceID, ok := tc.orderDatasource[workID]; ok {
		if flow, ok := tc.FlowOfDS[sourceID]; ok {
			return flow
		}
	}
	return uuid.NewSHA1(tc.RunID, []byte("legacy-flow|"+workID))
}

func (tc *TransformContext) legacyStageDatasource(workID string) uuid.UUID {
	if sourceID, ok := tc.orderDatasource[workID]; ok {
		if ds, ok := tc.Datasources[sourceID]; ok {
			return ds
		}
	}
	return uuid.NewSHA1(tc.RunID, []byte("legacy-stage-ds|"+workID))
}

// resolveAmbiguousSQL adjudicates ambiguous legacy SQL ticket statuses (1/4/7) row by row per the evidence contract.
func (tc *TransformContext) resolveAmbiguousSQL(status int64, row Row) (bool, string) {
	workID := strings.TrimSpace(rowString(row["work_id"]))
	records := tc.Records[workID]
	hasSuccess := false
	hasError := false
	for _, r := range records {
		if r.Success {
			hasSuccess = true
		}
		if r.HasError {
			hasError = true
		}
	}
	switch status {
	case 1:
		execTime := zeroAware(strings.TrimSpace(rowString(row["execute_time"])))
		if hasSuccess && execTime != "" {
			return true, "completed"
		}
		return false, ""
	case 4:
		delay, _ := rowInt64(row["delay"])
		execTime := zeroAware(strings.TrimSpace(rowString(row["execute_time"])))
		if delay > 0 && execTime == "" && !hasSuccess {
			return true, "cancelled"
		}
		if hasError {
			return true, "failed"
		}
		return false, ""
	case 7:
		for _, action := range tc.WorkflowEvents[workID] {
			lower := strings.ToLower(action)
			if strings.Contains(lower, "terminate") || strings.Contains(action, "\u7ec8\u6b62") {
				return true, "cancelled"
			}
		}
		return false, ""
	}
	return false, ""
}

// zeroAware normalizes legacy zero-date placeholder strings to the empty string.
func zeroAware(s string) string {
	switch s {
	case "", "0000-00-00 00:00:00", "0000-00-00", "0":
		return ""
	}
	return s
}

func terminalTime(tc *TransformContext, row Row) *time.Time {
	if t, err := tc.parseLegacyTime(row["execute_time"], "execute_time"); err == nil {
		return &t
	}
	if t, err := tc.parseLegacyTime(row["date"], "date"); err == nil {
		return &t
	}
	now := tc.Now
	return &now
}

// legacyDisplayNumber derives the contracted YR-YYYYMMDD-NNNNNN display number from work_id and submission date.
//
// Uses the wall-clock date in source_timezone rather than the UTC date: submittedAt is already a true instant,
// so formatting it directly in UTC would shift early-morning submissions in positive-offset zones (such as Asia/Shanghai)
// back to the previous calendar day, diverging from the date displayed in the legacy system. The contract pins
// the YR-YYYYMMDD-NNNNNN format for display/search only, so using the legacy local calendar date preserves human-readable continuity.
func legacyDisplayNumber(workID string, submittedAt time.Time, sourceZone *time.Location) string {
	seq := int64(0)
	for _, r := range workID {
		if r >= '0' && r <= '9' {
			seq = seq*10 + int64(r-'0')
		}
	}
	if seq < 0 {
		seq = -seq
	}
	return fmt.Sprintf("YR-%s-%06d", sourceWallClock(submittedAt, sourceZone).Format("20060102"), seq%1000000)
}

// sourceWallClock converts a UTC instant back into wall-clock time in sourceZone. Falls back to UTC if sourceZone is nil,
// consistent with parseLegacyTime's fail-closed behavior (the config layer already guarantees non-nil).
func sourceWallClock(instant time.Time, sourceZone *time.Location) time.Time {
	if sourceZone == nil {
		return instant.UTC()
	}
	return instant.In(sourceZone)
}

func fallbackName(realName, username string) string {
	if name := strings.TrimSpace(realName); name != "" {
		return name
	}
	return username
}

// TransformSQLRecord -> historical execution_statements facts (must attach to the execution attempt of an already mapped ticket).
func (tc *TransformContext) TransformSQLRecord(row Row) (bool, error) {
	key := rowString(row["id"])
	workID := strings.TrimSpace(rowString(row["work_id"]))
	attemptID, ok := tc.attemptOfOrder[workID]
	if !ok {
		tc.quarantine("core_sql_records", key, "parent order has no execution chain")
		return false, nil
	}
	finishedAt, err := tc.parseLegacyTime(row["time"], "time")
	if err != nil {
		tc.quarantine("core_sql_records", key, "time invalid: "+err.Error())
		return false, nil
	}
	errText := rowString(row["error"])
	state := "succeeded"
	var bizCode *int
	var failureName *string
	if errText != "" {
		state = "failed"
		code := 2600
		name := "legacy_execution_error"
		bizCode, failureName = &code, &name
	}
	var affected *int64
	if n, ok := rowInt64(row["affectrow"]); ok && n >= 0 {
		affected = &n
	}
	stmt := ExecutionStatementModel{
		ID:            tc.detID("execution_statements", key).String(),
		AttemptID:     attemptID.String(),
		Ordinal:       tc.statementOrdinal(workID),
		StatementKind: "dml", State: state,
		AffectedRowCount: affected, FinishedAt: &finishedAt,
		BusinessErrCode: bizCode, FailureName: failureName,
	}
	if errText != "" {
		// AAD mirrors runtime reader/writer sealErrorDetail/openErrorDetail:
		// PrimaryKey = attemptID#ordinal rather than the statement row's own UUID.
		sealed, err := tc.sealBytes("statement-error", tc.aad("execution_statements", fmt.Sprintf("%s#%d", stmt.AttemptID, stmt.Ordinal), "error_detail", crypto.PurposePayload), []byte(errText))
		if err != nil {
			return false, err
		}
		stmt.ErrorDetailCiphertext = sealed.Ciphertext
		stmt.ErrorNonce = sealed.Nonce
		keyID, alg := sealed.KeyID, sealed.Algorithm
		stmt.ErrorKeyID = &keyID
		stmt.ErrorAlgorithm = &alg
	}
	tc.addWrite("execution_statements", stmt)
	return true, nil
}

// TransformRollback -> buffers legacy rollback SQL in arrival order to be merged by FinalizeRollbackBundles
// into the ticket's order_flow_snapshots rollback_bundle and resealed (read-only archive, non-executable).
// Because the ticket snapshot was already sealed during TransformSQLOrder prior to this table, we cannot mutate an unsealed in-memory struct directly.
func (tc *TransformContext) TransformRollback(row Row) bool {
	key := rowString(row["id"])
	workID := strings.TrimSpace(rowString(row["work_id"]))
	if _, ok := tc.Orders[workID]; !ok {
		tc.quarantine("core_rollbacks", key, "parent order not mapped")
		return false
	}
	tc.pendingRollbacks[workID] = append(tc.pendingRollbacks[workID], rowString(row["sql"]))
	return true
}

// FinalizeRollbackBundles merges buffered legacy rollback SQL into the corresponding ticket snapshots and reseals them,
// replacing the existing order_flow_snapshots write (E003/M002: archiving rollback_bundle inside the encrypted snapshot).
// Must be called after all table transformations finish and before flushing writes to the target database.
func (tc *TransformContext) FinalizeRollbackBundles() error {
	for _, workID := range sortedKeys(tc.pendingRollbacks) {
		orderID, ok := tc.Orders[workID]
		if !ok {
			return fmt.Errorf("rollback finalize: order %s disappeared", workID)
		}
		snapshot, ok := tc.OrderSnapshots[workID]
		if !ok {
			return fmt.Errorf("rollback finalize: snapshot %s missing", workID)
		}
		// Ordered multi-row slice: source rows arrive in ascending ID order, so append order equals ID order.
		snapshot["rollback_bundle"] = tc.pendingRollbacks[workID]
		snapSealed, err := tc.sealJSON("order-snapshot", tc.aad("order_flow_snapshots", orderID.String(), "flow_snapshot_ciphertext", crypto.PurposePayload), snapshot)
		if err != nil {
			return fmt.Errorf("rollback finalize: reseal %s: %w", workID, err)
		}
		newSnapshotHash := tc.hash64("order-snapshot", []byte(mustJSON(snapshot)))
		replacedSnapshot := false
		replacedOrder := false
		for i, w := range tc.writes {
			switch m := w.Model.(type) {
			case OrderFlowSnapshotModel:
				if m.OrderID == orderID.String() {
					m.FlowSnapshotCiphertext = snapSealed.Ciphertext
					m.FlowSnapshotNonce = snapSealed.Nonce
					m.FlowSnapshotKeyID = snapSealed.KeyID
					m.FlowSnapshotAlgorithm = snapSealed.Algorithm
					m.SnapshotHash = newSnapshotHash
					tc.writes[i].Model = m
					replacedSnapshot = true
				}
			case ChangeOrderModel:
				// change_orders.snapshot_hash and order_flow_snapshots.snapshot_hash
				// both reference the snapshot payload and must be recomputed in lockstep (CL-1).
				if m.ID == orderID.String() {
					m.SnapshotHash = newSnapshotHash
					tc.writes[i].Model = m
					replacedOrder = true
				}
			}
		}
		if !replacedSnapshot {
			return fmt.Errorf("rollback finalize: order_flow_snapshots write for %s not found", workID)
		}
		if !replacedOrder {
			return fmt.Errorf("rollback finalize: change_orders write for %s not found", workID)
		}
	}
	return nil
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TransformWorkflowDetail -> immutable legacy domain event (never fabricates structured approval Decisions).
func (tc *TransformContext) TransformWorkflowDetail(row Row) bool {
	key := rowString(row["id"])
	workID := strings.TrimSpace(rowString(row["work_id"]))
	occurredAt, err := tc.parseLegacyTime(row["time"], "time")
	if err != nil {
		tc.quarantine("core_workflow_details", key, "time invalid: "+err.Error())
		return false
	}
	username := strings.TrimSpace(rowString(row["username"]))
	uid, _ := tc.Users[strings.ToLower(username)]
	subject := "legacy-order:" + workID
	if _, ok := tc.Orders[workID]; !ok {
		subject = "legacy-query:" + workID
	}
	action := rowString(row["action"])
	tc.addWrite("domain_events", DomainEventModel{
		ID:            tc.detID("domain_events", key).String(),
		EventType:     "legacy.workflow_detail.observed",
		Subject:       subject,
		Sequence:      tc.nextSequence(subject),
		CorrelationID: tc.RunID.String(),
		Actor:         mustJSON(map[string]any{"user_id": uid.String(), "username": username}),
		EventData:     mustJSON(map[string]any{"action": action, "structured": false}),
		OccurredAt:    occurredAt,
	})
	tc.WorkflowEvents[workID] = append(tc.WorkflowEvents[workID], action)
	return true
}

// TransformOrderComment -> order_comments.
func (tc *TransformContext) TransformOrderComment(row Row) bool {
	key := rowString(row["id"])
	workID := strings.TrimSpace(rowString(row["work_id"]))
	orderID, ok := tc.Orders[workID]
	if !ok {
		tc.quarantine("core_order_comments", key, "parent order not mapped")
		return false
	}
	occurredAt, err := tc.parseLegacyTime(row["time"], "time")
	if err != nil {
		tc.quarantine("core_order_comments", key, "time invalid: "+err.Error())
		return false
	}
	username := strings.TrimSpace(rowString(row["username"]))
	uid, resolved := tc.Users[strings.ToLower(username)]
	authorName := username
	if !resolved {
		authorName = username + " (deleted)"
		uid = uuid.NewSHA1(tc.RunID, []byte("deleted-user|"+username))
	}
	content := rowString(row["content"])
	if len(content) > 65535 {
		content = content[:65535]
	}
	tc.addWrite("order_comments", OrderCommentModel{
		ID:      tc.detID("order_comments", key).String(),
		OrderID: orderID.String(), AuthorUserIDSnapshot: uid.String(),
		AuthorUsernameSnapshot: authorName,
		Content:                content, OccurredAt: occurredAt,
	})
	return true
}

// TransformQueryOrder -> historical query_access_requests (never creates currently active Grants).
func (tc *TransformContext) TransformQueryOrder(row Row, ev *StatusEvidence) (bool, bool, error) {
	key := rowString(row["id"])
	workID := strings.TrimSpace(rowString(row["work_id"]))
	if workID == "" {
		tc.quarantine("core_query_orders", key, "empty work_id")
		return false, false, nil
	}
	status, ok := rowInt64(row["status"])
	if !ok {
		tc.quarantine("core_query_orders", key, "status missing")
		return false, true, nil
	}
	outcome := ev.ClassifyQueryOrder(status)
	if outcome.Kind == StatusActive || outcome.Kind == StatusUnknown {
		tc.quarantine("core_query_orders", key, outcome.Reason)
		return false, true, nil
	}
	var state string
	switch status {
	case 3:
		state = "grant_active" // Historical terminal state: terminal_at is non-null
	case 4:
		state = "access_rejected"
	default:
		tc.quarantine("core_query_orders", key, "unexpected terminal query status")
		return false, true, nil
	}
	createdAt, err := tc.parseLegacyTime(row["date"], "date")
	if err != nil {
		tc.quarantine("core_query_orders", key, "date invalid: "+err.Error())
		return false, false, nil
	}
	username := strings.TrimSpace(rowString(row["username"]))
	uid, resolved := tc.Users[strings.ToLower(username)]
	if !resolved {
		tc.quarantine("core_query_orders", key, "requester unresolved")
		return false, false, nil
	}
	requestID := tc.detID("query_access_requests", workID)
	tc.QueryOrders[workID] = requestID
	terminalAt := tc.Now
	snapshot := map[string]any{
		"text": rowString(row["text"]), "assigned": rowString(row["assigned"]),
		"closure_reason": "legacy_ambiguous",
	}
	// AAD binds the row primary key (requestID) per the aad_fields primary_key contract. Note that while
	// runtime writer access.go previously used "pending" on initial creation, migrated rows are born in a terminal
	// state and bind requestID directly ( matching the D005 closure in access.go).
	snapSealed, err := tc.sealJSON("query-snapshot", tc.aad("query_access_requests", requestID.String(), "flow_snapshot", crypto.PurposePayload), snapshot)
	if err != nil {
		return false, false, err
	}
	tc.QuerySnapshots[workID] = snapshot
	tc.addWrite("query_access_requests", QueryAccessRequestModel{
		ID:                      requestID.String(),
		RequesterUserIDSnapshot: uid.String(), RequesterUsernameSnapshot: username,
		RequesterDisplayNameSnapshot: fallbackName(rowString(row["real_name"]), username),
		FlowIDSnapshot:               uuid.NewSHA1(tc.RunID, []byte("legacy-query-flow|"+workID)).String(),
		FlowNameSnapshot:             "Legacy Query Flow",
		FlowSnapshotCiphertext:       snapSealed.Ciphertext, FlowSnapshotNonce: snapSealed.Nonce,
		FlowSnapshotKeyID: snapSealed.KeyID, FlowSnapshotAlgorithm: snapSealed.Algorithm,
		SnapshotHash: tc.hash64("query-snapshot", []byte(mustJSON(snapshot))),
		State:        state, Reason: "legacy query access " + workID,
		RequestedUntil: tc.requestedUntil(row), CreatedAt: createdAt, TerminalAt: &terminalAt,
	})
	return true, false, nil
}

// TransformQueryRecord -> query_executions + synthetic closed query session.
func (tc *TransformContext) TransformQueryRecord(row Row) (bool, error) {
	key := rowString(row["id"])
	workID := strings.TrimSpace(rowString(row["work_id"]))
	requestID, ok := tc.QueryOrders[workID]
	if !ok {
		tc.quarantine("core_query_records", key, "parent query order not mapped")
		return false, nil
	}
	username := strings.TrimSpace(rowString(row["username"]))
	uid, _ := tc.Users[strings.ToLower(username)]
	finishedAt, err := tc.parseLegacyTime(row["time"], "time")
	if err != nil {
		tc.quarantine("core_query_records", key, "time invalid: "+err.Error())
		return false, nil
	}
	sqlText := rowString(row["sql"])
	execID := tc.detID("query_executions", key).String()
	sealed, err := tc.sealBytes("query-sql", tc.aad("query_executions", execID, "query_payload", crypto.PurposePayload), []byte(sqlText))
	if err != nil {
		return false, err
	}
	sessionID, grantID := tc.ensureLegacyQuerySession(workID, requestID, uid, username)
	dsID, dsOK := tc.Datasources[strings.TrimSpace(rowString(row["source_id"]))]
	if !dsOK {
		tc.quarantine("core_query_records", key, "datasource unresolved")
		return false, nil
	}
	var elapsed *int32
	if ms, ok := rowInt64(row["ex_time"]); ok && ms >= 0 {
		v := int32(ms * 1000)
		elapsed = &v
	}
	state := "succeeded"
	if elapsed == nil {
		// Legacy records missing execution duration are quarantined per contract ("checked" conversions must be verifiable).
		tc.quarantine("core_query_records", key, "ex_time missing")
		return false, nil
	}
	tc.addWrite("query_executions", QueryExecutionModel{
		ID:        execID,
		SessionID: sessionID.String(), UserIDSnapshot: uid.String(), DatasourceID: dsID.String(),
		QueryPayloadCiphertext: sealed.Ciphertext, QueryPayloadNonce: sealed.Nonce,
		QueryPayloadKeyID: sealed.KeyID, QueryPayloadAlgorithm: sealed.Algorithm,
		SQLHMAC:         tc.Crypter.DomainHMAC("query-sql", []byte(sqlText)),
		FingerprintHMAC: tc.Crypter.DomainHMAC("query-fingerprint", []byte(normalizedFingerprint(sqlText))),
		State:           state, TimeoutMs: 300000, ElapsedMs: elapsed,
		CreatedAt: finishedAt, FinishedAt: &finishedAt,
	})
	_ = grantID
	return true, nil
}

// ensureLegacyQuerySession creates a synthetic closed query session and relinquished Grant for legacy query history.
func (tc *TransformContext) ensureLegacyQuerySession(workID string, requestID, uid uuid.UUID, username string) (uuid.UUID, uuid.UUID) {
	if sid, ok := tc.QuerySessions[workID]; ok {
		return sid, tc.detID("query_grants", workID)
	}
	sid := tc.detID("query_sessions", workID)
	gid := tc.detID("query_grants", workID)
	closedAt := tc.Now
	reason := "legacy_migration"
	tc.addWrite("query_grants", QueryGrantModel{
		ID: gid.String(), QueryAccessRequestID: requestID.String(),
		RequesterUserIDSnapshot: uid.String(), RequesterUsernameSnapshot: username,
		State: "relinquished", ExpiresAt: tc.Now.Add(-time.Second),
		CreatedAt: tc.Now, TerminalAt: &closedAt, TerminalReason: &reason,
	})
	tc.addWrite("query_sessions", QuerySessionModel{
		ID: sid.String(), UserIDSnapshot: uid.String(), UsernameSnapshot: username,
		FlowIDSnapshot:   uuid.NewSHA1(tc.RunID, []byte("legacy-query-flow|"+workID)).String(),
		FlowNameSnapshot: "Legacy Query Flow", GrantID: gid.String(),
		ApprovalRequiredSnapshot: true, State: "closed",
		CreatedAt: tc.Now, LastSeenAt: tc.Now, ClosedAt: &closedAt, CloseReason: &reason,
	})
	tc.QuerySessions[workID] = sid
	return sid, gid
}

// TransformAutoTask -> archive report entry only (no runtime automation capability is migrated).
// Per legacy-field-mapping CoreAutoTask: Name is hashed for redaction, while
// Source/SourceId/DataBase/Table/Tp/Affectrow/Status/IDC are retained as non-sensitive metadata.
func (tc *TransformContext) TransformAutoTask(row Row) bool {
	archive, _ := tc.Report["autotask_archive"].([]map[string]any)
	total, _ := rowInt64(row["affectrow"])
	tp, _ := rowInt64(row["tp"])
	tc.Report["autotask_archive"] = append(archive, map[string]any{
		"task_id":   rowString(row["task_id"]),
		"name_hash": tc.hash64("sample", []byte(rowString(row["name"]))),
		"source":    rowString(row["source"]), "source_id": rowString(row["source_id"]),
		"data_base": rowString(row["data_base"]), "table": rowString(row["table"]),
		"tp": tp, "affectrow": total, "status": rowString(row["status"]),
		"idc": rowString(row["idc"]), "non_executable": true,
	})
	return true
}

// TransformTotalTickets -> reconciliation aggregate only (does not seed v4 dashboard facts).
func (tc *TransformContext) TransformTotalTickets(row Row) bool {
	agg, _ := tc.Report["legacy_ticket_aggregates"].([]map[string]any)
	total, _ := rowInt64(row["total_order"])
	query, _ := rowInt64(row["total_query"])
	tc.Report["legacy_ticket_aggregates"] = append(agg, map[string]any{
		"date": rowString(row["date"]), "total_order": total, "total_query": query,
	})
	return true
}

func normalizedFingerprint(sqlText string) string {
	return strings.ToLower(strings.Join(strings.Fields(sqlText), " "))
}

func (tc *TransformContext) requestedUntil(row Row) time.Time {
	if t, err := tc.parseLegacyTime(row["approval_time"], "approval_time"); err == nil {
		return t
	}
	return tc.Now
}

func (tc *TransformContext) statementOrdinal(workID string) int32 {
	tc.ordinalCounter[workID]++
	return int32(tc.ordinalCounter[workID])
}

func (tc *TransformContext) nextSequence(subject string) int64 {
	tc.sequenceCounter[subject]++
	return tc.sequenceCounter[subject]
}
