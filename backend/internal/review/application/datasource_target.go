package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/cookieY/YearningX/backend/internal/crypto"
	"github.com/cookieY/YearningX/backend/internal/database"
	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

const (
	stageDatasourceTargetKey     = "datasource_target"
	stageDatasourceTargetVersion = 1
	datasourceBootstrapTimeout   = 5 * time.Second
)

var (
	errFrozenDatasourceTarget = errors.New("review: frozen datasource target unavailable")
	// errInputsDrift indicates frozen inputs have drifted (converging run -> outdated).
	errInputsDrift = errors.New("review: frozen inputs drifted")
)

// frozenDatasourceTarget is the encrypted, immutable connection target for a
// review stage. Raw endpoint and database identifiers remain local to the
// review application and never enter Provider requests.
type frozenDatasourceTarget struct {
	SchemaVersion        int    `json:"schema_version"`
	DatasourceID         string `json:"datasource_id"`
	Engine               string `json:"engine"`
	CompatibilityMode    string `json:"compatibility_mode"`
	Host                 string `json:"host"`
	Port                 int    `json:"port"`
	DatabaseName         string `json:"database_name"`
	ConfiguredIdentityFP string `json:"configured_identity_fingerprint"`
	EngineFamily         string `json:"engine_family"`
	ServerVersion        string `json:"server_version"`
	InstanceMarker       string `json:"instance_marker"`
	LowerCaseTableNames  string `json:"lower_case_table_names"`
	SystemIdentifier     string `json:"system_identifier"`
	DatabaseOID          uint32 `json:"database_oid"`
	ConfiguredEndpointID string `json:"configured_endpoint_id"`
}

// FrozenDatasourceTarget is exported only so the HTTP assembly and its test
// can express the AgentDeps FactSourceFor seam. Only this package constructs
// and validates populated values from encrypted stage context.
type FrozenDatasourceTarget = frozenDatasourceTarget

type datasourceTargetRow struct {
	ID                string
	Engine            string
	CompatibilityMode string
	Host              string
	Port              int
	DatabaseName      *string
	IdentityFP        *string
	Enabled           bool
}

func (t frozenDatasourceTarget) validate() error {
	if t.SchemaVersion != stageDatasourceTargetVersion ||
		t.DatasourceID == "" ||
		t.Engine == "" ||
		(t.CompatibilityMode != "mysql" && t.CompatibilityMode != "postgresql") ||
		t.Host == "" ||
		t.Port <= 0 ||
		t.ConfiguredIdentityFP == "" ||
		t.EngineFamily == "" ||
		t.ServerVersion == "" ||
		t.InstanceMarker == "" ||
		t.ConfiguredEndpointID == "" {
		return errFrozenDatasourceTarget
	}
	if t.CompatibilityMode != t.EngineFamily {
		return errFrozenDatasourceTarget
	}
	if t.ConfiguredEndpointID != fmt.Sprintf("%s:%d", t.Host, t.Port) {
		return errFrozenDatasourceTarget
	}
	switch t.CompatibilityMode {
	case "postgresql":
		if t.DatabaseName == "" || t.DatabaseOID == 0 {
			return errFrozenDatasourceTarget
		}
	case "mysql":
		if t.DatabaseOID != 0 ||
			(t.LowerCaseTableNames != "0" &&
				t.LowerCaseTableNames != "1" &&
				t.LowerCaseTableNames != "2") {
			return errFrozenDatasourceTarget
		}
	}
	return nil
}

func (t frozenDatasourceTarget) matchesConfig(row datasourceTargetRow) bool {
	identityFP := ""
	if row.IdentityFP != nil {
		identityFP = *row.IdentityFP
	}
	databaseName := ""
	if row.DatabaseName != nil {
		databaseName = *row.DatabaseName
	}
	dbMatched := databaseName == t.DatabaseName
	if t.CompatibilityMode == "postgresql" && databaseName == "" {
		dbMatched = true
	}
	return row.Enabled &&
		row.ID == t.DatasourceID &&
		row.Engine == t.Engine &&
		row.CompatibilityMode == t.CompatibilityMode &&
		row.Host == t.Host &&
		row.Port == t.Port &&
		dbMatched &&
		identityFP == t.ConfiguredIdentityFP
}

