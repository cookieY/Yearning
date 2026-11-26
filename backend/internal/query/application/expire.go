// expire.go advances lazy lifecycles in the query domain (invoked on Scheduler ticks):
// expired Grants -> expired with cascading Session expiration; deleted users -> revoked(user_deleted)
// + Session user_deleted (the delete_user command does not write to query tables, relying on this lazy terminal path).
package application

import (
	"context"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
)

var systemAuditActor = auditlog.SystemActor("00000000-0000-0000-0000-000000000000", "system")

// ExpireGrants advances expired or invalidated Grants and their cascading Sessions (idempotent and batch-bounded).
func (s *Services) ExpireGrants(ctx context.Context) error {
	now := s.now()
	// 1. Grant expiration: active Grants with expires_at <= now transition to expired.
	var expiredGrants []string
	if err := s.DB.WithContext(ctx).Raw(`
		update query_grants set state = 'expired', version = version + 1,
		    terminal_at = ?, terminal_reason = 'expired'
		where state = 'active' and expires_at <= ?
		returning id::text`, now, now).Scan(&expiredGrants).Error; err != nil {
		return err
	}
	for _, gid := range expiredGrants {
		if err := s.terminateGrantSessions(ctx, gid, "expired", "grant_expired"); err != nil {
			return err
		}
		_ = s.Recorder.NotifyRealtime(ctx, nil, "query_grant.expired", "query-grants/"+gid, "expired", systemAuditActor)
	}
	// 2. Deleted users: transition Grant to revoked(user_deleted) under the system actor (0000...00);
	// delete_user does not write to query tables, so this lazy path makes the terminal state visible.
	var userGone []string
	if err := s.DB.WithContext(ctx).Raw(`
		update query_grants g set state = 'revoked', version = version + 1,
		    revoked_by_user_id_snapshot = '00000000-0000-0000-0000-000000000000'::uuid,
		    revoked_by_username_snapshot = 'system',
		    revoke_reason = 'user_deleted', revoked_at = ?,
		    terminal_at = ?, terminal_reason = 'user_deleted'
		where g.state = 'active' and not exists (
		    select 1 from users u where u.id = g.requester_user_id_snapshot)
		returning g.id::text`, now, now).Scan(&userGone).Error; err != nil {
		return err
	}
	for _, gid := range userGone {
		if err := s.terminateGrantSessions(ctx, gid, "user_deleted", "user_deleted"); err != nil {
			return err
		}
		_ = s.Recorder.NotifyRealtime(ctx, nil, "query_grant.revoked", "query-grants/"+gid, "revoked", systemAuditActor)
	}
	// 2b. SEC-0007: loss of permission group membership or flow grant (member removed, group deleted/disabled,
	// or flow grant revoked) -> transition Grant to revoked(membership_revoked) and cascade to active Sessions.
	// Advanced periodically by the Scheduler (within one tick), while ensureSessionUsable provides immediate
	// enforcement at session use time. Revokes under the system identity and records an audit event per Grant
	// (group changes themselves are audited separately in the workflow domain).
	var memberGone []string
	if err := s.DB.WithContext(ctx).Raw(`
		update query_grants g set state = 'revoked', version = version + 1,
		    revoked_by_user_id_snapshot = '00000000-0000-0000-0000-000000000000'::uuid,
		    revoked_by_username_snapshot = 'system',
		    revoke_reason = 'membership_revoked', revoked_at = ?,
		    terminal_at = ?, terminal_reason = 'membership_revoked'
		where g.state = 'active' and not exists (
		    select 1 from query_access_requests r
		    join permission_group_flow_grants fg on fg.flow_id = r.flow_id_snapshot
		    join permission_groups pg on pg.id = fg.permission_group_id and pg.enabled
		    join permission_group_members m on m.permission_group_id = pg.id
		           and m.user_id = g.requester_user_id_snapshot
		    where r.id = g.query_access_request_id)
		returning g.id::text`, now, now).Scan(&memberGone).Error; err != nil {
		return err
	}
	for _, gid := range memberGone {
		if err := s.terminateGrantSessions(ctx, gid, "membership_revoked", "membership_revoked"); err != nil {
			return err
		}
		if aerr := s.Recorder.Audit(ctx, nil, auditInput(Actor{UserID: "00000000-0000-0000-0000-000000000000", Username: "system"},
			"query_grant.revoked", "query_grants", gid, "revoke", "succeeded")); aerr != nil {
			return aerr
		}
		_ = s.Recorder.NotifyRealtime(ctx, nil, "query_grant.revoked", "query-grants/"+gid, "revoked", systemAuditActor)
	}
	// Approval-free (grantless) active Sessions also terminate when group membership is lost
	// (lazy terminal path for Q002 direct-access sessions).
	var directGone []string
	if err := s.DB.WithContext(ctx).Raw(`
		update query_sessions s set state = 'revoked', version = version + 1,
		    closed_at = ?, close_reason = 'membership_revoked'
		where s.state = 'active' and s.grant_id is null and not exists (
		    select 1 from permission_group_flow_grants fg
		    join permission_groups pg on pg.id = fg.permission_group_id and pg.enabled
		    join permission_group_members m on m.permission_group_id = pg.id
		           and m.user_id = s.user_id_snapshot
		    where fg.flow_id = s.flow_id_snapshot)
		returning s.id::text`, now).Scan(&directGone).Error; err != nil {
		return err
	}
	if len(directGone) > 0 {
		s.terminateSessionsCursors(ctx, directGone)
		for _, sid := range directGone {
			_ = s.Recorder.NotifyRealtime(ctx, nil, EventSessionClosed, "query-sessions/"+sid, "revoked", systemAuditActor)
		}
	}
	// 3. All approvers missing for the active step: transition access request to invalid
	// (state machine edge all_current_step_actors_missing; lazy advancement path).
	if err := s.DB.WithContext(ctx).Exec(`
		update query_access_approval_steps st set state = 'invalid'
		where st.state = 'active' and not exists (
			select 1 from query_access_approval_actors a
			join users u on u.id = a.reviewer_user_id_snapshot
			where a.step_id = st.id)`).Error; err != nil {
		return err
	}
	// Contract edge all_current_step_actors_missing invalidates the entire request: once the active step
	// is marked invalid, the whole request becomes terminal (leaving no multi-step request stuck in an undecidable state).
	if err := s.DB.WithContext(ctx).Exec(`
		update query_access_requests r set state = 'invalid', version = version + 1,
		    terminal_at = now()
		where r.state = 'access_pending' and exists (
			select 1 from query_access_approval_steps st2
			where st2.request_id = r.id and st2.state = 'invalid')`).Error; err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Exec(`
		update query_access_requests r set state = 'invalid', version = version + 1,
		    terminal_at = now()
		where r.state = 'access_pending' and not exists (
			select 1 from query_access_approval_steps st3
			where st3.request_id = r.id and st3.state in ('active', 'pending'))`).Error
}

// terminateGrantSessions cascades terminal state transitions to active Sessions under a Grant
// (selecting the state machine trigger by scenario and terminating in-process cursors).
func (s *Services) terminateGrantSessions(ctx context.Context, grantID, reason, sessionTrigger string) error {
	state := string(sessionTerminalFor(sessionTrigger))
	var sessions []string
	if err := s.DB.WithContext(ctx).Raw(`
		update query_sessions set state = ?, version = version + 1,
		    closed_at = ?, close_reason = ?
		where grant_id = ? and state = 'active'
		returning id::text`, state, s.now(), reason, grantID).Scan(&sessions).Error; err != nil {
		return err
	}
	if len(sessions) > 0 {
		s.terminateSessionsCursors(ctx, sessions)
		for _, sid := range sessions {
			_ = s.Recorder.NotifyRealtime(ctx, nil, EventSessionClosed, "query-sessions/"+sid, state, systemAuditActor)
		}
	}
	return nil
}

func sessionTerminalFor(trigger string) string {
	switch trigger {
	case "user_deleted":
		return "user_deleted"
	case "expired":
		return "expired"
	default:
		return "revoked"
	}
}
