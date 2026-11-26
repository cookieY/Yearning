// service.go implements Database AI Assistant session management, message handling, encrypted persistence, and concurrent stream guards.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cookieY/YearningX/backend/internal/assistant/domain"
	"github.com/cookieY/YearningX/backend/internal/assistant/help"
	"github.com/cookieY/YearningX/backend/internal/assistant/persistence"
	"github.com/cookieY/YearningX/backend/internal/assistant/privacy"
	"github.com/cookieY/YearningX/backend/internal/assistant/tool"
	"github.com/cookieY/YearningX/backend/internal/review/fingerprint"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"strings"
	"sync"
	"time"
)

func makeSafeRestoringDeltaHandler(priv *privacy.TurnPrivacySession, emit func(string, any)) (func(string, string), func(string)) {
	onAnon, _, flush := makeTurnDeltaHandlers(priv, emit)
	return onAnon, flush
}

func buildAssistantRawSQLScope(
	ctx context.Context,
	auth *tool.Authorizer,
	ds tool.DatasourceTarget,
	schemaName string,
	rawMeta []SANTableDescription,
) privacy.RawSQLScope {
	schKey := strings.ToLower(strings.TrimSpace(schemaName))
	allowed := make(map[string]map[string]map[string]struct{})
	if schKey != "" {
		tblMap := make(map[string]map[string]struct{}, len(rawMeta))
		for _, tbl := range rawMeta {
			tSch := strings.ToLower(strings.TrimSpace(tbl.Schema))
			if tSch == "" {
				tSch = schKey
			}
			if tSch != schKey {
				continue
			}
			tKey := strings.ToLower(strings.TrimSpace(tbl.Table))
			if tKey == "" {
				continue
			}
			colSet := make(map[string]struct{}, len(tbl.Columns))
			for _, col := range tbl.Columns {
				if cKey := strings.ToLower(strings.TrimSpace(col.Name)); cKey != "" {
					colSet[cKey] = struct{}{}
				}
			}
			tblMap[tKey] = colSet
		}
		allowed[schKey] = tblMap
	}
	var (
		listOnce   sync.Once
		schemaTbls []string
		listErr    error
	)
	return privacy.RawSQLScope{
		AuthorizedSchema:      schemaName,
		AllowedColumnsByTable: allowed,
		LookupTableColumns: func(reqSchema, reqTable string) ([]string, bool) {
			if auth == nil || auth.Catalog == nil || schKey == "" {
				return nil, false
			}
			if reqSchema != "" && !strings.EqualFold(reqSchema, schemaName) {
				return nil, false
			}
			listOnce.Do(func() {
				schemaTbls, listErr = auth.Catalog.ListTables(ctx, ds, schemaName)
			})
			if listErr != nil {
				return nil, false
			}
			matched := ""
			for _, candidate := range schemaTbls {
				if strings.EqualFold(candidate, reqTable) {
					matched = candidate
					break
				}
			}
			if matched == "" {
				return nil, false
			}
			rawCols, cerr := auth.Catalog.ListColumns(ctx, ds, schemaName, matched)
			if cerr != nil {
				return nil, false
			}
			out := make([]string, 0, len(rawCols))
			for _, rc := range rawCols {
				if name := strings.TrimSpace(rc.ColumnName); name != "" {
					out = append(out, name)
				}
			}
			return out, true
		},
	}
}

