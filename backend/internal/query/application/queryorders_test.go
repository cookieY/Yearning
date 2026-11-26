package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/cookieY/YearningX/backend/internal/query/domain"
)

// Capture the SQL and bound values at GORM's row boundary without a database.
// The sentinel stops Scan before any connection can be opened.
func TestListQueryOrdersKeysetSQL(t *testing.T) {
	const actorID = "11111111-1111-4111-8111-111111111111"
	const cursorID = "22222222-2222-4222-8222-222222222222"
	cursorTime := time.Date(2026, 10, 27, 8, 30, 0, 123456000, time.UTC)
	const union = `select id::text as id, created_at, 'access_request' as kind from query_access_requests
 where requester_user_id_snapshot = $1
 or exists (select 1 from query_access_approval_actors a
 join query_access_approval_steps st on st.id = a.step_id
 where st.request_id = query_access_requests.id and a.reviewer_user_id_snapshot = $2)
 union all
 select id::text as id, created_at, 'session' as kind from query_sessions where user_id_snapshot = $3`
	for _, tc := range []struct {
		name, after, suffix string
		args                []any
	}{
		{"first page", "", "order by created_at desc, id desc limit $4", []any{actorID, actorID, actorID, 21}},
		{"next page", cursorTime.Format(time.RFC3339Nano) + "|" + cursorID,
			"where (created_at, id) < ($4, $5) order by created_at desc, id desc limit $6",
			[]any{actorID, actorID, actorID, cursorTime, cursorID, 21}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 dbname=unused sslmode=disable"}),
				&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := sqlDB.Close(); err != nil {
					t.Error(err)
				}
			})
			stop := errors.New("captured query")
			called := false
			if err := db.Callback().Row().Replace("gorm:row", func(tx *gorm.DB) {
				called = true
				got := strings.Join(strings.Fields(tx.Statement.SQL.String()), " ")
				// The cursor must filter both sources after the UNION, preserving each
				// source's actor predicate. Appending WHERE to the session SELECT is invalid.
				want := strings.Join(strings.Fields("select id, created_at, kind from ("+union+") as query_orders "+tc.suffix), " ")
				if got != want {
					t.Errorf("keyset SQL:\n got: %s\nwant: %s", got, want)
				}
				if !reflect.DeepEqual(tx.Statement.Vars, tc.args) {
					t.Errorf("bound arguments = %v, want %v", tx.Statement.Vars, tc.args)
				}
				tx.AddError(stop)
			}); err != nil {
				t.Fatal(err)
			}
			_, _, err = (&Services{DB: db}).ListQueryOrders(context.Background(), Actor{UserID: actorID}, 20, tc.after)
			if !called || !errors.Is(err, stop) {
				t.Fatalf("query boundary reached=%v err=%v", called, err)
			}
		})
	}
}

func TestListQueryOrdersRejectsInvalidCursorBeforeQuery(t *testing.T) {
	for _, after := range []string{"invalid", "|id", "not-a-time|22222222-2222-4222-8222-222222222222", "2026-10-27T08:30:00Z|not-a-uuid"} {
		t.Run(after, func(t *testing.T) {
			_, _, err := (&Services{}).ListQueryOrders(context.Background(), Actor{}, 20, after)
			var failure *domain.Error
			if !errors.As(err, &failure) || failure.Code != 1001 || failure.Name != "VALIDATION_FAILED" {
				t.Fatalf("invalid cursor error = %v", err)
			}
		})
	}
}

func TestQueryOrderEntryOmitsInactiveVariant(t *testing.T) {
	reqEntry := QueryOrderEntry{
		Kind:      "access_request",
		ID:        "11111111-1111-4111-8111-111111111111",
		CreatedAt: "2026-11-25T00:00:00Z",
		Request:   &AccessRequestView{ID: "11111111-1111-4111-8111-111111111111"},
	}
	reqBytes, err := json.Marshal(reqEntry)
	if err != nil {
		t.Fatal(err)
	}
	var reqMap map[string]any
	if err := json.Unmarshal(reqBytes, &reqMap); err != nil {
		t.Fatal(err)
	}
	if _, hasSession := reqMap["session"]; hasSession {
		t.Fatalf("access_request entry must omit inactive session field, got %s", string(reqBytes))
	}

	sessEntry := QueryOrderEntry{
		Kind:      "session",
		ID:        "22222222-2222-4222-8222-222222222222",
		CreatedAt: "2026-11-25T00:00:00Z",
		Session:   &SessionView{ID: "22222222-2222-4222-8222-222222222222"},
	}
	sessBytes, err := json.Marshal(sessEntry)
	if err != nil {
		t.Fatal(err)
	}
	var sessMap map[string]any
	if err := json.Unmarshal(sessBytes, &sessMap); err != nil {
		t.Fatal(err)
	}
	if _, hasRequest := sessMap["request"]; hasRequest {
		t.Fatalf("session entry must omit inactive request field, got %s", string(sessBytes))
	}
}
