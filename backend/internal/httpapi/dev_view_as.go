// dev_view_as.go implements the development-only "View as" role downgrade middleware (D18).
//
// In development mode only, an authenticated caller may pass X-Yearning-Dev-View-As with one of
// the non-admin presets ("user", "approver", "executor") to preview non-admin UI and authorization
// behavior without mutating the persisted session. Production builds unconditionally ignore the header,
// and privilege escalation (such as "admin") is never permitted in any environment.
package httpapi

import (
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	orderapp "github.com/cookieY/YearningX/backend/internal/changeorder/application"
	"github.com/cookieY/YearningX/backend/internal/identity"
	"github.com/cookieY/YearningX/backend/internal/identity/session"
	queryapp "github.com/cookieY/YearningX/backend/internal/query/application"
	querydomain "github.com/cookieY/YearningX/backend/internal/query/domain"
)

// DevViewAsHeader specifies the development-only role downgrade preset header.
const DevViewAsHeader = "X-Yearning-Dev-View-As"

// DevViewAsMiddleware downgrades the request-scoped session Identity when enabled is true and
// X-Yearning-Dev-View-As carries a valid non-admin preset ("user", "approver", or "executor").
// When enabled is false (production), the header is unconditionally ignored.
func DevViewAsMiddleware(enabled bool) gin.HandlerFunc {
	if !enabled {
		return func(c *gin.Context) {
			c.Next()
		}
	}
	return func(c *gin.Context) {
		preset := strings.ToLower(strings.TrimSpace(c.GetHeader(DevViewAsHeader)))
		switch preset {
		case "user", "approver", "executor":
			if id := session.FromContext(c.Request.Context()); id != nil {
				downgraded := *id
				downgraded.IsBuiltinAdmin = false
				downgraded.Role = "user"
				downgraded.ViewAsRole = preset
				c.Request = c.Request.WithContext(session.WithIdentity(c.Request.Context(), &downgraded))
			}
		}
		c.Next()
	}
}

// currentUserRoleClaims computes effective CurrentUser role flags, honoring any active
// in-memory DevViewAsMiddleware downgrade without altering stored DB user records.
func currentUserRoleClaims(id *session.Identity, view identity.UserView) (isBuiltinAdmin bool, role string, canAccessAdmin bool) {
	if id != nil && id.ViewAsRole != "" {
		return view.IsBuiltinAdmin && id.IsBuiltinAdmin, "user", id.HasAdminAccess()
	}
	return view.IsBuiltinAdmin, view.Role, view.IsBuiltinAdmin || view.Role == "admin"
}

func devViewAsIdentity(c *gin.Context) *session.Identity {
	if c == nil || c.Request == nil {
		return nil
	}
	return session.FromContext(c.Request.Context())
}

func devViewAsRole(c *gin.Context) string {
	id := devViewAsIdentity(c)
	if id == nil {
		return ""
	}
	return id.ViewAsRole
}

// devViewAsBlocksApproval returns true when a dev-mode View-As downgrade hides approval privileges.
func devViewAsBlocksApproval(c *gin.Context) bool {
	role := devViewAsRole(c)
	return role == "user" || role == "executor"
}

// devViewAsBlocksExecution returns true when a dev-mode View-As downgrade hides execution privileges.
func devViewAsBlocksExecution(c *gin.Context) bool {
	role := devViewAsRole(c)
	return role == "user" || role == "approver"
}

func filterActorsExcludingUser(actors []orderapp.ActorView, excludeUserID string) []orderapp.ActorView {
	out := make([]orderapp.ActorView, 0, len(actors))
	for _, actor := range actors {
		if actor.ID != excludeUserID {
			out = append(out, actor)
		}
	}
	return out
}

// projectOrderForDevViewAs clones and strips downgraded approval/execution actor slots for
// the caller while preserving SubmitterUserID and never mutating source slices.
func projectOrderForDevViewAs(c *gin.Context, view *orderapp.OrderView) *orderapp.OrderView {
	if view == nil {
		return nil
	}
	id := devViewAsIdentity(c)
	if id == nil || id.ViewAsRole == "" {
		return view
	}
	hideApprovals := id.ViewAsRole == "user" || id.ViewAsRole == "executor"
	hideExecutions := id.ViewAsRole == "user" || id.ViewAsRole == "approver"
	if !hideApprovals && !hideExecutions {
		return view
	}
	out := *view
	stages := make([]orderapp.StageView, len(view.Stages))
	for i, stage := range view.Stages {
		stCopy := stage
		steps := make([]orderapp.StepView, len(stage.ApprovalSteps))
		for j, step := range stage.ApprovalSteps {
			stepCopy := step
			if hideApprovals {
				stepCopy.Actors = filterActorsExcludingUser(step.Actors, id.UserID)
			} else {
				stepCopy.Actors = slices.Clone(step.Actors)
			}
			steps[j] = stepCopy
		}
		stCopy.ApprovalSteps = steps
		if hideExecutions {
			stCopy.ExecutionActors = filterActorsExcludingUser(stage.ExecutionActors, id.UserID)
		} else {
			stCopy.ExecutionActors = slices.Clone(stage.ExecutionActors)
		}
		stages[i] = stCopy
	}
	out.Stages = stages
	return &out
}

