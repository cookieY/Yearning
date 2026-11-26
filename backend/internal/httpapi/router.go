// router.go assembles the gin Engine for serve mode.
package httpapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/dashboard"
	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/cookieY/YearningX/backend/internal/notification"
	"github.com/cookieY/YearningX/backend/internal/platform/assets"
	queryapp "github.com/cookieY/YearningX/backend/internal/query/application"
	"github.com/cookieY/YearningX/backend/internal/realtime"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/scheduler"
	"github.com/cookieY/YearningX/backend/internal/settings"
	"github.com/cookieY/YearningX/backend/internal/tasks"
)

// ServeDeps is the explicit dependency set for the serve-mode router.
type ServeDeps struct {
	Logger        *zap.Logger
	Readiness     *Readiness
	SessionStore  session.Store
	SessionOption session.Options
	B3            *B3Deps
	B6            *B6Deps
	B7            *B7Deps
	B9            *B9Deps
	B10           *B10Deps
	B11           *B11Deps
	B13           *B13Deps
	Assistant     *AssistantDeps
	Diagnostics   *DiagnosticsDeps
	Dashboard     *dashboard.Deps
	Notification  *notification.Deps
	RealtimeHub   *realtime.Hub
	// ReleaseFeedURL upstream release snapshot feed URL (RCP-20261020); empty string disables
	// the background refresh loop (disabled by default in test wiring).
	ReleaseFeedURL   string
	ReleaseFeedProxy string
	// StartBackgroundLoops starts the in-process background loops for the ADR-0001 modular monolith
	// (stage_execution worker / scheduler / notification worker).
	// BLK-20261017 Decision A: enabled by default in serve; kept false in test wiring to avoid
	// background polling goroutines in test processes.
	StartBackgroundLoops bool
	// StrictWiring disables lazy domain derivation inside NewServeRouter when deps are pre-assembled by BuildServeDeps.
	StrictWiring bool
	// LifecycleCtx carries shutdown cancellation into background loops (defaults to context.Background() when nil).
	LifecycleCtx context.Context
	// MaxRequestBodyBytes unified request body limit (SEC-0023); <=0 uses
	// DefaultMaxRequestBodyBytes (64MiB, 2x margin over the 32MiB business batch boundary).
	MaxRequestBodyBytes int64
	// TrustedProxies trusted proxy CIDRs (NEW-AUTH-01, declared via deployment config
	// trusted_proxies): gin ClientIP trusts X-Forwarded-For only when RemoteAddr falls within
	// these CIDRs (restoring real client IP for audit source_ip and rate-limit keys); B3Deps
	// trusts X-Forwarded-Proto from the same source. Empty/nil = trust no proxies (direct connection:
	// ClientIP=RemoteAddr, XFF/XFP ignored).
	TrustedProxies []string
	// DevViewAsEnabled activates the development-only X-Yearning-Dev-View-As role downgrade
	// middleware (D18); must remain false in production so the header is unconditionally ignored.
	DevViewAsEnabled bool
}

