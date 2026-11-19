package application

import (
	"encoding/json"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
)

func TestStageReviewContextPreservesFrozenContextAndSummary(t *testing.T) {
	crypter := newSealTestCrypter(t)
	const runID = "00000000-0000-4000-8000-000000000101"
	const stagePosition int32 = 2
	contextValue := map[string]any{
		"stage_position": stagePosition,
		"review_run_id":  runID,
		"engine":         "postgresql",
		"large_integer":  json.Number("9007199254740993"),
		"decimal":        json.Number("0.123456789012345678901"),
	}
	env, err := sealStageReviewContext(crypter, runID, stagePosition, contextValue)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := openStageReviewContext(crypter, runID, stagePosition, env)
	if err != nil {
		t.Fatal(err)
	}
	opened["accepted_summary"] = "Review complete."
	opened["accepted_output_locale"] = "zh-CN"
	env, err = sealStageReviewContext(crypter, runID, stagePosition, opened)
	if err != nil {
		t.Fatal(err)
	}

	summary, locale, err := acceptedSummaryAndLocaleFromStageContext(crypter, runID, stagePosition, env)
	if err != nil {
		t.Fatal(err)
	}
	if summary == nil || *summary != "Review complete." {
		t.Fatalf("accepted summary = %#v, want Review complete.", summary)
	}
	if locale == nil || *locale != "zh-CN" {
		t.Fatalf("accepted locale = %#v, want zh-CN", locale)
	}
	reopened, err := openStageReviewContext(crypter, runID, stagePosition, env)
	if err != nil {
		t.Fatal(err)
	}
	if reopened["engine"] != "postgresql" || reopened["review_run_id"] != runID {
		t.Fatalf("stage context was not preserved: %#v", reopened)
	}
	if reopened["accepted_output_locale"] != "zh-CN" {
		t.Fatalf("accepted locale = %#v, want zh-CN", reopened["accepted_output_locale"])
	}
	if got := string(reopened["large_integer"].(json.Number)); got != "9007199254740993" {
		t.Fatalf("large integer = %q, want exact decimal preservation", got)
	}
	if got := string(reopened["decimal"].(json.Number)); got != "0.123456789012345678901" {
		t.Fatalf("decimal = %q, want exact decimal preservation", got)
	}
	if _, _, err := acceptedSummaryAndLocaleFromStageContext(crypter, runID, stagePosition+1, env); err == nil {
		t.Fatal("stage context must be bound to its stage position")
	}
}

func TestStageReviewContextRoundTripsFrozenDatasourceTarget(t *testing.T) {
	crypter := newSealTestCrypter(t)
	const runID = "00000000-0000-4000-8000-000000000102"
	const stagePosition int32 = 2
	target := frozenDatasourceTarget{
		SchemaVersion:        stageDatasourceTargetVersion,
		DatasourceID:         "00000000-0000-4000-8000-000000000103",
		Engine:               "postgresql",
		CompatibilityMode:    "postgresql",
		Host:                 "db.internal",
		Port:                 5432,
		DatabaseName:         "production",
		ConfiguredIdentityFP: "fingerprint",
		EngineFamily:         "postgresql",
		ServerVersion:        "17.2",
		InstanceMarker:       "pg:system",
		SystemIdentifier:     "system",
		DatabaseOID:          42,
		ConfiguredEndpointID: "db.internal:5432",
	}
	value, err := withFrozenDatasourceTarget(map[string]any{"engine": "postgresql"}, target)
	if err != nil {
		t.Fatal(err)
	}
	env, err := sealStageReviewContext(crypter, runID, stagePosition, value)
	if err != nil {
		t.Fatal(err)
	}
	got, err := frozenDatasourceTargetFromContext(crypter, runID, stagePosition, env)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("frozen target round trip = %+v, want %+v", got, target)
	}
}

