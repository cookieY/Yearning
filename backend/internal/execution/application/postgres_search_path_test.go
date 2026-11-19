package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

func TestDecodeFrozenPostgresSearchPathContext(t *testing.T) {
	t.Parallel()
	value := map[string]any{
		"accepted_summary": "existing context",
		frozenPostgresSearchPathContextKey: map[string]any{
			"version":         1,
			"current_user":    "writer",
			"configured":      "app, public",
			"explicit":        []string{"app", "public"},
			"effective":       []string{"pg_catalog", "app", "public"},
			"creation_schema": "app",
		},
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path, ok := decodeFrozenPostgresSearchPathContext(raw)
	if !ok {
		t.Fatal("valid frozen search_path was rejected")
	}
	if path.CurrentUser != "writer" || path.CreationSchema != "app" ||
		len(path.Explicit) != 2 || len(path.Effective) != 3 {
		t.Fatalf("unexpected decoded path: %+v", path)
	}
}

func TestDecodeFrozenPostgresSearchPathContextFailsClosed(t *testing.T) {
	t.Parallel()
	tests := map[string][]byte{
		"missing":   []byte(`{"accepted_summary":"x"}`),
		"malformed": []byte(`{"postgres_search_path":{"version":1}}`),
		"unknown": []byte(`{"postgres_search_path":{"version":1,"current_user":"writer",` +
			`"configured":"public","explicit":["public"],"effective":["pg_catalog","public"],` +
			`"creation_schema":"public","extra":true}}`),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, ok := decodeFrozenPostgresSearchPathContext(raw); ok {
				t.Fatal("invalid context was accepted")
			}
		})
	}
}

func TestEqualFrozenPostgresSearchPath(t *testing.T) {
	t.Parallel()
	base := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app, public",
		Explicit:  []string{"app", "public"},
		Effective: []string{"pg_catalog", "app", "public"}, CreationSchema: "app",
	}
	changed := base
	changed.Explicit = []string{"public", "app"}
	changed.Effective = []string{"pg_catalog", "public", "app"}
	if !equalFrozenPostgresSearchPath(base, base) {
		t.Fatal("identical paths differ")
	}
	if equalFrozenPostgresSearchPath(base, changed) {
		t.Fatal("reordered path was accepted")
	}
}

func TestStageSchemaResolutionForDatasource(t *testing.T) {
	t.Parallel()
	mysqlDB := "app_database"
	pgDS := &datasource.DatasourceModel{CompatibilityMode: "postgresql"}
	frozen := &reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app, public",
		Explicit:  []string{"app", "public"},
		Effective: []string{"pg_catalog", "app", "public"}, CreationSchema: "app",
	}

	t.Run("postgres replays explicit only", func(t *testing.T) {
		got, err := stageSchemaResolutionForDatasource(pgDS, frozen)
		if err != nil {
			t.Fatal(err)
		}
		if got.Schema != "app" ||
			!reflect.DeepEqual(got.SearchPath, []string{"app", "public"}) {
			t.Fatalf("unexpected resolution: %+v", got)
		}
		for _, schema := range got.SearchPath {
			if schema == "pg_catalog" {
				t.Fatal("implicit catalog namespace was installed in SET search_path")
			}
		}
		if got.PostgresPath == nil || !got.PostgresPath.Equal(*frozen) {
			t.Fatalf("full frozen path was not retained: %+v", got.PostgresPath)
		}
	})

	t.Run("postgres requires snapshot", func(t *testing.T) {
		_, err := stageSchemaResolutionForDatasource(pgDS, nil)
		if err == nil {
			t.Fatal("missing frozen path was accepted")
		}
	})

	t.Run("nil datasource fails", func(t *testing.T) {
		_, err := stageSchemaResolutionForDatasource(nil, frozen)
		if err == nil {
			t.Fatal("nil datasource was accepted")
		}
	})

	t.Run("mysql uses database", func(t *testing.T) {
		got, err := stageSchemaResolutionForDatasource(&datasource.DatasourceModel{
			CompatibilityMode: "mysql", DatabaseName: &mysqlDB,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.Schema != mysqlDB ||
			!reflect.DeepEqual(got.SearchPath, []string{mysqlDB}) ||
			got.PostgresPath != nil {
			t.Fatalf("mysql resolution changed: %+v", got)
		}
	})

	t.Run("mysql missing database fails", func(t *testing.T) {
		_, err := stageSchemaResolutionForDatasource(&datasource.DatasourceModel{
			CompatibilityMode: "mysql",
		}, nil)
		if err == nil {
			t.Fatal("missing mysql database was accepted")
		}
	})

}

func TestVerifyResolvedStageSearchPathFailsBeforeReturningTarget(t *testing.T) {
	t.Parallel()
	called := false
	svc := &Services{
		SearchPathProbe: func(
			context.Context, *database.PurposePool, database.PostgresSearchPath,
		) error {
			called = true
			return errors.New("path changed")
		},
	}
	frozen := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app",
		Explicit:  []string{"app"},
		Effective: []string{"pg_catalog", "app"}, CreationSchema: "app",
	}
	err := svc.verifyResolvedStageSearchPath(
		context.Background(), &database.PurposePool{},
		stageSchemaResolution{PostgresPath: &frozen},
	)
	if !called {
		t.Fatal("resolved path was not probed")
	}
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodePreflightFailed {
		t.Fatalf("probe failure must fail closed at preflight, got %v", err)
	}
}

