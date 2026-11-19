// taskhandler.go Adapts FakeRunner to tasks.Handler and wires the worker.
// Worker interruption recovery semantics: running tasks whose lease has expired can be re-claimed;
// all FakeRunner steps are idempotent (stage watermarks + terminal state checks), so replays never
// produce duplicate statements, groups, findings, or events.
package application

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/tasks"
)

// TaskRunner is the two-stage task chain executor contract (shared by FakeRunner and AgentRunner).
type TaskRunner interface {
	Execute(ctx context.Context, db *gorm.DB, in TaskInput,
		fenceGuard func() error, beat func(completed, total int64)) error
}

// FakeRunnerHandler returns a tasks.Handler for sql_grouping/ai_review tasks.
func (s *Services) FakeRunnerHandler(runner TaskRunner) tasks.Handler {
	return func(ctx context.Context, db *gorm.DB, task *tasks.ClaimedTask,
		fenceGuard func() error, beat func(completed, total int64)) error {
		var input TaskInput
		if err := json.Unmarshal(task.Input, &input); err != nil || input.ReviewRunID == "" {
			return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
		}
		if task.AggregateID != "" && task.AggregateID != input.ReviewRunID {
			return &domain.Error{Code: 1001, Name: "VALIDATION_FAILED"}
		}
		return runner.Execute(ctx, db, input, fenceGuard, beat)
	}
}

// NewReviewWorker constructs a worker with FakeRunner registered (in-process singleton for B6 serve).
func (s *Services) NewReviewWorker(owner string, runner *FakeRunner, lease time.Duration) *tasks.Worker {
	w := tasks.NewWorker(s.DB, owner, []string{tasks.KindAIReview, tasks.KindSQLGrouping}, lease, time.Second)
	handler := s.FakeRunnerHandler(runner)
	w.Register(tasks.KindAIReview, handler)
	w.Register(tasks.KindSQLGrouping, handler)
	return w
}

// NewAgentReviewWorker constructs an AgentRunner-first worker (B7 serve path).
// When no Provider is available, AgentRunner fails closed per contract (2011) — the production path
// never falls back to Fake heuristics; tests requiring a deterministic engine wire a FakeRunner worker directly.
func (s *Services) NewAgentReviewWorker(owner string, deps AgentDeps, lease time.Duration) (*tasks.Worker, *AgentRunner) {
	agent := NewAgentRunner(s, deps)
	w := tasks.NewWorker(s.DB, owner, []string{tasks.KindAIReview, tasks.KindSQLGrouping}, lease, time.Second)
	handler := s.FakeRunnerHandler(agent)
	w.Register(tasks.KindAIReview, handler)
	w.Register(tasks.KindSQLGrouping, handler)
	return w, agent
}
