package application

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

func TestProbeSubmitPostgresSearchPathUsesExecutionSnapshot(t *testing.T) {
	t.Parallel()
	databaseName := "analytics"
	identity := "fingerprint-a"
	stage := stageFact{
		Position: 2, DatasourceID: "ds-1", Name: "analytics-primary",
		Engine: "postgresql", CompatibilityMode: "postgresql",
		Host: "db.internal", Port: 5432, DatabaseName: &databaseName,
		IdentityFP: &identity,
	}
	expected := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app, public",
		Explicit:  []string{"app", "public"},
		Effective: []string{"pg_catalog", "app", "public"}, CreationSchema: "app",
	}
	expectedContext := reviewapp.FrozenPostgresReviewContext{
		Path: expected,
		Binding: reviewapp.FrozenPostgresPathBinding{
			Version: 3, Path: expected, Complete: true, CallablesSafe: true, CallablesChecked: true,
			Relations: map[string]reviewapp.FrozenPostgresObjectBinding{},
		},
	}
	var gotDS *datasource.DatasourceModel
	var gotPath reviewapp.FrozenPostgresSearchPath
	var gotBinding *reviewapp.FrozenPostgresPathBinding
	var gotOrdinal int32
	svc := &Services{
		SearchPathProbe: func(
			_ context.Context,
			ds *datasource.DatasourceModel,
			path reviewapp.FrozenPostgresSearchPath,
			binding *reviewapp.FrozenPostgresPathBinding,
			ordinal int32,
		) error {
			gotDS = ds
			gotPath = path
			gotBinding = binding
			gotOrdinal = ordinal
			return nil
		},
	}
	if err := svc.probeSubmitPostgresSearchPath(
		context.Background(), stage, expectedContext,
	); err != nil {
		t.Fatal(err)
	}
	if gotDS == nil || gotDS.ID != stage.DatasourceID ||
		gotDS.Name != stage.Name || gotDS.Engine != stage.Engine ||
		gotDS.CompatibilityMode != stage.CompatibilityMode ||
		gotDS.Host != stage.Host || gotDS.Port != stage.Port ||
		gotDS.DatabaseName == nil || *gotDS.DatabaseName != databaseName ||
		gotDS.IdentityFingerprint == nil || *gotDS.IdentityFingerprint != identity {
		t.Fatalf("probe did not receive the frozen datasource: %+v", gotDS)
	}
	if !gotPath.Equal(expected) {
		t.Fatalf("probe path = %+v, want %+v", gotPath, expected)
	}
	if gotBinding == nil || !gotBinding.Path.Equal(expected) || gotOrdinal != 0 {
		t.Fatalf("probe binding=%+v ordinal=%d", gotBinding, gotOrdinal)
	}
}

func TestVerifySubmitPostgresSearchPathProbeFailureIsOutdated(t *testing.T) {
	t.Parallel()
	svc := &Services{
		SearchPathProbe: func(
			context.Context,
			*datasource.DatasourceModel,
			reviewapp.FrozenPostgresSearchPath,
			*reviewapp.FrozenPostgresPathBinding,
			int32,
		) error {
			return errors.New("role changed")
		},
	}
	err := svc.verifyDecodedSubmitPostgresSearchPath(
		context.Background(), stageFact{}, reviewapp.FrozenPostgresReviewContext{},
	)
	if err == nil {
		t.Fatal("probe failure was ignored")
	}
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeReviewOutdated {
		t.Fatalf("probe failure must reject a stale review, got %v", err)
	}
}