// NewServeRouter builds the production serve router.
// Middleware order is fixed: RequestID -> SecurityHeaders -> APINoStore ->
// BodyLimit -> Recovery -> AccessLog -> Session -> DevViewAs -> business routes.
// gin always runs in ReleaseMode: debug output is not part of the structured logging contract.
func NewServeRouter(deps ServeDeps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.HandleMethodNotAllowed = true
	// SEC-0005/NEW-AUTH-01: trusted proxies are explicitly declared by deployment config (ServeDeps.
	// TrustedProxies; empty/nil = trust no proxies -- gin treats empty slice and nil identically,
	// keeping isTrustedProxy false so ClientIP uses only RemoteAddr). Forged XFF from untrusted
	// sources no longer affects login rate-limit keys or audit source_ip; when deployed behind a
	// trusted proxy, audit and rate limiting resolve the real client IP (per PRD 09 client address contract).
	if err := engine.SetTrustedProxies(deps.TrustedProxies); err != nil {
		panic("invalid trusted proxies: " + err.Error())
	}
	// NEW-AUTH-01: B3 OIDC redirect_uri scheme resolution shares the same trusted proxy
	// declaration as gin (injected into B3Deps.TrustedProxies by router wiring).
	if deps.B3 != nil {
		deps.B3.TrustedProxies = deps.TrustedProxies
	}

	engine.Use(RequestIDMiddleware())
	// Security headers precede Recovery: panic fallback responses also carry security headers (SEC-0012/0033).
	engine.Use(SecurityHeadersMiddleware())
	engine.Use(APINoStoreMiddleware())
	engine.Use(BodyLimitMiddleware(deps.MaxRequestBodyBytes))
	engine.Use(RecoveryMiddleware(deps.Logger))
	engine.Use(AccessLogMiddleware(deps.Logger))
	if deps.SessionStore != nil {
		engine.Use(session.Middleware(deps.SessionStore, deps.SessionOption))
	}
	engine.Use(DevViewAsMiddleware(deps.DevViewAsEnabled))

	// BE-B13-LEFTOVER-CLOSURE (F001): when `-tags assets` release builds embed locked
	// Frontend static assets, unmatched routes serve the SPA while API prefixes (including bare "/api")
	// retain Problem 404 contract semantics. Disabled in default builds.
	// Embedded asset self-consistency (index.html present + dist digest injected) fails fast at wiring time.
	if spa := assets.ReleaseProvider(); spa.Enabled() {
		if err := spa.Validate(); err != nil {
			panic(fmt.Sprintf("invalid embedded frontend assets: %v", err))
		}
		spaHandler := assets.Handler(spa, nil)
		engine.NoRoute(func(c *gin.Context) {
			if c.Request.URL.Path == "/api" || strings.HasPrefix(c.Request.URL.Path, "/api/") {
				NotFoundHandler(c)
				return
			}
			spaHandler.ServeHTTP(c.Writer, c.Request)
		})
	} else {
		engine.NoRoute(NotFoundHandler)
	}
	engine.NoMethod(MethodNotAllowedHandler)

	engine.GET("/api/v4/health/live", deps.Readiness.HandleLive)
	engine.GET("/api/v4/health/ready", deps.Readiness.HandleReady)
	engine.GET("/api/v4/version", HandleVersion)

	if deps.RealtimeHub == nil {
		deps.RealtimeHub = realtime.NewHub()
	}
	var streamSessions *identity.SessionStore
	if deps.B3 != nil {
		streamSessions = deps.B3.Sessions
	}
	engine.Group("/api/v4").GET("/events/stream",
		RequireOperation("streamDomainEvents", streamSessions),
		HandleStreamDomainEvents(deps.RealtimeHub),
	)

	// B6 is derived from B3 platform dependencies in non-strict test wiring. Must complete
	// before mountB3Routes: B3 task route registration depends on whether deps.B6 is present,
	// and registering the same method+path twice would cause gin to panic.
	if !deps.StrictWiring && deps.B3 != nil && deps.B6 == nil {
		if b6, err := buildB6FromB3(deps.B3); err == nil {
			deps.B6 = b6
		} else if deps.Logger != nil {
			deps.Logger.Error("review services unavailable", zap.Error(err))
		}
	}
	if deps.B3 != nil {
		mountB3Routes(engine, deps)
	}
	// B13 settings domain is derived from B3 dependencies in non-strict test wiring.
	if !deps.StrictWiring && deps.B3 != nil && deps.B13 == nil {
		deps.B13 = &B13Deps{Settings: settings.New(deps.B3.DB, deps.B3.Crypter)}
	}
	if deps.B13 != nil && deps.B3 != nil {
		admin := engine.Group("/api/v4")
		guard := func(op string) gin.HandlerFunc { return RequireOperation(op, deps.B3.Sessions) }
		registerSettingsRoutes(admin, deps.B13, guard)
	}
	// S17.6-5: dashboard (including announcements and audit read) and notification domain routing.
	if !deps.StrictWiring && deps.B3 != nil {
		if deps.Dashboard == nil {
			dashSvc := dashboard.New(deps.B3.DB, deps.B3.Crypter)
			if client, cerr := dashboard.NewReleaseHTTPClient(deps.ReleaseFeedProxy); cerr == nil {
				dashSvc.ReleaseHTTPClient = client
			}
			deps.Dashboard = &dashboard.Deps{
				Service: dashSvc,
				Guard:   func(op string) gin.HandlerFunc { return RequireOperation(op, deps.B3.Sessions) },
				// RCP-20261020: release snapshot feed source (config release_feed.upstream_url);
				// empty string disables the background refresh loop.
				ReleaseFeedURL: deps.ReleaseFeedURL,
			}
		}
		if deps.Notification == nil {
			deps.Notification = &notification.Deps{
				Service: notification.NewService(deps.B3.DB, deps.B3.Crypter),
				Guard:   func(op string) gin.HandlerFunc { return RequireOperation(op, deps.B3.Sessions) },
			}
		}
	}
	if deps.Dashboard != nil {
		dashboard.RegisterRoutes(engine.Group("/api/v4"), deps.Dashboard)
	}
	if deps.Notification != nil {
		notification.RegisterRoutes(engine.Group("/api/v4"), deps.Notification)
	}
	if !deps.StrictWiring && deps.B6 != nil && deps.B7 == nil {
		deps.B7 = buildB7FromB6(deps.B6)
	}
	if deps.B6 != nil {
		mountB6Routes(engine, deps)
	}
	if deps.B7 != nil {
		mountB7Routes(engine, deps)
	}
	if !deps.StrictWiring && deps.B6 != nil && deps.B9 == nil {
		if b9, err := buildB9FromB6(deps.B6); err == nil {
			deps.B9 = b9
		} else if deps.Logger != nil {
			deps.Logger.Error("changeorder services unavailable", zap.Error(err))
		}
	}
	if deps.B9 != nil {
		mountB9Routes(engine, deps)
	}
	if !deps.StrictWiring && deps.B9 != nil && deps.B10 == nil {
		if b10, err := buildB10FromB9(deps.B9); err == nil {
			deps.B10 = b10
		} else if deps.Logger != nil {
			deps.Logger.Error("execution services unavailable", zap.Error(err))
		}
	}
	if deps.B10 != nil {
		mountB10Routes(engine, deps)
	}
	if !deps.StrictWiring && deps.B10 != nil && deps.B11 == nil {
		if b11, err := buildB11FromB10(deps.B10); err == nil {
			deps.B11 = b11
		} else if deps.Logger != nil {
			deps.Logger.Error("query services unavailable", zap.Error(err))
		}
	}
	if deps.B11 != nil {
		mountB11Routes(engine, deps)
	}
	if !deps.StrictWiring && deps.B3 != nil && deps.B6 != nil && deps.B9 != nil && deps.Assistant == nil {
		if asst, err := buildAssistantDeps(deps.B3, deps.B6, deps.B9); err == nil {
			deps.Assistant = asst
		} else if deps.Logger != nil {
			deps.Logger.Error("assistant services unavailable", zap.Error(err))
		}
	}
	if deps.Assistant != nil {
		mountAssistantRoutes(engine, deps)
	}
	if !deps.StrictWiring && deps.B3 != nil && deps.Diagnostics == nil {
		if diag, err := buildDiagnosticsDeps(deps.B3, deps.B6, deps.B9); err == nil {
			deps.Diagnostics = diag
		} else if deps.Logger != nil {
			deps.Logger.Error("diagnostics services unavailable", zap.Error(err))
		}
	}
	if deps.Diagnostics != nil {
		mountDiagnosticsRoutes(engine, deps)
	}
	attachRealtimeHub(deps)
	if deps.StartBackgroundLoops {
		startBackgroundLoops(&deps)
	}
	return engine
}

