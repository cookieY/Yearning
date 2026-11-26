package application

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

type postgresAnalyzedStatement struct {
	result     fingerprint.Result
	objects    []fingerprint.ObjectReference
	pathUnsafe bool
	// callables lists the unqualified routine and operator references whose
	// catalog identity must be proven on the frozen effective path.
	callables []postgresCallableRef
	// callablesOK is false when the statement could not be tokenized into a
	// complete callable reference set.
	callablesOK bool
}

// postgresCallableRequests flattens per-statement callable refs into one
// deduplicated request set for the catalog batch.
func postgresCallableRequests(
	stmts []postgresAnalyzedStatement,
) ([]database.PostgresCallableRequest, bool) {
	out := make([]database.PostgresCallableRequest, 0)
	seen := map[string]bool{}
	for i := range stmts {
		if !stmts[i].callablesOK {
			return nil, false
		}
		for _, ref := range stmts[i].callables {
			key := database.PostgresCallablePayloadKey(ref.Kind, ref.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, database.PostgresCallableRequest{Kind: ref.Kind, Name: ref.Name})
		}
	}
	return out, true
}

// postgresStatementPathUnsafe identifies statement forms whose session-local
// relation, search_path, or operator semantics cannot be replayed from the
// frozen baseline. Passing these ordinals through the batch binder as
// unsupported keeps the review partial instead of presenting incomplete
// semantics as exact.
func postgresStatementPathUnsafe(statementSQL string) bool {
	lexed, err := fingerprint.Tokenize(
		[]byte(statementSQL),
		fingerprint.Profile{Mode: "postgresql"}.Flags(),
	)
	if err != nil {
		return true
	}
	return postgresStatementPathUnsafeFromTokens(lexed.Tokens)
}

func postgresStatementPathUnsafeFromTokens(tokens []fingerprint.Token) bool {
	if hasAdjacentUnrecognizedPostgresOperator(tokens) {
		return true
	}
	word := func(index int, want string) bool {
		return index >= 0 && index < len(tokens) && tokens[index].IsWord(want)
	}
	if len(tokens) == 0 {
		return false
	}
	switch {
	case tokens[0].IsWord("create"):
		for i := 1; i < len(tokens) && tokens[i].Kind == fingerprint.TokWord; i++ {
			switch tokens[i].Text {
			case "global", "local", "temporary", "temp":
				return true
			case "or", "replace", "unlogged", "unique":
				continue
			default:
				return false
			}
		}
	case tokens[0].IsWord("select"):
		for i := 1; i+1 < len(tokens); i++ {
			if tokens[i].IsWord("into") || tokens[i].IsWord("set_config") {
				return true
			}
			if word(i, "pg_catalog") && i+2 < len(tokens) &&
				tokens[i+1].IsPunct(".") && tokens[i+2].IsWord("set_config") {
				return true
			}
		}
	case tokens[0].IsWord("set"):
		index := 1
		if word(index, "session") || word(index, "local") {
			index++
		}
		if word(index, "search_path") || word(index, "role") ||
			word(index, "session") || word(index, "authorization") {
			return true
		}
	case tokens[0].IsWord("reset"):
		return word(1, "search_path") || word(1, "all")
	case tokens[0].IsWord("discard"):
		return word(1, "all") || word(1, "temp") || word(1, "temporary")
	}
	return false
}

// hasAdjacentUnrecognizedPostgresOperator rejects operator spellings that the
// shared tokenizer split into adjacent punctuation. PostgreSQL permits custom
// operator names such as @@; treating two @ tokens as syntax would otherwise
// mark the statement semantically complete without resolving the operator.
func hasAdjacentUnrecognizedPostgresOperator(tokens []fingerprint.Token) bool {
	const postgresOperatorChars = "+-*/<>=~!@#%^&|`?"
	for i := 1; i < len(tokens); i++ {
		left, right := tokens[i-1], tokens[i]
		if left.Kind != fingerprint.TokPunct || right.Kind != fingerprint.TokPunct ||
			left.End != right.Start || len(left.Text) != 1 || len(right.Text) != 1 ||
			!strings.ContainsRune(postgresOperatorChars, rune(left.Text[0])) ||
			!strings.ContainsRune(postgresOperatorChars, rune(right.Text[0])) {
			continue
		}
		return true
	}
	return false
}