func TestVerifySubmitPostgresSearchPathBranches(t *testing.T) {
	crypter := newSearchPathTestCrypter(t)
	stage := stageFact{
		StageID:  "00000000-0000-0000-0000-000000000010",
		Position: 1, DatasourceID: "00000000-0000-0000-0000-000000000011",
		CompatibilityMode: "postgresql", Engine: "postgresql",
		Host: "db.internal", Port: 5432,
	}
	frozenPath := reviewapp.FrozenPostgresSearchPath{
		Version: 1, CurrentUser: "writer", Configured: "app, public",
		Explicit:  []string{"app", "public"},
		Effective: []string{"pg_catalog", "app", "public"}, CreationSchema: "app",
	}
	sealSnapshot := func(includeContext bool) stageSnapRow {
		plain := `{"accepted_summary":"existing"}`
		if includeContext {
			plain = `{"accepted_summary":"existing","postgres_search_path":{` +
				`"version":1,"current_user":"writer","configured":"app, public",` +
				`"explicit":["app","public"],` +
				`"effective":["pg_catalog","app","public"],"creation_schema":"app"},` +
				`"postgres_object_binding":{` +
				`"version":3,"path":{` +
				`"version":1,"current_user":"writer","configured":"app, public",` +
				`"explicit":["app","public"],` +
				`"effective":["pg_catalog","app","public"],"creation_schema":"app"},` +
				`"complete":true,"callables_safe":true,"callables_checked":true,"baseline":{},"relations":{}}}`
		}
		env, err := crypter.Seal(crypto.PurposePayload, []byte(plain), crypto.AAD{
			DeploymentID: crypter.DeploymentID(), Table: "stage_review_snapshots",
			PrimaryKey: fmt.Sprintf(
				"%s#%d", "00000000-0000-0000-0000-000000000020", stage.Position),
			Column: "normalized_context_ciphertext", Purpose: crypto.PurposePayload,
		})
		if err != nil {
			t.Fatal(err)
		}
		return stageSnapRow{
			NormalizedContext: env.Ciphertext, NormalizedNonce: env.Nonce,
			NormalizedKeyID: env.KeyID, NormalizedAlgorithm: env.Algorithm,
		}
	}

	t.Run("postgres probes", func(t *testing.T) {
		st := stage
		called := 0
		svc := &Services{
			Crypter: crypter,
			SearchPathProbe: func(
				_ context.Context,
				_ *datasource.DatasourceModel,
				got reviewapp.FrozenPostgresSearchPath,
				binding *reviewapp.FrozenPostgresPathBinding,
				ordinal int32,
			) error {
				called++
				if !got.Equal(frozenPath) {
					t.Fatalf("probe path = %+v, want %+v", got, frozenPath)
				}
				if binding == nil || !binding.Path.Equal(frozenPath) ||
					ordinal != 0 {
					t.Fatalf("probe binding=%+v ordinal=%d", binding, ordinal)
				}
				return nil
			},
		}
		if err := svc.verifySubmitPostgresSearchPath(
			context.Background(), "00000000-0000-0000-0000-000000000020",
			sealSnapshot(true), st,
		); err != nil {
			t.Fatal(err)
		}
		if called != 1 {
			t.Fatalf("probe calls = %d, want 1", called)
		}
	})

	t.Run("mysql skips probe", func(t *testing.T) {
		st := stage
		st.CompatibilityMode = "mysql"
		called := 0
		svc := &Services{
			Crypter: crypter,
			SearchPathProbe: func(
				context.Context,
				*datasource.DatasourceModel,
				reviewapp.FrozenPostgresSearchPath,
				*reviewapp.FrozenPostgresPathBinding,
				int32,
			) error {
				called++
				return nil
			},
		}
		if err := svc.verifySubmitPostgresSearchPath(
			context.Background(), "00000000-0000-0000-0000-000000000020",
			stageSnapRow{}, st,
		); err != nil {
			t.Fatal(err)
		}
		if called != 0 {
			t.Fatalf("probe calls = %d, want 0", called)
		}
	})

	t.Run("missing historical path is outdated", func(t *testing.T) {
		st := stage
		called := 0
		svc := &Services{
			Crypter: crypter,
			SearchPathProbe: func(
				context.Context,
				*datasource.DatasourceModel,
				reviewapp.FrozenPostgresSearchPath,
				*reviewapp.FrozenPostgresPathBinding,
				int32,
			) error {
				called++
				return nil
			},
		}
		err := svc.verifySubmitPostgresSearchPath(
			context.Background(), "00000000-0000-0000-0000-000000000020",
			sealSnapshot(false), st,
		)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeReviewOutdated {
			t.Fatalf("missing snapshot must reject review, got %v", err)
		}
		if called != 0 {
			t.Fatalf("probe calls = %d, want 0", called)
		}
	})
}

func TestPostgresSearchPathSubmitDecodeFailuresRemainOutdated(t *testing.T) {
	t.Parallel()
	crypter := newSearchPathTestCrypter(t)
	const runID = "00000000-0000-4000-8000-000000000499"
	if _, ok := decodeStageSnapshotPostgresReviewContext(
		crypter, runID, 1, stageSnapRow{},
	); ok {
		t.Fatal("missing normalized context was accepted")
	}
	if _, ok := decodeStageSnapshotPostgresReviewContext(
		crypter, runID, 1, stageSnapRow{
			NormalizedContext: []byte("invalid"),
			NormalizedKeyID:   "payload-1",
		},
	); ok {
		t.Fatal("unreadable normalized context was accepted")
	}
	if _, ok := reviewapp.DecodeFrozenPostgresReviewContext(
		[]byte(`{`),
	); ok {
		t.Fatal("malformed normalized context JSON was accepted")
	}

	stage := stageFact{
		StageID:           "00000000-0000-4000-8000-000000000498",
		Position:          1,
		CompatibilityMode: "postgresql",
		Engine:            "postgresql",
	}
	svc := &Services{Crypter: crypter}
	if err := svc.probeSubmitPostgresSearchPath(
		context.Background(), stage, reviewapp.FrozenPostgresReviewContext{},
	); err == nil {
		t.Fatal("default probe with unavailable services succeeded")
	}
}

func newSearchPathTestCrypter(t *testing.T) *crypto.Crypter {
	t.Helper()
	dir := t.TempDir()
	key := base64.StdEncoding.EncodeToString([]byte(
		"0123456789abcdef0123456789abcdef"))
	doc := fmt.Sprintf(`{
		"contract_version":1,
		"deployment_id":"search-path-unit",
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
