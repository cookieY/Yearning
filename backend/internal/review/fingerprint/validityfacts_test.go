package fingerprint

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func analyzeValidity(t *testing.T, sql string) (Result, StatementValidity) {
	t.Helper()
	res := analyzeObjects(t, sql, privacy.NewRunMapper())
	return res, res.Validity
}

func analyzeValidityWithEngine(t *testing.T, engine, sql string) (Result, StatementValidity) {
	t.Helper()
	mode := engine
	switch engine {
	case "tidb", "oceanbase":
		mode = "mysql"
	case "polardb":
		mode = "postgresql"
	}
	res, err := testEngine(t).Analyze(
		Profile{Engine: engine, Mode: mode},
		[]byte(sql),
		privacy.NewRunMapper(),
	)
	if err != nil {
		t.Fatalf("analyze %q with %s: %v", sql, engine, err)
	}
	return res, res.Validity
}

func extractValidityFromParser(t *testing.T, sql string) StatementValidity {
	t.Helper()
	lex, err := Tokenize([]byte(sql), Profile{Engine: "postgresql", Mode: "postgresql"}.Flags())
	if err != nil {
		t.Fatalf("tokenize %q: %v", sql, err)
	}
	walk := walkStatement(lex.Tokens)
	facts, err := extractStatementValidity(
		lex.Tokens,
		walk,
		privacy.NewRunMapper(),
		"postgresql",
	)
	if err != nil {
		t.Fatalf("extract validity for %q: %v", sql, err)
	}
	return facts
}

func objectByKindRelation(t *testing.T, res Result, kind string, relation Relation) ObjectReference {
	t.Helper()
	object, ok := findObject(res, kind, relation)
	if !ok {
		t.Fatalf("missing %s/%s object: %+v", kind, relation, res.Objects)
	}
	return object
}

func validityObject(ref ObjectReference) ValidityObject {
	return ValidityObject{
		Kind:        ValidityObjectKind(ref.Kind),
		Token:       ref.Token,
		SchemaToken: ref.SchemaToken,
		ParentToken: ref.ParentToken,
	}
}

func hasRequirement(facts StatementValidity, object ValidityObject, state ValidityRequirementState) bool {
	for _, requirement := range facts.Requirements {
		if requirement.Object == object && requirement.State == state {
			return true
		}
	}
	return false
}

func hasEffect(facts StatementValidity, action ValidityEffectAction, object ValidityObject) bool {
	for _, effect := range facts.Effects {
		if effect.Action == action && effect.Object == object {
			return true
		}
	}
	return false
}