func frozenPostgresPathFromDatabase(path database.PostgresSearchPath) FrozenPostgresSearchPath {
	return FrozenPostgresSearchPath{
		Version:        path.SchemaVersion,
		CurrentUser:    path.CurrentUser,
		Configured:     path.Configured,
		Explicit:       append([]string(nil), path.Explicit...),
		Effective:      append([]string(nil), path.Effective...),
		CreationSchema: path.CreationSchema,
	}
}

func databasePostgresPath(path FrozenPostgresSearchPath) database.PostgresSearchPath {
	return database.PostgresSearchPath{
		SchemaVersion:  path.Version,
		CurrentUser:    path.CurrentUser,
		Configured:     path.Configured,
		Explicit:       append([]string(nil), path.Explicit...),
		Effective:      append([]string(nil), path.Effective...),
		CreationSchema: path.CreationSchema,
	}
}

func (s *Services) probePostgresSearchPath(
	ctx context.Context,
	target frozenDatasourceTarget,
) (FrozenPostgresSearchPath, error) {
	if s.PostgresPathProbe != nil {
		path, err := s.PostgresPathProbe(ctx, postgresSearchTargetOf(target))
		if err != nil || path.Validate() != nil {
			return FrozenPostgresSearchPath{}, ErrFactSourceUnavailable
		}
		return path, nil
	}
	pool, closePool, err := s.openVerifiedPostgresPool(
		ctx, target, "execution", database.PurposeExecution, true,
	)
	if err != nil {
		return FrozenPostgresSearchPath{}, err
	}
	defer closePool()
	path, err := database.ProbePostgresSearchPath(ctx, pool)
	if err != nil {
		return FrozenPostgresSearchPath{}, ErrFactSourceUnavailable
	}
	return frozenPostgresPathFromDatabase(path), nil
}

// probeIdentityMatchesTarget compares the search_path probe-side identity against the frozen target.
// When the DatasourceIdentityProbe test seam is installed, the frozen-side identity also comes from that
// seam (probeFrozenDatasourceTarget) — so comparison uses the seam identity to keep both sides same-sourced;
// the real dialed identity still proves connectivity first. In production the seam is always nil, using exact real-identity equality.
func (s *Services) probeIdentityMatchesTarget(
	ctx context.Context, row datasourceTargetRow, target frozenDatasourceTarget,
	probed *database.ServerIdentity,
) bool {
	if s.DatasourceIdentityProbe != nil {
		seamIdentity, err := s.DatasourceIdentityProbe(ctx, s.DB, s.Crypter, row)
		if err != nil {
			return false
		}
		return target.matchesIdentity(seamIdentity)
	}
	return probed != nil && target.matchesIdentity(*probed)
}

