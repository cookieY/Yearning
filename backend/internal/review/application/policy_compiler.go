package application

import (
	"crypto/sha256"
	"encoding/hex"
	"html"
	"sort"
	"strings"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

// canonicalBuiltinSkillTextHashes pins the SHA-256 digests of (name + "\x00" + knowledge_text)
// for the 48 system-seeded built-in skills in 000009_builtin_skill_package / 000015_review_input_definition.
// Because canonical built-in skill names and template texts are static repository constants rather than
// tenant-authored prose, SQL keywords inside their names/templates (such as "primary" in
// builtin.ddl-require-primary-key, which collides with MySQL's mandatory PRIMARY index/constraint name,
// or "column"/"table"/"index"/"order"/"limit"/"where"/"default"/"null") cannot disclose tenant catalog
// existence. User-tunable parameter values on built-in skills are still checked against c.lexical.
var canonicalBuiltinSkillTextHashes = map[string]string{
	"builtin.ddl-allow-create-view":             "96dfe9c0735c5fb225f36b5dd41101ad11b30d2c01db2174033aaebbc0167356",
	"builtin.ddl-allow-partition":               "451f212ed176e17bd8f79c5a423560a2859ab490b2ae621878607a94112a9ab5",
	"builtin.ddl-autoincrement-init-one":        "adbe89bdcaa24034b5364fe583a6bc607b64844536a47ccdd177de28bb710be1",
	"builtin.ddl-autoincrement-unsigned":        "b00cc9157fd39aa13c3c4e8a257dc56764b9e61cbecdced4e5d3f90b16ed3082",
	"builtin.ddl-charset-allowlist":             "75cdb83680375de7c073ae473444749a19aaf4d3b8cb1100b990ad94a7684089",
	"builtin.ddl-collation-allowlist":           "244dc231278f64d6bbbc5634a5c5a3fa9b7e19c8819a6f9953404d1cf191c203",
	"builtin.ddl-column-default-check":          "f92d2fbacbd0fc162d9adebc6ff80c3d3c59925e496f9c2941d94a12c19920b7",
	"builtin.ddl-column-nullable-check":         "e6bc27609c88c57950d942a3714cafce229d5d18075ed66d293a7834d815a75b",
	"builtin.ddl-float-double-decimal":          "e1e9460bcd1a3c6b3bbc8d3334568d47b4c76379526d9ef8a31826f02975726d",
	"builtin.ddl-forbid-across-db-rename":       "08fd2e943a8793799c6f5253add2e93f5887e503ed37621c48485bbb1fa8b806",
	"builtin.ddl-forbid-column-position-change": "3a80c949e24d02938a6c1885088d492b17970425cba31aced3923138dd2d5b9a",
	"builtin.ddl-forbid-column-type-change":     "2ffa146b1074d2fbcefe99be7ffdbe2e15486a8db3086f68cae1a12a1c1191d3",
	"builtin.ddl-forbid-drop-database":          "a22f5c1ccc330d34f74e558f8461e838bae26335fbfc8ebad284d9ab6939ea09",
	"builtin.ddl-forbid-drop-other":             "d1e1c597627763e88a376620abdeb3df1ed9355a6d5c7c11abf6b5ce619306e2",
	"builtin.ddl-forbid-drop-table":             "4f7c1c755df6aac4d2951658918925bdc2be5f3351c7b372c9de25db46b25453",
	"builtin.ddl-forbid-foreign-key":            "29887f1f18de4d720a4c2c48061e538990f6cf52f4d2a11701527c21726b7016",
	"builtin.ddl-forbid-unnamed-index":          "3c8db3e95edfd0534b0bd055f92286387698ef790ef715697c120e238043abe5",
	"builtin.ddl-identifier-keyword":            "a78500027deb941f96c0d712b27ca61425d92f7da5e31c63a5fe8e9d5a608dfe",
	"builtin.ddl-implicit-type-conversion":      "82c4ab54d452270141f9b3bb608ba158b2ecf69b440076506920b3cc67530b5a",
	"builtin.ddl-index-name-prefix":             "e08b3fc31a6561b472f99446a72d22f37e30509e1f4393c4970b796162752da9",
	"builtin.ddl-max-affect-rows":               "e89690938fa566b4b5fa8b96cf6e06f76ee4cf242a4d244597432a73a73b16ce",
	"builtin.ddl-max-char-length":               "93d7a293fc8bf3e2cb175a6dc369556d7c83947f785f7bb43f4d9efb2ebef67b",
	"builtin.ddl-max-index-count":               "af9971e90800d40e731854c06095f7b112d24caaf567e7b765e8828542818644",
	"builtin.ddl-max-index-key-parts":           "90b239a63820e231a9f8b3e5bdecb341d3e4c53afa5d316781cb870f161fe7e2",
	"builtin.ddl-max-table-name-length":         "9760496ea87acdc69d90fce922eac62824dc59f06b2789eecdb92e3305bc44fe",
	"builtin.ddl-multi-alter":                   "5bcb68688c2fa4f0d98484fb2e76c2dc304bef9c5bd60bbadb61864fedb1d5da",
	"builtin.ddl-multi-ddl":                     "f39e3fa8f6017ab349efddeb9b42d1a910d1327e5959043f6627d046d98d3dc6",
	"builtin.ddl-pk-auto-increment":             "b407493aeecd811036fafa44aeeb5e714a81aeda03788f661b4b2ddb392cdc02",
	"builtin.ddl-pk-integer":                    "a890a38babc79b024f119e8a790db689c9714b1b239c2b4b3fdbd2e375553e18",
	"builtin.ddl-pk-named-id":                   "00fb7dc27feaae1b92810211c1aa31f75dc91da14c317efaf6ef313a778e37ca",
	"builtin.ddl-require-column-comments":       "3dcf0837f425a8241c459d572fb59b05f4304ef329572c68eb45386846e8a51f",
	"builtin.ddl-require-primary-key":           "50b7a9b8550e6e073c11a082b9accfc10314b3308778fc67fc94ee7b2ed0a17b",
	"builtin.ddl-require-table-comment":         "92e056dd836bb729eca2dfa35a39d8d8322b0ef2b6462c631a2ca63932b0be6a",
	"builtin.ddl-special-type":                  "e1064cad039fb7d268d9d6870053a7e4cfaa2f79370611bcbbf25b9d0909cd93",
	"builtin.ddl-view-forbid-select-star":       "733f10348bf94c69add3301cf2b6ec2bc3dbf4cef3d02df2f3caaa497366d629",
	"builtin.dml-forbid-limit":                  "c4278f7067e96a59c2e794a31b16867c8a6917688d4db9565dfb022ef4ccef2f",
	"builtin.dml-forbid-order-by":               "a94a24e2d1bab2a7f66b45cc9dcce0522cd699ae20f9eb3116a0124a2b2f759a",
	"builtin.dml-forbid-subselect":              "335f0af1856b117c4e90a272efb23080709892240baed9fc23c2944713dc237e",
	"builtin.dml-insert-explicit-columns":       "f6c358f2be2d81fd64ecad3a9221d91af4be4ab76fa0c20fa612bb6edb4665d6",
	"builtin.dml-insert-forbid-null":            "9c5c27fe7bdc389088239f61cf3980ad3fdc38cc7f5fa6cd0761561592153ae9",
	"builtin.dml-insert-max-batch-rows":         "0edfae605d7a50408516070f116f83d82acf03a84d0fcca237e1c46ed30aed04",
	"builtin.dml-require-where":                 "5c595851f0fda5d6438ec5635f2f0aa46a42c9d6c5b5c981e360e853a7433109",
	"builtin.dml-where-not-null-literal":        "f8759916e4efb12abb829574d65394da9c1afe1d0d2fb9cd56970c50fde35b21",
	"builtin.forbid-truncate":                   "83025b618677fc73e8a826bea2a7c9e083754aebc7f1cd1f2b3710411bd1ccb3",
	"builtin.max-affect-rows":                   "1046caa609887b316a1dd67784bdf6f25a06ced7c156e4cad6d1d507ad0ad667",
	"builtin.max-group-statement-count":         "191fbf13b6ef5a69edc9807a07b505c6dae581ee24bc4a45293625f7652e84eb",
	"builtin.max-in-value-count":                "a25c96839636fea68d35ef0a72d05a43178d2bbc895b1f96e657725a8caa318f",
	"builtin.max-statement-size":                "77e731428710d5d3ba93928276f17e78946acd47b13187e78553610516bec55f",
}

func isCanonicalBuiltinSkill(name, knowledgeText string) bool {
	expected, ok := canonicalBuiltinSkillTextHashes[name]
	if !ok {
		return false
	}
	sum := sha256.Sum256([]byte(name + "\x00" + knowledgeText))
	return hex.EncodeToString(sum[:]) == expected
}

// compiledPolicy is anonymous IR, assembled from the immutable policy snapshot
// before any renderer runs. Catalog names never enter a rendered prompt.
type compiledPolicy struct {
	skills    []agent.SkillInjection
	knowledge []*knowledge.Entry
	objects   []provider.AnonymizedObjectReference
	scope     metadataScope
}

type policyCompiler struct {
	mapper  *privacy.RunMapper
	binding *stageSchemaBinding
	index   map[policyCatalogIdentity]struct{}
	lexical []string
	out     compiledPolicy
	seen    map[string]bool
}

func compilePolicy(skills []agent.SkillInjection, entries []*knowledge.Entry,
	mapper *privacy.RunMapper, binding *stageSchemaBinding, catalog database.PolicyCatalog,
	databaseName string, prior metadataScope,
) (compiledPolicy, error) {
	fail := func() (compiledPolicy, error) { return compiledPolicy{}, ErrPolicyPrivacy }
	if mapper == nil {
		return fail()
	}
	schemas, err := policyCatalogAuthorizedSchemas(binding)
	if err != nil {
		return fail()
	}
	index, lexical, err := validateCompletePolicyCatalog(catalog, schemas, databaseName)
	if err != nil {
		return fail()
	}
	// RCP-20261031-POLICY-TEXT-GENERIC-COLUMN-ALLOWLIST: the text-collision lexical set excludes frozen
	// generic allowlist words of column class in the catalog (zero specificity, constituting no object-existence
	// disclosure). The exemption applies only to the text-check lexical set and must run before appending
	// mapper originals and bound schemas — statement identifiers and database names are never exempted;
	// token collision checks continue to use the full lexical set.
	exempt := genericColumnExemptionSet(catalog, databaseName)
	textLexical := exemptPolicyTextLexical(lexical, exempt)
	// SQL identities and logical aliases are also private, including names of absent objects that cannot
	// occur in the live catalog. RCP-20261101 owner ruling extension: generic column names in the submitted
	// statement itself (the 16-word allowlist such as id/name) have the exact same zero specificity as
	// same-named columns in the catalog — and are likewise exempted from text-collision checks; otherwise
	// the most common DDL shapes like create table t (id ...) would inevitably collide with the ddl-pk-named-id
	// skill text. Token collision checks (below) still use the full lexical set.
	for _, p := range mapper.Snapshot() {
		lexical = append(lexical, p.Original)
		// Statement identifiers are directly exempted against the frozen allowlist (independent of catalog hits):
		// id/name columns in submitted SQL have the same zero specificity as same-named catalog columns, and
		// stable skill names (such as builtin.ddl-index-name-prefix) inherently contain generic words. Token
		// collision checks still use the full lexical set.
		if _, generic := genericPolicyColumnNames[strings.ToLower(p.Original)]; !generic {
			textLexical = append(textLexical, p.Original)
		}
	}
	for schema := range binding.schemas {
		lexical = append(lexical, schema)
		textLexical = append(textLexical, schema)
	}
	c := &policyCompiler{mapper: mapper, binding: binding, index: index, lexical: textLexical,
		out: compiledPolicy{scope: clonePolicyMetadataScope(prior)}, seen: map[string]bool{}}
	for _, sk := range skills {
		if agent.ValidatePolicyParameters(sk.KnowledgeText, sk.Parameters) != nil {
			return fail()
		}
		next := sk
		if isCanonicalBuiltinSkill(sk.Name, sk.KnowledgeText) {
			if err := c.validateParameterValues(sk.Parameters); err != nil {
				return fail()
			}
			next.Name = sk.Name
			next.KnowledgeText = agent.RenderParameters(sk.KnowledgeText, sk.Parameters)
		} else {
			next.Name, err = c.text(sk.Name)
			if err != nil {
				return fail()
			}
			next.KnowledgeText, err = c.text(agent.RenderParameters(sk.KnowledgeText, sk.Parameters))
			if err != nil {
				return fail()
			}
		}
		next.Parameters = nil // Rendering must not interpret compiled values a second time.
		c.out.skills = append(c.out.skills, next)
	}
	for _, entry := range entries {
		if entry == nil || agent.ValidatePolicyParameters(entry.Definition.KnowledgeText, nil) != nil {
			return fail()
		}
		next := *entry
		next.Name, err = c.text(entry.Name)
		if err != nil {
			return fail()
		}
		next.Purpose, err = c.text(entry.Purpose)
		if err != nil {
			return fail()
		}
		next.Definition = entry.Definition
		next.Definition.KnowledgeText, err = c.text(entry.Definition.KnowledgeText)
		if err != nil {
			return fail()
		}
		c.out.knowledge = append(c.out.knowledge, &next)
	}
	if policyMapperTokenCollidesWithCatalog(mapper, lexical) {
		return fail()
	}
	return c.out, nil
}

func (c *policyCompiler) validateParameterValues(params map[string]any) error {
	for _, raw := range params {
		switch v := raw.(type) {
		case string:
			if _, err := c.text(v); err != nil {
				return err
			}
		case []string:
			for _, item := range v {
				if _, err := c.text(item); err != nil {
					return err
				}
			}
		case []any:
			for _, elem := range v {
				if item, ok := elem.(string); ok {
					if _, err := c.text(item); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (c *policyCompiler) text(value string) (string, error) {
	// Resolve typed JSON identities exactly before decoding prose. Catalog
	// names may themselves contain HTML entity syntax; decoding those names
	// would silently change the referenced physical object.
	out, err := privacy.RewritePolicyReferences(value, c.resolve)
	if err != nil {
		return "", ErrPolicyPrivacy
	}
	for i := 0; i < 8; i++ {
		decoded := html.UnescapeString(out)
		if decoded == out {
			break
		}
		out = decoded
	}
	if html.UnescapeString(out) != out || strings.Contains(out, "{{") || strings.Contains(out, "}}") || policyTextLeaksCatalogIdentifier(out, c.lexical) {
		return "", ErrPolicyPrivacy
	}
	return out, nil
}

func (c *policyCompiler) resolve(ref privacy.PolicyObjectReference) (string, error) {
	identity := policyCatalogIdentity{kind: database.PolicyCatalogKind(ref.Kind), schema: ref.Schema, relation: ref.Relation, name: ref.Name}
	switch ref.Kind {
	case privacy.PolicyReferenceSchema:
		identity.name = ref.Schema
	case privacy.PolicyReferenceTable, privacy.PolicyReferenceView:
		identity.relation = ""
		identity.name = ref.Relation
	}
	if _, ok := c.index[identity]; !ok {
		return "", ErrPolicyPrivacy
	}
	// These references have no complete, relation-aware business fact tool yet.
	// Inventory presence is privacy proof, not schema/relation/constraint
	// validity. In particular, schema_metadata requires a table and the runtime
	// contract forbids schema-wide discovery. Fail before any Provider attempt
	// rather than later promoting a model-only mandatory finding to Critical.
	if ref.Kind == privacy.PolicyReferenceSchema ||
		ref.Kind == privacy.PolicyReferenceView ||
		ref.Kind == privacy.PolicyReferenceConstraint {
		return "", ErrPolicyPrivacy
	}
	st := c.schemaToken(ref.Schema)
	if st == "" {
		return "", ErrPolicyPrivacy
	}
	c.out.scope.schemas[st] = true
	c.add(provider.AnonymizedObjectReference{Kind: "schema", Token: st, SchemaToken: st, Relation: "reference"})
	// Child references require an exact base-table parent in the same catalog.
	if _, ok := c.index[policyCatalogIdentity{kind: database.PolicyCatalogTable, schema: ref.Schema, name: ref.Relation}]; !ok {
		return "", ErrPolicyPrivacy
	}
	st, tt := c.tableTokens(ref.Schema, ref.Relation, st)
	if tt == "" {
		return "", ErrPolicyPrivacy
	}
	if c.out.scope.objects[st] == nil {
		c.out.scope.objects[st] = map[string]bool{}
	}
	c.out.scope.objects[st][tt] = true
	c.add(provider.AnonymizedObjectReference{Kind: "table", Token: tt, SchemaToken: st, Relation: "reference"})
	if ref.Kind == privacy.PolicyReferenceTable {
		return st + "." + tt, nil
	}
	token := physicalPolicyToken(c.mapper, privacy.IdentifierKind(ref.Kind), ref.Name)
	if token == "" {
		return "", ErrPolicyPrivacy
	}
	c.add(provider.AnonymizedObjectReference{Kind: string(ref.Kind), Token: token, SchemaToken: st, ParentToken: tt, Relation: "reference"})
	return st + "." + tt + "." + token, nil
}

func (c *policyCompiler) add(o provider.AnonymizedObjectReference) {
	key := o.Kind + "\x00" + o.SchemaToken + "\x00" + o.ParentToken + "\x00" + o.Token
	if !c.seen[key] {
		c.seen[key] = true
		c.out.objects = append(c.out.objects, o)
	}
}

func (c *policyCompiler) schemaToken(physical string) string {
	for _, st := range sortedPolicyKeys(c.out.scope.schemas) {
		kind, name, ok := c.mapper.Original(st)
		actual, allowed := c.binding.physicalSchema(name)
		if ok && kind == privacy.KindSchema && allowed && actual == physical {
			return st
		}
	}
	return physicalPolicyToken(c.mapper, privacy.KindSchema, physical)
}

func (c *policyCompiler) tableTokens(physical, table, st string) (string, string) {
	// Prefer a complete SQL member already authorized by bindGroupMetadata.
	// A schema edge distinguishes equal basenames; basename alone never binds a
	// table to a physical target. Exact spelling avoids guessing case semantics.
	for _, schema := range sortedPolicyObjectSchemas(c.out.scope.objects) {
		_, name, _ := c.mapper.Original(schema)
		actual, ok := c.binding.physicalSchema(name)
		if !ok || actual != physical {
			continue
		}
		for _, tt := range sortedPolicyKeys(c.out.scope.objects[schema]) {
			kind, name, ok := c.mapper.Original(tt)
			if ok && kind == privacy.KindTable && name == table {
				return schema, tt
			}
		}
	}
	return st, physicalPolicyToken(c.mapper, privacy.KindTable, table)
}

func physicalPolicyToken(m *privacy.RunMapper, kind privacy.IdentifierKind, name string) string {
	for _, p := range m.Snapshot() {
		if p.Kind == kind && p.Original == name && isPhysicalSQLScope(p.Scope) {
			return p.Token
		}
	}
	token, _ := m.ScopedToken(kind, privacy.ScopeCaseSensitive, name)
	return token
}

func sortedPolicyKeys(m map[string]bool) []string {
	out := []string{}
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
func sortedPolicyObjectSchemas(m map[string]map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
