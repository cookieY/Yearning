package application

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

type submitTargetDBFx struct {
	draftTargetDB *string
	execQueries   []string
	execArgs      [][]driver.NamedValue
}

func (f *submitTargetDBFx) Connect(context.Context) (driver.Conn, error) { return f, nil }
func (f *submitTargetDBFx) Driver() driver.Driver                        { return submitTargetDBDriver{} }

type submitTargetDBDriver struct{}

func (submitTargetDBDriver) Open(string) (driver.Conn, error)   { return nil, errors.New("unused") }
func (f *submitTargetDBFx) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (f *submitTargetDBFx) Close() error                        { return nil }
func (f *submitTargetDBFx) Begin() (driver.Tx, error)           { return &submitTargetDBTx{}, nil }

type submitTargetDBTx struct{}

func (*submitTargetDBTx) Commit() error   { return nil }
func (*submitTargetDBTx) Rollback() error { return nil }

func (f *submitTargetDBFx) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	f.execQueries = append(f.execQueries, query)
	f.execArgs = append(f.execArgs, args)
	return driver.RowsAffected(1), nil
}

func (f *submitTargetDBFx) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	q := strings.ToLower(query)
	switch {
	case strings.Contains(q, "from change_drafts where id ="):
		return &submitTargetDBRows{
			cols: []string{"id", "owner_user_id", "flow_id", "flow_version_seen", "ticket_type", "rollback_enabled", "target_database", "state", "version", "revision"},
			data: [][]driver.Value{{"draft-1", "u-1", "flow-1", int64(3), "normal", true, strOrNilVal(f.draftTargetDB), "draft", int64(2), int64(1)}},
		}, nil
	case strings.Contains(q, "select version from flows where id ="):
		return &submitTargetDBRows{cols: []string{"version"}, data: [][]driver.Value{{int64(3)}}}, nil
	default:
		return &submitTargetDBRows{cols: []string{}}, nil
	}
}

func strOrNilVal(p *string) driver.Value {
	if p == nil {
		return nil
	}
	return *p
}

type submitTargetDBRows struct {
	cols []string
	data [][]driver.Value
	idx  int
}

func (r *submitTargetDBRows) Columns() []string { return r.cols }
func (r *submitTargetDBRows) Close() error      { return nil }
func (r *submitTargetDBRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.idx])
	r.idx++
	return nil
}