func (s *Services) resolvePostgresRelation(
	ctx context.Context,
	target frozenDatasourceTarget,
	path FrozenPostgresSearchPath,
	request PostgresRelationRequest,
) (PostgresRelationLookup, error) {
	if s.PostgresRelationResolver != nil {
		lookup, err := s.PostgresRelationResolver(
			ctx, postgresSearchTargetOf(target), path, request,
		)
		if err != nil {
			return PostgresRelationLookup{}, err
		}
		if lookup.Ordinal != request.Ordinal ||
			lookup.TableToken != request.TableToken ||
			lookup.TableName != request.TableName ||
			!lookup.Complete ||
			len(lookup.PresentBySchema) > postgresSearchPathMaxSchemas+3 {
			return PostgresRelationLookup{}, ErrFactSourceUnavailable
		}
		return clonePostgresRelationLookup(lookup), nil
	}

	pool, closePool, err := s.openVerifiedPostgresPool(
		ctx, target, "execution", database.PurposeReview, false,
	)
	if err != nil {
		return PostgresRelationLookup{}, err
	}
	defer closePool()
	dbPath := databasePostgresPath(path)
	lookups, err := database.LookupPostgresRelations(
		ctx, pool, dbPath, []string{request.TableName},
	)
	if err != nil || len(lookups) != 1 || lookups[0].Relation != request.TableName {
		return PostgresRelationLookup{}, ErrFactSourceUnavailable
	}
	lookup := PostgresRelationLookup{
		Ordinal:         request.Ordinal,
		TableToken:      request.TableToken,
		TableName:       request.TableName,
		PresentBySchema: make(map[string]string, len(path.Effective)),
		OIDBySchema:     map[string]uint32{},
		Complete:        true,
	}
	for schema, kind := range lookups[0].PresentBySchema {
		lookup.PresentBySchema[schema] = string(kind)
	}
	lookup.OIDBySchema = clonePostgresOIDs(lookups[0].OIDBySchema)
	return lookup, nil
}

func (s *Services) openVerifiedPostgresPool(
	ctx context.Context,
	target frozenDatasourceTarget,
	credPurpose string,
	poolPurpose database.Purpose,
	allowIdentitySeam bool,
) (*database.PurposePool, func(), error) {
	row, err := loadDatasourceTargetRow(ctx, s.DB, target.DatasourceID)
	if err != nil || !target.matchesConfig(row) {
		return nil, nil, ErrFactSourceIdentityChanged
	}
	adapter, credential, material, identity, err := probeDatasourceConnectionForPurpose(
		ctx, s.DB, s.Crypter, row, credPurpose,
	)
	if err != nil {
		return nil, nil, err
	}
	identityMatched := identity != nil && target.matchesIdentity(*identity)
	if allowIdentitySeam {
		identityMatched = s.probeIdentityMatchesTarget(ctx, row, target, identity)
	}
	if !identityMatched {
		_ = adapter.Close()
		return nil, nil, ErrFactSourceIdentityChanged
	}
	after, err := loadDatasourceTargetRow(ctx, s.DB, target.DatasourceID)
	if err != nil || !target.matchesConfig(after) {
		_ = adapter.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, ErrFactSourceIdentityChanged
	}
	pool, err := adapter.OpenPurposePool(ctx, poolPurpose, credential, material)
	if err != nil {
		_ = adapter.Close()
		return nil, nil, ErrFactSourceUnavailable
	}
	cleanup := func() {
		_ = pool.Close()
		_ = adapter.Close()
	}
	return pool, cleanup, nil
}

func (s *Services) openVerifiedPostgresReviewPool(
	ctx context.Context,
	target frozenDatasourceTarget,
) (*database.PurposePool, func(), error) {
	return s.openVerifiedPostgresPool(
		ctx, target, "review", database.PurposeReview, true,
	)
}

