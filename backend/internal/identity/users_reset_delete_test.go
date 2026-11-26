package identity

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
)

type userAdminFx struct {
	notFound      bool
	isBuiltin     bool
	version       int64
	externalCount int64
	draftIDs      []string
	users         []UserModel
	extProviders  map[string]string
	execQueries   []string
	txBegins      int
	txCommits     int
}

func (f *userAdminFx) Connect(context.Context) (driver.Conn, error) { return f, nil }
func (f *userAdminFx) Driver() driver.Driver                        { return userAdminDriver{} }

type userAdminDriver struct{}

func (userAdminDriver) Open(string) (driver.Conn, error)   { return nil, errors.New("unused") }
func (f *userAdminFx) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (f *userAdminFx) Close() error                        { return nil }
func (f *userAdminFx) Begin() (driver.Tx, error) {
	return f.BeginTx(context.Background(), driver.TxOptions{})
}
func (f *userAdminFx) BeginTx(_ context.Context, _ driver.TxOptions) (driver.Tx, error) {
	f.txBegins++
	return &userAdminTx{fx: f}, nil
}

type userAdminTx struct{ fx *userAdminFx }

func (tx *userAdminTx) Commit() error {
	tx.fx.txCommits++
	return nil
}
func (*userAdminTx) Rollback() error { return nil }

func (f *userAdminFx) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	f.execQueries = append(f.execQueries, query)
	return driver.RowsAffected(1), nil
}

func (f *userAdminFx) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	q := strings.ToLower(query)
	now := time.Now().UTC()
	switch {
	case strings.Contains(q, "select distinct on (user_id)"):
		var data [][]driver.Value
		for uid, pk := range f.extProviders {
			data = append(data, []driver.Value{uid, pk})
		}
		return &userAdminRows{cols: []string{"user_id", "provider_kind"}, data: data}, nil
	case strings.Contains(q, `from "external_identities"`):
		return &userAdminRows{cols: []string{"count"}, data: [][]driver.Value{{f.externalCount}}}, nil
	case strings.Contains(q, "from change_drafts d"):
		var data [][]driver.Value
		for _, id := range f.draftIDs {
			data = append(data, []driver.Value{id})
		}
		return &userAdminRows{cols: []string{"id"}, data: data}, nil
	case strings.Contains(q, "from change_orders"):
		return &userAdminRows{cols: []string{"count"}, data: [][]driver.Value{{int64(0)}}}, nil
	case strings.Contains(q, "from flow_approval_step_actors"), strings.Contains(q, "from flow_execution_actors"):
		return &userAdminRows{cols: []string{"step_id"}, data: nil}, nil
	case strings.Contains(q, `from "users"`):
		if len(f.users) > 0 && !strings.Contains(q, "limit $") {
			// Single-user lookup fallback uses first user unless notFound.
			u := f.users[0]
			ver := u.Version
			if ver == 0 {
				ver = 1
			}
			return &userAdminRows{
				cols: []string{"id", "username", "password_hash", "display_name", "email", "is_builtin_admin", "role", "version", "created_at", "updated_at"},
				data: [][]driver.Value{{u.ID, u.Username, nil, u.DisplayName, u.Email, u.IsBuiltinAdmin, u.Role, ver, now, now}},
			}, nil
		}
		if len(f.users) > 0 {
			var data [][]driver.Value
			for _, u := range f.users {
				ver := u.Version
				if ver == 0 {
					ver = 1
				}
				data = append(data, []driver.Value{u.ID, u.Username, nil, u.DisplayName, u.Email, u.IsBuiltinAdmin, u.Role, ver, now, now})
			}
			return &userAdminRows{
				cols: []string{"id", "username", "password_hash", "display_name", "email", "is_builtin_admin", "role", "version", "created_at", "updated_at"},
				data: data,
			}, nil
		}
		if f.notFound {
			return &userAdminRows{cols: []string{"id"}}, nil
		}
		ver := f.version
		if ver == 0 {
			ver = 1
		}
		return &userAdminRows{
			cols: []string{"id", "username", "password_hash", "display_name", "email", "is_builtin_admin", "role", "version", "created_at", "updated_at"},
			data: [][]driver.Value{{"0198da00-0000-7000-8000-000000000010", "alice", nil, "Alice", nil, f.isBuiltin, "user", ver, now, now}},
		}, nil
	default:
		return &userAdminRows{cols: []string{}}, nil
	}
}

