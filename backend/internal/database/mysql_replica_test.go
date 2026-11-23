// mysql_replica_test.go verifies MySQL replication status parsing across MySQL 8.0 (Source_*/Replica_*),
// legacy MySQL 5.7 (Master_*/Slave_*), stopped/configured replicas with empty Master_Log_File, and standalone primaries.
package database

import "testing"

func TestParseMySQLReplicaStatusRows(t *testing.T) {
	t.Run("empty rows returns non-replica primary", func(t *testing.T) {
		st := parseMySQLReplicaStatusRows([]string{"Master_Host"}, nil)
		if st.IsReplica || st.ReplicationRunning || st.MasterHost != "" {
			t.Fatalf("got %+v; want non-replica", st)
		}
	})

	t.Run("mysql 8.0 source/replica columns healthy", func(t *testing.T) {
		cols := []string{"Source_Host", "Source_Port", "Source_Log_File", "Replica_IO_Running", "Replica_SQL_Running"}
		rows := [][]string{{"10.0.0.10", "3307", "mysql-bin.000042", "Yes", "Yes"}}
		st := parseMySQLReplicaStatusRows(cols, rows)
		if !st.IsReplica || !st.ReplicationRunning || st.MasterHost != "10.0.0.10" || st.MasterPort != 3307 {
			t.Fatalf("got %+v; want healthy replica -> 10.0.0.10:3307", st)
		}
	})

	t.Run("legacy master/slave columns with broken sql thread", func(t *testing.T) {
		cols := []string{"Master_Host", "Master_Port", "Master_Log_File", "Slave_IO_Running", "Slave_SQL_Running"}
		rows := [][]string{{"primary.db.internal", "3306", "binlog.000010", "Yes", "No"}}
		st := parseMySQLReplicaStatusRows(cols, rows)
		if !st.IsReplica || st.ReplicationRunning || st.MasterHost != "primary.db.internal" || st.MasterPort != 3306 {
			t.Fatalf("got %+v; want IsReplica=true, ReplicationRunning=false", st)
		}
	})

	t.Run("configured replica before initial sync has Master_Host with empty log file", func(t *testing.T) {
		cols := []string{"Master_Host", "Master_Port", "Master_Log_File", "Slave_IO_Running", "Slave_SQL_Running"}
		rows := [][]string{{"169.254.169.254", "3306", "", "No", "No"}}
		st := parseMySQLReplicaStatusRows(cols, rows)
		if !st.IsReplica || st.ReplicationRunning || st.MasterHost != "169.254.169.254" {
			t.Fatalf("got %+v; want stopped replica detected even when log file empty", st)
		}
	})
}