func (s *Services) resolvePostgresRelationBaselines(
	ctx context.Context,
	target frozenDatasourceTarget,
	path FrozenPostgresSearchPath,
	relations []string,
	extraSchemas ...string,
) (map[string]PostgresRelationBaseline, error) {
	if len(relations) == 0 || len(relations) > postgresSearchPathMaxResolvedTables {
		return map[string]PostgresRelationBaseline{}, nil
	}
	if s.PostgresRelationBatchResolver != nil {
		values, err := s.PostgresRelationBatchResolver(
			ctx, postgresSearchTargetOf(target), path, append([]string(nil), relations...),
		)
		if err != nil {
			return nil, err
		}
		return validatePostgresRelationBaselines(path, relations, values, extraSchemas...)
	}
	if s.PostgresRelationResolver != nil {
		out := make(map[string]PostgresRelationBaseline, len(relations))
		for _, relation := range relations {
			lookup, err := s.resolvePostgresRelation(
				ctx, target, path, PostgresRelationRequest{
					Ordinal: 1, TableToken: relation, TableName: relation,
				},
			)
			if err != nil {
				return nil, err
			}
			out[relation] = PostgresRelationBaseline{
				TableName:       relation,
				PresentBySchema: clonePostgresPresence(lookup.PresentBySchema),
				OIDBySchema:     clonePostgresOIDs(lookup.OIDBySchema),
			}
		}
		return validatePostgresRelationBaselines(path, relations, mapBaselines(out), extraSchemas...)
	}

	pool, closePool, err := s.openVerifiedPostgresReviewPool(ctx, target)
	if err != nil {
		return nil, err
	}
	defer closePool()
	lookups, err := database.LookupPostgresRelations(
		ctx, pool, databasePostgresPath(path), relations, extraSchemas...,
	)
	if err != nil {
		if errors.Is(err, database.ErrPolicyCatalogSchemaUnavailable) ||
			errors.Is(err, database.ErrMetadataVisibilityUnproven) {
			return nil, err
		}
		return nil, ErrFactSourceUnavailable
	}
	values := make([]PostgresRelationBaseline, 0, len(lookups))
	for _, lookup := range lookups {
		present := make(map[string]string, len(lookup.PresentBySchema))
		for schema, kind := range lookup.PresentBySchema {
			present[schema] = string(kind)
		}
		values = append(values, PostgresRelationBaseline{
			TableName: lookup.Relation, PresentBySchema: present,
			OIDBySchema: clonePostgresOIDs(lookup.OIDBySchema),
		})
	}
	return validatePostgresRelationBaselines(path, relations, values, extraSchemas...)
}

func (s *Services) verifyPostgresSchemasVisible(
	ctx context.Context,
	target frozenDatasourceTarget,
	path FrozenPostgresSearchPath,
	extraSchemas ...string,
) error {
	if s.PostgresPathProbe != nil {
		return nil
	}
	pool, closePool, err := s.openVerifiedPostgresReviewPool(ctx, target)
	if err != nil {
		return err
	}
	defer closePool()
	err = database.VerifyPostgresSchemasVisible(ctx, pool, databasePostgresPath(path), extraSchemas...)
	if err != nil {
		if errors.Is(err, database.ErrPolicyCatalogSchemaUnavailable) ||
			errors.Is(err, database.ErrMetadataVisibilityUnproven) {
			return err
		}
		return ErrFactSourceUnavailable
	}
	return nil
}

func mapBaselines(values map[string]PostgresRelationBaseline) []PostgresRelationBaseline {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]PostgresRelationBaseline, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		value.PresentBySchema = clonePostgresPresence(value.PresentBySchema)
		value.OIDBySchema = clonePostgresOIDs(value.OIDBySchema)
		out = append(out, value)
	}
	return out
}

func validatePostgresRelationBaselines(
	path FrozenPostgresSearchPath,
	relations []string,
	values []PostgresRelationBaseline,
	extraSchemas ...string,
) (map[string]PostgresRelationBaseline, error) {
	want := make(map[string]bool, len(relations))
	for _, relation := range relations {
		if database.ValidateIdentifier(relation) != nil {
			return nil, ErrFactSourceUnavailable
		}
		want[relation] = true
	}
	if len(values) != len(want) {
		return nil, ErrFactSourceUnavailable
	}
	for _, schema := range extraSchemas {
		if !isValidPostgresExplicitSchema(schema) {
			return nil, ErrFactSourceUnavailable
		}
	}
	pathSchemas := postgresAllowedSchemaSet(path.Effective, extraSchemas)
	out := make(map[string]PostgresRelationBaseline, len(values))
	for _, value := range values {
		if !want[value.TableName] {
			return nil, ErrFactSourceUnavailable
		}
		if _, duplicate := out[value.TableName]; duplicate {
			return nil, ErrFactSourceUnavailable
		}
		present := make(map[string]string, len(value.PresentBySchema))
		for schema, kind := range value.PresentBySchema {
			if !pathSchemas[schema] || !validPostgresRelationKind(kind) {
				return nil, ErrFactSourceUnavailable
			}
			present[schema] = kind
		}
		out[value.TableName] = PostgresRelationBaseline{
			TableName: value.TableName, PresentBySchema: present,
			OIDBySchema: clonePostgresOIDs(value.OIDBySchema),
		}
	}
	return out, nil
}