func TestVerifyPostgresSearchPathSuccessUsesProbeSeam(t *testing.T) {
	t.Parallel()
	frozen := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app, public",
		Explicit:  []string{"app", "public"},
		Effective: []string{"pg_catalog", "app", "public"}, CreationSchema: "app",
	}
	if err := frozen.Validate(); err != nil {
		t.Fatalf("review snapshot invalid: %v", err)
	}
	if err := databasePostgresSearchPath(frozen).Validate(); err != nil {
		t.Fatalf("database snapshot invalid: %v", err)
	}
	called := false
	svc := &Services{
		SearchPathProbe: func(
			_ context.Context,
			_ *database.PurposePool,
			got database.PostgresSearchPath,
		) error {
			called = true
			want := databasePostgresSearchPath(frozen)
			if !want.Equal(got) {
				t.Fatalf("probe snapshot = %+v, want %+v", got, want)
			}
			return nil
		},
	}
	if err := svc.verifyPostgresSearchPath(
		context.Background(), &database.PurposePool{}, frozen,
	); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("probe seam was not called")
	}
}

func TestVerifyPostgresSearchPathRejectsTemporarySchemaBeforeProbe(t *testing.T) {
	t.Parallel()
	called := false
	svc := &Services{
		SearchPathProbe: func(
			context.Context, *database.PurposePool, database.PostgresSearchPath,
		) error {
			called = true
			return nil
		},
	}
	frozen := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app, pg_temp_4",
		Explicit:  []string{"app", "pg_temp_4"},
		Effective: []string{"app", "pg_temp_4", "pg_catalog"}, CreationSchema: "app",
	}
	err := svc.verifyPostgresSearchPath(
		context.Background(), &database.PurposePool{}, frozen)
	if called {
		t.Fatal("temporary path reached the probe seam")
	}
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodePreflightFailed {
		t.Fatalf("temporary path must fail closed, got %v", err)
	}
}

func TestLoadFrozenPostgresSearchPathRequiresSnapshotID(t *testing.T) {
	t.Parallel()
	_, err := (&Services{}).loadFrozenPostgresSearchPath(
		context.Background(), nil, nil)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodePreflightFailed {
		t.Fatalf("missing snapshot ID must fail closed, got %v", err)
	}
}

