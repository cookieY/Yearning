// connect.go Target database connection construction (sole path; B3 datasource.Factory also delegates here).
// TLS invariants: full certificate chain + hostname verification by default; verify_server=false is rejected outright;
// no InsecureSkipVerify=true path exists anywhere (legacy InitDSN defect does not recur).
// Address guard (SEC-0009/T05): DialFunc on both paths uniformly routes through addrguard
// (validates resolved addresses after DNS resolution and before connect — rejecting loopback/link-local/
// metadata/unspecified/broadcast/multicast/Unix sockets as a defense against DNS rebinding);
// all connection parameters are constructed explicitly.
package database

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/cookieY/YearningX/backend/internal/platform/addrguard"
)

// ApplicationName is the session identifier within the allowlist.
const ApplicationName = "yearning_v4"

// MySQLSessionSQLMode is the session sql_mode enforced by YearningX on target MySQL-family
// connections (database-adapter-contract.json session_initialization).
// Backslash escape and double-quoted string semantics must be deterministic: excludes
// NO_BACKSLASH_ESCAPES and ANSI_QUOTES; lexical fingerprinting parses under the same mode
// (sql-fingerprint.json). Other modes retain standard MySQL 8 strictness to avoid silently relaxing write semantics.
const MySQLSessionSQLMode = "ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION"

