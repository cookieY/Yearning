// Package tool implements the Tool Registry and normalized Tool results
// (ai-review-runtime-contract.json tool_runtime).
//
// Invariants:
//   - Only built-in database fact tools registered in Registry may be called; unknown Tools/arguments are rejected (2012).
//   - Tool results are untrusted data: after normalization, they enter model context only via the Evidence channel.
//   - No database-write Tools, no Shell/filesystem Tools, no external HTTP custom Tools, and no MCP.
//   - prompt_tool/knowledge_entry are not subject to free model scheduling (deterministic injection);
//     Registry only registers their descriptors for unified lifecycle management.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Kind is the controlled enum for review_tool_calls.tool_kind.
type Kind string

const (
	KindBuiltinDatabaseFact Kind = "builtin_database_fact"
	KindPromptTool          Kind = "prompt_tool"
	KindKnowledgeEntry      Kind = "knowledge_entry"
)

// Built-in tool names (tool_runtime.database_fact_tools).
const (
	NameDatasourceIdentity = "datasource_identity"
	NameSchemaMetadata     = "schema_metadata"
	NameTableStatistics    = "table_statistics"
	NameIndexMetadata      = "index_metadata"
	NameSafeExplain        = "safe_explain"
)

// ErrUnknownTool indicates an unknown tool or argument (fail closed).
var ErrUnknownTool = errors.New("tool: unknown tool or arguments rejected")

// ErrToolFailed indicates tool execution failed (2012; does not form an Observed fact).
var ErrToolFailed = errors.New("tool: execution failed")

// Descriptor is a tool declaration within the Registry.
type Descriptor struct {
	Name        string
	Kind        Kind
	Description string
	// ArgumentNames is the set of allowed argument names (strict; unknown arguments are rejected).
	ArgumentNames []string
	// RequiredArgs is the set of required argument names.
	RequiredArgs []string
	Timeout      time.Duration
}

// FactSource is the evidence collection port for built-in tools (wired by the application layer using B4 Adapter).
type FactSource interface {
	// DatasourceIdentity returns target instance identity facts.
	DatasourceIdentity(ctx context.Context) (map[string]any, error)
	// SchemaMetadata returns schema-level metadata facts.
	SchemaMetadata(ctx context.Context, schema string) (map[string]any, error)
	// TableStatistics returns table-level statistics facts (all marked as estimate).
	TableStatistics(ctx context.Context, schema, table string) (map[string]any, error)
	// IndexMetadata returns index metadata facts.
	IndexMetadata(ctx context.Context, schema, table string) (map[string]any, error)
	// SafeExplain performs a non-executing plan read on the real representative statement (statement SQL
	// is resolved only on the application side, and results enter model context only after normalization).
	SafeExplain(ctx context.Context, schema, statementSQL string) (map[string]any, error)
}

// TableMetadataSource is the optional scoped-metadata extension. Keeping it
// separate preserves the existing FactSource contract while ensuring that a
// table-scoped request fails closed when the source cannot observe one table.
type TableMetadataSource interface {
	TableMetadata(ctx context.Context, schema, table string) (map[string]any, error)
}

// Result is a normalized Tool result (the ten fields of tool_runtime.tool_result_normalization).
// RawFact preserves the pre-normalization raw map (entering only the controlled ciphertext of
// evidence_raw_payloads with 7-day retention, never entering model context — L7 fidelity).
type Result struct {
	ToolCallID        string         `json:"tool_call_id"`
	ToolKind          Kind           `json:"tool_kind"`
	ToolName          string         `json:"tool_name"`
	Success           bool           `json:"success"`
	FactSchemaVersion int            `json:"fact_schema_version"`
	NormalizedFact    map[string]any `json:"normalized_fact"`
	RawFact           []byte         `json:"-"`
	EvidenceIDs       []string       `json:"evidence_ids"`
	ErrorCode         *int           `json:"error_code"`
	StartedAt         time.Time      `json:"started_at"`
	FinishedAt        time.Time      `json:"finished_at"`
}

// FactSchemaVersion is the normalized fact schema version. Version 2 adds explicit metadata
// existence/completeness/estimate states. Historical evidence remains readable
// under its stored version and is never reinterpreted as version 2.
const FactSchemaVersion = 2

// Registry is the built-in tool registry (in-process static; recursive/dynamic registration is forbidden).
type Registry struct {
	descriptors map[string]Descriptor
}

// NewRegistry constructs the five-tool registry.
func NewRegistry() *Registry {
	r := &Registry{descriptors: map[string]Descriptor{}}
	for _, d := range []Descriptor{
		{Name: NameDatasourceIdentity, Kind: KindBuiltinDatabaseFact,
			Description:   "Target datasource engine, server version, and stable instance identity.",
			ArgumentNames: []string{}, Timeout: 10 * time.Second},
		{Name: NameSchemaMetadata, Kind: KindBuiltinDatabaseFact,
			Description:   "Controlled column profile and observed existence for one SQL-referenced table. Schema-wide discovery is not allowed.",
			ArgumentNames: []string{"schema", "table"}, RequiredArgs: []string{"schema", "table"}, Timeout: 15 * time.Second},
		{Name: NameTableStatistics, Kind: KindBuiltinDatabaseFact,
			Description:   "Table-level statistics derived from metadata; all values are estimates.",
			ArgumentNames: []string{"schema", "table"}, RequiredArgs: []string{"schema", "table"}, Timeout: 15 * time.Second},
		{Name: NameIndexMetadata, Kind: KindBuiltinDatabaseFact,
			Description:   "Bounded index key/include roles, expression/prefix presence, and structural completeness for one table.",
			ArgumentNames: []string{"schema", "table"}, RequiredArgs: []string{"schema", "table"}, Timeout: 15 * time.Second},
		{Name: NameSafeExplain, Kind: KindBuiltinDatabaseFact,
			Description:   "Non-executing plan fact for one statement group (never ANALYZE).",
			ArgumentNames: []string{"schema", "group_id"}, RequiredArgs: []string{"schema", "group_id"}, Timeout: 20 * time.Second},
	} {
		r.descriptors[d.Name] = d
	}
	return r
}

