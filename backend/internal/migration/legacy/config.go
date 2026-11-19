package legacy

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// SecretReference references a secret per migration-config.schema.json:
// exactly one of environment or file must be set; secret values never enter logs, reports, or CLI flags.
type SecretReference struct {
	Environment string
	File        string
}

var envNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func (r SecretReference) validate() error {
	if r.Environment == "" && r.File == "" {
		return fmt.Errorf("secret reference must set environment or file")
	}
	if r.Environment != "" && r.File != "" {
		return fmt.Errorf("secret reference must not set both environment and file")
	}
	if r.Environment != "" && !envNamePattern.MatchString(r.Environment) {
		return fmt.Errorf("secret environment name violates pattern")
	}
	if r.File != "" && !strings.HasPrefix(r.File, "/") {
		return fmt.Errorf("secret file must be an absolute path")
	}
	if len(r.File) > 4096 {
		return fmt.Errorf("secret file path too long")
	}
	return nil
}

// Resolve reads the secret value. Callers must never write the returned value into logs or output structures.
func (r SecretReference) Resolve() ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	if r.Environment != "" {
		v, ok := os.LookupEnv(r.Environment)
		if !ok {
			return nil, fmt.Errorf("environment secret %s not set", r.Environment)
		}
		return []byte(v), nil
	}
	data, err := os.ReadFile(r.File)
	if err != nil {
		return nil, fmt.Errorf("read secret file: %w", err)
	}
	return data, nil
}

// rawSecretReference mirrors the schema's oneOf shape for strict validation.
type rawSecretReference struct {
	Environment *string `json:"environment"`
	File        *string `json:"file"`
}

func (r *rawSecretReference) decode(dec *json.Decoder, where string) (SecretReference, error) {
	if err := dec.Decode(r); err != nil {
		return SecretReference{}, fmt.Errorf("%s: %w", where, err)
	}
	if r.Environment != nil && r.File != nil {
		return SecretReference{}, fmt.Errorf("%s: environment and file are mutually exclusive", where)
	}
	if r.Environment == nil && r.File == nil {
		return SecretReference{}, fmt.Errorf("%s: environment or file required", where)
	}
	out := SecretReference{}
	if r.Environment != nil {
		out.Environment = *r.Environment
	}
	if r.File != nil {
		out.File = *r.File
	}
	if err := out.validate(); err != nil {
		return SecretReference{}, fmt.Errorf("%s: %w", where, err)
	}
	return out, nil
}

type rawTLS struct {
	Mode            string  `json:"mode"`
	ServerName      string  `json:"server_name"`
	CAFile          *string `json:"ca_file"`
	CertificateFile *string `json:"certificate_file"`
	PrivateKeyFile  *string `json:"private_key_file"`
}

type rawEndpoint struct {
	Host            string          `json:"host"`
	Port            int             `json:"port"`
	Database        string          `json:"database"`
	Username        string          `json:"username"`
	Password        json.RawMessage `json:"password"`
	TLS             rawTLS          `json:"tls"`
	LegacySecretKey json.RawMessage `json:"legacy_secret_key"`
}

type rawEncryption struct {
	KeyID        string          `json:"key_id"`
	Key          json.RawMessage `json:"key"`
	DeploymentID string          `json:"deployment_id"`
}

type rawConfig struct {
	Source         rawEndpoint   `json:"source"`
	Target         rawEndpoint   `json:"target"`
	Encryption     rawEncryption `json:"encryption"`
	SourceTimezone string        `json:"source_timezone"`
	BatchRows      *int          `json:"batch_rows"`
}

// Endpoint represents a validated database endpoint.
type Endpoint struct {
	Host     string
	Port     int
	Database string
	Username string
	Password SecretReference
	TLS      rawTLS
	// LegacySecretKey is permitted only on the source endpoint (RCP-20260825-B2-LEGACY-SECRET-KEY):
	// references legacy General.SecretKey used to decrypt datasource password ciphertexts; nil when unconfigured.
	LegacySecretKey *SecretReference
}

// Encryption declares the v4 envelope encryption key and deployment identifier.
type Encryption struct {
	KeyID string
	Key   SecretReference
	// DeploymentID shares the same value as deployment_id in the target environment's keyring (non-secret)
	// and forms part of the runtime AAD (RCP-20260831-LEGACY-MIGRATION-AAD Option A).
	DeploymentID string
}

// Config is the strict runtime representation of migration-config.schema.json.
type Config struct {
	Source         Endpoint
	Target         Endpoint
	Encryption     Encryption
	SourceTimezone string
	BatchRows      int
}

// DefaultBatchRows matches the JSON schema default.
const DefaultBatchRows = 1000