func TestFrozenStageReviewRowMatchesOrderStageBindings(t *testing.T) {
	t.Parallel()
	stage := &targetSnapshotRow{
		Position:            2,
		DatasourceID:        "00000000-0000-0000-0000-000000000101",
		IdentityFingerprint: strings.Repeat("a", 64),
		ReviewSnapshotHash:  strings.Repeat("c", 64),
	}
	row := frozenStageReviewRow{
		ReviewRunID:   "00000000-0000-0000-0000-000000000102",
		StagePosition: 2, DatasourceID: stage.DatasourceID,
		IdentityFingerprint: stage.IdentityFingerprint,
		SnapshotHash:        stage.ReviewSnapshotHash,
		State:               "passed", GatePassed: true,
		Ciphertext: []byte("ciphertext"),
	}
	if !row.matchesStage(stage) {
		t.Fatal("matching reviewed stage binding was rejected")
	}
	tests := map[string]func(*frozenStageReviewRow){
		"stage position": func(r *frozenStageReviewRow) { r.StagePosition = 3 },
		"datasource": func(r *frozenStageReviewRow) {
			r.DatasourceID = "00000000-0000-0000-0000-000000000103"
		},
		"identity fingerprint": func(r *frozenStageReviewRow) {
			r.IdentityFingerprint = strings.Repeat("b", 64)
		},
		"review snapshot hash": func(r *frozenStageReviewRow) {
			r.SnapshotHash = strings.Repeat("d", 64)
		},
		"review state": func(r *frozenStageReviewRow) { r.State = "partial" },
		"gate":         func(r *frozenStageReviewRow) { r.GatePassed = false },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			changed := row
			mutate(&changed)
			if changed.matchesStage(stage) {
				t.Fatalf("%s mismatch was accepted", name)
			}
		})
	}
}

