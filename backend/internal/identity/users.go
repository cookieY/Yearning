// users.go user lifecycle management: list, create, update, and the delete_user transactional command
// (repository-commands: delete_user; D003 same-transaction logical reference validation).
package identity

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
)

// UserView is the user representation in API responses; never includes password_hash.
type UserView struct {
	ID             string    `json:"id"`
	Username       string    `json:"username"`
	DisplayName    string    `json:"display_name"`
	Email          *string   `json:"email"`
	IsBuiltinAdmin bool      `json:"is_builtin_admin"`
	Role           string    `json:"role"`
	Version        int64     `json:"version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// UserViewOf converts a UserModel into a UserView.
func UserViewOf(u *UserModel) UserView {
	role := u.Role
	if u.IsBuiltinAdmin {
		role = "admin"
	} else if role == "" {
		role = "user"
	}
	return UserView{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email,
		IsBuiltinAdmin: u.IsBuiltinAdmin, Role: role, Version: u.Version,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}

// ListUsers paginates users in creation order (limit + after cursor). An empty kind means no filter;
// builtin_admin/local_user narrows by account kind (RCP-20261105-ADMIN-LIST-FIELD-FILTERS,
// contract §18.73 admin list column filtering; narrows only and never widens authorization scope).
func (s *Service) ListUsers(ctx context.Context, limit int, after, search, kind string) ([]UserView, *string, error) {
	if limit < 1 || limit > 200 {
		return nil, nil, bizerr.ErrValidationFailed
	}
	switch kind {
	case "", "builtin_admin", "local_user":
	default:
		return nil, nil, bizerr.ErrValidationFailed
	}
	q := s.db.WithContext(ctx).Model(&UserModel{}).
		Order("created_at asc, id asc").Limit(limit + 1)
	if after != "" {
		// after is the ID cursor of the last item on the previous page.
		var cursor UserModel
		if err := s.db.WithContext(ctx).Select("created_at", "id").
			Where("id = ?", after).First(&cursor).Error; err != nil {
			return nil, nil, bizerr.ErrNotFound
		}
		q = q.Where("(created_at, id) > (?, ?)", cursor.CreatedAt, cursor.ID)
	}
	switch kind {
	case "builtin_admin":
		q = q.Where("is_builtin_admin = ?", true)
	case "local_user":
		q = q.Where("is_builtin_admin = ?", false)
	}
	if search != "" {
		needle := "%" + search + "%"
		q = q.Where("username ILIKE ? or display_name ILIKE ? or email ILIKE ?", needle, needle, needle)
	}
	var rows []UserModel
	if err := q.Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	views := make([]UserView, 0, len(rows))
	for i := range rows {
		views = append(views, UserViewOf(&rows[i]))
	}
	var next *string
	if hasMore && len(rows) > 0 {
		next = &rows[len(rows)-1].ID
	}
	return views, next, nil
}

// CreateUser creates a user as an administrator: immediately active with zero business privileges. role accepts 'user' | 'admin' (default 'user').
func (s *Service) CreateUser(ctx context.Context, actor ActorRef, username, displayName, email, password string, meta RequestMeta, roles ...string) (*UserView, error) {
	if username == "" || displayName == "" || len(password) < 12 || !validOptionalEmail(email) {
		return nil, bizerr.ErrValidationFailed
	}
	role := "user"
	if len(roles) > 0 && roles[0] != "" {
		role = roles[0]
	}
	if role != "user" && role != "admin" {
		return nil, bizerr.ErrValidationFailed
	}
	user, err := s.createUserRowWithRole(ctx, username, displayName, email, password, role)
	if err != nil {
		return nil, err
	}
	view := UserViewOf(user)
	_ = s.audit.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "user.created", ActorKind: "user", ActorID: &actor.UserID, ActorUsername: &actor.Username,
		ResourceType: "user", ResourceID: &user.ID, Action: "create_user", Outcome: "succeeded",
		RequestID: strPtr(meta.RequestID),
		Metadata:  map[string]any{"username": user.Username, "role": view.Role},
	})
	return &view, nil
}

// UpdateUser updates display_name/email/role (optimistic lock). Username and built-in admin status are immutable.
func (s *Service) UpdateUser(ctx context.Context, actor ActorRef, userID string, expectVersion int64, displayName *string, email *string, meta RequestMeta, roles ...*string) (*UserView, error) {
	var rolePtr *string
	if len(roles) > 0 {
		rolePtr = roles[0]
	}
	if displayName == nil && email == nil && rolePtr == nil {
		return nil, bizerr.ErrValidationFailed
	}
	updates := map[string]any{"version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC()}
	if displayName != nil {
		if *displayName == "" {
			return nil, bizerr.ErrValidationFailed
		}
		updates["display_name"] = *displayName
	}
	if email != nil {
		if !validOptionalEmail(*email) {
			return nil, bizerr.ErrValidationFailed
		}
		updates["email"] = nilIfEmpty(email)
	}
	if rolePtr != nil {
		if *rolePtr != "user" && *rolePtr != "admin" {
			return nil, bizerr.ErrValidationFailed
		}
		var target UserModel
		if err := s.db.WithContext(ctx).Where("id = ?", userID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, bizerr.ErrNotFound
			}
			return nil, err
		}
		if target.IsBuiltinAdmin && *rolePtr != "admin" {
			return nil, bizerr.AdminImmutableError{}
		}
		updates["role"] = *rolePtr
	}
	res := s.db.WithContext(ctx).Model(&UserModel{}).
		Where("id = ? and version = ?", userID, expectVersion).
		Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if s.userExists(ctx, userID) {
			return nil, bizerr.ErrVersionConflict
		}
		return nil, bizerr.ErrNotFound
	}
	var user UserModel
	if err := s.db.WithContext(ctx).Where("id = ?", userID).First(&user).Error; err != nil {
		return nil, err
	}
	view := UserViewOf(&user)
	_ = s.audit.Audit(ctx, nil, auditlog.AuditInput{
		EventType: "user.updated", ActorKind: "user", ActorID: &actor.UserID, ActorUsername: &actor.Username,
		ResourceType: "user", ResourceID: &userID, Action: "update_user", Outcome: "succeeded",
		RequestID: strPtr(meta.RequestID),
		Metadata:  map[string]any{"role": view.Role},
	})
	return &view, nil
}

// DeletionImpactBlocker represents a single blocker in the user deletion impact report.
type DeletionImpactBlocker struct {
	Code  string `json:"code"`
	Count int64  `json:"count"`
}

// DeletionImpact conforms to OpenAPI UserDeletionImpact.
type DeletionImpact struct {
	UserID                       string                  `json:"user_id"`
	CanDelete                    bool                    `json:"can_delete"`
	Blockers                     []DeletionImpactBlocker `json:"blockers"`
	FlowActorReferences          int64                   `json:"flow_actor_references"`
	PermissionGroupMemberships   int64                   `json:"permission_group_memberships"`
	ActiveQueryGrants            int64                   `json:"active_query_grants"`
	ActiveQuerySessions          int64                   `json:"active_query_sessions"`
	HistoricalSnapshotsPreserved bool                    `json:"historical_snapshots_preserved"`
	CalculatedAt                 time.Time               `json:"calculated_at"`
}

// nonTerminalOrderStates matches non-terminal states in the change_orders state machine.
var nonTerminalOrderStates = []string{
	"submitted", "stage_approval_active", "stage_execution_pending", "scheduled",
	"running", "result_unknown", "blocked_datasource_unavailable",
}

// GetDeletionImpact computes the impact of deleting a user (read-only).
func (s *Service) GetDeletionImpact(ctx context.Context, actor ActorRef, userID string) (*DeletionImpact, error) {
	var user UserModel
	if err := s.db.WithContext(ctx).Where("id = ?", userID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizerr.ErrNotFound
		}
		return nil, err
	}
	impact := &DeletionImpact{
		UserID:    userID,
		CanDelete: true,
		// Serialize as [] rather than null when there are no blockers (contract UserDeletionImpact.blockers is an array, F12 #018).
		Blockers:                     []DeletionImpactBlocker{},
		HistoricalSnapshotsPreserved: true,
		CalculatedAt:                 time.Now().UTC(),
	}
	db := s.db.WithContext(ctx)
	if user.IsBuiltinAdmin {
		impact.Blockers = append(impact.Blockers, DeletionImpactBlocker{Code: "builtin_admin_immutable", Count: 1})
	}
	var activeOrders int64
	if err := db.Table("change_orders").
		Where("submitter_user_id_snapshot = ? and state in ?", userID, nonTerminalOrderStates).
		Count(&activeOrders).Error; err != nil {
		return nil, err
	}
	if activeOrders > 0 {
		impact.Blockers = append(impact.Blockers, DeletionImpactBlocker{Code: "active_orders", Count: activeOrders})
	}
	if err := db.Table("flow_approval_step_actors").Where("user_id = ?", userID).Count(&impact.FlowActorReferences).Error; err != nil {
		return nil, err
	}
	var execRefs int64
	if err := db.Table("flow_execution_actors").Where("user_id = ?", userID).Count(&execRefs).Error; err != nil {
		return nil, err
	}
	impact.FlowActorReferences += execRefs
	if err := db.Table("permission_group_members").Where("user_id = ?", userID).Count(&impact.PermissionGroupMemberships).Error; err != nil {
		return nil, err
	}
	if err := db.Table("query_grants").Where("requester_user_id_snapshot = ? and state = 'active'", userID).Count(&impact.ActiveQueryGrants).Error; err != nil {
		return nil, err
	}
	if err := db.Table("query_sessions").Where("user_id_snapshot = ? and state = 'active'", userID).Count(&impact.ActiveQuerySessions).Error; err != nil {
		return nil, err
	}
	impact.CanDelete = len(impact.Blockers) == 0
	return impact, nil
}

// DeleteUser executes the delete_user command (serializable):
// after verifying the user is not the built-in admin and has no non-terminal change orders, removes
// membership/role references, revokes Query Grants and Sessions, marks flows with emptied nodes as invalid,
// deletes the user row, and writes domain_events/outbox and audit records in the same transaction.
func (s *Service) DeleteUser(ctx context.Context, actor ActorRef, userID string, expectVersion int64, meta RequestMeta) error {
	var affectedFlows map[string]bool
	err := txutil.Serializable(ctx, s.db, func(tx *gorm.DB) error {
		affectedFlows = map[string]bool{}
		var user UserModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", userID).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bizerr.ErrNotFound
			}
			return err
		}
		if user.IsBuiltinAdmin {
			return bizerr.AdminImmutableError{}
		}
		if user.Version != expectVersion {
			return bizerr.ErrConcurrentModification
		}
		var activeOrders int64
		if err := tx.Table("change_orders").
			Where("submitter_user_id_snapshot = ? and state in ?", userID, nonTerminalOrderStates).
			Count(&activeOrders).Error; err != nil {
			return err
		}
		if activeOrders > 0 {
			return bizerr.ActiveOrdersError{Count: activeOrders}
		}
		// Collect affected approval nodes, delete memberships, and evaluate node validity from remaining members.
		type stepRef struct{ StepID string }
		var steps []stepRef
		if err := tx.Table("flow_approval_step_actors").
			Select("step_id").Where("user_id = ?", userID).Scan(&steps).Error; err != nil {
			return err
		}
		type stageRef struct{ StageID string }
		var stages []stageRef
		if err := tx.Table("flow_execution_actors").
			Select("stage_id").Where("user_id = ?", userID).Scan(&stages).Error; err != nil {
			return err
		}

		// Delete external identities, sessions, and group/node memberships (logical-relations: delete_source).
		if err := tx.Where("user_id = ?", userID).Delete(&ExternalIdentityModel{}).Error; err != nil {
			return err
		}
		if err := tx.Exec("delete from auth_sessions where user_id = ?", userID).Error; err != nil {
			return err
		}
		if err := tx.Exec("delete from permission_group_members where user_id = ?", userID).Error; err != nil {
			return err
		}
		if err := tx.Exec("delete from flow_approval_step_actors where user_id = ?", userID).Error; err != nil {
			return err
		}
		if err := tx.Exec("delete from flow_execution_actors where user_id = ?", userID).Error; err != nil {
			return err
		}
		if err := tx.Exec("delete from sql_diagnostics_ai_reports where user_id = ?", userID).Error; err != nil {
			return err
		}
		if err := tx.Exec("delete from sql_diagnostics_order_links where confirmed_by_user_id = ?", userID).Error; err != nil {
			return err
		}
		// Revoke Query Grants and terminate active Query Sessions (reason fixed to user_deleted).
		if err := tx.Exec(`update query_grants set state = 'revoked',
			revoked_by_user_id_snapshot = ?, revoked_by_username_snapshot = ?,
			revoke_reason = 'user_deleted', revoked_at = now(),
			terminal_at = now(), terminal_reason = 'user_deleted', version = version + 1
			where requester_user_id_snapshot = ? and state = 'active'`,
			actor.UserID, actor.Username, userID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`update query_sessions set state = 'user_deleted',
			closed_at = now(), close_reason = 'user_deleted', version = version + 1
			where user_id_snapshot = ? and state = 'active'`, userID).Error; err != nil {
			return err
		}
		// Empty approval nodes invalidate the flow (flows remain valid while nodes still have other candidates).
		if len(steps) > 0 {
			stepIDs := make([]string, len(steps))
			for i, ref := range steps {
				stepIDs[i] = ref.StepID
			}
			var emptiedFlowIDs []string
			if err := tx.Raw(`
				select distinct s.flow_id::text
				from flow_approval_steps s
				where s.id in ?
				  and not exists (
				      select 1 from flow_approval_step_actors a where a.step_id = s.id
				  )`, stepIDs).Scan(&emptiedFlowIDs).Error; err != nil {
				return err
			}
			for _, flowID := range emptiedFlowIDs {
				if flowID != "" {
					affectedFlows[flowID] = true
				}
			}
		}
		if len(stages) > 0 {
			stageIDs := make([]string, len(stages))
			for i, ref := range stages {
				stageIDs[i] = ref.StageID
			}
			var emptiedFlowIDs []string
			if err := tx.Raw(`
				select distinct st.flow_id::text
				from flow_stages st
				where st.id in ?
				  and not exists (
				      select 1 from flow_execution_actors a where a.stage_id = st.id
				  )`, stageIDs).Scan(&emptiedFlowIDs).Error; err != nil {
				return err
			}
			for _, flowID := range emptiedFlowIDs {
				if flowID != "" {
					affectedFlows[flowID] = true
				}
			}
		}
		if len(affectedFlows) > 0 {
			flowIDs := make([]string, 0, len(affectedFlows))
			for flowID := range affectedFlows {
				flowIDs = append(flowIDs, flowID)
			}
			sort.Strings(flowIDs)
			if err := tx.Exec(`update flows set status = 'invalid', version = version + 1, updated_at = now() where id in ?`, flowIDs).Error; err != nil {
				return err
			}
		}
		// Delete the user row itself (delete_exactly_one).
		res := tx.Where("id = ?", userID).Delete(&UserModel{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return bizerr.ErrNotFound
		}
		// Persist domain events and audit records in the same transaction.
		actorSnap := auditlog.Actor{Kind: "user", UserID: actor.UserID, Username: actor.Username}
		if err := s.audit.Emit(ctx, tx, "user.deleted", "user:"+userID, actorSnap, map[string]any{
			"user_id": userID, "username": user.Username,
		}); err != nil {
			return err
		}
		for flowID := range affectedFlows {
			if err := s.audit.Emit(ctx, tx, "workflow.node_invalidated", "flow:"+flowID, actorSnap, map[string]any{
				"flow_id": flowID, "reason": "actor_deleted",
			}); err != nil {
				return err
			}
		}
		return s.audit.Audit(ctx, tx, auditlog.AuditInput{
			EventType: "user.deleted", ActorKind: "user", ActorID: &actor.UserID, ActorUsername: &actor.Username,
			ResourceType: "user", ResourceID: &userID, Action: "delete_user", Outcome: "succeeded",
			RequestID: strPtr(meta.RequestID),
			Metadata:  map[string]any{"invalidated_flow_count": len(affectedFlows)},
		})
	})
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) userExists(ctx context.Context, userID string) bool {
	var count int64
	_ = s.db.WithContext(ctx).Model(&UserModel{}).Where("id = ?", userID).Count(&count).Error
	return count > 0
}

// newUserID exposes user ID generation for adjacent domains (tests).
func newUserID() string { return uuid.NewString() }

func nilIfEmpty(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func validOptionalEmail(email string) bool {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return true
	}
	if len(trimmed) > 320 || strings.ContainsAny(trimmed, " \t\r\n") {
		return false
	}
	at := strings.IndexByte(trimmed, '@')
	if at <= 0 || at != strings.LastIndexByte(trimmed, '@') {
		return false
	}
	domain := trimmed[at+1:]
	dot := strings.IndexByte(domain, '.')
	return dot > 0 && dot < len(domain)-1
}
