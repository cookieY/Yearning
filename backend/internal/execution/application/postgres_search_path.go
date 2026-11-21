package application

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	orderdomain "github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/execution/domain"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

type SearchPathProbe func(
	context.Context,
	*database.PurposePool,
	database.PostgresSearchPath,
) error

type frozenStageReviewRow struct {
	ReviewRunID         string `gorm:"column:review_run_id"`
	StagePosition       int32  `gorm:"column:stage_position"`
	DatasourceID        string `gorm:"column:datasource_id"`
	IdentityFingerprint string `gorm:"column:datasource_identity_fingerprint"`
	SnapshotHash        string `gorm:"column:snapshot_hash"`
	State               string `gorm:"column:state"`
	GatePassed          bool   `gorm:"column:gate_passed"`
	Ciphertext          []byte `gorm:"column:normalized_context_ciphertext"`
	Nonce               []byte `gorm:"column:normalized_context_nonce"`
	KeyID               string `gorm:"column:normalized_context_key_id"`
	Algorithm           string `gorm:"column:normalized_context_algorithm"`
}

// loadFrozenPostgresReviewContext returns the environment and object bindings
// frozen by the accepted stage review. A missing or malformed snapshot is not
// treated as a legacy default: empty PostgreSQL mappings require a new review.
func (s *Services) loadFrozenPostgresReviewContext(
	ctx context.Context, db *gorm.DB, stage *targetSnapshotRow,
) (reviewapp.FrozenPostgresReviewContext, error) {
	if db == nil || stage == nil || stage.StageReviewSnapshotID == "" {
		return reviewapp.FrozenPostgresReviewContext{},
			domain.ErrPreflightFailed("postgres_search_path_snapshot_missing")
	}
	var row frozenStageReviewRow
	if err := db.WithContext(ctx).Raw(`
		select review_run_id::text as review_run_id, stage_position,
		       datasource_id::text as datasource_id,
		       datasource_identity_fingerprint, snapshot_hash,
		       state, gate_passed,
		       normalized_context_ciphertext, normalized_context_nonce,
		       normalized_context_key_id, normalized_context_algorithm
		from stage_review_snapshots where id = ?`,
		stage.StageReviewSnapshotID).Scan(&row).Error; err != nil {
		return reviewapp.FrozenPostgresReviewContext{}, err
	}
	if !row.matchesStage(stage) {
		return reviewapp.FrozenPostgresReviewContext{},
			domain.ErrPreflightFailed("postgres_search_path_snapshot_mismatch")
	}
	return s.openFrozenPostgresReviewContext(row)
}

// matchesStage binds the encrypted context to the reviewed order stage as
// well as its own AAD primary key. The execution-side snapshot reference is a
// UUID column without a database foreign key, so this comparison is the
// fail-closed boundary against a swapped or stale snapshot reference.
func (row frozenStageReviewRow) matchesStage(stage *targetSnapshotRow) bool {
	return stage != nil &&
		row.ReviewRunID != "" &&
		row.StagePosition > 0 &&
		row.DatasourceID != "" &&
		row.IdentityFingerprint != "" &&
		row.SnapshotHash != "" &&
		len(row.Ciphertext) > 0 &&
		row.State == "passed" &&
		row.GatePassed &&
		row.StagePosition == stage.Position &&
		row.DatasourceID == stage.DatasourceID &&
		row.IdentityFingerprint == stage.IdentityFingerprint &&
		row.SnapshotHash == stage.ReviewSnapshotHash
}

func isPostgresSearchPathFailure(name string) bool {
	if strings.IndexByte(name, ':') >= 0 {
		name = name[strings.IndexByte(name, ':')+1:]
	}
	return strings.HasPrefix(name, "postgres_search_path_")
}

func postgresSearchPathFailureReason(name string) string {
	if strings.IndexByte(name, ':') >= 0 {
		name = name[strings.IndexByte(name, ':')+1:]
	}
	if isPostgresSearchPathFailure(name) {
		return name
	}
	return "postgres_search_path_changed"
}