func TestOpenFrozenPostgresSearchPathDecryptsStrictContext(t *testing.T) {
	crypter := newExecutionSearchPathTestCrypter(t)
	runID := "00000000-0000-0000-0000-000000000030"
	plain := []byte(`{
		"accepted_summary":"existing",
		"postgres_search_path":{
			"version":1,
			"current_user":"writer",
			"configured":"app, public",
			"explicit":["app","public"],
			"effective":["pg_catalog","app","public"],
			"creation_schema":"app"
		},
		"postgres_object_binding":{
			"version":3,
			"path":{
				"version":1,
				"current_user":"writer",
				"configured":"app, public",
				"explicit":["app","public"],
				"effective":["pg_catalog","app","public"],
				"creation_schema":"app"
			},
			"complete":true,
			"callables_safe":true,
			"callables_checked":true,
			"baseline":{},
			"relations":{}
		}
	}`)
	env, err := crypter.Seal(crypto.PurposePayload, plain, crypto.AAD{
		DeploymentID: crypter.DeploymentID(), Table: "stage_review_snapshots",
		PrimaryKey: fmt.Sprintf("%s#%d", runID, 2),
		Column:     "normalized_context_ciphertext", Purpose: crypto.PurposePayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &Services{Crypter: crypter}
	got, err := svc.openFrozenPostgresSearchPath(frozenStageReviewRow{
		ReviewRunID: runID, StagePosition: 2,
		DatasourceID:        "00000000-0000-0000-0000-000000000101",
		IdentityFingerprint: strings.Repeat("a", 64),
		SnapshotHash:        strings.Repeat("c", 64),
		State:               "passed", GatePassed: true,
		Ciphertext: env.Ciphertext, Nonce: env.Nonce,
		KeyID: env.KeyID, Algorithm: env.Algorithm,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentUser != "writer" || got.CreationSchema != "app" ||
		!reflect.DeepEqual(got.Explicit, []string{"app", "public"}) {
		t.Fatalf("unexpected decrypted path: %+v", got)
	}

	row := frozenStageReviewRow{
		ReviewRunID: runID, StagePosition: 2,
		DatasourceID:        "00000000-0000-0000-0000-000000000101",
		IdentityFingerprint: strings.Repeat("a", 64),
		SnapshotHash:        strings.Repeat("c", 64),
		State:               "passed", GatePassed: true,
		Ciphertext: env.Ciphertext, Nonce: env.Nonce,
		KeyID: env.KeyID, Algorithm: env.Algorithm,
	}
	row.StagePosition = 3
	if _, err := svc.openFrozenPostgresSearchPath(row); err == nil {
		t.Fatal("AAD-bound stage mismatch was accepted")
	}
}

func TestFrozenPostgresSearchPathValidationFailures(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string][]byte{
		"malformed root": []byte(`{`),
		"missing key":    []byte(`{"accepted_summary":"existing"}`),
		"nil key":        []byte(`{"postgres_search_path":null}`),
	} {
		if _, ok := decodeFrozenPostgresSearchPathContext(raw); ok {
			t.Fatalf("%s was accepted", name)
		}
	}

	valid := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app",
		Explicit: []string{"app"}, Effective: []string{"pg_catalog", "app"},
		CreationSchema: "app",
	}
	crypter := newExecutionSearchPathTestCrypter(t)
	for name, args := range map[string]struct {
		db      *gorm.DB
		crypter *crypto.Crypter
		ds      *datasource.DatasourceModel
		path    reviewapp.FrozenPostgresSearchPath
	}{
		"missing db": {
			crypter: crypter,
			ds:      &datasource.DatasourceModel{CompatibilityMode: "postgresql"},
			path:    valid,
		},
		"missing crypter": {
			db:   &gorm.DB{},
			ds:   &datasource.DatasourceModel{CompatibilityMode: "postgresql"},
			path: valid,
		},
		"missing datasource": {
			db: &gorm.DB{}, crypter: crypter, path: valid,
		},
		"wrong compatibility": {
			db: &gorm.DB{}, crypter: crypter,
			ds:   &datasource.DatasourceModel{CompatibilityMode: "mysql"},
			path: valid,
		},
		"invalid frozen path": {
			db: &gorm.DB{}, crypter: crypter,
			ds: &datasource.DatasourceModel{CompatibilityMode: "postgresql"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := ProbeFrozenPostgresSearchPath(
				context.Background(), args.db, args.crypter, args.ds, args.path,
			)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodePreflightFailed {
				t.Fatalf("error=%v, want preflight failed", err)
			}
		})
	}
}

func TestDatabasePostgresSearchPathCopiesSliceMembers(t *testing.T) {
	t.Parallel()
	source := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app",
		Explicit: []string{"app"}, Effective: []string{"pg_catalog", "app"},
		CreationSchema: "app",
	}
	got := databasePostgresSearchPath(source)
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	got.Explicit[0] = "mutated"
	got.Effective[1] = "mutated"
	if source.Explicit[0] != "app" || source.Effective[1] != "app" {
		t.Fatal("database projection shares backing slices")
	}
}

func TestVerifyPostgresInspectionUsesPreBatchBaselineAtOrdinalZero(t *testing.T) {
	t.Parallel()
	path := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app, public",
		Explicit:       []string{"app", "public"},
		Effective:      []string{"pg_catalog", "app", "public"},
		CreationSchema: "app",
	}
	binding := reviewapp.FrozenPostgresPathBinding{
		Version: 3, Path: path, Complete: true, CallablesSafe: true, CallablesChecked: true,
		Baseline: map[string]reviewapp.FrozenPostgresBaselineRelation{
			"orders": {
				PresentBySchema: map[string]string{"public": "table"},
				OIDBySchema:     map[string]uint32{"public": 201},
			},
		},
		Relations: map[string]reviewapp.FrozenPostgresObjectBinding{
			"1:table_1": {
				TableName: "orders", Schema: "app", Kind: "table", OID: 101,
				Present: true, Supported: true,
			},
		},
	}
	baselineLookup := database.PostgresRelationLookup{
		Relation: "orders",
		PresentBySchema: map[string]database.PostgresRelationKind{
			"public": database.PostgresRelationTable,
		},
		OIDBySchema: map[string]uint32{"public": 201},
	}
	if err := verifyPostgresInspection(
		path, binding, 0, []database.PostgresRelationLookup{baselineLookup},
	); err != nil {
		t.Fatalf("pre-batch baseline rejected: %v", err)
	}
	changed := baselineLookup
	changed.OIDBySchema = map[string]uint32{"public": 202}
	if err := verifyPostgresInspection(
		path, binding, 0, []database.PostgresRelationLookup{changed},
	); err == nil {
		t.Fatal("pre-batch baseline accepted a drop/recreate OID")
	}
}

