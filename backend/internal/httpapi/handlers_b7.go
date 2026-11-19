// handlers_b7.go B7 management surface: Prompt Tools (review skills), AI Providers,
// and internal knowledge base entries. Restricted to builtin_admin (authorization-policy
// builtin-admin-management rule); state-changing requests are covered by router-level CSRF guard.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/review/application"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
)

// B7Deps aggregates B7 management dependencies (reusing B6 session guards and wiring infrastructure).
type B7Deps struct {
	Review    *application.Services
	Knowledge *knowledge.Service
}

// writeB7Err maps domain errors to business errors; returns false for non-domain errors (500).
func writeB7Err(c *gin.Context, err error) bool {
	be := MapReviewError(err)
	if be == nil {
		return false
	}
	WriteBusinessError(c, be)
	return true
}

// MapReviewError maps review domain errors to BusinessError (shared by B6 and B7).
// domain.Error.Detail (upstream rejection reason snapshot, S18.75-A) is passed through meta.detail to the UI;
// Fields (field-level rejection reasons, RCP-20261102-S008) is passed through meta.fields for form field targeting.
func MapReviewError(err error) *BusinessError {
	var de *domain.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &de):
		be := &BusinessError{Code: de.Code, Name: de.Name, Message: de.Name}
		if de.Detail != "" || len(de.Fields) > 0 {
			meta := map[string]any{}
			if de.Detail != "" {
				meta["detail"] = de.Detail
			}
			if len(de.Fields) > 0 {
				meta["fields"] = de.Fields
			}
			be.Meta = meta
		}
		return be
	case errors.Is(err, application.ErrNotFound), errors.Is(err, knowledge.ErrNotFound):
		return &BusinessError{Code: bizerr.CodeResourceNotFound, Name: "NOT_FOUND", Message: "Resource not found"}
	case errors.Is(err, application.ErrVersionConflict), errors.Is(err, knowledge.ErrVersionConflict):
		return &BusinessError{Code: bizerr.CodeVersionConflict, Name: "VERSION_CONFLICT", Message: "Resource version conflict; please refresh and retry"}
	case errors.Is(err, application.ErrConcurrent), errors.Is(err, knowledge.ErrConcurrent):
		return &BusinessError{Code: bizerr.CodeConcurrentModification, Name: "CONCURRENT_MODIFICATION", Message: "Concurrent modification conflict; please retry"}
	case errors.Is(err, application.ErrReferencedTool), errors.Is(err, knowledge.ErrReferenced):
		return &BusinessError{Code: bizerr.CodeResourceReferenced, Name: "REFERENCED", Message: "Resource is referenced and cannot be deleted"}
	case errors.Is(err, application.ErrRuleSetReferenced):
		return &BusinessError{Code: bizerr.CodeResourceReferenced, Name: "REFERENCED", Message: "Resource is referenced and cannot be deleted"}
	case errors.Is(err, knowledge.ErrValidation):
		return &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"}
	default:
		return nil
	}
}

// ---- Prompt Tool (review skill) ----

type promptToolWriteRequest struct {
	Name        string                           `json:"name" binding:"required"`
	Description string                           `json:"description"`
	State       string                           `json:"state"`
	Engine      string                           `json:"engine"`
	Parameters  map[string]any                   `json:"parameters"`
	Definition  application.PromptToolDefinition `json:"definition"`
}

func (d *B7Deps) HandleListPromptTools(c *gin.Context) {
	items, next, err := d.Review.ListPromptTools(c.Request.Context(), intQuery(c, "limit", 50), c.Query("after"), c.Query("q"))
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

func (d *B7Deps) HandleCreatePromptTool(c *gin.Context) {
	var req promptToolWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Review.CreatePromptToolForEngine(c.Request.Context(), b6Actor(c).UserID,
		req.Name, req.State, req.Engine, req.Parameters, req.Definition, req.Description)
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B7Deps) HandleGetPromptTool(c *gin.Context) {
	view, err := d.Review.GetPromptTool(c.Request.Context(), c.Param("prompt_tool_id"))
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B7Deps) HandleReplacePromptTool(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	var req promptToolWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Review.ReplacePromptToolForEngine(c.Request.Context(), c.Param("prompt_tool_id"), version,
		req.Name, req.State, req.Engine, req.Parameters, req.Definition, req.Description)
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B7Deps) HandleDeletePromptTool(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	if err := d.Review.DeletePromptTool(c.Request.Context(), c.Param("prompt_tool_id"), version); err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

func (d *B7Deps) HandleOptimizePromptToolPrompt(c *gin.Context) {
	var req application.OptimizePromptToolInput
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "invalid request payload"})
		return
	}
	out, err := d.Review.OptimizePromptToolPrompt(c.Request.Context(), req)
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, out)
}