// attachRealtimeHub wires the shared realtime.Hub onto every domain Recorder instance.
func attachRealtimeHub(deps ServeDeps) {
	if deps.RealtimeHub == nil {
		return
	}
	if deps.B3 != nil {
		if deps.B3.Recorder != nil {
			deps.B3.Recorder.SetRealtimeHub(deps.RealtimeHub)
		}
		if deps.B3.Identity != nil && deps.B3.Identity.Recorder() != nil {
			deps.B3.Identity.Recorder().SetRealtimeHub(deps.RealtimeHub)
		}
		if deps.B3.Workflow != nil && deps.B3.Workflow.Recorder() != nil {
			deps.B3.Workflow.Recorder().SetRealtimeHub(deps.RealtimeHub)
		}
		if deps.B3.Datasource != nil && deps.B3.Datasource.Recorder() != nil {
			deps.B3.Datasource.Recorder().SetRealtimeHub(deps.RealtimeHub)
		}
	}
	if deps.B6 != nil && deps.B6.Review != nil && deps.B6.Review.Recorder != nil {
		deps.B6.Review.Recorder.SetRealtimeHub(deps.RealtimeHub)
	}
	if deps.B9 != nil && deps.B9.Orders != nil && deps.B9.Orders.Recorder != nil {
		deps.B9.Orders.Recorder.SetRealtimeHub(deps.RealtimeHub)
	}
	if deps.B10 != nil && deps.B10.Exec != nil && deps.B10.Exec.Recorder != nil {
		deps.B10.Exec.Recorder.SetRealtimeHub(deps.RealtimeHub)
	}
	if deps.B11 != nil && deps.B11.Query != nil && deps.B11.Query.Recorder != nil {
		deps.B11.Query.Recorder.SetRealtimeHub(deps.RealtimeHub)
	}
	if deps.Diagnostics != nil && deps.Diagnostics.Diagnostics != nil && deps.Diagnostics.Diagnostics.Audit != nil {
		deps.Diagnostics.Diagnostics.Audit.SetRealtimeHub(deps.RealtimeHub)
	}
}