func TestVerifyPostgresInspectionRejectsOrdinalShadowAndOIDDrift(t *testing.T) {
	t.Parallel()
	path := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app, public",
		Explicit:       []string{"app", "public"},
		Effective:      []string{"pg_catalog", "app", "public"},
		CreationSchema: "app",
	}
	binding := reviewapp.FrozenPostgresPathBinding{
		Version: 3, Path: path, Complete: true, CallablesSafe: true, CallablesChecked: true,
		Baseline: map[string]reviewapp.FrozenPostgresBaselineRelation{
			"orders": {
				PresentBySchema: map[string]string{"app": "table"},
				OIDBySchema:     map[string]uint32{"app": 101},
			},
		},
		Relations: map[string]reviewapp.FrozenPostgresObjectBinding{
			"2:table_1": {
				TableName: "orders", Schema: "app", Kind: "table", OID: 101,
				Present: true, Supported: true,
			},
		},
	}
	valid := database.PostgresRelationLookup{
		Relation: "orders",
		PresentBySchema: map[string]database.PostgresRelationKind{
			"app": database.PostgresRelationTable,
		},
		OIDBySchema: map[string]uint32{"app": 101},
	}
	if err := verifyPostgresInspection(
		path, binding, 2, []database.PostgresRelationLookup{valid},
	); err != nil {
		t.Fatalf("reviewed relation rejected: %v", err)
	}
	shadow := valid
	shadow.PresentBySchema = map[string]database.PostgresRelationKind{
		"public": database.PostgresRelationTable,
	}
	shadow.OIDBySchema = map[string]uint32{"public": 202}
	if err := verifyPostgresInspection(
		path, binding, 2, []database.PostgresRelationLookup{shadow},
	); err == nil {
		t.Fatal("same-path reveal/drop was accepted")
	}
	recreated := valid
	recreated.OIDBySchema = map[string]uint32{"app": 102}
	if err := verifyPostgresInspection(
		path, binding, 2, []database.PostgresRelationLookup{recreated},
	); err == nil {
		t.Fatal("same-name drop/recreate OID was accepted")
	}
}

func TestVerifyPostgresInspectionCreatedIdentityAndOrdinalValidation(t *testing.T) {
	t.Parallel()
	path := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app, public",
		Explicit:       []string{"app", "public"},
		Effective:      []string{"pg_catalog", "app", "public"},
		CreationSchema: "app",
	}
	binding := reviewapp.FrozenPostgresPathBinding{
		Version: 3, Path: path, Complete: true, CallablesSafe: true, CallablesChecked: true,
		Baseline: map[string]reviewapp.FrozenPostgresBaselineRelation{
			"orders": {},
		},
		Relations: map[string]reviewapp.FrozenPostgresObjectBinding{
			"2:table_1": {
				TableName: "orders", Schema: "app", Kind: "table",
				CreatedByOrdinal: 1, Present: true, Supported: true,
			},
		},
	}
	created := database.PostgresRelationLookup{
		Relation: "orders",
		PresentBySchema: map[string]database.PostgresRelationKind{
			"app": database.PostgresRelationTable,
		},
		OIDBySchema: map[string]uint32{"app": 999},
	}
	if err := verifyPostgresInspection(
		path, binding, 2, []database.PostgresRelationLookup{created},
	); err != nil {
		t.Fatalf("reviewed in-batch CREATE identity rejected: %v", err)
	}
	if _, err := reviewedPostgresRelationNames(binding, -1); err == nil {
		t.Fatal("negative statement ordinal was accepted")
	}
}

func TestReviewedPostgresRelationNamesFailsClosedAndDeduplicates(t *testing.T) {
	t.Parallel()
	binding := reviewapp.FrozenPostgresPathBinding{
		Baseline: map[string]reviewapp.FrozenPostgresBaselineRelation{
			"customers": {},
			"orders":    {},
		},
		Relations: map[string]reviewapp.FrozenPostgresObjectBinding{
			"2:table_a": {
				TableName: "orders", Supported: true,
			},
			"2:table_b": {
				TableName: "orders", Supported: true,
			},
			"3:table_c": {
				TableName: "customers", Supported: true,
			},
		},
	}
	baseline, err := reviewedPostgresRelationNames(binding, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		map[string]bool{baseline[0]: true, baseline[1]: true},
		map[string]bool{"customers": true, "orders": true},
	) {
		t.Fatalf("baseline names = %v", baseline)
	}
	second, err := reviewedPostgresRelationNames(binding, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, []string{"orders"}) {
		t.Fatalf("second ordinal names = %v, want de-duplicated orders", second)
	}
	third, err := reviewedPostgresRelationNames(binding, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(third, []string{"customers"}) {
		t.Fatalf("third ordinal names = %v", third)
	}

	tests := map[string]reviewapp.FrozenPostgresObjectBinding{
		"missing separator": {
			TableName: "orders", Supported: true,
		},
		"non-numeric ordinal": {
			TableName: "orders", Supported: true,
		},
		"zero ordinal": {
			TableName: "orders", Supported: true,
		},
		"unsupported relation": {
			TableName: "orders",
		},
	}
	keys := map[string]string{
		"missing separator":    "broken",
		"non-numeric ordinal":  "x:table_a",
		"zero ordinal":         "0:table_a",
		"unsupported relation": "2:table_a",
	}
	for name, relation := range tests {
		relation := relation
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			changed := binding
			changed.Relations = map[string]reviewapp.FrozenPostgresObjectBinding{
				keys[name]: relation,
			}
			_, err := reviewedPostgresRelationNames(changed, 2)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodePreflightFailed {
				t.Fatalf("malformed binding error = %v, want preflight failure", err)
			}
		})
	}
}