type userAdminRows struct {
	cols []string
	data [][]driver.Value
	idx  int
}

func (r *userAdminRows) Columns() []string { return r.cols }
func (r *userAdminRows) Close() error      { return nil }
func (r *userAdminRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.idx])
	r.idx++
	return nil
}

func newUserAdminTestService(t *testing.T, fx *userAdminFx) *Service {
	t.Helper()
	sqlDB := sql.OpenDB(fx)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := NewSessionStore(db, DefaultSessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := auditlog.NewRecorder(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(db, sessions, nil, recorder)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestResetUserPasswordValidationAndGuards(t *testing.T) {
	ctx := context.Background()
	actor := ActorRef{UserID: "0198da00-0000-7000-8000-000000000001", Username: "admin"}
	targetID := "0198da00-0000-7000-8000-000000000010"

	t.Run("rejects_bad_uuid_and_short_password", func(t *testing.T) {
		fx := &userAdminFx{}
		svc := newUserAdminTestService(t, fx)
		if err := svc.ResetUserPassword(ctx, actor, "not-a-uuid", "valid-password-123", RequestMeta{}); !errors.Is(err, bizerr.ErrValidationFailed) {
			t.Fatalf("bad uuid err = %v, want ErrValidationFailed", err)
		}
		if err := svc.ResetUserPassword(ctx, actor, targetID, "short", RequestMeta{}); !errors.Is(err, bizerr.ErrValidationFailed) {
			t.Fatalf("short password err = %v, want ErrValidationFailed", err)
		}
	})

	t.Run("rejects_missing_builtin_and_external", func(t *testing.T) {
		fxNotFound := &userAdminFx{notFound: true}
		if err := newUserAdminTestService(t, fxNotFound).ResetUserPassword(ctx, actor, targetID, "valid-password-123", RequestMeta{}); !errors.Is(err, bizerr.ErrNotFound) {
			t.Fatalf("not found err = %v, want ErrNotFound", err)
		}

		fxBuiltin := &userAdminFx{isBuiltin: true}
		var imm bizerr.AdminImmutableError
		if err := newUserAdminTestService(t, fxBuiltin).ResetUserPassword(ctx, actor, targetID, "valid-password-123", RequestMeta{}); !errors.As(err, &imm) {
			t.Fatalf("builtin admin err = %v, want AdminImmutableError", err)
		}
		if fxBuiltin.txBegins != 0 {
			t.Fatalf("builtin admin reject must happen before tx, got %d txs", fxBuiltin.txBegins)
		}

		fxExt := &userAdminFx{externalCount: 1}
		if err := newUserAdminTestService(t, fxExt).ResetUserPassword(ctx, actor, targetID, "valid-password-123", RequestMeta{}); !errors.Is(err, bizerr.ErrPasswordManagedByIdentitySource) {
			t.Fatalf("external user err = %v, want ErrPasswordManagedByIdentitySource", err)
		}
		if fxExt.txBegins != 0 {
			t.Fatalf("external user reject must happen before tx, got %d txs", fxExt.txBegins)
		}
	})

	t.Run("argon2_busy_avoids_tx_lock", func(t *testing.T) {
		fx := &userAdminFx{}
		svc := newUserAdminTestService(t, fx)
		defer SetArgon2LimiterForTest(1, 20*time.Millisecond)()
		release, err := acquireArgon2Slot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		errBusy := svc.ResetUserPassword(ctx, actor, targetID, "valid-password-123", RequestMeta{})
		release()
		if !errors.Is(errBusy, ErrPasswordHashBusy) {
			t.Fatalf("busy err = %v, want ErrPasswordHashBusy", errBusy)
		}
		if fx.txBegins != 0 {
			t.Fatalf("expected 0 DB transactions while Argon2 busy, got %d", fx.txBegins)
		}
	})

	t.Run("local_user_reset_succeeds_and_revokes_sessions", func(t *testing.T) {
		fx := &userAdminFx{}
		svc := newUserAdminTestService(t, fx)
		if err := svc.ResetUserPassword(ctx, actor, targetID, "new-strong-password-123", RequestMeta{RequestID: "req-1"}); err != nil {
			t.Fatalf("ResetUserPassword failed: %v", err)
		}
		if fx.txBegins != 1 {
			t.Fatalf("expected 1 DB transaction, got %d", fx.txBegins)
		}
		joined := strings.ToLower(strings.Join(fx.execQueries, "\n"))
		if !strings.Contains(joined, `update "users"`) || !strings.Contains(joined, `password_hash`) || !strings.Contains(joined, `auth_sessions`) {
			t.Fatalf("expected users update and auth_sessions revoke, got:\n%s", joined)
		}
	})
}

func TestListUsersBatchesAuthProvider(t *testing.T) {
	uLocal := "0198da00-0000-7000-8000-000000000010"
	uLdap := "0198da00-0000-7000-8000-000000000011"
	uOidc := "0198da00-0000-7000-8000-000000000012"
	fx := &userAdminFx{
		users: []UserModel{
			{ID: uLocal, Username: "alice", DisplayName: "Alice", Role: "user", Version: 1},
			{ID: uLdap, Username: "bob", DisplayName: "Bob", Role: "user", Version: 1},
			{ID: uOidc, Username: "carol", DisplayName: "Carol", Role: "user", Version: 1},
		},
		extProviders: map[string]string{
			uLdap: "ldap",
			uOidc: "oidc",
		},
	}
	svc := newUserAdminTestService(t, fx)
	views, _, err := svc.ListUsers(context.Background(), 50, "", "", "")
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}
	if len(views) != 3 {
		t.Fatalf("expected 3 users, got %d", len(views))
	}
	got := map[string]string{}
	for _, v := range views {
		got[v.ID] = v.AuthProvider
	}
	if got[uLocal] != "local" || got[uLdap] != "ldap" || got[uOidc] != "oidc" {
		t.Fatalf("unexpected auth_provider map: %+v", got)
	}
}

func TestDeleteUserCleansPrivateDraftsAssistantAndInvokesHookPostCommit(t *testing.T) {
	userID := "0198da00-0000-7000-8000-000000000010"
	draftID := "0198da00-0000-7000-8000-000000000099"
	fx := &userAdminFx{
		version:  2,
		draftIDs: []string{draftID},
	}
	svc := newUserAdminTestService(t, fx)

	hookCalls := 0
	var hookCommitsObserved int
	var hookUserID string
	svc.SetOnUserDeleted(func(uid string) {
		hookCalls++
		hookUserID = uid
		hookCommitsObserved = fx.txCommits
	})

	actor := ActorRef{UserID: "0198da00-0000-7000-8000-000000000001", Username: "admin"}
	if err := svc.DeleteUser(context.Background(), actor, userID, 2, RequestMeta{RequestID: "req-del"}); err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}

	if hookCalls != 1 || hookUserID != userID || hookCommitsObserved != 1 {
		t.Fatalf("hook invariant failed: calls=%d uid=%q commits=%d", hookCalls, hookUserID, hookCommitsObserved)
	}

	joined := strings.ToLower(strings.Join(fx.execQueries, "\n"))
	for _, want := range []string{
		"update async_tasks",
		"delete from review_identifier_mappings",
		"delete from stage_review_snapshots",
		"delete from review_runs",
		"delete from draft_sql_payloads",
		"delete from change_drafts",
		"delete from ai_assistant_context_summaries",
		"delete from ai_assistant_sql_drafts",
		"delete from ai_assistant_messages",
		"delete from ai_assistant_sessions",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("DeleteUser missing expected cleanup SQL %q; got:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{
		"delete from change_orders",
		"delete from sql_statements",
		"delete from review_findings",
		"delete from evidence_facts",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("DeleteUser must not execute %q (P105 / insert-only grants)", forbidden)
		}
	}
}