// startBackgroundLoops starts the in-process background loops required by the ADR-0001 modular monolith
// (BLK-20261017 Decision A, BE-B13-F12-CONTRACT-ALIGNMENT):
//   - stage_execution worker: claims execution tasks -> drives Runner.RunAttempt to terminal state;
//   - scheduler: adjudicates due execution schedules + sweeps expired query grants/cursors;
//   - notification worker: delivers Outbox events.
//
// Multi-replica safety is guaranteed by existing PG leases/fences; errors inside loops back off and retry
// without exiting the process. Note: Readiness probes only metadata_postgresql and key_provider without
// exposing loop health -- if derivation fails, loops are skipped along with the corresponding domain routes.
// owner uses instance identifier for idempotency isolation.
func startBackgroundLoops(deps *ServeDeps) {
	bgCtx := deps.LifecycleCtx
	if bgCtx == nil {
		bgCtx = context.Background()
	}
	owner := "serve-" + uuid.NewString()[:8]
	if deps.B10 != nil && deps.B10.Exec != nil {
		w := tasks.NewWorker(deps.B10.Exec.DB, owner,
			[]string{execapp.KindStageExecution}, 30*time.Second, time.Second)
		w.Register(execapp.KindStageExecution, func(ctx context.Context, db *gorm.DB,
			task *tasks.ClaimedTask, fenceGuard func() error, beat func(completed, total int64)) error {
			return deps.B10.Runner.Execute(ctx, db, task.Input, fenceGuard, beat)
		})
		go w.Run(bgCtx)
		if deps.Logger != nil {
			deps.Logger.Info("background stage_execution worker started", zap.String("owner", owner))
		}
	}
	var execSvc *execapp.Services
	if deps.B10 != nil {
		execSvc = deps.B10.Exec
	}
	var querySvc *queryapp.Services
	if deps.B11 != nil {
		querySvc = deps.B11.Query
	}
	var schedDB *gorm.DB
	if deps.B3 != nil {
		schedDB = deps.B3.DB
	} else if execSvc != nil {
		schedDB = execSvc.DB
	} else if querySvc != nil {
		schedDB = querySvc.DB
	}
	if (execSvc != nil || querySvc != nil) && schedDB != nil {
		s := scheduler.New(schedDB, execSvc, querySvc, 5*time.Second)
		go s.Run(bgCtx)
		if deps.Logger != nil {
			deps.Logger.Info("background scheduler started")
		}
	}
	if deps.B3 != nil {
		nw := notification.NewWorker(deps.B3.DB, deps.B3.Crypter, 10*time.Second)
		// S007: stale pending reminder suppression audit (reuses B3Recorder; audit failure does not block delivery).
		if auditor, aerr := auditlog.NewRecorder(deps.B3.DB); aerr == nil {
			auditor.SetRealtimeHub(deps.RealtimeHub)
			nw.SetAuditor(auditor)
		} else if deps.Logger != nil {
			deps.Logger.Warn("notification suppression auditor unavailable", zap.Error(aerr))
		}
		// S007/R1 M6: structured log for explainable skips.
		nw.SetLogger(deps.Logger)
		// S007: callback link base URL in notifications comes from general.public_base_url (controlled setting;
		// degrades to empty on read failure -- omits link rather than guessing from request Host).
		settingsSvc := settings.New(deps.B3.DB, deps.B3.Crypter)
		syncNotificationGeneralSettings := func() {
			if view, gerr := settingsSvc.Get(bgCtx, "general"); gerr == nil {
				if u, ok := view.Settings["public_base_url"].(string); ok {
					nw.SetPublicBaseURL(u)
				}
				if l, ok := view.Settings["default_locale"].(string); ok {
					nw.SetLocale(l)
				}
			} else if deps.Logger != nil {
				deps.Logger.Warn("notification worker settings read failed; links disabled", zap.Error(gerr))
			}
		}
		syncNotificationGeneralSettings()
		settings.RegisterPostReplaceHook(func(namespace string) {
			if namespace == settings.NsGeneral && bgCtx.Err() == nil {
				syncNotificationGeneralSettings()
			}
		})
		go nw.Run(bgCtx)
		if deps.Logger != nil {
			deps.Logger.Info("background notification worker started")
		}
	}
	if deps.Diagnostics != nil && deps.Diagnostics.Diagnostics != nil {
		go deps.Diagnostics.Diagnostics.RunCollectionLoop(bgCtx)
		if deps.Logger != nil {
			deps.Logger.Info("background diagnostics collector started")
		}
	}
	// RCP-20261020: background refresh for release snapshot (fetches immediately on startup, then daily).
	// Retains existing DB snapshot on fetch failure without exiting loop; disabled when URL is empty (test wiring).
	if deps.Dashboard != nil && deps.Dashboard.ReleaseFeedURL != "" {
		go deps.Dashboard.Service.RunReleaseRefreshLoop(bgCtx,
			deps.Dashboard.ReleaseFeedURL, deps.Logger)
		if deps.Logger != nil {
			deps.Logger.Info("background release feed refresher started",
				zap.String("source", deps.Dashboard.ReleaseFeedURL))
		}
	}
}

func businessSessions(deps ServeDeps) *identity.SessionStore {
	if deps.B6 != nil && deps.B6.Sessions != nil {
		return deps.B6.Sessions
	}
	if deps.B9 != nil && deps.B9.Sessions != nil {
		return deps.B9.Sessions
	}
	if deps.B10 != nil && deps.B10.Sessions != nil {
		return deps.B10.Sessions
	}
	if deps.B3 != nil {
		return deps.B3.Sessions
	}
	return nil
}