func validPostgresRelationKind(kind string) bool {
	switch kind {
	case "table", "view", "sequence", "index", "foreign_table", "other":
		return true
	default:
		return false
	}
}

func clonePostgresRelationLookup(lookup PostgresRelationLookup) PostgresRelationLookup {
	out := lookup
	out.PresentBySchema = clonePostgresPresence(lookup.PresentBySchema)
	out.OIDBySchema = clonePostgresOIDs(lookup.OIDBySchema)
	return out
}

func postgresStatementsOf(stmts []postgresAnalyzedStatement) []StatementValidityAtOrdinal {
	out := make([]StatementValidityAtOrdinal, 0, len(stmts))
	for i := range stmts {
		out = append(out, StatementValidityAtOrdinal{
			Ordinal: int32(i + 1),
			Facts:   stmts[i].result.Validity,
		})
	}
	return out
}

func postgresRelationRequests(
	stmts []postgresAnalyzedStatement,
	mapper *privacy.RunMapper,
) ([]PostgresRelationRequest, error) {
	if mapper == nil || len(stmts) > 100000 {
		return nil, ErrFactSourceUnavailable
	}
	requests := make([]PostgresRelationRequest, 0, len(stmts))
	tokenToName := make(map[string]string, 64)
	uniqueNames := make(map[string]struct{}, 64)
	for i := range stmts {
		ordinal := int32(i + 1)
		stmtStart := len(requests)
		var seenLarge map[string]struct{}
		for _, object := range stmts[i].objects {
			if object.Kind != string(privacy.KindTable) || object.Token == "" {
				continue
			}
			duplicate := false
			if seenLarge != nil {
				_, duplicate = seenLarge[object.Token]
			} else {
				for k := stmtStart; k < len(requests); k++ {
					if requests[k].TableToken == object.Token {
						duplicate = true
						break
					}
				}
			}
			if duplicate {
				continue
			}
			name, cached := tokenToName[object.Token]
			if !cached {
				kind, orig, ok := mapper.Original(object.Token)
				if !ok || kind != privacy.KindTable || orig == "" {
					return nil, ErrFactSourceUnavailable
				}
				name = orig
				tokenToName[object.Token] = name
				uniqueNames[name] = struct{}{}
				if len(uniqueNames) > postgresSearchPathMaxResolvedTables {
					return nil, ErrFactSourceUnavailable
				}
			}
			if seenLarge == nil && len(requests)-stmtStart >= 8 {
				seenLarge = make(map[string]struct{}, len(requests)-stmtStart+4)
				for k := stmtStart; k < len(requests); k++ {
					seenLarge[requests[k].TableToken] = struct{}{}
				}
			}
			if seenLarge != nil {
				seenLarge[object.Token] = struct{}{}
			}
			requests = append(requests, PostgresRelationRequest{
				Ordinal: ordinal, TableToken: object.Token, TableName: name,
			})
		}
		if len(requests)-stmtStart > 1 {
			stmtSlice := requests[stmtStart:]
			sort.Slice(stmtSlice, func(a, b int) bool {
				return stmtSlice[a].TableToken < stmtSlice[b].TableToken
			})
		}
	}
	return requests, nil
}

// resolvePostgresCallables resolves callable identity on the frozen effective
// path. The catalog read happens on a review-purpose connection whose own
// search path is pg_catalog, so user schemas cannot intercept the lookup.
func (s *Services) resolvePostgresCallables(
	ctx context.Context,
	target frozenDatasourceTarget,
	path FrozenPostgresSearchPath,
	requests []database.PostgresCallableRequest,
) ([]database.PostgresCallableResolution, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	if s.PostgresCallableResolver != nil {
		return s.PostgresCallableResolver(
			ctx, postgresSearchTargetOf(target), path,
			append([]database.PostgresCallableRequest(nil), requests...),
		)
	}
	pool, closePool, err := s.openVerifiedPostgresReviewPool(ctx, target)
	if err != nil {
		return nil, err
	}
	defer closePool()
	resolutions, err := database.ResolvePostgresCallables(
		ctx, pool, databasePostgresPath(path), requests,
	)
	if err != nil {
		return nil, ErrFactSourceUnavailable
	}
	return resolutions, nil
}

