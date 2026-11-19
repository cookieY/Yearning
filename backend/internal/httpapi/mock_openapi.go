package httpapi

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// RouteTable is the read-only route inventory extracted from the OpenAPI contract.
// The Mock Server uses it to distinguish contract paths from unknown paths
// without duplicating business semantics in Go -- the single source of truth is always api/openapi/yearning-v4.yaml.
type RouteTable struct {
	entries []routeEntry
}

type routeEntry struct {
	template string
	pattern  *regexp.Regexp
	methods  map[string]bool
}

type openAPIPaths struct {
	// Path-level objects may also carry non-method keys such as parameters/summary, so values use a generic map.
	Paths map[string]map[string]any `yaml:"paths"`
}

// LoadRouteTable parses the paths section of the OpenAPI document. Any YAML error fails immediately
// to prevent the Mock from running against a stale or corrupt contract.
func LoadRouteTable(path string) (*RouteTable, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read openapi %s: %w", path, err)
	}
	var doc openAPIPaths
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse openapi %s: %w", path, err)
	}
	table := &RouteTable{}
	methods := []string{"get", "post", "put", "patch", "delete", "head", "options"}
	for template, operations := range doc.Paths {
		entry := routeEntry{
			template: template,
			pattern:  templateToPattern(template),
			methods:  map[string]bool{},
		}
		for _, method := range methods {
			if _, ok := operations[method]; ok {
				entry.methods[strings.ToUpper(method)] = true
			}
		}
		if len(entry.methods) > 0 {
			table.entries = append(table.entries, entry)
		}
	}
	return table, nil
}

// MatchKind describes how a request matches the contract route table.
type MatchKind int

const (
	MatchUnknown MatchKind = iota
	Matched
	MethodNotAllowed
)

// Match checks whether method+path matches a contract route.
func (t *RouteTable) Match(path, method string) (MatchKind, string) {
	pathKnown := false
	for _, entry := range t.entries {
		if entry.pattern.MatchString(path) {
			pathKnown = true
			if entry.methods[method] {
				return Matched, entry.template
			}
		}
	}
	if pathKnown {
		return MethodNotAllowed, ""
	}
	return MatchUnknown, ""
}

// Templates returns all contract path templates (for diagnostics).
func (t *RouteTable) Templates() []string {
	out := make([]string, 0, len(t.entries))
	for _, entry := range t.entries {
		out = append(out, entry.template)
	}
	return out
}

// templateToPattern converts an OpenAPI path template into a matching regex.
// /admin/users/{user_id} -> ^/api prefix is guaranteed by the caller; only the template itself is converted here.
func templateToPattern(template string) *regexp.Regexp {
	segments := strings.Split(template, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			segments[i] = "[^/]+"
			continue
		}
		segments[i] = regexp.QuoteMeta(segment)
	}
	return regexp.MustCompile("^" + strings.Join(segments, "/") + "$")
}