// SendMessage processes a single user turn in the Database AI Assistant:
//   - enforces per-user concurrent stream mutual exclusion (6007);
//   - creates a request-scoped in-memory TurnPrivacySession and registers defer ZeroAndRelease();
//   - performs capability routing, scope gating (6001), dual-track datasource authorization (6002),
//     controlled metadata loading, per-turn anonymization and zero-leak assertion (6003), token budget
//     and automatic summary compaction (6006), model synthesis/optimization, structured SQL restoration
//     verification (6004), and AEAD envelope persistence.
func (s *Services) SendMessage(ctx context.Context, actor domain.Actor, in SendMessageInput) (*domain.SendMessageResult, error) {
	content := strings.TrimSpace(in.Content)
	if content == "" || len(content) > 16384 {
		return nil, domain.ErrValidation("Message content must be non-empty and at most 16384 bytes")
	}
	if in.CapabilityHint != "" && !domain.ValidCapability(in.CapabilityHint) {
		return nil, domain.ErrValidation("Unsupported capability hint")
	}

	sess, err := s.loadOwnedSession(ctx, s.DB, actor.UserID, in.SessionID)
	if err != nil {
		return nil, err
	}

	// Check per-session message quota (at most 100 messages, i.e., 50 Q&A turns).
	var msgCount int64
	if err := s.DB.WithContext(ctx).Model(&persistence.AIAssistantMessageModel{}).
		Where("session_id = ?", sess.ID).
		Count(&msgCount).Error; err != nil {
		return nil, err
	}
	if msgCount+2 > domain.MaxMessagesPerSession {
		return nil, domain.ErrCapacityExhausted(fmt.Sprintf("Each session supports at most %d messages; start a new session to continue", domain.MaxMessagesPerSession))
	}

	// Per-user concurrent stream lock (6007 ASSISTANT_STREAM_IN_PROGRESS).
	assistantMsgID := uuid.NewString()
	runCtx, cancelStream, acquired := s.acquireActiveStream(ctx, actor.UserID, sess.ID, assistantMsgID)
	if !acquired {
		return nil, domain.ErrStreamInProgress()
	}
	defer s.releaseActiveStream(actor.UserID, assistantMsgID, cancelStream)

	// Per-request in-memory anonymization session: always overwrite and wipe on exit (success, failure, or cancellation)!
	priv := privacy.NewTurnPrivacySession()
	defer priv.ZeroAndRelease()

	hasInputSQL := in.InputSQL != nil && strings.TrimSpace(*in.InputSQL) != ""
	capKind := help.ClassifyCapability(content, in.CapabilityHint, hasInputSQL)
	if capKind == domain.CapabilityOutOfScope {
		return nil, domain.ErrOutOfScope("The Database AI Assistant only supports Platform Usage Help, Natural Language SQL Generation, and SQL Analysis & Optimization; non-database requests are out of scope")
	}

	inputSQL := ""
	if in.InputSQL != nil {
		inputSQL = strings.TrimSpace(*in.InputSQL)
	}
	planText := ""
	if in.ExecutionPlanText != nil {
		planText = strings.TrimSpace(*in.ExecutionPlanText)
	}
	if containsForbiddenStatementScope(content, inputSQL) {
		return nil, domain.ErrOutOfScope("High-risk statements such as TRUNCATE, DROP DATABASE/SCHEMA, and GRANT/REVOKE are not supported by the Database AI Assistant")
	}

	// If this is the first user message and the session title is still default, derive the session title from the first 36 characters (without credentials).
	currentTitle, err := s.openSessionTitle(sess)
	if err != nil {
		currentTitle = defaultSessionTitleEN
	}
	if msgCount == 0 && isDefaultSessionTitle(currentTitle) {
		cand := strings.ReplaceAll(content, "\n", " ")
		if len([]rune(cand)) > 36 {
			cand = string([]rune(cand)[:36])
		}
		currentTitle = cand
	}

	in.emit("progress", map[string]any{"phase": "authorizing_and_anonymizing", "message_id": assistantMsgID})
	in.emit("tool", map[string]any{"tool": "privacy_session.anonymize_and_gate", "status": "running", "capability": string(capKind)})

	// Branch 1: Platform help consultation (invokes model via system prompt scope gate + controlled knowledge base to synthesize Markdown reply and citations).
	if capKind == domain.CapabilityPlatformHelp {
		anonPrompt, aerr := priv.AnonymizeNaturalLanguage(content)
		if aerr != nil {
			return nil, aerr
		}
		existingHistory, herr := s.loadSessionMessagesForContext(runCtx, actor.UserID, sess.ID)
		if herr != nil {
			return nil, herr
		}
		anonHistory, anonSummary, compactedNow, cerr := s.BuildAndCompactHistory(runCtx, sess, existingHistory, priv)
		if cerr != nil {
			return nil, cerr
		}
		in.emit("tool", map[string]any{"tool": "knowledge_corpus.retrieve_articles", "status": "running"})
		var historyCtxBuilder strings.Builder
		if anonSummary != "" {
			historyCtxBuilder.WriteString(anonSummary)
			historyCtxBuilder.WriteByte('\n')
		}
		for _, turn := range anonHistory {
			historyCtxBuilder.WriteString(turn.Content)
			historyCtxBuilder.WriteByte('\n')
		}
		visibleArticles := help.RetrieveRelevantArticles(actor, content, historyCtxBuilder.String(), 4)
		matchedIDs := help.PrimaryMatchedArticleIDs(visibleArticles)
		in.emit("tool", map[string]any{
			"tool":        "knowledge_corpus.retrieve_articles",
			"status":      "completed",
			"count":       len(matchedIDs),
			"matched_ids": matchedIDs,
		})
		synthIn := SynthesizeInput{
			Capability:        domain.CapabilityPlatformHelp,
			OutputLocale:      in.OutputLocale,
			AnonymizedPrompt:  anonPrompt,
			AnonymizedHistory: anonHistory,
			AnonymizedSummary: anonSummary,
			PlatformKnowledge: visibleArticles,
			Actor:             actor,
			RawPrompt:         content,
			RawHistory:        retainRawHistory(existingHistory, anonHistory),
			RawSummary:        anonSummary,
		}
		turnRes, turnErr := s.executeSynthesizeTurn(runCtx, priv, in, assistantMsgID, capKind, content, currentTitle, &synthIn, "")
		if turnErr != nil {
			return nil, turnErr
		}
		currentTitle = turnRes.NextTitle
		citations := help.ResolveCitationsWithLocale(actor, turnRes.Out.CitedArticleIDs, content, in.OutputLocale)
		return s.persistTurnOutcome(runCtx, actor, sess, currentTitle, content, capKind,
			persistedMessagePayload{Content: turnRes.RestoredReply, Citations: citations},
			nil, compactedNow, assistantMsgID)
	}

	// Branch 2 & 3: Natural-language SQL generation (sql_generate) or SQL analysis & optimization (sql_optimize).
	// 1. Resolve target datasource, flow, and schema.
	dsID := firstNonEmptyString(in.DatasourceID, sess.ActiveDatasourceID)
	flowID := firstNonEmptyString(in.FlowID, sess.ActiveFlowID)
	schemaName := firstNonEmptyString(in.SchemaName, sess.ActiveSchemaName)
	targetTable := ""
	if in.TargetTable != nil {
		targetTable = strings.TrimSpace(*in.TargetTable)
	}

	// If no datasource is selected yet, still run anonymization and system prompt scope gating (detecting 6003 credential leaks or 6001 out-of-scope requests, or returning Markdown clarification guidance).
	if dsID == "" {
		anonPrompt, aerr := priv.AnonymizeNaturalLanguage(content)
		if aerr != nil {
			return nil, aerr
		}
		synthIn := SynthesizeInput{
			Capability:        capKind,
			OutputLocale:      in.OutputLocale,
			AnonymizedPrompt:  anonPrompt,
			PlatformKnowledge: help.RetrieveRelevantArticles(actor, content, "", 2),
			Actor:             actor,
			RawPrompt:         content,
		}
		fallbackClarify := missingDatasourceClarifyMarkdown(in.OutputLocale)
		turnRes, turnErr := s.executeSynthesizeTurn(runCtx, priv, in, assistantMsgID, capKind, content, currentTitle, &synthIn, fallbackClarify)
		if turnErr != nil {
			return nil, turnErr
		}
		currentTitle = turnRes.NextTitle
		return s.persistTurnOutcome(runCtx, actor, sess, currentTitle, content, capKind,
			persistedMessagePayload{Content: turnRes.RestoredReply},
			nil, false, assistantMsgID)
	}

	// 2. Determine whether change order submission permission is required (DML / DDL / order submission requires change_review grant; read-only SELECT allows query grant).
	requireChangeGrant := requiresChangeFlowPermission(capKind, content, inputSQL)

	// 3. Server-side dual-track permission gate (immediately returns 6002 ASSISTANT_DATASOURCE_FORBIDDEN if unauthorized, before invoking any tool).
	dsTarget, err := s.Authorizer.AuthorizeDatasource(runCtx, actor.UserID, dsID, flowID, requireChangeGrant)
	if err != nil {
		_ = s.InvalidateSessionSummaries(runCtx, s.DB, sess.ID)
		return nil, err
	}
	if flowID == "" && dsTarget.ResolvedFlowID != "" {
		flowID = dsTarget.ResolvedFlowID
	}
	if schemaName == "" {
		schemaName = dsTarget.DefaultDatabase
		if schemaName == "" && dsTarget.CompatibilityMode == "postgresql" {
			schemaName = "public"
		}
	}

	profile, err := fingerprint.ParseProfile(dsTarget.Engine + "/" + dsTarget.CompatibilityMode)
	if err != nil {
		profile = fingerprint.Profile{Engine: "mysql", Mode: "mysql"}
		if dsTarget.CompatibilityMode == "postgresql" {
			profile = fingerprint.Profile{Engine: "postgresql", Mode: "postgresql"}
		}
	}

	// 4. If an existing draft is selected and inputSQL is not explicitly provided in optimization mode, load it before table discovery
	// so table-scoped metadata and internal experience also match the draft's tables.
	if inputSQL == "" && capKind == domain.CapabilitySQLOptimize {
		selectedID := firstNonEmptyString(in.SelectedDraftID, sess.SelectedDraftID)
		if selectedID != "" {
			if _, dp, derr := s.loadOwnedDraft(runCtx, s.DB, actor.UserID, sess.ID, selectedID); derr == nil {
				inputSQL = dp.SQLText
			}
		}
	}

	// 5. Load column, type, nullability, default behavior, and index metadata for referenced tables via controlled read-only tools within the authorized scope.
	in.emit("tool", map[string]any{"tool": "catalog_inspector.load_schema_metadata", "status": "running", "schema": schemaName})
	tablesToDescribe, catalogTables := s.resolveTablesAndCatalogToDescribe(runCtx, dsTarget, schemaName, targetTable, inputSQL, content, profile)

	var anonMetadata []*tool.AnonymizedTableDescription
	describedSet := make(map[string]bool, len(tablesToDescribe))
	for _, tblName := range tablesToDescribe {
		desc, derr := s.Authorizer.DescribeAuthorizedTable(runCtx, priv, dsTarget, schemaName, tblName)
		if derr != nil {
			return nil, derr
		}
		describedSet[strings.ToLower(strings.TrimSpace(tblName))] = true
		anonMetadata = append(anonMetadata, desc)
	}
	in.emit("tool", map[string]any{"tool": "catalog_inspector.load_schema_metadata", "status": "completed", "schema": schemaName, "tables": len(anonMetadata)})

	metaBytes, _ := json.Marshal(anonMetadata)
	if err := s.CheckSingleInputBudget(content, inputSQL, planText, string(metaBytes)); err != nil {
		return nil, err
	}

	var extraCatalogTables []string
	for _, tbl := range catalogTables {
		clean := strings.TrimSpace(tbl)
		if clean != "" && !describedSet[strings.ToLower(clean)] {
			extraCatalogTables = append(extraCatalogTables, clean)
		}
	}

	// 5b. Load enabled datasource/table internal experience within the authorized scope (skipping empty schemaName and global entries).
	var anonKnowledge []AssistantKnowledgeItem
	var rawKnowledge []AssistantKnowledgeItem
	if schemaName != "" {
		if _, rerr := priv.RegisterSchema(schemaName); rerr != nil {
			return nil, rerr
		}
		matchedEntries, kerr := (&knowledge.Repository{DB: s.DB}).ListEnabledForScope(runCtx, dsTarget.DatasourceID, schemaName, tablesToDescribe)
		if kerr != nil {
			return nil, kerr
		}
		selectedEntries, truncatedCount := selectAndTrimAssistantKnowledge(matchedEntries, content, inputSQL, tablesToDescribe)
		if len(matchedEntries) > 0 || truncatedCount > 0 {
			zap.L().Info("assistant_knowledge_matched",
				zap.String("session_id", sess.ID),
				zap.Int("matched_count", len(matchedEntries)),
				zap.Int("injected_count", len(selectedEntries)),
				zap.Int("truncated_count", truncatedCount),
			)
		}
		if len(selectedEntries) > 0 {
			anonKnowledge, err = buildDefaultAssistantKnowledge(priv, selectedEntries, extraCatalogTables)
			if err != nil {
				return nil, err
			}
			rawKnowledge = buildSANAssistantKnowledge(selectedEntries)
		}
	}

	// 6. Anonymize the current input SQL, execution plan, and natural-language prompt uniformly using this turn's TurnPrivacySession.
	var anonInputSQL string
	if inputSQL != "" {
		anonInputSQL, err = priv.AnonymizeSQL(inputSQL, profile)
		if err != nil {
			return nil, err
		}
	}
	var anonPlan string
	if planText != "" {
		anonPlan, err = priv.AnonymizeNaturalLanguage(planText)
		if err != nil {
			return nil, err
		}
	}
	anonPrompt, err := priv.AnonymizeNaturalLanguage(content)
	if err != nil {
		return nil, err
	}

	// 7. Load session history, automatically compact earlier turns within budget, and anonymize history entering the model using this turn's TurnPrivacySession.
	existingHistory, err := s.loadSessionMessagesForContext(runCtx, actor.UserID, sess.ID)
	if err != nil {
		return nil, err
	}
	anonHistory, anonSummary, compactedNow, err := s.BuildAndCompactHistory(runCtx, sess, existingHistory, priv)
	if err != nil {
		return nil, err
	}

	// Collect bound value placeholders in order for generative draft binding.
	boundPhs := extractPlaceholdersInOrder(anonPrompt + " " + anonInputSQL)

	synthIn := SynthesizeInput{
		Capability:          capKind,
		EngineProfile:       profile.String(),
		OutputLocale:        in.OutputLocale,
		AnonymizedPrompt:    anonPrompt,
		AnonymizedInputSQL:  anonInputSQL,
		AnonymizedPlanText:  anonPlan,
		AnonymizedMetadata:  anonMetadata,
		AnonymizedKnowledge: anonKnowledge,
		AnonymizedHistory:   anonHistory,
		AnonymizedSummary:   anonSummary,
		BoundPlaceholders:   boundPhs,
		PlatformKnowledge:   help.RetrieveRelevantArticles(actor, content, "", 2),
		Actor:               actor,
		RawPrompt:           content,
		RawInputSQL:         inputSQL,
		RawPlanText:         planText,
		RawMetadata:         restoreSANMetadata(priv, anonMetadata),
		RawKnowledge:        rawKnowledge,
		RawHistory:          retainRawHistory(existingHistory, anonHistory),
		RawSummary:          anonSummary,
	}

	// 8–9. Zero-leak outbound check, daily AI budget reservation, synthesis execution, settlement, and token restoration.
	turnRes, err := s.executeSynthesizeTurn(runCtx, priv, in, assistantMsgID, capKind, content, currentTitle, &synthIn, "")
	if err != nil {
		return nil, err
	}
	synthOut := turnRes.Out
	restoredReply := turnRes.RestoredReply
	currentTitle = turnRes.NextTitle

	// 10. If the model returned an SQL draft, run ValidateRawAssistantSQL (for SAN) or ValidateAndRestoreSQL (for default)
	// for dialect parsing, statement boundary, authorized object, and parameter binding verification before structured restoration (6004).
	var draftSpec *newDraftSpec
	if strings.TrimSpace(synthOut.AnonymizedSQL) != "" && !synthOut.NeedsClarify {
		in.emit("tool", map[string]any{"tool": "sql_validator.restore_and_verify_ast", "status": "running", "engine": dsTarget.Engine})
		var (
			restored *privacy.RestorationResult
			rerr     error
		)
		if synthOut.UsedSANProfile {
			rawScope := buildAssistantRawSQLScope(runCtx, s.Authorizer, dsTarget, schemaName, synthIn.RawMetadata)
			restored, rerr = privacy.ValidateRawAssistantSQL(synthOut.AnonymizedSQL, profile, rawScope, in.OutputLocale)
		} else {
			restored, rerr = privacy.ValidateAndRestoreSQL(priv, synthOut.AnonymizedSQL, profile, in.OutputLocale)
		}
		if rerr != nil {
			zap.L().Warn("assistant_sql_restoration_rejected",
				zap.String("session_id", sess.ID),
				zap.String("capability", string(capKind)),
				zap.String("engine", dsTarget.Engine),
				zap.Bool("used_san_profile", synthOut.UsedSANProfile),
				zap.String("reason", sanitizeRestorationLogReason(rerr)),
			)
			return nil, rerr
		}
		// If the restored statement is DML/DDL, double-check that the user holds a change_review grant.
		if restored.RiskCategory != domain.RiskReadOnly && !dsTarget.HasChangeGrant {
			return nil, domain.ErrDatasourceForbidden()
		}
		in.emit("tool", map[string]any{"tool": "sql_validator.restore_and_verify_ast", "status": "completed", "engine": dsTarget.Engine, "statement_kind": string(restored.StatementKind)})
		semanticWarningsIn := synthOut.SemanticWarnings
		assumptionsIn := synthOut.Assumptions
		nextStepsIn := synthOut.NextSteps
		if !synthOut.UsedSANProfile {
			semanticWarningsIn = priv.RestoreTextSlice(semanticWarningsIn)
			assumptionsIn = priv.RestoreTextSlice(assumptionsIn)
			nextStepsIn = priv.RestoreTextSlice(nextStepsIn)
		}
		warnings := append([]string{}, semanticWarningsIn...)
		warnings = append(warnings, restored.SemanticWarnings...)
		draftSpec = &newDraftSpec{
			DatasourceID:   dsTarget.DatasourceID,
			DatasourceName: dsTarget.DatasourceName,
			FlowID:         optionalStringPtr(flowID),
			Engine:         dsTarget.Engine,
			SchemaName:     schemaName,
			StatementKind:  string(restored.StatementKind),
			RiskCategory:   string(restored.RiskCategory),
			Payload: persistedDraftPayload{
				SQLText:          restored.RestoredSQL,
				Assumptions:      ensureStringSlice(assumptionsIn),
				NextSteps:        ensureStringSlice(nextStepsIn),
				SemanticWarnings: ensureStringSlice(warnings),
				ReferencedTables: ensureStringSlice(restored.ReferencedTables),
			},
		}
	}

	// Update active session context bindings.
	sess.ActiveDatasourceID = &dsTarget.DatasourceID
	if flowID != "" {
		sess.ActiveFlowID = &flowID
	}
	if schemaName != "" {
		sess.ActiveSchemaName = &schemaName
	}

	return s.persistTurnOutcome(runCtx, actor, sess, currentTitle, content, capKind,
		persistedMessagePayload{Content: restoredReply},
		draftSpec, compactedNow, assistantMsgID)
}