// mountB11Routes mounts B11 query and schedule routes (guard = RequireBusinessOperation + CSRF; query
// actor relationships and schedule eligibility are verified in the application layer against frozen snapshots; export audited per page).
func mountB11Routes(engine *gin.Engine, deps ServeDeps) {
	b11 := deps.B11
	guard := func(op string) gin.HandlerFunc {
		return RequireBusinessOperation(op, businessSessions(deps))
	}
	api := engine.Group("/api/v4")

	// Flow sensitive column vocabulary belongs to builtin-admin-management; operationId matches OpenAPI/
	// authorization-policy without reusing business query relationship guards.
	admin := api.Group("/admin")
	admin.GET("/flows/:flow_id/masking-rules",
		RequireOperation("listFlowMaskingRules", deps.B6.Sessions), b11.HandleListFlowMaskingRules)
	admin.PUT("/flows/:flow_id/datasources/:datasource_id/masking-rules",
		RequireOperation("replaceFlowMaskingRule", deps.B6.Sessions), b11.HandleReplaceFlowMaskingRule)

	requests := api.Group("/query-access-requests")
	requests.GET("", guard("listQueryAccessRequests"), b11.HandleListQueryAccessRequests)
	requests.POST("", guard("createQueryAccessRequest"), b11.HandleCreateQueryAccessRequest)
	requests.GET("/:request_id", guard("getQueryAccessRequest"), b11.HandleGetQueryAccessRequest)
	requests.POST("/:request_id/decisions", guard("decideQueryAccess"), b11.HandleDecideQueryAccess)
	requests.POST("/:request_id/withdrawal", guard("withdrawQueryAccessRequest"), b11.HandleWithdrawQueryAccessRequest)

	grants := api.Group("/query-grants")
	grants.GET("", guard("listQueryGrants"), b11.HandleListQueryGrants)
	grants.GET("/:grant_id", guard("getQueryGrant"), b11.HandleGetQueryGrant)
	grants.POST("/:grant_id/revocations", guard("revokeQueryGrant"), b11.HandleRevokeQueryGrant)
	grants.POST("/:grant_id/relinquishment", guard("relinquishQueryGrant"), b11.HandleRelinquishQueryGrant)
	grants.POST("/:grant_id/renewal-requests", guard("createQueryGrantRenewalRequest"), b11.HandleRenewQueryGrant)

	qorders := api.Group("/query/orders")
	qorders.GET("", guard("listQueryOrders"), b11.HandleListQueryOrders)
	qsessions := api.Group("/query-sessions")
	qsessions.GET("", guard("listQuerySessions"), b11.HandleListQuerySessions)
	qsessions.POST("", guard("createQuerySession"), b11.HandleCreateQuerySession)
	qsessions.GET("/:session_id", guard("getQuerySession"), b11.HandleGetQuerySession)
	qsessions.POST("/:session_id/closure", guard("closeQuerySession"), b11.HandleCloseQuerySession)
	qsessions.GET("/:session_id/metadata/schemas", guard("listQuerySessionSchemas"), b11.HandleListQuerySessionSchemas)
	qsessions.GET("/:session_id/metadata/tables", guard("listQuerySessionTables"), b11.HandleListQuerySessionTables)
	qsessions.GET("/:session_id/metadata/columns", guard("listQuerySessionColumns"), b11.HandleListQuerySessionColumns)
	// Submitter-side metadata projection (RCP-20260904-SUBMITTER-METADATA-PROJECTION):
	// read-only schema/table/column surface for stage datasources of granted change_review flows.
	flowDatasourceMeta := api.Group("/users/me/flows/:flow_id/datasources/:datasource_id/metadata")
	flowDatasourceMeta.GET("/schemas", guard("listMyFlowDatasourceSchemas"), b11.HandleListMyFlowDatasourceSchemas)
	flowDatasourceMeta.GET("/tables", guard("listMyFlowDatasourceTables"), b11.HandleListMyFlowDatasourceTables)
	flowDatasourceMeta.GET("/columns", guard("listMyFlowDatasourceColumns"), b11.HandleListMyFlowDatasourceColumns)
	qsessions.POST("/:session_id/executions", guard("executeSelect"), b11.HandleExecuteSelect)

	api.GET("/query-executions/:execution_id/pages", guard("fetchQueryResultPage"), b11.HandleFetchQueryResultPage)

	// E007: createExecutionSchedule is mounted by B11 (superseding B10 placeholder comment).
	api.POST("/change-orders/:order_id/execution-schedules",
		guard("createExecutionSchedule"), b11.HandleCreateExecutionSchedule)
	// Cancel schedule (owner decision 2026-10-30): any frozen executor; returns order to execution_pending.
	api.POST("/change-orders/:order_id/execution-schedules/cancellation",
		guard("cancelExecutionSchedule"), b11.HandleCancelExecutionSchedule)
}

// mountB10Routes mounts B10 execution domain routes (guard = RequireBusinessOperation + CSRF; execution
// eligibility is verified in the application layer against frozen executor snapshots).
func mountB10Routes(engine *gin.Engine, deps ServeDeps) {
	b10 := deps.B10
	guard := func(op string) gin.HandlerFunc {
		return RequireBusinessOperation(op, businessSessions(deps))
	}

	api := engine.Group("/api/v4")
	orders := api.Group("/change-orders")
	orders.POST("/:order_id/execution-attempts", guard("createExecutionAttempt"), b10.HandleCreateExecutionAttempt)

	attempts := api.Group("/execution-attempts")
	attempts.GET("/:attempt_id", guard("getExecutionAttempt"), b10.HandleGetExecutionAttempt)
	attempts.GET("/:attempt_id/statements", guard("listExecutionAttemptStatements"), b10.HandleListExecutionStatements)
	attempts.POST("/:attempt_id/cancellation", guard("cancelExecutionAttempt"), b10.HandleCancelExecutionAttempt)
	attempts.POST("/:attempt_id/verifications", guard("createExecutionVerification"), b10.HandleCreateExecutionVerification)
}