func TestFrozenDatasourceTargetRejectsMutableConfigDrift(t *testing.T) {
	target := frozenDatasourceTarget{
		SchemaVersion:        stageDatasourceTargetVersion,
		DatasourceID:         "datasource-1",
		Engine:               "postgresql",
		CompatibilityMode:    "postgresql",
		Host:                 "db.internal",
		Port:                 5432,
		DatabaseName:         "production_a",
		ConfiguredIdentityFP: "fingerprint",
		EngineFamily:         "postgresql",
		ServerVersion:        "17.2",
		InstanceMarker:       "pg:system",
		SystemIdentifier:     "system",
		DatabaseOID:          42,
		ConfiguredEndpointID: "db.internal:5432",
	}
	databaseName := "production_b"
	identityFP := "fingerprint"
	row := datasourceTargetRow{
		ID: "datasource-1", Engine: "postgresql", CompatibilityMode: "postgresql",
		Host: "db.internal", Port: 5432, DatabaseName: &databaseName,
		IdentityFP: &identityFP, Enabled: true,
	}
	if target.matchesConfig(row) {
		t.Fatal("database-name drift must reject the frozen target")
	}
	databaseName = "production_a"
	if !target.matchesConfig(row) {
		t.Fatal("unchanged target must match")
	}
	for name, mutate := range map[string]func(*datasourceTargetRow){
		"engine":   func(v *datasourceTargetRow) { v.Engine = "mysql" },
		"mode":     func(v *datasourceTargetRow) { v.CompatibilityMode = "mysql" },
		"host":     func(v *datasourceTargetRow) { v.Host = "other.internal" },
		"port":     func(v *datasourceTargetRow) { v.Port = 5433 },
		"disabled": func(v *datasourceTargetRow) { v.Enabled = false },
		"fingerprint": func(v *datasourceTargetRow) {
			other := "other-fingerprint"
			v.IdentityFP = &other
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := row
			mutate(&got)
			if target.matchesConfig(got) {
				t.Fatalf("configuration drift in %s was accepted", name)
			}
		})
	}
}

func TestFrozenDatasourceTargetRejectsLiveIdentityDrift(t *testing.T) {
	target := frozenDatasourceTarget{
		SchemaVersion:        stageDatasourceTargetVersion,
		DatasourceID:         "datasource-1",
		Engine:               "postgresql",
		CompatibilityMode:    "postgresql",
		Host:                 "db.internal",
		Port:                 5432,
		DatabaseName:         "production",
		ConfiguredIdentityFP: "fingerprint",
		EngineFamily:         "postgresql",
		ServerVersion:        "17.2",
		InstanceMarker:       "pg:system",
		SystemIdentifier:     "system",
		DatabaseOID:          42,
		ConfiguredEndpointID: "db.internal:5432",
	}
	identity := database.ServerIdentity{
		EngineFamily: "postgresql", ServerVersion: "17.2",
		InstanceMarker: "pg:system", SystemIdentifier: "system",
		DatabaseOID: 42, DatabaseName: "production",
		ConfiguredEndpointID: "db.internal:5432",
	}
	if !target.matchesIdentity(identity) {
		t.Fatal("unchanged live identity must match")
	}
	for name, mutate := range map[string]func(*database.ServerIdentity){
		"database_name":  func(v *database.ServerIdentity) { v.DatabaseName = "other" },
		"database_oid":   func(v *database.ServerIdentity) { v.DatabaseOID = 43 },
		"server_version": func(v *database.ServerIdentity) { v.ServerVersion = "17.3" },
		"instance":       func(v *database.ServerIdentity) { v.InstanceMarker = "pg:other" },
		"endpoint":       func(v *database.ServerIdentity) { v.ConfiguredEndpointID = "other:5432" },
	} {
		t.Run(name, func(t *testing.T) {
			got := identity
			mutate(&got)
			if target.matchesIdentity(got) {
				t.Fatalf("identity drift in %s was accepted", name)
			}
		})
	}
}