// sanitizeRestorationLogReason extracts the fixed failure category from a 6004 domain error
// while stripping any suffix after ":" so raw SQL, literals, or identifiers never reach logs.
func sanitizeRestorationLogReason(err error) string {
	var de *domain.Error
	if !errors.As(err, &de) || de.Meta == nil {
		return "unknown"
	}
	raw, _ := de.Meta["reason"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}
	if idx := strings.IndexByte(raw, ':'); idx > 0 {
		return raw[:idx]
	}
	return raw
}

func scrubAndAssertSynthesizeInput(priv *privacy.TurnPrivacySession, in *SynthesizeInput) error {
	in.AnonymizedPrompt = priv.ScrubRegisteredTerms(in.AnonymizedPrompt)
	in.AnonymizedPlanText = priv.ScrubRegisteredTerms(in.AnonymizedPlanText)
	in.AnonymizedSummary = priv.ScrubRegisteredTerms(in.AnonymizedSummary)
	for i := range in.AnonymizedHistory {
		in.AnonymizedHistory[i].Content = priv.ScrubRegisteredTerms(in.AnonymizedHistory[i].Content)
	}
	for _, v := range []string{
		in.AnonymizedPrompt,
		in.AnonymizedInputSQL,
		in.AnonymizedPlanText,
		in.AnonymizedSummary,
	} {
		if v == "" {
			continue
		}
		if err := priv.AssertZeroLeak(v); err != nil {
			return err
		}
	}
	for _, t := range in.AnonymizedHistory {
		if t.Content == "" {
			continue
		}
		if err := priv.AssertZeroLeak(t.Content); err != nil {
			return err
		}
	}
	for _, m := range in.AnonymizedMetadata {
		if m == nil {
			continue
		}
		for _, tok := range []string{m.SchemaToken, m.TableToken} {
			if tok != "" {
				if err := priv.AssertZeroLeak(tok); err != nil {
					return err
				}
			}
		}
		for _, c := range m.Columns {
			if err := priv.AssertZeroLeak(c.ColumnToken); err != nil {
				return err
			}
		}
		for _, idx := range m.Indexes {
			if err := priv.AssertZeroLeak(idx.IndexToken); err != nil {
				return err
			}
		}
	}
	for _, item := range in.AnonymizedKnowledge {
		for _, field := range []string{item.Name, item.Text} {
			if field == "" {
				continue
			}
			if err := priv.AssertZeroLeak(field); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Services) persistTurnOutcome(
	ctx context.Context,
	actor domain.Actor,
	sess *persistence.AIAssistantSessionModel,
	sessionTitle string,
	userContent string,
	capKind domain.Capability,
	assistantPayload persistedMessagePayload,
	draftSpec *newDraftSpec,
	compactedNow bool,
	assistantMsgID string,
) (*domain.SendMessageResult, error) {
	now := time.Now().UTC()
	userMsgID := uuid.NewString()

	userEnv, err := s.sealMessagePayload(userMsgID, persistedMessagePayload{Content: userContent})
	if err != nil {
		return nil, err
	}
	asstEnv, err := s.sealMessagePayload(assistantMsgID, assistantPayload)
	if err != nil {
		return nil, err
	}
	titleEnv, err := s.sealSessionTitle(sess.ID, sessionTitle)
	if err != nil {
		return nil, err
	}

	var (
		userMsgRow  persistence.AIAssistantMessageModel
		asstMsgRow  persistence.AIAssistantMessageModel
		draftRow    *persistence.AIAssistantSQLDraftModel
		draftViewPt *domain.SQLDraftView
	)

	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int64
		if err := tx.Model(&persistence.AIAssistantMessageModel{}).
			Where("session_id = ?", sess.ID).
			Select("coalesce(max(sequence), 0)").
			Scan(&maxSeq).Error; err != nil {
			return err
		}

		userMsgRow = persistence.AIAssistantMessageModel{
			ID:                userMsgID,
			SessionID:         sess.ID,
			UserID:            actor.UserID,
			Sequence:          maxSeq + 1,
			Role:              string(domain.RoleUser),
			Capability:        string(capKind),
			State:             string(domain.MessageCompleted),
			ContentCiphertext: userEnv.Ciphertext,
			ContentNonce:      userEnv.Nonce,
			ContentKeyID:      userEnv.KeyID,
			ContentAlgorithm:  userEnv.Algorithm,
			Version:           1,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if err := tx.Create(&userMsgRow).Error; err != nil {
			return err
		}

		// Create the assistant message row with sql_draft_id = nil first (satisfying FK ai_assistant_sql_drafts.message_id -> ai_assistant_messages.id).
		asstMsgRow = persistence.AIAssistantMessageModel{
			ID:                assistantMsgID,
			SessionID:         sess.ID,
			UserID:            actor.UserID,
			Sequence:          maxSeq + 2,
			Role:              string(domain.RoleAssistant),
			Capability:        string(capKind),
			State:             string(domain.MessageCompleted),
			ContentCiphertext: asstEnv.Ciphertext,
			ContentNonce:      asstEnv.Nonce,
			ContentKeyID:      asstEnv.KeyID,
			ContentAlgorithm:  asstEnv.Algorithm,
			Version:           1,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if err := tx.Create(&asstMsgRow).Error; err != nil {
			return err
		}

		if draftSpec != nil {
			var maxRev int64
			if err := tx.Model(&persistence.AIAssistantSQLDraftModel{}).
				Where("session_id = ?", sess.ID).
				Select("coalesce(max(revision), 0)").
				Scan(&maxRev).Error; err != nil {
				return err
			}
			// Mark earlier unsubmitted drafts in this session as stale (preventing submission of outdated draft revisions).
			if maxRev > 0 {
				if err := tx.Model(&persistence.AIAssistantSQLDraftModel{}).
					Where("session_id = ? and submission_state != ?", sess.ID, string(domain.SubmissionOrderSubmitted)).
					Updates(map[string]any{"submission_state": string(domain.SubmissionStale), "updated_at": now}).Error; err != nil {
					return err
				}
			}

			dID := uuid.NewString()
			dEnv, serr := s.sealDraftPayload(dID, draftSpec.Payload)
			if serr != nil {
				return serr
			}
			macBytes, macKeyID, herr := s.computeSQLHMAC(draftSpec.Payload.SQLText)
			if herr != nil {
				return herr
			}
			dr := persistence.AIAssistantSQLDraftModel{
				ID:              dID,
				SessionID:       sess.ID,
				MessageID:       assistantMsgID,
				UserID:          actor.UserID,
				Revision:        maxRev + 1,
				DatasourceID:    draftSpec.DatasourceID,
				FlowID:          draftSpec.FlowID,
				Engine:          draftSpec.Engine,
				SchemaName:      draftSpec.SchemaName,
				StatementKind:   draftSpec.StatementKind,
				RiskCategory:    draftSpec.RiskCategory,
				SQLCiphertext:   dEnv.Ciphertext,
				SQLNonce:        dEnv.Nonce,
				SQLKeyID:        dEnv.KeyID,
				SQLAlgorithm:    dEnv.Algorithm,
				SQLHMAC:         macBytes,
				SQLHMACKeyID:    macKeyID,
				SubmissionState: string(domain.SubmissionDraft),
				Version:         1,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			if err := tx.Create(&dr).Error; err != nil {
				return err
			}
			if err := tx.Model(&persistence.AIAssistantMessageModel{}).
				Where("id = ?", assistantMsgID).
				Update("sql_draft_id", dID).Error; err != nil {
				return err
			}
			asstMsgRow.SQLDraftID = &dID
			sess.SelectedDraftID = &dID
			draftRow = &dr
		}

		if err := tx.Model(&persistence.AIAssistantSessionModel{}).
			Where("id = ?", sess.ID).
			Updates(map[string]any{
				"title_ciphertext":     titleEnv.Ciphertext,
				"title_nonce":          titleEnv.Nonce,
				"title_key_id":         titleEnv.KeyID,
				"title_algorithm":      titleEnv.Algorithm,
				"active_datasource_id": sess.ActiveDatasourceID,
				"active_flow_id":       sess.ActiveFlowID,
				"active_schema_name":   sess.ActiveSchemaName,
				"selected_draft_id":    sess.SelectedDraftID,
				"version":              sess.Version + 1,
				"updated_at":           now,
			}).Error; err != nil {
			return err
		}
		sess.Version++
		sess.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, err
	}

	if draftRow != nil && draftSpec != nil {
		dsName := draftSpec.DatasourceName
		dv := s.draftView(draftRow, draftSpec.Payload, &dsName)
		draftViewPt = &dv
	}

	userView := s.messageView(&userMsgRow, persistedMessagePayload{Content: userContent}, nil)
	asstView := s.messageView(&asstMsgRow, assistantPayload, draftViewPt)
	sessView := s.sessionView(sess, sessionTitle)

	return &domain.SendMessageResult{
		Session:          sessView,
		UserMessage:      userView,
		AssistantMessage: asstView,
		SQLDraft:         draftViewPt,
		ContextCompacted: compactedNow,
	}, nil
}