func TestValidityCreateAndDropConditions(t *testing.T) {
	tests := []struct {
		name            string
		sql             string
		operation       ValidityOperation
		required        ValidityRequirementState
		condition       ValidityCondition
		action          ValidityEffectAction
		effectCondition ValidityCondition
		tableRelation   Relation
	}{
		{
			name:            "unconditional create",
			sql:             "CREATE TABLE app.orders (id int)",
			operation:       ValidityOperationCreate,
			required:        ValidityRequirementAbsent,
			condition:       ValidityConditionNone,
			action:          ValidityEffectCreate,
			effectCondition: ValidityConditionNone,
			tableRelation:   RelationDefine,
		},
		{
			name:            "conditional create",
			sql:             "CREATE TABLE IF NOT EXISTS app.orders (id int)",
			operation:       ValidityOperationCreate,
			required:        ValidityRequirementAbsent,
			condition:       ValidityConditionIfNotExists,
			action:          ValidityEffectCreate,
			effectCondition: ValidityConditionIfNotExists,
			tableRelation:   RelationDefine,
		},
		{
			name:            "unconditional drop",
			sql:             "DROP TABLE app.orders",
			operation:       ValidityOperationDrop,
			required:        ValidityRequirementPresent,
			condition:       ValidityConditionNone,
			action:          ValidityEffectDrop,
			effectCondition: ValidityConditionNone,
			tableRelation:   RelationTarget,
		},
		{
			name:            "conditional drop",
			sql:             "DROP TABLE IF EXISTS app.orders",
			operation:       ValidityOperationDrop,
			required:        ValidityRequirementPresent,
			condition:       ValidityConditionIfExists,
			action:          ValidityEffectDrop,
			effectCondition: ValidityConditionIfExists,
			tableRelation:   RelationTarget,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, facts := analyzeValidity(t, tt.sql)
			if !facts.Complete {
				t.Fatalf("validity must be complete for %q: %+v", tt.sql, facts)
			}
			if facts.Operation != tt.operation {
				t.Fatalf("operation = %q, want %q", facts.Operation, tt.operation)
			}
			table := objectByKindRelation(t, res, "table", tt.tableRelation)
			wantObject := validityObject(table)
			if !hasRequirement(facts, wantObject, tt.required) {
				t.Fatalf("missing %q requirement for %+v: %+v", tt.required, wantObject, facts.Requirements)
			}
			for _, requirement := range facts.Requirements {
				if requirement.Object == wantObject && requirement.State == tt.required &&
					requirement.Condition != tt.condition {
					t.Fatalf("requirement condition = %q, want %q", requirement.Condition, tt.condition)
				}
			}
			if !hasEffect(facts, tt.action, wantObject) {
				t.Fatalf("missing %q effect for %+v: %+v", tt.action, wantObject, facts.Effects)
			}
			for _, effect := range facts.Effects {
				if effect.Action == tt.action && effect.Object == wantObject &&
					effect.Condition != tt.effectCondition {
					t.Fatalf("effect condition = %q, want %q", effect.Condition, tt.effectCondition)
				}
			}
		})
	}
}

func TestValidityCreateCapturesColumnDefinitions(t *testing.T) {
	res, facts := analyzeValidity(t, "CREATE TABLE app.orders (id int, amount decimal(10,2))")
	table := objectByKindRelation(t, res, "table", RelationDefine)
	if !facts.DefinitionsComplete {
		t.Fatalf("normal CREATE TABLE definitions must be complete: %+v", facts)
	}
	columns := 0
	for _, object := range res.Objects {
		if object.Kind == "column" && object.Relation == RelationDefine {
			columns++
			if !hasEffect(facts, ValidityEffectCreate, validityObject(object)) {
				t.Fatalf("missing create-column effect for %+v: %+v", object, facts.Effects)
			}
		}
	}
	if columns != 2 {
		t.Fatalf("column definitions = %d, want 2", columns)
	}
	if !hasEffect(facts, ValidityEffectCreate, validityObject(table)) {
		t.Fatalf("missing create-table effect: %+v", facts.Effects)
	}
}

func TestValidityCreateTableConstraintsDoNotRequireDeclaredColumns(t *testing.T) {
	tests := []struct {
		name   string
		engine string
		sql    string
	}{
		{
			name:   "primary key",
			engine: "postgresql",
			sql:    "CREATE TABLE app.orders (id int, PRIMARY KEY (id))",
		},
		{
			name:   "unnamed unique",
			engine: "postgresql",
			sql:    "CREATE TABLE app.orders (id int, UNIQUE (id))",
		},
		{
			name:   "named unique",
			engine: "postgresql",
			sql:    "CREATE TABLE app.orders (id int, CONSTRAINT uq_orders UNIQUE (id))",
		},
		{
			name:   "mysql index",
			engine: "mysql",
			sql:    "CREATE TABLE app.orders (id int, INDEX idx_id (id))",
		},
		{
			name:   "check expression",
			engine: "postgresql",
			sql:    "CREATE TABLE app.orders (id int, CHECK (id > 0))",
		},
		{
			name:   "foreign key local column",
			engine: "postgresql",
			sql:    "CREATE TABLE app.orders (incoming_id int, FOREIGN KEY (incoming_id) REFERENCES app.incoming(id))",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, facts := analyzeValidityWithEngine(t, tt.engine, tt.sql)
			if !facts.Complete {
				t.Fatalf("CREATE TABLE constraint facts = %+v, want complete", facts)
			}
			definedColumns := map[ValidityObject]bool{}
			for _, effect := range facts.Effects {
				if effect.Action == ValidityEffectCreate &&
					effect.Object.Kind == ValidityObjectColumn {
					definedColumns[effect.Object] = true
				}
			}
			if len(definedColumns) == 0 {
				t.Fatalf("missing declared column effects: %+v", facts.Effects)
			}
			for _, requirement := range facts.Requirements {
				if requirement.State == ValidityRequirementPresent &&
					definedColumns[requirement.Object] {
					t.Fatalf(
						"constraint reference was treated as a pre-existing declared column: %+v",
						requirement,
					)
				}
			}
		})
	}
}