// Descriptor returns a copy of the tool declaration.
func (r *Registry) Descriptor(name string) (Descriptor, bool) {
	d, ok := r.descriptors[name]
	return d, ok
}

// InputSchema generates the JSON-Schema-shaped parameter declaration for this tool (NEW-AI-03 bootstrap:
// on turn 1 the model has no schema value to fill in — a circular dependency — and must first know
// parameter names, types, and requiredness to construct valid tool calls; after SEC-0041 binding,
// guessing non-frozen set members triggers 2012, so unknowable parameter names would leave tool facts
// permanently empty). Parameter values are uniformly string (matching validateArguments); property
// names match the ArgumentNames allowlist.
func (d Descriptor) InputSchema() map[string]any {
	props := make(map[string]any, len(d.ArgumentNames))
	for _, a := range d.ArgumentNames {
		props[a] = map[string]any{"type": "string"}
	}
	required := append([]string{}, d.RequiredArgs...)
	sort.Strings(required)
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

// Names returns all registered tool names (ordered; the model-visible allowlist).
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.descriptors))
	for n := range r.descriptors {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// validateArguments strictly validates the argument name set (rejecting unknown, missing, or non-string arguments).
func validateArguments(d Descriptor, args map[string]any) error {
	allowed := map[string]bool{}
	for _, a := range d.ArgumentNames {
		allowed[a] = true
	}
	required := map[string]bool{}
	for _, a := range d.RequiredArgs {
		required[a] = true
	}
	for name := range args {
		if !allowed[name] {
			return fmt.Errorf("%w: argument %q not allowed for %s", ErrUnknownTool, name, d.Name)
		}
	}
	for name := range allowed {
		v, ok := args[name]
		if !ok {
			if required[name] {
				return fmt.Errorf("%w: missing required argument %q for %s", ErrUnknownTool, name, d.Name)
			}
			continue
		}
		if v == nil {
			if required[name] {
				return fmt.Errorf("%w: missing required argument %q for %s", ErrUnknownTool, name, d.Name)
			}
			return fmt.Errorf("%w: argument %q must be a non-empty string", ErrUnknownTool, name)
		}
		s, isStr := v.(string)
		if !isStr || s == "" {
			return fmt.Errorf("%w: argument %q must be a non-empty string", ErrUnknownTool, name)
		}
	}
	return nil
}

// Execute validates and executes a single built-in tool call, returning a normalized result (never panics on failure;
// failed results have Success=false and NormalizedFact=nil, forming no Observed fact).
// The caller is responsible for persisting Result as evidence and review_tool_calls.
func (r *Registry) Execute(ctx context.Context, src FactSource, toolCallID, name string, args map[string]any) Result {
	started := time.Now().UTC()
	res := Result{ToolCallID: toolCallID, ToolKind: KindBuiltinDatabaseFact, ToolName: name,
		FactSchemaVersion: FactSchemaVersion, StartedAt: started}
	d, ok := r.descriptors[name]
	if !ok || d.Kind != KindBuiltinDatabaseFact {
		res.FinishedAt = time.Now().UTC()
		res.ErrorCode = errCodePtr(2012)
		return res
	}
	if err := validateArguments(d, args); err != nil {
		res.FinishedAt = time.Now().UTC()
		res.ErrorCode = errCodePtr(2012)
		return res
	}
	cctx, cancel := context.WithTimeout(ctx, d.Timeout)
	defer cancel()
	arg := func(k string) string { s, _ := args[k].(string); return s }
	var (
		fact map[string]any
		err  error
	)
	switch name {
	case NameDatasourceIdentity:
		fact, err = src.DatasourceIdentity(cctx)
	case NameSchemaMetadata:
		if table := arg("table"); table != "" {
			scoped, ok := src.(TableMetadataSource)
			if !ok {
				err = ErrToolFailed
				break
			}
			fact, err = scoped.TableMetadata(cctx, arg("schema"), table)
		} else {
			fact, err = src.SchemaMetadata(cctx, arg("schema"))
		}
	case NameTableStatistics:
		fact, err = src.TableStatistics(cctx, arg("schema"), arg("table"))
	case NameIndexMetadata:
		fact, err = src.IndexMetadata(cctx, arg("schema"), arg("table"))
	case NameSafeExplain:
		fact, err = src.SafeExplain(cctx, arg("schema"), arg("group_id"))
	default:
		err = ErrUnknownTool
	}
	res.FinishedAt = time.Now().UTC()
	if err != nil {
		res.ErrorCode = errCodePtr(2012)
		return res
	}
	// Tool output is untrusted data: retain only normalized JSON scalars/lists/maps,
	// and reject any instruction-like top-level key rewrites (injection defense is handled by context encapsulation).
	res.Success = true
	if raw, err := json.Marshal(fact); err == nil {
		res.RawFact = raw
	}
	res.NormalizedFact = normalizeFact(fact)
	return res
}

func errCodePtr(c int) *int { return &c }
