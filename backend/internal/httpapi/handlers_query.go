// handlers_query.go query domain HTTP handlers: access request / approval / Grant /
// Session / execution / cursor page fetch / metadata.
// Authorization follows authorization-policy: query domain actor relationships are checked in the application layer;
// cursor affinity cookie is issued signed (kubernetes-deployment-contract).
package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/execution/application"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/platform/systz"
	queryapp "github.com/cookieY/YearningX/backend/internal/query/application"
)

// B11Deps aggregates B11 domain dependencies.
type B11Deps struct {
	Query *queryapp.Services
	Exec  *application.Services
	DB    *gorm.DB
}

// buildB11FromB10 constructs B11 from B10 dependencies (cmd wiring is outside B11 paths, following derivation pattern).
func buildB11FromB10(b10 *B10Deps) (*B11Deps, error) {
	q, err := queryapp.NewServices(b10.DB, b10.Exec.Crypter, b10.Exec.Recorder)
	if err != nil {
		return nil, err
	}
	return &B11Deps{Query: q, Exec: b10.Exec, DB: b10.DB}, nil
}

// b11Actor extracts the current identity (including management eligibility flag).
func b11Actor(c *gin.Context) queryapp.Actor {
	id := session.FromContext(c.Request.Context())
	if id == nil {
		return queryapp.Actor{}
	}
	return queryapp.Actor{UserID: id.UserID, Username: id.Username, IsBuiltinAdmin: id.HasAdminAccess()}
}

// writeQueryProblem records a sanitized internal error marker on c.Errors (S18.75-B) while
// preserving SEC-0030 (never echoing raw target driver messages, SQL text, or DSNs) and returns Problem 500.
func writeQueryProblem(c *gin.Context, err error) {
	if err != nil {
		_ = c.Error(sanitizeB11InternalErr(err))
	}
	WriteProblem(c, http.StatusInternalServerError, "internal error")
}

func handleQueryFailure(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if !writeCodedError(c, err) {
		writeQueryProblem(c, err)
	}
	return true
}

func sanitizeB11InternalErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.ConstraintName != "" {
			return fmt.Errorf("metadata_db_error: sqlstate=%s constraint=%s", pgErr.Code, pgErr.ConstraintName)
		}
		return fmt.Errorf("metadata_db_error: sqlstate=%s", pgErr.Code)
	}
	return errors.New("b11_internal_error")
}

// limitAfter parses pagination parameters.
func limitAfter(c *gin.Context) (int, string) {
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	return limit, c.Query("after")
}

// ---- Flow sensitive column vocabulary (builtin admin management surface) ----

func (d *B11Deps) HandleListFlowMaskingRules(c *gin.Context) {
	flowID, ok := uuidParam(c, "flow_id")
	if !ok {
		return
	}
	items, err := d.Query.ListMaskingRules(c.Request.Context(), b11Actor(c), flowID)
	if handleQueryFailure(c, err) {
		return
	}
	WriteSuccess(c, gin.H{"items": items})
}