func postgresExplicitSQLSchemas(
	stmts []postgresAnalyzedStatement,
	mapper *privacy.RunMapper,
	effective []string,
) (extra []string, hasDisallowed bool) {
	if mapper == nil {
		return nil, false
	}
	effectiveSet := make(map[string]bool, len(effective))
	for _, schema := range effective {
		effectiveSet[schema] = true
	}
	seen := map[string]bool{}
	considerToken := func(kind, tok, relation string) {
		if tok == "" {
			return
		}
		origKind, orig, found := mapper.Original(tok)
		if !found || origKind != privacy.KindSchema || orig == "" {
			hasDisallowed = true
			return
		}
		if strings.EqualFold(orig, "pg_catalog") && kind == string(privacy.KindSchema) && relation == "reference" && effectiveSet[orig] {
			return
		}
		if !isValidPostgresExplicitSchema(orig) {
			hasDisallowed = true
			return
		}
		if effectiveSet[orig] || seen[orig] {
			return
		}
		seen[orig] = true
		extra = append(extra, orig)
	}
	for i := range stmts {
		for _, obj := range stmts[i].objects {
			if obj.Kind == "alias" || obj.Kind == "cte" {
				continue
			}
			if obj.Kind == string(privacy.KindSchema) {
				tok := obj.SchemaToken
				if tok == "" {
					tok = obj.Token
				}
				considerToken(obj.Kind, tok, string(obj.Relation))
				continue
			}
			if obj.SchemaToken != "" {
				considerToken(obj.Kind, obj.SchemaToken, string(obj.Relation))
			}
		}
	}
	sort.Strings(extra)
	return extra, hasDisallowed
}

