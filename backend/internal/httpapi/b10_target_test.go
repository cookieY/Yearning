//go:build integration

// b10_target_test.go direct target DB helpers (for asserting transaction / partial-success semantics) and
// docker exec utilities (test build only).
package httpapi

import (
	"database/sql"
	"fmt"
	"os/exec"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// dockerExecOutput reads container environment variable (target DB password; never enters assertion output).
func dockerExecOutput(container string, args ...string) (string, error) {
	full := append([]string{"exec", container}, args...)
	out, err := exec.Command("docker", full...).Output()
	if err != nil {
		return "", err
	}
	return strings_TrimSpace(string(out)), nil
}

func strings_TrimSpace(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// openTargetPG opens a connection to the target PG database (pgx stdlib).
func openTargetPG(t *testing.T, host, port, dbname, user, password string) *sql.DB {
	return openTargetPGNamed(t, host, port, dbname, user, password, "")
}

// openTargetPGNamed opens a target PG connection with an explicit
// application_name so fixture-owned connections can be cleaned up precisely.
func openTargetPGNamed(t *testing.T, host, port, dbname, user, password, applicationName string) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, password, dbname)
	if applicationName != "" {
		dsn += " application_name=" + applicationName
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("target pg ping: %v", err)
	}
	return db
}

// openTargetMySQL opens a connection to the target MySQL database.
func openTargetMySQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("target mysql ping: %v", err)
	}
	return db
}

// queryTarget executes a query and returns a string matrix.
func queryTarget(t *testing.T, db *sql.DB, query string, args ...any) [][]string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out [][]string
	for rows.Next() {
		cols, cerr := rows.Columns()
		if cerr != nil {
			t.Fatal(cerr)
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if serr := rows.Scan(ptrs...); serr != nil {
			t.Fatal(serr)
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				row[i] = string(b)
			} else if v == nil {
				row[i] = "NULL"
			} else {
				row[i] = fmt.Sprint(v)
			}
		}
		out = append(out, row)
	}
	return out
}

// execTarget executes a mutation statement.
func execTarget(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

// b10TargetQueryMySQL queries target MySQL directly (exec_target schema).
func (fx *b10Fixture) b10TargetQueryMySQL(t *testing.T, query string, args ...any) [][]string {
	t.Helper()
	cfg := b10MySQLTarget()
	pw := b10MySQLPassword(t)
	db := openTargetMySQL(t, fmt.Sprintf("%s:%s@tcp(%s:%d)/exec_target", cfg.User, pw, cfg.Host, cfg.Port))
	defer func() { _ = db.Close() }()
	return queryTarget(t, db, query, args...)
}

// mustOpenTargetPG opens a target PG connection (caller is responsible for Close).
func mustOpenTargetPG(t *testing.T, fx *b10Fixture) *sql.DB {
	t.Helper()
	cfg := b10PGTarget()
	return openTargetPG(t, cfg.Host, fmt.Sprint(cfg.Port), "yearning_exec_target", cfg.User, fx.b10PGPassword(t))
}

// openTargetMySQLRoot opens a root connection to target MySQL (caller is responsible for Close).
func openTargetMySQLRoot(t *testing.T) *sql.DB {
	t.Helper()
	cfg := b10MySQLTarget()
	pw := b10MySQLPassword(t)
	return openTargetMySQL(t, fmt.Sprintf("%s:%s@tcp(%s:%d)/exec_target", cfg.User, pw, cfg.Host, cfg.Port))
}