// failOrderAfterPostgresSearchPathPreflightTx durably closes an order in the
// same serializable transaction that observed a begin-time search_path
// rejection. The caller commits this terminal decision and only then returns
// the original 3006 error to the API. E004 same-order retry remains available
// for connection failures, but a changed or unreadable frozen path invalidates
// the accepted review and requires a new draft.
func (s *Services) failOrderAfterPostgresSearchPathPreflightTx(
	ctx context.Context, tx *gorm.DB, in BeginInput, failureName string,
) error {
	reason := postgresSearchPathFailureReason(failureName)
	order, err := s.lockOrder(ctx, tx, in.OrderID)
	if err != nil {
		return err
	}
	var running orderdomain.OrderState
	switch orderdomain.OrderState(order.State) {
	case orderdomain.StateStageExecution:
		running, err = orderdomain.OrderTransition(
			orderdomain.StateStageExecution, orderdomain.ActionExecute,
		)
	case orderdomain.StateScheduled:
		running, err = orderdomain.OrderTransition(
			orderdomain.StateScheduled, orderdomain.EventScheduleDue,
		)
	case orderdomain.StateRunning:
		running = orderdomain.StateRunning
	default:
		// A concurrent withdrawal/void or an already-final state owns the
		// outcome and must not be overwritten by this rejection.
		return nil
	}
	if err != nil {
		return domain.ErrIllegal()
	}
	next, err := orderdomain.OrderTransition(
		running, orderdomain.EventDmlFailedRolledBack,
	)
	if err != nil {
		return domain.ErrIllegal()
	}
	stage, err := s.loadOrderStage(ctx, tx, order.ID)
	if err != nil {
		return err
	}
	live, sent, err := s.stageAttemptFacts(ctx, tx, stage.StageID)
	if err != nil {
		return err
	}
	if live > 0 || sent > 0 {
		// Another begin won the race. Its runner must independently
		// revalidate the frozen environment before any send.
		return nil
	}
	if err := s.touchStageLocked(
		ctx, tx, stage.StageID, string(orderdomain.StageFailed),
	); err != nil {
		return err
	}
	now := nowUTC()
	nextVersion := order.Version + 1
	if err := s.updateOrderVersionedRaw(ctx, tx, order.ID, order.Version,
		map[string]any{
			"state": string(next), "terminal_at": now,
			"version": nextVersion,
		}); err != nil {
		return err
	}
	if err := s.syncSourceOrderRollbackOnOrderTransitionTx(ctx, tx, order, next, now); err != nil {
		return err
	}
	return s.emitOrderEvent(
		ctx, tx, EventExecFailed, order.ID, auditlog.Actor{
			UserID: in.Actor.UserID, Username: in.Actor.Username,
		}, StateChangedPayload{
			AggregateID: order.ID, From: strPtr(order.State),
			To: string(next), ReasonCode: reason,
			StageID: strPtr(stage.StageID), AggregateVersion: nextVersion,
		},
	)
}

func (s *Services) openFrozenPostgresReviewContext(
	row frozenStageReviewRow,
) (reviewapp.FrozenPostgresReviewContext, error) {
	plain, err := s.Crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: row.Ciphertext, Nonce: row.Nonce,
		KeyID: row.KeyID, Algorithm: row.Algorithm,
	}, crypto.AAD{
		DeploymentID: s.Crypter.DeploymentID(),
		Table:        "stage_review_snapshots",
		PrimaryKey:   fmt.Sprintf("%s#%d", row.ReviewRunID, row.StagePosition),
		Column:       "normalized_context_ciphertext",
		Purpose:      crypto.PurposePayload,
	})
	if err != nil {
		return reviewapp.FrozenPostgresReviewContext{},
			domain.ErrPreflightFailed("postgres_search_path_snapshot_unreadable")
	}
	environment, ok := decodeFrozenPostgresReviewContext(plain)
	if !ok {
		return reviewapp.FrozenPostgresReviewContext{},
			domain.ErrPreflightFailed("postgres_search_path_snapshot_unreadable")
	}
	return environment, nil
}

func decodeFrozenPostgresReviewContext(
	plain []byte,
) (reviewapp.FrozenPostgresReviewContext, bool) {
	return reviewapp.DecodeFrozenPostgresReviewContext(plain)
}