func (d *B11Deps) HandleReplaceFlowMaskingRule(c *gin.Context) {
	flowID, ok := uuidParam(c, "flow_id")
	if !ok {
		return
	}
	datasourceID, ok := uuidParam(c, "datasource_id")
	if !ok {
		return
	}
	var req struct {
		SensitiveColumns *[]string `json:"sensitive_columns"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.SensitiveColumns == nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Query.ReplaceMaskingRule(c.Request.Context(), b11Actor(c), flowID, datasourceID, *req.SensitiveColumns)
	if handleQueryFailure(c, err) {
		return
	}
	WriteSuccess(c, view)
}

// ---- Query orders combined view ----

// HandleListQueryOrders combined query orders view (RCP-20261021): merges requests + sessions
// with server-side keyset pagination; actor relationship checks run in the application layer.
func (d *B11Deps) HandleListQueryOrders(c *gin.Context) {
	limit, after := limitAfter(c)
	items, next, err := d.Query.ListQueryOrders(c.Request.Context(), b11Actor(c), limit, after)
	if handleQueryFailure(c, err) {
		return
	}
	writePage(c, filterQueryOrdersForDevViewAs(c, items), next)
}

// ---- Access requests ----

func (d *B11Deps) HandleListQueryAccessRequests(c *gin.Context) {
	limit, after := limitAfter(c)
	// RCP-20261021: optional state filter (enum validated; unfiltered when omitted).
	state := c.Query("state")
	if state != "" && !queryapp.ValidQueryAccessRequestState(state) {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	scope := c.Query("scope")
	if !queryapp.ValidQueryAccessRequestScope(scope) {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	scope, empty := effectiveQueryAccessScopeForDevViewAs(c, scope)
	if empty {
		writePage(c, []queryapp.AccessRequestView{}, nil)
		return
	}
	items, next, _, err := d.Query.ListAccessRequests(c.Request.Context(), b11Actor(c), limit, after, state, scope)
	if handleQueryFailure(c, err) {
		return
	}
	// F12 #005/#016: query domain lists use contract PageInfo envelope (reusing writePage).
	writePage(c, filterQueryAccessRequestsForDevViewAs(c, items), next)
}

func (d *B11Deps) HandleCreateQueryAccessRequest(c *gin.Context) {
	key, ok := idemKey(c)
	if !ok {
		return
	}
	var req struct {
		FlowID         string   `json:"flow_id"`
		DatasourceIDs  []string `json:"datasource_ids"`
		RequestedUntil string   `json:"requested_until"`
		Reason         string   `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	until, err := time.Parse(time.RFC3339Nano, req.RequestedUntil)
	if err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	// RCP-20261021-AI-E2E-ACCEPTANCE-FIXES: requested_until is wall-clock time
	// (Z-suffixed from frontend), re-anchored to a UTC instant under general.system_timezone -- keeping
	// "same day" semantics consistent across timezones; falls back to raw instant on parse failure.
	until = reanchorSystemTimezone(c.Request.Context(), d.DB, until)
	view, _, err := d.Query.CreateAccessRequest(c.Request.Context(), queryapp.CreateAccessRequestInput{
		Actor: b11Actor(c), FlowID: req.FlowID, DatasourceIDs: req.DatasourceIDs,
		RequestedUntil: until, Reason: req.Reason, IdempotencyKey: key,
	})
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	writeQueryAccessRequestForDevViewAs(c, view)
}

// reanchorSystemTimezone reinterprets wall-clock time (Y-M-D H:m, marked UTC) as a UTC instant
// in the configured system timezone via canonical platform/systz.
func reanchorSystemTimezone(ctx context.Context, db *gorm.DB, wall time.Time) time.Time {
	return systz.ReanchorWallClock(wall, systz.Location(ctx, db))
}

func (d *B11Deps) HandleGetQueryAccessRequest(c *gin.Context) {
	requestID, ok := uuidParam(c, "request_id")
	if !ok {
		return
	}
	view, err := d.Query.GetAccessRequest(c.Request.Context(), b11Actor(c), requestID)
	if handleQueryFailure(c, err) {
		return
	}
	writeQueryAccessRequestForDevViewAs(c, view)
}

func (d *B11Deps) HandleDecideQueryAccess(c *gin.Context) {
	requestID, ok := uuidParam(c, "request_id")
	if !ok {
		return
	}
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	var req struct {
		Decision string `json:"decision"`
		Comment  string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if rejectQueryApprovalForDevViewAs(c) {
		return
	}
	view, _, err := d.Query.Decide(c.Request.Context(), queryapp.DecideInput{
		Actor: b11Actor(c), RequestID: requestID, Decision: req.Decision,
		Comment: req.Comment, ExpectVersion: version, IdempotencyKey: key,
	})
	if handleQueryFailure(c, err) {
		return
	}
	WriteSuccess(c, view)
}

func (d *B11Deps) HandleWithdrawQueryAccessRequest(c *gin.Context) {
	requestID, ok := uuidParam(c, "request_id")
	if !ok {
		return
	}
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if req.Reason == "" || len(req.Reason) > 4096 {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, _, err := d.Query.Withdraw(c.Request.Context(), queryapp.WithdrawInput{
		Actor: b11Actor(c), RequestID: requestID, Reason: req.Reason,
		ExpectVersion: version, IdempotencyKey: key,
	})
	if handleQueryFailure(c, err) {
		return
	}
	WriteSuccess(c, view)
}

// ---- Grant ----

func (d *B11Deps) HandleListQueryGrants(c *gin.Context) {
	limit, after := limitAfter(c)
	items, next, err := d.Query.ListGrants(c.Request.Context(), b11Actor(c), limit, after)
	if handleQueryFailure(c, err) {
		return
	}
	// F12 #005/#016: query domain lists use contract PageInfo envelope (reusing writePage).
	writePage(c, filterQueryGrantsForDevViewAs(c, items), next)
}

func (d *B11Deps) HandleGetQueryGrant(c *gin.Context) {
	grantID, ok := uuidParam(c, "grant_id")
	if !ok {
		return
	}
	view, err := d.Query.GetGrant(c.Request.Context(), b11Actor(c), grantID)
	if handleQueryFailure(c, err) {
		return
	}
	writeQueryGrantForDevViewAs(c, view)
}

func (d *B11Deps) HandleRevokeQueryGrant(c *gin.Context) {
	grantID, ok := uuidParam(c, "grant_id")
	if !ok {
		return
	}
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if rejectQueryApprovalForDevViewAs(c) {
		return
	}
	view, _, err := d.Query.Revoke(c.Request.Context(), queryapp.RevokeInput{
		Actor: b11Actor(c), GrantID: grantID, Reason: req.Reason,
		ExpectVersion: version, IdempotencyKey: key,
	})
	if handleQueryFailure(c, err) {
		return
	}
	WriteSuccess(c, view)
}

func (d *B11Deps) HandleRelinquishQueryGrant(c *gin.Context) {
	grantID, ok := uuidParam(c, "grant_id")
	if !ok {
		return
	}
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	if req.Reason == "" || len(req.Reason) > 4096 {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, _, err := d.Query.Relinquish(c.Request.Context(), queryapp.RelinquishInput{
		Actor: b11Actor(c), GrantID: grantID, Reason: req.Reason,
		ExpectVersion: version, IdempotencyKey: key,
	})
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B11Deps) HandleRenewQueryGrant(c *gin.Context) {
	grantID, ok := uuidParam(c, "grant_id")
	if !ok {
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	var req struct {
		RequestedUntil string `json:"requested_until"`
		Reason         string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	until, err := time.Parse(time.RFC3339Nano, req.RequestedUntil)
	if err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	// RCP-20261021-AI-E2E-ACCEPTANCE-FIXES (OBS-009): renewal uses the same semantics as creation --
	// requested_until is wall-clock time (Z-suffixed from frontend), re-anchored to a UTC instant under
	// general.system_timezone; previously renewal passed through the raw instant (historical review F8 closed 2026-11-02).
	until = reanchorSystemTimezone(c.Request.Context(), d.DB, until)
	view, _, err := d.Query.Renew(c.Request.Context(), queryapp.RenewInput{
		Actor: b11Actor(c), GrantID: grantID, RequestedUntil: until,
		Reason: req.Reason, IdempotencyKey: key,
	})
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	writeQueryAccessRequestForDevViewAs(c, view)
}

// ---- Session ----

func (d *B11Deps) HandleListQuerySessions(c *gin.Context) {
	limit, after := limitAfter(c)
	items, next, err := d.Query.ListSessions(c.Request.Context(), b11Actor(c), limit, after)
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	// F12 #005/#016: query domain lists use contract PageInfo envelope (reusing writePage).
	writePage(c, items, next)
}

func (d *B11Deps) HandleCreateQuerySession(c *gin.Context) {
	var req struct {
		FlowID        string   `json:"flow_id"`
		GrantID       string   `json:"grant_id"`
		DatasourceIDs []string `json:"datasource_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, _, err := d.Query.CreateSession(c.Request.Context(), queryapp.CreateSessionInput{
		Actor: b11Actor(c), FlowID: req.FlowID, GrantID: req.GrantID,
		DatasourceIDs: req.DatasourceIDs,
	})
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B11Deps) HandleGetQuerySession(c *gin.Context) {
	sessionID, ok := uuidParam(c, "session_id")
	if !ok {
		return
	}
	view, err := d.Query.GetSession(c.Request.Context(), b11Actor(c), sessionID)
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B11Deps) HandleCloseQuerySession(c *gin.Context) {
	sessionID, ok := uuidParam(c, "session_id")
	if !ok {
		return
	}
	key, ok := idemKey(c)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, _, err := d.Query.CloseSession(c.Request.Context(), queryapp.CloseSessionInput{
		Actor: b11Actor(c), SessionID: sessionID, Reason: req.Reason, IdempotencyKey: key,
	})
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	WriteSuccess(c, view)
}

// ---- Metadata ----

func (d *B11Deps) HandleListQuerySessionSchemas(c *gin.Context) {
	sessionID, ok := uuidParam(c, "session_id")
	if !ok {
		return
	}
	dsID := c.Query("datasource_id")
	items, err := d.Query.ListSchemas(c.Request.Context(), b11Actor(c), sessionID, dsID)
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	WriteSuccess(c, gin.H{"items": items})
}

func (d *B11Deps) HandleListQuerySessionTables(c *gin.Context) {
	sessionID, ok := uuidParam(c, "session_id")
	if !ok {
		return
	}
	items, err := d.Query.ListTables(c.Request.Context(), b11Actor(c), sessionID,
		c.Query("datasource_id"), c.Query("schema_name"))
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	WriteSuccess(c, gin.H{"items": items})
}

func (d *B11Deps) HandleListQuerySessionColumns(c *gin.Context) {
	sessionID, ok := uuidParam(c, "session_id")
	if !ok {
		return
	}
	items, err := d.Query.ListColumns(c.Request.Context(), b11Actor(c), sessionID,
		c.Query("datasource_id"), c.Query("schema_name"), c.Query("table_name"))
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	WriteSuccess(c, gin.H{"items": items})
}

// ---- Submitter-side metadata projection (RCP-20260904-SUBMITTER-METADATA-PROJECTION) ----

// SubmitterDatasourceGranted verifies that flow is granted to the session user and datasource belongs to its stages.
func (d *B11Deps) SubmitterDatasourceGranted(c *gin.Context, flowID, dsID string) (bool, error) {
	actor := b11Actor(c)
	return d.Query.FlowDatasourceGranted(c.Request.Context(), actor.UserID, flowID, dsID)
}

func (d *B11Deps) HandleListMyFlowDatasourceSchemas(c *gin.Context) {
	granted, err := d.SubmitterDatasourceGranted(c, c.Param("flow_id"), c.Param("datasource_id"))
	if err != nil {
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	if !granted {
		WriteBusinessError(c, &BusinessError{Code: 2014, Name: "FLOW_GRANT_REVOKED", Message: "flow not granted to the current user"})
		return
	}
	items, err := d.Query.ListFlowDatasourceSchemas(c.Request.Context(), c.Param("datasource_id"))
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	WriteSuccess(c, gin.H{"items": items})
}

func (d *B11Deps) HandleListMyFlowDatasourceTables(c *gin.Context) {
	granted, err := d.SubmitterDatasourceGranted(c, c.Param("flow_id"), c.Param("datasource_id"))
	if err != nil {
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	if !granted {
		WriteBusinessError(c, &BusinessError{Code: 2014, Name: "FLOW_GRANT_REVOKED", Message: "flow not granted to the current user"})
		return
	}
	items, err := d.Query.ListFlowDatasourceTables(c.Request.Context(), c.Param("datasource_id"), c.Query("schema_name"))
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	WriteSuccess(c, gin.H{"items": items})
}

func (d *B11Deps) HandleListMyFlowDatasourceColumns(c *gin.Context) {
	granted, err := d.SubmitterDatasourceGranted(c, c.Param("flow_id"), c.Param("datasource_id"))
	if err != nil {
		WriteProblem(c, http.StatusInternalServerError, "internal error")
		return
	}
	if !granted {
		WriteBusinessError(c, &BusinessError{Code: 2014, Name: "FLOW_GRANT_REVOKED", Message: "flow not granted to the current user"})
		return
	}
	items, err := d.Query.ListFlowDatasourceColumns(c.Request.Context(), c.Param("flow_id"),
		c.Param("datasource_id"), c.Query("schema_name"), c.Query("table_name"))
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	WriteSuccess(c, gin.H{"items": items})
}

// ---- Execution and page fetch ----

// cursorAffinityCookie affinity cookie name (kubernetes-deployment-contract
// query_cursor_affinity; HttpOnly, routed by L7 load balancer to Owner Pod).
const cursorAffinityCookie = "ynq-cursor-affinity"

// signAffinity signs the affinity value (cursor_affinity HMAC purpose).
func (d *B11Deps) signAffinity(value string) string {
	mac, err := d.Query.AffinityHMAC([]byte(value))
	if err != nil {
		return ""
	}
	return hex.EncodeToString(mac)
}

// setAffinityCookie issues a signed affinity cookie on the first page (M7: named per execution,
// so concurrent queries carry independent cookies without overwriting each other -- a single cookie
// would route earlier cursors to non-Owner Pods and trigger false owner loss).
func (d *B11Deps) setAffinityCookie(c *gin.Context, executionID, instance, tokenHash string) {
	value := instance + "." + tokenHash
	sig := d.signAffinity(value)
	if sig == "" {
		return
	}
	c.SetCookie(cursorAffinityCookie+"-"+executionID[:8], value+"."+sig[:16],
		6*3600, "/api/v4", "", c.Request.TLS != nil, true)
}

func (d *B11Deps) HandleExecuteSelect(c *gin.Context) {
	sessionID, ok := uuidParam(c, "session_id")
	if !ok {
		return
	}
	var req struct {
		DatasourceID string `json:"datasource_id"`
		SchemaName   string `json:"schema_name"`
		SQL          string `json:"sql"`
		TimeoutMS    int    `json:"timeout_ms"`
		PageSize     int    `json:"page_size"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Query.ExecuteSelect(c.Request.Context(), queryapp.ExecuteSelectInput{
		Actor: b11Actor(c), SessionID: sessionID, DatasourceID: req.DatasourceID,
		SchemaName: req.SchemaName, SQL: req.SQL, TimeoutMS: req.TimeoutMS, PageSize: req.PageSize,
	})
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	// Affinity cookie is issued per cursor Owner (for continuation routing; loss is detected as expired by registry).
	if view.Page.NextCursor != nil {
		sum := sha256.Sum256([]byte("yearning-query-cursor:" + *view.Page.NextCursor))
		d.setAffinityCookie(c, view.ExecutionID, d.Query.InstanceID(), hex.EncodeToString(sum[:]))
	}
	WriteSuccess(c, view)
}

func (d *B11Deps) HandleFetchQueryResultPage(c *gin.Context) {
	executionID, ok := uuidParam(c, "execution_id")
	if !ok {
		return
	}
	cursor := c.Query("cursor")
	purpose := c.Query("purpose")
	// Continuation page size override (RCP-20260901-QUERY-PAGE-SIZE-PARAM): optional,
	// 1..5000; retains cursor current value when omitted.
	pageSize := 0
	if raw := c.Query("page_size"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 5000 {
			WriteBusinessError(c, &BusinessError{Code: 1001, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
			return
		}
		pageSize = v
	}
	view, err := d.Query.FetchPage(c.Request.Context(), queryapp.FetchPageInput{
		Actor: b11Actor(c), ExecutionID: executionID, CursorToken: cursor,
		Purpose: purpose, PageSize: pageSize,
	})
	if err != nil {
		if !writeCodedError(c, err) {
			writeQueryProblem(c, err)
		}
		return
	}
	WriteSuccess(c, view)
}