func (d *B7Deps) HandleGeneratePromptToolTestCases(c *gin.Context) {
	var req application.GeneratePromptToolTestCasesInput
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "invalid request payload"})
		return
	}
	out, err := d.Review.GeneratePromptToolTestCases(c.Request.Context(), req)
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, out)
}

func (d *B7Deps) HandleRunPromptToolTest(c *gin.Context) {
	var req application.RunPromptToolTestInput
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "invalid request payload"})
		return
	}
	out, err := d.Review.RunPromptToolTest(c.Request.Context(), req)
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, out)
}

// ---- Rule Set (S18.73 route gap wired in this package) ----

type ruleSetWriteRequest struct {
	Name          string          `json:"name" binding:"required"`
	Enabled       bool            `json:"enabled"`
	Rules         json.RawMessage `json:"rules"`
	PromptToolIDs []string        `json:"prompt_tool_ids"`
}

func (d *B7Deps) HandleListRuleSets(c *gin.Context) {
	limit := intQuery(c, "limit", 50)
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	items, next, err := d.Review.ListRuleSets(c.Request.Context(), limit, c.Query("after"), c.Query("q"))
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

func (d *B7Deps) HandleCreateRuleSet(c *gin.Context) {
	var req ruleSetWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Review.CreateRuleSet(c.Request.Context(), b6Actor(c).UserID,
		req.Name, req.Enabled, req.Rules, req.PromptToolIDs)
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B7Deps) HandleGetRuleSet(c *gin.Context) {
	view, err := d.Review.GetRuleSet(c.Request.Context(), c.Param("rule_set_id"))
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B7Deps) HandleReplaceRuleSet(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	var req ruleSetWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Review.ReplaceRuleSet(c.Request.Context(), c.Param("rule_set_id"), version,
		req.Name, req.Enabled, req.Rules, req.PromptToolIDs)
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B7Deps) HandleDeleteRuleSet(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	if err := d.Review.DeleteRuleSet(c.Request.Context(), c.Param("rule_set_id"), version); err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

// ---- AI Provider ----

// secretInputRequest corresponds to contract SecretInput ({value: string}). F12 #001:
// api_key was previously bound as a flat string in violation of OpenAPI; unpack per contract shape here
// while the application layer continues consuming the plaintext string.
type secretInputRequest struct {
	Value string `json:"value"`
}

type aiProviderWriteRequest struct {
	Name              string              `json:"name" binding:"required"`
	ProviderKind      string              `json:"provider_kind" binding:"required"`
	BaseURL           string              `json:"base_url" binding:"required"`
	ModelName         string              `json:"model_name" binding:"required"`
	Enabled           bool                `json:"enabled"`
	SelectionPriority int                 `json:"selection_priority"`
	APIKey            *secretInputRequest `json:"api_key"`
	Configuration     map[string]any      `json:"configuration"`
}

// apiKeyValue returns the unpacked plaintext api_key; nil/empty is treated as "not provided" (rejected
// by caller on create, or keeps existing secret on replace).
func (r *aiProviderWriteRequest) apiKeyValue() string {
	if r.APIKey == nil {
		return ""
	}
	return r.APIKey.Value
}

func (d *B7Deps) HandleListAiProviders(c *gin.Context) {
	items, next, err := d.Review.ListAIProviders(c.Request.Context(), intQuery(c, "limit", 50), c.Query("after"), c.Query("q"))
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

func (d *B7Deps) HandleCreateAiProvider(c *gin.Context) {
	var req aiProviderWriteRequest
	// Contract requires api_key to be a SecretInput with a non-empty value; empty value is an invalid payload
	// rather than a "keep existing secret" instruction (R1 review: silent normalization masks client bugs).
	if err := c.ShouldBindJSON(&req); err != nil || req.APIKey == nil || req.APIKey.Value == "" {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Review.CreateAIProvider(c.Request.Context(), b6Actor(c).UserID, application.CreateAIProviderInput{
		Name: req.Name, ProviderKind: req.ProviderKind, BaseURL: req.BaseURL,
		ModelName: req.ModelName, Enabled: req.Enabled, SelectionPriority: req.SelectionPriority,
		APIKey: req.apiKeyValue(), Configuration: req.Configuration,
	})
	if err != nil {
		// S18.75-B: attach error details to c.Errors (whether mapped or not) so access log
		// middleware emits a warn-level detail line; Problem response still leaks no internal details.
		_ = c.Error(err)
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B7Deps) HandleGetAiProvider(c *gin.Context) {
	view, err := d.Review.GetAIProvider(c.Request.Context(), c.Param("ai_provider_id"))
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B7Deps) HandleReplaceAiProvider(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	var req aiProviderWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	// Omitted api_key (nil) = keep existing secret (B13 credential keep semantics); explicit empty value
	// is rejected as validation failure rather than silently normalized to keep (R1 review).
	if req.APIKey != nil && req.APIKey.Value == "" {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	view, err := d.Review.ReplaceAIProvider(c.Request.Context(), c.Param("ai_provider_id"), application.ReplaceAIProviderInput{
		ExpectVersion: version,
		Name:          req.Name, ProviderKind: req.ProviderKind, BaseURL: req.BaseURL,
		ModelName: req.ModelName, Enabled: req.Enabled, SelectionPriority: req.SelectionPriority,
		APIKey: req.apiKeyValue(), Configuration: req.Configuration,
	})
	if err != nil {
		// S18.75-B: same as create -- attach error details on both branches.
		_ = c.Error(err)
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

func (d *B7Deps) HandleDeleteAiProvider(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	if err := d.Review.DeleteAIProvider(c.Request.Context(), c.Param("ai_provider_id"), version); err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

// HandleTestAiProviderConnection runs a targeted health probe on the specified Provider
// (H5: targeted by path parameter rather than whole chain; result contains only conclusion, omitting secrets and endpoint payload).
func (d *B7Deps) HandleTestAiProviderConnection(c *gin.Context) {
	err := d.Review.TestAIProviderConnection(c.Request.Context(), c.Param("ai_provider_id"))
	if err != nil {
		// S18.75-B: same as create -- attach error details on both branches.
		_ = c.Error(err)
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, gin.H{"ok": true})
}

// providerModelCatalogRequest payload for fetching model catalog (S18.76-B). Uses POST rather than
// GET: api_key is a plaintext credential that must not enter URLs or access logs; provider_id is provided
// only in edit mode (where secret is not echoed) to reuse the stored secret.
type providerModelCatalogRequest struct {
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key"`
	ProviderID string `json:"provider_id"`
}

// HandleListAiProviderModels fetches upstream model catalog for form autocomplete; upstream rejection
// reason is surfaced via domain.Error.Detail (same channel as S18.75-A).
func (d *B7Deps) HandleListAiProviderModels(c *gin.Context) {
	var req providerModelCatalogRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	models, err := d.Review.ListProviderModels(c.Request.Context(), req.BaseURL, req.APIKey, req.ProviderID)
	if err != nil {
		_ = c.Error(err)
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, gin.H{"items": models})
}

// ---- Internal knowledge base ----

type knowledgeEntryWriteRequest struct {
	Name            string               `json:"name" binding:"required"`
	Purpose         string               `json:"purpose"`
	State           string               `json:"state"`
	ScopeType       string               `json:"scope_type" binding:"required"`
	DatasourceID    string               `json:"datasource_id"`
	DatabaseName    string               `json:"database_name"`
	TableName       string               `json:"table_name"`
	Definition      knowledge.Definition `json:"definition"`
	Provenance      string               `json:"provenance"`
	SourceFindingID string               `json:"source_finding_id"`
}

func (d *B7Deps) HandleListKnowledgeEntries(c *gin.Context) {
	limit := intQuery(c, "limit", 50)
	items, err := d.Knowledge.Repo.List(c.Request.Context(), limit+1, c.Query("after"), c.Query("q"))
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		v := knowledge.CursorOf(items[len(items)-1])
		next = &v
	}
	writePage(c, items, next)
}

func (d *B7Deps) HandleCreateKnowledgeEntry(c *gin.Context) {
	var req knowledgeEntryWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	entry, err := d.Knowledge.Create(c.Request.Context(), b6Actor(c).UserID, knowledge.CreateInput{
		Name: req.Name, Purpose: req.Purpose, State: knowledge.State(req.State), ScopeType: knowledge.ScopeType(req.ScopeType),
		DatasourceID: req.DatasourceID, DatabaseName: req.DatabaseName, TableName: req.TableName,
		Definition: req.Definition, Provenance: knowledge.Provenance(req.Provenance),
		SourceFindingID: req.SourceFindingID,
	})
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, entry)
}

func (d *B7Deps) HandleGetKnowledgeEntry(c *gin.Context) {
	entry, err := d.Knowledge.Repo.Get(c.Request.Context(), c.Param("knowledge_entry_id"))
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, entry)
}

func (d *B7Deps) HandleReplaceKnowledgeEntry(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	var req knowledgeEntryWriteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	entry, err := d.Knowledge.Replace(c.Request.Context(), c.Param("knowledge_entry_id"), knowledge.ReplaceInput{
		ExpectVersion: version,
		Name:          req.Name, Purpose: req.Purpose, State: knowledge.State(req.State),
		ScopeType:    knowledge.ScopeType(req.ScopeType),
		DatasourceID: req.DatasourceID, DatabaseName: req.DatabaseName, TableName: req.TableName,
		Definition: req.Definition,
	})
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, entry)
}

func (d *B7Deps) HandleDeleteKnowledgeEntry(c *gin.Context) {
	version, ok := parseIfMatch(c)
	if !ok {
		return
	}
	if err := d.Knowledge.Delete(c.Request.Context(), c.Param("knowledge_entry_id"), version); err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, nil)
}

// HandleEvaluateKnowledgeEntry runs activation gates and returns the evaluation report
// (Schema subset / privacy scan / injection check / severity consistency).
func (d *B7Deps) HandleEvaluateKnowledgeEntry(c *gin.Context) {
	entry, err := d.Knowledge.Repo.Get(c.Request.Context(), c.Param("knowledge_entry_id"))
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, knowledge.RunEval(entry))
}

// rateVersionWindowRequest S008 rate window write payload (prices are restricted decimal
// strings -- fixed-point semantics rejecting binary floating-point drift; cross-midnight start>end
// is normalized server-side into two same-day window rows).
type rateVersionWindowRequest struct {
	StartMinute      int     `json:"start_minute"`
	EndMinute        int     `json:"end_minute"`
	InputPrice       string  `json:"input_price"`
	OutputPrice      string  `json:"output_price"`
	CachedReadPrice  *string `json:"cached_read_price"`
	CachedWritePrice *string `json:"cached_write_price"`
}

type rateVersionCreateRequest struct {
	Currency       string                     `json:"currency"`
	Timezone       string                     `json:"timezone"`
	EffectiveFrom  string                     `json:"effective_from"`
	EffectiveUntil *string                    `json:"effective_until"`
	Windows        []rateVersionWindowRequest `json:"windows"`
}

// parseRateWindowRequest converts fixed-point price strings to micro-units; invalid values are rejected with 1001
// (never silently truncated).
// priceFieldError attaches price parse failures to the specific field (RCP-20261102-S008:
// server-side field targeting via meta.fields while preserving detail).
func priceFieldError(field string, err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return &domain.Error{Code: de.Code, Name: de.Name, Detail: de.Detail,
			Fields: map[string][]string{field: {de.Detail}}}
	}
	return err
}

func parseRateWindowRequest(w rateVersionWindowRequest) (application.RateWindowInput, error) {
	out := application.RateWindowInput{StartMinute: w.StartMinute, EndMinute: w.EndMinute}
	var err error
	if out.InputPriceMicro, err = application.ParsePriceMicro(w.InputPrice); err != nil {
		return out, priceFieldError("input_price", err)
	}
	if out.OutputPriceMicro, err = application.ParsePriceMicro(w.OutputPrice); err != nil {
		return out, priceFieldError("output_price", err)
	}
	if w.CachedReadPrice != nil {
		v, err := application.ParsePriceMicro(*w.CachedReadPrice)
		if err != nil {
			return out, priceFieldError("cached_read_price", err)
		}
		out.CachedReadPriceMicro = &v
	}
	if w.CachedWritePrice != nil {
		v, err := application.ParsePriceMicro(*w.CachedWritePrice)
		if err != nil {
			return out, priceFieldError("cached_write_price", err)
		}
		out.CachedWritePriceMicro = &v
	}
	return out, nil
}

// HandleCreateAiProviderRateVersion S008: creates a rate version (append-only new version).
func (d *B7Deps) HandleCreateAiProviderRateVersion(c *gin.Context) {
	var req rateVersionCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
		return
	}
	from, err := time.Parse(time.RFC3339, req.EffectiveFrom)
	if err != nil {
		// RCP-20261102-S008: parse failure carries field targeting (meta.fields).
		WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters",
			Meta: map[string]any{"fields": map[string][]string{"effective_from": {"must be an RFC 3339 date-time"}}}})
		return
	}
	var until *time.Time
	if req.EffectiveUntil != nil {
		u, perr := time.Parse(time.RFC3339, *req.EffectiveUntil)
		if perr != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters",
				Meta: map[string]any{"fields": map[string][]string{"effective_until": {"must be an RFC 3339 date-time"}}}})
			return
		}
		until = &u
	}
	windows := make([]application.RateWindowInput, 0, len(req.Windows))
	for _, w := range req.Windows {
		parsed, err := parseRateWindowRequest(w)
		if err != nil {
			// R1 L-1: price parse failures use the same channel as service-layer errors on this endpoint -- 1001 + detail
			// passed through MapReviewError (without dropping detail).
			_ = c.Error(err)
			if !writeB7Err(c, err) {
				WriteProblem(c, http.StatusInternalServerError, "internal error")
			}
			return
		}
		windows = append(windows, parsed)
	}
	view, err := d.Review.CreateRateVersion(c.Request.Context(), b6Actor(c).UserID, application.CreateRateVersionInput{
		ProviderID: c.Param("ai_provider_id"), Currency: req.Currency, Timezone: req.Timezone,
		EffectiveFrom: from, EffectiveUntil: until, Windows: windows,
	})
	if err != nil {
		_ = c.Error(err)
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, view)
}

