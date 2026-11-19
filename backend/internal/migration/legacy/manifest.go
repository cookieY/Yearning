package legacy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Manifest mirrors api/migration/migration-manifest.schema.json.
// manifest_hash is computed after removing the manifest_hash field itself; all hashes use RFC 8785 canonicalization.
type Manifest struct {
	ManifestVersion          string                 `json:"manifest_version"`
	MigrationRunID           string                 `json:"migration_run_id"`
	Mode                     string                 `json:"mode"` // dry_run | apply
	MigratorVersion          string                 `json:"migrator_version"`
	StartedAt                string                 `json:"started_at"`
	FinishedAt               *string                `json:"finished_at"`
	Outcome                  string                 `json:"outcome"` // succeeded | failed | awaiting_confirmation
	Source                   ManifestSource         `json:"source"`
	Target                   ManifestTarget         `json:"target"`
	FieldMappingContractHash string                 `json:"field_mapping_contract_hash"`
	StatusMappings           ManifestStatusMappings `json:"status_mappings"`
	TableResults             []ManifestTableResult  `json:"table_results"`
	CandidateMappings        ManifestCandidates     `json:"candidate_mappings"`
	Invariants               []ManifestInvariant    `json:"invariants"`
	SensitiveData            ManifestSensitiveData  `json:"sensitive_data"`
	Approval                 ManifestApproval       `json:"approval"`
	Warnings                 []ManifestDiagnostic   `json:"warnings,omitempty"`
	Errors                   []ManifestDiagnostic   `json:"errors,omitempty"`
	ManifestHash             string                 `json:"manifest_hash"`
}

type ManifestSource struct {
	Release                        string  `json:"release"`
	GitCommit                      *string `json:"git_commit,omitempty"`
	DatabaseEngine                 string  `json:"database_engine"`
	DatabaseVersion                string  `json:"database_version"`
	SchemaHash                     string  `json:"schema_hash"`
	SnapshotHash                   string  `json:"snapshot_hash"`
	CutoffAt                       string  `json:"cutoff_at"`
	ActiveWorkCount                int     `json:"active_work_count"`
	ExecutionServerSourceAvailable *bool   `json:"execution_server_source_available,omitempty"`
}

type ManifestTarget struct {
	Release         string `json:"release"`
	DatabaseEngine  string `json:"database_engine"`
	DatabaseVersion string `json:"database_version"`
	SchemaVersion   int64  `json:"schema_version"`
	SchemaHash      string `json:"schema_hash"`
}

type ManifestTableResult struct {
	SourceTable          string   `json:"source_table"`
	TargetTables         []string `json:"target_tables"`
	Read                 int64    `json:"read"`
	Written              int64    `json:"written"`
	Excluded             int64    `json:"excluded"`
	Quarantined          int64    `json:"quarantined"`
	Failed               int64    `json:"failed"`
	SourceRowsHash       string   `json:"source_rows_hash"`
	TargetRowsHash       string   `json:"target_rows_hash"`
	ReconciliationPassed *bool    `json:"reconciliation_passed,omitempty"`
}

// ManifestCandidate mirrors the candidate definition in the manifest schema.
type ManifestCandidate struct {
	CandidateID          string   `json:"candidate_id"`
	Kind                 string   `json:"kind"` // permission_group_flow_grant | single_stage_flow | multi_stage_flow | rule_set
	SourceRefs           []string `json:"source_refs"`
	TargetDefinitionHash string   `json:"target_definition_hash"`
	Risk                 string   `json:"risk"` // no_expansion | possible_expansion | unmapped
	Confirmed            bool     `json:"confirmed"`
	ConfirmedBy          *string  `json:"confirmed_by,omitempty"`
	ConfirmedAt          *string  `json:"confirmed_at,omitempty"`
}

type ManifestCandidates struct {
	Items        []ManifestCandidate `json:"items"`
	MappingHash  string              `json:"mapping_hash"`
	AllConfirmed bool                `json:"all_confirmed"`
}

type ManifestInvariant struct {
	Code     string `json:"code"`
	Scope    string `json:"scope"`
	Outcome  string `json:"outcome"` // passed | failed
	Observed any    `json:"observed"`
	Expected any    `json:"expected"`
}

type ManifestSensitiveData struct {
	PlaintextInReport     bool   `json:"plaintext_in_report"`
	PlaintextInLogs       bool   `json:"plaintext_in_logs"`
	EncryptedFieldCount   int64  `json:"encrypted_field_count"`
	EncryptionFailedCount int    `json:"encryption_failed_count"`
	KeyID                 string `json:"key_id"`
}

type ManifestApproval struct {
	Required             bool    `json:"required"`
	Approved             bool    `json:"approved"`
	ApprovedBy           *string `json:"approved_by,omitempty"`
	ApprovedAt           *string `json:"approved_at,omitempty"`
	ApprovedManifestHash *string `json:"approved_manifest_hash,omitempty"`
}

type ManifestDiagnostic struct {
	Code         string   `json:"code"`
	Scope        string   `json:"scope"`
	Count        int64    `json:"count"`
	SampleHashes []string `json:"sample_hashes,omitempty"`
}

// ComputeManifestHash computes manifest_hash per contract: removes the manifest_hash field and hashes
// the RFC 8785 canonical JSON with SHA-256. All struct fields map to contracted keys with no floating-point values.
func (m *Manifest) ComputeManifestHash() (string, error) {
	clone := *m
	clone.ManifestHash = ""
	value, err := manifestToJCS(clone)
	if err != nil {
		return "", err
	}
	return JCSHash(value)
}

// Finalize populates ManifestHash; must be called once after all other manifest fields are finalized.
func (m *Manifest) Finalize() error {
	hash, err := m.ComputeManifestHash()
	if err != nil {
		return err
	}
	m.ManifestHash = hash
	return nil
}

// FieldMappingContractHash computes the canonical hash of the field-mapping contract file.
// Because the contract requires binding the "complete mapping file", it hashes the raw file bytes
// directly with SHA-256 (as the contract file itself is already deterministic canonical text).
func FieldMappingContractHash(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read field mapping contract: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// NowUTC formats the current time in the UTC timestamp format used by the Manifest.
func NowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// manifestToJCS round-trips through JSON using UseNumber into JCS-safe values (all numbers are integers).
func manifestToJCS(m Manifest) (any, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return JSONToJCSValue(v)
}