func databasePostgresSearchPath(
	path reviewapp.FrozenPostgresSearchPath,
) database.PostgresSearchPath {
	return database.PostgresSearchPath{
		SchemaVersion:  path.Version,
		CurrentUser:    path.CurrentUser,
		Configured:     path.Configured,
		Explicit:       append([]string(nil), path.Explicit...),
		Effective:      append([]string(nil), path.Effective...),
		CreationSchema: path.CreationSchema,
	}
}

// ProbeFrozenPostgresReviewContext revalidates the frozen path and every
// relation binding visible at the requested statement ordinal. Ordinal zero
// verifies the complete reviewed environment.
func ProbeFrozenPostgresReviewContext(
	ctx context.Context,
	db *gorm.DB,
	crypter *crypto.Crypter,
	ds *datasource.DatasourceModel,
	expected reviewapp.FrozenPostgresSearchPath,
	binding *reviewapp.FrozenPostgresPathBinding,
	ordinal int32,
) error {
	if db == nil || crypter == nil || ds == nil ||
		ds.CompatibilityMode != "postgresql" || expected.Validate() != nil {
		return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
	}
	s := &Services{DB: db, Crypter: crypter}
	pool, err := s.openPool(ctx, ds)
	if err != nil {
		return domain.ErrPreflightFailed("postgres_search_path_unavailable")
	}
	defer func() { _ = pool.Close() }()
	return s.verifyPostgresReviewContext(ctx, pool, expected, binding, ordinal)
}

func (s *Services) verifyPostgresSearchPath(
	ctx context.Context,
	pool *database.PurposePool,
	expected reviewapp.FrozenPostgresSearchPath,
) error {
	return s.verifyPostgresReviewContext(ctx, pool, expected, nil, 0)
}

func (s *Services) verifyPostgresReviewContext(
	ctx context.Context,
	pool *database.PurposePool,
	expected reviewapp.FrozenPostgresSearchPath,
	binding *reviewapp.FrozenPostgresPathBinding,
	ordinal int32,
) error {
	want := databasePostgresSearchPath(expected)
	if pool == nil || expected.Validate() != nil ||
		expected.HasTemporarySchema() || want.Validate() != nil {
		return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
	}
	if binding != nil && (!binding.Complete || !binding.CallablesSafe) {
		return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
	}
	if s.SearchPathProbe != nil {
		if err := s.SearchPathProbe(ctx, pool, want); err != nil {
			return domain.ErrPreflightFailed("postgres_search_path_changed")
		}
		if binding != nil && !binding.Path.Equal(expected) {
			return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
		}
		return nil
	}
	observed, err := database.ProbePostgresSearchPath(ctx, pool)
	if err != nil || !want.Equal(observed) {
		return domain.ErrPreflightFailed("postgres_search_path_changed")
	}
	if binding != nil {
		if err := verifyPostgresObjectBindings(ctx, pool, expected, *binding, ordinal); err != nil {
			return err
		}
	}
	return nil
}

func (s *Services) verifyPostgresReviewContextOnDMLTransaction(
	ctx context.Context,
	pool *database.PurposePool,
	tx database.DMLTransaction,
	expected reviewapp.FrozenPostgresSearchPath,
	binding *reviewapp.FrozenPostgresPathBinding,
	ordinal int32,
) error {
	if expected.Validate() != nil || expected.HasTemporarySchema() ||
		databasePostgresSearchPath(expected).Validate() != nil {
		return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
	}
	if binding == nil || !binding.Complete || !binding.CallablesSafe {
		return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
	}
	names, err := reviewedPostgresRelationNames(*binding, ordinal)
	if err != nil {
		return domain.ErrPreflightFailed(
			"postgres_search_path_snapshot_invalid",
		)
	}
	requests, requestsOK := postgresCallableRequestsFromBinding(*binding)
	if !requestsOK {
		return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
	}
	observedPath, lookups, resolutions, err := database.InspectPostgresDMLTransaction(
		ctx, pool, tx, databasePostgresSearchPath(expected), names, requests,
	)
	if err != nil ||
		!databasePostgresSearchPath(expected).Equal(observedPath) {
		return domain.ErrPreflightFailed("postgres_search_path_changed")
	}
	if err := verifyPostgresCallables(*binding, resolutions); err != nil {
		return err
	}
	return verifyPostgresInspection(expected, *binding, ordinal, lookups)
}

