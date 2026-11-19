package application

import (
	"context"
	"strings"

	"golang.org/x/text/language"
)

type outputLocaleCtxKey struct{}

// WithOutputLocale attaches the negotiated platform output locale ("zh-CN" or "en-US") to ctx.
func WithOutputLocale(ctx context.Context, headerOrLocale string) context.Context {
	return context.WithValue(ctx, outputLocaleCtxKey{}, ResolveOutputLocale(headerOrLocale))
}

// OutputLocaleFromContext returns the negotiated platform output locale from ctx, defaulting to "zh-CN".
func OutputLocaleFromContext(ctx context.Context) string {
	if ctx == nil {
		return "zh-CN"
	}
	if v, ok := ctx.Value(outputLocaleCtxKey{}).(string); ok && strings.TrimSpace(v) != "" {
		return ResolveOutputLocale(v)
	}
	return "zh-CN"
}

// ResolveOutputLocale negotiates only supported languages. Raw client text is
// never persisted in task input or interpolated into a provider instruction.
func ResolveOutputLocale(header string) string {
	if len(header) > 1024 {
		return "zh-CN"
	}
	tags, _, err := language.ParseAcceptLanguage(header)
	if err != nil {
		return "zh-CN"
	}
	for _, tag := range tags {
		base, _ := tag.Base()
		switch base.String() {
		case "en":
			return "en-US"
		case "zh":
			return "zh-CN"
		}
	}
	return "zh-CN"
}
