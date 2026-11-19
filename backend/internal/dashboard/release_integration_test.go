//go:build integration

// release_integration_test.go RCP-20261020 acceptance: startup/scheduled release snapshot fetch and
// persistence, preserving previous snapshot on failure, rejecting malformed payloads, returning data:null
// on an empty table, and serving on the authenticated route surface (same as getCurrentAnnouncement).
package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/identity/session"
	"github.com/gin-gonic/gin"
)

func TestReleaseSnapshotLifecycle(t *testing.T) {
	db, s := newTestService(t)
	t.Cleanup(func() { db.Exec(`delete from upstream_release`) })
	ctx := context.Background()

	// Empty table: read returns (nil, nil) -> envelope data:null.
	view, err := s.LatestRelease(ctx)
	if err != nil || view != nil {
		t.Fatalf("empty read: view=%+v err=%v", view, err)
	}

	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		// GitHub requires User-Agent; returns non-2xx when missing.
		if r.Header.Get("User-Agent") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch n {
		case 1:
			_, _ = w.Write([]byte(`{"tag_name":"v4.2.0","name":"Yearning v4.2.0",` +
				`"html_url":"https://example.test/releases/v4.2.0","body":"notes",` +
				`"published_at":"2026-10-19T00:00:00Z"}`))
		default:
			// Rate-limit scenario: HTTP 403 preserves the existing snapshot.
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer upstream.Close()
	s.ReleaseHTTPClient = upstream.Client()

	// Startup seed fetch (seedCurrent=true): persists both the snapshot and current_version.
	if err := s.RefreshReleaseSnapshot(ctx, upstream.URL, true); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	view, err = s.LatestRelease(ctx)
	if err != nil || view == nil {
		t.Fatalf("read after refresh: view=%+v err=%v", view, err)
	}
	if view.TagName != "v4.2.0" || view.Name != "Yearning v4.2.0" ||
		view.HTMLURL != "https://example.test/releases/v4.2.0" || view.Body != "notes" {
		t.Fatalf("snapshot payload: %+v", view)
	}
	if view.PublishedAt == nil || *view.PublishedAt != "2026-10-19T00:00:00Z" || view.FetchedAt == "" {
		t.Fatalf("snapshot timestamps: %+v", view)
	}
	if view.CurrentVersion == nil || *view.CurrentVersion != "v4.2.0" {
		t.Fatalf("seeded current_version: %+v", view.CurrentVersion)
	}

	// HTTP 403: returns an error while keeping the previous snapshot intact in the database.
	if err := s.RefreshReleaseSnapshot(ctx, upstream.URL, false); err == nil {
		t.Fatal("rate-limited refresh must fail")
	}
	view2, err := s.LatestRelease(ctx)
	if err != nil || view2 == nil || view2.TagName != "v4.2.0" {
		t.Fatalf("snapshot must survive failed refresh: view=%+v err=%v", view2, err)
	}

	// Malformed payload (missing tag_name): rejected without modifying the database.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"no tag"}`))
	}))
	defer broken.Close()
	s.ReleaseHTTPClient = broken.Client()
	if err := s.RefreshReleaseSnapshot(ctx, broken.URL, false); err == nil {
		t.Fatal("payload without tag_name must fail")
	}
	if view3, _ := s.LatestRelease(ctx); view3 == nil || view3.TagName != "v4.2.0" {
		t.Fatalf("snapshot must survive bad payload: %+v", view3)
	}

	// Runtime refresh (seedCurrent=false): advances snapshot columns while keeping the previous current_version baseline.
	advanced := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v4.3.0","name":"n",` +
			`"html_url":"https://example.test/releases/v4.3.0","body":"newer",` +
			`"published_at":"2026-10-20T00:00:00Z"}`))
	}))
	defer advanced.Close()
	s.ReleaseHTTPClient = advanced.Client()
	if err := s.RefreshReleaseSnapshot(ctx, advanced.URL, false); err != nil {
		t.Fatalf("scheduled refresh: %v", err)
	}
	view4, err := s.LatestRelease(ctx)
	if err != nil || view4 == nil {
		t.Fatalf("read after scheduled refresh: view=%+v err=%v", view4, err)
	}
	if view4.TagName != "v4.3.0" {
		t.Fatalf("scheduled snapshot must advance: %+v", view4)
	}
	if view4.CurrentVersion == nil || *view4.CurrentVersion != "v4.2.0" {
		t.Fatalf("current_version must stay at the seeded reference: %+v", view4.CurrentVersion)
	}

	// Restart re-seed (seedCurrent=true): current_version catches up to the new baseline (upgrade = restart).
	if err := s.RefreshReleaseSnapshot(ctx, advanced.URL, true); err != nil {
		t.Fatalf("restart seed: %v", err)
	}
	if view5, _ := s.LatestRelease(ctx); view5 == nil || view5.CurrentVersion == nil || *view5.CurrentVersion != "v4.3.0" {
		t.Fatalf("restart must re-seed current_version: %+v", view5)
	}
}

func TestLatestReleaseAuthzAndEnvelope(t *testing.T) {
	db, s := newTestService(t)
	t.Cleanup(func() { db.Exec(`delete from upstream_release`) })

	// Stub upstream: single successful snapshot (used for handler-level envelope assertions).
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v4.2.0","name":"n",` +
			`"html_url":"https://example.test/releases/v4.2.0","body":"notes",` +
			`"published_at":"2026-10-19T00:00:00Z"}`))
	}))
	defer upstream.Close()
	s.ReleaseHTTPClient = upstream.Client()

	gin.SetMode(gin.TestMode)
	admin := &session.Identity{UserID: "u-admin", Username: "admin", IsBuiltinAdmin: true}
	member := &session.Identity{UserID: "u-member", Username: "member", IsBuiltinAdmin: false}
	store := fakeStore{
		"sess-admin":  {Identity: admin},
		"sess-member": {Identity: member},
	}
	engine := gin.New()
	engine.Use(session.Middleware(store, session.Options{CookieName: "yearning_session"}))
	RegisterRoutes(engine, &Deps{Service: s})

	do := func(cookie string, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "yearning_session", Value: cookie})
		}
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}

	// Anonymous request returns 401.
	if rec := do("", "/releases/latest"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous releases/latest: want 401 got %d", rec.Code)
	}
	// Empty table: authenticated session returns 200 with data:null.
	for _, cookie := range []string{"sess-member", "sess-admin"} {
		rec := do(cookie, "/releases/latest")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s releases/latest: want 200 got %d", cookie, rec.Code)
		}
		var envelope struct {
			ErrCode int              `json:"err_code"`
			Data    *json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.ErrCode != 0 {
			t.Fatalf("%s envelope: %v %+v", cookie, err, envelope)
		}
		// Unmarshaling JSON null leaves a nil RawMessage pointer — the exact expected shape for an empty table.
		if envelope.Data != nil {
			t.Fatalf("%s empty data: want null got %s", cookie, string(*envelope.Data))
		}
	}

	// After persisting a snapshot: data is a ReleaseSnapshot object (visible to member on the authenticated surface).
	if err := s.RefreshReleaseSnapshot(context.Background(), upstream.URL, true); err != nil {
		t.Fatalf("seed refresh: %v", err)
	}
	rec := do("sess-member", "/releases/latest")
	var payload struct {
		Data *struct {
			TagName string `json:"tag_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || payload.Data == nil {
		t.Fatalf("snapshot payload: %v %+v %s", err, payload, rec.Body.String())
	}
	if payload.Data.TagName != "v4.2.0" {
		t.Fatalf("snapshot tag: %s", payload.Data.TagName)
	}
}