func (s *Services) verifyPostgresReviewContextOnDDLConnection(
	ctx context.Context,
	conn *sql.Conn,
	expected reviewapp.FrozenPostgresSearchPath,
	binding *reviewapp.FrozenPostgresPathBinding,
	ordinal int32,
) error {
	if conn == nil || expected.Validate() != nil ||
		expected.HasTemporarySchema() ||
		databasePostgresSearchPath(expected).Validate() != nil {
		return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
	}
	if binding == nil || !binding.Complete || !binding.CallablesSafe {
		return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
	}
	names, err := reviewedPostgresRelationNames(*binding, ordinal)
	if err != nil {
		return domain.ErrPreflightFailed(
			"postgres_search_path_snapshot_invalid",
		)
	}
	requests, requestsOK := postgresCallableRequestsFromBinding(*binding)
	if !requestsOK {
		return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
	}
	observedPath, lookups, resolutions, err := database.InspectPostgresDDLConnection(
		ctx, conn, databasePostgresSearchPath(expected), names, requests,
	)
	if err != nil ||
		!databasePostgresSearchPath(expected).Equal(observedPath) {
		return domain.ErrPreflightFailed("postgres_search_path_changed")
	}
	if err := verifyPostgresCallables(*binding, resolutions); err != nil {
		return err
	}
	return verifyPostgresInspection(expected, *binding, ordinal, lookups)
}

func verifyPostgresObjectBindings(
	ctx context.Context,
	pool *database.PurposePool,
	path reviewapp.FrozenPostgresSearchPath,
	binding reviewapp.FrozenPostgresPathBinding,
	ordinal int32,
) error {
	if !binding.Complete || !binding.CallablesSafe ||
		!path.Equal(binding.Path) {
		return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
	}
	if err := verifyPostgresCallablesOnPool(ctx, pool, path, binding); err != nil {
		return err
	}
	if ordinal == 0 {
		names := make([]string, 0, len(binding.Baseline))
		for name := range binding.Baseline {
			names = append(names, name)
		}
		lookups, err := database.LookupPostgresRelations(
			ctx, pool, databasePostgresSearchPath(path), names,
		)
		if err != nil {
			return domain.ErrPreflightFailed("postgres_search_path_changed")
		}
		return verifyPostgresBaseline(path, binding.Baseline, lookups)
	}
	expected := []reviewapp.FrozenPostgresObjectBinding{}
	for key, relation := range binding.Relations {
		currentOrdinal, _, ok := strings.Cut(key, ":")
		if !ok {
			return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
		}
		value, err := strconv.ParseInt(currentOrdinal, 10, 32)
		if err != nil || value <= 0 {
			return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
		}
		if int32(value) == ordinal {
			if !relation.Supported {
				return domain.ErrPreflightFailed("postgres_search_path_changed")
			}
			expected = append(expected, relation)
		}
	}
	if len(expected) == 0 {
		return nil
	}
	names := make([]string, 0, len(expected))
	seen := map[string]bool{}
	for _, relation := range expected {
		if !seen[relation.TableName] {
			seen[relation.TableName] = true
			names = append(names, relation.TableName)
		}
	}
	lookups, err := database.LookupPostgresRelations(
		ctx, pool, databasePostgresSearchPath(path), names,
	)
	if err != nil || len(lookups) != len(names) {
		return domain.ErrPreflightFailed("postgres_search_path_changed")
	}
	byName := make(map[string]database.PostgresRelationLookup, len(lookups))
	for _, lookup := range lookups {
		byName[lookup.Relation] = lookup
	}
	for _, reviewed := range expected {
		lookup, ok := byName[reviewed.TableName]
		if !ok {
			return domain.ErrPreflightFailed("postgres_search_path_changed")
		}
		schema, kind, oid, present := reviewedBindingPosition(
			path, lookup, reviewed.Explicit, reviewed.Schema,
		)
		if present != reviewed.Present || (present &&
			(schema != reviewed.Schema || kind != reviewed.Kind ||
				(reviewed.OID != 0 && oid != reviewed.OID))) ||
			(!present && schema != reviewed.Schema) {
			return domain.ErrPreflightFailed("postgres_search_path_changed")
		}
	}
	return nil
}

