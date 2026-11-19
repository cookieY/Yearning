// groups.go implements permission group management: atomic replacement of members and flow grants,
// deletion impact, and immediate authorization effect (no caching; union resolved from DB on every check).
package workflow

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
)

// GroupWrite corresponds to the OpenAPI PermissionGroupWrite schema.
type GroupWrite struct {
	Name           string   `json:"name"`
	Enabled        bool     `json:"enabled"`
	MemberUserIDs  []string `json:"member_user_ids"`
	GrantedFlowIDs []string `json:"granted_flow_ids"`
}

// GroupView is the permission group response view.
type GroupView struct {
	GroupWrite
	ID        string    `json:"id"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateGroup creates a permission group (member and grant sets may be empty).
func (s *Service) CreateGroup(ctx context.Context, actor AdminActor, w *GroupWrite, meta RequestMeta) (*GroupView, error) {
	if w.Name == "" || len(w.Name) > 128 {
		return nil, bizerr.ErrValidationFailed
	}
	if err := validateIDSet(w.MemberUserIDs); err != nil {
		return nil, err
	}
	if err := validateIDSet(w.GrantedFlowIDs); err != nil {
		return nil, err
	}
	groupID := uuid.NewString()
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := assertUsersExist(ctx, tx, w.MemberUserIDs); err != nil {
			return err
		}
		if err := assertFlowsExist(ctx, tx, w.GrantedFlowIDs); err != nil {
			return err
		}
		row := PermissionGroupModel{
			ID: groupID, Name: w.Name, Enabled: w.Enabled,
			Version: 1, CreatedByUserID: actor.UserID, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			if isUniqueErr(err) {
				return bizerr.ErrAlreadyExists
			}
			return err
		}
		if err := writeGroupMembership(ctx, tx, groupID, w); err != nil {
			return err
		}
		_ = s.audit.NotifyRealtimeTargets(ctx, tx, "flow.updated", "permission-groups/"+groupID, "created",
			auditlog.Actor{Kind: "user", UserID: actor.UserID, Username: actor.Username},
			w.MemberUserIDs, true)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.auditGroup(ctx, actor, "permission_group.created", "create_permission_group", groupID, meta)
	return s.GetGroup(ctx, groupID)
}

// ReplaceGroup atomically replaces group members and flow grants (replace_membership_exact).
// Authorization changes take effect immediately: uncommitted operations are affected, while submitted tickets are unaffected.
func (s *Service) ReplaceGroup(ctx context.Context, actor AdminActor, groupID string, expectVersion int64, w *GroupWrite, meta RequestMeta) (*GroupView, error) {
	if w.Name == "" || len(w.Name) > 128 {
		return nil, bizerr.ErrValidationFailed
	}
	if err := validateIDSet(w.MemberUserIDs); err != nil {
		return nil, err
	}
	if err := validateIDSet(w.GrantedFlowIDs); err != nil {
		return nil, err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row PermissionGroupModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", groupID).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bizerr.ErrNotFound
			}
			return err
		}
		if row.Version != expectVersion {
			return bizerr.ErrVersionConflict
		}
		if err := assertUsersExist(ctx, tx, w.MemberUserIDs); err != nil {
			return err
		}
		if err := assertFlowsExist(ctx, tx, w.GrantedFlowIDs); err != nil {
			return err
		}
		var prevMembers []string
		if err := tx.WithContext(ctx).Model(&PermissionGroupMemberModel{}).
			Where("permission_group_id = ?", groupID).
			Pluck("user_id", &prevMembers).Error; err != nil {
			return err
		}
		if err := tx.Where("permission_group_id = ?", groupID).
			Delete(&PermissionGroupMemberModel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("permission_group_id = ?", groupID).
			Delete(&PermissionGroupFlowGrantModel{}).Error; err != nil {
			return err
		}
		if err := writeGroupMembership(ctx, tx, groupID, w); err != nil {
			return err
		}
		res := tx.Model(&PermissionGroupModel{}).
			Where("id = ? and version = ?", groupID, expectVersion).
			Updates(map[string]any{
				"name": w.Name, "enabled": w.Enabled,
				"version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return bizerr.ErrVersionConflict
		}
		allMembers := append(prevMembers, w.MemberUserIDs...)
		_ = s.audit.NotifyRealtimeTargets(ctx, tx, "flow.updated", "permission-groups/"+groupID, "updated",
			auditlog.Actor{Kind: "user", UserID: actor.UserID, Username: actor.Username},
			allMembers, true)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	s.auditGroup(ctx, actor, "permission_group.updated", "replace_permission_group", groupID, meta)
	return s.GetGroup(ctx, groupID)
}

// DeleteGroup deletes a permission group: members and flow grants are removed via cascade; submitted tickets are unaffected.
// Callers display the impact scope via GetGroup and require secondary confirmation before calling DeleteGroup.
func (s *Service) DeleteGroup(ctx context.Context, actor AdminActor, groupID string, expectVersion int64, meta RequestMeta) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row PermissionGroupModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", groupID).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bizerr.ErrNotFound
			}
			return err
		}
		if row.Version != expectVersion {
			// admin_delete profile is C1004-only: stale If-Match surfaces
			// as CONCURRENT_MODIFICATION.
			return bizerr.ErrConcurrentModification
		}
		var prevMembers []string
		if err := tx.WithContext(ctx).Model(&PermissionGroupMemberModel{}).
			Where("permission_group_id = ?", groupID).
			Pluck("user_id", &prevMembers).Error; err != nil {
			return err
		}
		if err := tx.Where("permission_group_id = ?", groupID).
			Delete(&PermissionGroupMemberModel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("permission_group_id = ?", groupID).
			Delete(&PermissionGroupFlowGrantModel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", groupID).Delete(&PermissionGroupModel{}).Error; err != nil {
			return err
		}
		if err := s.audit.Audit(ctx, tx, auditlog.AuditInput{
			EventType: "permission_group.deleted", ActorKind: "user",
			ActorID: &actor.UserID, ActorUsername: &actor.Username,
			ResourceType: "permission_group", ResourceID: &groupID,
			Action: "delete_permission_group", Outcome: "succeeded",
			RequestID: strPtr(meta.RequestID),
		}); err != nil {
			return err
		}
		_ = s.audit.NotifyRealtimeTargets(ctx, tx, "flow.updated", "permission-groups/"+groupID, "deleted",
			auditlog.Actor{Kind: "user", UserID: actor.UserID, Username: actor.Username},
			prevMembers, true)
		return nil
	})
	return err
}

// GetGroup and ListGroups return permission group views.
func (s *Service) GetGroup(ctx context.Context, groupID string) (*GroupView, error) {
	var row PermissionGroupModel
	if err := s.db.WithContext(ctx).Where("id = ?", groupID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizerr.ErrNotFound
		}
		return nil, err
	}
	view := newGroupView(row)
	var members []PermissionGroupMemberModel
	if err := s.db.WithContext(ctx).Where("permission_group_id = ?", groupID).
		Order("user_id asc").Find(&members).Error; err != nil {
		return nil, err
	}
	for _, m := range members {
		view.MemberUserIDs = append(view.MemberUserIDs, m.UserID)
	}
	var grants []PermissionGroupFlowGrantModel
	if err := s.db.WithContext(ctx).Where("permission_group_id = ?", groupID).
		Order("flow_id asc").Find(&grants).Error; err != nil {
		return nil, err
	}
	for _, g := range grants {
		view.GrantedFlowIDs = append(view.GrantedFlowIDs, g.FlowID)
	}
	return &view, nil
}

// newGroupView constructs a group view skeleton with empty member and grant slices. Both collections are required
// arrays in OpenAPI and must serialize as [] rather than null, or frontend iteration per contract will crash.
func newGroupView(row PermissionGroupModel) GroupView {
	return GroupView{
		GroupWrite: GroupWrite{
			Name: row.Name, Enabled: row.Enabled,
			MemberUserIDs:  []string{},
			GrantedFlowIDs: []string{},
		},
		ID: row.ID, Version: row.Version,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

// ListGroups returns a paginated list of groups. search filters by name substring (§18.73 server-side q);
// enabled, memberUserID, and flowID are column-level narrowing filters (RCP-20261105-ADMIN-LIST-FIELD-FILTERS)
// where zero values mean unset and multiple filters combine with AND; member/grant filters use join-table EXISTS without altering authorization semantics.
func (s *Service) ListGroups(ctx context.Context, limit int, after, search string, enabled *bool, memberUserID, flowID string) ([]GroupView, *string, error) {
	if limit < 1 || limit > 200 {
		return nil, nil, bizerr.ErrValidationFailed
	}
	q := s.db.WithContext(ctx).Model(&PermissionGroupModel{}).
		Order("created_at asc, id asc").Limit(limit + 1)
	if after != "" {
		var cursor PermissionGroupModel
		if err := s.db.WithContext(ctx).Select("created_at", "id").
			Where("id = ?", after).First(&cursor).Error; err != nil {
			return nil, nil, bizerr.ErrNotFound
		}
		q = q.Where("(created_at, id) > (?, ?)", cursor.CreatedAt, cursor.ID)
	}
	if enabled != nil {
		q = q.Where("enabled = ?", *enabled)
	}
	if memberUserID != "" {
		q = q.Where(`exists (select 1 from permission_group_members m
 where m.permission_group_id = permission_groups.id and m.user_id = ?)`, memberUserID)
	}
	if flowID != "" {
		q = q.Where(`exists (select 1 from permission_group_flow_grants g
 where g.permission_group_id = permission_groups.id and g.flow_id = ?)`, flowID)
	}
	if search != "" {
		q = q.Where("name ILIKE ?", "%"+search+"%")
	}
	var rows []PermissionGroupModel
	if err := q.Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	views := make([]GroupView, 0, len(rows))
	for i := range rows {
		v, err := s.GetGroup(ctx, rows[i].ID)
		if err != nil {
			return nil, nil, err
		}
		views = append(views, *v)
	}
	var next *string
	if hasMore && len(rows) > 0 {
		next = &rows[len(rows)-1].ID
	}
	return views, next, nil
}

func writeGroupMembership(ctx context.Context, tx *gorm.DB, groupID string, w *GroupWrite) error {
	for _, uid := range w.MemberUserIDs {
		if err := tx.WithContext(ctx).Create(&PermissionGroupMemberModel{
			PermissionGroupID: groupID, UserID: uid,
		}).Error; err != nil {
			if isUniqueErr(err) {
				return bizerr.ErrAlreadyExists
			}
			return err
		}
	}
	for _, fid := range w.GrantedFlowIDs {
		if err := tx.WithContext(ctx).Create(&PermissionGroupFlowGrantModel{
			PermissionGroupID: groupID, FlowID: fid,
		}).Error; err != nil {
			if isUniqueErr(err) {
				return bizerr.ErrAlreadyExists
			}
			return err
		}
	}
	return nil
}

func validateIDSet(ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return bizerr.ErrValidationFailed
		}
		seen[id] = true
	}
	return nil
}

func assertUsersExist(ctx context.Context, tx *gorm.DB, ids []string) error {
	for _, id := range ids {
		var got []string
		if err := tx.WithContext(ctx).Model(&UserRow{}).
			Clauses(clause.Locking{Strength: "KEY SHARE"}).
			Where("id = ?", id).Pluck("id", &got).Error; err != nil {
			return err
		}
		if len(got) != 1 {
			return bizerr.ErrValidationFailed
		}
	}
	return nil
}

func assertFlowsExist(ctx context.Context, tx *gorm.DB, ids []string) error {
	for _, id := range ids {
		var got []string
		if err := tx.WithContext(ctx).Model(&FlowModel{}).
			Clauses(clause.Locking{Strength: "KEY SHARE"}).
			Where("id = ?", id).Pluck("id", &got).Error; err != nil {
			return err
		}
		if len(got) != 1 {
			return bizerr.ErrValidationFailed
		}
	}
	return nil
}

func (s *Service) auditGroup(ctx context.Context, actor AdminActor, eventType, action, groupID string, meta RequestMeta) {
	_ = s.audit.Audit(ctx, nil, auditlog.AuditInput{
		EventType: eventType, ActorKind: "user", ActorID: &actor.UserID, ActorUsername: &actor.Username,
		ResourceType: "permission_group", ResourceID: &groupID, Action: action, Outcome: "succeeded",
		RequestID: strPtr(meta.RequestID),
	})
}
