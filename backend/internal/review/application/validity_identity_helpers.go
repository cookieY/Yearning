package application

import "github.com/cookieY/YearningX/backend/internal/review/fingerprint"

func cloneStatementValidity(in fingerprint.StatementValidity) fingerprint.StatementValidity {
	out := in
	out.Requirements = append([]fingerprint.ValidityRequirement(nil), in.Requirements...)
	out.Effects = make([]fingerprint.ValidityEffect, len(in.Effects))
	for i := range in.Effects {
		out.Effects[i] = in.Effects[i]
		if in.Effects[i].Replacement != nil {
			replacement := *in.Effects[i].Replacement
			out.Effects[i].Replacement = &replacement
		}
	}
	return out
}

func cloneValidityFact(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneValidityFactValue(value)
	}
	return out
}

func cloneValidityFactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneValidityFact(typed)
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = cloneValidityFactValue(typed[i])
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(typed))
		for i := range typed {
			out[i] = cloneValidityFact(typed[i])
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