// postgresCallableRequestsFromBinding reconstructs the exact catalog questions
// the frozen snapshot asked so send-time verification re-proves the same set.
func postgresCallableRequestsFromBinding(
	binding reviewapp.FrozenPostgresPathBinding,
) ([]database.PostgresCallableRequest, bool) {
	requests := make([]database.PostgresCallableRequest, 0, len(binding.CallablesRequested))
	for _, key := range binding.CallablesRequested {
		kind, name, ok := strings.Cut(key, ":")
		if !ok || kind == "" || name == "" {
			return nil, false
		}
		requests = append(requests, database.PostgresCallableRequest{
			Kind: database.PostgresCallableKind(kind), Name: name,
		})
	}
	return requests, true
}

// verifyPostgresCallables compares send-time callable identity with the frozen
// baseline. Any drift or unprovable identity fails closed before sending.
func verifyPostgresCallables(
	binding reviewapp.FrozenPostgresPathBinding,
	resolutions []database.PostgresCallableResolution,
) error {
	if !binding.CallablesChecked || len(resolutions) != len(binding.CallablesRequested) {
		return domain.ErrPreflightFailed("postgres_search_path_changed")
	}
	seen := map[string]bool{}
	for _, resolution := range resolutions {
		key := database.PostgresCallablePayloadKey(
			resolution.Request.Kind, resolution.Request.Name,
		)
		if seen[key] {
			return domain.ErrPreflightFailed("postgres_search_path_changed")
		}
		seen[key] = true
		frozen, ok := binding.Callables[key]
		if !ok ||
			frozen.Kind != string(resolution.Request.Kind) ||
			frozen.Name != resolution.Request.Name ||
			len(frozen.Winners) != len(resolution.Winners) {
			return domain.ErrPreflightFailed("postgres_search_path_changed")
		}
		for i := range frozen.Winners {
			observed := resolution.Winners[i]
			if frozen.Winners[i].Schema != observed.Schema ||
				frozen.Winners[i].Signature != observed.Signature ||
				frozen.Winners[i].OID != observed.OID ||
				frozen.Winners[i].PgCatalog != observed.PgCatalog ||
				frozen.Winners[i].ResultType != observed.ResultType {
				return domain.ErrPreflightFailed("postgres_search_path_changed")
			}
		}
	}
	return nil
}

// verifyPostgresCallablesOnPool re-resolves callable identity on a fresh
// review-purpose connection when no send-time transaction is available.
func verifyPostgresCallablesOnPool(
	ctx context.Context,
	pool *database.PurposePool,
	path reviewapp.FrozenPostgresSearchPath,
	binding reviewapp.FrozenPostgresPathBinding,
) error {
	requests, ok := postgresCallableRequestsFromBinding(binding)
	if !ok {
		return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
	}
	if len(requests) == 0 {
		return verifyPostgresCallables(binding, nil)
	}
	resolutions, err := database.ResolvePostgresCallables(
		ctx, pool, databasePostgresSearchPath(path), requests,
	)
	if err != nil {
		return domain.ErrPreflightFailed("postgres_search_path_changed")
	}
	return verifyPostgresCallables(binding, resolutions)
}

func reviewedPostgresRelationNames(
	binding reviewapp.FrozenPostgresPathBinding,
	ordinal int32,
) ([]string, error) {
	names := make([]string, 0)
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if ordinal == 0 {
		for name := range binding.Baseline {
			add(name)
		}
		return names, nil
	}
	if ordinal < 0 {
		return nil, domain.ErrPreflightFailed(
			"postgres_search_path_snapshot_invalid",
		)
	}
	for key, relation := range binding.Relations {
		current, _, ok := strings.Cut(key, ":")
		if !ok {
			return nil, domain.ErrPreflightFailed(
				"postgres_search_path_snapshot_invalid",
			)
		}
		value, err := strconv.ParseInt(current, 10, 32)
		if err != nil || value <= 0 {
			return nil, domain.ErrPreflightFailed(
				"postgres_search_path_snapshot_invalid",
			)
		}
		if int32(value) == ordinal {
			if !relation.Supported {
				return nil, domain.ErrPreflightFailed(
					"postgres_search_path_changed",
				)
			}
			add(relation.TableName)
		}
	}
	return names, nil
}

