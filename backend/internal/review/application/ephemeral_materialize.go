// ephemeral_materialize.go Submission-time materialization: batch-inserts the in-memory models of an
// ephemeral Run into the caller's transaction under the existing table schemas and constraints
// (RCP-20261101-EPHEMERAL-PRECHECK — submit = sole persistence point for review artifacts). Thereafter,
// approval/audit/evidence read surfaces are completely isomorphic to existing orders (order_stages -> snapshot pointers -> live rows).
package application

import (
	"bytes"
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cookieY/YearningX/backend/internal/review/changeset"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/persistence"
)

// MaterializeEphemeralRunTx materializes an ephemeral Run inside the submission transaction using er.SQLPlaintext
// when statement rows were not kept inline.
func (s *Services) MaterializeEphemeralRunTx(ctx context.Context, tx *gorm.DB, er *EphemeralRun, draftID string, draftRevision int64) error {
	var sqlBytes []byte
	if er != nil {
		er.mu.RLock()
		sqlBytes = er.SQLPlaintext
		er.mu.RUnlock()
	}
	return s.MaterializeEphemeralRunTxWithSQL(ctx, tx, er, draftID, draftRevision, sqlBytes)
}

// MaterializeEphemeralRunTxWithSQL materializes an ephemeral Run inside the submission transaction. draftID/draftRevision
// are supplied by the submitter (trial Runs only learn their attached draft at submit time); row values are column-for-column
// isomorphic to the DB path, streaming large (>500 statement) batches directly from sqlBytes.
func (s *Services) MaterializeEphemeralRunTxWithSQL(ctx context.Context, tx *gorm.DB, er *EphemeralRun, draftID string, draftRevision int64, sqlBytes []byte) error {
	er.mu.RLock()
	defer er.mu.RUnlock()
	now := nowUTC()
	run := er.Run
	run.DraftID = draftID
	run.DraftRevision = draftRevision
	if err := tx.WithContext(ctx).Create(&run).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(`
		insert into review_run_sql_bindings (review_run_id, sql_payload_hmac, created_at)
		values (?, ?, ?)
		on conflict (review_run_id) do nothing`, er.ID, er.SQLHMAC, now).Error; err != nil {
		return err
	}
	// Identifier mapping envelope (isomorphic to existing read surfaces / display.Load).
	if err := persistIdentifierMappingTx(ctx, tx, s.Crypter, er.ID, er.Mapper); err != nil {
		return err
	}
	if len(er.Statements) > 0 {
		if err := insertSQLStatementsBulkTx(ctx, tx, er.Statements); err != nil {
			return err
		}
	} else if er.Run.StatementCount > 0 && !er.skipStmtMaterialize {
		if len(sqlBytes) == 0 || s.Engine == nil {
			return domain.ErrCoverageIncomplete
		}
		if err := er.Profile.Validate(); err != nil {
			return domain.ErrCoverageIncomplete
		}
		dialectFlags := er.Profile.Flags()
		split := changeset.NewSplitter(bytes.NewReader(sqlBytes), er.Profile, 0)
		batch := make([]persistence.SQLStatementModel, 0, sqlStatementBulkBatchSize)
		var ordinal int32
		for {
			st, done, serr := split.Next()
			if serr != nil {
				return domain.ErrCoverageIncomplete
			}
			if done {
				break
			}
			ordinal++
			tokens, lexErr := fingerprint.Tokenize(st.SQL, dialectFlags)
			if lexErr != nil {
				return domain.ErrCoverageIncomplete
			}
			res, aerr := s.Engine.AnalyzeLexed(er.Profile, st.SQL, tokens, er.Mapper)
			if aerr != nil {
				return domain.ErrCoverageIncomplete
			}
			op := operationLower(st.SQL)
			stmtID := er.repOrdinalToStmtID[ordinal]
			if stmtID == "" {
				stmtID = newID()
			}
			rowModel, berr := buildSQLStatementModel(
				er.ID, stmtID, ordinal, res, op, statementClassOf(op),
				objectsAtStatementOrdinal(res.Objects, ordinal),
			)
			if berr != nil {
				return berr
			}
			batch = append(batch, rowModel)
			if len(batch) == sqlStatementBulkBatchSize {
				if err := insertSQLStatementsBulkTx(ctx, tx, batch); err != nil {
					return err
				}
				batch = batch[:0]
			}
		}
		if ordinal != er.Run.StatementCount {
			return domain.ErrCoverageIncomplete
		}
		if len(batch) > 0 {
			if err := insertSQLStatementsBulkTx(ctx, tx, batch); err != nil {
				return err
			}
		}
	}
	for i := range er.Groups {
		if err := tx.WithContext(ctx).Create(&er.Groups[i]).Error; err != nil {
			return err
		}
	}
	for i := range er.Snapshots {
		if err := tx.WithContext(ctx).Create(&er.Snapshots[i]).Error; err != nil {
			return err
		}
	}
	for i := range er.Facts {
		f := &er.Facts[i]
		if err := tx.WithContext(ctx).Exec(`
			insert into evidence_facts (id, stage_review_id, source_kind, source_ref,
				normalized_fact_ciphertext, normalized_fact_nonce, normalized_fact_key_id,
				normalized_fact_algorithm, fact_hash, created_at)
			values (?, ?, 'database_tool', ?, ?, ?, ?, 'aes-256-gcm', ?, ?)`,
			f.ID, f.SnapID, f.SourceRef,
			f.FactEnv.Ciphertext, f.FactEnv.Nonce, f.FactEnv.KeyID, f.FactMAC, f.CreatedAt).Error; err != nil {
			return err
		}
		rawExpires := f.CreatedAt.Add(7 * 24 * time.Hour)
		if rawExpires.Before(now) {
			rawExpires = now.Add(7 * 24 * time.Hour)
		}
		if err := tx.WithContext(ctx).Exec(`
			insert into evidence_raw_payloads (id, evidence_fact_id, raw_payload_ciphertext,
				raw_payload_nonce, raw_payload_key_id, raw_payload_algorithm, payload_hash,
				created_at, expires_at)
			values (?, ?, ?, ?, ?, 'aes-256-gcm', ?, ?, ?)`,
			newID(), f.ID, f.RawEnv.Ciphertext, f.RawEnv.Nonce, f.RawEnv.KeyID, f.RawHash,
			f.CreatedAt, rawExpires).Error; err != nil {
			return err
		}
	}
	if len(er.ToolCalls) > 0 {
		if err := tx.WithContext(ctx).CreateInBatches(er.ToolCalls, 512).Error; err != nil {
			return err
		}
	}
	for _, f := range er.Findings {
		if err := tx.WithContext(ctx).Exec(`
			insert into review_findings (id, stage_review_id, fingerprint_group_id, category,
				severity, title, message, suggestion, created_at)
			values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.ID, f.SnapID, f.GroupID, f.Category, f.Severity,
			f.Title, f.Message, f.Suggestion, f.CreatedAt).Error; err != nil {
			return err
		}
		for _, eid := range f.Evidence {
			if err := tx.WithContext(ctx).Exec(`
				insert into review_finding_evidence (finding_id, evidence_fact_id) values (?, ?)`,
				f.ID, eid).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// PersistDraftSQLPayloadTx writes the draft SQL payload inside the submission transaction (RCP-20261101:
// submit carries the current SQL, so persistence point = submit). Does not bump revision — revision advances
// only on "Save and Return" (ReplaceSQL); after submit, the draft is terminal and no longer editable.
// Returns the plaintext HMAC for the caller to compare against the reviewed HMAC (preventing review-A-submit-B).
func (s *Services) PersistDraftSQLPayloadTx(ctx context.Context, tx *gorm.DB, draftID string, sqlText []byte, statementCount int32) (string, error) {
	env, mac, err := sealPayload(s.Crypter, "draft_sql_payloads", draftID, "sql_ciphertext", sqlText)
	if err != nil {
		return "", err
	}
	if err := tx.WithContext(ctx).Exec(`
		delete from draft_sql_payloads where draft_id = ?`, draftID).Error; err != nil {
		return "", err
	}
	if err := tx.WithContext(ctx).Exec(`
		insert into draft_sql_payloads (draft_id, sql_ciphertext, sql_nonce, sql_key_id,
			sql_algorithm, plaintext_hmac, size_bytes, statement_count, updated_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		draftID, env.Ciphertext, env.Nonce, env.KeyID, env.Algorithm, mac,
		int32(len(sqlText)), statementCount, nowUTC()).Error; err != nil {
		return "", err
	}
	return mac, nil
}

const sqlStatementBulkBatchSize = 2048

// pgRawArrayArg prevents GORM's default reflect.Slice expansion into ($1, $2, ...)
// so Go slices pass through as single PostgreSQL array bind parameters to pgx/v5.
type pgRawArrayArg struct{ val any }

func (a pgRawArrayArg) Build(builder clause.Builder) {
	if stmt, ok := builder.(*gorm.Statement); ok {
		stmt.Vars = append(stmt.Vars, a.val)
		stmt.DB.Dialector.BindVarTo(stmt, stmt, a.val)
	}
}

const insertSQLStatementsUnnestSQL = `
INSERT INTO sql_statements (
	id, review_run_id, ordinal, statement_kind, operation, size_bytes,
	algorithm_version, dialect_profile, fingerprint_quality, fingerprint_key_id,
	canonical_fingerprint_hmac, dialect_fingerprint_hmac, execution_shape_hmac,
	typed_shape, literal_type_counts, tuple_count, parameter_count
)
SELECT u1::uuid, u2::uuid, u3, u4, u5, u6,
       u7, u8, u9, u10,
       u11, u12, u13,
       u14, u15, u16, u17
FROM unnest(
	?::text[], ?::text[], ?::int4[], ?::text[], ?::text[], ?::int4[],
	?::text[], ?::text[], ?::text[], ?::text[],
	?::text[], ?::text[], ?::text[],
	?::jsonb[], ?::jsonb[], ?::int4[], ?::int4[]
) AS t(u1, u2, u3, u4, u5, u6, u7, u8, u9, u10, u11, u12, u13, u14, u15, u16, u17)`

func insertSQLStatementsBulkTx(ctx context.Context, tx *gorm.DB, rows []persistence.SQLStatementModel) error {
	if len(rows) == 0 {
		return nil
	}
	if tx.Dialector == nil || tx.Dialector.Name() != "postgres" {
		return tx.WithContext(ctx).CreateInBatches(rows, 512).Error
	}
	capHint := len(rows)
	if capHint > sqlStatementBulkBatchSize {
		capHint = sqlStatementBulkBatchSize
	}
	ids := make([]string, 0, capHint)
	runIDs := make([]string, 0, capHint)
	ordinals := make([]int32, 0, capHint)
	kinds := make([]string, 0, capHint)
	ops := make([]string, 0, capHint)
	sizeBytes := make([]int32, 0, capHint)
	algVersions := make([]string, 0, capHint)
	profiles := make([]string, 0, capHint)
	qualities := make([]string, 0, capHint)
	keyIDs := make([]string, 0, capHint)
	canonHMACs := make([]string, 0, capHint)
	dialectHMACs := make([]string, 0, capHint)
	shapeHMACs := make([]string, 0, capHint)
	typedShapes := make([]string, 0, capHint)
	literalCounts := make([]string, 0, capHint)
	tupleCounts := make([]int32, 0, capHint)
	paramCounts := make([]int32, 0, capHint)

	for start := 0; start < len(rows); start += sqlStatementBulkBatchSize {
		end := start + sqlStatementBulkBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[start:end]
		ids = ids[:0]
		runIDs = runIDs[:0]
		ordinals = ordinals[:0]
		kinds = kinds[:0]
		ops = ops[:0]
		sizeBytes = sizeBytes[:0]
		algVersions = algVersions[:0]
		profiles = profiles[:0]
		qualities = qualities[:0]
		keyIDs = keyIDs[:0]
		canonHMACs = canonHMACs[:0]
		dialectHMACs = dialectHMACs[:0]
		shapeHMACs = shapeHMACs[:0]
		typedShapes = typedShapes[:0]
		literalCounts = literalCounts[:0]
		tupleCounts = tupleCounts[:0]
		paramCounts = paramCounts[:0]

		for i := range chunk {
			r := &chunk[i]
			ids = append(ids, r.ID)
			runIDs = append(runIDs, r.ReviewRunID)
			ordinals = append(ordinals, r.Ordinal)
			kinds = append(kinds, r.StatementKind)
			ops = append(ops, r.Operation)
			sizeBytes = append(sizeBytes, r.SizeBytes)
			algVersions = append(algVersions, r.AlgorithmVersion)
			profiles = append(profiles, r.DialectProfile)
			qualities = append(qualities, r.FingerprintQuality)
			keyIDs = append(keyIDs, r.FingerprintKeyID)
			canonHMACs = append(canonHMACs, r.CanonicalFingerprintHMAC)
			dialectHMACs = append(dialectHMACs, r.DialectFingerprintHMAC)
			shapeHMACs = append(shapeHMACs, r.ExecutionShapeHMAC)
			if len(r.TypedShape) > 0 {
				typedShapes = append(typedShapes, string(r.TypedShape))
			} else {
				typedShapes = append(typedShapes, "{}")
			}
			if len(r.LiteralTypeCounts) > 0 {
				literalCounts = append(literalCounts, string(r.LiteralTypeCounts))
			} else {
				literalCounts = append(literalCounts, "{}")
			}
			tupleCounts = append(tupleCounts, r.TupleCount)
			paramCounts = append(paramCounts, r.ParameterCount)
		}
		if err := tx.WithContext(ctx).Exec(
			insertSQLStatementsUnnestSQL,
			pgRawArrayArg{ids},
			pgRawArrayArg{runIDs},
			pgRawArrayArg{ordinals},
			pgRawArrayArg{kinds},
			pgRawArrayArg{ops},
			pgRawArrayArg{sizeBytes},
			pgRawArrayArg{algVersions},
			pgRawArrayArg{profiles},
			pgRawArrayArg{qualities},
			pgRawArrayArg{keyIDs},
			pgRawArrayArg{canonHMACs},
			pgRawArrayArg{dialectHMACs},
			pgRawArrayArg{shapeHMACs},
			pgRawArrayArg{typedShapes},
			pgRawArrayArg{literalCounts},
			pgRawArrayArg{tupleCounts},
			pgRawArrayArg{paramCounts},
		).Error; err != nil {
			return err
		}
	}
	return nil
}