func orderRetainsActor(view *orderapp.OrderView, userID string) bool {
	if view.SubmitterUserID == userID {
		return true
	}
	for _, stage := range view.Stages {
		for _, step := range stage.ApprovalSteps {
			for _, actor := range step.Actors {
				if actor.ID == userID {
					return true
				}
			}
		}
		for _, actor := range stage.ExecutionActors {
			if actor.ID == userID {
				return true
			}
		}
	}
	return false
}

// filterOrdersForDevViewAs projects order actor slots and excludes non-submitter orders whose
// only caller membership came from a stripped role slot.
func filterOrdersForDevViewAs(c *gin.Context, items []*orderapp.OrderView) []*orderapp.OrderView {
	id := devViewAsIdentity(c)
	if id == nil || id.ViewAsRole == "" {
		return items
	}
	out := make([]*orderapp.OrderView, 0, len(items))
	for _, item := range items {
		proj := projectOrderForDevViewAs(c, item)
		if proj != nil && orderRetainsActor(proj, id.UserID) {
			out = append(out, proj)
		}
	}
	return out
}

// effectiveQueryAccessScopeForDevViewAs narrows query access list scope before DB pagination when
// approval slots are stripped, preventing hidden non-self rows from skewing pages or leaking in next_cursor.
func effectiveQueryAccessScopeForDevViewAs(c *gin.Context, scope string) (string, bool) {
	if !devViewAsBlocksApproval(c) {
		return scope, false
	}
	if scope == "reviewer" {
		return "", true
	}
	return "requester", false
}

// filterQueryAccessRequestsForDevViewAs hides non-self query access requests and clears decision
// eligibility when approval slots are stripped.
func filterQueryAccessRequestsForDevViewAs(c *gin.Context, items []queryapp.AccessRequestView) []queryapp.AccessRequestView {
	if !devViewAsBlocksApproval(c) {
		return items
	}
	id := devViewAsIdentity(c)
	out := make([]queryapp.AccessRequestView, 0, len(items))
	for _, item := range items {
		if item.RequesterUserID == id.UserID {
			item.CanDecide = false
			out = append(out, item)
		}
	}
	return out
}

// filterQueryGrantsForDevViewAs hides non-self query grants and clears revocation eligibility when
// approval/revocation slots are stripped.
func filterQueryGrantsForDevViewAs(c *gin.Context, items []queryapp.GrantView) []queryapp.GrantView {
	if !devViewAsBlocksApproval(c) {
		return items
	}
	id := devViewAsIdentity(c)
	out := make([]queryapp.GrantView, 0, len(items))
	for _, item := range items {
		if item.RequesterUserID == id.UserID {
			item.CanRevoke = false
			out = append(out, item)
		}
	}
	return out
}

// filterQueryOrdersForDevViewAs omits non-self query access request entries and clears decision
// eligibility when approval slots are stripped.
func filterQueryOrdersForDevViewAs(c *gin.Context, items []queryapp.QueryOrderEntry) []queryapp.QueryOrderEntry {
	if !devViewAsBlocksApproval(c) {
		return items
	}
	id := devViewAsIdentity(c)
	out := make([]queryapp.QueryOrderEntry, 0, len(items))
	for _, entry := range items {
		if entry.Request != nil {
			if entry.Request.RequesterUserID != id.UserID {
				continue
			}
			reqCopy := *entry.Request
			reqCopy.CanDecide = false
			entry.Request = &reqCopy
		}
		out = append(out, entry)
	}
	return out
}

func rejectQueryApprovalForDevViewAs(c *gin.Context) bool {
	if !devViewAsBlocksApproval(c) {
		return false
	}
	writeCodedError(c, querydomain.ErrNotFound())
	return true
}

func writeQueryAccessRequestForDevViewAs(c *gin.Context, view *queryapp.AccessRequestView) {
	if devViewAsBlocksApproval(c) && view != nil {
		if view.RequesterUserID != b11Actor(c).UserID {
			writeCodedError(c, querydomain.ErrNotFound())
			return
		}
		cp := *view
		cp.CanDecide = false
		WriteSuccess(c, &cp)
		return
	}
	WriteSuccess(c, view)
}

func writeQueryGrantForDevViewAs(c *gin.Context, view *queryapp.GrantView) {
	if devViewAsBlocksApproval(c) && view != nil {
		if view.RequesterUserID != b11Actor(c).UserID {
			writeCodedError(c, querydomain.ErrNotFound())
			return
		}
		cp := *view
		cp.CanRevoke = false
		WriteSuccess(c, &cp)
		return
	}
	WriteSuccess(c, view)
}