func TestValidityCreateTableSelfForeignKeyUsesDeclaredColumn(t *testing.T) {
	_, facts := analyzeValidityWithEngine(
		t,
		"postgresql",
		"CREATE TABLE app.orders ("+
			"id int PRIMARY KEY, "+
			"parent_id int, "+
			"FOREIGN KEY (parent_id) REFERENCES app.orders(id)"+
			")",
	)
	if !facts.Complete {
		t.Fatalf("self-FK validity facts = %+v, want complete", facts)
	}

	definedColumns := map[ValidityObject]bool{}
	for _, effect := range facts.Effects {
		if effect.Action == ValidityEffectCreate &&
			effect.Object.Kind == ValidityObjectColumn {
			definedColumns[effect.Object] = true
		}
	}
	if len(definedColumns) != 2 {
		t.Fatalf("declared columns = %d, want 2: %+v", len(definedColumns), facts.Effects)
	}
	for _, requirement := range facts.Requirements {
		if requirement.State == ValidityRequirementPresent &&
			definedColumns[requirement.Object] {
			t.Fatalf(
				"self-FK referenced a same-statement column as pre-existing: %+v",
				requirement,
			)
		}
	}
}

func TestValidityCreateTableUnresolvedSelfForeignKeyIsUnknown(t *testing.T) {
	_, facts := analyzeValidityWithEngine(
		t,
		"postgresql",
		"CREATE TABLE app.orders ("+
			"parent_id int, "+
			"FOREIGN KEY (parent_id) REFERENCES app.orders(missing_id)"+
			")",
	)
	if facts.Complete || facts.Unsupported != "unresolved_create_table_self_reference" {
		t.Fatalf(
			"unresolved self-FK validity = %+v, want fail-closed unknown",
			facts,
		)
	}
}

func TestValidityCreateTableSameNameOtherSchemaStaysExternal(t *testing.T) {
	_, facts := analyzeValidityWithEngine(
		t,
		"postgresql",
		"CREATE TABLE app.orders ("+
			"id int PRIMARY KEY, "+
			"audit_id int, "+
			"FOREIGN KEY (audit_id) REFERENCES audit.orders(id)"+
			")",
	)
	if !facts.Complete {
		t.Fatalf("cross-schema FK validity facts = %+v, want complete", facts)
	}

	var externalTables, externalColumns int
	for _, requirement := range facts.Requirements {
		if requirement.State != ValidityRequirementPresent {
			continue
		}
		switch requirement.Object.Kind {
		case ValidityObjectTable:
			externalTables++
		case ValidityObjectColumn:
			externalColumns++
		}
	}
	if externalTables != 1 || externalColumns != 1 {
		t.Fatalf(
			"cross-schema FK requirements = %+v, want one external table and column",
			facts.Requirements,
		)
	}
}

