package application

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func TestResolveKnowledgeScopeUsesFrozenPhysicalObjects(t *testing.T) {
	mapper := privacy.NewRunMapper()
	prodSchema, _ := mapper.Token(privacy.KindSchema, "prod")
	auditSchema, _ := mapper.Token(privacy.KindSchema, "audit")
	orders, _ := mapper.Token(privacy.KindTable, "orders")
	quotedEvents, _ := mapper.ScopedToken(
		privacy.KindTable, privacy.ScopeCaseSensitive, "Events",
	)

	groups := []provider.AnonymizedGroup{
		{
			RepresentativeAnonymizedSQL: "select ...",
			MetadataContextComplete:     true,
			Objects: []provider.AnonymizedObjectReference{
				{
					Token: orders, Kind: "table", Relation: "read",
					SchemaToken: prodSchema,
				},
				{
					Token: quotedEvents, Kind: "table", Relation: "read",
					SchemaToken: auditSchema,
				},
				{
					Token: orders, Kind: "table", Relation: "read",
					SchemaToken: auditSchema,
				},
			},
		},
	}
	scope, complete, err := resolveKnowledgeScope(
		"ds-1", groups, bindingOf("prod", "audit"), mapper,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || !scope.PreciseScopes || !scope.ScopeComplete {
		t.Fatalf("complete frozen scope rejected: %+v complete=%v", scope, complete)
	}
	if len(scope.Objects) != 3 ||
		scope.Objects[0].SchemaName != "prod" ||
		scope.Objects[0].TableName != "orders" ||
		scope.Objects[1].SchemaName != "audit" ||
		scope.Objects[1].TableName != "Events" ||
		!scope.Objects[1].TableCaseSensitive ||
		scope.Objects[2].SchemaName != "audit" ||
		scope.Objects[2].TableName != "orders" {
		t.Fatalf("wrong physical object scope: %+v", scope.Objects)
	}

	table := &knowledge.Entry{
		ScopeType: knowledge.ScopeTable, DatasourceID: "ds-1",
		DatabaseName: "prod", TableName: "orders",
	}
	if !table.Matched(scope) {
		t.Fatal("resolved table did not match its knowledge entry")
	}
	table.DatabaseName = "audit"
	if !table.Matched(scope) {
		t.Fatal("same basename in the explicitly touched second schema did not match")
	}
	table.DatabaseName, table.TableName = "audit", "events"
	if table.Matched(scope) {
		t.Fatal("quoted case-sensitive table matched a case variant")
	}

}

func TestResolveKnowledgeScopeUsesUniqueDefaultSchema(t *testing.T) {
	mapper := privacy.NewRunMapper()
	orders, _ := mapper.Token(privacy.KindTable, "orders")
	scope, complete, err := resolveKnowledgeScope(
		"ds-1",
		[]provider.AnonymizedGroup{{
			RepresentativeAnonymizedSQL: "select * from orders",
			MetadataContextComplete:     true,
			Objects: []provider.AnonymizedObjectReference{{
				Token: orders, Kind: "table", Relation: "read",
			}},
		}},
		&stageSchemaBinding{
			defensible: true,
			schemas:    map[string]bool{"prod": true},
		},
		mapper,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || len(scope.Objects) != 1 ||
		scope.Objects[0].SchemaName != "prod" ||
		!scope.Objects[0].SchemaCaseSensitive {
		t.Fatalf("unique default schema was not bound: %+v complete=%v", scope, complete)
	}
}

func TestResolveKnowledgeScopeFailsClosedWhenBindingIsUnprovable(t *testing.T) {
	mapper := privacy.NewRunMapper()
	logicalSchema, _ := mapper.Token(privacy.KindSchema, "app")
	orders, _ := mapper.Token(privacy.KindTable, "orders")
	groups := []provider.AnonymizedGroup{{
		RepresentativeAnonymizedSQL: "drop table app.orders",
		MetadataContextComplete:     true,
		Objects: []provider.AnonymizedObjectReference{{
			Token: orders, Kind: "table", Relation: "target",
			SchemaToken: logicalSchema,
		}},
	}}

	scope, complete, err := resolveKnowledgeScope(
		"ds-1", groups,
		bindingOf("prod"), mapper,
	)
	if err != nil {
		t.Fatal(err)
	}
	if complete || scope.ScopeComplete {
		t.Fatalf("explicit logical qualifier must not become a knowledge scope: %+v", scope)
	}
	if len(scope.Objects) != 0 {
		t.Fatalf("unproven table leaked into scope: %+v", scope.Objects)
	}

	unknown := groups[0].Objects[0]
	unknown.SchemaToken = "schema_missing"
	scope, complete, err = resolveKnowledgeScope(
		"ds-1", []provider.AnonymizedGroup{{
			RepresentativeAnonymizedSQL: "drop table orders",
			MetadataContextComplete:     true,
			Objects:                     []provider.AnonymizedObjectReference{unknown},
		}},
		bindingOf("prod"), mapper,
	)
	if err != nil {
		t.Fatal(err)
	}
	if complete || scope.ScopeComplete {
		t.Fatalf("unknown schema token was accepted: %+v", scope)
	}
}

func TestResolveKnowledgeScopeRequiresCompleteObjectContext(t *testing.T) {
	mapper := privacy.NewRunMapper()
	orders, _ := mapper.Token(privacy.KindTable, "orders")
	scope, complete, err := resolveKnowledgeScope(
		"ds-1", []provider.AnonymizedGroup{{
			RepresentativeAnonymizedSQL: "select * from orders",
			MetadataContextComplete:     false,
			Objects: []provider.AnonymizedObjectReference{{
				Token: orders, Kind: "table", Relation: "read",
			}},
		}},
		bindingOf("prod"), mapper,
	)
	if err != nil {
		t.Fatal(err)
	}
	if complete || scope.ScopeComplete {
		t.Fatalf("incomplete object context must make knowledge scope partial: %+v", scope)
	}
}
