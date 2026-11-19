// timeline.go Work order timeline: reads domain_events by subject (ordered by sequence)
// and maps them to ChangeOrderTimelineEntry. Read-only, zero Provider calls.
package application

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
)

// TimelineEntryView maps to the ChangeOrderTimelineEntry schema.
type TimelineEntryView struct {
	ID            string  `json:"id"`
	EventType     string  `json:"event_type"`
	OccurredAt    string  `json:"occurred_at"`
	ActorKind     string  `json:"actor_kind"`
	ActorUserID   *string `json:"actor_user_id"`
	ActorName     *string `json:"actor_display_name"`
	Summary       string  `json:"summary"`
	Comment       *string `json:"comment,omitempty"`
	StagePosition *int32  `json:"stage_position"`
	State         *string `json:"state"`
}

// timelineSummary maps internal events to human-readable summaries (no SQL or sensitive content).
func timelineSummary(internalType string, data map[string]any) string {
	switch internalType {
	case EventOrderSubmitted:
		return "Change order submitted and workflow snapshot frozen"
	case EventApprovalDecided:
		if d, _ := data["decision"].(string); d == "reject" {
			return "Approval rejected; change order terminated"
		}
		return "Approval granted"
	case EventStageReadyForExec:
		return "Final approval granted; awaiting frozen executor"
	// I18N-001: execution domain events previously fell back to raw keys; provide human-readable summaries.
	case "execution.scheduled":
		return "Scheduled timed execution (no automatic catch-up if missed)"
	case "execution.schedule_cancelled":
		return "Scheduled execution cancelled; returned to pending execution"
	case "execution.started":
		return "Stage execution started"
	case "execution.succeeded":
		return "Stage execution succeeded"
	case "execution.failed":
		return "Stage execution failed"
	case "execution.partial_failed":
		return "Stage execution partially failed (completed DDL statements retained)"
	case "execution.cancelled":
		return "Stage execution cancelled"
	case "execution.result_unknown":
		return "Execution result unknown; manual verification required"
	case EventOrderRejected:
		return "Change order rejected"
	case EventOrderWithdrawn:
		return "Change order withdrawn by submitter"
	case EventOrderVoided:
		return "Change order voided by submitter"
	case EventNodeInvalidated:
		return "Frozen approval or execution node has no active candidates; marked invalid"
	}
	return internalType
}

// ListTimeline reads the work order timeline (relation check; cursor=sequence).
func (s *Services) ListTimeline(ctx context.Context, actor Actor, orderID string, limit int, after string) ([]*TimelineEntryView, *string, error) {
	ok, err := s.CanRead(ctx, s.DB, orderID, actor.UserID)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, domain.ErrNotFound()
	}
	q := `
		select id::text as id, event_type, occurred_at, actor, event_data, sequence
		from domain_events where subject = @subject`
	args := map[string]any{"subject": orderSubject(orderID)}
	if after != "" {
		q += ` and sequence < @seq`
		args["seq"] = after
	}
	q += ` order by sequence desc limit @lim`
	args["lim"] = limit + 1
	var rows []struct {
		ID         string
		EventType  string
		OccurredAt time.Time
		Actor      []byte
		EventData  []byte
		Sequence   int64
	}
	if err := s.DB.WithContext(ctx).Raw(q, args).Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	// Parse event payloads once uniformly; approval_decided collects step_id keys for decision row lookup.
	datas := make([]map[string]any, len(rows))
	stepIDs := make([]string, 0)
	for i, r := range rows {
		data := map[string]any{}
		_ = json.Unmarshal(r.EventData, &data)
		datas[i] = data
		if r.EventType == EventApprovalDecided {
			if sid, _ := data["step_id"].(string); sid != "" {
				stepIDs = append(stepIDs, sid)
			}
		}
	}
	// Approval comment projection (NEW-FE-01: after SEC-0036, comment is only written to approval_decisions
	// rows and excluded from event payloads — batch-query decision rows by step_id to retrieve comments;
	// read surface reuses the CanRead gate at ListTimeline entry without adding new exposure surfaces).
	commentsByStep := map[string]string{}
	if len(stepIDs) > 0 {
		var crows []struct {
			StepID  string
			Comment string
		}
		if err := s.DB.WithContext(ctx).Raw(
			`select step_id::text as step_id, comment from approval_decisions where step_id in ?`,
			stepIDs).Scan(&crows).Error; err != nil {
			return nil, nil, err
		}
		for _, cr := range crows {
			commentsByStep[cr.StepID] = cr.Comment
		}
	}
	out := make([]*TimelineEntryView, 0, len(rows))
	for i, r := range rows {
		data := datas[i]
		entry := &TimelineEntryView{
			ID: r.ID, EventType: r.EventType,
			OccurredAt: r.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999Z"),
			ActorKind:  "system",
		}
		var act auditlog.Actor
		if json.Unmarshal(r.Actor, &act) == nil && act.Kind != "" {
			entry.ActorKind = act.Kind
			if act.UserID != "" {
				u := act.UserID
				entry.ActorUserID = &u
				// Frozen display name: event actor only carries id/username; look up current display name
				// for presentation (timeline is not historical identity evidence; approval decision rows hold username snapshots).
				var name string
				_ = s.DB.WithContext(ctx).Raw(
					`select display_name from users where id = ?`, u).Scan(&name).Error
				if name != "" {
					entry.ActorName = &name
				}
			}
		}
		entry.Summary = timelineSummary(r.EventType, data)
		// Expose approval comments along with the timeline (BUG-004 read surface): decision row comment
		// is the authoritative source; falls back to event payload comment for historical events written before SEC-0036.
		comment := ""
		if r.EventType == EventApprovalDecided {
			if sid, _ := data["step_id"].(string); sid != "" {
				comment = commentsByStep[sid]
			}
		}
		if comment == "" {
			if c, ok := data["comment"].(string); ok {
				comment = c
			}
		}
		if comment != "" {
			entry.Comment = &comment
		}
		if sp, ok := data["stage_id"].(string); ok && sp != "" {
			// Stage position is resolved from the current frozen table (stage_id is stable).
			var pos int32
			if err := s.DB.WithContext(ctx).Raw(
				`select position from order_stages where id = ?`, sp).Scan(&pos).Error; err == nil && pos > 0 {
				entry.StagePosition = &pos
			}
		}
		if to, ok := data["to"].(string); ok && to != "" {
			entry.State = &to
		}
		out = append(out, entry)
	}
	var next *string
	if hasMore && len(rows) > 0 {
		cur := rows[len(rows)-1].Sequence
		// Cursor uses the numeric sequence string (splitCursor does not apply; passed through directly by handler).
		s := timeCursor(cur)
		next = &s
	}
	return out, next, nil
}

func timeCursor(seq int64) string {
	// Simple decimal string (timeline cursor semantics are independent of "ts|id" cursors).
	return fmtInt(seq)
}

func fmtInt(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [24]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
