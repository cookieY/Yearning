// service.go datasource application service: CRUD, per-Purpose credential replacement, connection test tasks,
// capability resolution, identity fingerprinting, and reference-protected deletion (02-datasource-management-prd).
package datasource

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/cookieY/YearningX/backend/internal/authorization/auditlog"
	"github.com/cookieY/YearningX/backend/internal/authorization/bizerr"
	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
	"github.com/cookieY/YearningX/backend/internal/platform/txutil"
	"github.com/cookieY/YearningX/backend/internal/tasks"
)

// View is the API response view; credential_status expresses only Presence and never contains secrets.
type View struct {
	ID                    string          `json:"id"`
	Name                  string          `json:"name"`
	Engine                string          `json:"engine"`
	CompatibilityMode     string          `json:"compatibility_mode"`
	DeploymentKind        string          `json:"deployment_kind"`
	Host                  string          `json:"host"`
	Port                  int             `json:"port"`
	DatabaseName          *string         `json:"database_name"`
	Enabled               bool            `json:"enabled"`
	TestPurpose           string          `json:"test_purpose"`
	CredentialStatus      map[string]bool `json:"credential_status"`
	TLSVerified           bool            `json:"tls_verified"`
	ReferencedByFlowCount int64           `json:"referenced_by_flow_count"`
	Version               int64           `json:"version"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

// CredentialInput maps to OpenAPI CredentialWrite (three explicit edit modes):
// password = replace; reuse_credential_purpose pointing to another purpose = reuse;
// equal to its own purpose = keep (copies the existing full credential, valid only when that row already exists).
type CredentialInput struct {
	Purpose  string `json:"purpose"`
	Username string `json:"username"`
	Password *struct {
		Value string `json:"value"`
	} `json:"password"`
	ReuseCredentialPurpose *string `json:"reuse_credential_purpose"`
}

// TLSInput maps to the object form of OpenAPI DatasourceTLSWrite (write-only SecretInput).
type TLSInput struct {
	CAPem *struct {
		Value string `json:"value"`
	} `json:"ca_pem"`
	ClientCertPEM *struct {
		Value string `json:"value"`
	} `json:"client_cert_pem"`
	ClientKeyPEM *struct {
		Value string `json:"value"`
	} `json:"client_key_pem"`
}

// TLSWriteField distinguishes the three states of the tls payload (RCP-20261016-DATASOURCE-TLS-KEEP):
// omitted key = keep existing material and tls_verified (Set=false); explicit null = remove all
// material and revert to plaintext (Set=true, Null=true); object = full replacement (Value != nil,
// replacement object must carry a non-null ca_pem). On create, omitted is equivalent to null (no existing material to keep).
type TLSWriteField struct {
	Set   bool
	Null  bool
	Value *TLSInput
}

// UnmarshalJSON distinguishes omitted (not called), null, and object states.
func (f *TLSWriteField) UnmarshalJSON(b []byte) error {
	f.Set = true
	if string(b) == "null" {
		f.Null = true
		f.Value = nil
		return nil
	}
	f.Null = false
	return json.Unmarshal(b, &f.Value)
}

func (t *TLSInput) ca() string {
	if t == nil || t.CAPem == nil {
		return ""
	}
	return t.CAPem.Value
}

func (t *TLSInput) clientCert() string {
	if t == nil || t.ClientCertPEM == nil {
		return ""
	}
	return t.ClientCertPEM.Value
}

func (t *TLSInput) clientKey() string {
	if t == nil || t.ClientKeyPEM == nil {
		return ""
	}
	return t.ClientKeyPEM.Value
}

// WriteInput maps to OpenAPI DatasourceWrite.
type WriteInput struct {
	Name              string  `json:"name"`
	Engine            string  `json:"engine"`
	CompatibilityMode string  `json:"compatibility_mode"`
	DeploymentKind    string  `json:"deployment_kind"`
	Host              string  `json:"host"`
	Port              int     `json:"port"`
	DatabaseName      *string `json:"database_name"`
	VersionConstraint string  `json:"version_constraint"`
	Enabled           bool    `json:"enabled"`
	// TestPurpose is the connection test purpose (§18.61). nil = default, falling back to
	// review under full-replacement semantics (DatasourceWrite declares default review; omitting resets it).
	TestPurpose *string           `json:"test_purpose"`
	Credentials []CredentialInput `json:"credentials"`
	TLS         TLSWriteField     `json:"tls"`
}

// testPurposeOrDefault resolves the connection test purpose for a write payload.
func testPurposeOrDefault(in *WriteInput) string {
	if in.TestPurpose == nil || *in.TestPurpose == "" {
		return "review"
	}
	return *in.TestPurpose
}

// ActorRef describes the actor initiating a management operation.
type ActorRef struct {
	UserID   string
	Username string
}

// Service is the datasource application service.
type Service struct {
	db       *gorm.DB
	factory  *Factory
	crypter  *crypto.Crypter
	audit    *auditlog.Recorder
	probeMu  sync.Map // datasource_id -> last probe time (connection test rate limiting)
	lastCaps sync.Map // datasource_id -> cachedCapabilities from the latest successful probe
}

type cachedCapabilities struct {
	purpose  string
	view     CapabilitiesView
	cachedAt time.Time
}

func cloneCapabilitiesView(in *CapabilitiesView) CapabilitiesView {
	caps := make(map[string]bool, len(in.Capabilities))
	for k, v := range in.Capabilities {
		caps[k] = v
	}
	return CapabilitiesView{
		DatasourceID:        in.DatasourceID,
		DetectedVersion:     in.DetectedVersion,
		IdentityFingerprint: in.IdentityFingerprint,
		Capabilities:        caps,
		CheckedAt:           in.CheckedAt,
	}
}

// NewService constructs a Service.
func NewService(db *gorm.DB, factory *Factory, crypter *crypto.Crypter, audit *auditlog.Recorder) (*Service, error) {
	if db == nil || factory == nil || crypter == nil || audit == nil {
		return nil, errors.New("datasource: nil dependency")
	}
	return &Service{db: db, factory: factory, crypter: crypter, audit: audit}, nil
}

// Recorder returns the attached auditlog.Recorder.
func (s *Service) Recorder() *auditlog.Recorder {
	if s == nil {
		return nil
	}
	return s.audit
}

// engineModeOK mirrors the datasource_engine_mode CHECK constraint.
func engineModeOK(engine, mode string) bool {
	switch {
	case engine == "mysql" || engine == "tidb" || engine == "oceanbase":
		return mode == "mysql"
	case engine == "postgresql":
		return mode == "postgresql"
	case engine == "polardb":
		return mode == "mysql" || mode == "postgresql"
	default:
		return false
	}
}

// validateWrite validates a write payload (lengths, enums, unique credential Purposes, mutually exclusive edit modes, and TLS material shape).
func validateWrite(in *WriteInput) error {
	if in.Name == "" || len(in.Name) > 128 || in.Host == "" || len(in.Host) > 253 {
		return bizerr.ErrValidationFailed
	}
	if in.Port < 1 || in.Port > 65535 || in.VersionConstraint == "" || len(in.VersionConstraint) > 64 {
		return bizerr.ErrValidationFailed
	}
	if in.DeploymentKind != "native" && in.DeploymentKind != "cloud" {
		return bizerr.ErrValidationFailed
	}
	if !engineModeOK(in.Engine, in.CompatibilityMode) {
		return bizerr.ErrValidationFailed
	}
	if in.TestPurpose != nil && *in.TestPurpose != "review" &&
		*in.TestPurpose != "query" && *in.TestPurpose != "execution" {
		return bizerr.ErrValidationFailed
	}
	if len(in.Credentials) < 1 || len(in.Credentials) > 3 {
		return bizerr.ErrValidationFailed
	}
	seen := map[string]bool{}
	for _, c := range in.Credentials {
		if c.Purpose != "review" && c.Purpose != "query" && c.Purpose != "execution" {
			return bizerr.ErrValidationFailed
		}
		if seen[c.Purpose] {
			return bizerr.ErrValidationFailed
		}
		seen[c.Purpose] = true
		hasPassword := c.Password != nil && c.Password.Value != ""
		hasReuse := c.ReuseCredentialPurpose != nil && *c.ReuseCredentialPurpose != ""
		if hasPassword && hasReuse {
			// Providing both password and reuse_credential_purpose is an ambiguous mode error.
			return bizerr.ErrValidationFailed
		}
		if !hasPassword && !hasReuse {
			// Caller must explicitly choose one of replace, reuse, or keep.
			return bizerr.ErrValidationFailed
		}
		if hasReuse && *c.ReuseCredentialPurpose != "review" &&
			*c.ReuseCredentialPurpose != "query" && *c.ReuseCredentialPurpose != "execution" {
			return bizerr.ErrValidationFailed
		}
		if hasPassword && c.Username == "" {
			// replace: new SecretInput must be accompanied by a username.
			return bizerr.ErrValidationFailed
		}
		if hasReuse && *c.ReuseCredentialPurpose != c.Purpose && c.Username == "" {
			// cross-purpose reuse: the new purpose row carries its own username.
			return bizerr.ErrValidationFailed
		}
		if hasReuse && *c.ReuseCredentialPurpose == c.Purpose && c.Username != "" {
			// keep: copies the existing complete credential (including username), so the request must not supply a username.
			return bizerr.ErrValidationFailed
		}
	}
	return validateTLS(in.TLS.Value)
}

// validateTLS validates the TLS replacement object shape (RCP-20261016): ca_pem is required (preventing
// all-null material objects from persisting empty bundle rows, as RootCAs are the foundation of verifying TLS),
// client cert and private key must appear as a pair, and each piece must be a parseable PEM block. The semantic
// invariant is enforced by connect.go: presence of material mandates verifying TLS with no skip-verification path.
func validateTLS(t *TLSInput) error {
	if t == nil {
		return nil
	}
	ca, cert, key := t.ca(), t.clientCert(), t.clientKey()
	if ca == "" {
		return bizerr.ErrValidationFailed
	}
	if (cert == "") != (key == "") {
		return bizerr.ErrValidationFailed
	}
	// Validate block types per field: block type must match the field (CA/client cert fields accept only
	// CERTIFICATE blocks, private key field accepts only PRIVATE KEY blocks), preventing syntactically valid
	// but semantically misplaced material (such as a certificate in the key field) from silently entering the splitter.
	for _, material := range []struct {
		value    string
		wantCert bool
	}{
		{ca, true}, {cert, true}, {key, false},
	} {
		if material.value == "" {
			continue
		}
		rest := []byte(material.value)
		blocks := 0
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			blocks++
			isCert := block.Type == "CERTIFICATE"
			if isCert != material.wantCert {
				return bizerr.ErrValidationFailed
			}
		}
		if blocks == 0 {
			return bizerr.ErrValidationFailed
		}
	}
	return nil
}

// sealCredential encrypts a single Purpose credential with AAD bound to the row ID about to be written.
func (s *Service) sealCredential(datasourceID, purpose, username, password string) (*DatasourceCredentialModel, error) {
	plain, err := json.Marshal(credentialPayload{Username: username, Password: password})
	if err != nil {
		return nil, err
	}
	rowID := newCredentialID()
	env, err := s.crypter.Seal(crypto.PurposePayload, plain, crypto.AAD{
		DeploymentID: s.crypter.DeploymentID(),
		Table:        "datasource_credentials",
		PrimaryKey:   rowID,
		Column:       "credential",
		Purpose:      "datasource_credential",
	})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &DatasourceCredentialModel{
		ID:                   rowID,
		DatasourceID:         datasourceID,
		Purpose:              purpose,
		CredentialCiphertext: env.Ciphertext,
		CredentialNonce:      env.Nonce,
		CredentialKeyID:      env.KeyID,
		CredentialAlgorithm:  env.Algorithm,
		Enabled:              true,
		CreatedAt:            now,
		UpdatedAt:            now,
	}, nil
}

// replaceCredentials replaces all enabled credentials: old rows are marked enabled=false to preserve history.
// Three explicit modes: replace (new SecretInput); reuse (cross-purpose reference to an old row's password +
// new purpose's username); keep (self-reference — copies the complete credential including username from the
// old row of the same purpose; valid only when the old row exists, never on Create).
func (s *Service) replaceCredentials(ctx context.Context, tx *gorm.DB, dsID string, inputs []CredentialInput) error {
	// Load currently enabled credentials (source for keep and reuse).
	var existing []DatasourceCredentialModel
	if err := tx.WithContext(ctx).
		Where("datasource_id = ? and enabled = true", dsID).Find(&existing).Error; err != nil {
		return err
	}
	existingByPurpose := map[string]*DatasourceCredentialModel{}
	for i := range existing {
		existingByPurpose[existing[i].Purpose] = &existing[i]
	}
	if err := tx.WithContext(ctx).Model(&DatasourceCredentialModel{}).
		Where("datasource_id = ? and enabled = true", dsID).
		Update("enabled", false).Error; err != nil {
		return err
	}
	for _, in := range inputs {
		var username, password string
		hasPassword := in.Password != nil && in.Password.Value != ""
		switch {
		case hasPassword:
			username, password = in.Username, in.Password.Value
		case in.ReuseCredentialPurpose != nil && *in.ReuseCredentialPurpose == in.Purpose:
			src := existingByPurpose[in.Purpose]
			if src == nil {
				return bizerr.ErrValidationFailed
			}
			payload, err := s.factory.OpenCredential(src)
			if err != nil {
				return fmt.Errorf("datasource: keep source credential unreadable")
			}
			username, password = payload.Username, payload.Password
		default:
			src := existingByPurpose[*in.ReuseCredentialPurpose]
			if src == nil {
				return bizerr.ErrValidationFailed
			}
			payload, err := s.factory.OpenCredential(src)
			if err != nil {
				return fmt.Errorf("datasource: reuse source credential unreadable")
			}
			username, password = in.Username, payload.Password
		}
		row, err := s.sealCredential(dsID, in.Purpose, username, password)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(row).Error; err != nil {
			if isUniqueErr(err) {
				return bizerr.ErrAlreadyExists
			}
			return err
		}
	}
	return nil
}

// sealTLSMaterial encrypts concatenated PEM material; AAD matches the readers (factory/execution/query)
// field for field. VerifyServer is always true: presence of material mandates verifying TLS.
func (s *Service) sealTLSMaterial(datasourceID, purpose string, bundle []byte) (*DatasourceTLSMaterialModel, error) {
	rowID := newCredentialID()
	env, err := s.crypter.Seal(crypto.PurposePayload, bundle, crypto.AAD{
		DeploymentID: s.crypter.DeploymentID(),
		Table:        "datasource_tls_materials",
		PrimaryKey:   rowID,
		Column:       "material",
		Purpose:      "datasource_tls",
	})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &DatasourceTLSMaterialModel{
		ID: rowID, DatasourceID: datasourceID, Purpose: purpose,
		VerifyServer:       true,
		MaterialCiphertext: env.Ciphertext, MaterialNonce: env.Nonce,
		MaterialKeyID: &env.KeyID, MaterialAlgorithm: &env.Algorithm,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// openTLSBundleRaw decrypts an existing TLS material row into the raw concatenated bundle (used for
// re-sealing in keep mode without round-tripping material through the client). AAD matches sealTLSMaterial
// and readers field for field. Malformed rows (empty ciphertext/key_id) fail closed as corrupt rows and never
// participate in re-sealing (matching the nil guard in sibling readers, but rejecting writes rather than skipping).
func (s *Service) openTLSBundleRaw(m *DatasourceTLSMaterialModel) ([]byte, error) {
	if m == nil || m.MaterialCiphertext == nil || m.MaterialKeyID == nil {
		return nil, fmt.Errorf("datasource: tls material malformed")
	}
	plain, err := s.crypter.Open(crypto.PurposePayload, crypto.Envelope{
		Ciphertext: m.MaterialCiphertext,
		Nonce:      m.MaterialNonce,
		KeyID:      *m.MaterialKeyID,
		Algorithm:  derefString(m.MaterialAlgorithm),
	}, crypto.AAD{
		DeploymentID: s.crypter.DeploymentID(),
		Table:        "datasource_tls_materials", PrimaryKey: m.ID,
		Column: "material", Purpose: "datasource_tls",
	})
	return plain, err
}

// keepTLSMaterials implements the 'keep' state (RCP-20261016 tri-state + R1 High-1 fix):
// existing material content stays unchanged, but rows are re-sealed across the new credential purpose set —
// newly added purposes inherit the material and removed purposes have their rows deleted. Because material
// is sealed per (datasource_id, purpose) while replaceCredentials replaces the purpose set wholesale, leaving
// TLS rows untouched when tls is omitted while adding a new purpose would leave the new purpose without a TLS
// row — silently downgrading connections to plaintext while tls_verified stays true; re-sealing prevents this
// downgrade. No-op when no material exists; fails closed if existing rows cannot be decrypted (consistent with
// runtime read behavior: decryption failure = datasource unavailable, never falling back to plaintext).
func (s *Service) keepTLSMaterials(ctx context.Context, tx *gorm.DB, dsID string, inputs []CredentialInput) error {
	var rows []DatasourceTLSMaterialModel
	if err := tx.WithContext(ctx).Where("datasource_id = ?", dsID).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	bundle, err := s.openTLSBundleRaw(&rows[0])
	if err != nil {
		return fmt.Errorf("datasource: tls material unreadable")
	}
	if err := tx.WithContext(ctx).
		Where("datasource_id = ?", dsID).Delete(&DatasourceTLSMaterialModel{}).Error; err != nil {
		return err
	}
	for _, in := range inputs {
		row, err := s.sealTLSMaterial(dsID, in.Purpose, bundle)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}

// replaceTLSMaterials deletes existing TLS material and re-seals from the replacement object (handling
// replace/remove semantics; 'keep' is handled by keepTLSMaterials). Because connections read by
// (datasource_id, purpose), the same material is sealed into one row per credential purpose in the current write.
// Concatenation order is canonical: CA chain -> client certificate -> client private key (splitTLSMaterial
// treats the last certificate block as the client certificate when a private key is present, and puts the rest into RootCAs).
func (s *Service) replaceTLSMaterials(ctx context.Context, tx *gorm.DB, dsID string, inputs []CredentialInput, tls *TLSInput) error {
	if err := tx.WithContext(ctx).
		Where("datasource_id = ?", dsID).Delete(&DatasourceTLSMaterialModel{}).Error; err != nil {
		return err
	}
	if tls == nil {
		return nil
	}
	parts := make([]string, 0, 3)
	for _, part := range []string{tls.ca(), tls.clientCert(), tls.clientKey()} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	bundle := []byte(strings.Join(parts, "\n"))
	for _, in := range inputs {
		row, err := s.sealTLSMaterial(dsID, in.Purpose, bundle)
		if err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}

// validateHostAddress SEC-0009/T05 configuration-surface address guard: validates hostname shape
// (rejecting loopback/link-local/metadata/unspecified/broadcast/Unix sockets) and checks each resolved IP
// when DNS resolution succeeds (DNS failure does not reject — preserving create-before-test semantics while
// dial-layer addrguard remains the authoritative defense). Private network datasource targets are allowed.
func (s *Service) validateTargetEndpoint(ctx context.Context, host string, port int, dbName string) error {
	if err := addrguard.ValidateEndpointFor(ctx, addrguard.PurposeDatasource, host, port, dbName, nil); err != nil {
		return ErrEndpointForbidden{Err: err}
	}
	return nil
}

// Create creates a datasource (initial creation does not require reachability; connectivity is verified by the test task).
func (s *Service) Create(ctx context.Context, actor ActorRef, in *WriteInput, meta auditMeta) (*View, error) {
	if err := validateWrite(in); err != nil {
		return nil, err
	}
	if err := s.validateTargetEndpoint(ctx, in.Host, in.Port, derefString(in.DatabaseName)); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	row := DatasourceModel{
		ID: uuid.NewString(), Name: in.Name, Engine: in.Engine,
		CompatibilityMode: in.CompatibilityMode, DeploymentKind: in.DeploymentKind,
		Host: in.Host, Port: in.Port, DatabaseName: in.DatabaseName,
		VersionConstraint: in.VersionConstraint, Enabled: in.Enabled,
		TestPurpose: testPurposeOrDefault(in),
		Metadata:    []byte("{}"), Version: 1,
		CreatedByUserID: actor.UserID, CreatedAt: now, UpdatedAt: now,
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			if isUniqueErr(err) {
				return bizerr.ErrAlreadyExists
			}
			return err
		}
		if err := s.replaceCredentials(ctx, tx, row.ID, in.Credentials); err != nil {
			return err
		}
		// Create tri-state: omitted is equivalent to null (no existing material to keep) -> nil;
		// object -> seal across all purposes (validateWrite already enforced ca_pem).
		var tlsObj *TLSInput
		if in.TLS.Set && !in.TLS.Null {
			tlsObj = in.TLS.Value
		}
		return s.replaceTLSMaterials(ctx, tx, row.ID, in.Credentials, tlsObj)
	})
	if err != nil {
		return nil, err
	}
	s.auditChange(ctx, actor, "datasource.created", "create_datasource", row.ID, "succeeded", meta, map[string]any{"engine": row.Engine})
	return s.getView(ctx, &row)
}

// Replace performs a full replacement of a datasource definition (optimistic lock).
// Before disabling (enabled=false), verifies that no running execution tasks exist for this datasource.
// SEC-0026: changing endpoint-family fields (engine/compatibility_mode/host/port) clears
// identity_fingerprint and probe metadata — fingerprint comparisons for frozen approvals and Query Sessions
// will fail closed (identity_changed), forcing a fresh connection test to rebuild the identity fingerprint
// so approved orders and sessions never silently follow a new target.
func (s *Service) Replace(ctx context.Context, actor ActorRef, id string, expectVersion int64, in *WriteInput, meta auditMeta) (*View, error) {
	if err := validateWrite(in); err != nil {
		return nil, err
	}
	if err := s.validateTargetEndpoint(ctx, in.Host, in.Port, derefString(in.DatabaseName)); err != nil {
		return nil, err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row DatasourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", id).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bizerr.ErrNotFound
			}
			return err
		}
		if row.Version != expectVersion {
			return bizerr.ErrVersionConflict
		}
		if row.Enabled && !in.Enabled {
			if err := assertNoRunningTasks(ctx, tx, id); err != nil {
				return err
			}
		}
		// Reference consistency for engine-family changes (BUG-005 / review F3): multi-stage change_review
		// flows must bind datasources of the exact same engine and compatibility mode, otherwise runtime
		// preflight recomputes dialect fingerprints under each stage's engine, mismatching the single dialect
		// frozen at review time and permanently wedging stage 2 with 3006. Create/ReplaceFlow already fails
		// closed at flow configuration time, but mutating a datasource's engine afterward would silently turn
		// an existing single-engine flow into a mixed-engine flow; block it here: if any change_review flow
		// referencing this datasource has another stage that disagrees with the new engine/compatibility_mode,
		// reject the change (1001) and require adjusting or retiring the affected flow first.
		if row.Engine != in.Engine || row.CompatibilityMode != in.CompatibilityMode {
			conflict, err := engineChangeConflictsFlow(ctx, tx, id, in.Engine, in.CompatibilityMode)
			if err != nil {
				return err
			}
			if conflict {
				return bizerr.ErrValidationFailed
			}
		}
		updates := map[string]any{
			"name": in.Name, "engine": in.Engine,
			"compatibility_mode": in.CompatibilityMode, "deployment_kind": in.DeploymentKind,
			"host": in.Host, "port": in.Port, "database_name": in.DatabaseName,
			"version_constraint": in.VersionConstraint, "enabled": in.Enabled,
			"test_purpose": testPurposeOrDefault(in),
			"version":      gorm.Expr("version + 1"), "updated_at": time.Now().UTC(),
		}
		// Endpoint-family change -> clear frozen identity (SEC-0026: fingerprint material covers
		// engine/host/port, and compatibility_mode determines the wire protocol family so it is treated
		// as an endpoint change as well). Once cleared, TestConnection is the sole path to rebuild the fingerprint.
		if row.Engine != in.Engine || row.CompatibilityMode != in.CompatibilityMode ||
			row.Host != in.Host || row.Port != in.Port {
			updates["identity_fingerprint"] = nil
			updates["metadata"] = []byte("{}")
		}
		if err := tx.Model(&DatasourceModel{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			if isUniqueErr(err) {
				return bizerr.ErrAlreadyExists
			}
			return err
		}
		if err := s.replaceCredentials(ctx, tx, id, in.Credentials); err != nil {
			return err
		}
		// Replace tri-state dispatch (RCP-20261016): omitted = keep existing material and
		// tls_verified (content unchanged, re-sealed across the new purpose set, R1 High-1);
		// null = explicit removal; object = full replacement.
		switch {
		case !in.TLS.Set:
			return s.keepTLSMaterials(ctx, tx, id, in.Credentials)
		case in.TLS.Null:
			return s.replaceTLSMaterials(ctx, tx, id, in.Credentials, nil)
		default:
			return s.replaceTLSMaterials(ctx, tx, id, in.Credentials, in.TLS.Value)
		}
	})
	if err != nil {
		return nil, err
	}
	s.lastCaps.Delete(id)
	s.auditChange(ctx, actor, "datasource.updated", "replace_datasource", id, "succeeded", meta, nil)
	return s.Get(ctx, id)
}

// runningTaskStates matches execution_attempts active states.
var runningTaskStates = []string{"created", "preflight", "running", "cancelling", "result_unknown"}

func assertNoRunningTasks(ctx context.Context, tx *gorm.DB, datasourceID string) error {
	var count int64
	err := tx.WithContext(ctx).Table("execution_attempts").
		Joins("join order_stages on order_stages.id = execution_attempts.stage_id").
		Where("order_stages.datasource_id = ? and execution_attempts.state in ?", datasourceID, runningTaskStates).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count > 0 {
		return errDatasourceBusy{Count: count}
	}
	return nil
}

type errDatasourceBusy struct{ Count int64 }

func (e errDatasourceBusy) Error() string {
	return fmt.Sprintf("datasource: %d running tasks block disable", e.Count)
}

// engineChangeConflictsFlow reports whether changing the datasource to (engine, compatibilityMode)
// would create a mixed-engine flow in any multi-stage change_review flow that references it.
//
// The rule matches workflow.validateChangeFlowSingleEngine: all stages of a change_review flow must
// share the same engine and compatibility_mode. This function inspects only stages other than the
// datasource being modified — they must match the new values, otherwise the update creates a mixed-engine flow.
//
// Single-stage flows are unaffected (no cross-engine issue), so only flows with >= 2 stage positions are checked.
func engineChangeConflictsFlow(ctx context.Context, tx *gorm.DB, datasourceID, engine, compatibilityMode string) (bool, error) {
	var count int64
	err := tx.WithContext(ctx).Raw(`
		select count(*) from flow_stages fs
		join flows f on f.id = fs.flow_id
		join datasources d on d.id = fs.datasource_id
		where f.flow_type = 'change_review'
		  and fs.flow_id in (
		      select flow_id from flow_stages where datasource_id = ?
		  )
		  and fs.datasource_id <> ?
		  and (d.engine <> ? or d.compatibility_mode <> ?)
		  and (select count(*) from flow_stages x where x.flow_id = fs.flow_id) >= 2`,
		datasourceID, datasourceID, engine, compatibilityMode).Scan(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// Delete performs reference-protected deletion: datasources ever referenced by flow templates, snapshots,
// or query capabilities can only be disabled (returning 1107); physical deletion is restricted to zero references.
func (s *Service) Delete(ctx context.Context, actor ActorRef, id string, expectVersion int64, meta auditMeta) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row DatasourceModel
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", id).First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return bizerr.ErrNotFound
			}
			return err
		}
		if row.Version != expectVersion {
			return bizerr.ErrConcurrentModification
		}
		refs, err := countReferences(ctx, tx, id)
		if err != nil {
			return err
		}
		if refs > 0 {
			return bizerr.ErrReferenced
		}
		// Credentials, TLS materials, and SQL diagnostics data cascade on delete (logical-relations: delete_source).
		if err := tx.Where("datasource_id = ?", id).Delete(&DatasourceCredentialModel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("datasource_id = ?", id).Delete(&DatasourceTLSMaterialModel{}).Error; err != nil {
			return err
		}
		for _, diagStmt := range []string{
			"delete from sql_diagnostics_order_links where datasource_id = ?",
			"delete from sql_diagnostics_ai_reports where datasource_id = ?",
			"delete from sql_diagnostics_bucket_metrics where datasource_id = ?",
			"delete from sql_diagnostics_statements where datasource_id = ?",
			"delete from sql_diagnostics_snapshots where datasource_id = ?",
			"delete from diagnostics_instance_leases where datasource_id = ?",
			"delete from datasource_diagnostics_configs where datasource_id = ?",
		} {
			if err := tx.Exec(diagStmt, id).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("id = ?", id).Delete(&DatasourceModel{}).Error; err != nil {
			return err
		}
		return s.audit.Audit(ctx, tx, auditlog.AuditInput{
			EventType: "datasource.deleted", ActorKind: "user",
			ActorID: &actor.UserID, ActorUsername: &actor.Username,
			ResourceType: "datasource", ResourceID: &id,
			Action: "delete_datasource", Outcome: "succeeded",
			RequestID: strPtr(meta.RequestID),
		})
	})
	if err == nil {
		s.lastCaps.Delete(id)
	}
	return err
}

// referenceTables lists the business tables holding a datasource_id reference.
var referenceTables = []string{
	"flow_stages", "flow_query_capabilities", "stage_review_snapshots",
	"order_stages", "query_access_request_datasources", "query_grant_capabilities",
	"query_session_capabilities", "query_executions",
}

func countReferences(ctx context.Context, tx *gorm.DB, datasourceID string) (int64, error) {
	var total int64
	for _, table := range referenceTables {
		var count int64
		if err := tx.WithContext(ctx).Table(table).
			Where("datasource_id = ?", datasourceID).Count(&count).Error; err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

// Get and List return API views.
func (s *Service) Get(ctx context.Context, id string) (*View, error) {
	var row DatasourceModel
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, bizerr.ErrNotFound
		}
		return nil, err
	}
	return s.getView(ctx, &row)
}

// StoredTestPurpose returns the persisted connection test purpose of a datasource (§18.61; NOT NULL
// defaulting to review). The capabilities endpoint selects probe credentials using this value (§18.73).
func (s *Service) StoredTestPurpose(ctx context.Context, id string) (string, error) {
	var purpose string
	err := s.db.WithContext(ctx).Model(&DatasourceModel{}).
		Where("id = ?", id).Select("test_purpose").Scan(&purpose).Error
	if err != nil {
		return "", err
	}
	if purpose == "" {
		return "review", nil
	}
	return purpose, nil
}

// ListFilters holds per-column filters for the admin datasource list (RCP-20261105-ADMIN-LIST-FIELD-FILTERS).
// Zero values mean the filter was omitted; all active filters narrow results via AND and compose with q.
type ListFilters struct {
	Engine            string // mysql|postgresql|tidb|oceanbase|polardb
	CompatibilityMode string // mysql|postgresql
	CredentialMode    string // read_write = has enabled execution credential; read_only = none
	Referenced        *bool  // true = referenced by >= 1 flow stage; false = unreferenced
}

// credentialExecutionExists returns the EXISTS subquery fragment for read_write filtering:
// checks only existence of an enabled execution credential without ever reading secret material.
const credentialExecutionExists = `exists (select 1 from datasource_credentials dc
 where dc.datasource_id = datasources.id and dc.enabled = true and dc.purpose = 'execution')`

// flowStageReferenceExists returns the EXISTS subquery fragment for referenced filtering.
const flowStageReferenceExists = `exists (select 1 from flow_stages fs where fs.datasource_id = datasources.id)`

// List returns a paginated list of views (search = case-insensitive substring on name/host, §18.73 server-side q;
// filters apply per-column narrowing).
func (s *Service) List(ctx context.Context, limit int, after, search string, filters ListFilters) ([]View, *string, error) {
	if limit < 1 || limit > 200 {
		return nil, nil, bizerr.ErrValidationFailed
	}
	switch filters.Engine {
	case "", "mysql", "postgresql", "tidb", "oceanbase", "polardb":
	default:
		return nil, nil, bizerr.ErrValidationFailed
	}
	switch filters.CompatibilityMode {
	case "", "mysql", "postgresql":
	default:
		return nil, nil, bizerr.ErrValidationFailed
	}
	switch filters.CredentialMode {
	case "", "read_write", "read_only":
	default:
		return nil, nil, bizerr.ErrValidationFailed
	}
	q := s.db.WithContext(ctx).Model(&DatasourceModel{}).
		Order("created_at asc, id asc").Limit(limit + 1)
	if after != "" {
		var cursor DatasourceModel
		if err := s.db.WithContext(ctx).Select("created_at", "id").
			Where("id = ?", after).First(&cursor).Error; err != nil {
			return nil, nil, bizerr.ErrNotFound
		}
		q = q.Where("(created_at, id) > (?, ?)", cursor.CreatedAt, cursor.ID)
	}
	if filters.Engine != "" {
		q = q.Where("engine = ?", filters.Engine)
	}
	if filters.CompatibilityMode != "" {
		q = q.Where("compatibility_mode = ?", filters.CompatibilityMode)
	}
	switch filters.CredentialMode {
	case "read_write":
		q = q.Where(credentialExecutionExists)
	case "read_only":
		q = q.Where("not " + credentialExecutionExists)
	}
	if filters.Referenced != nil {
		if *filters.Referenced {
			q = q.Where(flowStageReferenceExists)
		} else {
			q = q.Where("not " + flowStageReferenceExists)
		}
	}
	if search != "" {
		needle := "%" + search + "%"
		q = q.Where("name ILIKE ? or host ILIKE ?", needle, needle)
	}
	var rows []DatasourceModel
	if err := q.Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	views := make([]View, 0, len(rows))
	for i := range rows {
		v, err := s.getView(ctx, &rows[i])
		if err != nil {
			return nil, nil, err
		}
		views = append(views, *v)
	}
	var next *string
	if hasMore && len(rows) > 0 {
		next = &rows[len(rows)-1].ID
	}
	return views, next, nil
}

func (s *Service) getView(ctx context.Context, row *DatasourceModel) (*View, error) {
	view := View{
		ID: row.ID, Name: row.Name, Engine: row.Engine,
		CompatibilityMode: row.CompatibilityMode, DeploymentKind: row.DeploymentKind,
		Host: row.Host, Port: row.Port, DatabaseName: row.DatabaseName,
		Enabled: row.Enabled, TestPurpose: row.TestPurpose, CredentialStatus: map[string]bool{},
		Version: row.Version, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	var creds []DatasourceCredentialModel
	if err := s.db.WithContext(ctx).
		Where("datasource_id = ? and enabled = true", row.ID).Find(&creds).Error; err != nil {
		return nil, err
	}
	for _, c := range creds {
		view.CredentialStatus[c.Purpose] = true
	}
	// tls_verified expresses only material presence (present => verifying TLS enforced) without exposing material.
	var tlsCount int64
	if err := s.db.WithContext(ctx).Model(&DatasourceTLSMaterialModel{}).
		Where("datasource_id = ?", row.ID).Count(&tlsCount).Error; err != nil {
		return nil, err
	}
	view.TLSVerified = tlsCount > 0
	var refCount int64
	if err := s.db.WithContext(ctx).Table("flow_stages").
		Where("datasource_id = ?", row.ID).Count(&refCount).Error; err != nil {
		return nil, err
	}
	view.ReferencedByFlowCount = refCount
	return &view, nil
}

// CapabilitiesView maps to OpenAPI DatasourceCapabilities.
type CapabilitiesView struct {
	DatasourceID        string          `json:"datasource_id"`
	DetectedVersion     string          `json:"detected_version"`
	IdentityFingerprint string          `json:"identity_fingerprint"`
	Capabilities        map[string]bool `json:"capabilities"`
	CheckedAt           time.Time       `json:"checked_at"`
}

// TestAndCapabilities executes a connection probe, resolves the capability matrix, and persists the identity fingerprint.
// Probe failures return a business error (3010) without updating the fingerprint.
func (s *Service) TestAndCapabilities(ctx context.Context, id, purpose string) (*CapabilitiesView, *AsyncTaskModel, error) {
	if purpose != "review" && purpose != "query" && purpose != "execution" {
		return nil, nil, bizerr.ErrValidationFailed
	}
	if last, ok := s.probeMu.Load(id); ok {
		if t, ok := last.(time.Time); ok && time.Since(t) < testInterval {
			return nil, nil, ErrTestRateLimited
		}
	}
	if err := tasks.CheckPendingCapacity(ctx, s.db); err != nil {
		return nil, nil, err
	}
	s.probeMu.Store(id, time.Now())

	task := AsyncTaskModel{
		ID: uuid.NewString(), Kind: "admin_connection_test", State: "queued",
		AggregateType: strPtr("datasource"), AggregateID: strPtr(id),
		Input:         []byte(`{"purpose":"` + purpose + `"}`),
		ProgressTotal: 1, ProgressUnit: "step", FenceToken: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(&task).Error; err != nil {
		return nil, nil, err
	}
	finished := false
	finish := func(state string, code *int, failure *string, resultRef *string) {
		finished = true
		now := time.Now().UTC()
		task.State = state
		task.BusinessErrCode = code
		task.FailureName = failure
		task.ResultRef = resultRef
		task.FinishedAt = &now
		task.UpdatedAt = now
		task.ProgressCompleted = 1
		_ = s.db.WithContext(context.WithoutCancel(ctx)).Model(&AsyncTaskModel{}).
			Where("id = ?", task.ID).
			Updates(map[string]any{
				"state": state, "business_err_code": code, "failure_name": failure,
				"result_ref": resultRef, "finished_at": now, "updated_at": now,
				"progress_completed": 1,
			}).Error
	}
	defer func() {
		if !finished {
			finish("failed", intPtr(3010), strPtr("internal_error"), nil)
		}
	}()

	var ds DatasourceModel
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&ds).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			finish("failed", intPtr(1002), strPtr("resource_not_found"), nil)
			return nil, &task, bizerr.ErrNotFound
		}
		return nil, &task, err
	}
	var cred DatasourceCredentialModel
	probePurposes := []string{purpose}
	if purpose == "review" {
		probePurposes = append(probePurposes, "query")
	} else if purpose == "query" {
		probePurposes = append(probePurposes, "review")
	}
	resolvedPurpose := ""
	var credErr error
	for _, candidate := range probePurposes {
		credErr = s.db.WithContext(ctx).
			Where("datasource_id = ? and purpose = ? and enabled = true", id, candidate).
			First(&cred).Error
		if credErr == nil {
			resolvedPurpose = candidate
			break
		}
		if !errors.Is(credErr, gorm.ErrRecordNotFound) {
			break
		}
	}
	if resolvedPurpose == "" {
		finish("failed", intPtr(3010), strPtr("credential_missing"), nil)
		// §18.76 supplement (owner ruling: datasource connection test surfaces diagnostic detail along the same path as Provider rejection reasons):
		// failure sentinel carries operator-readable Detail, which errmap attaches to meta.detail for inline display.
		return nil, &task, fmt.Errorf("%w: purpose %s", ErrCredentialMissing, purpose)
	}
	var tlsMat DatasourceTLSMaterialModel
	tlsPurposes := []string{resolvedPurpose}
	if resolvedPurpose != purpose {
		tlsPurposes = append(tlsPurposes, purpose)
	}
	for _, tlsPurpose := range tlsPurposes {
		if err := s.db.WithContext(ctx).
			Where("datasource_id = ? and purpose = ?", id, tlsPurpose).
			First(&tlsMat).Error; err == nil {
			break
		}
	}

	probe, probeErr := s.factory.Probe(ctx, &ds, &cred, &tlsMat)
	if probeErr != nil {
		finish("failed", intPtr(3010), strPtr("connection_failed"), nil)
		return nil, &task, fmt.Errorf("%w: %v", ErrConnectionFailed{}, probeErr)
	}
	fingerprint, err := s.factory.IdentityFingerprint(&ds, probe.InstanceMarker)
	if err != nil {
		finish("failed", intPtr(3010), strPtr("fingerprint_failed"), nil)
		return nil, &task, err
	}
	target, caps, err := ResolveCapabilities(ds.Engine, ds.CompatibilityMode, probe.ServerVersion)
	if err != nil {
		finish("failed", intPtr(3010), strPtr("capability_unresolved"), nil)
		return nil, &task, err
	}
	// Persist fingerprint and detected version (remains stable when database identity is unchanged).
	metadataBytes, err := json.Marshal(struct {
		DetectedVersion  string `json:"detected_version"`
		CapabilityTarget string `json:"capability_target"`
	}{
		DetectedVersion:  probe.ServerVersion,
		CapabilityTarget: target,
	})
	if err != nil {
		return nil, &task, err
	}
	if err := s.db.WithContext(ctx).Model(&DatasourceModel{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"identity_fingerprint": fingerprint,
			"metadata":             metadataBytes,
			"updated_at":           time.Now().UTC(),
		}).Error; err != nil {
		return nil, &task, err
	}
	resultRefBytes, err := json.Marshal(struct {
		Stages           []string `json:"stages"`
		DurationMS       int64    `json:"duration_ms"`
		DetectedVersion  string   `json:"detected_version"`
		CapabilityTarget string   `json:"capability_target"`
	}{
		Stages:           []string{"dial", "authenticate", "probe"},
		DurationMS:       probe.DurationMS,
		DetectedVersion:  probe.ServerVersion,
		CapabilityTarget: target,
	})
	if err != nil {
		return nil, &task, err
	}
	resultRef := string(resultRefBytes)
	finish("succeeded", nil, nil, strPtr(resultRef))
	view := CapabilitiesView{
		DatasourceID: id, DetectedVersion: probe.ServerVersion,
		IdentityFingerprint: fingerprint, Capabilities: caps,
		CheckedAt: time.Now().UTC(),
	}
	s.lastCaps.Store(id, cachedCapabilities{
		purpose:  purpose,
		view:     cloneCapabilitiesView(&view),
		cachedAt: time.Now(),
	})
	return &view, &task, nil
}

// Capabilities returns the capability matrix for a datasource. When a matching
// TestAndCapabilities probe succeeded within the rate-limit window, it serves
// that fresh snapshot directly so the immediate post-test GET /capabilities
// read never collides with the 5-second connection-test rate limit or creates
// a duplicate probe task.
func (s *Service) Capabilities(ctx context.Context, id, purpose string) (*CapabilitiesView, error) {
	if purpose != "review" && purpose != "query" && purpose != "execution" {
		return nil, bizerr.ErrValidationFailed
	}
	if raw, ok := s.lastCaps.Load(id); ok {
		if cached, ok := raw.(cachedCapabilities); ok && cached.purpose == purpose && time.Since(cached.cachedAt) < testInterval {
			v := cloneCapabilitiesView(&cached.view)
			return &v, nil
		}
	}
	caps, _, err := s.TestAndCapabilities(ctx, id, purpose)
	return caps, err
}

// TestInterval is the minimum interval between connection tests (429 rate-limit window).
const testInterval = 5 * time.Second

// ErrTestRateLimited indicates connection testing is rate-limited.
var ErrTestRateLimited = errors.New("datasource: connection test rate limited")

// ErrCapacityExhausted indicates the durable async_tasks queue has reached its pending limit.
var ErrCapacityExhausted = tasks.ErrQueueFull

// ErrConnectionFailed indicates a connection probe failure (mapped to 3010 DATASOURCE_UNAVAILABLE).
type ErrConnectionFailed struct{}

func (ErrConnectionFailed) Error() string { return "datasource: connection failed" }

// ErrEndpointForbidden indicates a datasource endpoint failed create/replace address guard checks
// (mapped to declared 1001 VALIDATION_FAILED with safe diagnostic detail and host field metadata).
type ErrEndpointForbidden struct {
	Err error
}

func (e ErrEndpointForbidden) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "datasource: target address forbidden"
}

func (e ErrEndpointForbidden) Unwrap() []error {
	return []error{bizerr.ErrValidationFailed, e.Err}
}

// auditMeta carries request context metadata (isomorphic to identity.RequestMeta to avoid circular imports).
type auditMeta struct {
	RequestID string
}

func (s *Service) auditChange(ctx context.Context, actor ActorRef, eventType, action, resourceID, outcome string, meta auditMeta, extra map[string]any) {
	_ = s.audit.Audit(ctx, nil, auditlog.AuditInput{
		EventType: eventType, ActorKind: "user", ActorID: &actor.UserID, ActorUsername: &actor.Username,
		ResourceType: "datasource", ResourceID: &resourceID, Action: action, Outcome: outcome,
		RequestID: strPtr(meta.RequestID), Metadata: extra,
	})
}

func newCredentialID() string { return uuid.NewString() }

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(i int) *int { return &i }

func isUniqueErr(err error) bool { return txutil.IsUniqueViolation(err) }
