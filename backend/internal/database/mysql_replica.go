// mysql_replica.go provides read-only MySQL replication topology status probing for OSC preflight.
// Uses an unexported adapter extension interface so MetadataReader and Connector method sets stay frozen.
package database

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// ErrReplicaStatusPrivilege indicates the database credential lacks REPLICATION CLIENT / SUPER privilege.
var ErrReplicaStatusPrivilege = errors.New("database: insufficient privilege for replica status")

// MySQLReplicaStatus summarizes the target MySQL instance's replication role and upstream master.
type MySQLReplicaStatus struct {
	IsReplica          bool
	ReplicationRunning bool
	MasterHost         string
	MasterPort         int
}

type mysqlReplicaStatusReader interface {
	ReadMySQLReplicaStatus(ctx context.Context, pool *PurposePool) (*MySQLReplicaStatus, error)
}

// ReadMySQLReplicaStatus inspects SHOW REPLICA STATUS (falling back to SHOW SLAVE STATUS on older MySQL)
// over an addrguard-protected PurposePool. Never modifies MetadataReader/Connector contracts.
func ReadMySQLReplicaStatus(ctx context.Context, a Adapter, pool *PurposePool) (*MySQLReplicaStatus, error) {
	if pool == nil {
		return nil, errors.New("database: replica status requires non-nil pool")
	}
	if a != nil {
		if r, ok := a.(mysqlReplicaStatusReader); ok {
			return r.ReadMySQLReplicaStatus(ctx, pool)
		}
	}
	return readMySQLReplicaStatusWithPool(ctx, pool)
}

func (a *baseAdapter) ReadMySQLReplicaStatus(ctx context.Context, pool *PurposePool) (*MySQLReplicaStatus, error) {
	if a.engine() == "postgresql" {
		return nil, ErrUnsupportedTarget
	}
	return readMySQLReplicaStatusWithPool(ctx, pool)
}

func readMySQLReplicaStatusWithPool(ctx context.Context, pool *PurposePool) (*MySQLReplicaStatus, error) {
	var columns []string
	var rawRows [][]string
	err := pool.withReadQueryer(ctx, true, func(q queryer) error {
		rows, qerr := q.QueryContext(ctx, "SHOW REPLICA STATUS")
		if qerr != nil && isMySQLSyntaxFallbackError(qerr) {
			rows, qerr = q.QueryContext(ctx, "SHOW SLAVE STATUS")
		}
		if qerr != nil {
			if isMySQLPrivilegeError(qerr) {
				return ErrReplicaStatusPrivilege
			}
			return errors.New("database: replica status query failed")
		}
		defer func() { _ = rows.Close() }()
		var serr error
		columns, rawRows, serr = collectStringRows(rows)
		if serr != nil {
			return errors.New("database: replica status output unreadable")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return parseMySQLReplicaStatusRows(columns, rawRows), nil
}

func isMySQLPrivilegeError(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1227, 1142, 1045:
			return true
		}
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "access denied") || strings.Contains(lower, "replication client")
}

func isMySQLSyntaxFallbackError(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1064
	}
	return strings.Contains(strings.ToLower(err.Error()), "syntax")
}

func parseMySQLReplicaStatusRows(columns []string, rawRows [][]string) *MySQLReplicaStatus {
	if len(rawRows) == 0 || len(columns) == 0 {
		return &MySQLReplicaStatus{IsReplica: false}
	}
	colIndex := make(map[string]int, len(columns))
	for i, c := range columns {
		colIndex[strings.ToLower(strings.TrimSpace(c))] = i
	}
	val := func(row []string, names ...string) string {
		for _, n := range names {
			if idx, ok := colIndex[n]; ok && idx < len(row) {
				return strings.TrimSpace(row[idx])
			}
		}
		return ""
	}
	for _, row := range rawRows {
		masterHost := val(row, "source_host", "master_host")
		logFile := val(row, "source_log_file", "master_log_file")
		if masterHost == "" && logFile == "" {
			continue
		}
		ioRunning := val(row, "replica_io_running", "slave_io_running")
		sqlRunning := val(row, "replica_sql_running", "slave_sql_running")
		portStr := val(row, "source_port", "master_port")
		port := 3306
		if portStr != "" {
			if parsed, perr := strconv.Atoi(portStr); perr == nil && parsed >= 1 && parsed <= 65535 {
				port = parsed
			}
		}
		running := strings.EqualFold(ioRunning, "Yes") && strings.EqualFold(sqlRunning, "Yes") && masterHost != ""
		return &MySQLReplicaStatus{
			IsReplica:          true,
			ReplicationRunning: running,
			MasterHost:         masterHost,
			MasterPort:         port,
		}
	}
	return &MySQLReplicaStatus{IsReplica: false}
}
