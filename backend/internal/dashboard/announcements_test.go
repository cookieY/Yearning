package dashboard

import (
	"strings"
	"testing"
)

// BUG-012 regression: inline code inside list items must render as <code> (previously escapeHTML
// ran before inline formatting, escaping backtick delimiters so inline code never took effect).
func TestSanitizeMarkdownInlineCodeInListItem(t *testing.T) {
	out := sanitizeMarkdown("- code snippet: `SELECT 1`")
	if !strings.Contains(out, "<code>SELECT 1</code>") {
		t.Fatalf("expected inline code rendered, got %q", out)
	}
}

// XSS surface regression: script/event attributes inside inline constructs remain escaped as plain text.
func TestSanitizeMarkdownXSSProbesStayEscaped(t *testing.T) {
	out := sanitizeMarkdown("- XSS: <script>alert(1)</script> <img src=x onerror=alert(2)>")
	if strings.Contains(out, "<script>") || strings.Contains(out, "<img") {
		t.Fatalf("raw html leaked: %q", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("expected escaped probe text, got %q", out)
	}
}

// Bold and inline code coexist in the same paragraph; blockquote raw > prefix.
func TestSanitizeMarkdownMixedInline(t *testing.T) {
	out := sanitizeMarkdown("**bold** and `code`")
	if !strings.Contains(out, "<strong>bold</strong>") || !strings.Contains(out, "<code>code</code>") {
		t.Fatalf("mixed inline failed: %q", out)
	}
	if quote := sanitizeMarkdown("> quote"); !strings.Contains(quote, "<blockquote>quote</blockquote>") {
		t.Fatalf("blockquote failed: %q", quote)
	}
}