func TestReviewedBindingPositionUsesOrderedOrExplicitSchema(t *testing.T) {
	t.Parallel()
	path := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app, public",
		Explicit:       []string{"app", "public"},
		Effective:      []string{"pg_catalog", "app", "public"},
		CreationSchema: "app",
	}
	lookup := database.PostgresRelationLookup{
		Relation: "orders",
		PresentBySchema: map[string]database.PostgresRelationKind{
			"app":    database.PostgresRelationTable,
			"public": database.PostgresRelationView,
		},
		OIDBySchema: map[string]uint32{"app": 101, "public": 202},
	}

	schema, kind, oid, present := reviewedBindingPosition(path, lookup, false, "")
	if !present || schema != "app" || kind != "table" || oid != 101 {
		t.Fatalf("implicit resolution = %q/%q/%d/%v", schema, kind, oid, present)
	}
	schema, kind, oid, present = reviewedBindingPosition(path, lookup, true, "public")
	if !present || schema != "public" || kind != "view" || oid != 202 {
		t.Fatalf("explicit resolution = %q/%q/%d/%v", schema, kind, oid, present)
	}
	schema, kind, oid, present = reviewedBindingPosition(path, lookup, true, "missing")
	if present || schema != "missing" || kind != "" || oid != 0 {
		t.Fatalf("missing explicit resolution = %q/%q/%d/%v", schema, kind, oid, present)
	}
	lookup.PresentBySchema = map[string]database.PostgresRelationKind{}
	lookup.OIDBySchema = map[string]uint32{}
	schema, kind, oid, present = reviewedBindingPosition(path, lookup, false, "")
	if present || schema != path.CreationSchema || kind != "" || oid != 0 {
		t.Fatalf("missing implicit resolution = %q/%q/%d/%v", schema, kind, oid, present)
	}
}