func (t frozenDatasourceTarget) matchesIdentity(identity database.ServerIdentity) bool {
	return identity.EngineFamily == t.EngineFamily &&
		identity.ServerVersion == t.ServerVersion &&
		identity.InstanceMarker == t.InstanceMarker &&
		identity.LowerCaseTableNames == t.LowerCaseTableNames &&
		identity.SystemIdentifier == t.SystemIdentifier &&
		identity.DatabaseOID == t.DatabaseOID &&
		identity.DatabaseName == t.DatabaseName &&
		identity.ConfiguredEndpointID == t.ConfiguredEndpointID
}

func configuredIdentityFingerprint(
	crypter *crypto.Crypter, row datasourceTargetRow, instanceMarker string,
) (string, error) {
	material := fmt.Sprintf("engine=%s\nhost=%s\nport=%d\ninstance=%s",
		row.Engine, row.Host, row.Port, instanceMarker)
	mac, err := crypter.HMAC("identifier_token", []byte(material))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", mac), nil
}

// ensureFlowStagesIdentity bootstraps datasources.identity_fingerprint on demand for enabled stage datasources
// whose fingerprint is NULL when running against real target databases (DatasourceIdentityProbe == nil),
// gated by flow grant authorization so unauthorized callers cannot trigger probes.
func (s *Services) ensureFlowStagesIdentity(ctx context.Context, actorUserID, flowID string) {
	if s == nil || s.DB == nil || s.Crypter == nil || s.DatasourceIdentityProbe != nil {
		return
	}
	if _, err := uuid.Parse(actorUserID); err != nil {
		return
	}
	if _, err := uuid.Parse(flowID); err != nil {
		return
	}
	granted, err := s.flowGranted(ctx, s.DB, actorUserID, flowID)
	if err != nil || !granted {
		return
	}
	stages, err := s.loadFlowStages(ctx, s.DB, flowID)
	if err != nil {
		return
	}
	for i := range stages {
		st := stages[i]
		if !st.Enabled || (st.IdentityFP != nil && *st.IdentityFP != "") {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, datasourceBootstrapTimeout)
		dbName := ""
		if st.DatabaseName != nil {
			dbName = *st.DatabaseName
		}
		if err := addrguard.ValidateEndpointFor(probeCtx, addrguard.PurposeDatasource, st.Host, st.Port, dbName, nil); err != nil {
			cancel()
			continue
		}
		row := datasourceTargetRow{
			ID:                st.DatasourceID,
			Engine:            st.Engine,
			CompatibilityMode: st.CompatibilityMode,
			Host:              st.Host,
			Port:              st.Port,
			DatabaseName:      st.DatabaseName,
			Enabled:           true,
		}
		identity, err := probeDatasourceIdentity(probeCtx, s.DB, s.Crypter, row)
		cancel()
		if err != nil || identity.InstanceMarker == "" {
			continue
		}
		fp, err := configuredIdentityFingerprint(s.Crypter, row, identity.InstanceMarker)
		if err != nil || len(fp) != 64 {
			continue
		}
		_ = s.DB.WithContext(ctx).Exec(`
			update datasources
			set identity_fingerprint = ?, updated_at = now()
			where id = ? and enabled = true and (identity_fingerprint is null or identity_fingerprint = '')`,
			fp, st.DatasourceID).Error
	}
}

