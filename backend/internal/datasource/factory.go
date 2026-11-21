// factory.go MySQL/PostgreSQL connection factory (B3 scope: connection, TLS, and version/instance identity probing).
// Starting from B4, dialing and TLS construction are delegated to internal/database (the sole connection construction path);
// this file handles only envelope decryption and datasource row assembly, ensuring credentials never enter logs or error messages.
package datasource

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

// ProbeTimeout is the total timeout for a single connection probe.
const ProbeTimeout = 8 * time.Second

// credentialPayload is the plaintext structure inside a credential envelope.
type credentialPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ErrCredentialMissing indicates no enabled credential exists for the requested Purpose.
var ErrCredentialMissing = errors.New("datasource: credential for purpose missing")

// Factory establishes target database connections using envelope-encrypted material.
type Factory struct {
	crypter *crypto.Crypter
}

// NewFactory constructs a connection factory.
func NewFactory(crypter *crypto.Crypter) (*Factory, error) {
	if crypter == nil {
		return nil, errors.New("datasource: nil crypter")
	}
	return &Factory{crypter: crypter}, nil
}

// OpenCredential decrypts the specified credential row.
func (f *Factory) OpenCredential(c *DatasourceCredentialModel) (credentialPayload, error) {
	if f == nil {
		return credentialPayload{}, errors.New("datasource: nil factory")
	}
	cred, err := OpenCredentialEnvelope(f.crypter, c)
	if err != nil {
		return credentialPayload{}, err
	}
	return credentialPayload{Username: cred.Username, Password: cred.Password}, nil
}

// openTLSMaterial decrypts TLS material and splits its PEM blocks; returns nil when unconfigured.
func (f *Factory) openTLSMaterial(m *DatasourceTLSMaterialModel) (*database.TLSMaterial, error) {
	if f == nil {
		return nil, errors.New("datasource: nil factory")
	}
	return OpenTLSEnvelope(f.crypter, m)
}

func splitTLSMaterial(verifyServer bool, serverName string, plain []byte) (*database.TLSMaterial, error) {
	return SplitTLSMaterial(verifyServer, serverName, plain)
}

// ProbeResult is the structured result of a connection probe (excluding credentials).
type ProbeResult struct {
	Engine         string
	ServerVersion  string
	InstanceMarker string
	DurationMS     int64
}

// Probe connects to the target database using the specified Purpose credential and reads its version and instance identity.
// Dialing, TLS construction, and identity queries all go through the internal/database Adapter.
// SEC-0009/T05: validate the host address before probing (checking each resolved IP when resolution succeeds;
// dial-layer addrguard defends against DNS rebinding).
func (f *Factory) Probe(ctx context.Context, ds *DatasourceModel, cred *DatasourceCredentialModel, tlsMat *DatasourceTLSMaterialModel) (*ProbeResult, error) {
	if cred == nil || !cred.Enabled {
		return nil, ErrCredentialMissing
	}
	payload, err := f.OpenCredential(cred)
	if err != nil {
		return nil, fmt.Errorf("datasource: credential unreadable")
	}
	mat, err := f.openTLSMaterial(tlsMat)
	if err != nil {
		return nil, fmt.Errorf("datasource: tls material unreadable")
	}
	switch ds.CompatibilityMode {
	case "mysql", "postgresql":
	default:
		return nil, ErrUnsupportedCapability
	}
	if err := addrguard.ValidateHost(ctx, ds.Host, nil); err != nil {
		return nil, fmt.Errorf("datasource: %w", err)
	}
	adapter := adapterForMode(ds)
	probeCtx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	start := time.Now()
	identity, err := adapter.ReadServerIdentity(probeCtx, database.Credential{Username: payload.Username, Password: payload.Password}, mat)
	if err != nil {
		return nil, err
	}
	return &ProbeResult{
		Engine:         identity.EngineFamily,
		ServerVersion:  identity.ServerVersion,
		InstanceMarker: identity.InstanceMarker,
		DurationMS:     time.Since(start).Milliseconds(),
	}, nil
}

// adapterForMode constructs an Adapter for the compatibility mode (with nil capability resolver; probing does not resolve capabilities).
func adapterForMode(ds *DatasourceModel) database.Adapter {
	ep := database.Endpoint{
		Host:             ds.Host,
		Port:             ds.Port,
		DatabaseName:     derefString(ds.DatabaseName),
		CompatibilityMod: ds.CompatibilityMode,
	}
	if ds.CompatibilityMode == "mysql" {
		return database.NewMySQLAdapter(ep, database.AdapterOptions{})
	}
	return database.NewPostgreSQLAdapter(ep, database.AdapterOptions{})
}

// IdentityFingerprint computes an unforgeable database identity fingerprint using the identifier_token HMAC.
// Input covers engine, deployment address, and server instance identity; rotating credentials does not change the fingerprint.
func (f *Factory) IdentityFingerprint(ds *DatasourceModel, instanceMarker string) (string, error) {
	material := fmt.Sprintf("engine=%s\nhost=%s\nport=%d\ninstance=%s",
		ds.Engine, ds.Host, ds.Port, instanceMarker)
	mac, err := f.crypter.HMAC("identifier_token", []byte(material))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", mac), nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// openTLSMaterialForTest constructs TLS material directly from plaintext bytes (for tests; bypasses envelope decryption).
func (f *Factory) openTLSMaterialForTest(m *DatasourceTLSMaterialModel) (*database.TLSMaterial, error) {
	// Reuse the splitting logic: replacing ciphertext parameters with decrypted plaintext is not possible on Open,
	// so call the shared splitTLSMaterial implementation directly here.
	return splitTLSMaterial(m.VerifyServer, derefString(m.ServerName), m.MaterialCiphertext)
}