func TestEnsureDraftForSubmitV2TargetDatabaseContract(t *testing.T) {
	ctx := context.Background()
	actor := Actor{UserID: "u-1", Username: "alice"}

	openDB := func(fx *submitTargetDBFx) *gorm.DB {
		sqlDB := sql.OpenDB(fx)
		t.Cleanup(func() { _ = sqlDB.Close() })
		db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
			Conn:                 sqlDB,
			PreferSimpleProtocol: true,
			WithoutReturning:     true,
		}), &gorm.Config{Logger: gormlogger.Discard, SkipDefaultTransaction: true})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}

	t.Run("rejects_target_database_mismatch_with_review_outdated", func(t *testing.T) {
		db := openDB(&submitTargetDBFx{})
		svc := &Services{DB: db}
		er := &reviewapp.EphemeralRun{FlowID: "flow-1", FlowVersion: 3, TargetDatabase: "db_a"}
		reqDB := "db_b"
		_, _, _, err := svc.ensureDraftForSubmitV2(ctx, db, actor, SubmitOrderV2Input{
			FlowID: "flow-1", DraftID: "draft-1", TargetDatabase: &reqDB,
		}, er)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.ErrReviewOutdated().Code {
			t.Fatalf("mismatch err = %v, want ErrReviewOutdated (2001)", err)
		}
	})

	t.Run("updates_draft_target_database_when_matching_ephemeral_run", func(t *testing.T) {
		oldDB := "db_a"
		fx := &submitTargetDBFx{draftTargetDB: &oldDB}
		db := openDB(fx)
		svc := &Services{DB: db}
		er := &reviewapp.EphemeralRun{FlowID: "flow-1", FlowVersion: 3, TargetDatabase: "db_b"}
		reqDB := "db_b"
		draftID, ver, rev, err := svc.ensureDraftForSubmitV2(ctx, db, actor, SubmitOrderV2Input{
			FlowID: "flow-1", DraftID: "draft-1", TargetDatabase: &reqDB,
		}, er)
		if err != nil || draftID != "draft-1" || ver != 2 || rev != 1 {
			t.Fatalf("got (%q, %d, %d, %v), want ('draft-1', 2, 1, nil)", draftID, ver, rev, err)
		}
		joined := strings.ToLower(strings.Join(fx.execQueries, "\n"))
		if !strings.Contains(joined, "update change_drafts set target_database =") {
			t.Fatalf("expected target_database update on draft, got:\n%s", joined)
		}
	})

	t.Run("direct_create_backfills_target_database", func(t *testing.T) {
		fx := &submitTargetDBFx{}
		db := openDB(fx)
		svc := &Services{DB: db}
		er := &reviewapp.EphemeralRun{FlowID: "flow-1", FlowVersion: 3, TargetDatabase: "shop_db"}
		title := "Add column"
		reqDB := "shop_db"
		draftID, ver, rev, err := svc.ensureDraftForSubmitV2(ctx, db, actor, SubmitOrderV2Input{
			FlowID: "flow-1", Title: &title, TargetDatabase: &reqDB,
		}, er)
		if err != nil || draftID == "" || ver != 1 || rev != 1 {
			t.Fatalf("direct create failed: (%q, %d, %d, %v)", draftID, ver, rev, err)
		}
		joined := strings.ToLower(strings.Join(fx.execQueries, "\n"))
		if !strings.Contains(joined, "target_database") {
			t.Fatalf("expected insert into change_drafts with target_database, got:\n%s", joined)
		}
	})

	t.Run("clears_draft_target_database_when_explicit_null_matches_empty_ephemeral_run", func(t *testing.T) {
		oldDB := "db_a"
		fx := &submitTargetDBFx{draftTargetDB: &oldDB}
		db := openDB(fx)
		svc := &Services{DB: db}
		er := &reviewapp.EphemeralRun{FlowID: "flow-1", FlowVersion: 3, TargetDatabase: ""}
		draftID, ver, rev, err := svc.ensureDraftForSubmitV2(ctx, db, actor, SubmitOrderV2Input{
			FlowID: "flow-1", DraftID: "draft-1", TargetDatabase: nil, TargetDatabaseSet: true,
		}, er)
		if err != nil || draftID != "draft-1" || ver != 2 || rev != 1 {
			t.Fatalf("got (%q, %d, %d, %v), want ('draft-1', 2, 1, nil)", draftID, ver, rev, err)
		}
		if len(fx.execQueries) != 1 || !strings.Contains(strings.ToLower(fx.execQueries[0]), "update change_drafts set target_database =") {
			t.Fatalf("expected single target_database clear query, got: %v", fx.execQueries)
		}
		if len(fx.execArgs[0]) < 1 || fx.execArgs[0][0].Value != nil {
			t.Fatalf("expected nil SQL parameter when clearing target_database, got %+v", fx.execArgs[0])
		}
	})

	t.Run("rejects_explicit_null_against_non_empty_ephemeral_run", func(t *testing.T) {
		oldDB := "db_a"
		fx := &submitTargetDBFx{draftTargetDB: &oldDB}
		db := openDB(fx)
		svc := &Services{DB: db}
		er := &reviewapp.EphemeralRun{FlowID: "flow-1", FlowVersion: 3, TargetDatabase: "db_a"}
		_, _, _, err := svc.ensureDraftForSubmitV2(ctx, db, actor, SubmitOrderV2Input{
			FlowID: "flow-1", DraftID: "draft-1", TargetDatabase: nil, TargetDatabaseSet: true,
		}, er)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.ErrReviewOutdated().Code {
			t.Fatalf("explicit null mismatch err = %v, want ErrReviewOutdated (2001)", err)
		}
	})
}