func TestVerifyPostgresObjectBindingsRejectsMalformedEvidenceBeforeLookup(
	t *testing.T,
) {
	t.Parallel()
	path := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app",
		Explicit: []string{"app"}, Effective: []string{"pg_catalog", "app"},
		CreationSchema: "app",
	}
	binding := reviewapp.FrozenPostgresPathBinding{
		Version: 3, Path: path, Complete: true, CallablesSafe: true, CallablesChecked: true,
		Baseline: map[string]reviewapp.FrozenPostgresBaselineRelation{},
		Relations: map[string]reviewapp.FrozenPostgresObjectBinding{
			"2:table_a": {
				TableName: "orders", Schema: "app", Kind: "table",
				Present: true, Supported: true,
			},
		},
	}

	tests := map[string]struct {
		path     reviewapp.FrozenPostgresSearchPath
		binding  reviewapp.FrozenPostgresPathBinding
		ordinal  int32
		wantCode string
	}{
		"incomplete": {
			path: path,
			binding: func() reviewapp.FrozenPostgresPathBinding {
				value := binding
				value.Complete = false
				return value
			}(),
			wantCode: "postgres_search_path_snapshot_invalid",
		},
		"callables unsafe": {
			path: path,
			binding: func() reviewapp.FrozenPostgresPathBinding {
				value := binding
				value.CallablesSafe = false
				return value
			}(),
			wantCode: "postgres_search_path_snapshot_invalid",
		},
		"path mismatch": {
			path: func() reviewapp.FrozenPostgresSearchPath {
				value := path
				value.CreationSchema = "public"
				value.Explicit = []string{"public"}
				value.Effective = []string{"pg_catalog", "public"}
				return value
			}(),
			binding:  binding,
			wantCode: "postgres_search_path_snapshot_invalid",
		},
		"malformed relation key": {
			path: path,
			binding: func() reviewapp.FrozenPostgresPathBinding {
				value := binding
				value.Relations = map[string]reviewapp.FrozenPostgresObjectBinding{
					"broken": binding.Relations["2:table_a"],
				}
				return value
			}(),
			ordinal:  2,
			wantCode: "postgres_search_path_snapshot_invalid",
		},
		"unsupported relation": {
			path: path,
			binding: func() reviewapp.FrozenPostgresPathBinding {
				value := binding
				value.Relations = map[string]reviewapp.FrozenPostgresObjectBinding{
					"2:table_a": {
						TableName: "orders", Schema: "app",
						Kind: "table", Present: true,
					},
				}
				return value
			}(),
			ordinal:  2,
			wantCode: "postgres_search_path_changed",
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := verifyPostgresObjectBindings(
				context.Background(), nil, tc.path, tc.binding, tc.ordinal,
			)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodePreflightFailed ||
				!strings.HasSuffix(de.Name, ":"+tc.wantCode) {
				t.Fatalf("error = %v, want %s", err, tc.wantCode)
			}
		})
	}
}

func TestVerifyPostgresObjectBindingsRejectsEmptyLookupAndSkipsOtherOrdinals(
	t *testing.T,
) {
	t.Parallel()
	path := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app",
		Explicit: []string{"app"}, Effective: []string{"pg_catalog", "app"},
		CreationSchema: "app",
	}
	binding := reviewapp.FrozenPostgresPathBinding{
		Version: 3, Path: path, Complete: true, CallablesSafe: true, CallablesChecked: true,
		Baseline: map[string]reviewapp.FrozenPostgresBaselineRelation{},
		Relations: map[string]reviewapp.FrozenPostgresObjectBinding{
			"2:table_a": {
				TableName: "orders", Schema: "app", Kind: "table",
				Present: true, Supported: true,
			},
		},
	}
	if err := verifyPostgresObjectBindings(
		context.Background(), nil, path, binding, 3,
	); err != nil {
		t.Fatalf("unrelated ordinal should require no lookup: %v", err)
	}
	baseline := binding
	baseline.Relations = nil
	err := verifyPostgresObjectBindings(
		context.Background(), nil, path, baseline, 0,
	)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodePreflightFailed ||
		!strings.HasSuffix(de.Name, ":postgres_search_path_changed") {
		t.Fatalf("empty baseline lookup error = %v", err)
	}

	for name, relation := range map[string]reviewapp.FrozenPostgresObjectBinding{
		"non-numeric ordinal": {
			TableName: "orders", Supported: true,
		},
		"zero ordinal": {
			TableName: "orders", Supported: true,
		},
	} {
		relation := relation
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			key := "x:table_a"
			if name == "zero ordinal" {
				key = "0:table_a"
			}
			changed := binding
			changed.Relations = map[string]reviewapp.FrozenPostgresObjectBinding{
				key: relation,
			}
			err := verifyPostgresObjectBindings(
				context.Background(), nil, path, changed, 3,
			)
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodePreflightFailed ||
				!strings.HasSuffix(de.Name, ":postgres_search_path_snapshot_invalid") {
				t.Fatalf("malformed ordinal error = %v", err)
			}
		})
	}

	malformedName := binding
	malformedName.Relations = map[string]reviewapp.FrozenPostgresObjectBinding{
		"2:table_a": {
			Schema: "app", Kind: "table", Present: true, Supported: true,
		},
	}
	err = verifyPostgresObjectBindings(
		context.Background(), nil, path, malformedName, 2,
	)
	if !errors.As(err, &de) || de.Code != domain.CodePreflightFailed ||
		!strings.HasSuffix(de.Name, ":postgres_search_path_changed") {
		t.Fatalf("empty relation name error = %v", err)
	}
	if got := postgresSearchPathFailureReason("execution:other_failure"); got !=
		"postgres_search_path_changed" {
		t.Fatalf("non-search-path failure reason = %q", got)
	}
}