// HandleListAiProviderRateVersions S008: lists rate versions (newest to oldest).
func (d *B7Deps) HandleListAiProviderRateVersions(c *gin.Context) {
	items, err := d.Review.ListRateVersions(c.Request.Context(), c.Param("ai_provider_id"))
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, items)
}

// HandleListAiUsageCalls S008: paginated per-HTTP-call reference ledger entries (provider_id
// filter from RCP-20261102-S008 replaces lossy client-side single-page filtering).
func (d *B7Deps) HandleListAiUsageCalls(c *gin.Context) {
	runID := c.Query("run_id")
	if runID != "" {
		// R1 L-2: invalid run_id is rejected with 1001 rather than hitting a PG error.
		if _, err := uuid.Parse(runID); err != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
			return
		}
	}
	providerID := c.Query("provider_id")
	if providerID != "" {
		// R1 L-2: invalid provider_id is likewise rejected with 1001.
		if _, err := uuid.Parse(providerID); err != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
			return
		}
	}
	items, next, err := d.Review.ListCallUsage(c.Request.Context(),
		intQuery(c, "limit", 50), c.Query("after"), runID, providerID, c.Query("currency"))
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writePage(c, items, next)
}

// HandleListAiUsageSummary S008: aggregates usage by currency within time window (multi-currency kept separate,
// no FX conversion).
func (d *B7Deps) HandleListAiUsageSummary(c *gin.Context) {
	// R1 L-2: supports only OpenAPI-declared from/to (defaults to last 30 days), with no days parameter.
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -30)
	if v := c.Query("from"); v != "" {
		f, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
			return
		}
		from = f
	}
	if v := c.Query("to"); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			WriteBusinessError(c, &BusinessError{Code: bizerr.CodeValidationFailed, Name: "VALIDATION_FAILED", Message: "Invalid request parameters"})
			return
		}
		to = t
	}
	items, err := d.Review.SummarizeCallUsage(c.Request.Context(), from, to)
	if err != nil {
		if !writeB7Err(c, err) {
			WriteProblem(c, http.StatusInternalServerError, "internal error")
		}
		return
	}
	WriteSuccess(c, items)
}
