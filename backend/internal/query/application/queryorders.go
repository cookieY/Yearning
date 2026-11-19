// queryorders.go implements the combined query orders view (RCP-20261021): merges access_requests and
// query_sessions into a unified (created_at, id) descending keyset-paginated stream -- replacing
// client-side dual-source merging and pagination (per owner ruling: every table except the query result grid uses backend pagination).
// Relationship scoping matches the individual list endpoints: requests = requester or frozen approver; sessions = owner.
package application

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/query/domain"
	"github.com/cookieY/YearningX/backend/internal/query/persistence"
)

// QueryOrderEntry is a combined entry carrying either a request or a session according to kind.
type QueryOrderEntry struct {
	Kind      string             `json:"kind"` // access_request | session
	ID        string             `json:"id"`
	CreatedAt string             `json:"created_at"`
	Request   *AccessRequestView `json:"request"`
	Session   *SessionView       `json:"session"`
}

const queryOrdersKeysetBase = `select id, created_at, kind from (select id::text as id, created_at, 'access_request' as kind from query_access_requests
		where requester_user_id_snapshot = ?
		or exists (select 1 from query_access_approval_actors a
			join query_access_approval_steps st on st.id = a.step_id
			where st.request_id = query_access_requests.id
			  and a.reviewer_user_id_snapshot = ?)
	union all
	select id::text as id, created_at, 'session' as kind from query_sessions
		where user_id_snapshot = ?) as query_orders`

// ListQueryOrders performs combined keyset pagination where (created_at, id) < cursor; the cursor format
// matches other business lists as "RFC3339Nano|uuid" (both tables use UUIDv4 primary keys with negligible
// cross-table collision probability, so (created_at, id) provides a deterministic total order).
func (s *Services) ListQueryOrders(ctx context.Context, actor Actor, limit int, after string) ([]QueryOrderEntry, *string, error) {
	type keyRow struct {
		ID        string
		CreatedAt time.Time
		Kind      string
	}
	sql := queryOrdersKeysetBase
	args := []interface{}{actor.UserID, actor.UserID, actor.UserID}
	if after != "" {
		sep := strings.LastIndex(after, "|")
		if sep <= 0 {
			return nil, nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
		}
		cursorTime, err := time.Parse(time.RFC3339Nano, after[:sep])
		if err != nil {
			return nil, nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
		}
		if _, err := uuid.Parse(after[sep+1:]); err != nil {
			return nil, nil, &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
		}
		sql += ` where (created_at, id) < (?, ?)`
		args = append(args, cursorTime, after[sep+1:])
	}
	sql += ` order by created_at desc, id desc limit ?`
	args = append(args, limit+1)
	var keys []keyRow
	if err := s.DB.WithContext(ctx).Raw(sql, args...).Scan(&keys).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(keys) > limit
	if hasMore {
		keys = keys[:limit]
	}
	// Batch-load full views bucketed by source kind.
	var reqIDs, sessIDs []string
	for _, k := range keys {
		if k.Kind == "access_request" {
			reqIDs = append(reqIDs, k.ID)
		} else {
			sessIDs = append(sessIDs, k.ID)
		}
	}
	reqViews := map[string]*AccessRequestView{}
	if len(reqIDs) > 0 {
		var rows []persistence.QueryAccessRequestModel
		if err := s.DB.WithContext(ctx).
			Where("id in ?", reqIDs).Find(&rows).Error; err != nil {
			return nil, nil, err
		}
		for i := range rows {
			v, verr := s.requestView(ctx, &rows[i])
			if verr != nil {
				return nil, nil, verr
			}
			reqViews[rows[i].ID] = v
		}
	}
	sessViews := map[string]*SessionView{}
	if len(sessIDs) > 0 {
		var rows []persistence.QuerySessionModel
		if err := s.DB.WithContext(ctx).
			Where("id in ?", sessIDs).Find(&rows).Error; err != nil {
			return nil, nil, err
		}
		for i := range rows {
			sessViews[rows[i].ID] = s.sessionViewLocked(ctx, s.DB, &rows[i])
		}
	}
	out := make([]QueryOrderEntry, 0, len(keys))
	for _, k := range keys {
		entry := QueryOrderEntry{
			Kind:      k.Kind,
			ID:        k.ID,
			CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if k.Kind == "access_request" {
			entry.Request = reqViews[k.ID]
		} else {
			entry.Session = sessViews[k.ID]
		}
		out = append(out, entry)
	}
	if !hasMore || len(out) == 0 {
		return out, nil, nil
	}
	last := out[len(out)-1]
	next := last.CreatedAt + "|" + last.ID
	return out, &next, nil
}
