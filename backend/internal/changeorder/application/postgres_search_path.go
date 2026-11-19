package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/cookieY/YearningX/backend/internal/changeorder/domain"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	execapp "github.com/cookieY/YearningX/backend/internal/execution/application"
	reviewapp "github.com/cookieY/YearningX/backend/internal/review/application"
)

const frozenPostgresSearchPathContextKey = "postgres_search_path"

// ExecutionSearchPathProbe opens an execution-purpose connection, reads the
// fixed environment, and rejects a path different from the accepted review.
type ExecutionSearchPathProbe func(
	ctx context.Context,
	ds *datasource.DatasourceModel,
	expected reviewapp.FrozenPostgresSearchPath,
	binding *reviewapp.FrozenPostgresPathBinding,
	ordinal int32,
) error

func (s *Services) verifySubmitPostgresSearchPath(
	ctx context.Context, runID string, snap stageSnapRow, stage stageFact,
) error {
	if stage.CompatibilityMode != "postgresql" {
		return nil
	}
	// RCP-20261031-REMOVE-FLOW-SCHEMA-MAPPINGS: stages no longer configure logical-to-physical
	// schema mappings; PostgreSQL stages uniformly require the execution identity search_path
	// snapshot frozen during review and re-probe to compare at submission time (the former
	// "skip probe on explicit mapping" bypass disappeared with the removal of mappings).
	expected, ok := decodeStageSnapshotPostgresReviewContext(
		s.Crypter, runID, int32(stage.Position), snap)
	if !ok {
		return domain.ErrReviewOutdated()
	}
	if err := s.verifyDecodedSubmitPostgresSearchPath(ctx, stage, expected); err != nil {
		return err
	}
	return nil
}

func (s *Services) verifyDecodedSubmitPostgresSearchPath(
	ctx context.Context,
	stage stageFact,
	expected reviewapp.FrozenPostgresReviewContext,
) error {
	if err := s.probeSubmitPostgresSearchPath(ctx, stage, expected); err != nil {
		return domain.ErrReviewOutdated()
	}
	return nil
}

func (s *Services) probeSubmitPostgresSearchPath(
	ctx context.Context,
	stage stageFact,
	expected reviewapp.FrozenPostgresReviewContext,
) error {
	if !expected.Binding.Complete || !expected.Binding.CallablesSafe {
		return domain.ErrReviewOutdated()
	}
	ds := &datasource.DatasourceModel{
		ID:                  stage.DatasourceID,
		Name:                stage.Name,
		Engine:              stage.Engine,
		CompatibilityMode:   stage.CompatibilityMode,
		Host:                stage.Host,
		Port:                stage.Port,
		DatabaseName:        stage.DatabaseName,
		Enabled:             true,
		IdentityFingerprint: stage.IdentityFP,
	}
	probe := s.SearchPathProbe
	if probe == nil {
		probe = func(ctx context.Context, ds *datasource.DatasourceModel,
			path reviewapp.FrozenPostgresSearchPath,
			binding *reviewapp.FrozenPostgresPathBinding,
			ordinal int32,
		) error {
			return execapp.ProbeFrozenPostgresReviewContext(
				ctx, s.DB, s.Crypter, ds, path, binding, ordinal)
		}
	}
	if err := probe(
		ctx, ds, expected.Path, &expected.Binding, 0,
	); err != nil {
		return err
	}
	return nil
}

func decodeStageSnapshotPostgresSearchPath(
	crypter *crypto.Crypter, runID string, stagePosition int32, snap stageSnapRow,
) (reviewapp.FrozenPostgresSearchPath, bool) {
	return decodeStageSnapshotPostgresSearchPathPlain(
		openStageSnapshotContext(crypter, runID, stagePosition, snap),
	)
}

func decodeStageSnapshotPostgresReviewContext(
	crypter *crypto.Crypter, runID string, stagePosition int32, snap stageSnapRow,
) (reviewapp.FrozenPostgresReviewContext, bool) {
	return reviewapp.DecodeFrozenPostgresReviewContext(
		openStageSnapshotContext(crypter, runID, stagePosition, snap),
	)
}

func openStageSnapshotContext(
	crypter *crypto.Crypter, runID string, stagePosition int32, snap stageSnapRow,
) []byte {
	if snap.NormalizedKeyID == "" || len(snap.NormalizedContext) == 0 {
		return nil
	}
	plain, err := crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: snap.NormalizedContext, Nonce: snap.NormalizedNonce,
		KeyID: snap.NormalizedKeyID, Algorithm: snap.NormalizedAlgorithm,
	}, crypto.AAD{
		DeploymentID: crypter.DeploymentID(), Table: "stage_review_snapshots",
		PrimaryKey: fmt.Sprintf("%s#%d", runID, stagePosition),
		Column:     "normalized_context_ciphertext",
		Purpose:    crypto.PurposePayload,
	})
	if err != nil {
		return nil
	}
	return plain
}

func decodeStageSnapshotPostgresSearchPathPlain(
	plain []byte,
) (reviewapp.FrozenPostgresSearchPath, bool) {
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.UseNumber()
	contextValue := map[string]any{}
	if err := decoder.Decode(&contextValue); err != nil {
		return reviewapp.FrozenPostgresSearchPath{}, false
	}
	rawValue, ok := contextValue[frozenPostgresSearchPathContextKey]
	if !ok || rawValue == nil {
		return reviewapp.FrozenPostgresSearchPath{}, false
	}
	raw, err := json.Marshal(rawValue)
	if err != nil {
		return reviewapp.FrozenPostgresSearchPath{}, false
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	var path reviewapp.FrozenPostgresSearchPath
	if err := strict.Decode(&path); err != nil || path.Validate() != nil {
		return reviewapp.FrozenPostgresSearchPath{}, false
	}
	return path, true
}

func decodeStageSnapshotPostgresReviewContextPlain(
	plain []byte,
) (reviewapp.FrozenPostgresReviewContext, bool) {
	return reviewapp.DecodeFrozenPostgresReviewContext(plain)
}
