// comments.go Work order comments: append_only writes and cursor pagination reads (permanently retained per S004).
// Write authorization = change-order-relevant-actor (submitter / frozen approver / executor).
package application

import (
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/changeorder/persistence"
)

// CommentView maps to the ChangeOrderComment schema.
type CommentView struct {
	ID                string `json:"id"`
	OrderID           string `json:"order_id"`
	AuthorUserID      string `json:"author_user_id"`
	AuthorDisplayName string `json:"author_display_name"`
	Content           string `json:"content"`
	OccurredAt        string `json:"occurred_at"`
}

// ListComments reads comments with cursor pagination (with relation check).
func (s *Services) ListComments(ctx context.Context, actor Actor, orderID string, limit int, after string) ([]*CommentView, *string, error) {
	ok, err := s.CanRead(ctx, s.DB, orderID, actor.UserID)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, domain.ErrNotFound()
	}
	q := `
		select id::text as id, order_id::text as order_id,
		       author_user_id_snapshot::text as author_user_id,
		       author_username_snapshot, content, occurred_at
		from order_comments where order_id = ?`
	args := []any{orderID}
	if after != "" {
		if ts, id, ok := splitCursor(after); ok {
			q += ` and (occurred_at, id) < (?::timestamptz, ?::uuid)`
			args = append(args, ts, id)
		}
	}
	q += ` order by occurred_at desc, id desc limit ?`
	args = append(args, limit+1)
	var rows []struct {
		ID             string
		OrderID        string
		AuthorUserID   string
		AuthorUsername string
		Content        string
		OccurredAt     time.Time
	}
	if err := s.DB.WithContext(ctx).Raw(q, args...).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	nameByUser := map[string]string{}
	userIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		if _, ok := nameByUser[r.AuthorUserID]; !ok {
			userIDs = append(userIDs, r.AuthorUserID)
		}
	}
	if len(userIDs) > 0 {
		var nameRows []struct {
			ID          string
			UserName    string
			DisplayName string
		}
		if err := s.DB.WithContext(ctx).Raw(
			`select id::text as id, username, display_name from users where id in ?`, userIDs).
			Scan(&nameRows).Error; err != nil {
			return nil, nil, err
		}
		for _, r := range nameRows {
			if r.DisplayName != "" {
				nameByUser[r.ID] = r.DisplayName
			} else {
				nameByUser[r.ID] = r.UserName
			}
		}
	}
	out := make([]*CommentView, 0, len(rows))
	for _, r := range rows {
		out = append(out, &CommentView{
			ID: r.ID, OrderID: r.OrderID, AuthorUserID: r.AuthorUserID,
			AuthorDisplayName: nameByUser[r.AuthorUserID],
			Content:           r.Content,
			OccurredAt:        r.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999Z"),
		})
	}
	var next *string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		cur := last.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999Z") + "|" + last.ID
		next = &cur
	}
	return out, next, nil
}

// CreateComment appends a comment (idempotent; author identity is frozen from the session snapshot).
func (s *Services) CreateComment(ctx context.Context, actor Actor, meta RequestMeta,
	orderID, content, idempotencyKey string) (view *CommentView, replayed bool, err error) {
	if content == "" || utf8.RuneCountInString(content) > 4096 {
		return nil, false, domain.ErrValidation()
	}
	const route = "POST /change-orders/{order_id}/comments"
	reqHash := idemRequestHash(actor.UserID, orderID, content)
	claimed, replay, err := s.idemClaim(ctx, actor.UserID, route, orderID, idempotencyKey, reqHash)
	if err != nil {
		return nil, false, err
	}
	if claimed == nil {
		var v CommentView
		if err := json.Unmarshal(replay, &v); err != nil {
			return nil, false, err
		}
		return &v, true, nil
	}
	ok, err := s.CanRead(ctx, s.DB, orderID, actor.UserID)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		_ = s.idemRelease(ctx, actor.UserID, route, orderID, idempotencyKey)
		return nil, false, domain.ErrNotFound()
	}
	var author struct {
		Username    string
		DisplayName string
	}
	if err := s.DB.WithContext(ctx).Raw(
		`select username, display_name from users where id = ?`, actor.UserID).Scan(&author).Error; err != nil {
		_ = s.idemRelease(ctx, actor.UserID, route, orderID, idempotencyKey)
		return nil, false, err
	}
	now := nowUTC()
	row := &persistence.OrderCommentModel{
		ID: newID(), OrderID: orderID, AuthorUserIDSnapshot: actor.UserID,
		AuthorUsernameSnapshot: author.Username, Content: content, OccurredAt: now,
	}
	if err := s.DB.WithContext(ctx).Create(row).Error; err != nil {
		_ = s.idemRelease(ctx, actor.UserID, route, orderID, idempotencyKey)
		return nil, false, err
	}
	display := author.DisplayName
	if display == "" {
		display = author.Username
	}
	view = &CommentView{
		ID: row.ID, OrderID: orderID, AuthorUserID: actor.UserID,
		AuthorDisplayName: display, Content: content,
		OccurredAt: now.UTC().Format("2006-01-02T15:04:05.999999Z"),
	}
	raw, err := json.Marshal(view)
	if err == nil {
		err = s.idemComplete(ctx, actor.UserID, route, orderID, idempotencyKey, raw)
	}
	if err != nil {
		return nil, false, err
	}
	return view, false, nil
}
