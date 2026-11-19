package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/review/agent"
	"github.com/cookieY/YearningX/backend/internal/review/domain"
	"github.com/cookieY/YearningX/backend/internal/review/knowledge"
	"github.com/cookieY/YearningX/backend/internal/review/privacy"
	"github.com/cookieY/YearningX/backend/internal/review/promptprofile"
	"github.com/cookieY/YearningX/backend/internal/review/provider"
)

func endpointIsSAN(ep provider.Endpoint) bool {
	return ep.EffectivePromptProfile() == promptprofile.ProfileSAN
}

func mergeSANObjectRefs(
	groups []provider.AnonymizedGroup,
	policyObjects []provider.AnonymizedObjectReference,
) []provider.AnonymizedObjectReference {
	total := len(policyObjects)
	for _, g := range groups {
		total += len(g.Objects)
	}
	merged := make([]provider.AnonymizedObjectReference, 0, total)
	seen := make(map[string]bool, total)
	appendUnique := func(refs []provider.AnonymizedObjectReference) {
		for _, obj := range refs {
			key := obj.Kind + "\x00" + obj.SchemaToken + "\x00" + obj.ParentToken + "\x00" + obj.Token + "\x00" + obj.Relation
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, obj)
		}
	}
	for _, g := range groups {
		appendUnique(g.Objects)
	}
	appendUnique(policyObjects)
	return merged
}

func buildSANStageRequest(
	ctx context.Context,
	db *gorm.DB,
	crypter *crypto.Crypter,
	runID string,
	target frozenDatasourceTarget,
	groups []provider.AnonymizedGroup,
	policyObjects []provider.AnonymizedObjectReference,
	stageSkills []agent.SkillInjection,
	stageKnowledge []*knowledge.Entry,
	sanEvidence []provider.NormalizedEvidenceRef,
	runMapper *privacy.RunMapper,
	outputLocale string,
	evidenceIncomplete bool,
	localePending bool,
	schemaPending bool,
	cachedSANGroups *[]provider.AnonymizedGroup,
) (provider.Request, error) {
	sanGroups, err := prepareSANGroups(
		ctx, db, crypter, runID,
		target.Engine+"/"+target.CompatibilityMode,
		groups, runMapper, cachedSANGroups,
	)
	if err != nil {
		return provider.Request{}, err
	}

	rawRefs := detokenizeObjectRefsForSAN(mergeSANObjectRefs(groups, policyObjects), runMapper)
	promptRefs := make([]promptprofile.ObjectReference, len(rawRefs))
	for i, obj := range rawRefs {
		ranges := make([]promptprofile.StatementOrdinalRange, len(obj.StatementOrdinalRanges))
		for j, rg := range obj.StatementOrdinalRanges {
			ranges[j] = promptprofile.StatementOrdinalRange{Start: rg.Start, End: rg.End}
		}
		promptRefs[i] = promptprofile.ObjectReference{
			Token:                  obj.Token,
			Kind:                   obj.Kind,
			Relation:               obj.Relation,
			SchemaToken:            obj.SchemaToken,
			ParentToken:            obj.ParentToken,
			StatementOrdinal:       obj.StatementOrdinal,
			StatementOrdinalRanges: ranges,
		}
	}

	rawSkills := make([]promptprofile.RuleInput, 0, len(stageSkills))
	for _, sk := range stageSkills {
		renderedText, rerr := renderSANPolicyText(sk.KnowledgeText)
		if rerr != nil {
			return provider.Request{}, fmt.Errorf("%w: %v", domain.ErrCoverageIncomplete, rerr)
		}
		rawSkills = append(rawSkills, promptprofile.RuleInput{
			FindingKey:    sk.FindingKey,
			KnowledgeText: renderedText,
			Parameters:    sk.Parameters,
		})
	}

	rawKnowledge := make([]promptprofile.RuleInput, 0, len(stageKnowledge))
	for _, entry := range stageKnowledge {
		if entry == nil {
			continue
		}
		renderedText, rerr := renderSANPolicyText(entry.Definition.KnowledgeText)
		if rerr != nil {
			return provider.Request{}, fmt.Errorf("%w: %v", domain.ErrCoverageIncomplete, rerr)
		}
		rawKnowledge = append(rawKnowledge, promptprofile.RuleInput{
			FindingKey:    entry.FindingKey,
			KnowledgeText: renderedText,
		})
	}

	sysPrompt := promptprofile.BuildSANReviewSystemPrompt(promptprofile.ReviewSystemPromptInput{
		OutputLocale:       outputLocale,
		EngineProfile:      target.Engine + "/" + target.CompatibilityMode,
		ServerVersion:      target.ServerVersion,
		ObjectReferences:   promptRefs,
		Skills:             rawSkills,
		Knowledge:          rawKnowledge,
		EvidenceIncomplete: evidenceIncomplete,
		LocaleRetry:        localePending,
		SchemaRetry:        schemaPending,
	})

	evidenceOut := sanEvidence
	if evidenceOut == nil {
		evidenceOut = []provider.NormalizedEvidenceRef{}
	}

	return provider.Request{
		RequestSchemaVersion:      1,
		ReviewRunID:               runID,
		EngineProfile:             target.Engine + "/" + target.CompatibilityMode,
		SystemPrompt:              sysPrompt,
		AnonymizedStatementGroups: sanGroups,
		NormalizedEvidence:        evidenceOut,
		AllowedToolDescriptors:    nil,
	}, nil
}