// mountB9Routes mounts B9 change order domain routes (guard = RequireBusinessOperation + CSRF; actor relationships
// verified in application layer; submitter pre-check for withdraw/void/copy rejects with 403 at handler layer).
func mountB9Routes(engine *gin.Engine, deps ServeDeps) {
	b9 := deps.B9
	guard := func(op string) gin.HandlerFunc {
		return RequireBusinessOperation(op, businessSessions(deps))
	}

	api := engine.Group("/api/v4")
	// RCP-20261101-EPHEMERAL-PRECHECK: submission moves to payload-carrying v2 endpoint; draft-scoped
	// submission route is retired.
	api.POST("/change-orders", guard("submitChangeDraft"), b9.HandleSubmitOrderV2)

	orders := api.Group("/change-orders")
	orders.GET("", guard("listChangeOrders"), b9.HandleListOrders)
	orders.GET("/:order_id", guard("getChangeOrder"), b9.HandleGetOrder)
	orders.POST("/:order_id/approval-decisions", guard("decideChangeOrder"), b9.HandleDecideOrder)
	orders.POST("/:order_id/withdrawal", guard("withdrawChangeOrder"), b9.HandleWithdrawOrder)
	orders.POST("/:order_id/voidance", guard("voidChangeOrder"), b9.HandleVoidOrder)
	orders.POST("/:order_id/draft-copies", guard("copyChangeOrderToDraft"), b9.HandleCopyToDraft)
	orders.POST("/:order_id/rollback-drafts", guard("createRollbackDraft"), b9.HandleCreateRollbackDraft)
	orders.GET("/:order_id/timeline", guard("listChangeOrderTimeline"), b9.HandleListTimeline)
	orders.GET("/:order_id/comments", guard("listChangeOrderComments"), b9.HandleListComments)
	orders.POST("/:order_id/comments", guard("createChangeOrderComment"), b9.HandleCreateComment)
	orders.GET("/:order_id/review-findings", guard("listOrderReviewFindings"), b9.HandleListOrderFindings)
	orders.POST("/:order_id/sql-reveals", guard("revealOrderSql"), b9.HandleRevealOrderSql)
	orders.POST("/:order_id/sql-copy-events", guard("recordOrderSqlCopy"), b9.HandleRecordSqlCopy)
}

// buildB7FromB6 constructs B7 management dependencies from B6.
func buildB7FromB6(b6 *B6Deps) *B7Deps {
	return &B7Deps{
		Review: b6.Review,
		Knowledge: &knowledge.Service{
			Repo: &knowledge.Repository{DB: b6.DB},
			Invalidate: func(ctx context.Context, tx *gorm.DB, toolKind, inputRefID, oldHash string) error {
				return application.InvalidateInputRefs(ctx, tx, toolKind, inputRefID, oldHash)
			},
		},
	}
}

// mountB7Routes mounts B7 management routes (review engine: skills / Providers / internal knowledge).
// All operations fall under the builtin-admin-management rule (builtin_admin only).
func mountB7Routes(engine *gin.Engine, deps ServeDeps) {
	b7 := deps.B7
	guard := func(op string) gin.HandlerFunc { return RequireOperation(op, deps.B6.Sessions) }

	admin := engine.Group("/api/v4/admin")
	admin.GET("/prompt-tools", guard("listPromptTools"), b7.HandleListPromptTools)
	admin.POST("/prompt-tools", guard("createPromptTool"), b7.HandleCreatePromptTool)
	admin.POST("/prompt-tools/prompt-optimizations", guard("optimizePromptToolPrompt"), b7.HandleOptimizePromptToolPrompt)
	admin.POST("/prompt-tools/test-cases", guard("generatePromptToolTestCases"), b7.HandleGeneratePromptToolTestCases)
	admin.POST("/prompt-tools/test-runs", guard("runPromptToolTest"), b7.HandleRunPromptToolTest)
	admin.GET("/prompt-tools/:prompt_tool_id", guard("getPromptTool"), b7.HandleGetPromptTool)
	admin.PUT("/prompt-tools/:prompt_tool_id", guard("replacePromptTool"), b7.HandleReplacePromptTool)
	admin.DELETE("/prompt-tools/:prompt_tool_id", guard("deletePromptTool"), b7.HandleDeletePromptTool)

	// Rule sets: route gap identified in S18.73 (contract / policy / FE / mock and service layer
	// were all present, missing HTTP wiring) -- wired here with server-side q search.
	admin.GET("/rule-sets", guard("listRuleSets"), b7.HandleListRuleSets)
	admin.POST("/rule-sets", guard("createRuleSet"), b7.HandleCreateRuleSet)
	admin.GET("/rule-sets/:rule_set_id", guard("getRuleSet"), b7.HandleGetRuleSet)
	admin.PUT("/rule-sets/:rule_set_id", guard("replaceRuleSet"), b7.HandleReplaceRuleSet)
	admin.DELETE("/rule-sets/:rule_set_id", guard("deleteRuleSet"), b7.HandleDeleteRuleSet)

	admin.GET("/ai-providers", guard("listAiProviders"), b7.HandleListAiProviders)
	admin.POST("/ai-providers", guard("createAiProvider"), b7.HandleCreateAiProvider)
	admin.GET("/ai-providers/:ai_provider_id", guard("getAiProvider"), b7.HandleGetAiProvider)
	admin.PUT("/ai-providers/:ai_provider_id", guard("replaceAiProvider"), b7.HandleReplaceAiProvider)
	admin.DELETE("/ai-providers/:ai_provider_id", guard("deleteAiProvider"), b7.HandleDeleteAiProvider)
	// S18.76-B: model catalog autocomplete (POST carrying base_url/api_key; static segment must be
	// registered before :ai_provider_id parameter segment).
	admin.POST("/ai-providers/models", guard("listAiProviderModels"), b7.HandleListAiProviderModels)
	admin.GET("/ai-providers/:ai_provider_id/rate-versions", guard("listAiProviderRateVersions"), b7.HandleListAiProviderRateVersions)
	admin.POST("/ai-providers/:ai_provider_id/rate-versions", guard("createAiProviderRateVersion"), b7.HandleCreateAiProviderRateVersion)
	admin.GET("/ai-usage/calls", guard("listAiUsageCalls"), b7.HandleListAiUsageCalls)
	admin.GET("/ai-usage/summary", guard("listAiUsageSummary"), b7.HandleListAiUsageSummary)
	admin.POST("/ai-providers/:ai_provider_id/connection-tests", guard("testAiProviderConnection"), b7.HandleTestAiProviderConnection)

	admin.GET("/knowledge-entries", guard("listKnowledgeEntries"), b7.HandleListKnowledgeEntries)
	admin.POST("/knowledge-entries", guard("createKnowledgeEntry"), b7.HandleCreateKnowledgeEntry)
	admin.GET("/knowledge-entries/:knowledge_entry_id", guard("getKnowledgeEntry"), b7.HandleGetKnowledgeEntry)
	admin.PUT("/knowledge-entries/:knowledge_entry_id", guard("replaceKnowledgeEntry"), b7.HandleReplaceKnowledgeEntry)
	admin.DELETE("/knowledge-entries/:knowledge_entry_id", guard("deleteKnowledgeEntry"), b7.HandleDeleteKnowledgeEntry)
	admin.POST("/knowledge-entries/:knowledge_entry_id/evaluations", guard("evaluateKnowledgeEntry"), b7.HandleEvaluateKnowledgeEntry)
}