// BuildTLSConfig constructs a verifying TLS configuration.
// host is used as the default SNI/hostname verification target; overridden when material explicitly specifies server_name.
func BuildTLSConfig(host string, mat *TLSMaterial) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if mat == nil {
		return cfg, nil
	}
	if !mat.VerifyServer {
		return nil, errors.New("database: verify_server must stay enabled")
	}
	if mat.ServerName != "" {
		cfg.ServerName = mat.ServerName
	}
	if len(mat.CAPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(mat.CAPEM) {
			return nil, errors.New("database: tls ca unparsable")
		}
		cfg.RootCAs = pool
	}
	if len(mat.CertPEM) > 0 && len(mat.KeyPEM) > 0 {
		cert, err := tls.X509KeyPair(mat.CertPEM, mat.KeyPEM)
		if err != nil {
			return nil, errors.New("database: tls client material invalid")
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// mysqlTLSRegistration is a one-time registered TLS config name (deregistered on Close).
type mysqlTLSRegistration struct {
	name string
}

func (r *mysqlTLSRegistration) close() {
	if r != nil && r.name != "" {
		mysql.DeregisterTLSConfig(r.name)
		r.name = ""
	}
}

// BuildMySQLConfig constructs a go-sql-driver configuration.
// TLS policy: material configured -> enforced verifying TLS (full chain + hostname); unconfigured -> plaintext
// (consistent with non-TLS datasources). No TLS path skips verification.
func BuildMySQLConfig(ep Endpoint, cred Credential, mat *TLSMaterial, dialTimeout time.Duration) (*mysql.Config, *mysqlTLSRegistration, error) {
	if dialTimeout <= 0 {
		dialTimeout = ConnectTimeout
	}
	cfg := mysql.NewConfig()
	cfg.User = cred.Username
	cfg.Passwd = cred.Password
	cfg.Net = "tcp"
	if err := addrguard.CheckEndpoint(ep.Host, ep.Port); err != nil {
		return nil, nil, err
	}
	cfg.Addr = net.JoinHostPort(ep.Host, fmt.Sprintf("%d", ep.Port))
	if ep.DatabaseName != "" {
		cfg.DBName = ep.DatabaseName
	}
	cfg.Timeout = dialTimeout
	// SEC-0009/T05: validate actual resolved address before dialing (guard fails closed in Control callback).
	cfg.DialFunc = addrguard.DialFunc(dialTimeout)
	// Do not set ReadTimeout/WriteTimeout: cursors and long statements rely on context and statement_timeout control.
	// Charset uses the driver's SET NAMES (Config.Params["charset"] would be interpolated as
	// SET charset=..., which is not a valid MySQL variable); collation is left to the server default.
	if err := cfg.Apply(mysql.Charset("utf8mb4", "")); err != nil {
		return nil, nil, err
	}
	// sql_mode must be explicitly pinned: the driver executes SET sql_mode = <value> after establishing the connection.
	// Enclose the value in single quotes so commas are not parsed as SQL syntax separators.
	cfg.Params = map[string]string{"sql_mode": "'" + MySQLSessionSQLMode + "'"}

	if mat == nil {
		return cfg, nil, nil
	}
	tlsCfg, err := BuildTLSConfig(ep.Host, mat)
	if err != nil {
		return nil, nil, err
	}
	reg := &mysqlTLSRegistration{name: "yb4-" + uuid.NewString()[:8]}
	mysql.RegisterTLSConfig(reg.name, tlsCfg)
	cfg.TLSConfig = reg.name
	return cfg, reg, nil
}

// BuildPostgresConfig constructs a pgx connection configuration.
// TLS policy: material configured -> sslmode=verify-full (enforced verification); unconfigured -> engine default
// (prefer). All user data is assigned programmatically without going through a DSN string, eliminating injection surfaces.
// SEC-0057: pgx.ParseConfig reads process PG* environment variables (PGSSLMODE/PGHOST/PGDATABASE/
// PGOPTIONS/PGTZ/PGSSL* material/multi-host PGHOST fallbacks, etc.). Because ParseConfigOptions
// has no Getenv hook, all environment-Susceptible derived surfaces (TLSConfig, multi-host
// Fallbacks, RuntimeParams) are explicitly reset after parsing before programmatically assigning
// all connection parameters — process environment no longer affects connection targets or TLS verification policy.
func BuildPostgresConfig(ctx context.Context, ep Endpoint, cred Credential, mat *TLSMaterial, dialTimeout time.Duration) (*pgx.ConnConfig, error) {
	if dialTimeout <= 0 {
		dialTimeout = ConnectTimeout
	}
	if err := addrguard.CheckEndpoint(ep.Host, ep.Port); err != nil {
		return nil, err
	}
	base := ""
	if mat != nil {
		base = "sslmode=verify-full"
	}
	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		// Environment-injected TLS material paths (PGSSLROOTCERT, etc.) read files during parsing, which
		// can cause empty base parsing to fail. connString keys take precedence over environment variables:
		// explicitly override file-related and sslmode keys with quoted empty values and retry
		// (unquoted empty values in keyword=value format would be parsed as the next key name, so '' is required).
		// TLS is then completely reset afterward.
		retry := base
		if retry != "" {
			retry += " "
		}
		retry += "sslmode='' sslrootcert='' sslcert='' sslkey='' sslpassword=''"
		cfg, err = pgx.ParseConfig(retry)
		if err != nil {
			return nil, errors.New("database: postgresql config invalid")
		}
	}
	// SEC-0057 environment isolation: clear environment-taintable derived fields and construct everything explicitly.
	cfg.TLSConfig = nil
	cfg.Fallbacks = nil
	cfg.RuntimeParams = map[string]string{}
	// PGTARGETSESSIONATTRS is materialized by pgx v5 into a ValidateConnect hook (e.g.,
	// target_session_attrs=primary -> ValidateConnectTargetSessionAttrsPrimary,
	// altering multi-host connection selection) — clear it explicitly so the target endpoint is determined solely by inputs.
	cfg.ValidateConnect = nil
	cfg.Host = ep.Host
	cfg.Port = uint16(ep.Port)
	cfg.User = cred.Username
	cfg.Password = cred.Password
	cfg.Database = ep.DatabaseName
	cfg.ConnectTimeout = dialTimeout
	// SEC-0009/T05: validate actual resolved address before dialing (including Unix socket path rejection).
	cfg.DialFunc = addrguard.DialFunc(dialTimeout)
	if mat == nil {
		// After SEC-0057, the material-less path no longer uses pgx's prefer default (whose TLS config
		// varies with process PG* environment variables and is implemented with InsecureSkipVerify, violating
		// this package's "no verification-skipping path" invariant and static gate). Deterministic replacement:
		// try system-root-verified TLS first and fall back to plaintext on failure — upgrading to encrypted +
		// verified connections for servers with valid certificates while remaining consistent with plaintext datasources otherwise.
		cfg.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: ep.Host}
		cfg.Fallbacks = []*pgconn.FallbackConfig{{
			Host: ep.Host, Port: uint16(ep.Port), TLSConfig: nil,
		}}
		cfg.RuntimeParams["application_name"] = ApplicationName
		return cfg, nil
	}
	tlsCfg, err := BuildTLSConfig(ep.Host, mat)
	if err != nil {
		return nil, err
	}
	cfg.TLSConfig = tlsCfg
	cfg.RuntimeParams["application_name"] = ApplicationName
	return cfg, nil
}
