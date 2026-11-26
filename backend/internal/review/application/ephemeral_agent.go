// ephemeral_agent.go Ephemeral Run executor: in-memory grouping + stage review loop + terminal convergence.
//
// Shares the exact same pure-compute routines as the DB pipeline (fakerunner.groupingPhase / AgentRunner.review:
// analyzeRunStatements / reviewStageCore / evaluateValidityProgram / budget ledger), differing only in where
// results land: all business rows stay in EphemeralRun memory and are written by the materializer upon submit.
// Model calls, budget reservation/settlement, and the per-call reference ledger still write to the database
// per ruling boundaries (pure counts without SQL or finding content).
package application

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/platform/hashutil"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// fakeProviderPlanHash is the static plan hash stamped onto review runs using the fallback plan identity.
var fakeProviderPlanHash = hashutil.SHA256Hex([]byte("fake-runner-v1"))

// heavyGroupingSem limits concurrent heavyweight (> ephemeralSyncSyntaxCheckMaxBytes) groupings
// inside a single review worker process to keep peak RSS within the Pod memory budget.
var heavyGroupingSem = make(chan struct{}, 2)

// ephemeralGrouping mirrors the analysis section of groupingPhase: streaming analysis -> in-memory model construction.
func (r *AgentRunner) ephemeralGrouping(ctx context.Context, er *EphemeralRun) error {
	s := r.svc
	stages, err := s.loadFlowStages(ctx, s.DB, er.FlowID)
	if err != nil {
		return err
	}
	if len(stages) == 0 {
		return domain.ErrCoverageIncomplete
	}
	hasPostgresStage := false
	for i := range stages {
		if stages[i].Engine == "postgresql" || stages[i].CompatibilityMode == "postgresql" {
			hasPostgresStage = true
			break
		}
	}

	er.mu.RLock()
	plain := er.SQLPlaintext
	profile := er.Profile
	mapper := er.Mapper
	expectedCount := int32(er.ExpectedStatementCount)
	validateSyntax := !er.syntaxValidated
	runID := er.ID
	er.mu.RUnlock()

	releaseRawSQL := func() {
		er.mu.Lock()
		er.SQLPlaintext = nil
		er.mu.Unlock()
		if s.Ephemeral != nil {
			s.Ephemeral.releaseSQLBytes(er)
		}
	}
	defer releaseRawSQL()

	if len(plain) > ephemeralSyncSyntaxCheckMaxBytes {
		select {
		case heavyGroupingSem <- struct{}{}:
			defer func() { <-heavyGroupingSem }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	analysis, err := analyzeRunStatementsStreaming(streamingAnalysisOptions{
		runID:           runID,
		rawSQL:          plain,
		profile:         profile,
		mapper:          mapper,
		engine:          s.Engine,
		expectedCount:   expectedCount,
		validateSyntax:  validateSyntax,
		inlineLimit:     ephemeralInlineStatementLimit,
		collectPostgres: hasPostgresStage,
	})
	plain = nil
	releaseRawSQL()
	if err != nil {
		return err
	}

	for i := range stages {
		if stages[i].IdentityFP == nil || *stages[i].IdentityFP == "" {
			zap.L().Error("ephemeral_grouping_identity_unverified",
				zap.String("review_run_id", er.ID),
				zap.Int("stage_position", stages[i].Position),
				zap.String("datasource_id", stages[i].DatasourceID))
			return &domain.Error{Code: domain.CodeReviewCoverageIncomp, Name: "datasource_identity_unverified"}
		}
	}
	frozenTargets := make(map[int]frozenDatasourceTarget, len(stages))
	for i := range stages {
		st := stages[i]
		target, terr := s.probeFrozenDatasourceTarget(ctx, datasourceTargetRow{
			ID: st.DatasourceID, Engine: st.Engine,
			CompatibilityMode: st.CompatibilityMode,
			Host:              st.Host, Port: st.Port, DatabaseName: st.DatabaseName,
			IdentityFP: st.IdentityFP, Enabled: st.Enabled,
		})
		if terr != nil {
			reason := factSourceFailureReason(terr)
			zap.L().Error("ephemeral_grouping_target_failed",
				zap.String("review_run_id", er.ID),
				zap.Int("stage_position", st.Position),
				zap.String("datasource_id", st.DatasourceID),
				zap.String("reason", reason))
			return &domain.Error{Code: domain.CodeReviewCoverageIncomp, Name: reason}
		}
		frozenTargets[st.Position] = target
	}
	postgresContextByPosition := make(map[int]map[string]any, len(stages))
	for i := range stages {
		stage := stages[i]
		extra, perr := postgresContextValueForStage(ctx, s, stage, frozenTargets[stage.Position], analysis.postgresStatements, er.Mapper, er.TargetDatabase)
		if perr != nil {
			return domain.ErrCoverageIncomplete
		}
		postgresContextByPosition[stage.Position] = extra
	}
	analysis.postgresStatements = nil
	fpKeyID, err := s.Crypter.HMACKeyID(sqlHMACPurpose)
	if err != nil {
		return err
	}
	now := nowUTC()
	er.mu.Lock()
	defer er.mu.Unlock()
	er.SQLPlaintext = nil
	er.Run = persistence.ReviewRunModel{
		ID: er.ID, DraftID: er.DraftID, DraftRevision: 0,
		State:            string(domain.RunStateRunning),
		FlowSnapshotHash: er.FlowHash, RuleConfigHash: er.RuleHash,
		ToolConfigHash: er.ToolHash, ProviderPlanHash: fakeProviderPlanHash,
		FingerprintAlgorithmVersion:   fingerprint.AlgorithmVersion,
		AnonymizationAlgorithmVersion: "run-mapper-v1",
		StatementCount:                analysis.statementCount,
		FingerprintGroupCount:         int32(len(analysis.groups)),
		BudgetDay:                     now.Format("2006-01-02"),
		Version:                       1,
		CreatedAt:                     er.CreatedAt,
		StartedAt:                     &now,
	}
	er.analyzedStatementCount = analysis.statementCount
	er.Statements = analysis.inlineStatements
	er.repOrdinalToStmtID = analysis.repOrdinalToStmtID
	er.validityTable = analysis.validityTable
	er.validityRefs = analysis.validityRefs

	// Group models (representative statements resolved in O(1) via repOrdinalToStmtID).
	er.Groups = make([]persistence.SQLFingerprintGroupModel, 0, len(analysis.groups))
	er.groupMeta = make(map[string]ephemeralGroupMeta, len(analysis.groups))
	er.repSQLByGroup = make(map[string]string, len(analysis.groups))
	for i := range analysis.groups {
		g := analysis.groups[i]
		repID := analysis.repOrdinalToStmtID[g.representativeOrdinal]
		if repID == "" {
			return &domain.Error{Code: domain.CodeReviewCoverageIncomp, Name: "REVIEW_COVERAGE_INCOMPLETE"}
		}
		typed, merr := buildGroupTypedShape(g)
		if merr != nil {
			return merr
		}
		gid := newID()
		er.Groups = append(er.Groups, persistence.SQLFingerprintGroupModel{
			ID: gid, ReviewRunID: er.ID,
			CanonicalFingerprintHMAC: g.canon, DialectFingerprintHMAC: g.dialect,
			AlgorithmVersion: fingerprint.AlgorithmVersion, FingerprintKeyID: fpKeyID,
			TypedShape: typed, InValueCountMax: int32(g.inMax), ValuesRowCountMax: int32(g.valMax),
			StatementCount: int32(g.count), RepresentativeStatementID: repID,
		})
		er.groupMeta[gid] = ephemeralGroupMeta{
			RepKind:      g.repKind,
			RepOperation: g.repOperation,
			SizeMax:      g.sizeMax,
			RepOrdinal:   g.representativeOrdinal,
		}
		er.repSQLByGroup[gid] = g.repSQL
	}
	// Stage snapshot skeletons (identical to groupingPhase: encrypted frozen target context).
	er.Snapshots = make([]persistence.StageReviewSnapshotModel, 0, len(stages))
	for i := range stages {
		st := stages[i]
		contextBase := map[string]any{
			"stage_position": st.Position, "datasource_id": st.DatasourceID,
			"engine": st.Engine, "review_run_id": er.ID,
		}
		for key, value := range postgresContextByPosition[st.Position] {
			contextBase[key] = value
		}
		contextValue, cerr := withFrozenDatasourceTarget(contextBase, frozenTargets[st.Position])
		if cerr != nil {
			return cerr
		}
		env, serr := sealStageReviewContext(s.Crypter, er.ID, int32(st.Position), contextValue)
		if serr != nil {
			return serr
		}
		er.Snapshots = append(er.Snapshots, persistence.StageReviewSnapshotModel{
			ID: newID(), ReviewRunID: er.ID, StagePosition: int32(st.Position),
			DatasourceID: st.DatasourceID, DatasourceIdentityFingerprint: *st.IdentityFP,
			State: string(domain.StageStatePending), HighestSeverity: string(domain.SeverityNone),
			GatePassed: false, GateReasonCodes: persistence.EncodeTextArray(nil),
			NormalizedContextCiphertext: env.Ciphertext, NormalizedContextNonce: env.Nonce,
			NormalizedContextKeyID: env.KeyID, NormalizedContextAlgorithm: env.Algorithm,
			SnapshotHash: hashutil.SHA256Hex([]byte(er.ID + "|" + fmt.Sprint(st.Position) + "|" + *st.IdentityFP)),
			CreatedAt:    now,
		})
	}
	er.StartedAt = &now
	er.state = domain.RunStateRunning
	return nil
}

// ephemeralStageGroups assembles AnonymizedGroup slices from in-memory group models (column-for-column mirror
// of stageGroups row assembly: M7 shape synthesis + maximum single-statement byte count within the group).
func ephemeralStageGroups(er *EphemeralRun, datasourceID string) []provider.AnonymizedGroup {
	er.mu.RLock()
	defer er.mu.RUnlock()
	out := []provider.AnonymizedGroup{}
	for gi := range er.Groups {
		g := &er.Groups[gi]
		var repKind, repOp string
		var sizeMax int64
		if er.groupMeta != nil {
			meta, ok := er.groupMeta[g.ID]
			if !ok {
				continue
			}
			repKind = meta.RepKind
			repOp = meta.RepOperation
			sizeMax = meta.SizeMax
		} else {
			var rep *persistence.SQLStatementModel
			for i := range er.Statements {
				if er.Statements[i].ID == g.RepresentativeStatementID {
					rep = &er.Statements[i]
					break
				}
			}
			if rep == nil {
				continue
			}
			repKind = rep.StatementKind
			repOp = rep.Operation
			for i := range er.Statements {
				st := &er.Statements[i]
				if st.CanonicalFingerprintHMAC == g.CanonicalFingerprintHMAC &&
					st.DialectFingerprintHMAC == g.DialectFingerprintHMAC &&
					int64(st.SizeBytes) > sizeMax {
					sizeMax = int64(st.SizeBytes)
				}
			}
		}
		stats := map[string]any{}
		if len(g.TypedShape) > 0 {
			if err := json.Unmarshal(g.TypedShape, &stats); err != nil {
				continue
			}
		}
		var metadata struct {
			SQL                   string                               `json:"representative_anonymized_sql"`
			Objects               []provider.AnonymizedObjectReference `json:"objects"`
			Complete              bool                                 `json:"metadata_context_complete"`
			Ordinal               int32                                `json:"statement_ordinal"`
			OrdinalRanges         []provider.StatementOrdinalRange     `json:"statement_ordinal_ranges"`
			ObjectRangesTruncated bool                                 `json:"object_ranges_truncated"`
		}
		if err := json.Unmarshal(g.TypedShape, &metadata); err != nil {
			continue
		}
		delete(stats, "representative_anonymized_sql")
		delete(stats, "objects")
		delete(stats, "metadata_context_complete")
		delete(stats, "statement_ordinal")
		delete(stats, "statement_ordinal_ranges")
		delete(stats, "object_ranges_truncated")
		if metadata.ObjectRangesTruncated {
			for i := range metadata.Objects {
				metadata.Objects[i].OrdinalRangesTruncated = true
			}
		}
		stats["max_statement_bytes"] = sizeMax
		shape := fmt.Sprintf("kind=%v op=%v where=%v",
			repKind, repOp, !boolValue(stats["unbounded_mutation"]))
		out = append(out, provider.AnonymizedGroup{
			GroupID: g.ID, RepresentativeShape: shape, StatementCount: int64(g.StatementCount), Statistics: stats,
			RepresentativeAnonymizedSQL: metadata.SQL, Objects: metadata.Objects,
			MetadataContextComplete: metadata.Complete,
			StatementOrdinal:        metadata.Ordinal, StatementOrdinalRanges: metadata.OrdinalRanges,
		})
	}
	return out
}

// ephemeralValidityOf evaluates execution preconditions from in-memory data: program/group mappings are
// constructed from in-memory models (mirroring loadValidityProgram row logic).
func ephemeralValidityOf(er *EphemeralRun) stageValidityFunc {
	return func(ctx context.Context, db *gorm.DB, run *persistence.ReviewRunModel,
		binding *stageSchemaBinding, mapper *privacy.RunMapper, engine, locale string,
		refs []provider.NormalizedEvidenceRef,
	) (ValidityResult, []mappedFinding, map[string]any, error) {
		er.mu.RLock()
		if len(er.validityRefs) > 0 {
			refsCompact := er.validityRefs
			table := er.validityTable
			stmtCount := run.StatementCount
			groupIDs := make([]string, len(er.Groups))
			for i := range er.Groups {
				groupIDs[i] = er.Groups[i].ID
			}
			er.mu.RUnlock()
			program := ValidityProgram{
				Complete:   int32(len(refsCompact)) == stmtCount,
				Statements: make([]StatementValidityAtOrdinal, len(refsCompact)),
			}
			groupIdxByOrd := make([]uint16, len(refsCompact))
			for i, ref := range refsCompact {
				program.Statements[i] = StatementValidityAtOrdinal{
					Ordinal: int32(i + 1),
					Facts:   table[ref.validityIdx],
				}
				groupIdxByOrd[i] = ref.groupIdx
			}
			return evaluateValidityProgramWithGroupLookup(ctx, program, func(ord int32) string {
				idx := int(ord - 1)
				if idx < 0 || idx >= len(groupIdxByOrd) {
					return ""
				}
				gi := int(groupIdxByOrd[idx])
				if gi < 0 || gi >= len(groupIDs) {
					return ""
				}
				return groupIDs[gi]
			}, binding, mapper, engine, locale, refs)
		}
		program := ValidityProgram{Complete: int32(len(er.Statements)) == run.StatementCount}
		groups := map[int32]string{}
		for i := range er.Statements {
			st := &er.Statements[i]
			var shape struct {
				Validity fingerprint.StatementValidity `json:"execution_validity"`
			}
			if err := json.Unmarshal(st.TypedShape, &shape); err != nil {
				er.mu.RUnlock()
				return ValidityResult{}, nil, nil, err
			}
			if st.Ordinal != int32(i+1) {
				program.Complete = false
			}
			gid := ""
			for gi := range er.Groups {
				g := &er.Groups[gi]
				if g.CanonicalFingerprintHMAC == st.CanonicalFingerprintHMAC &&
					g.DialectFingerprintHMAC == st.DialectFingerprintHMAC {
					gid = g.ID
					break
				}
			}
			if gid == "" {
				program.Complete = false
			}
			program.Statements = append(program.Statements, StatementValidityAtOrdinal{Ordinal: st.Ordinal, Facts: shape.Validity})
			groups[st.Ordinal] = gid
		}
		er.mu.RUnlock()
		return evaluateValidityProgram(ctx, program, groups, binding, mapper, engine, locale, refs)
	}
}