func TestValidityExpressionIndexesAreNotTrustedAsColumns(t *testing.T) {
	tests := []struct {
		name         string
		engine       string
		sql          string
		wantComplete bool
	}{
		{
			name:   "function expression",
			engine: "postgresql",
			sql:    "CREATE INDEX users_lower_email_idx ON app.users (lower(email))",
		},
		{
			name:   "cast expression",
			engine: "postgresql",
			sql:    "CREATE INDEX users_email_text_idx ON app.users ((email)::text)",
		},
		{
			name:   "operator class",
			engine: "postgresql",
			sql:    "CREATE INDEX users_email_pattern_idx ON app.users (email text_pattern_ops)",
		},
		{
			name:   "multiple expressions",
			engine: "postgresql",
			sql:    "CREATE INDEX users_lower_created_idx ON app.users (lower(email), created_at)",
		},
		{
			name:         "include columns",
			engine:       "postgresql",
			sql:          "CREATE INDEX users_email_idx ON app.users (email) INCLUDE (created_at)",
			wantComplete: true,
		},
		{
			name:         "partial index",
			engine:       "postgresql",
			sql:          "CREATE INDEX active_users_email_idx ON app.users (email) WHERE active",
			wantComplete: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, facts := analyzeValidityWithEngine(t, tt.engine, tt.sql)
			if facts.Complete != tt.wantComplete {
				t.Fatalf("expression index facts = %+v, want complete=%v", facts, tt.wantComplete)
			}
			if !facts.Complete && facts.Unsupported == "" {
				t.Fatalf("incomplete expression index lacks a controlled reason: %+v", facts)
			}
		})
	}
}

func TestValidityDMLRequiresPhysicalTablesPresent(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		operation ValidityOperation
	}{
		{name: "select", sql: "SELECT id FROM app.orders", operation: ValidityOperationSelect},
		{name: "insert", sql: "INSERT INTO app.orders (id) VALUES (1)", operation: ValidityOperationInsert},
		{name: "update", sql: "UPDATE app.orders SET id = 2", operation: ValidityOperationUpdate},
		{name: "delete", sql: "DELETE FROM app.orders", operation: ValidityOperationDelete},
		{name: "truncate", sql: "TRUNCATE TABLE app.orders", operation: ValidityOperationTruncate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, facts := analyzeValidity(t, tt.sql)
			if !facts.Complete {
				t.Fatalf("validity must be complete for %q: %+v", tt.sql, facts)
			}
			if facts.Operation != tt.operation {
				t.Fatalf("operation = %q, want %q", facts.Operation, tt.operation)
			}
			found := false
			for _, requirement := range facts.Requirements {
				if requirement.Object.Kind != ValidityObjectTable ||
					requirement.State != ValidityRequirementPresent {
					continue
				}
				for _, object := range res.Objects {
					if object.Kind == "table" && validityObject(object) == requirement.Object {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("missing present table requirement: %+v objects=%+v", facts.Requirements, res.Objects)
			}
		})
	}
}

func TestValidityUnsupportedMergeIsParserFallback(t *testing.T) {
	res, facts := analyzeValidity(
		t,
		"MERGE INTO app.orders o USING app.incoming i ON o.id = i.id WHEN MATCHED THEN UPDATE SET id = i.id",
	)
	if res.Quality != QualityLexer {
		t.Fatalf("MERGE quality = %q, want lexer fallback", res.Quality)
	}
	if facts.Complete || facts.Unsupported != "parser_fallback" {
		t.Fatalf("MERGE validity = %+v, want an explicit parser fallback", facts)
	}
}

func TestValidityMultiActionAlterIsConservativelyIncomplete(t *testing.T) {
	tests := []struct {
		name   string
		engine string
		sql    string
	}{
		{
			name:   "add then drop same column",
			engine: "mysql",
			sql:    "ALTER TABLE app.orders ADD COLUMN quantity int, DROP COLUMN quantity",
		},
		{
			name:   "rename then change",
			engine: "postgresql",
			sql:    "ALTER TABLE app.orders RENAME COLUMN old_name TO new_name, CHANGE new_name newer_name int",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, facts := analyzeValidityWithEngine(t, tt.engine, tt.sql)
			if facts.Complete || facts.Unsupported == "" {
				t.Fatalf("multi-action ALTER validity = %+v, want conservative unknown", facts)
			}
		})
	}
}

func TestValidityAlterConstraintsAreConservativelyIncomplete(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{
			name: "add",
			sql:  "ALTER TABLE app.orders ADD CONSTRAINT fk_orders FOREIGN KEY (id) REFERENCES app.incoming(id)",
		},
		{
			name: "drop",
			sql:  "ALTER TABLE app.orders DROP CONSTRAINT IF EXISTS fk_orders",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, facts := analyzeValidityWithEngine(t, "postgresql", tt.sql)
			if facts.Complete || facts.Unsupported != "unsupported_alter_constraint" {
				t.Fatalf("constraint ALTER validity = %+v, want conservative unknown", facts)
			}
		})
	}
}