// LoadConfig parses and strictly validates the migration configuration. Because the schema specifies
// additionalProperties:false, any unknown field is rejected (prior to RCP-20260825-B2-LEGACY-SECRET-KEY,
// source.legacy_secret_key was rejected here as well).
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read migration config: %w", err)
	}
	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse migration config: %w", err)
	}
	if err := rejectUnknownFields(data); err != nil {
		return nil, fmt.Errorf("migration config: %w", err)
	}
	cfg := &Config{BatchRows: DefaultBatchRows, SourceTimezone: raw.SourceTimezone}
	if raw.BatchRows != nil {
		cfg.BatchRows = *raw.BatchRows
	}
	if cfg.BatchRows < 1 || cfg.BatchRows > 10000 {
		return nil, fmt.Errorf("batch_rows out of range")
	}
	if err := cfg.Source.decode(raw.Source, "source"); err != nil {
		return nil, err
	}
	if err := cfg.Target.decode(raw.Target, "target"); err != nil {
		return nil, err
	}
	if raw.Encryption.KeyID == "" {
		return nil, fmt.Errorf("encryption.key_id is required")
	}
	if raw.Encryption.DeploymentID == "" || len(raw.Encryption.DeploymentID) > 128 {
		return nil, fmt.Errorf("encryption.deployment_id invalid (required, 1..128 chars; must match the target keyring deployment_id)")
	}
	keyRef, err := (&rawSecretReference{}).decodeRef(raw.Encryption.Key, "encryption.key")
	if err != nil {
		return nil, err
	}
	cfg.Encryption = Encryption{KeyID: raw.Encryption.KeyID, Key: keyRef, DeploymentID: raw.Encryption.DeploymentID}
	if raw.SourceTimezone == "" || len(raw.SourceTimezone) > 255 {
		return nil, fmt.Errorf("source_timezone invalid")
	}
	if _, err := loadLocation(raw.SourceTimezone); err != nil {
		return nil, fmt.Errorf("source_timezone: %w", err)
	}
	return cfg, nil
}

func (e *Endpoint) decode(raw rawEndpoint, where string) error {
	if raw.Host == "" || len(raw.Host) > 253 {
		return fmt.Errorf("%s.host invalid", where)
	}
	if raw.Port < 1 || raw.Port > 65535 {
		return fmt.Errorf("%s.port out of range", where)
	}
	if raw.Database == "" || len(raw.Database) > 128 {
		return fmt.Errorf("%s.database invalid", where)
	}
	if raw.Username == "" || len(raw.Username) > 128 {
		return fmt.Errorf("%s.username invalid", where)
	}
	ref, err := (&rawSecretReference{}).decodeRef(raw.Password, where+".password")
	if err != nil {
		return err
	}
	if raw.TLS.Mode != "verify_ca" && raw.TLS.Mode != "verify_full" {
		return fmt.Errorf("%s.tls.mode must be verify_ca or verify_full", where)
	}
	if raw.TLS.ServerName == "" || len(raw.TLS.ServerName) > 253 {
		return fmt.Errorf("%s.tls.server_name invalid", where)
	}
	*e = Endpoint{
		Host: raw.Host, Port: raw.Port, Database: raw.Database,
		Username: raw.Username, Password: ref, TLS: raw.TLS,
	}
	if raw.LegacySecretKey != nil {
		legacyRef, err := (&rawSecretReference{}).decodeRef(raw.LegacySecretKey, where+".legacy_secret_key")
		if err != nil {
			return err
		}
		e.LegacySecretKey = &legacyRef
	}
	return nil
}

// decodeRef decodes a secret reference and validates its oneOf and pattern constraints.
func (r *rawSecretReference) decodeRef(raw json.RawMessage, where string) (SecretReference, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return SecretReference{}, fmt.Errorf("%s: %w", where, err)
	}
	for k := range probe {
		if k != "environment" && k != "file" {
			return SecretReference{}, fmt.Errorf("%s: unknown field %q", where, k)
		}
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	return r.decode(dec, where)
}

// rejectUnknownFields enforces additionalProperties:false at the top level.
// Prior to RCP-20260825-B2-LEGACY-SECRET-KEY approval, legacy_secret_key was rejected here.
func rejectUnknownFields(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	allowed := map[string]bool{
		"source": true, "target": true, "encryption": true,
		"source_timezone": true, "batch_rows": true,
	}
	for k := range probe {
		if !allowed[k] {
			return fmt.Errorf("unknown field %q", k)
		}
	}
	for _, required := range []string{"source", "target", "encryption", "source_timezone"} {
		if _, ok := probe[required]; !ok {
			return fmt.Errorf("missing required field %q", required)
		}
	}
	for _, section := range []string{"source", "target"} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(probe[section], &fields); err != nil {
			return err
		}
		// legacy_secret_key is permitted only under the source section (RCP-20260825-B2-LEGACY-SECRET-KEY).
		sectionFields := map[string]bool{
			"host": true, "port": true, "database": true, "username": true,
			"password": true, "tls": true,
		}
		if section == "source" {
			sectionFields["legacy_secret_key"] = true
		}
		for k := range fields {
			if !sectionFields[k] {
				return fmt.Errorf("unknown field %q in %s", k, section)
			}
		}
		for _, required := range []string{"host", "port", "database", "username", "password", "tls"} {
			if _, ok := fields[required]; !ok {
				return fmt.Errorf("missing required field %q in %s", required, section)
			}
		}
		var tlsFields map[string]json.RawMessage
		if err := json.Unmarshal(fields["tls"], &tlsFields); err != nil {
			return err
		}
		for k := range tlsFields {
			switch k {
			case "mode", "server_name", "ca_file", "certificate_file", "private_key_file":
			default:
				return fmt.Errorf("unknown field %q in %s.tls", k, section)
			}
		}
		if _, ok := tlsFields["mode"]; !ok {
			return fmt.Errorf("missing required field %q in %s.tls", "mode", section)
		}
		if _, ok := tlsFields["server_name"]; !ok {
			return fmt.Errorf("missing required field %q in %s.tls", "server_name", section)
		}
	}
	var encFields map[string]json.RawMessage
	if err := json.Unmarshal(probe["encryption"], &encFields); err != nil {
		return err
	}
	for k := range encFields {
		if k != "key_id" && k != "key" && k != "deployment_id" {
			return fmt.Errorf("unknown field %q in encryption", k)
		}
	}
	return nil
}