func verifyPostgresInspection(
	path reviewapp.FrozenPostgresSearchPath,
	binding reviewapp.FrozenPostgresPathBinding,
	ordinal int32,
	lookups []database.PostgresRelationLookup,
) error {
	if !path.Equal(binding.Path) {
		return domain.ErrPreflightFailed("postgres_search_path_snapshot_invalid")
	}
	if ordinal == 0 {
		return verifyPostgresBaseline(path, binding.Baseline, lookups)
	}
	expected := make([]reviewapp.FrozenPostgresObjectBinding, 0)
	for key, relation := range binding.Relations {
		current, _, ok := strings.Cut(key, ":")
		value, err := strconv.ParseInt(current, 10, 32)
		if !ok || err != nil || value <= 0 {
			return domain.ErrPreflightFailed(
				"postgres_search_path_snapshot_invalid",
			)
		}
		if int32(value) == ordinal {
			if !relation.Supported {
				return domain.ErrPreflightFailed("postgres_search_path_changed")
			}
			expected = append(expected, relation)
		}
	}
	if len(expected) == 0 {
		return nil
	}
	byName := make(map[string]database.PostgresRelationLookup, len(lookups))
	for _, lookup := range lookups {
		byName[lookup.Relation] = lookup
	}
	for _, reviewed := range expected {
		lookup, ok := byName[reviewed.TableName]
		if !ok {
			return domain.ErrPreflightFailed("postgres_search_path_changed")
		}
		schema, kind, oid, present := reviewedBindingPosition(
			path, lookup, reviewed.Explicit, reviewed.Schema,
		)
		if present != reviewed.Present ||
			(present && (schema != reviewed.Schema || kind != reviewed.Kind ||
				(reviewed.OID != 0 && oid != reviewed.OID))) ||
			(!present && schema != reviewed.Schema) {
			return domain.ErrPreflightFailed("postgres_search_path_changed")
		}
	}
	return nil
}

func verifyPostgresBaseline(
	path reviewapp.FrozenPostgresSearchPath,
	baseline map[string]reviewapp.FrozenPostgresBaselineRelation,
	lookups []database.PostgresRelationLookup,
) error {
	byName := make(map[string]database.PostgresRelationLookup, len(lookups))
	for _, lookup := range lookups {
		byName[lookup.Relation] = lookup
	}
	for tableName, reviewed := range baseline {
		lookup, ok := byName[tableName]
		if !ok {
			return domain.ErrPreflightFailed("postgres_search_path_changed")
		}
		for _, schema := range path.Effective {
			kind := lookup.PresentBySchema[schema]
			oid := lookup.OIDBySchema[schema]
			if string(kind) != reviewed.PresentBySchema[schema] ||
				(kind != "" && oid != reviewed.OIDBySchema[schema]) {
				return domain.ErrPreflightFailed("postgres_search_path_changed")
			}
		}
	}
	return nil
}

func reviewedBindingPosition(
	path reviewapp.FrozenPostgresSearchPath,
	lookup database.PostgresRelationLookup,
	explicit bool,
	explicitSchema string,
) (schema, kind string, oid uint32, present bool) {
	schemas := path.Effective
	if explicit {
		value := lookup.PresentBySchema[explicitSchema]
		if value == "" {
			return explicitSchema, "", 0, false
		}
		return explicitSchema, string(value), lookup.OIDBySchema[explicitSchema], true
	}
	for _, candidate := range schemas {
		value := lookup.PresentBySchema[candidate]
		if value == "" {
			continue
		}
		return candidate, string(value), lookup.OIDBySchema[candidate], true
	}
	return path.CreationSchema, "", 0, false
}
