package workflow

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type stageViewStubFx struct{}

func (stageViewStubFx) Connect(context.Context) (driver.Conn, error) { return stageViewStubFx{}, nil }
func (stageViewStubFx) Driver() driver.Driver                        { return stageViewStubDriver{} }

type stageViewStubDriver struct{}

func (stageViewStubDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }
func (stageViewStubFx) Prepare(string) (driver.Stmt, error)  { return nil, errors.New("unused") }
func (stageViewStubFx) Close() error                         { return nil }
func (stageViewStubFx) Begin() (driver.Tx, error)            { return nil, errors.New("unused") }
func (stageViewStubFx) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

func (stageViewStubFx) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	q := strings.ToLower(query)
	now := time.Now().UTC()
	switch {
	case strings.Contains(q, `from "flow_stages"`):
		return &stageViewStubRows{
			cols: []string{"id", "flow_id", "position", "datasource_id"},
			data: [][]driver.Value{{"st-1", "flow-1", int32(1), "ds-1"}},
		}, nil
	case strings.Contains(q, "from datasources where id in"):
		return &stageViewStubRows{
			cols: []string{"id", "name", "engine", "compatibility_mode", "database_name"},
			data: [][]driver.Value{{"ds-1", "mysql-primary", "mysql", "mysql", nil}},
		}, nil
	case strings.Contains(q, `from "flow_approval_steps"`):
		stageID := "st-1"
		return &stageViewStubRows{
			cols: []string{"id", "flow_id", "stage_id", "position"},
			data: [][]driver.Value{{"sp-1", "flow-1", stageID, int32(1)}},
		}, nil
	case strings.Contains(q, `from "flow_approval_step_actors"`):
		return &stageViewStubRows{
			cols: []string{"step_id", "user_id"},
			data: [][]driver.Value{{"sp-1", "u-approver"}},
		}, nil
	case strings.Contains(q, `from "flow_execution_actors"`):
		return &stageViewStubRows{
			cols: []string{"stage_id", "user_id"},
			data: [][]driver.Value{{"st-1", "u-exec"}},
		}, nil
	case strings.Contains(q, "from flow_query_capabilities"):
		return &stageViewStubRows{cols: []string{"flow_id", "datasource_id", "datasource_name", "can_query", "can_export"}}, nil
	default:
		_ = now
		return &stageViewStubRows{cols: []string{}}, nil
	}
}

type stageViewStubRows struct {
	cols []string
	data [][]driver.Value
	idx  int
}

func (r *stageViewStubRows) Columns() []string { return r.cols }
func (r *stageViewStubRows) Close() error      { return nil }
func (r *stageViewStubRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.idx])
	r.idx++
	return nil
}

func TestStageViewIncludesDatasourceMetadata(t *testing.T) {
	sqlDB := sql.OpenDB(stageViewStubFx{})
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
		WithoutReturning:     true,
	}), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{db: db}
	rs := "rs-1"
	now := time.Now().UTC()
	views, err := svc.viewsOf(context.Background(), []FlowModel{{
		ID: "flow-1", Name: "f1", FlowType: "change_review", Status: "enabled", RuleSetID: &rs, Version: 1, CreatedAt: now, UpdatedAt: now,
	}})
	if err != nil {
		t.Fatalf("viewsOf failed: %v", err)
	}
	if len(views) != 1 || len(views[0].Stages) != 1 {
		t.Fatalf("unexpected views: %+v", views)
	}
	st := views[0].Stages[0]
	if st.DatasourceName != "mysql-primary" || st.DatasourceEngine != "mysql" || st.DatasourceCompatibilityMode != "mysql" || st.DatasourceDatabaseName != nil {
		t.Fatalf("unexpected stage datasource metadata: %+v", st)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"datasource_engine":"mysql"`) || !strings.Contains(string(raw), `"datasource_compatibility_mode":"mysql"`) {
		t.Fatalf("expected serialized datasource_engine/mode in %s", string(raw))
	}
}