// mountB6Routes mounts B6 domain routes (drafts / Runs / findings / evidence / task reads).
// Guard enforces RequireBusinessOperation (default-deny operation registration, session authentication,
// and CSRF prerequisites; actor relationships verified in application layer); getTask is mounted here with
// task-reader semantics, replacing B3 admin-only assumption.
func mountB6Routes(engine *gin.Engine, deps ServeDeps) {
	b6 := deps.B6
	guard := func(op string) gin.HandlerFunc {
		return RequireBusinessOperation(op, businessSessions(deps))
	}

	api := engine.Group("/api/v4")
	drafts := api.Group("/change-drafts")
	drafts.GET("", guard("listChangeDrafts"), b6.HandleListChangeDrafts)
	drafts.POST("", guard("createChangeDraft"), b6.HandleCreateChangeDraft)
	drafts.GET("/:draft_id", guard("getChangeDraft"), b6.HandleGetChangeDraft)
	drafts.PATCH("/:draft_id", guard("updateChangeDraft"), b6.HandleUpdateChangeDraft)
	drafts.DELETE("/:draft_id", guard("deleteChangeDraft"), b6.HandleDeleteChangeDraft)
	drafts.PUT("/:draft_id/sql", guard("replaceDraftSql"), b6.HandleReplaceDraftSql)
	drafts.POST("/:draft_id/sql-reveals", guard("revealDraftSql"), b6.HandleRevealDraftSql)
	// RCP-20261101-EPHEMERAL-PRECHECK: precheck moves to ephemeral endpoint (zero DB writes);
	// draft-scoped review start route is retired.
	api.POST("/review-runs", guard("runDraftReview"), b6.HandleStartEphemeralReview)

	api.GET("/review-runs/:review_run_id", guard("getReviewRun"), b6.HandleGetReviewRun)
	api.GET("/review-runs/:review_run_id/findings", guard("listReviewRunFindings"), b6.HandleListReviewRunFindings)
	// RCP-20261022-IDENTIFIER-MAPPING-REVEAL (approved by owner 2026-10-29):
	// authorized reveal of anonymized identifier mappings (draft owner only, audited per call, no-store).
	api.POST("/review-runs/:review_run_id/identifier-mapping-reveals", guard("revealIdentifierMapping"), b6.HandleRevealIdentifierMapping)
	api.GET("/review-findings/:finding_id/evidence", guard("listReviewFindingEvidence"), b6.HandleListReviewFindingEvidence)
	api.POST("/review-evidence/:evidence_id/raw-reveals", guard("revealRawReviewEvidence"), b6.HandleRevealRawReviewEvidence)
	api.POST("/review-evidence/:evidence_id/raw-copy-events", guard("recordRawReviewEvidenceCopy"), b6.HandleRecordRawReviewEvidenceCopy)

	// Task read: replaces B3 mount when B6 is present (registering the same path twice would panic,
	// so mountB3Routes skips mounting it when deps.B6 is non-nil).
	api.GET("/tasks/:task_id", guard("getTask"), b6.HandleGetTask)
}