func (s *Services) probeFrozenDatasourceTarget(
	ctx context.Context, row datasourceTargetRow,
) (frozenDatasourceTarget, error) {
	if !row.Enabled || row.IdentityFP == nil || *row.IdentityFP == "" {
		return frozenDatasourceTarget{}, ErrFactSourceUnavailable
	}
	var (
		identity database.ServerIdentity
		err      error
	)
	if s.DatasourceIdentityProbe != nil {
		identity, err = s.DatasourceIdentityProbe(ctx, s.DB, s.Crypter, row)
	} else {
		identity, err = probeDatasourceIdentity(ctx, s.DB, s.Crypter, row)
	}
	if err != nil {
		return frozenDatasourceTarget{}, err
	}
	fingerprint := ""
	if s.DatasourceIdentityProbe == nil {
		fingerprint, err = configuredIdentityFingerprint(s.Crypter, row, identity.InstanceMarker)
		if err != nil {
			return frozenDatasourceTarget{}, err
		}
	}
	databaseName := ""
	if row.DatabaseName != nil {
		databaseName = *row.DatabaseName
	}
	// PostgreSQL datasources may omit default database_name in configuration while probing
	// current_database(); freeze the probed database name so identity matching and validate() stay consistent.
	if row.CompatibilityMode == "postgresql" && databaseName == "" {
		databaseName = identity.DatabaseName
	}
	target := frozenDatasourceTarget{
		SchemaVersion:        stageDatasourceTargetVersion,
		DatasourceID:         row.ID,
		Engine:               row.Engine,
		CompatibilityMode:    row.CompatibilityMode,
		Host:                 row.Host,
		Port:                 row.Port,
		DatabaseName:         databaseName,
		ConfiguredIdentityFP: *row.IdentityFP,
		EngineFamily:         identity.EngineFamily,
		ServerVersion:        identity.ServerVersion,
		InstanceMarker:       identity.InstanceMarker,
		LowerCaseTableNames:  identity.LowerCaseTableNames,
		SystemIdentifier:     identity.SystemIdentifier,
		DatabaseOID:          identity.DatabaseOID,
		ConfiguredEndpointID: identity.ConfiguredEndpointID,
	}
	// Application integration fixtures intentionally use unreachable
	// datasources and replace the live probe with deterministic facts. The
	// production probe must still match the configured fingerprint.
	if (s.DatasourceIdentityProbe == nil && fingerprint != target.ConfiguredIdentityFP) ||
		!target.matchesIdentity(identity) {
		return frozenDatasourceTarget{}, ErrFactSourceIdentityChanged
	}
	if err := target.validate(); err != nil {
		return frozenDatasourceTarget{}, err
	}
	return target, nil
}

func (s *Services) verifyFrozenDatasourceConfig(
	ctx context.Context, db *gorm.DB, target frozenDatasourceTarget,
) error {
	row, err := loadDatasourceTargetRow(ctx, db, target.DatasourceID)
	if err != nil {
		return err
	}
	if !target.matchesConfig(row) {
		return errInputsDrift
	}
	return nil
}

func loadDatasourceTargetRow(
	ctx context.Context, db *gorm.DB, datasourceID string,
) (datasourceTargetRow, error) {
	var row datasourceTargetRow
	if err := db.WithContext(ctx).Raw(`
		select id::text as id, engine, compatibility_mode, host, port,
		       database_name, identity_fingerprint as identity_fp, enabled
		from datasources where id = ?`, datasourceID).Scan(&row).Error; err != nil {
		return datasourceTargetRow{}, err
	}
	if row.ID == "" {
		return datasourceTargetRow{}, ErrNotFound
	}
	return row, nil
}

func withFrozenDatasourceTarget(
	value map[string]any, target frozenDatasourceTarget,
) (map[string]any, error) {
	if err := target.validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(target)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any)
	for key, item := range value {
		out[key] = item
	}
	out[stageDatasourceTargetKey] = json.RawMessage(raw)
	return out, nil
}

func frozenDatasourceTargetFromContext(
	c *crypto.Crypter, runID string, stagePosition int32, env crypto.Envelope,
) (frozenDatasourceTarget, error) {
	value, err := openStageReviewContext(c, runID, stagePosition, env)
	if err != nil {
		return frozenDatasourceTarget{}, err
	}
	raw, err := json.Marshal(value[stageDatasourceTargetKey])
	if err != nil {
		return frozenDatasourceTarget{}, errFrozenDatasourceTarget
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var target frozenDatasourceTarget
	if err := decoder.Decode(&target); err != nil || target.validate() != nil {
		return frozenDatasourceTarget{}, errFrozenDatasourceTarget
	}
	return target, nil
}