func TestVerifyPostgresInspectionRejectsMalformedBindingState(t *testing.T) {
	t.Parallel()
	path := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app",
		Explicit: []string{"app"}, Effective: []string{"pg_catalog", "app"},
		CreationSchema: "app",
	}
	binding := reviewapp.FrozenPostgresPathBinding{
		Version: 3, Path: path, Complete: true, CallablesSafe: true, CallablesChecked: true,
		Baseline: map[string]reviewapp.FrozenPostgresBaselineRelation{},
		Relations: map[string]reviewapp.FrozenPostgresObjectBinding{
			"2:table_a": {
				TableName: "orders", Schema: "app", Kind: "table",
				Present: true, Supported: true,
			},
		},
	}
	tests := map[string]struct {
		path     reviewapp.FrozenPostgresSearchPath
		binding  reviewapp.FrozenPostgresPathBinding
		ordinal  int32
		lookups  []database.PostgresRelationLookup
		wantText string
	}{
		"path mismatch": {
			path: func() reviewapp.FrozenPostgresSearchPath {
				value := path
				value.CreationSchema = "public"
				value.Explicit = []string{"public"}
				value.Effective = []string{"pg_catalog", "public"}
				return value
			}(),
			binding: binding, ordinal: 2,
			wantText: "postgres_search_path_snapshot_invalid",
		},
		"malformed relation key": {
			path: path,
			binding: func() reviewapp.FrozenPostgresPathBinding {
				value := binding
				value.Relations = map[string]reviewapp.FrozenPostgresObjectBinding{
					"broken": binding.Relations["2:table_a"],
				}
				return value
			}(),
			ordinal: 2, wantText: "postgres_search_path_snapshot_invalid",
		},
		"unsupported relation": {
			path: path,
			binding: func() reviewapp.FrozenPostgresPathBinding {
				value := binding
				value.Relations = map[string]reviewapp.FrozenPostgresObjectBinding{
					"2:table_a": {
						TableName: "orders", Schema: "app",
						Kind: "table", Present: true,
					},
				}
				return value
			}(),
			ordinal: 2, wantText: "postgres_search_path_changed",
		},
		"no relation for ordinal": {
			path: path, binding: binding, ordinal: 3,
		},
		"missing inspected lookup": {
			path: path, binding: binding, ordinal: 2,
			wantText: "postgres_search_path_changed",
		},
		"missing baseline lookup": {
			path: path,
			binding: func() reviewapp.FrozenPostgresPathBinding {
				value := binding
				value.Baseline = map[string]reviewapp.FrozenPostgresBaselineRelation{
					"orders": {},
				}
				return value
			}(),
			ordinal:  0,
			wantText: "postgres_search_path_changed",
		},
	}
	for name, tc := range tests {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := verifyPostgresInspection(
				tc.path, tc.binding, tc.ordinal, tc.lookups,
			)
			if tc.wantText == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodePreflightFailed ||
				!strings.HasSuffix(de.Name, ":"+tc.wantText) {
				t.Fatalf("error = %v, want %s", err, tc.wantText)
			}
		})
	}
}

func newExecutionSearchPathTestCrypter(t *testing.T) *crypto.Crypter {
	t.Helper()
	dir := t.TempDir()
	key := base64.StdEncoding.EncodeToString([]byte(
		"0123456789abcdef0123456789abcdef"))
	doc := fmt.Sprintf(`{
		"contract_version":1,
		"deployment_id":"execution-search-path-unit",
		"keys":[{
			"key_id":"payload-1",
			"purpose":"payload",
			"status":"encrypt_decrypt",
			"created_at":"2026-10-26T00:00:00Z",
			"key_base64":"%s"
		}]
	}`, key)
	path := filepath.Join(dir, "keyring.json")
	if err := os.WriteFile(path, []byte(doc), 0o400); err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.LoadKeyring(path)
	if err != nil {
		t.Fatal(err)
	}
	crypter, err := crypto.NewCrypter(ring)
	if err != nil {
		t.Fatal(err)
	}
	return crypter
}
