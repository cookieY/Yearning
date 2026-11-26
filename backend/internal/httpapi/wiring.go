// wiring.go explicit production composition root for ServeDeps.
package httpapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/cookieY/YearningX/backend/internal/dashboard"
	"github.com/cookieY/YearningX/backend/internal/notification"
	"github.com/cookieY/YearningX/backend/internal/realtime"
	"github.com/cookieY/YearningX/backend/internal/settings"
)

// BuildServeDeps explicitly constructs and verifies all serve-mode domain dependencies in
// dependency order, propagates the root lifecycle context, and runs startup AI usage reconciliation.
func BuildServeDeps(ctx context.Context, deps ServeDeps) (ServeDeps, error) {
	if ctx == nil {
		return ServeDeps{}, errors.New("httpapi: lifecycle context is required")
	}
	if deps.B3 == nil || deps.B3.DB == nil || deps.B3.Crypter == nil {
		return ServeDeps{}, errors.New("httpapi: B3 platform dependencies (DB/Crypter) are required")
	}
	deps.B3.TrustedProxies = deps.TrustedProxies
	if deps.RealtimeHub == nil {
		deps.RealtimeHub = realtime.NewHub()
	}
	if deps.B6 == nil {
		b6, err := buildB6FromB3(deps.B3)
		if err != nil {
			return ServeDeps{}, fmt.Errorf("httpapi: build review services: %w", err)
		}
		deps.B6 = b6
	}
	if deps.B13 == nil {
		deps.B13 = &B13Deps{Settings: settings.New(deps.B3.DB, deps.B3.Crypter)}
	}
	if deps.Dashboard == nil {
		dashSvc := dashboard.New(deps.B3.DB, deps.B3.Crypter)
		relClient, rerr := dashboard.NewReleaseHTTPClient(deps.ReleaseFeedProxy)
		if rerr != nil {
			return ServeDeps{}, fmt.Errorf("httpapi: build release feed client: %w", rerr)
		}
		dashSvc.ReleaseHTTPClient = relClient
		deps.Dashboard = &dashboard.Deps{
			Service:        dashSvc,
			Guard:          func(op string) gin.HandlerFunc { return RequireOperation(op, deps.B3.Sessions) },
			ReleaseFeedURL: deps.ReleaseFeedURL,
		}
	}
	if deps.B3.Identity != nil && deps.Dashboard != nil && deps.Dashboard.Service != nil {
		dashSvc := deps.Dashboard.Service
		deps.B3.Identity.SetOnUserDeleted(func(string) {
			dashSvc.InvalidateOperationsCache()
		})
	}
	if deps.Notification == nil {
		deps.Notification = &notification.Deps{
			Service: notification.NewService(deps.B3.DB, deps.B3.Crypter),
			Guard:   func(op string) gin.HandlerFunc { return RequireOperation(op, deps.B3.Sessions) },
		}
	}
	if deps.B7 == nil {
		deps.B7 = buildB7FromB6(deps.B6)
	}
	if deps.B9 == nil {
		b9, err := buildB9FromB6(deps.B6)
		if err != nil {
			return ServeDeps{}, fmt.Errorf("httpapi: build changeorder services: %w", err)
		}
		deps.B9 = b9
	}
	if deps.B10 == nil {
		b10, err := buildB10FromB9(deps.B9)
		if err != nil {
			return ServeDeps{}, fmt.Errorf("httpapi: build execution services: %w", err)
		}
		if b10.Sessions == nil {
			b10.Sessions = deps.B3.Sessions
		}
		deps.B10 = b10
	}
	if deps.B11 == nil {
		b11, err := buildB11FromB10(deps.B10)
		if err != nil {
			return ServeDeps{}, fmt.Errorf("httpapi: build query services: %w", err)
		}
		deps.B11 = b11
	}
	if deps.Assistant == nil {
		asst, err := buildAssistantDeps(deps.B3, deps.B6, deps.B9)
		if err != nil {
			return ServeDeps{}, fmt.Errorf("httpapi: build assistant services: %w", err)
		}
		deps.Assistant = asst
	}
	if deps.Diagnostics == nil {
		diag, err := buildDiagnosticsDeps(deps.B3, deps.B6, deps.B9)
		if err != nil {
			return ServeDeps{}, fmt.Errorf("httpapi: build diagnostics services: %w", err)
		}
		deps.Diagnostics = diag
	}

	if deps.B6 != nil && deps.B6.Review != nil {
		if budgetCfg, err := deps.B6.Review.LoadBudgetSettings(ctx, deps.B6.DB); err == nil {
			if rerr := deps.B6.Review.ReconcileDailyUsage(ctx, deps.B6.DB, budgetCfg.Currency); rerr != nil && deps.Logger != nil {
				deps.Logger.Warn("startup ai usage reconciliation failed", zap.Error(rerr))
			}
		} else if deps.Logger != nil {
			deps.Logger.Warn("startup ai budget settings load failed", zap.Error(err))
		}
	}

	deps.StrictWiring = true
	deps.LifecycleCtx = ctx
	return deps, nil
}