func prepareSANGroups(
	ctx context.Context,
	db *gorm.DB,
	crypter *crypto.Crypter,
	runID string,
	profileHint string,
	groups []provider.AnonymizedGroup,
	mapper *privacy.RunMapper,
	cached *[]provider.AnonymizedGroup,
) ([]provider.AnonymizedGroup, error) {
	if cached != nil && *cached != nil {
		return *cached, nil
	}
	out := make([]provider.AnonymizedGroup, len(groups))
	for i, g := range groups {
		rawSQL, err := resolveRepresentativeStatementSQL(ctx, db, crypter, runID, g.GroupID, profileHint)
		if err != nil {
			return nil, fmt.Errorf("%w: san raw sql unavailable for group %s: %v", domain.ErrCoverageIncomplete, g.GroupID, err)
		}
		g.RepresentativeAnonymizedSQL = promptprofile.TruncateSQL(rawSQL)
		g.Objects = detokenizeObjectRefsForSAN(g.Objects, mapper)
		out[i] = g
	}
	if cached != nil {
		*cached = out
	}
	return out, nil
}

func detokenizeObjectRefsForSAN(
	refs []provider.AnonymizedObjectReference,
	mapper *privacy.RunMapper,
) []provider.AnonymizedObjectReference {
	out := make([]provider.AnonymizedObjectReference, len(refs))
	for i, ref := range refs {
		if mapper != nil {
			if _, orig, ok := mapper.Original(ref.Token); ok {
				ref.Token = orig
			}
			if ref.SchemaToken != "" {
				if _, orig, ok := mapper.Original(ref.SchemaToken); ok {
					ref.SchemaToken = orig
				}
			}
			if ref.ParentToken != "" {
				if _, orig, ok := mapper.Original(ref.ParentToken); ok {
					ref.ParentToken = orig
				}
			}
		}
		if (ref.Kind == "table" || ref.Kind == "view") && ref.SchemaToken != "" && !strings.Contains(ref.Token, ".") {
			ref.Token = ref.SchemaToken + "." + ref.Token
		}
		if ref.ParentToken != "" && ref.SchemaToken != "" && !strings.Contains(ref.ParentToken, ".") {
			ref.ParentToken = ref.SchemaToken + "." + ref.ParentToken
		}
		if ref.StatementOrdinalRanges == nil {
			ref.StatementOrdinalRanges = []provider.StatementOrdinalRange{}
		}
		out[i] = ref
	}
	return out
}

