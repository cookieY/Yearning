package application

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func TestValidateTargetDatabase(t *testing.T) {
	if err := ValidateTargetDatabase("shop_db"); err != nil {
		t.Fatalf("valid db rejected: %v", err)
	}
	for _, bad := range []string{
		"",
		"   ",
		"mysql",
		"MySQL",
		"sys",
		"information_schema",
		"performance_schema",
		"bad db!",
	} {
		err := ValidateTargetDatabase(bad)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != 1001 {
			t.Fatalf("ValidateTargetDatabase(%q) = %v, want domain error 1001", bad, err)
		}
	}
}

func TestProbeFrozenDatasourceTargetPostgresWithoutConfiguredDatabase(t *testing.T) {
	idFP := "fp-pg-empty"
	svc := &Services{
		DatasourceIdentityProbe: func(_ context.Context, _ *gorm.DB, _ *crypto.Crypter, _ datasourceTargetRow) (database.ServerIdentity, error) {
			return database.ServerIdentity{
				EngineFamily:         "postgresql",
				ServerVersion:        "17.2",
				InstanceMarker:       "pg:sys-1",
				SystemIdentifier:     "sys-1",
				DatabaseOID:          16384,
				DatabaseName:         "postgres",
				ConfiguredEndpointID: "127.0.0.1:5432",
			}, nil
		},
	}
	row := datasourceTargetRow{
		ID:                "ds-pg",
		Engine:            "postgresql",
		CompatibilityMode: "postgresql",
		Host:              "127.0.0.1",
		Port:              5432,
		DatabaseName:      nil,
		IdentityFP:        &idFP,
		Enabled:           true,
	}
	target, err := svc.probeFrozenDatasourceTarget(context.Background(), row)
	if err != nil {
		t.Fatalf("probeFrozenDatasourceTarget failed with nil PG database_name: %v", err)
	}
	if target.DatabaseName != "postgres" {
		t.Fatalf("target.DatabaseName = %q, want probed postgres", target.DatabaseName)
	}
	if !target.matchesConfig(row) {
		t.Fatal("matchesConfig must accept PG datasource with empty configured database_name")
	}
	otherDB := "other_db"
	rowChanged := row
	rowChanged.DatabaseName = &otherDB
	if target.matchesConfig(rowChanged) {
		t.Fatal("matchesConfig must reject explicit conflicting PG database_name")
	}
}