func (s *Services) freezePostgresStageBinding(
	ctx context.Context,
	target frozenDatasourceTarget,
	stmts []postgresAnalyzedStatement,
	mapper *privacy.RunMapper,
	targetSchema ...string,
) (frozenPostgresPathBinding, error) {
	if target.CompatibilityMode != "postgresql" || mapper == nil {
		return frozenPostgresPathBinding{}, ErrFactSourceUnavailable
	}
	var overrideSchema string
	if len(targetSchema) > 0 {
		overrideSchema = strings.TrimSpace(targetSchema[0])
	}
	if overrideSchema != "" && !isValidPostgresExplicitSchema(overrideSchema) {
		return frozenPostgresPathBinding{}, errPostgresDisallowedExplicitSchema
	}
	path, err := s.probePostgresSearchPath(ctx, target)
	if err != nil {
		return frozenPostgresPathBinding{}, err
	}
	if overrideSchema != "" {
		path = FrozenPostgresSearchPath{
			Version:        path.Version,
			CurrentUser:    path.CurrentUser,
			Configured:     overrideSchema,
			Explicit:       []string{overrideSchema},
			Effective:      []string{"pg_catalog", overrideSchema},
			CreationSchema: overrideSchema,
		}
	}
	explicitSchemas, hasDisallowed := postgresExplicitSQLSchemas(stmts, mapper, path.Effective)
	if hasDisallowed {
		return frozenPostgresPathBinding{}, errPostgresDisallowedExplicitSchema
	}
	requests, err := postgresRelationRequests(stmts, mapper)
	if err != nil {
		return frozenPostgresPathBinding{}, err
	}
	baselineCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	uniqueRelations := make([]string, 0)
	seenRelations := map[string]bool{}
	for _, request := range requests {
		if !seenRelations[request.TableName] {
			seenRelations[request.TableName] = true
			uniqueRelations = append(uniqueRelations, request.TableName)
		}
	}
	var baselines map[string]PostgresRelationBaseline
	if len(uniqueRelations) > 0 {
		baselines, err = s.resolvePostgresRelationBaselines(
			baselineCtx, target, path, uniqueRelations, explicitSchemas...,
		)
		if err != nil {
			return frozenPostgresPathBinding{}, err
		}
	} else {
		baselines = map[string]PostgresRelationBaseline{}
		if overrideSchema != "" || len(explicitSchemas) > 0 {
			if err := s.verifyPostgresSchemasVisible(baselineCtx, target, path, explicitSchemas...); err != nil {
				return frozenPostgresPathBinding{}, err
			}
		}
	}
	lookups := make([]postgresRelationLookup, 0, len(requests))
	for _, request := range requests {
		baseline := baselines[request.TableName]
		lookups = append(lookups, postgresRelationLookup{
			Ordinal:         request.Ordinal,
			TableToken:      request.TableToken,
			TableName:       request.TableName,
			PresentBySchema: baseline.PresentBySchema,
			OIDBySchema:     baseline.OIDBySchema,
			Complete:        true,
		})
	}
	callableRequests, callablesOK := postgresCallableRequests(stmts)
	var callables []database.PostgresCallableResolution
	if callablesOK && len(callableRequests) != 0 {
		callables, err = s.resolvePostgresCallables(baselineCtx, target, path, callableRequests)
		if err != nil {
			if errors.Is(err, ErrFactSourceIdentityChanged) {
				return frozenPostgresPathBinding{}, err
			}
			// Catalog identity is unprovable: keep the relation binding but
			// leave the snapshot partial so execution fails closed.
			callablesOK = false
			callables = nil
		}
	}
	binding, _ := preparePostgresObjectBindingsWithOptions(
		path, postgresStatementsOf(stmts), lookups,
		callables, callablesOK,
		explicitSchemas, overrideSchema,
		// Identity resolution detokenizes tokens as-is (both schema and table tokens enter object resolution:
		// the schema token of explicitly qualified SQL must be detokenizable to a physical schema name;
		// after RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS, qualified SQL flows through this frozen path —
		// the legacy explicit-mapping bypass previously masked this resolution). Authorization boundaries are
		// unaffected: whether a schema falls within the frozen scope is adjudicated by allowedSchemas comparison.
		func(token string) (string, bool) {
			_, original, ok := mapper.Original(token)
			return original, ok
		},
		func() map[int32]bool {
			unsafe := map[int32]bool{}
			for i := range stmts {
				if stmts[i].pathUnsafe {
					unsafe[int32(i+1)] = true
				}
			}
			return unsafe
		}(),
	)
	return binding, nil
}

func postgresContextValueForStage(
	ctx context.Context,
	svc *Services,
	stage stageRow,
	target frozenDatasourceTarget,
	stmts []postgresAnalyzedStatement,
	mapper *privacy.RunMapper,
	targetSchema ...string,
) (map[string]any, error) {
	if stage.Engine != "postgresql" && stage.CompatibilityMode != "postgresql" {
		return map[string]any{}, nil
	}
	// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: stages no longer configure logical-to-physical Schema mappings,
	// so PostgreSQL stages uniformly freeze the execution-identity search_path binding (and optional user-selected
	// target schema override / explicit non-system SQL schemas within the same connected database).
	binding, err := svc.freezePostgresStageBinding(ctx, target, stmts, mapper, targetSchema...)
	if err != nil {
		status := postgresSearchPathUnavailable
		switch {
		case errors.Is(err, errPostgresDisallowedExplicitSchema):
			status = "sql_schema_out_of_scope"
		case errors.Is(err, database.ErrPolicyCatalogSchemaUnavailable),
			errors.Is(err, database.ErrMetadataVisibilityUnproven):
			status = policyCatalogReadFailureReason(err)
		}
		return map[string]any{
			stagePostgresSearchPathStatusKey: status,
		}, nil
	}
	return withFrozenPostgresBinding(map[string]any{}, binding)
}