// mountB3Routes mounts B3 domain routes; every route binds an operationId and passes through
// RequireOperation guard (default deny + double-submit CSRF).
func mountB3Routes(engine *gin.Engine, deps ServeDeps) {
	b3 := deps.B3
	guard := func(op string) gin.HandlerFunc {
		return RequireOperation(op, b3.Sessions)
	}

	api := engine.Group("/api/v4")
	// Authentication entry points (anonymous). SEC-0004: site-initiated anonymous state-changing
	// entry points (local login, registration, LDAP login, OIDC redirect start) attach same-origin validation --
	// cross-site Origin / Referer / Sec-Fetch-Site: cross-site are rejected with 403, while non-browser clients
	// without origin headers are allowed; anonymous endpoints are exempt from double-submit CSRF (no session yet),
	// making same-origin check their sole browser-side defense (api-conventions S8).
	// OIDC callback does not attach RequireSameOrigin (Review R2 [High] fix): IdP 302 redirect back is a
	// cross-site top-level navigation where real browsers send Sec-Fetch-Site: cross-site and IdP Referer,
	// which same-origin check would block with 403; browser-side defense for callback is handled by the
	// HttpOnly+SameSite=Lax state cookie set during start (403 state mismatch in handler) and PKCE.
	api.POST("/auth/login", RequireSameOrigin(), guard("login"), b3.HandleLogin)
	api.POST("/auth/register", RequireSameOrigin(), guard("register"), b3.HandleRegister)
	api.GET("/auth/providers", guard("listAuthenticationProviders"), b3.HandleListAuthProviders)
	api.POST("/auth/ldap/login", RequireSameOrigin(), guard("loginWithLdap"), b3.HandleLdapLogin)
	api.GET("/auth/oidc/:provider_key/start", RequireSameOrigin(), guard("startOidcLogin"), b3.HandleOidcStart)
	api.GET("/auth/oidc/:provider_key/callback", guard("finishOidcLogin"), b3.HandleOidcCallback)
	// Session.
	api.POST("/auth/logout", guard("logout"), b3.HandleLogout)
	api.GET("/users/me", guard("getCurrentUser"), b3.HandleCurrentUser)
	api.POST("/users/me/password", guard("changeMyPassword"), b3.HandleChangeMyPassword)
	api.GET("/users/me/flows", guard("listCurrentUserFlows"), b3.HandleCurrentUserFlows)
	// Management surface: Users.
	admin := api.Group("/admin")
	admin.GET("/users", guard("listUsers"), b3.HandleListUsers)
	admin.POST("/users", guard("createUser"), b3.HandleCreateUser)
	admin.PATCH("/users/:user_id", guard("updateUser"), b3.HandleUpdateUser)
	admin.POST("/users/:user_id/password", guard("resetUserPassword"), b3.HandleResetUserPassword)
	admin.DELETE("/users/:user_id", guard("deleteUser"), b3.HandleDeleteUser)
	admin.GET("/users/:user_id/deletion-impact", guard("getUserDeletionImpact"), b3.HandleUserDeletionImpact)
	// Management surface: Flows.
	admin.GET("/flows", guard("listFlows"), b3.HandleListFlows)
	admin.POST("/flows", guard("createFlow"), b3.HandleCreateFlow)
	admin.GET("/flows/:flow_id", guard("getFlow"), b3.HandleGetFlow)
	admin.PUT("/flows/:flow_id", guard("replaceFlow"), b3.HandleReplaceFlow)
	admin.DELETE("/flows/:flow_id", guard("deleteFlow"), b3.HandleDeleteFlow)
	// Management surface: Datasources.
	admin.GET("/datasources", guard("listDatasources"), b3.HandleListDatasources)
	admin.POST("/datasources", guard("createDatasource"), b3.HandleCreateDatasource)
	admin.GET("/datasources/:datasource_id", guard("getDatasource"), b3.HandleGetDatasource)
	admin.PUT("/datasources/:datasource_id", guard("replaceDatasource"), b3.HandleReplaceDatasource)
	admin.DELETE("/datasources/:datasource_id", guard("deleteDatasource"), b3.HandleDeleteDatasource)
	admin.POST("/datasources/:datasource_id/connection-tests", guard("testDatasourceConnection"), b3.HandleTestDatasourceConnection)
	admin.GET("/datasources/:datasource_id/capabilities", guard("getDatasourceCapabilities"), b3.HandleDatasourceCapabilities)
	// Management surface: Permission groups.
	admin.GET("/permission-groups", guard("listPermissionGroups"), b3.HandleListPermissionGroups)
	admin.POST("/permission-groups", guard("createPermissionGroup"), b3.HandleCreatePermissionGroup)
	admin.GET("/permission-groups/:group_id", guard("getPermissionGroup"), b3.HandleGetPermissionGroup)
	admin.PUT("/permission-groups/:group_id", guard("replacePermissionGroup"), b3.HandleReplacePermissionGroup)
	admin.DELETE("/permission-groups/:group_id", guard("deletePermissionGroup"), b3.HandleDeletePermissionGroup)
	// Management surface: Identity Providers.
	admin.GET("/identity-providers", guard("listIdentityProviders"), b3.HandleListIdentityProviders)
	admin.POST("/identity-providers", guard("createIdentityProvider"), b3.HandleCreateIdentityProvider)
	admin.GET("/identity-providers/:identity_provider_id", guard("getIdentityProvider"), b3.HandleGetIdentityProvider)
	admin.PUT("/identity-providers/:identity_provider_id", guard("replaceIdentityProvider"), b3.HandleReplaceIdentityProvider)
	admin.DELETE("/identity-providers/:identity_provider_id", guard("deleteIdentityProvider"), b3.HandleDeleteIdentityProvider)
	admin.POST("/identity-providers/:identity_provider_id/connection-tests", guard("testIdentityProviderConnection"), b3.HandleTestIdentityProviderConnection)
	// Task read: mounted by mountB6Routes with task-reader relationship semantics when B6 is present
	// (admin tasks remain restricted to builtin_admin; business tasks follow draft owner relationship).
	if deps.B6 == nil {
		api.GET("/tasks/:task_id", guard("getTask"), b3.HandleGetTask)
	}
}