func TestValidityEngineSpecificConditionalDDL(t *testing.T) {
	tests := []struct {
		name       string
		engine     string
		sql        string
		complete   bool
		condition  ValidityCondition
		wantReason string
	}{
		{
			name:       "mysql add column conditional",
			engine:     "mysql",
			sql:        "ALTER TABLE app.orders ADD COLUMN IF NOT EXISTS quantity int",
			wantReason: "engine_unsupported_conditional_ddl",
		},
		{
			name:       "mysql drop column conditional",
			engine:     "mysql",
			sql:        "ALTER TABLE app.orders DROP COLUMN IF EXISTS quantity",
			wantReason: "engine_unsupported_conditional_ddl",
		},
		{
			name:       "mysql create index conditional",
			engine:     "mysql",
			sql:        "CREATE INDEX IF NOT EXISTS idx_quantity ON app.orders (quantity)",
			wantReason: "engine_unsupported_conditional_ddl",
		},
		{
			name:       "mysql drop index conditional",
			engine:     "mysql",
			sql:        "DROP INDEX IF EXISTS idx_quantity",
			wantReason: "engine_unsupported_conditional_ddl",
		},
		{
			name:      "mysql drop table conditional",
			engine:    "mysql",
			sql:       "DROP TABLE IF EXISTS app.orders",
			complete:  true,
			condition: ValidityConditionIfExists,
		},
		{
			name:      "postgres add column conditional",
			engine:    "postgresql",
			sql:       "ALTER TABLE app.orders ADD COLUMN IF NOT EXISTS quantity int",
			complete:  true,
			condition: ValidityConditionIfNotExists,
		},
		{
			name:      "postgres drop column conditional",
			engine:    "postgresql",
			sql:       "ALTER TABLE app.orders DROP COLUMN IF EXISTS quantity",
			complete:  true,
			condition: ValidityConditionIfExists,
		},
		{
			name:      "postgres create index conditional",
			engine:    "postgresql",
			sql:       "CREATE INDEX IF NOT EXISTS idx_quantity ON app.orders (quantity)",
			complete:  true,
			condition: ValidityConditionIfNotExists,
		},
		{
			name:      "postgres drop index conditional",
			engine:    "postgresql",
			sql:       "DROP INDEX IF EXISTS idx_quantity",
			complete:  true,
			condition: ValidityConditionIfExists,
		},
		{
			name:      "postgres drop table conditional",
			engine:    "postgresql",
			sql:       "DROP TABLE IF EXISTS app.orders",
			complete:  true,
			condition: ValidityConditionIfExists,
		},
		{
			name:       "vendor version undetermined",
			engine:     "tidb",
			sql:        "ALTER TABLE app.orders ADD COLUMN IF NOT EXISTS quantity int",
			wantReason: "engine_unsupported_conditional_ddl",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, facts := analyzeValidityWithEngine(t, tt.engine, tt.sql)
			if facts.Complete != tt.complete {
				t.Fatalf("validity = %+v, complete want %v", facts, tt.complete)
			}
			if !tt.complete {
				if facts.Unsupported != tt.wantReason {
					t.Fatalf("unsupported reason = %q, want %q", facts.Unsupported, tt.wantReason)
				}
				return
			}
			found := false
			for _, requirement := range facts.Requirements {
				if requirement.Condition == tt.condition {
					found = true
				}
			}
			if !found {
				t.Fatalf("conditional requirement %q missing: %+v", tt.condition, facts.Requirements)
			}
		})
	}
}