func TestResolveMySQLStageBinding(t *testing.T) {
	type groupSpec struct {
		db    string
		table string
	}
	buildGroups := func(mapper *privacy.RunMapper, specs []groupSpec) []provider.AnonymizedGroup {
		out := make([]provider.AnonymizedGroup, 0, len(specs))
		for i, sp := range specs {
			var schemaTok string
			var objs []provider.AnonymizedObjectReference
			if sp.db != "" {
				var err error
				schemaTok, err = mapper.ScopedToken(privacy.KindSchema, privacy.ScopeCaseSensitive, sp.db)
				if err != nil {
					t.Fatalf("schema token(%s): %v", sp.db, err)
				}
				objs = append(objs, provider.AnonymizedObjectReference{
					Kind: "schema", Token: schemaTok, Relation: "reference",
				})
			}
			tableTok, err := mapper.ScopedToken(privacy.KindTable, "", sp.table)
			if err != nil {
				t.Fatalf("table token(%s): %v", sp.table, err)
			}
			colTok, err := mapper.ScopedToken(privacy.KindColumn, "", "project_name")
			if err != nil {
				t.Fatalf("col token: %v", err)
			}
			objs = append(objs,
				provider.AnonymizedObjectReference{Kind: "table", Token: tableTok, SchemaToken: schemaTok, Relation: "write"},
				provider.AnonymizedObjectReference{Kind: "column", Token: colTok, ParentToken: tableTok, SchemaToken: schemaTok, Relation: "write"},
			)
			out = append(out, provider.AnonymizedGroup{
				GroupID:                     "g" + string(rune('1'+i)),
				RepresentativeAnonymizedSQL: "INSERT INTO tbl (col) VALUES (:str)",
				MetadataContextComplete:     true,
				Objects:                     objs,
			})
		}
		return out
	}

	if effective := resolveEffectiveMySQLDatabase(frozenFlowStageRow{DatabaseName: ptrString("default_db")}, "override_db"); effective != "override_db" {
		t.Fatalf("effective = %q, want override_db", effective)
	}
	if effective := resolveEffectiveMySQLDatabase(frozenFlowStageRow{DatabaseName: ptrString("default_db")}); effective != "default_db" {
		t.Fatalf("effective = %q, want default_db", effective)
	}

	for _, tc := range []struct {
		name           string
		dsDB           *string
		overrideDB     string
		specs          []groupSpec
		wantDefensible bool
		wantUnavail    string
		wantSchemas    []string
		wantDefault    string
		wantDerived    bool
		wantComplete   bool
		wantReason     string
	}{
		{
			name:        "empty default and empty sql fails datasource_database_required",
			wantUnavail: "datasource_database_required", wantComplete: false, wantReason: "datasource_database_required",
		},
		{
			name:        "system schema override fails datasource_database_required",
			dsDB:        ptrString("default_db"),
			overrideDB:  "information_schema",
			wantUnavail: "datasource_database_required", wantComplete: false, wantReason: "datasource_database_required",
		},
		{
			name:           "override satisfies empty default",
			overrideDB:     "orders_db",
			specs:          []groupSpec{{table: "orders"}},
			wantDefensible: true, wantSchemas: []string{"orders_db"}, wantDefault: "orders_db", wantDerived: true, wantComplete: true,
		},
		{
			name:           "override matching explicit db still verifies schema catalog",
			overrideDB:     "orders_db",
			specs:          []groupSpec{{db: "orders_db", table: "orders"}},
			wantDefensible: true, wantSchemas: []string{"orders_db"}, wantDefault: "orders_db", wantDerived: true, wantComplete: true,
		},
		{
			name:           "default DB with unqualified SQL does not trigger SQL-derived check",
			dsDB:           ptrString("demo"),
			specs:          []groupSpec{{table: "orders"}},
			wantDefensible: true, wantSchemas: []string{"demo"}, wantDefault: "demo", wantDerived: false, wantComplete: true,
		},
		{
			name:           "empty default with single explicit db passes",
			specs:          []groupSpec{{db: "demo", table: "deploy_record"}},
			wantDefensible: true, wantSchemas: []string{"demo"}, wantDerived: true, wantComplete: true,
		},
		{
			name:           "empty default with multi-db SQL passes",
			specs:          []groupSpec{{db: "demo", table: "t1"}, {db: "devops", table: "t2"}},
			wantDefensible: true, wantSchemas: []string{"demo", "devops"}, wantDerived: true, wantComplete: true,
		},
		{
			name:        "empty default with unqualified SQL fails datasource_database_required",
			specs:       []groupSpec{{table: "deploy_record"}},
			wantUnavail: "datasource_database_required", wantComplete: false, wantReason: "datasource_database_required",
		},
		{
			name:           "empty default with mixed qualified and unqualified SQL fails datasource_database_required",
			specs:          []groupSpec{{db: "demo", table: "t1"}, {table: "orders"}},
			wantDefensible: true, wantSchemas: []string{"demo"}, wantDerived: true, wantComplete: false, wantReason: "datasource_database_required",
		},
		{
			name:           "default DB plus extra explicit DB authorizes both",
			dsDB:           ptrString("demo"),
			specs:          []groupSpec{{table: "t1"}, {db: "devops", table: "t2"}},
			wantDefensible: true, wantSchemas: []string{"demo", "devops"}, wantDefault: "demo", wantDerived: true, wantComplete: true,
		},
		{
			name:        "system schema alone fails sql_schema_out_of_scope",
			specs:       []groupSpec{{db: "mysql", table: "user"}},
			wantUnavail: "sql_schema_out_of_scope", wantComplete: false, wantReason: "sql_schema_out_of_scope",
		},
		{
			name:           "mixed valid DB and system schema fails sql_schema_out_of_scope",
			specs:          []groupSpec{{db: "demo", table: "t1"}, {db: "information_schema", table: "tables"}},
			wantDefensible: true, wantSchemas: []string{"demo"}, wantDerived: true, wantComplete: false, wantReason: "sql_schema_out_of_scope",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapper := privacy.NewRunMapper()
			groups := buildGroups(mapper, tc.specs)
			b := resolveMySQLStageBinding(frozenFlowStageRow{
				Position: 1, CompatibilityMode: "mysql", DatabaseName: tc.dsDB,
			}, tc.overrideDB, groups, mapper)
			if b.defensible != tc.wantDefensible || b.unavailableReason != tc.wantUnavail ||
				b.defaultSchema() != tc.wantDefault || b.hasSQLDerivedSchemas != tc.wantDerived ||
				len(b.schemas) != len(tc.wantSchemas) {
				t.Fatalf("binding mismatch: got=%+v default=%q, want defensible=%v unavail=%q schemas=%v default=%q derived=%v",
					b, b.defaultSchema(), tc.wantDefensible, tc.wantUnavail, tc.wantSchemas, tc.wantDefault, tc.wantDerived)
			}
			for _, s := range tc.wantSchemas {
				if !b.schemas[s] {
					t.Fatalf("missing schema %q in %+v", s, b.schemas)
				}
			}
			if len(groups) > 0 {
				_, _, complete, reason := bindGroupMetadataWithReason(groups, b, mapper)
				if complete != tc.wantComplete || reason != tc.wantReason {
					t.Fatalf("bindGroupMetadataWithReason = (%v, %q), want (%v, %q)",
						complete, reason, tc.wantComplete, tc.wantReason)
				}
			}
		})
	}
}

func TestResolveEphemeralDraftTargetDatabase(t *testing.T) {
	saved := "shop_db"
	got, err := resolveEphemeralDraftTargetDatabase("", false, &saved)
	if err != nil || got != "shop_db" {
		t.Fatalf("omitted target_database = (%q, %v), want ('shop_db', nil)", got, err)
	}
	got, err = resolveEphemeralDraftTargetDatabase("", true, &saved)
	if err != nil || got != "" {
		t.Fatalf("explicit null target_database = (%q, %v), want ('', nil)", got, err)
	}
	got, err = resolveEphemeralDraftTargetDatabase("orders_db", true, &saved)
	if err != nil || got != "orders_db" {
		t.Fatalf("explicit override target_database = (%q, %v), want ('orders_db', nil)", got, err)
	}
	badSaved := "mysql"
	if _, err := resolveEphemeralDraftTargetDatabase("", false, &badSaved); err == nil {
		t.Fatal("expected system schema draft fallback to fail validation")
	}
}
