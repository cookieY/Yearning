package fingerprint

// Relation is a controlled semantic relationship of an anonymous object within a statement.
type Relation string

const (
	RelationTarget    Relation = "target"
	RelationRead      Relation = "read"
	RelationWrite     Relation = "write"
	RelationFilter    Relation = "filter"
	RelationJoin      Relation = "join"
	RelationDefine    Relation = "define"
	RelationReference Relation = "reference"
)

// ObjectReference is an anonymous object relationship within a single statement consumable by a Provider.
// All names have been converted to tokens from the same RunMapper; StatementOrdinal is populated by
// the upper grouping layer for the original statement position. StatementOrdinalRanges is merged by grouping
// into all appearance positions; at the single-statement fingerprint stage, they are zero and empty respectively.
type ObjectReference struct {
	Token                  string                  `json:"token"`
	Kind                   string                  `json:"kind"`
	Relation               Relation                `json:"relation"`
	SchemaToken            string                  `json:"schema_token,omitempty"`
	ParentToken            string                  `json:"parent_token,omitempty"`
	StatementOrdinal       int32                   `json:"statement_ordinal"`
	StatementOrdinalRanges []StatementOrdinalRange `json:"statement_ordinal_ranges"`
}

// StatementOrdinalRange is an inclusive range of statement positions where
// this exact object reference occurs.
type StatementOrdinalRange struct {
	Start int32 `json:"start"`
	End   int32 `json:"end"`
}