func TestValidityRenameUsesPairedEffects(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		kind ValidityObjectKind
	}{
		{
			name: "table",
			sql:  "ALTER TABLE app.orders RENAME TO app.archived_orders",
			kind: ValidityObjectTable,
		},
		{
			name: "column",
			sql:  "ALTER TABLE app.orders RENAME COLUMN old_name TO new_name",
			kind: ValidityObjectColumn,
		},
		{
			name: "index",
			sql:  "ALTER TABLE app.orders RENAME INDEX old_idx TO new_idx",
			kind: ValidityObjectIndex,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, facts := analyzeValidity(t, tt.sql)
			if !facts.Complete {
				t.Fatalf("rename validity must be complete: %+v", facts)
			}
			var rename *ValidityEffect
			for i := range facts.Effects {
				if facts.Effects[i].Action == ValidityEffectRename {
					rename = &facts.Effects[i]
					break
				}
			}
			if rename == nil || rename.Replacement == nil {
				t.Fatalf("missing paired rename effect: %+v", facts.Effects)
			}
			if rename.Object.Kind != tt.kind || rename.Replacement.Kind != tt.kind {
				t.Fatalf("rename kinds = %q/%q, want %q", rename.Object.Kind, rename.Replacement.Kind, tt.kind)
			}
			if rename.Object.Token == rename.Replacement.Token {
				t.Fatalf("rename old/new tokens must differ: %+v", rename)
			}
			if !hasRequirement(facts, rename.Object, ValidityRequirementPresent) ||
				!hasRequirement(facts, *rename.Replacement, ValidityRequirementAbsent) {
				t.Fatalf("rename prerequisites missing: %+v", facts.Requirements)
			}
		})
	}
}

func TestValidityCTEIsNotAPhysicalTableRequirement(t *testing.T) {
	res, facts := analyzeValidity(t, "WITH recent AS (SELECT id FROM app.orders) SELECT id FROM recent")
	if !facts.Complete {
		t.Fatalf("CTE validity must be complete: %+v", facts)
	}
	var physicalOrders ObjectReference
	for _, object := range res.Objects {
		if object.Kind == "table" && object.Relation != RelationDefine {
			physicalOrders = object
			break
		}
	}
	if physicalOrders.Token == "" {
		t.Fatalf("physical table missing from object graph: %+v", res.Objects)
	}
	if !hasRequirement(facts, validityObject(physicalOrders), ValidityRequirementPresent) {
		t.Fatalf("physical CTE input missing requirement: %+v", facts.Requirements)
	}
	for _, requirement := range facts.Requirements {
		if requirement.Object.Kind == ValidityObjectTable && requirement.Object.Token == "cte_1" {
			t.Fatalf("CTE leaked into physical requirements: %+v", facts.Requirements)
		}
	}
}

func TestValidityUnsupportedSyntaxIsExplicitlyIncomplete(t *testing.T) {
	tests := []string{
		"EXPLAIN ANALYZE DELETE FROM app.orders",
		"GRANT SELECT ON app.orders TO app_reader",
	}
	for _, sql := range tests {
		t.Run(sql, func(t *testing.T) {
			res := analyzeObjects(t, sql, privacy.NewRunMapper())
			if res.Validity.Complete {
				t.Fatalf("unsupported/incomplete syntax must not claim complete validity: %+v", res.Validity)
			}
			if res.Validity.Unsupported == "" {
				t.Fatalf("incomplete validity must carry a controlled reason: %+v", res.Validity)
			}
		})
	}
}

func TestValidityBareViewStaysIncompleteAsUnsupportedView(t *testing.T) {
	facts := extractValidityFromParser(t, "CREATE VIEW app.v AS SELECT id FROM app.orders")
	if facts.Complete || facts.Unsupported != "unsupported_view" {
		t.Fatalf("view validity = %+v, want unsupported_view", facts)
	}
}

func TestValidityFactsContainOnlyAnonymousIdentifiers(t *testing.T) {
	_, facts := analyzeValidity(t, "DROP TABLE private_schema.private_orders")
	raw, err := json.Marshal(facts)
	if err != nil {
		t.Fatalf("marshal validity facts: %v", err)
	}
	for _, secret := range []string{"private_schema", "private_orders"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("validity facts leak %q: %s", secret, raw)
		}
	}
}