func renderSANPolicyText(text string) (string, error) {
	return privacy.RewritePolicyReferences(text, func(ref privacy.PolicyObjectReference) (string, error) {
		switch ref.Kind {
		case privacy.PolicyReferenceSchema:
			return ref.Schema, nil
		case privacy.PolicyReferenceTable, privacy.PolicyReferenceView:
			return ref.Schema + "." + ref.Relation, nil
		case privacy.PolicyReferenceColumn, privacy.PolicyReferenceIndex, privacy.PolicyReferenceConstraint:
			return ref.Schema + "." + ref.Relation + "." + ref.Name, nil
		default:
			return "", privacy.ErrInvalidPolicyReference
		}
	})
}

func resultEndpointIsSAN(chain *provider.Chain, result *provider.Result) bool {
	if chain == nil || result == nil {
		return false
	}
	idx := result.FallbackCount
	if idx < 0 || idx >= len(chain.Endpoints) {
		return false
	}
	return chain.Endpoints[idx].EffectivePromptProfile() == promptprofile.ProfileSAN
}

// translateSANToolArgsToTokens converts physical identifier arguments produced by a SAN model
// into canonical RunMapper tokens so SEC-0041 scope enforcement and RunMapper-based audit logging
// apply identically without exposing unverified raw identifiers to FactSource.
func translateSANToolArgsToTokens(
	args map[string]any,
	mapper *privacy.RunMapper,
	binding *stageSchemaBinding,
) (map[string]any, bool) {
	if mapper == nil {
		return nil, false
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}

	var physSchema, rawSchema string
	if sv, exists := args["schema"]; exists {
		s, ok := sv.(string)
		if !ok {
			return nil, false
		}
		if s != "" {
			if binding == nil {
				return nil, false
			}
			phys, ok := binding.physicalSchema(s)
			if !ok {
				return nil, false
			}
			rawSchema = s
			physSchema = phys
			tok := mapper.Lookup(privacy.KindSchema, phys)
			if tok == "" && rawSchema != phys {
				tok = mapper.Lookup(privacy.KindSchema, rawSchema)
			}
			if tok == "" {
				return nil, false
			}
			out["schema"] = tok
		}
	}

	if tv, exists := args["table"]; exists {
		rawTable, ok := tv.(string)
		if !ok {
			return nil, false
		}
		if rawTable != "" {
			if database.ValidateIdentifier(rawTable) != nil {
				return nil, false
			}
			tok := lookupTableTokenForSAN(mapper, binding, physSchema, rawSchema, rawTable)
			if tok == "" {
				return nil, false
			}
			out["table"] = tok
		}
	}

	return out, true
}

func lookupTableTokenForSAN(
	mapper *privacy.RunMapper,
	binding *stageSchemaBinding,
	physSchema string,
	rawSchema string,
	rawTable string,
) string {
	candidates := make([]string, 0, 3)
	if physSchema != "" {
		candidates = append(candidates, physSchema)
	}
	if rawSchema != "" && rawSchema != physSchema {
		candidates = append(candidates, rawSchema)
	}
	if binding != nil {
		if def := strings.TrimSpace(binding.defaultSchema()); def != "" && def != physSchema && def != rawSchema {
			candidates = append(candidates, def)
		}
	}
	for _, sc := range candidates {
		if tok := mapper.LookupScoped(privacy.KindTable, sc, rawTable); tok != "" {
			return tok
		}
	}
	return mapper.Lookup(privacy.KindTable, rawTable)
}

func copyNormalizedFactForSAN(fact map[string]any, schemaVersion int) map[string]any {
	if fact == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(fact)
	if err != nil {
		return map[string]any{}
	}
	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return map[string]any{}
	}
	for key := range factFreeTextListFields {
		delete(cloned, key)
	}
	cloned["fact_schema_version"] = schemaVersion
	return cloned
}
