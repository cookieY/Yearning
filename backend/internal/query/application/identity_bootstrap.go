// identity_bootstrap.go implements on-demand datasource identity_fingerprint initialization for
// query access requests, grant renewals, and approval-free query sessions (BE-B13-QUERY-DATASOURCE-IDENTITY-FIX).
//
// Newly created datasources and datasources whose endpoint fields changed (SEC-0026) store NULL in
// datasources.identity_fingerprint until probed. Probing on demand before entering the serializable
// write transaction populates the canonical 64-hex HMAC fingerprint for reachable enabled targets
// without breaking SEC-0026 fail-closed drift checks on pre-existing Grants, Sessions, or Change Orders.
package application

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/datasource"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

// IdentityProbeFunc probes a target datasource's server identity during on-demand fingerprint bootstrap.
type IdentityProbeFunc func(
	ctx context.Context,
	ds *datasource.DatasourceModel,
	cred database.Credential,
	tlsMat *database.TLSMaterial,
) (*database.ServerIdentity, error)

// isValidIdentityFingerprint reports whether fp matches PostgreSQL's char(64) CHECK (^[0-9a-f]{64}$).
func isValidIdentityFingerprint(fp string) bool {
	if len(fp) != 64 {
		return false
	}
	for i := 0; i < 64; i++ {
		ch := fp[i]
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}

// computeDatasourceIdentityFingerprint computes the canonical identifier_token HMAC matching datasource.Factory.IdentityFingerprint.
func computeDatasourceIdentityFingerprint(crypter *crypto.Crypter, ds *datasource.DatasourceModel, instanceMarker string) (string, error) {
	if crypter == nil || ds == nil || instanceMarker == "" {
		return "", errors.New("query: incomplete datasource identity material")
	}
	material := fmt.Sprintf("engine=%s\nhost=%s\nport=%d\ninstance=%s",
		ds.Engine, ds.Host, ds.Port, instanceMarker)
	mac, err := crypter.HMAC("identifier_token", []byte(material))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(mac), nil
}

// ensureDatasourcesIdentity probes and persists datasources.identity_fingerprint for enabled, unprobed
// datasources belonging to a query_access flow granted to actorUserID. Failures are intentionally swallowed
// (never exposing raw target driver errors per SEC-0030) so the subsequent transactional validation returns
// a clean 3010 DATASOURCE_UNAVAILABLE business error.
func (s *Services) ensureDatasourcesIdentity(ctx context.Context, actorUserID, flowID string, datasourceIDs []string) {
	if s == nil || s.DB == nil || s.Crypter == nil {
		return
	}
	if _, err := uuid.Parse(actorUserID); err != nil {
		return
	}
	if _, err := uuid.Parse(flowID); err != nil {
		return
	}
	var validIDs []string
	if len(datasourceIDs) > 0 {
		for _, id := range datasourceIDs {
			if _, err := uuid.Parse(id); err == nil {
				validIDs = append(validIDs, id)
			}
		}
		if len(validIDs) == 0 {
			return
		}
	}

	var candidates []datasource.DatasourceModel
	q := s.DB.WithContext(ctx).Table("datasources d").
		Select("d.*").
		Joins("join flow_query_capabilities c on c.datasource_id = d.id").
		Joins("join flows f on f.id = c.flow_id").
		Where(`c.flow_id = ? and f.flow_type = 'query_access' and f.status = 'enabled'
			and d.enabled = true and (d.identity_fingerprint is null or d.identity_fingerprint = '')
			and exists(
				select 1 from permission_group_flow_grants g
				join permission_groups pg on pg.id = g.permission_group_id
				join permission_group_members m on m.permission_group_id = pg.id
				where g.flow_id = f.id and m.user_id = ? and pg.enabled
			)`, flowID, actorUserID)
	if len(validIDs) > 0 {
		q = q.Where("d.id in ?", validIDs)
	}
	if err := q.Find(&candidates).Error; err != nil || len(candidates) == 0 {
		return
	}

	for i := range candidates {
		s.bootstrapSingleDatasourceIdentity(ctx, &candidates[i])
	}
}

func (s *Services) bootstrapSingleDatasourceIdentity(ctx context.Context, ds *datasource.DatasourceModel) {
	if ds == nil || !ds.Enabled || ds.Host == "" || ds.Port <= 0 {
		return
	}
	if ds.CompatibilityMode != "mysql" && ds.CompatibilityMode != "postgresql" {
		return
	}
	credRow, err := s.loadBootstrapCredential(ctx, ds)
	if err != nil {
		return
	}
	plain, err := s.openQueryCredential(credRow)
	if err != nil {
		return
	}
	var tlsMat *database.TLSMaterial
	var tlsRow datasource.DatasourceTLSMaterialModel
	tlsErr := s.DB.WithContext(ctx).Where(
		"datasource_id = ? and purpose = ?", ds.ID, credRow.Purpose).First(&tlsRow).Error
	if tlsErr == nil {
		mat, merr := s.openQueryTLS(&tlsRow)
		if merr != nil {
			// Fail closed: present but unreadable TLS material must never downgrade to plaintext.
			return
		}
		tlsMat = mat
	} else if !errors.Is(tlsErr, gorm.ErrRecordNotFound) {
		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, datasource.ProbeTimeout)
	defer cancel()

	dbCred := database.Credential{Username: plain.Username, Password: plain.Password}
	var identity *database.ServerIdentity
	if s.IdentityProbe != nil {
		identity, err = s.IdentityProbe(probeCtx, ds, dbCred, tlsMat)
	} else {
		if err = addrguard.ValidateHost(probeCtx, ds.Host, nil); err != nil {
			return
		}
		ep := database.Endpoint{
			Host:             ds.Host,
			Port:             ds.Port,
			DatabaseName:     derefString(ds.DatabaseName),
			CompatibilityMod: ds.CompatibilityMode,
		}
		var adapter database.Adapter
		if ds.CompatibilityMode == "mysql" {
			adapter = database.NewMySQLAdapter(ep, database.AdapterOptions{})
		} else {
			adapter = database.NewPostgreSQLAdapter(ep, database.AdapterOptions{})
		}
		defer func() { _ = adapter.Close() }()
		identity, err = adapter.ReadServerIdentity(probeCtx, dbCred, tlsMat)
	}
	if err != nil || identity == nil || identity.InstanceMarker == "" {
		return
	}
	fp, err := computeDatasourceIdentityFingerprint(s.Crypter, ds, identity.InstanceMarker)
	if err != nil || !isValidIdentityFingerprint(fp) {
		return
	}
	updates := map[string]any{
		"identity_fingerprint": fp,
		"updated_at":           s.now(),
	}
	if target, _, capErr := datasource.ResolveCapabilities(ds.Engine, ds.CompatibilityMode, identity.ServerVersion); capErr == nil {
		metaBytes, merr := json.Marshal(map[string]string{
			"detected_version":  identity.ServerVersion,
			"capability_target": target,
		})
		if merr == nil {
			updates["metadata"] = metaBytes
		}
	}
	_ = s.DB.WithContext(ctx).Model(&datasource.DatasourceModel{}).
		Where("id = ? and enabled = true and (identity_fingerprint is null or identity_fingerprint = '')", ds.ID).
		Updates(updates).Error
}

func (s *Services) loadBootstrapCredential(ctx context.Context, ds *datasource.DatasourceModel) (*datasource.DatasourceCredentialModel, error) {
	purposes := []string{"query"}
	if ds.TestPurpose != "" && ds.TestPurpose != "query" {
		purposes = append(purposes, ds.TestPurpose)
	}
	if ds.TestPurpose != "review" {
		purposes = append(purposes, "review")
	}
	for _, purpose := range purposes {
		var cred datasource.DatasourceCredentialModel
		err := s.DB.WithContext(ctx).Where(
			"datasource_id = ? and purpose = ? and enabled = true", ds.ID, purpose).First(&cred).Error
		if err == nil {
			return &cred, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return nil, gorm.ErrRecordNotFound
}
