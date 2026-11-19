package application

import (
	"context"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/review/display"
)

// orderIdentifierDisplay lives for one authorized request only. Frozen result
// records stay anonymous; no mapper or decrypted text is cached across users.
type orderIdentifierDisplay struct {
	service   *Services
	actor     Actor
	meta      RequestMeta
	renderers map[string]*display.Renderer
	counts    map[string]int
}

func newOrderIdentifierDisplay(s *Services, actor Actor, meta []RequestMeta) *orderIdentifierDisplay {
	request := RequestMeta{}
	if len(meta) > 0 {
		request = meta[0]
	}
	return &orderIdentifierDisplay{service: s, actor: actor, meta: request, renderers: map[string]*display.Renderer{}, counts: map[string]int{}}
}

func (d *orderIdentifierDisplay) restore(ctx context.Context, runID string, finding *FindingView) (*FindingView, error) {
	renderer, ok := d.renderers[runID]
	if !ok {
		loaded, err := display.Load(ctx, d.service.DB, d.service.Crypter, runID)
		if err != nil {
			return nil, err
		}
		renderer = loaded
		d.renderers[runID] = loaded
	}
	title, n1 := renderer.RestoreText(finding.Title)
	message, n2 := renderer.RestoreText(finding.Message)
	suggestion, n3 := renderer.RestorePointer(finding.Suggestion)
	out := *finding
	out.Title, out.Message, out.Suggestion = title, message, suggestion
	d.counts[runID] += n1 + n2 + n3
	return &out, nil
}

func (d *orderIdentifierDisplay) audit(ctx context.Context) error {
	// Use one transaction: no partial audit batch if a later Run write fails.
	return d.service.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for runID, count := range d.counts {
			if count == 0 {
				continue
			}
			meta := d.meta
			if err := d.service.Recorder.Audit(ctx, tx, auditlog.AuditInput{
				EventType: "review.identifier_displayed", ActorKind: "user", ActorID: strPtrOrNil(d.actor.UserID), ActorUsername: strPtrOrNil(d.actor.Username),
				ResourceType: "review_run", ResourceID: strPtrOrNil(runID), Action: "read", Outcome: "succeeded",
				RequestID: strPtrOrNil(meta.RequestID), SourceIP: strPtrOrNil(meta.SourceIP), UserAgent: strPtrOrNil(meta.UserAgent),
				Metadata: map[string]any{"surface": "order_findings", "restored_count": count},
			}); err != nil {
				return err
			}
		}
		return nil
	})
}
